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

// InitiateConnectionRequest is the input for initiating a user connection.
type InitiateConnectionRequest struct {
	UserID       string `json:"user_id"`
	AuthConfigID string `json:"auth_config_id"`
	RedirectURL  string `json:"redirect_url,omitempty"`
}

// InitiateConnectionResponse is returned after starting OAuth.
type InitiateConnectionResponse struct {
	RedirectURL        string `json:"redirectUrl"`
	ConnectionStatus   string `json:"connectionStatus"` // INITIATED, ACTIVE, etc.
	ConnectedAccountID string `json:"connectedAccountId"`
}

// InitiateConnection starts an OAuth flow for a user on a platform.
func (c *Client) InitiateConnection(ctx context.Context, req InitiateConnectionRequest) (*InitiateConnectionResponse, error) {
	var resp InitiateConnectionResponse
	err := c.post(ctx, "/v1/connectedAccounts", req, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// ConnectedAccount represents a user's connection to a platform in Composio.
type ConnectedAccount struct {
	ID      string `json:"id"`
	Status  string `json:"status"`  // ACTIVE, INITIATED, EXPIRED, FAILED
	AppName string `json:"appName"`
}

// GetConnection retrieves a connected account by ID.
func (c *Client) GetConnection(ctx context.Context, connectedAccountID string) (*ConnectedAccount, error) {
	var resp ConnectedAccount
	err := c.get(ctx, "/v1/connectedAccounts/"+connectedAccountID, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListConnectionsResponse wraps the list of connected accounts.
type ListConnectionsResponse struct {
	Items []ConnectedAccount `json:"items"`
}

// ListConnections lists active connections for a user, optionally filtered by app.
func (c *Client) ListConnections(ctx context.Context, userID, appName string) ([]ConnectedAccount, error) {
	params := url.Values{"user_id": {userID}, "status": {"ACTIVE"}}
	if appName != "" {
		params.Set("app_name", appName)
	}
	var resp ListConnectionsResponse
	err := c.get(ctx, "/v1/connectedAccounts?"+params.Encode(), &resp)
	if err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

// Action represents a Composio action definition (from listing API).
type Action struct {
	Name        string            `json:"name"`        // e.g. "GMAIL_SEND_EMAIL"
	DisplayName string            `json:"displayName"` // e.g. "Send Email"
	Description string            `json:"description"`
	AppName     string            `json:"appName"`
	Parameters  ActionParameters  `json:"parameters"`
}

// ActionParameters holds the JSON Schema for an action's input.
type ActionParameters struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Required   []string       `json:"required"`
}

// ListActions fetches available actions for an app from Composio.
func (c *Client) ListActions(ctx context.Context, appName string, limit int) ([]Action, error) {
	if limit <= 0 {
		limit = 30
	}
	path := fmt.Sprintf("/v2/actions?apps=%s&limit=%d", url.QueryEscape(appName), limit)
	var resp struct {
		Items []Action `json:"items"`
	}
	err := c.get(ctx, path, &resp)
	if err != nil {
		return nil, err
	}
	log.Printf("[Composio] ListActions(%s): got %d raw actions", appName, len(resp.Items))
	return resp.Items, nil
}

// ExecuteActionRequest is the input for executing a Composio action.
type ExecuteActionRequest struct {
	ConnectedAccountID string         `json:"connectedAccountId"`
	Input              map[string]any `json:"input,omitempty"`
	EntityID           string         `json:"entityId,omitempty"`
}

// ExecuteActionResponse is the output of a Composio action execution.
type ExecuteActionResponse struct {
	Data       any    `json:"data"`
	Error      string `json:"error,omitempty"`
	Successful bool   `json:"successfull"` // Composio API uses this spelling
}

// ExecuteAction runs an action via Composio.
func (c *Client) ExecuteAction(ctx context.Context, actionID string, req ExecuteActionRequest) (*ExecuteActionResponse, error) {
	var resp ExecuteActionResponse
	err := c.post(ctx, "/v2/actions/"+actionID+"/execute", req, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
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
