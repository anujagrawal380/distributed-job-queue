package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateAPIKey(t *testing.T) {
	tests := []struct {
		name      string
		keyName   string
		ownerID   string
		keyType   KeyType
		wantErr   bool
		checkFunc func(t *testing.T, key *APIKey)
	}{
		{
			name:    "client key generation",
			keyName: "Test Client Key",
			ownerID: "alice",
			keyType: KeyTypeClient,
			wantErr: false,
			checkFunc: func(t *testing.T, key *APIKey) {
				assert.True(t, strings.HasPrefix(key.Key, "client_"), "key should start with 'client_'")
				assert.Equal(t, "Test Client Key", key.Name)
				assert.Equal(t, "alice", key.OwnerID)
				assert.Equal(t, KeyTypeClient, key.Type)
				assert.Equal(t, DefaultScopes(KeyTypeClient), key.Scopes)
				assert.Nil(t, key.RevokedAt, "new key should not be revoked")
				assert.WithinDuration(t, time.Now(), key.CreatedAt, 1*time.Second)
				assert.WithinDuration(t, time.Now(), key.LastUsedAt, 1*time.Second)
			},
		},
		{
			name:    "worker key generation",
			keyName: "Test Worker Key",
			ownerID: "worker-pool",
			keyType: KeyTypeWorker,
			wantErr: false,
			checkFunc: func(t *testing.T, key *APIKey) {
				assert.True(t, strings.HasPrefix(key.Key, "worker_"), "key should start with 'worker_'")
				assert.Equal(t, KeyTypeWorker, key.Type)
				assert.Equal(t, DefaultScopes(KeyTypeWorker), key.Scopes)
			},
		},
		{
			name:    "admin key generation",
			keyName: "Test Admin Key",
			ownerID: "admin-user",
			keyType: KeyTypeAdmin,
			wantErr: false,
			checkFunc: func(t *testing.T, key *APIKey) {
				assert.True(t, strings.HasPrefix(key.Key, "admin_"), "key should start with 'admin_'")
				assert.Equal(t, KeyTypeAdmin, key.Type)
				assert.Equal(t, DefaultScopes(KeyTypeAdmin), key.Scopes)
				// Admin should have more scopes
				assert.Greater(t, len(key.Scopes), len(DefaultScopes(KeyTypeClient)))
			},
		},
		{
			name:    "empty name",
			keyName: "",
			ownerID: "test",
			keyType: KeyTypeClient,
			wantErr: false,
			checkFunc: func(t *testing.T, key *APIKey) {
				assert.Equal(t, "", key.Name, "empty name should be allowed")
			},
		},
		{
			name:    "empty owner ID",
			keyName: "Test",
			ownerID: "",
			keyType: KeyTypeClient,
			wantErr: false,
			checkFunc: func(t *testing.T, key *APIKey) {
				assert.Equal(t, "", key.OwnerID, "empty owner ID should be allowed")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := GenerateAPIKey(tt.keyName, tt.ownerID, tt.keyType)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, key)
			} else {
				require.NoError(t, err)
				require.NotNil(t, key)
				if tt.checkFunc != nil {
					tt.checkFunc(t, key)
				}
			}
		})
	}
}

func TestGenerateAPIKey_Uniqueness(t *testing.T) {
	// Generate multiple keys and ensure they're unique
	keys := make(map[string]bool)
	for i := 0; i < 100; i++ {
		key, err := GenerateAPIKey("Test", "test", KeyTypeClient)
		require.NoError(t, err)

		assert.False(t, keys[key.Key], "generated duplicate key: %s", key.Key)
		keys[key.Key] = true
	}
}

func TestGenerateAPIKey_TokenLength(t *testing.T) {
	key, err := GenerateAPIKey("Test", "test", KeyTypeClient)
	require.NoError(t, err)

	// Token format: "prefix_base64token"
	// Prefix is 6-7 chars (client_, worker_, admin_)
	// Base64 of 24 bytes = 32 chars (no padding)
	// Total should be around 39-40 chars
	assert.Greater(t, len(key.Key), 30, "token should be sufficiently long")
	assert.Less(t, len(key.Key), 50, "token should not be too long")
}

func TestDefaultScopes(t *testing.T) {
	tests := []struct {
		name             string
		keyType          KeyType
		expectedScopes   []string
		shouldContain    []string
		shouldNotContain []string
	}{
		{
			name:             "client scopes",
			keyType:          KeyTypeClient,
			expectedScopes:   []string{ScopeJobsSubmit, ScopeJobsRead},
			shouldContain:    []string{ScopeJobsSubmit, ScopeJobsRead},
			shouldNotContain: []string{ScopeJobsLease, ScopeJobsAck, ScopeKeysCreate},
		},
		{
			name:             "worker scopes",
			keyType:          KeyTypeWorker,
			expectedScopes:   []string{ScopeJobsLease, ScopeJobsAck, ScopeJobsRead},
			shouldContain:    []string{ScopeJobsLease, ScopeJobsAck, ScopeJobsRead},
			shouldNotContain: []string{ScopeJobsSubmit, ScopeKeysCreate},
		},
		{
			name:    "admin scopes",
			keyType: KeyTypeAdmin,
			shouldContain: []string{
				ScopeJobsSubmit, ScopeJobsRead, ScopeJobsLease, ScopeJobsAck,
				ScopeKeysCreate, ScopeKeysRevoke, ScopeKeysRead,
			},
		},
		{
			name:           "unknown key type",
			keyType:        KeyType("unknown"),
			expectedScopes: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scopes := DefaultScopes(tt.keyType)

			if tt.expectedScopes != nil {
				assert.Equal(t, tt.expectedScopes, scopes)
			}

			for _, scope := range tt.shouldContain {
				assert.Contains(t, scopes, scope, "should contain scope: %s", scope)
			}

			for _, scope := range tt.shouldNotContain {
				assert.NotContains(t, scopes, scope, "should not contain scope: %s", scope)
			}
		})
	}
}
