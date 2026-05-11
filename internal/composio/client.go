// Package composio provides a client for the Composio REST API.
// Composio handles OAuth + token management for third-party platforms
// (Twitter, LinkedIn, etc.), so we don't need to manage tokens ourselves.
package composio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

const defaultBaseURL = "https://backend.composio.dev/api"

// Client is a thin HTTP wrapper around the Composio REST API.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// NewClient creates a new Composio API client.
func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: defaultBaseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// ---------------------------------------------------------------------------
// Connected Accounts
// ---------------------------------------------------------------------------

// InitiateLinkRequest is the input for creating an auth link session (v3 API).
type InitiateLinkRequest struct {
	AuthConfigID string `json:"auth_config_id"` // ac_xxx from dashboard
	UserID       string `json:"user_id"`
	CallbackURL  string `json:"callback_url,omitempty"`
}

// InitiateLinkResponse is returned after creating an auth link session.
type InitiateLinkResponse struct {
	LinkToken          string `json:"link_token"`
	RedirectURL        string `json:"redirect_url"`
	ExpiresAt          string `json:"expires_at"`
	ConnectedAccountID string `json:"connected_account_id"`
}

// InitiateLink creates an auth link session via the v3 API.
// Returns a redirect URL the user should visit to complete OAuth.
func (c *Client) InitiateLink(ctx context.Context, req InitiateLinkRequest) (*InitiateLinkResponse, error) {
	var resp InitiateLinkResponse
	if err := c.post(ctx, "/v3/connected_accounts/link", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Integration represents a Composio integration (auth config).
type Integration struct {
	ID      string `json:"id"`      // UUID — used as integrationId in API calls
	AppName string `json:"appName"` // e.g. "linkedin"
	Name    string `json:"name"`
}

// GetIntegrations returns all integrations, keyed by appName.
func (c *Client) GetIntegrations(ctx context.Context) (map[string]Integration, error) {
	var resp struct {
		Items []Integration `json:"items"`
	}
	if err := c.get(ctx, "/v1/integrations?pageSize=100", &resp); err != nil {
		return nil, err
	}
	m := make(map[string]Integration, len(resp.Items))
	for _, i := range resp.Items {
		m[i.AppName] = i
	}
	return m, nil
}

// ConnectedAccount represents a user's connection to a platform in Composio.
type ConnectedAccount struct {
	ID      string `json:"id"`
	Status  string `json:"status"` // ACTIVE, INITIATED, EXPIRED, FAILED
	AppName string `json:"appName"`
	// Data contains the raw OAuth token payload (v3 API). We use id_token
	// (OIDC JWT) or access_token to resolve the authenticated user's email,
	// since Composio's response does not expose an account email directly.
	Data struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
	} `json:"data"`
}

// GetConnection retrieves a connected account by ID (v3 API).
func (c *Client) GetConnection(ctx context.Context, connectedAccountID string) (*ConnectedAccount, error) {
	var resp ConnectedAccount
	if err := c.get(ctx, "/v3/connected_accounts/"+connectedAccountID, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListConnectionsResponse wraps the list of connected accounts.
type ListConnectionsResponse struct {
	Items []ConnectedAccount `json:"items"`
}

// ListConnections lists active connections for a user, optionally filtered by toolkit (v3 API).
func (c *Client) ListConnections(ctx context.Context, userID, appName string) ([]ConnectedAccount, error) {
	params := url.Values{"user_ids": {userID}, "statuses": {"ACTIVE"}}
	if appName != "" {
		params.Set("toolkit_slugs", appName)
	}
	var resp ListConnectionsResponse
	if err := c.get(ctx, "/v3/connected_accounts?"+params.Encode(), &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// ---------------------------------------------------------------------------
// Tools (v3 API)
// ---------------------------------------------------------------------------

// ToolV3 represents a tool definition from the v3 tools API.
type ToolV3 struct {
	Slug              string           `json:"slug"` // e.g. "SLACK_SEND_MESSAGE"
	Name              string           `json:"name"` // e.g. "Send message"
	Description       string           `json:"description"`
	Version           string           `json:"version"`            // default version from list
	AvailableVersions []string         `json:"available_versions"` // newest first
	InputParameters   ActionParameters `json:"input_parameters"`
	IsDeprecated      bool             `json:"is_deprecated"`
	Deprecated        any              `json:"deprecated"` // bool or object depending on API response
}

// LatestVersion returns the newest available version, falling back to the default.
func (t ToolV3) LatestVersion() string {
	if len(t.AvailableVersions) > 0 {
		return t.AvailableVersions[0]
	}
	return t.Version
}

// ActionParameters holds the JSON Schema for a tool's input.
type ActionParameters struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Required   []string       `json:"required"`
}

// ListToolsV3 lists tools for an app via the v3 API.
// If importantOnly is true, only tools tagged "important" are returned.
func (c *Client) ListToolsV3(ctx context.Context, appName string, importantOnly bool) ([]ToolV3, error) {
	params := url.Values{
		"toolkit_slug": {appName},
		"limit":        {"200"},
	}
	if importantOnly {
		params.Set("tags", "important")
	}
	var resp struct {
		Items []ToolV3 `json:"items"`
	}
	if err := c.get(ctx, "/v3/tools?"+params.Encode(), &resp); err != nil {
		return nil, err
	}
	log.Printf("[Composio] ListToolsV3(%s, important=%v): %d tools", appName, importantOnly, len(resp.Items))
	return resp.Items, nil
}

// GetToolV3 fetches a single tool by slug, including full available_versions.
func (c *Client) GetToolV3(ctx context.Context, slug string) (*ToolV3, error) {
	var resp ToolV3
	if err := c.get(ctx, "/v3/tools/"+url.PathEscape(slug), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ExecuteToolRequest is the input for the v3 tools execute API.
type ExecuteToolRequest struct {
	UserID    string         `json:"user_id"`
	Arguments map[string]any `json:"arguments"`
	Version   string         `json:"version,omitempty"`
}

// ExecuteActionResponse is the unified output returned after tool execution.
type ExecuteActionResponse struct {
	Data       any    `json:"data"`
	Error      string `json:"error,omitempty"`
	Successful bool   `json:"successful"`
}

// ExecuteTool runs a tool via the v3 tools execute API.
func (c *Client) ExecuteTool(ctx context.Context, slug string, req ExecuteToolRequest) (*ExecuteActionResponse, error) {
	bodyData, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		c.baseURL+"/v3/tools/execute/"+url.PathEscape(slug),
		bytes.NewReader(bodyData),
	)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("composio request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("composio read body: %w", err)
	}

	log.Printf("[Composio] ExecuteTool %s status=%d body=%s", slug, resp.StatusCode, truncateLog(string(raw), 500))

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("composio API error %d: %s", resp.StatusCode, string(raw))
	}

	var result struct {
		Data       any  `json:"data"`
		Successful bool `json:"successful"`
		Error      any  `json:"error"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("composio decode: %w", err)
	}
	var errStr string
	if s, ok := result.Error.(string); ok {
		errStr = s
	}
	return &ExecuteActionResponse{
		Data:       result.Data,
		Error:      errStr,
		Successful: result.Successful,
	}, nil
}

func truncateLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", c.apiKey)
	return c.do(req, out)
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("composio request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("composio read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("composio API error %d: %s", resp.StatusCode, string(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("composio decode: %w", err)
		}
	}
	return nil
}
