package auth

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestRedis creates a test Redis instance using miniredis
func setupTestRedis(t *testing.T) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()

	// Start miniredis (in-memory Redis)
	mr := miniredis.RunT(t)

	// Create Redis client
	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	// Create store
	store := NewRedisStore(client)

	return store, mr
}

func TestRedisStore_CreateKey(t *testing.T) {
	tests := []struct {
		name    string
		key     *APIKey
		wantErr bool
	}{
		{
			name: "create client key",
			key: &APIKey{
				Key:        "client_test123",
				Name:       "Test Client",
				OwnerID:    "alice",
				Type:       KeyTypeClient,
				Scopes:     []string{"jobs:submit", "jobs:read"},
				CreatedAt:  time.Now(),
				LastUsedAt: time.Now(),
			},
			wantErr: false,
		},
		{
			name: "create worker key",
			key: &APIKey{
				Key:        "worker_test456",
				Name:       "Test Worker",
				OwnerID:    "worker-pool",
				Type:       KeyTypeWorker,
				Scopes:     []string{"jobs:lease", "jobs:ack"},
				CreatedAt:  time.Now(),
				LastUsedAt: time.Now(),
			},
			wantErr: false,
		},
		{
			name: "create with empty name",
			key: &APIKey{
				Key:        "client_test789",
				Name:       "",
				OwnerID:    "bob",
				Type:       KeyTypeClient,
				Scopes:     []string{"jobs:submit"},
				CreatedAt:  time.Now(),
				LastUsedAt: time.Now(),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := setupTestRedis(t)
			ctx := context.Background()

			err := store.CreateKey(ctx, tt.key)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)

				// Verify key was created
				retrieved, err := store.GetKey(ctx, tt.key.Key)
				require.NoError(t, err)
				assert.Equal(t, tt.key.Key, retrieved.Key)
				assert.Equal(t, tt.key.Name, retrieved.Name)
				assert.Equal(t, tt.key.OwnerID, retrieved.OwnerID)
				assert.Equal(t, tt.key.Type, retrieved.Type)
				assert.Equal(t, tt.key.Scopes, retrieved.Scopes)
			}
		})
	}
}

func TestRedisStore_GetKey(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*RedisStore, context.Context) string
		getKey      string
		wantErr     bool
		errContains string
	}{
		{
			name: "get existing key",
			setup: func(s *RedisStore, ctx context.Context) string {
				key := &APIKey{
					Key:        "client_existing",
					Name:       "Test",
					OwnerID:    "alice",
					Type:       KeyTypeClient,
					Scopes:     []string{"jobs:submit"},
					CreatedAt:  time.Now(),
					LastUsedAt: time.Now(),
				}
				s.CreateKey(ctx, key)
				return key.Key
			},
			wantErr: false,
		},
		{
			name: "get non-existent key",
			setup: func(s *RedisStore, ctx context.Context) string {
				return "nonexistent_key"
			},
			wantErr:     true,
			errContains: "key not found",
		},
		{
			name: "get revoked key",
			setup: func(s *RedisStore, ctx context.Context) string {
				key := &APIKey{
					Key:        "client_revoked",
					Name:       "Test",
					OwnerID:    "alice",
					Type:       KeyTypeClient,
					Scopes:     []string{"jobs:submit"},
					CreatedAt:  time.Now(),
					RevokedAt:  timePtr(time.Now()),
					LastUsedAt: time.Now(),
				}
				s.CreateKey(ctx, key)
				return key.Key
			},
			wantErr: false, // GetKey should return revoked keys (check happens elsewhere)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := setupTestRedis(t)
			ctx := context.Background()

			keyToGet := tt.setup(store, ctx)
			retrieved, err := store.GetKey(ctx, keyToGet)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, keyToGet, retrieved.Key)
			}
		})
	}
}

