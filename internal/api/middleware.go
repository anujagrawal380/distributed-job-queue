package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/anujagrawal380/distributed-job-queue/internal/auth"
)

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

const (
	contextKeyOwnerID contextKey = "owner_id"
	contextKeyKeyType contextKey = "key_type"
	contextKeyScopes  contextKey = "scopes"
	contextKeyAPIKey  contextKey = "api_key"
)

// AuthMiddleware validates API keys and injects authentication context
func AuthMiddleware(store auth.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract Authorization header
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, `{"error":"Missing Authorization header"}`, http.StatusUnauthorized)
				return
			}

			// Parse "Bearer <token>"
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || parts[0] != "Bearer" {
				http.Error(w, `{"error":"Invalid Authorization header format. Expected: Bearer <token>"}`, http.StatusUnauthorized)
				return
			}

			apiKey := parts[1]
			if apiKey == "" {
				http.Error(w, `{"error":"Empty API key"}`, http.StatusUnauthorized)
				return
			}

			// Validate API key with Redis
			ctx := r.Context()
			key, err := store.GetKey(ctx, apiKey)
			if err != nil {
				http.Error(w, `{"error":"Invalid API key"}`, http.StatusUnauthorized)
				return
			}

			// Check if revoked
			if key.IsRevoked() {
				http.Error(w, `{"error":"API key has been revoked"}`, http.StatusUnauthorized)
				return
			}

			// Update last used timestamp (async, don't block request)
			go func() {
				// Use background context since request context will be cancelled
				bgCtx := context.Background()
				store.UpdateLastUsed(bgCtx, apiKey)
			}()

			// Inject authentication context
			ctx = context.WithValue(ctx, contextKeyOwnerID, key.OwnerID)
			ctx = context.WithValue(ctx, contextKeyKeyType, key.Type)
			ctx = context.WithValue(ctx, contextKeyScopes, key.Scopes)
			ctx = context.WithValue(ctx, contextKeyAPIKey, key)

			// Continue to next handler with enriched context
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireScope middleware checks if the authenticated key has a specific scope
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract scopes from context (set by AuthMiddleware)
			scopes, ok := r.Context().Value(contextKeyScopes).([]string)
			if !ok {
				http.Error(w, `{"error":"Authentication context missing"}`, http.StatusInternalServerError)
				return
			}

			// Check if scope exists
			hasScope := false
			for _, s := range scopes {
				if s == scope {
					hasScope = true
					break
				}
			}

			if !hasScope {
				http.Error(w, `{"error":"Insufficient permissions"}`, http.StatusForbidden)
				return
			}

			// Scope check passed
			next.ServeHTTP(w, r)
		})
	}
}

// Helper functions to extract values from context

// GetOwnerID extracts the owner ID from request context
func GetOwnerID(ctx context.Context) (string, bool) {
	ownerID, ok := ctx.Value(contextKeyOwnerID).(string)
	return ownerID, ok
}

// GetKeyType extracts the key type from request context
func GetKeyType(ctx context.Context) (auth.KeyType, bool) {
	keyType, ok := ctx.Value(contextKeyKeyType).(auth.KeyType)
	return keyType, ok
}

// GetScopes extracts the scopes from request context
func GetScopes(ctx context.Context) ([]string, bool) {
	scopes, ok := ctx.Value(contextKeyScopes).([]string)
	return scopes, ok
}

// GetAPIKey extracts the full API key object from request context
func GetAPIKey(ctx context.Context) (*auth.APIKey, bool) {
	key, ok := ctx.Value(contextKeyAPIKey).(*auth.APIKey)
	return key, ok
}
