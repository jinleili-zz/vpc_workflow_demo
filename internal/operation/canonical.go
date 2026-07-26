package operation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

const CanonicalHashVersion int16 = 1

var credentialFieldNames = map[string]struct{}{
	"accesskey":       {},
	"accesstoken":     {},
	"apikey":          {},
	"authorization":   {},
	"clientsecret":    {},
	"credential":      {},
	"credentials":     {},
	"password":        {},
	"passwd":          {},
	"privatekey":      {},
	"refreshtoken":    {},
	"secret":          {},
	"secretaccesskey": {},
	"secretkey":       {},
	"sessiontoken":    {},
	"token":           {},
}

func CanonicalHash(version int16, target string, payload any) (string, json.RawMessage, error) {
	if version != CanonicalHashVersion {
		return "", nil, fmt.Errorf("unsupported request hash version: %d", version)
	}

	normalized, err := normalizePayload(payload)
	if err != nil {
		return "", nil, err
	}
	redacted := redactCredentials(normalized)
	canonicalPayload, err := json.Marshal(redacted)
	if err != nil {
		return "", nil, fmt.Errorf("marshal canonical payload: %w", err)
	}
	hashInput, err := json.Marshal(struct {
		Target  string          `json:"target"`
		Payload json.RawMessage `json:"payload"`
	}{Target: target, Payload: canonicalPayload})
	if err != nil {
		return "", nil, fmt.Errorf("marshal hash input: %w", err)
	}
	sum := sha256.Sum256(hashInput)
	return hex.EncodeToString(sum[:]), canonicalPayload, nil
}

func normalizePayload(payload any) (any, error) {
	var raw []byte
	switch value := payload.(type) {
	case []byte:
		raw = value
	case json.RawMessage:
		raw = value
	default:
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal request payload: %w", err)
		}
		raw = encoded
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, fmt.Errorf("decode request payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode request payload: multiple JSON values")
		}
		return nil, fmt.Errorf("decode request payload: %w", err)
	}
	return normalizeNumbers(normalized)
}

func normalizeNumbers(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			normalized, err := normalizeNumbers(item)
			if err != nil {
				return nil, err
			}
			typed[key] = normalized
		}
		return typed, nil
	case []any:
		for i, item := range typed {
			normalized, err := normalizeNumbers(item)
			if err != nil {
				return nil, err
			}
			typed[i] = normalized
		}
		return typed, nil
	case json.Number:
		normalized, err := canonicalNumber(typed.String())
		if err != nil {
			return nil, fmt.Errorf("normalize JSON number %q: %w", typed, err)
		}
		return json.Number(normalized), nil
	default:
		return value, nil
	}
}

// canonicalNumber preserves decimal values exactly without converting through
// float64. It represents a non-zero value as an integer coefficient plus an
// optional base-10 exponent, so 1, 1.0, and 1e0 share one representation.
func canonicalNumber(value string) (string, error) {
	sign := ""
	unsigned := value
	if strings.HasPrefix(unsigned, "-") {
		sign = "-"
		unsigned = unsigned[1:]
	}

	exponent := int64(0)
	if index := strings.IndexAny(unsigned, "eE"); index >= 0 {
		parsed, err := strconv.ParseInt(unsigned[index+1:], 10, 64)
		if err != nil {
			return "", fmt.Errorf("invalid exponent: %w", err)
		}
		exponent = parsed
		unsigned = unsigned[:index]
	}

	fractionDigits := 0
	if index := strings.IndexByte(unsigned, '.'); index >= 0 {
		fractionDigits = len(unsigned) - index - 1
		unsigned = unsigned[:index] + unsigned[index+1:]
	}
	if exponent < math.MinInt64+int64(fractionDigits) {
		return "", fmt.Errorf("exponent underflow")
	}
	exponent -= int64(fractionDigits)
	unsigned = strings.TrimLeft(unsigned, "0")
	if unsigned == "" {
		return "0", nil
	}

	trailingZeros := len(unsigned) - len(strings.TrimRight(unsigned, "0"))
	if exponent > math.MaxInt64-int64(trailingZeros) {
		return "", fmt.Errorf("exponent overflow")
	}
	unsigned = strings.TrimRight(unsigned, "0")
	exponent += int64(trailingZeros)
	if exponent == 0 {
		return sign + unsigned, nil
	}
	return sign + unsigned + "e" + strconv.FormatInt(exponent, 10), nil
}

func redactCredentials(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, item := range typed {
			normalizedKey := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
			if _, sensitive := credentialFieldNames[normalizedKey]; sensitive {
				continue
			}
			redacted[key] = redactCredentials(item)
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for i, item := range typed {
			redacted[i] = redactCredentials(item)
		}
		return redacted
	default:
		return value
	}
}
