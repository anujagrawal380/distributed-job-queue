package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anujagrawal380/distributed-job-queue/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockAuthStore is a mock implementation of auth.Store for testing
type MockAuthStore struct {
	mock.Mock
}

func (m *MockAuthStore) CreateKey(ctx context.Context, key *auth.APIKey) error {
	args := m.Called(ctx, key)
	return args.Error(0)
}

func (m *MockAuthStore) GetKey(ctx context.Context, apiKey string) (*auth.APIKey, error) {
	args := m.Called(ctx, apiKey)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*auth.APIKey), args.Error(1)
}

func (m *MockAuthStore) RevokeKey(ctx context.Context, apiKey string) error {
	args := m.Called(ctx, apiKey)
	return args.Error(0)
}

func (m *MockAuthStore) ListKeys(ctx context.Context, ownerID string) ([]*auth.APIKey, error) {
	args := m.Called(ctx, ownerID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*auth.APIKey), args.Error(1)
}

func (m *MockAuthStore) ListAllKeys(ctx context.Context) ([]*auth.APIKey, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*auth.APIKey), args.Error(1)
}

func (m *MockAuthStore) UpdateLastUsed(ctx context.Context, apiKey string) error {
	args := m.Called(ctx, apiKey)
	return args.Error(0)
}

// Test helper: creates a valid API key
func createTestAPIKey(keyType auth.KeyType) *auth.APIKey {
	return &auth.APIKey{
		Key:        "test_key_123",
		Name:       "Test Key",
		OwnerID:    "test-owner",
		Type:       keyType,
		Scopes:     auth.DefaultScopes(keyType),
		CreatedAt:  time.Now(),
		RevokedAt:  nil,
		LastUsedAt: time.Now(),
	}
}

// Test helper: creates a revoked API key
func createRevokedAPIKey() *auth.APIKey {
	now := time.Now()
	return &auth.APIKey{
		Key:        "revoked_key_123",
		Name:       "Revoked Key",
		OwnerID:    "test-owner",
		Type:       auth.KeyTypeClient,
		Scopes:     auth.DefaultScopes(auth.KeyTypeClient),
		CreatedAt:  time.Now(),
		RevokedAt:  &now,
		LastUsedAt: time.Now(),
	}
}

