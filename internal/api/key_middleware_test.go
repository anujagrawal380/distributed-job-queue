package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anujagrawal380/distributed-job-queue/internal/auth"
	"github.com/stretchr/testify/assert"
)

// TestRequireAdmin tests the admin-only middleware
func TestRequireAdmin(t *testing.T) {
	tests := []struct {
		name           string
		keyType        auth.KeyType
		contextSet     bool
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "admin key allowed",
			keyType:        auth.KeyTypeAdmin,
			contextSet:     true,
			expectedStatus: http.StatusOK,
			expectedBody:   "success",
		},
		{
			name:           "client key forbidden",
			keyType:        auth.KeyTypeClient,
			contextSet:     true,
			expectedStatus: http.StatusForbidden,
			expectedBody:   "Admin access required",
		},
		{
			name:           "worker key forbidden",
			keyType:        auth.KeyTypeWorker,
			contextSet:     true,
			expectedStatus: http.StatusForbidden,
			expectedBody:   "Admin access required",
		},
		{
			name:           "missing context",
			keyType:        auth.KeyTypeAdmin,
			contextSet:     false,
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Authentication context missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test handler that returns success
			successHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("success"))
			})

			// Wrap with RequireAdmin middleware
			handler := RequireAdmin(successHandler)

			// Create request with context
			req := httptest.NewRequest("GET", "/admin/test", nil)

			if tt.contextSet {
				ctx := context.WithValue(req.Context(), contextKeyKeyType, tt.keyType)
				req = req.WithContext(ctx)
			}

			// Record response
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			// Assert
			assert.Equal(t, tt.expectedStatus, rr.Code)
			assert.Contains(t, rr.Body.String(), tt.expectedBody)
		})
	}
}
