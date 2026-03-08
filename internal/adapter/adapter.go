// Package adapter defines the PlatformAdapter interface.
// Every external platform (GitHub, Google, Slack, Notion, etc.)
// implements this interface to plug into dinq-connector.
package adapter

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/DINQ-labs/dinq-connector/internal/composio"
)

// AuthScheme defines the authentication method a platform uses.
type AuthScheme string

const (
	AuthOAuth2   AuthScheme = "oauth2"
	AuthAPIKey   AuthScheme = "api_key"
	AuthBearer   AuthScheme = "bearer"
	AuthComposio AuthScheme = "composio" // OAuth managed by Composio
)

// OAuthConfig holds the OAuth2 configuration for a platform.
type OAuthConfig struct {
	AuthorizeURL string   // e.g. "https://github.com/login/oauth/authorize"
	TokenURL     string   // e.g. "https://github.com/login/oauth/access_token"
	Scopes       []string // e.g. ["repo", "read:user"]
	// ClientID and ClientSecret come from env/DB at runtime, not hardcoded here.
}

// PlatformAdapter is the interface every platform connector must implement.
// Adding a new platform = implement this interface + register in the registry.
type PlatformAdapter interface {
	// Name returns the platform identifier, e.g. "github", "gmail", "slack".
	// Tool names will be prefixed with this: github_list_repos, gmail_send_email.
	Name() string

	// DisplayName returns a human-readable name, e.g. "GitHub", "Gmail".
	DisplayName() string

	// AuthScheme returns the authentication method this platform uses.
	AuthScheme() AuthScheme

	// OAuthConfig returns the OAuth2 configuration (if AuthScheme is oauth2).
	// Returns nil for non-OAuth platforms.
	OAuthConfig() *OAuthConfig

	// Tools returns the MCP tool definitions this adapter provides.
	// Each tool name should be prefixed with the platform name: "github_list_repos".
	Tools() []mcp.Tool

	// Execute runs a tool call with the user's access token.
	// toolName is the unprefixed name (e.g. "list_repos", not "github_list_repos").
	Execute(ctx context.Context, toolName string, args map[string]any, accessToken string) (*mcp.CallToolResult, error)
}

// ToolHandler wraps an adapter's Execute method for use with the MCP server.
// The MCP server layer calls this, injecting the correct user token.
type ToolHandler func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// ComposioAuthProvider is implemented by adapters whose OAuth is managed by Composio.
// The auth manager uses this interface to delegate connection flows to Composio.
type ComposioAuthProvider interface {
	PlatformAdapter
	AuthConfigID() string
	ComposioClient() *composio.Client
	ComposioAppName() string
}
