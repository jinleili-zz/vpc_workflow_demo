package operation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jinleili-zz/nsp-platform/logger"
)

const (
	HeaderSagaTransactionID        = "X-Saga-Transaction-Id"
	HeaderIdempotencyKey           = "X-Idempotency-Key"
	HeaderNorthboundIdempotencyKey = "Idempotency-Key"
	HeaderIdempotencyGenerated     = "X-Idempotency-Key-Generated"
	HeaderRootOperationID          = "X-Root-Operation-Id"
	HeaderParentOperationID        = "X-Parent-Operation-Id"
	HeaderResourceGeneration       = "X-Resource-Generation"
)

type RequestIdentity struct {
	SagaTransactionID  string `json:"saga_transaction_id"`
	IdempotencyKey     string `json:"idempotency_key"`
	RootOperationID    string `json:"root_operation_id,omitempty"`
	ParentOperationID  string `json:"parent_operation_id,omitempty"`
	ResourceGeneration int64  `json:"resource_generation,omitempty"`
}

type identityContextKey struct{}

func HTTPMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		var generation int64
		generationHeader := c.GetHeader(HeaderResourceGeneration)
		if generationHeader != "" {
			parsed, err := strconv.ParseInt(generationHeader, 10, 64)
			if err != nil || parsed <= 0 {
				c.AbortWithStatusJSON(400, gin.H{"code": ErrInvalidResourceGeneration.Error(), "message": "X-Resource-Generation must be a positive integer"})
				return
			}
			generation = parsed
		}
		identity := RequestIdentity{
			SagaTransactionID:  c.GetHeader(HeaderSagaTransactionID),
			IdempotencyKey:     c.GetHeader(HeaderIdempotencyKey),
			RootOperationID:    c.GetHeader(HeaderRootOperationID),
			ParentOperationID:  c.GetHeader(HeaderParentOperationID),
			ResourceGeneration: generation,
		}
		if identity.SagaTransactionID != "" || identity.IdempotencyKey != "" || identity.RootOperationID != "" || identity.ParentOperationID != "" || identity.ResourceGeneration != 0 {
			ctx := context.WithValue(c.Request.Context(), identityContextKey{}, identity)
			c.Request = c.Request.WithContext(ctx)
			logger.InfoContext(ctx, "收到Saga幂等请求",
				"saga_transaction_id", identity.SagaTransactionID,
				"idempotency_key_hash", KeyDigest(identity.IdempotencyKey),
			)
		}
		c.Next()
	}
}

// NorthboundHTTPMiddleware establishes the client request identity. During the
// compatibility period a missing key is generated and returned to the caller;
// enforce=true switches the endpoint to the strict contract.
func NorthboundHTTPMiddleware(enforce bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.GetHeader(HeaderNorthboundIdempotencyKey)
		if key == "" {
			key = c.GetHeader(HeaderIdempotencyKey)
		}
		if key == "" && enforce {
			c.AbortWithStatusJSON(400, gin.H{"code": ErrInvalidIdempotencyKey.Error(), "message": "Idempotency-Key is required"})
			return
		}
		if key == "" {
			key = uuid.NewString()
			c.Header(HeaderNorthboundIdempotencyKey, key)
			c.Header(HeaderIdempotencyGenerated, "true")
			c.Header("Warning", `299 NSP "Idempotency-Key was generated; retry protection requires clients to reuse it"`)
		}
		if err := validateIdempotencyKey(key); err != nil {
			c.AbortWithStatusJSON(400, gin.H{"code": ErrInvalidIdempotencyKey.Error(), "message": err.Error()})
			return
		}

		identity := RequestIdentity{IdempotencyKey: key}
		ctx := ContextWithIdentity(c.Request.Context(), identity)
		c.Request = c.Request.WithContext(ctx)
		logger.InfoContext(ctx, "收到Northbound幂等请求", "idempotency_key_hash", KeyDigest(key))
		c.Next()
	}
}

func IdentityFromContext(ctx context.Context) (RequestIdentity, bool) {
	identity, ok := ctx.Value(identityContextKey{}).(RequestIdentity)
	return identity, ok
}

func ContextWithIdentity(ctx context.Context, identity RequestIdentity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, identity)
}

func ContextWithIdentityFallback(ctx context.Context, fallback RequestIdentity) context.Context {
	identity, _ := IdentityFromContext(ctx)
	if identity.RootOperationID == "" {
		identity.RootOperationID = fallback.RootOperationID
	}
	if identity.ParentOperationID == "" {
		identity.ParentOperationID = fallback.ParentOperationID
	}
	if identity.ResourceGeneration == 0 {
		identity.ResourceGeneration = fallback.ResourceGeneration
	}
	if identity.IdempotencyKey == "" {
		identity.IdempotencyKey = fallback.IdempotencyKey
	}
	if identity.SagaTransactionID == "" {
		identity.SagaTransactionID = fallback.SagaTransactionID
	}
	return ContextWithIdentity(ctx, identity)
}

type HeaderSetter interface {
	Set(string, string)
}

func ApplyIdentityHeaders(headers HeaderSetter, identity RequestIdentity) {
	if headers == nil {
		return
	}
	if identity.SagaTransactionID != "" {
		headers.Set(HeaderSagaTransactionID, identity.SagaTransactionID)
	}
	if identity.IdempotencyKey != "" {
		headers.Set(HeaderIdempotencyKey, identity.IdempotencyKey)
	}
	if identity.RootOperationID != "" {
		headers.Set(HeaderRootOperationID, identity.RootOperationID)
	}
	if identity.ParentOperationID != "" {
		headers.Set(HeaderParentOperationID, identity.ParentOperationID)
	}
	if identity.ResourceGeneration > 0 {
		headers.Set(HeaderResourceGeneration, strconv.FormatInt(identity.ResourceGeneration, 10))
	}
}

func DeriveChildIdentity(ctx context.Context, routeScope, targetScope string, payload any) (RequestIdentity, error) {
	parent, _ := IdentityFromContext(ctx)
	rootOperationID := parent.RootOperationID
	if rootOperationID == "" {
		rootOperationID = parent.SagaTransactionID
	}
	if rootOperationID == "" {
		seed := parent.IdempotencyKey
		if seed == "" {
			hash, _, err := CanonicalHash(CanonicalHashVersion, targetScope, payload)
			if err != nil {
				return RequestIdentity{}, err
			}
			seed = hash
		}
		rootOperationID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("root:"+seed)).String()
	}
	if routeScope == "" || targetScope == "" {
		return RequestIdentity{}, fmt.Errorf("child route and target scope are required")
	}
	generation := parent.ResourceGeneration
	if generation == 0 {
		generation = 1
	}
	return RequestIdentity{
		SagaTransactionID:  parent.SagaTransactionID,
		IdempotencyKey:     uuid.NewSHA1(uuid.NameSpaceOID, []byte(rootOperationID+":"+routeScope+":"+targetScope)).String(),
		RootOperationID:    rootOperationID,
		ParentOperationID:  rootOperationID,
		ResourceGeneration: generation,
	}, nil
}

func KeyDigest(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
