package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anujagrawal380/distributed-job-queue/internal/auth"
)

// CreateKeyRequest represents the request to create a new API key
type CreateKeyRequest struct {
	Name    string `json:"name"`
	OwnerID string `json:"owner_id"`
	Type    string `json:"type"` // "client", "worker", or "admin"
}

// KeyResponse represents a sanitized API key response
type KeyResponse struct {
	Key        string     `json:"key"`
	Name       string     `json:"name"`
	OwnerID    string     `json:"owner_id"`
	Type       string     `json:"type"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt time.Time  `json:"last_used_at"`
}

// RevokeKeyResponse represents the response after revoking a key
type RevokeKeyResponse struct {
	Message   string    `json:"message"`
	Key       string    `json:"key"`
	RevokedAt time.Time `json:"revoked_at"`
}

// toKeyResponse converts an auth.APIKey to KeyResponse
func toKeyResponse(key *auth.APIKey) KeyResponse {
	return KeyResponse{
		Key:        key.Key,
		Name:       key.Name,
		OwnerID:    key.OwnerID,
		Type:       string(key.Type),
		Scopes:     key.Scopes,
		CreatedAt:  key.CreatedAt,
		RevokedAt:  key.RevokedAt,
		LastUsedAt: key.LastUsedAt,
	}
}

// HandleCreateKey handles POST /admin/keys - creates a new API key (admin only)
func (s *Server) HandleCreateKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.sendError(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req CreateKeyRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.sendError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate request
	if req.Name == "" {
		s.sendError(w, "name is required", http.StatusBadRequest)
		return
	}
	if req.OwnerID == "" {
		s.sendError(w, "owner_id is required", http.StatusBadRequest)
		return
	}
	if req.Type == "" {
		s.sendError(w, "type is required", http.StatusBadRequest)
		return
	}

	// Validate key type
	keyType, err := auth.ValidateKeyType(req.Type)
	if err != nil {
		s.sendError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Generate new API key
	apiKey, err := auth.GenerateAPIKey(req.Name, req.OwnerID, keyType)
	if err != nil {
		s.sendError(w, "failed to generate API key", http.StatusInternalServerError)
		return
	}

	// Store in Redis
	ctx := r.Context()
	if err := s.authStore.CreateKey(ctx, apiKey); err != nil {
		s.sendError(w, "failed to store API key", http.StatusInternalServerError)
		return
	}

	// Return the key
	response := toKeyResponse(apiKey)
	s.sendJSON(w, response, http.StatusCreated)
}

// HandleListKeys handles GET /keys - lists keys owned by the authenticated user
func (s *Server) HandleListKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get owner ID from authenticated context
	ownerID, ok := GetOwnerID(r.Context())
	if !ok {
		s.sendError(w, "authentication context missing", http.StatusInternalServerError)
		return
	}

	// Get keys for this owner
	ctx := r.Context()
	keys, err := s.authStore.ListKeys(ctx, ownerID)
	if err != nil {
		s.sendError(w, "failed to list keys", http.StatusInternalServerError)
		return
	}

	// Convert to response format
	response := make([]KeyResponse, 0, len(keys))
	for _, key := range keys {
		response = append(response, toKeyResponse(key))
	}

	s.sendJSON(w, response, http.StatusOK)
}

// HandleListAllKeys handles GET /admin/keys - lists all keys in the system (admin only)
func (s *Server) HandleListAllKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get all keys
	ctx := r.Context()
	keys, err := s.authStore.ListAllKeys(ctx)
	if err != nil {
		s.sendError(w, "failed to list keys", http.StatusInternalServerError)
		return
	}

	// Convert to response format
	response := make([]KeyResponse, 0, len(keys))
	for _, key := range keys {
		response = append(response, toKeyResponse(key))
	}

	s.sendJSON(w, response, http.StatusOK)
}

// HandleRevokeKey handles DELETE /keys/{key} - revokes an API key
func (s *Server) HandleRevokeKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract key from URL path: /keys/{key}
	path := strings.TrimPrefix(r.URL.Path, "/keys/")
	keyToRevoke := strings.TrimSpace(path)

	if keyToRevoke == "" {
		s.sendError(w, "key parameter is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Get the key to be revoked
	targetKey, err := s.authStore.GetKey(ctx, keyToRevoke)
	if err != nil {
		s.sendError(w, "key not found", http.StatusNotFound)
		return
	}

	// Authorization check: admin can revoke any key, users can only revoke their own keys
	keyType, _ := GetKeyType(ctx)
	if keyType != auth.KeyTypeAdmin {
		currentOwnerID, _ := GetOwnerID(ctx)
		if targetKey.OwnerID != currentOwnerID {
			s.sendError(w, "access denied: not the owner", http.StatusForbidden)
			return
		}
	}

	// Revoke the key
	if err := s.authStore.RevokeKey(ctx, keyToRevoke); err != nil {
		s.sendError(w, "failed to revoke key", http.StatusInternalServerError)
		return
	}

	// Return success response
	response := RevokeKeyResponse{
		Message:   "Key revoked successfully",
		Key:       keyToRevoke,
		RevokedAt: time.Now(),
	}

	s.sendJSON(w, response, http.StatusOK)
}
