// Package models defines shared data types used across auth, db, and mcpserver.
package models

import "time"

// ConnectedAccount represents a user's authenticated connection to a platform.
type ConnectedAccount struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"`
	Platform     string    `json:"platform"`      // "github", "google", "slack", "notion"
	Status       string    `json:"status"`         // "initiated", "active", "expired", "failed"
	StatusReason string    `json:"status_reason"`  // reason for current status
	AccessToken  string    `json:"-"`              // never expose in JSON
	RefreshToken string    `json:"-"`
	TokenType    string    `json:"token_type"`
	Scopes       string    `json:"scopes"`         // comma-separated
	ExpiresAt    *time.Time `json:"expires_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// IsActive returns true if the account can be used for tool execution.
func (a *ConnectedAccount) IsActive() bool {
	return a.Status == StatusActive
}

// IsExpired returns true if the access token has expired.
func (a *ConnectedAccount) IsExpired() bool {
	if a.ExpiresAt == nil {
		return false // tokens without expiry (e.g. GitHub) don't expire
	}
	return time.Now().After(*a.ExpiresAt)
}

// NeedsRefresh returns true if the token is expired but has a refresh token.
func (a *ConnectedAccount) NeedsRefresh() bool {
	return a.IsExpired() && a.RefreshToken != ""
}

// Account statuses
const (
	StatusInitiated = "initiated" // OAuth flow started, waiting for user
	StatusActive    = "active"    // Token valid, tools executable
	StatusExpired   = "expired"   // Token expired, refresh failed
	StatusFailed    = "failed"    // Auth attempt failed
)

// AuthConfig holds the developer-level OAuth configuration for a platform.
// One config per platform, reused for all users.
type AuthConfig struct {
	Platform     string `json:"platform"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"-"`
	Scopes       string `json:"scopes"`
}

// PendingAuth tracks an in-progress OAuth flow (before user completes authorization).
type PendingAuth struct {
	State       string    `json:"state"`        // OAuth state parameter (CSRF protection)
	UserID      string    `json:"user_id"`
	Platform    string    `json:"platform"`
	CallbackURL string    `json:"callback_url"` // where to redirect after auth
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`   // 10-minute TTL
}
