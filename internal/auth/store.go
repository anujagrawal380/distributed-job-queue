package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store defines the interface for API key storage
type Store interface {
	CreateKey(ctx context.Context, key *APIKey) error
	GetKey(ctx context.Context, apiKey string) (*APIKey, error)
	RevokeKey(ctx context.Context, apiKey string) error
	ListKeys(ctx context.Context, ownerID string) ([]*APIKey, error)
	ListAllKeys(ctx context.Context) ([]*APIKey, error)
	UpdateLastUsed(ctx context.Context, apiKey string) error
}

// RedisStore implements Store using Redis
type RedisStore struct {
	client *redis.Client
}

// NewRedisStore creates a new Redis-backed API key store
func NewRedisStore(client *redis.Client) *RedisStore {
	return &RedisStore{client: client}
}

// Redis key patterns
const (
	keyPrefix       = "api_key:"      // api_key:client_abc123
	ownerIndexKey   = "owner:%s:keys" // owner:anuj:keys
	typeIndexKey    = "keys:type:%s"  // keys:type:client
	activeIndexKey  = "keys:active"   // keys:active
	revokedIndexKey = "keys:revoked"  // keys:revoked
)

// CreateKey stores a new API key in Redis
func (s *RedisStore) CreateKey(ctx context.Context, key *APIKey) error {
	// Serialize key to JSON for scopes
	scopesJSON, err := json.Marshal(key.Scopes)
	if err != nil {
		return fmt.Errorf("failed to marshal scopes: %w", err)
	}

	// Store main key data as hash
	keyName := keyPrefix + key.Key
	pipe := s.client.Pipeline()

	pipe.HSet(ctx, keyName, map[string]interface{}{
		"key":          key.Key,
		"name":         key.Name,
		"owner_id":     key.OwnerID,
		"type":         string(key.Type),
		"scopes":       string(scopesJSON),
		"created_at":   key.CreatedAt.Unix(),
		"revoked_at":   "",
		"last_used_at": key.LastUsedAt.Unix(),
	})

	// Create indexes
	pipe.SAdd(ctx, fmt.Sprintf(ownerIndexKey, key.OwnerID), key.Key)
	pipe.SAdd(ctx, fmt.Sprintf(typeIndexKey, key.Type), key.Key)
	pipe.SAdd(ctx, activeIndexKey, key.Key)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to create key: %w", err)
	}

	return nil
}

// GetKey retrieves an API key from Redis
func (s *RedisStore) GetKey(ctx context.Context, apiKey string) (*APIKey, error) {
	keyName := keyPrefix + apiKey

	// Get all fields from hash
	data, err := s.client.HGetAll(ctx, keyName).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get key: %w", err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("key not found: %s", apiKey)
	}

	// Parse the data
	key := &APIKey{
		Key:     data["key"],
		Name:    data["name"],
		OwnerID: data["owner_id"],
		Type:    KeyType(data["type"]),
	}

	// Parse scopes JSON
	if scopesStr := data["scopes"]; scopesStr != "" {
		if err := json.Unmarshal([]byte(scopesStr), &key.Scopes); err != nil {
			return nil, fmt.Errorf("failed to unmarshal scopes: %w", err)
		}
	}

	// Parse timestamps
	if createdAt := data["created_at"]; createdAt != "" {
		var timestamp int64
		fmt.Sscanf(createdAt, "%d", &timestamp)
		key.CreatedAt = time.Unix(timestamp, 0)
	}

	if revokedAt := data["revoked_at"]; revokedAt != "" {
		var timestamp int64
		fmt.Sscanf(revokedAt, "%d", &timestamp)
		t := time.Unix(timestamp, 0)
		key.RevokedAt = &t
	}

	if lastUsedAt := data["last_used_at"]; lastUsedAt != "" {
		var timestamp int64
		fmt.Sscanf(lastUsedAt, "%d", &timestamp)
		key.LastUsedAt = time.Unix(timestamp, 0)
	}

	return key, nil
}

// RevokeKey marks an API key as revoked
func (s *RedisStore) RevokeKey(ctx context.Context, apiKey string) error {
	keyName := keyPrefix + apiKey

	// Check if key exists
	exists, err := s.client.Exists(ctx, keyName).Result()
	if err != nil {
		return fmt.Errorf("failed to check key existence: %w", err)
	}
	if exists == 0 {
		return fmt.Errorf("key not found: %s", apiKey)
	}

	now := time.Now()
	pipe := s.client.Pipeline()

	// Update revoked_at timestamp
	pipe.HSet(ctx, keyName, "revoked_at", now.Unix())

	// Update indexes
	pipe.SRem(ctx, activeIndexKey, apiKey)
	pipe.SAdd(ctx, revokedIndexKey, apiKey)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to revoke key: %w", err)
	}

	return nil
}

// ListKeys returns all API keys for a specific owner
func (s *RedisStore) ListKeys(ctx context.Context, ownerID string) ([]*APIKey, error) {
	// Get all key IDs for this client
	indexKey := fmt.Sprintf(ownerIndexKey, ownerID)
	keyIDs, err := s.client.SMembers(ctx, indexKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list keys: %w", err)
	}

	// Fetch each key
	keys := make([]*APIKey, 0, len(keyIDs))
	for _, keyID := range keyIDs {
		key, err := s.GetKey(ctx, keyID)
		if err != nil {
			// Log error but continue with other keys
			continue
		}
		keys = append(keys, key)
	}

	return keys, nil
}

// ListAllKeys returns all API keys (admin only)
func (s *RedisStore) ListAllKeys(ctx context.Context) ([]*APIKey, error) {
	// Get all active keys
	activeKeys, err := s.client.SMembers(ctx, activeIndexKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list active keys: %w", err)
	}

	// Get all revoked keys
	revokedKeys, err := s.client.SMembers(ctx, revokedIndexKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list revoked keys: %w", err)
	}

	// Combine
	allKeyIDs := append(activeKeys, revokedKeys...)

	// Fetch each key
	keys := make([]*APIKey, 0, len(allKeyIDs))
	for _, keyID := range allKeyIDs {
		key, err := s.GetKey(ctx, keyID)
		if err != nil {
			continue
		}
		keys = append(keys, key)
	}

	return keys, nil
}

// UpdateLastUsed updates the last_used_at timestamp
func (s *RedisStore) UpdateLastUsed(ctx context.Context, apiKey string) error {
	keyName := keyPrefix + apiKey
	now := time.Now()

	err := s.client.HSet(ctx, keyName, "last_used_at", now.Unix()).Err()
	if err != nil {
		return fmt.Errorf("failed to update last_used_at: %w", err)
	}

	return nil
}
