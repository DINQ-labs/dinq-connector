// Package mcpserver bridges platform adapters into a single MCP endpoint.
//
// Instead of registering 200+ individual tools, we expose 4 meta tools:
//   - connector_list_accounts: check which platforms are connected
//   - connector_connect: initiate OAuth for a platform
//   - connector_discover_tools: discover available actions for a platform
//   - connector_execute: execute a specific action on a platform
//
// This "lazy tool discovery" pattern keeps the tool count minimal and lets
// the LLM discover and call platform actions on demand.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/DINQ-labs/dinq-connector/internal/adapter"
	"github.com/DINQ-labs/dinq-connector/internal/auth"
)

// Config holds the MCP server configuration.
type Config struct {
	Port     int
	Endpoint string // default "/mcp"
}

// Server wraps the MCP server with adapter + auth integration.
type Server struct {
	mcpServer *server.MCPServer
	registry  *adapter.Registry
	authMgr   *auth.Manager
	config    Config
}

// New creates a new MCP server backed by the adapter registry.
func New(registry *adapter.Registry, authMgr *auth.Manager, cfg Config) *Server {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "/mcp"
	}
	if cfg.Port == 0 {
		cfg.Port = 8091
	}

	mcpSrv := server.NewMCPServer(
		"dinq-connector",
		"0.2.0",
		server.WithToolCapabilities(true),
	)

	s := &Server{
		mcpServer: mcpSrv,
		registry:  registry,
		authMgr:   authMgr,
		config:    cfg,
	}

	s.registerMetaTools()
	return s
}

// registerMetaTools registers only the 4 meta tools — no per-platform tools.
func (s *Server) registerMetaTools() {
	// Build platform list for descriptions
	var platformNames []string
	for _, a := range s.registry.List() {
		platformNames = append(platformNames, a.Name())
	}
	platformList := strings.Join(platformNames, ", ")

	s.mcpServer.AddTool(
		mcp.NewTool("connector_list_accounts",
			mcp.WithDescription(
				"Check which platforms the user has connected. "+
					"Call this FIRST before any platform action — never assume a platform is connected. "+
					"Status meanings: 'active'=ready to use; 'initiated'=OAuth started but user hasn't completed it yet (send them the auth link again); 'expired'=token expired, call connector_connect to re-authorize. "+
					"Available platforms: "+platformList,
			),
			mcp.WithString("user_id", mcp.Required(), mcp.Description("User ID")),
		),
		s.handleListAccounts,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("connector_connect",
			mcp.WithDescription(
				"Initiate OAuth authorization for a platform. "+
					"Call when: (1) connector_list_accounts shows platform is not connected or 'initiated'; (2) connector_execute returns 'User not connected'. "+
					"Returns a URL — present it to the user exactly as: {{AUTH_LINK[platform=xxx]<EXACT_URL>}}. "+
					"NEVER modify, shorten, or guess the URL. If this tool fails, tell the user it failed. "+
					"After the user visits the URL and authorizes, the platform status becomes 'active'. "+
					"Platforms: "+platformList,
			),
			mcp.WithString("user_id", mcp.Required(), mcp.Description("User ID")),
			mcp.WithString("platform", mcp.Required(), mcp.Description("Platform to connect: "+platformList)),
			mcp.WithString("callback_url", mcp.Description("URL to redirect after authorization")),
		),
		s.handleConnect,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("connector_discover_tools",
			mcp.WithDescription(
				"List all available actions for a platform, with parameter schemas and descriptions. "+
					"MUST be called before connector_execute — do not guess action names or params. "+
					"Call once per platform per conversation and reuse the result; do not call repeatedly for the same platform. "+
					"Platforms: "+platformList,
			),
			mcp.WithString("platform", mcp.Required(), mcp.Description("Platform to discover tools for: "+platformList)),
		),
		s.handleDiscoverTools,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("connector_execute",
			mcp.WithDescription(
				"Execute an action on a connected platform. "+
					"Prerequisites: (1) verify user is connected via connector_list_accounts; (2) call connector_discover_tools first to get valid action names and param schemas. "+
					"WRITE actions (send, post, create, delete, update, reply) are irreversible — confirm with the user before calling. "+
					"READ actions (list, get, search) are safe to call without confirmation. "+
					"On 'User not connected' error: call connector_connect. "+
					"On 'terminated' or network error: retry once. "+
					"Platforms: "+platformList,
			),
			mcp.WithString("user_id", mcp.Required(), mcp.Description("User ID")),
			mcp.WithString("platform", mcp.Required(), mcp.Description("Platform name: "+platformList)),
			mcp.WithString("action", mcp.Required(), mcp.Description("Action name exactly as returned by connector_discover_tools")),
			mcp.WithObject("params", mcp.Description("Action parameters matching the schema from connector_discover_tools")),
		),
		s.handleExecute,
	)

	log.Printf("[MCP] Registered 4 meta tools (platforms: %s)", platformList)
}

