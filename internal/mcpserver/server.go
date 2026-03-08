// Package mcpserver bridges platform adapters into a single MCP endpoint.
// All adapter tools are registered with the MCP server, and each tool call
// automatically resolves the user's token from the connected accounts store.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

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
	mcpServer  *server.MCPServer
	registry   *adapter.Registry
	authMgr    *auth.Manager
	config     Config
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
		"0.1.0",
		server.WithToolCapabilities(true),
	)

	s := &Server{
		mcpServer: mcpSrv,
		registry:  registry,
		authMgr:   authMgr,
		config:    cfg,
	}

	s.registerTools()
	return s
}

// registerTools registers all adapter tools with the MCP server.
// Each tool gets a handler that resolves user_id → token → adapter.Execute().
func (s *Server) registerTools() {
	for _, a := range s.registry.List() {
		for _, tool := range a.Tools() {
			// Inject user_id parameter into every tool
			tool = injectUserIDParam(tool)
			s.mcpServer.AddTool(tool, s.makeHandler(a))
			log.Printf("[MCP] Registered tool: %s", tool.Name)
		}
	}

	// Meta tool: list connected platforms for a user
	s.mcpServer.AddTool(
		mcp.NewTool("connector_list_accounts",
			mcp.WithDescription("List all connected platform accounts for a user. Shows which platforms are connected and their status."),
			mcp.WithString("user_id", mcp.Required(), mcp.Description("User ID")),
		),
		s.handleListAccounts,
	)

	// Meta tool: initiate platform connection
	s.mcpServer.AddTool(
		mcp.NewTool("connector_connect",
			mcp.WithDescription("Start connecting a user to a platform. Returns an authorization URL the user must visit to grant access."),
			mcp.WithString("user_id", mcp.Required(), mcp.Description("User ID")),
			mcp.WithString("platform", mcp.Required(), mcp.Description("Platform to connect: github, twitter, linkedin, google, slack, notion")),
			mcp.WithString("callback_url", mcp.Description("URL to redirect after authorization")),
		),
		s.handleConnect,
	)
}

// makeHandler creates an MCP tool handler that resolves user token and delegates to adapter.
func (s *Server) makeHandler(a adapter.PlatformAdapter) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract user_id from args
		userID, ok := req.GetArguments()["user_id"].(string)
		if !ok || userID == "" {
			return mcp.NewToolResultError("user_id is required"), nil
		}

		// Get active token for this user+platform
		token, err := s.authMgr.GetActiveToken(ctx, userID, a.Name())
		if err != nil {
			// User not connected — return helpful message
			return mcp.NewToolResultError(fmt.Sprintf(
				"User not connected to %s. Use connector_connect tool to initiate authorization.",
				a.DisplayName(),
			)), nil
		}

		// Strip platform prefix from tool name to get the adapter-local name
		_, localName, found := s.registry.FindAdapter(req.Params.Name)
		if !found {
			return mcp.NewToolResultError("unknown tool"), nil
		}

		// Build args map without user_id (adapter doesn't need it)
		args := make(map[string]any)
		for k, v := range req.GetArguments() {
			if k != "user_id" {
				args[k] = v
			}
		}

		return a.Execute(ctx, localName, args, token)
	}
}

// handleListAccounts returns connected account status for a user.
func (s *Server) handleListAccounts(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	userID, ok := req.GetArguments()["user_id"].(string)
	if !ok || userID == "" {
		return mcp.NewToolResultError("user_id is required"), nil
	}

	accounts, err := s.authMgr.ListAccounts(ctx, userID)
	if err != nil {
		return mcp.NewToolResultError("failed to list accounts: " + err.Error()), nil
	}

	if len(accounts) == 0 {
		// List available platforms
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

	if userID == "" || platform == "" {
		return mcp.NewToolResultError("user_id and platform are required"), nil
	}

	redirectURL, err := s.authMgr.InitiateOAuth(ctx, userID, platform, callbackURL)
	if err != nil {
		return mcp.NewToolResultError("failed to initiate OAuth: " + err.Error()), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"Please visit this URL to connect your %s account:\n%s\n\nAfter authorization, you can use %s tools.",
		s.registry.Get(platform).DisplayName(),
		redirectURL,
		platform,
	)), nil
}

// Start starts the MCP server on the configured port.
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.config.Port)
	httpServer := server.NewStreamableHTTPServer(s.mcpServer,
		server.WithEndpointPath(s.config.Endpoint),
	)

	log.Printf("[MCP] dinq-connector listening on %s%s", addr, s.config.Endpoint)
	return httpServer.Start(addr)
}

// injectUserIDParam adds a user_id parameter to a tool definition.
func injectUserIDParam(tool mcp.Tool) mcp.Tool {
	schema := tool.InputSchema
	if schema.Properties == nil {
		schema.Properties = make(map[string]any)
	}
	schema.Properties["user_id"] = map[string]any{
		"type":        "string",
		"description": "User ID for authenticated access",
	}

	// Add user_id to required
	schema.Required = append(schema.Required, "user_id")
	tool.InputSchema = schema

	return tool
}
