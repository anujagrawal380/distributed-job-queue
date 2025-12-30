package api

import (
	"net/http"

	"github.com/anujagrawal380/distributed-job-queue/internal/auth"
)

// RequireAdmin middleware ensures only admin keys can access the endpoint
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyType, ok := GetKeyType(r.Context())
		if !ok {
			http.Error(w, `{"error":"Authentication context missing"}`, http.StatusInternalServerError)
			return
		}

		if keyType != auth.KeyTypeAdmin {
			http.Error(w, `{"error":"Admin access required"}`, http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}