func TestAuthMiddleware(t *testing.T) {
	tests := []struct {
		name           string
		authHeader     string
		setupMock      func(*MockAuthStore)
		expectedStatus int
		expectedBody   string
		checkContext   func(t *testing.T, r *http.Request)
	}{
		{
			name:       "valid client key",
			authHeader: "Bearer test_key_123",
			setupMock: func(m *MockAuthStore) {
				m.On("GetKey", mock.Anything, "test_key_123").Return(createTestAPIKey(auth.KeyTypeClient), nil)
				m.On("UpdateLastUsed", mock.Anything, "test_key_123").Return(nil)
			},
			expectedStatus: http.StatusOK,
			checkContext: func(t *testing.T, r *http.Request) {
				ownerID, ok := GetOwnerID(r.Context())
				assert.True(t, ok)
				assert.Equal(t, "test-owner", ownerID)

				keyType, ok := GetKeyType(r.Context())
				assert.True(t, ok)
				assert.Equal(t, auth.KeyTypeClient, keyType)

				scopes, ok := GetScopes(r.Context())
				assert.True(t, ok)
				assert.NotEmpty(t, scopes)
			},
		},
		{
			name:       "valid worker key",
			authHeader: "Bearer worker_key_456",
			setupMock: func(m *MockAuthStore) {
				m.On("GetKey", mock.Anything, "worker_key_456").Return(createTestAPIKey(auth.KeyTypeWorker), nil)
				m.On("UpdateLastUsed", mock.Anything, "worker_key_456").Return(nil)
			},
			expectedStatus: http.StatusOK,
			checkContext: func(t *testing.T, r *http.Request) {
				keyType, ok := GetKeyType(r.Context())
				assert.True(t, ok)
				assert.Equal(t, auth.KeyTypeWorker, keyType)
			},
		},
		{
			name:           "missing authorization header",
			authHeader:     "",
			setupMock:      func(m *MockAuthStore) {},
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "Missing Authorization header",
		},
		{
			name:           "invalid header format - no Bearer",
			authHeader:     "test_key_123",
			setupMock:      func(m *MockAuthStore) {},
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "Invalid Authorization header format",
		},
		{
			name:           "invalid header format - wrong prefix",
			authHeader:     "Basic test_key_123",
			setupMock:      func(m *MockAuthStore) {},
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "Invalid Authorization header format",
		},
		{
			name:           "empty token",
			authHeader:     "Bearer ",
			setupMock:      func(m *MockAuthStore) {},
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "Empty API key",
		},
		{
			name:       "invalid API key - not found",
			authHeader: "Bearer invalid_key",
			setupMock: func(m *MockAuthStore) {
				m.On("GetKey", mock.Anything, "invalid_key").Return(nil, assert.AnError)
			},
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "Invalid API key",
		},
		{
			name:       "revoked API key",
			authHeader: "Bearer revoked_key_123",
			setupMock: func(m *MockAuthStore) {
				m.On("GetKey", mock.Anything, "revoked_key_123").Return(createRevokedAPIKey(), nil)
			},
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "API key has been revoked",
		},
		{
			name:       "token with spaces",
			authHeader: "Bearer test key 123",
			setupMock: func(m *MockAuthStore) {
				m.On("GetKey", mock.Anything, "test key 123").Return(createTestAPIKey(auth.KeyTypeClient), nil)
				m.On("UpdateLastUsed", mock.Anything, "test key 123").Return(nil)
			},
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStore := new(MockAuthStore)
			tt.setupMock(mockStore)

			// Create test handler that will be wrapped
			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.checkContext != nil {
					tt.checkContext(t, r)
				}
				w.WriteHeader(http.StatusOK)
			})

			// Wrap with auth middleware
			handler := AuthMiddleware(mockStore)(nextHandler)

			// Create test request
			req := httptest.NewRequest("GET", "/test", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			// Record response
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			// Assertions
			assert.Equal(t, tt.expectedStatus, rr.Code)
			if tt.expectedBody != "" {
				assert.Contains(t, rr.Body.String(), tt.expectedBody)
			}

			if tt.expectedStatus == http.StatusOK {
				time.Sleep(50 * time.Millisecond)
			}

			mockStore.AssertExpectations(t)
		})
	}
}

func TestRequireScope(t *testing.T) {
	tests := []struct {
		name           string
		requiredScope  string
		contextScopes  []string
		hasContext     bool
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "has required scope",
			requiredScope:  "jobs:submit",
			contextScopes:  []string{"jobs:submit", "jobs:read"},
			hasContext:     true,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "missing required scope",
			requiredScope:  "jobs:lease",
			contextScopes:  []string{"jobs:submit", "jobs:read"},
			hasContext:     true,
			expectedStatus: http.StatusForbidden,
			expectedBody:   "Insufficient permissions",
		},
		{
			name:           "empty scopes",
			requiredScope:  "jobs:submit",
			contextScopes:  []string{},
			hasContext:     true,
			expectedStatus: http.StatusForbidden,
			expectedBody:   "Insufficient permissions",
		},
		{
			name:           "nil scopes",
			requiredScope:  "jobs:submit",
			contextScopes:  nil,
			hasContext:     true,
			expectedStatus: http.StatusForbidden,
			expectedBody:   "Insufficient permissions",
		},
		{
			name:           "no context",
			requiredScope:  "jobs:submit",
			hasContext:     false,
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Authentication context missing",
		},
		{
			name:           "case sensitive scope check",
			requiredScope:  "jobs:Submit",
			contextScopes:  []string{"jobs:submit"},
			hasContext:     true,
			expectedStatus: http.StatusForbidden,
			expectedBody:   "Insufficient permissions",
		},
		{
			name:           "multiple scopes with match",
			requiredScope:  "jobs:read",
			contextScopes:  []string{"jobs:submit", "jobs:read", "jobs:lease"},
			hasContext:     true,
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test handler
			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			// Wrap with scope middleware
			handler := RequireScope(tt.requiredScope)(nextHandler)

			// Create request with context
			req := httptest.NewRequest("GET", "/test", nil)
			if tt.hasContext {
				ctx := context.WithValue(req.Context(), contextKeyScopes, tt.contextScopes)
				req = req.WithContext(ctx)
			}

			// Record response
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			// Assertions
			assert.Equal(t, tt.expectedStatus, rr.Code)
			if tt.expectedBody != "" {
				assert.Contains(t, rr.Body.String(), tt.expectedBody)
			}
		})
	}
}

