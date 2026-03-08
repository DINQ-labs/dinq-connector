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
	IntegrationID string         `json:"integrationId"`      // UUID from /v1/integrations
	EntityID      string         `json:"entityId,omitempty"` // user entity; set to our user_id
	Data          map[string]any `json:"data"`               // empty object required
	RedirectURI   string         `json:"redirectUri,omitempty"`
}

// InitiateConnectionResponse is returned after starting OAuth.
type InitiateConnectionResponse struct {
	RedirectURL        string `json:"redirectUrl"`
	ConnectionStatus   string `json:"connectionStatus"` // INITIATED, ACTIVE, etc.
	ConnectedAccountID string `json:"connectedAccountId"`
}

// InitiateConnection starts an OAuth flow for a user on a platform.
func (c *Client) InitiateConnection(ctx context.Context, req InitiateConnectionRequest) (*InitiateConnectionResponse, error) {
	if req.Data == nil {
		req.Data = map[string]any{}
	}
	var resp InitiateConnectionResponse
	err := c.post(ctx, "/v1/connectedAccounts", req, &resp)
	if err != nil {
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
	Name        string           `json:"name"`        // e.g. "GMAIL_SEND_EMAIL"
	DisplayName string           `json:"displayName"` // e.g. "Send Email"
	Description string           `json:"description"`
	AppName     string           `json:"appName"`
	Version     string           `json:"version"` // e.g. "20260307_00"
	Parameters  ActionParameters `json:"parameters"`
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

// ExecuteToolRequest is the input for the Composio v3 tools execute API.
type ExecuteToolRequest struct {
	ConnectedAccountID string         `json:"connected_account_id,omitempty"`
	UserID             string         `json:"user_id,omitempty"`
	Arguments          map[string]any `json:"arguments"` // always send — v3 requires exactly one of 'text' or 'arguments'
	Version            string         `json:"version,omitempty"`
}

// ExecuteActionResponse is the unified output returned to callers after tool execution.
type ExecuteActionResponse struct {
	Data       any    `json:"data"`
	Error      string `json:"error,omitempty"`
	Successful bool   `json:"successful"`
}

// executeToolV3Response is the raw envelope from POST /v3/tools/execute/{slug}.
type executeToolV3Response struct {
	Data struct {
		Results []struct {
			Response struct {
				Successful bool   `json:"successful"`
				Data       any    `json:"data"`
				Error      string `json:"error,omitempty"`
			} `json:"response"`
			ToolSlug string `json:"tool_slug"`
		} `json:"results"`
	} `json:"data"`
	Successful bool `json:"successful"`
}

// ExecuteTool runs a tool via the Composio v3 tools API.
// connectedAccountID is the Composio connected account UUID.
func (c *Client) ExecuteTool(ctx context.Context, toolSlug string, req ExecuteToolRequest) (*ExecuteActionResponse, error) {
	var v3resp executeToolV3Response
	err := c.post(ctx, "/v3/tools/execute/"+url.PathEscape(toolSlug), req, &v3resp)
	if err != nil {
		return nil, err
	}
	if len(v3resp.Data.Results) == 0 {
		return nil, fmt.Errorf("composio v3: no results for %s", toolSlug)
	}
	r := v3resp.Data.Results[0]
	return &ExecuteActionResponse{
		Data:       r.Response.Data,
		Error:      r.Response.Error,
		Successful: r.Response.Successful,
	}, nil
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
