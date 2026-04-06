// aksk.go - Core AK/SK signing and verification logic.
package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HTTP header names for AK/SK authentication.
const (
	// HeaderAuthorization contains the authentication information.
	// Format: "NSP-HMAC-SHA256 AK=<ak>, Signature=<signature>"
	HeaderAuthorization = "Authorization"

	// HeaderTimestamp contains the Unix timestamp (seconds) of the request.
	HeaderTimestamp = "X-NSP-Timestamp"

	// HeaderNonce contains a 16-byte random hex string for replay prevention.
	HeaderNonce = "X-NSP-Nonce"

	// HeaderSignedHeaders lists the headers included in the signature.
	// Format: lowercase header names, semicolon-separated, sorted alphabetically.
	HeaderSignedHeaders = "X-NSP-SignedHeaders"

	// AuthScheme is the authentication scheme prefix.
	AuthScheme = "NSP-HMAC-SHA256"

	// DefaultSignedHeaders is the default list of headers to include in the signature.
	DefaultSignedHeaders = "content-type;x-nsp-nonce;x-nsp-timestamp"
)

// Default configuration values.
const (
	// DefaultTimestampTolerance is the default tolerance for timestamp validation.
	DefaultTimestampTolerance = 5 * time.Minute

	// DefaultNonceTTL is the default time-to-live for nonce storage.
	DefaultNonceTTL = 15 * time.Minute

	// MaxRequestBodySize is the maximum request body size for signature computation.
	// Requests exceeding this size will return an error.
	MaxRequestBodySize = 10 * 1024 * 1024 // 10MB
)

// Sentinel errors for authentication failures.
var (
	// ErrMissingAuthHeader is returned when the Authorization header is missing.
	ErrMissingAuthHeader = errors.New("authorization header is missing")

	// ErrInvalidAuthFormat is returned when the Authorization header format is invalid.
	ErrInvalidAuthFormat = errors.New("invalid authorization header format")

	// ErrMissingTimestamp is returned when X-NSP-Timestamp header is missing or malformed.
	ErrMissingTimestamp = errors.New("timestamp header is missing or malformed")

	// ErrMissingNonce is returned when X-NSP-Nonce header is missing.
	ErrMissingNonce = errors.New("nonce header is missing")

	// ErrTimestampExpired is returned when the timestamp is outside the tolerance window.
	ErrTimestampExpired = errors.New("timestamp is expired or outside tolerance window")

	// ErrNonceReused is returned when a nonce has already been used.
	ErrNonceReused = errors.New("nonce has already been used")

	// ErrAKNotFound is returned when the AccessKey is not found or is disabled.
	ErrAKNotFound = errors.New("access key not found or disabled")

	// ErrSignatureMismatch is returned when the signature verification fails.
	ErrSignatureMismatch = errors.New("signature does not match")

	// ErrBodyTooLarge is returned when the request body exceeds the maximum allowed size.
	ErrBodyTooLarge = errors.New("request body exceeds maximum allowed size")
)

// Signer provides client-side request signing functionality.
type Signer struct {
	accessKey string
	secretKey string
}

// NewSigner creates a new Signer with the given AK/SK pair.
func NewSigner(ak, sk string) *Signer {
	return &Signer{
		accessKey: ak,
		secretKey: sk,
	}
}

// Sign signs an HTTP request by adding the necessary authentication headers.
// It performs the following steps:
// 1. Sets X-NSP-Timestamp with the current Unix timestamp
// 2. Sets X-NSP-Nonce with a 16-byte random hex string
// 3. Reads and hashes the request body, then restores it
// 4. Sets X-NSP-SignedHeaders with the list of signed headers
// 5. Constructs the StringToSign and computes the HMAC-SHA256 signature
// 6. Sets the Authorization header
func (s *Signer) Sign(req *http.Request) error {
	// Step 1: Set timestamp
	timestamp := time.Now().Unix()
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(timestamp, 10))

	// Step 2: Generate and set nonce (16 bytes = 32 hex chars)
	nonce, err := generateNonce(16)
	if err != nil {
		return fmt.Errorf("failed to generate nonce: %w", err)
	}
	req.Header.Set(HeaderNonce, nonce)

	// Step 3: Read and hash body
	bodyHash, err := hashRequestBody(req)
	if err != nil {
		return fmt.Errorf("failed to hash request body: %w", err)
	}

	// Step 4: Determine and set signed headers
	signedHeaders := DefaultSignedHeaders
	req.Header.Set(HeaderSignedHeaders, signedHeaders)

	// Step 5: Build StringToSign
	stringToSign := buildStringToSign(req, signedHeaders, bodyHash)

	// Step 6: Compute signature
	signature := computeHMACSHA256(s.secretKey, stringToSign)

	// Step 7: Set Authorization header
	authHeader := fmt.Sprintf("%s AK=%s, Signature=%s", AuthScheme, s.accessKey, signature)
	req.Header.Set(HeaderAuthorization, authHeader)

	return nil
}