// handleListAccounts returns connected account status for a user.
func (s *Server) handleListAccounts(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	userID, ok := req.GetArguments()["user_id"].(string)
	if !ok || userID == "" {
		return mcp.NewToolResultError("user_id is required"), nil
	}
	log.Printf("[MCP] connector_list_accounts user=%s", userID)

	accounts, err := s.authMgr.ListAccounts(ctx, userID)
	if err != nil {
		return mcp.NewToolResultError("failed to list accounts: " + err.Error()), nil
	}

	if len(accounts) == 0 {
		var platforms []string
		for _, a := range s.registry.List() {
			platforms = append(platforms, a.Name())
		}
		return mcp.NewToolResultText(fmt.Sprintf(
			"No connected accounts. Available platforms: %v\nUse connector_connect to connect.",
			platforms,
		)), nil
	}

	data, _ := json.MarshalIndent(accounts, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

// handleConnect initiates an OAuth flow.
func (s *Server) handleConnect(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	userID, _ := args["user_id"].(string)
	platform, _ := args["platform"].(string)
	callbackURL, _ := args["callback_url"].(string)

	platform = adapter.ResolveName(strings.ToLower(platform))

	if userID == "" || platform == "" {
		return mcp.NewToolResultError("user_id and platform are required"), nil
	}
	log.Printf("[MCP] connector_connect user=%s platform=%s", userID, platform)

	redirectURL, err := s.authMgr.InitiateOAuth(ctx, userID, platform, callbackURL)
	if err != nil {
		return mcp.NewToolResultError("failed to initiate OAuth: " + err.Error()), nil
	}

	displayName := platform
	if a := s.registry.Get(platform); a != nil {
		displayName = a.DisplayName()
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"Please visit this URL to connect your %s account:\n%s\n\nAfter authorization, you can use %s tools.",
		displayName,
		redirectURL,
		platform,
	)), nil
}

// toolInfo is the JSON structure returned by connector_discover_tools.
type toolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// handleDiscoverTools returns available actions for a platform with schemas.
func (s *Server) handleDiscoverTools(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	platform, _ := req.GetArguments()["platform"].(string)
	platform = adapter.ResolveName(strings.ToLower(platform))

	if platform == "" {
		return mcp.NewToolResultError("platform is required"), nil
	}

	a := s.registry.Get(platform)
	if a == nil {
		return mcp.NewToolResultError(fmt.Sprintf("unknown platform: %s", platform)), nil
	}
	log.Printf("[MCP] connector_discover_tools platform=%s tools=%d", platform, len(a.Tools()))

	tools := a.Tools()
	prefix := a.Name() + "_"

	result := make([]toolInfo, 0, len(tools))
	for _, t := range tools {
		// Strip platform prefix: "gmail_send_email" → "send_email"
		actionName := strings.TrimPrefix(t.Name, prefix)

		// Strip user_id from parameters (injected by us, not by LLM in this mode)
		params := t.InputSchema
		if params.Properties != nil {
			cleaned := make(map[string]any, len(params.Properties))
			for k, v := range params.Properties {
				if k != "user_id" {
					cleaned[k] = v
				}
			}
			params.Properties = cleaned
		}
		var cleanedRequired []string
		for _, r := range params.Required {
			if r != "user_id" {
				cleanedRequired = append(cleanedRequired, r)
			}
		}
		params.Required = cleanedRequired

		result = append(result, toolInfo{
			Name:        actionName,
			Description: t.Description,
			Parameters:  params,
		})
	}

	data, _ := json.MarshalIndent(map[string]any{
		"platform":    platform,
		"displayName": a.DisplayName(),
		"toolCount":   len(result),
		"tools":       result,
	}, "", "  ")

	return mcp.NewToolResultText(string(data)), nil
}

// handleExecute runs a specific action on a platform.
func (s *Server) handleExecute(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	userID, _ := args["user_id"].(string)
	platform, _ := args["platform"].(string)
	action, _ := args["action"].(string)

	platform = adapter.ResolveName(strings.ToLower(platform))

	if userID == "" || platform == "" || action == "" {
		return mcp.NewToolResultError("user_id, platform, and action are required"), nil
	}

	// Extract params (nested object)
	params := make(map[string]any)
	if p, ok := args["params"].(map[string]any); ok {
		params = p
	}
	log.Printf("[MCP] connector_execute user=%s platform=%s action=%s params=%v", userID, platform, action, params)

	a := s.registry.Get(platform)
	if a == nil {
		log.Printf("[MCP] connector_execute FAIL: unknown platform %s", platform)
		return mcp.NewToolResultError(fmt.Sprintf("unknown platform: %s", platform)), nil
	}

	// Bot-token and dinq-internal adapters use server/user-id directly — no per-user OAuth needed.
	var token string
	if a.AuthScheme() != adapter.AuthBotToken && a.AuthScheme() != adapter.AuthDinqInternal {
		var err error
		token, err = s.authMgr.GetActiveToken(ctx, userID, platform)
		if err != nil {
			log.Printf("[MCP] connector_execute FAIL: user %s not connected to %s", userID, platform)
			return mcp.NewToolResultError(fmt.Sprintf(
				"User not connected to %s. Use connector_connect to initiate authorization.",
				a.DisplayName(),
			)), nil
		}
	}

	result, execErr := a.Execute(ctx, action, params, token, userID)
	if result != nil && len(result.Content) > 0 {
		if txt, ok := result.Content[0].(mcp.TextContent); ok {
			preview := txt.Text
			if len(preview) > 300 {
				preview = preview[:300] + "..."
			}
			log.Printf("[MCP] connector_execute DONE platform=%s action=%s isError=%v result=%s", platform, action, result.IsError, preview)
		}
	}
	return result, execErr
}

// NewHandler returns an http.Handler for embedding in a shared mux.
// This allows MCP and HTTP API to share the same port.
func NewHandler(registry *adapter.Registry, authMgr *auth.Manager, endpoint string) http.Handler {
	s := &Server{
		mcpServer: server.NewMCPServer("dinq-connector", "0.2.0", server.WithToolCapabilities(true)),
		registry:  registry,
		authMgr:   authMgr,
		config:    Config{Endpoint: endpoint},
	}
	s.registerMetaTools()
	sh := server.NewStreamableHTTPServer(s.mcpServer, server.WithEndpointPath(endpoint))
	log.Printf("[MCP] dinq-connector handler at %s", endpoint)
	return sh
}

// Start starts the MCP server on the configured port (standalone mode).
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.config.Port)
	httpServer := server.NewStreamableHTTPServer(s.mcpServer,
		server.WithEndpointPath(s.config.Endpoint),
	)

	log.Printf("[MCP] dinq-connector listening on %s%s", addr, s.config.Endpoint)
	return httpServer.Start(addr)
}