func TestRedisStore_RevokeKey(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*RedisStore, context.Context) string
		revokeKey   string
		wantErr     bool
		errContains string
	}{
		{
			name: "revoke existing key",
			setup: func(s *RedisStore, ctx context.Context) string {
				key := &APIKey{
					Key:        "client_torevoke",
					Name:       "Test",
					OwnerID:    "alice",
					Type:       KeyTypeClient,
					Scopes:     []string{"jobs:submit"},
					CreatedAt:  time.Now(),
					LastUsedAt: time.Now(),
				}
				s.CreateKey(ctx, key)
				return key.Key
			},
			wantErr: false,
		},
		{
			name: "revoke non-existent key",
			setup: func(s *RedisStore, ctx context.Context) string {
				return "nonexistent_key"
			},
			wantErr:     true,
			errContains: "key not found",
		},
		{
			name: "revoke already revoked key",
			setup: func(s *RedisStore, ctx context.Context) string {
				key := &APIKey{
					Key:        "client_alreadyrevoked",
					Name:       "Test",
					OwnerID:    "alice",
					Type:       KeyTypeClient,
					Scopes:     []string{"jobs:submit"},
					CreatedAt:  time.Now(),
					LastUsedAt: time.Now(),
				}
				s.CreateKey(ctx, key)
				s.RevokeKey(ctx, key.Key)
				return key.Key
			},
			wantErr: false, // Should succeed (idempotent)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := setupTestRedis(t)
			ctx := context.Background()

			keyToRevoke := tt.setup(store, ctx)
			err := store.RevokeKey(ctx, keyToRevoke)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)

				// Verify key is revoked
				retrieved, err := store.GetKey(ctx, keyToRevoke)
				require.NoError(t, err)
				assert.True(t, retrieved.IsRevoked())
			}
		})
	}
}

func TestRedisStore_ListKeys(t *testing.T) {
	store, _ := setupTestRedis(t)
	ctx := context.Background()

	// Create keys for different owners
	keys := []*APIKey{
		{Key: "client_alice1", Name: "Alice Key 1", OwnerID: "alice", Type: KeyTypeClient, Scopes: []string{"jobs:submit"}, CreatedAt: time.Now(), LastUsedAt: time.Now()},
		{Key: "client_alice2", Name: "Alice Key 2", OwnerID: "alice", Type: KeyTypeClient, Scopes: []string{"jobs:submit"}, CreatedAt: time.Now(), LastUsedAt: time.Now()},
		{Key: "client_bob1", Name: "Bob Key 1", OwnerID: "bob", Type: KeyTypeClient, Scopes: []string{"jobs:submit"}, CreatedAt: time.Now(), LastUsedAt: time.Now()},
	}

	for _, key := range keys {
		require.NoError(t, store.CreateKey(ctx, key))
	}

	tests := []struct {
		name      string
		ownerID   string
		wantCount int
		wantKeys  []string
	}{
		{
			name:      "list alice's keys",
			ownerID:   "alice",
			wantCount: 2,
			wantKeys:  []string{"client_alice1", "client_alice2"},
		},
		{
			name:      "list bob's keys",
			ownerID:   "bob",
			wantCount: 1,
			wantKeys:  []string{"client_bob1"},
		},
		{
			name:      "list non-existent owner",
			ownerID:   "charlie",
			wantCount: 0,
			wantKeys:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retrieved, err := store.ListKeys(ctx, tt.ownerID)
			require.NoError(t, err)
			assert.Len(t, retrieved, tt.wantCount)

			retrievedKeys := make([]string, len(retrieved))
			for i, k := range retrieved {
				retrievedKeys[i] = k.Key
			}
			assert.ElementsMatch(t, tt.wantKeys, retrievedKeys)
		})
	}
}

func TestRedisStore_UpdateLastUsed(t *testing.T) {
	store, _ := setupTestRedis(t)
	ctx := context.Background()

	// Create a key
	originalTime := time.Now().Add(-1 * time.Hour)
	key := &APIKey{
		Key:        "client_test",
		Name:       "Test",
		OwnerID:    "alice",
		Type:       KeyTypeClient,
		Scopes:     []string{"jobs:submit"},
		CreatedAt:  originalTime,
		LastUsedAt: originalTime,
	}
	require.NoError(t, store.CreateKey(ctx, key))

	// Update last used
	time.Sleep(10 * time.Millisecond) // Ensure time difference
	err := store.UpdateLastUsed(ctx, key.Key)
	require.NoError(t, err)

	// Verify updated
	retrieved, err := store.GetKey(ctx, key.Key)
	require.NoError(t, err)
	assert.True(t, retrieved.LastUsedAt.After(originalTime))
}

// Helper function to create time pointer
func timePtr(t time.Time) *time.Time {
	return &t
}