// VerifierConfig holds configuration options for the Verifier.
type VerifierConfig struct {
	// TimestampTolerance is the maximum allowed time difference between
	// request timestamp and server time. Default is 5 minutes.
	TimestampTolerance time.Duration

	// NonceTTL is the duration for which nonces are stored to prevent replay.
	// Default is 15 minutes.
	NonceTTL time.Duration
}

// Verifier provides server-side request verification functionality.
type Verifier struct {
	store              CredentialStore
	nonces             NonceStore
	timestampTolerance time.Duration
	nonceTTL           time.Duration
}

// NewVerifier creates a new Verifier with the given stores and configuration.
// If cfg is nil, default values are used.
func NewVerifier(store CredentialStore, nonces NonceStore, cfg *VerifierConfig) *Verifier {
	v := &Verifier{
		store:              store,
		nonces:             nonces,
		timestampTolerance: DefaultTimestampTolerance,
		nonceTTL:           DefaultNonceTTL,
	}

	if cfg != nil {
		if cfg.TimestampTolerance > 0 {
			v.timestampTolerance = cfg.TimestampTolerance
		}
		if cfg.NonceTTL > 0 {
			v.nonceTTL = cfg.NonceTTL
		}
	}

	return v
}

// Verify verifies an HTTP request's authentication.
// It returns the associated credential on success, or an error on failure.
// The verification steps are:
// 1. Parse Authorization header and extract AK and signature
// 2. Verify X-NSP-Timestamp is within tolerance window
// 3. Verify X-NSP-Nonce has not been used
// 4. Look up credential by AK (must exist and be enabled)
// 5. Read and hash request body, then restore it
// 6. Reconstruct StringToSign and compute expected signature
// 7. Compare signatures using constant-time comparison
func (v *Verifier) Verify(req *http.Request) (*Credential, error) {
	ctx := req.Context()

	// Step 1: Parse Authorization header
	ak, clientSignature, err := parseAuthHeader(req.Header.Get(HeaderAuthorization))
	if err != nil {
		return nil, err
	}

	// Step 2: Verify timestamp
	timestamp, err := parseAndValidateTimestamp(req.Header.Get(HeaderTimestamp), v.timestampTolerance)
	if err != nil {
		return nil, err
	}
	_ = timestamp // timestamp is validated but not used further

	// Step 3: Verify nonce
	nonce := req.Header.Get(HeaderNonce)
	if nonce == "" {
		return nil, ErrMissingNonce
	}

	used, err := v.nonces.CheckAndStore(ctx, nonce, v.nonceTTL)
	if err != nil {
		return nil, fmt.Errorf("nonce check failed: %w", err)
	}
	if used {
		return nil, ErrNonceReused
	}

	// Step 4: Look up credential
	cred, err := v.store.GetByAK(ctx, ak)
	if err != nil {
		return nil, fmt.Errorf("credential lookup failed: %w", err)
	}
	if cred == nil || !cred.Enabled {
		return nil, ErrAKNotFound
	}

	// Step 5: Read and hash body
	bodyHash, err := hashRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("failed to hash request body: %w", err)
	}

	// Step 6: Build StringToSign and compute expected signature
	signedHeaders := req.Header.Get(HeaderSignedHeaders)
	if signedHeaders == "" {
		signedHeaders = DefaultSignedHeaders
	}

	stringToSign := buildStringToSign(req, signedHeaders, bodyHash)
	expectedSignature := computeHMACSHA256(cred.SecretKey, stringToSign)

	// Step 7: Compare signatures (constant-time to prevent timing attacks)
	if !hmac.Equal([]byte(clientSignature), []byte(expectedSignature)) {
		return nil, ErrSignatureMismatch
	}

	return cred, nil
}

