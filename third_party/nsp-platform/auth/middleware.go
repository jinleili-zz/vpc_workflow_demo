// middleware.go - Gin middleware for AK/SK authentication.
package auth

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
)

// credentialContextKey is a private type for context keys to avoid collisions.
type credentialContextKey struct{}

// ginContextKey is the key used to store credential in gin.Context.
const ginContextKey = "nsp.auth.credential"

// MiddlewareOption holds configuration options for the AK/SK authentication middleware.
type MiddlewareOption struct {
	// Skipper is a function that returns true to skip authentication for the request.
	// If nil, all requests will be authenticated.
	Skipper func(c *gin.Context) bool

	// OnAuthFailed is a custom handler for authentication failures.
	// If nil, a default JSON response with error details will be sent.
	OnAuthFailed func(c *gin.Context, err error)
}

// AKSKAuthMiddleware creates a Gin middleware for AK/SK authentication.
// It uses the provided Verifier to validate requests and stores the
// authenticated credential in both gin.Context and request context.
func AKSKAuthMiddleware(verifier *Verifier, opt *MiddlewareOption) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Check if authentication should be skipped
		if opt != nil && opt.Skipper != nil && opt.Skipper(c) {
			c.Next()
			return
		}

		// Verify the request
		cred, err := verifier.Verify(c.Request)
		if err != nil {
			// Authentication failed
			if opt != nil && opt.OnAuthFailed != nil {
				opt.OnAuthFailed(c, err)
			} else {
				defaultAuthFailedHandler(c, err)
			}
			c.Abort()
			return
		}

		// Authentication succeeded
		// Store credential in gin.Context
		c.Set(ginContextKey, cred)

		// Store credential in standard context and update request
		ctx := ContextWithCredential(c.Request.Context(), cred)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

// defaultAuthFailedHandler is the default handler for authentication failures.
func defaultAuthFailedHandler(c *gin.Context, err error) {
	status := ErrorToHTTPStatus(err)
	c.JSON(status, gin.H{
		"code":    status,
		"message": err.Error(),
	})
}

// CredentialFromGin retrieves the authenticated credential from gin.Context.
// This is intended for use in Gin handler functions.
// Returns (nil, false) if no credential is found.
func CredentialFromGin(c *gin.Context) (*Credential, bool) {
	val, exists := c.Get(ginContextKey)
	if !exists {
		return nil, false
	}

	cred, ok := val.(*Credential)
	if !ok {
		return nil, false
	}

	return cred, true
}

// ContextWithCredential stores the credential in a standard context.
// This is useful for passing credentials to service/repository layers.
func ContextWithCredential(ctx context.Context, cred *Credential) context.Context {
	return context.WithValue(ctx, credentialContextKey{}, cred)
}

// CredentialFromContext retrieves the authenticated credential from a standard context.
// This is intended for use in service/repository layers.
// Returns (nil, false) if no credential is found.
func CredentialFromContext(ctx context.Context) (*Credential, bool) {
	val := ctx.Value(credentialContextKey{})
	if val == nil {
		return nil, false
	}

	cred, ok := val.(*Credential)
	if !ok {
		return nil, false
	}

	return cred, true
}

// HTTPStatusFromError returns the appropriate HTTP status code for the given error.
// This is an alias for ErrorToHTTPStatus for middleware convenience.
func HTTPStatusFromError(err error) int {
	return ErrorToHTTPStatus(err)
}

// NewSkipperByPath creates a Skipper function that skips authentication
// for requests matching any of the given paths.
func NewSkipperByPath(paths ...string) func(c *gin.Context) bool {
	pathSet := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		pathSet[p] = struct{}{}
	}

	return func(c *gin.Context) bool {
		_, skip := pathSet[c.Request.URL.Path]
		return skip
	}
}

// NewSkipperByPathPrefix creates a Skipper function that skips authentication
// for requests whose path starts with any of the given prefixes.
func NewSkipperByPathPrefix(prefixes ...string) func(c *gin.Context) bool {
	return func(c *gin.Context) bool {
		path := c.Request.URL.Path
		for _, prefix := range prefixes {
			if strings.HasPrefix(path, prefix) {
				return true
			}
		}
		return false
	}
}
