package operation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/gin-gonic/gin"
	"github.com/jinleili-zz/nsp-platform/logger"
)

const (
	HeaderSagaTransactionID = "X-Saga-Transaction-Id"
	HeaderIdempotencyKey    = "X-Idempotency-Key"
)

type RequestIdentity struct {
	SagaTransactionID string `json:"saga_transaction_id"`
	IdempotencyKey    string `json:"idempotency_key"`
}

type identityContextKey struct{}

func HTTPMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		identity := RequestIdentity{
			SagaTransactionID: c.GetHeader(HeaderSagaTransactionID),
			IdempotencyKey:    c.GetHeader(HeaderIdempotencyKey),
		}
		if identity.SagaTransactionID != "" || identity.IdempotencyKey != "" {
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

func IdentityFromContext(ctx context.Context) (RequestIdentity, bool) {
	identity, ok := ctx.Value(identityContextKey{}).(RequestIdentity)
	return identity, ok
}

func KeyDigest(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