// generateNonce generates a random hex string of the specified byte length.
func generateNonce(byteLen int) (string, error) {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// hashRequestBody reads the request body, computes its SHA256 hash,
// and restores the body for further reading.
// Returns ErrBodyTooLarge if the body exceeds MaxRequestBodySize.
func hashRequestBody(req *http.Request) (string, error) {
	var bodyBytes []byte
	if req.Body != nil {
		var err error
		// Limit read to prevent memory exhaustion from oversized requests
		limitedReader := io.LimitReader(req.Body, MaxRequestBodySize+1)
		bodyBytes, err = io.ReadAll(limitedReader)
		if err != nil {
			return "", err
		}
		// Check if body exceeded the limit
		if len(bodyBytes) > MaxRequestBodySize {
			return "", ErrBodyTooLarge
		}
		// Restore the body
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	hash := sha256.Sum256(bodyBytes)
	return hex.EncodeToString(hash[:]), nil
}

// buildStringToSign constructs the string to be signed according to the specification.
// Format:
//
//	Line 1: HTTP Method (uppercase)
//	Line 2: Canonical URI (path only, "/" if empty)
//	Line 3: Canonical Query String (sorted parameters)
//	Line 4: Canonical Headers (key:value\n format, in SignedHeaders order)
//	Line 5: SignedHeaders (semicolon-separated list)
//	Line 6: hex(SHA256(body))
func buildStringToSign(req *http.Request, signedHeaders, bodyHash string) string {
	var sb strings.Builder

	// Line 1: HTTP Method
	sb.WriteString(strings.ToUpper(req.Method))
	sb.WriteString("\n")

	// Line 2: Canonical URI
	uri := req.URL.Path
	if uri == "" {
		uri = "/"
	}
	sb.WriteString(uri)
	sb.WriteString("\n")

	// Line 3: Canonical Query String
	sb.WriteString(buildCanonicalQueryString(req.URL.RawQuery))
	sb.WriteString("\n")

	// Line 4: Canonical Headers
	sb.WriteString(buildCanonicalHeaders(req.Header, signedHeaders))

	// Line 5: SignedHeaders
	sb.WriteString(signedHeaders)
	sb.WriteString("\n")

	// Line 6: Body Hash
	sb.WriteString(bodyHash)

	return sb.String()
}

// buildCanonicalQueryString creates a sorted query string from the raw query.
// Parameters are sorted by name, then by value.
func buildCanonicalQueryString(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}

	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rawQuery
	}

	// Sort parameter names
	var keys []string
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var pairs []string
	for _, k := range keys {
		// Sort values for each parameter
		vals := values[k]
		sort.Strings(vals)
		for _, v := range vals {
			pairs = append(pairs, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}

	return strings.Join(pairs, "&")
}

// buildCanonicalHeaders creates the canonical headers string.
// Headers are output in the order specified by signedHeaders,
// with lowercase keys and trimmed values.
func buildCanonicalHeaders(headers http.Header, signedHeaders string) string {
	if signedHeaders == "" {
		return ""
	}

	var sb strings.Builder
	headerList := strings.Split(signedHeaders, ";")

	for _, h := range headerList {
		h = strings.TrimSpace(h)
		// Get the header value (case-insensitive lookup)
		value := headers.Get(h)
		sb.WriteString(strings.ToLower(h))
		sb.WriteString(":")
		sb.WriteString(strings.TrimSpace(value))
		sb.WriteString("\n")
	}

	return sb.String()
}

// computeHMACSHA256 computes the HMAC-SHA256 signature.
func computeHMACSHA256(key, data string) string {
	h := hmac.New(sha256.New, []byte(key))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// parseAuthHeader parses the Authorization header and extracts AK and signature.
// Expected format: "NSP-HMAC-SHA256 AK=<ak>, Signature=<signature>"
func parseAuthHeader(header string) (ak, signature string, err error) {
	if header == "" {
		return "", "", ErrMissingAuthHeader
	}

	// Check scheme prefix
	if !strings.HasPrefix(header, AuthScheme+" ") {
		return "", "", ErrInvalidAuthFormat
	}

	// Remove scheme prefix
	content := strings.TrimPrefix(header, AuthScheme+" ")

	// Parse key-value pairs
	parts := strings.Split(content, ", ")
	params := make(map[string]string)

	for _, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return "", "", ErrInvalidAuthFormat
		}
		params[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	}

	ak, akOK := params["AK"]
	signature, sigOK := params["Signature"]

	if !akOK || !sigOK || ak == "" || signature == "" {
		return "", "", ErrInvalidAuthFormat
	}

	return ak, signature, nil
}

// parseAndValidateTimestamp parses the timestamp header and validates it.
func parseAndValidateTimestamp(timestampStr string, tolerance time.Duration) (time.Time, error) {
	if timestampStr == "" {
		return time.Time{}, ErrMissingTimestamp
	}

	timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return time.Time{}, ErrMissingTimestamp
	}

	requestTime := time.Unix(timestamp, 0)
	now := time.Now()

	// Check if timestamp is within tolerance window
	diff := now.Sub(requestTime)
	if diff < 0 {
		diff = -diff
	}

	if diff > tolerance {
		return time.Time{}, ErrTimestampExpired
	}

	return requestTime, nil
}

// ErrorToHTTPStatus maps authentication errors to HTTP status codes.
// Returns:
//   - 400 Bad Request: ErrMissingAuthHeader, ErrInvalidAuthFormat, ErrMissingTimestamp, ErrMissingNonce
//   - 401 Unauthorized: ErrTimestampExpired, ErrNonceReused, ErrAKNotFound, ErrSignatureMismatch
//   - 500 Internal Server Error: all other errors
func ErrorToHTTPStatus(err error) int {
	switch {
	case errors.Is(err, ErrMissingAuthHeader),
		errors.Is(err, ErrInvalidAuthFormat),
		errors.Is(err, ErrMissingTimestamp),
		errors.Is(err, ErrMissingNonce):
		return http.StatusBadRequest

	case errors.Is(err, ErrTimestampExpired),
		errors.Is(err, ErrNonceReused),
		errors.Is(err, ErrAKNotFound),
		errors.Is(err, ErrSignatureMismatch):
		return http.StatusUnauthorized

	default:
		return http.StatusInternalServerError
	}
}
