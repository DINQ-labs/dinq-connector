package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/DINQ-labs/dinq-connector/internal/composio"
)

// ComposioToolMapping maps a local tool name to a Composio action.
type ComposioToolMapping struct {
	LocalName      string              // e.g. "create_tweet" (without platform prefix)
	ComposioAction string              // e.g. "TWITTER_CREATION_OF_A_TWEET"
	Version        string              // e.g. "20260307_00" — used when executing
	Description    string
	InputSchema    mcp.ToolInputSchema
}

// ComposioAdapterConfig configures a Composio-backed platform adapter.
type ComposioAdapterConfig struct {
	Platform      string                // "twitter", "linkedin"
	DisplayName_  string                // "Twitter", "LinkedIn"
	AuthConfigID  string                // Composio auth config ID (from dashboard, ac_xxx)
	IntegrationID string                // Composio integration UUID (from /v1/integrations)
	AppName       string                // Composio app name, e.g. "twitter", "linkedin"
	Tools_        []ComposioToolMapping // Tool definitions
}

// ComposioAdapter implements PlatformAdapter using Composio as the backend.
// OAuth, token management, and API calls are all delegated to Composio.
type ComposioAdapter struct {
	config ComposioAdapterConfig
	client *composio.Client
}

// NewComposioAdapter creates a new Composio-backed adapter with static tool definitions.
func NewComposioAdapter(client *composio.Client, config ComposioAdapterConfig) *ComposioAdapter {
	return &ComposioAdapter{config: config, client: client}
}

// NewDynamicComposioAdapter creates an adapter by fetching tool definitions from Composio API at startup.
// No need to hardcode action IDs — tools are discovered automatically.
func NewDynamicComposioAdapter(ctx context.Context, client *composio.Client, platform, displayName, authConfigID, integrationID, appName string) (*ComposioAdapter, error) {
	actions, err := client.ListActions(ctx, appName, 30)
	if err != nil {
		return nil, fmt.Errorf("fetch %s actions: %w", appName, err)
	}

	tools := make([]ComposioToolMapping, 0, len(actions))
	prefix := strings.ToUpper(appName) + "_"

	for _, a := range actions {
		// Filter: only keep actions belonging to this app
		if !strings.EqualFold(a.AppName, appName) && !strings.HasPrefix(a.Name, prefix) {
			log.Printf("[Composio] Skipping unrelated action %s (app=%s) for %s", a.Name, a.AppName, displayName)
			continue
		}

		// "GMAIL_SEND_EMAIL" → "send_email"
		localName := strings.ToLower(strings.TrimPrefix(a.Name, prefix))
		if localName == "" || localName == strings.ToLower(a.Name) {
			// Prefix didn't match, use full name lowercased
			localName = strings.ToLower(a.Name)
		}

		// Remove user_id from properties (we inject it ourselves)
		props := a.Parameters.Properties
		if props == nil {
			props = map[string]any{}
		}
		filtered := make(map[string]any, len(props))
		var required []string
		for k, v := range props {
			if k == "user_id" {
				continue
			}
			filtered[k] = v
		}
		for _, r := range a.Parameters.Required {
			if r != "user_id" {
				required = append(required, r)
			}
		}

		desc := a.Description
		if desc == "" {
			desc = a.DisplayName
		}

		tools = append(tools, ComposioToolMapping{
			LocalName:      localName,
			ComposioAction: a.Name,
			Version:        a.Version,
			Description:    desc,
			InputSchema: mcp.ToolInputSchema{
				Type:       "object",
				Properties: filtered,
				Required:   required,
			},
		})
	}

	if len(tools) == 0 {
		return nil, fmt.Errorf("no actions found for %s on Composio", appName)
	}

	log.Printf("[Composio] Discovered %d actions for %s", len(tools), displayName)

	return NewComposioAdapter(client, ComposioAdapterConfig{
		Platform:      platform,
		DisplayName_:  displayName,
		AuthConfigID:  authConfigID,
		IntegrationID: integrationID,
		AppName:       appName,
		Tools_:        tools,
	}), nil
}

func (a *ComposioAdapter) Name() string        { return a.config.Platform }
func (a *ComposioAdapter) DisplayName() string  { return a.config.DisplayName_ }
func (a *ComposioAdapter) AuthScheme() AuthScheme { return AuthComposio }
func (a *ComposioAdapter) OAuthConfig() *OAuthConfig { return nil }

// ComposioAuthProvider methods (used by auth manager)
func (a *ComposioAdapter) AuthConfigID() string             { return a.config.AuthConfigID }
func (a *ComposioAdapter) IntegrationID() string            { return a.config.IntegrationID }
func (a *ComposioAdapter) ComposioClient() *composio.Client { return a.client }
func (a *ComposioAdapter) ComposioAppName() string          { return a.config.AppName }

// Tools returns MCP tool definitions prefixed with the platform name.
func (a *ComposioAdapter) Tools() []mcp.Tool {
	tools := make([]mcp.Tool, 0, len(a.config.Tools_))
	for _, t := range a.config.Tools_ {
		tools = append(tools, mcp.Tool{
			Name:        fmt.Sprintf("%s_%s", a.config.Platform, t.LocalName),
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return tools
}

// Execute runs a tool via the Composio API.
// For Composio adapters, the "accessToken" parameter is the Composio connectedAccountId.
func (a *ComposioAdapter) Execute(ctx context.Context, toolName string, args map[string]any, accessToken string) (*mcp.CallToolResult, error) {
	// Find the Composio action ID for this tool
	var actionID string
	for _, t := range a.config.Tools_ {
		if t.LocalName == toolName {
			actionID = t.ComposioAction
			break
		}
	}
	if actionID == "" {
		return mcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", toolName)), nil
	}

	resp, err := a.client.ExecuteAction(ctx, actionID, composio.ExecuteActionRequest{
		ConnectedAccountID: accessToken,
		Input:              args,
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Composio error: %s", err)), nil
	}
	// If LinkedIn API version is stale (426), retry with the latest toolkit version.
	if !resp.Successful && strings.Contains(resp.Error, "426") {
		resp, err = a.client.ExecuteAction(ctx, actionID, composio.ExecuteActionRequest{
			ConnectedAccountID: accessToken,
			Input:              args,
			Version:            "20260307_00",
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Composio error: %s", err)), nil
		}
	}
	if !resp.Successful {
		errMsg := resp.Error
		if errMsg == "" {
			errMsg = "action failed"
		}
		return mcp.NewToolResultError(errMsg), nil
	}

	data, err := json.Marshal(resp.Data)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("%v", resp.Data)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}
