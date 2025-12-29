package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"
)

// GenerateAPIKey creates a new API key with the given parameters
func GenerateAPIKey(name, ownerID string, keyType KeyType) (*APIKey, error) {
	// Generate random token
	token, err := generateToken(keyType)
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	now := time.Now()

	return &APIKey{
		Key:        token,
		Name:       name,
		OwnerID:    ownerID,
		Type:       keyType,
		Scopes:     DefaultScopes(keyType),
		CreatedAt:  now,
		RevokedAt:  nil,
		LastUsedAt: now,
	}, nil
}

// generateToken creates a cryptographically secure random token
func generateToken(keyType KeyType) (string, error) {
	// Generate 24 random bytes (will be 32 chars in base64)
	randomBytes := make([]byte, 24)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}

	// Encode to URL-safe base64 (no padding)
	token := base64.RawURLEncoding.EncodeToString(randomBytes)

	// Prefix with key type for easy identification
	prefix := string(keyType)
	return fmt.Sprintf("%s_%s", prefix, token), nil
}

// Example tokens:
// client_8x9j2k4n5m6p7q8r9s0t1u2v3w4x5y6z
// worker_a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6
// admin_z9y8x7w6v5u4t3s2r1q0p9o8n7m6l5k4