func TestGetOwnerID(t *testing.T) {
	tests := []struct {
		name      string
		setupCtx  func() context.Context
		wantValue string
		wantOk    bool
	}{
		{
			name: "valid owner ID",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), contextKeyOwnerID, "alice")
			},
			wantValue: "alice",
			wantOk:    true,
		},
		{
			name: "missing owner ID",
			setupCtx: func() context.Context {
				return context.Background()
			},
			wantValue: "",
			wantOk:    false,
		},
		{
			name: "wrong type in context",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), contextKeyOwnerID, 123)
			},
			wantValue: "",
			wantOk:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setupCtx()
			value, ok := GetOwnerID(ctx)
			assert.Equal(t, tt.wantValue, value)
			assert.Equal(t, tt.wantOk, ok)
		})
	}
}

func TestGetKeyType(t *testing.T) {
	tests := []struct {
		name      string
		setupCtx  func() context.Context
		wantValue auth.KeyType
		wantOk    bool
	}{
		{
			name: "valid key type",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), contextKeyKeyType, auth.KeyTypeClient)
			},
			wantValue: auth.KeyTypeClient,
			wantOk:    true,
		},
		{
			name: "missing key type",
			setupCtx: func() context.Context {
				return context.Background()
			},
			wantValue: "",
			wantOk:    false,
		},
		{
			name: "wrong type in context",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), contextKeyKeyType, "not-a-keytype")
			},
			wantValue: "",
			wantOk:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setupCtx()
			value, ok := GetKeyType(ctx)
			assert.Equal(t, tt.wantValue, value)
			assert.Equal(t, tt.wantOk, ok)
		})
	}
}

func TestGetScopes(t *testing.T) {
	tests := []struct {
		name      string
		setupCtx  func() context.Context
		wantValue []string
		wantOk    bool
	}{
		{
			name: "valid scopes",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), contextKeyScopes, []string{"jobs:submit", "jobs:read"})
			},
			wantValue: []string{"jobs:submit", "jobs:read"},
			wantOk:    true,
		},
		{
			name: "empty scopes",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), contextKeyScopes, []string{})
			},
			wantValue: []string{},
			wantOk:    true,
		},
		{
			name: "missing scopes",
			setupCtx: func() context.Context {
				return context.Background()
			},
			wantValue: nil,
			wantOk:    false,
		},
		{
			name: "wrong type in context",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), contextKeyScopes, "not-a-slice")
			},
			wantValue: nil,
			wantOk:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setupCtx()
			value, ok := GetScopes(ctx)
			assert.Equal(t, tt.wantValue, value)
			assert.Equal(t, tt.wantOk, ok)
		})
	}
}

func TestGetAPIKey(t *testing.T) {
	validKey := createTestAPIKey(auth.KeyTypeClient)

	tests := []struct {
		name      string
		setupCtx  func() context.Context
		wantValue *auth.APIKey
		wantOk    bool
	}{
		{
			name: "valid API key",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), contextKeyAPIKey, validKey)
			},
			wantValue: validKey,
			wantOk:    true,
		},
		{
			name: "missing API key",
			setupCtx: func() context.Context {
				return context.Background()
			},
			wantValue: nil,
			wantOk:    false,
		},
		{
			name: "wrong type in context",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), contextKeyAPIKey, "not-a-key")
			},
			wantValue: nil,
			wantOk:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setupCtx()
			value, ok := GetAPIKey(ctx)
			assert.Equal(t, tt.wantValue, value)
			assert.Equal(t, tt.wantOk, ok)
		})
	}
}

func TestAuthMiddleware_UpdateLastUsedAsync(t *testing.T) {
	mockStore := new(MockAuthStore)
	validKey := createTestAPIKey(auth.KeyTypeClient)

	mockStore.On("GetKey", mock.Anything, "test_key_123").Return(validKey, nil)
	mockStore.On("UpdateLastUsed", mock.Anything, "test_key_123").Return(nil)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := AuthMiddleware(mockStore)(nextHandler)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer test_key_123")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	// Give goroutine time to complete
	time.Sleep(100 * time.Millisecond)

	// UpdateLastUsed should have been called
	mockStore.AssertExpectations(t)
}
