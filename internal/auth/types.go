package auth

import "time"

// KeyType represents the type of API key
type KeyType string

const (
	KeyTypeClient KeyType = "client" // Submits jobs
	KeyTypeWorker KeyType = "worker" // Processes jobs
	KeyTypeAdmin  KeyType = "admin"  // Manages keys and views all data
)

// Scope constants for permission checking
const (
	ScopeJobsSubmit = "jobs:submit" // Can create new jobs
	ScopeJobsRead   = "jobs:read"   // Can read job status
	ScopeJobsLease  = "jobs:lease"  // Can lease jobs for processing
	ScopeJobsAck    = "jobs:ack"    // Can acknowledge job completion
	ScopeKeysCreate = "keys:create" // Can create API keys
	ScopeKeysRevoke = "keys:revoke" // Can revoke API keys
	ScopeKeysRead   = "keys:read"   // Can list API keys
)

// APIKey represents an authentication credential
type APIKey struct {
	Key        string     `json:"key"`                  // The actual API key token
	Name       string     `json:"name"`                 // Human-friendly name
	OwnerID    string     `json:"owner_id"`             // Logical owner identifier
	Type       KeyType    `json:"type"`                 // client, worker, or admin
	Scopes     []string   `json:"scopes"`               // Permissions array
	CreatedAt  time.Time  `json:"created_at"`           // When key was created
	RevokedAt  *time.Time `json:"revoked_at,omitempty"` // nil = active, set = revoked
	LastUsedAt time.Time  `json:"last_used_at"`         // Last request timestamp
}

// IsRevoked checks if the key has been revoked
func (k *APIKey) IsRevoked() bool {
	return k.RevokedAt != nil
}

// HasScope checks if the key has a specific permission
func (k *APIKey) HasScope(scope string) bool {
	for _, s := range k.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// DefaultScopes returns the default scopes for each key type
func DefaultScopes(keyType KeyType) []string {
	switch keyType {
	case KeyTypeClient:
		return []string{ScopeJobsSubmit, ScopeJobsRead}
	case KeyTypeWorker:
		return []string{ScopeJobsLease, ScopeJobsAck, ScopeJobsRead}
	case KeyTypeAdmin:
		return []string{
			ScopeJobsSubmit, ScopeJobsRead, ScopeJobsLease, ScopeJobsAck,
			ScopeKeysCreate, ScopeKeysRevoke, ScopeKeysRead,
		}
	default:
		return []string{}
	}
}
