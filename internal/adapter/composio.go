package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/DINQ-labs/dinq-connector/internal/composio"
)

// ExtraTool is an additional tool attached to a ComposioAdapter at runtime.
// Execute is a closure that calls an external API (e.g. Apify) directly,
// independently of the user's OAuth token for this platform.
type ExtraTool struct {
	LocalName   string
	Description string
	Schema      mcp.ToolInputSchema
	Execute     func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error)
}

// ComposioToolMapping maps a local tool name to a Composio tool slug + version.
type ComposioToolMapping struct {
	LocalName   string // e.g. "send_message" (without platform prefix)
	Slug        string // e.g. "SLACK_SEND_MESSAGE"
	Version     string // e.g. "20260309_00" — latest at discovery time
	Description string
	InputSchema mcp.ToolInputSchema
}

// ComposioAdapterConfig configures a Composio-backed platform adapter.
type ComposioAdapterConfig struct {
	Platform     string                // "twitter", "linkedin"
	DisplayName_ string                // "Twitter", "LinkedIn"
	AuthConfigID string                // Composio auth config ID (from dashboard, ac_xxx)
	AppName      string                // Composio app name, e.g. "twitter", "linkedin"
	Tools_       []ComposioToolMapping // Tool definitions
	ExtraTools   []ExtraTool           // Non-Composio tools (e.g. Apify search)
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

// NewDynamicComposioAdapter discovers tools via the v3 API at startup.
// Uses tags=important for platforms with curated tools, falls back to all tools otherwise.
// Each tool is fetched individually to resolve the latest version.
func NewDynamicComposioAdapter(ctx context.Context, client *composio.Client, platform, displayName, authConfigID, appName string, excludeTools ...string) (*ComposioAdapter, error) {
	listed, err := client.ListToolsV3(ctx, appName, true)
	if err != nil {
		return nil, fmt.Errorf("list %s tools: %w", appName, err)
	}
	if len(listed) < 3 {
		all, err2 := client.ListToolsV3(ctx, appName, false)
		if err2 == nil && len(all) > len(listed) {
			listed = all
		}
	}

	prefix := strings.ToUpper(appName) + "_"

	// Fetch each tool individually (parallel) to get latest version + full schema.
	type result struct {
		idx  int
		tool *composio.ToolV3
		err  error
	}
	ch := make(chan result, len(listed))
	var wg sync.WaitGroup
	for i, t := range listed {
		wg.Add(1)
		go func(idx int, slug string) {
			defer wg.Done()
			full, fetchErr := client.GetToolV3(ctx, slug)
			ch <- result{idx: idx, tool: full, err: fetchErr}
		}(i, t.Slug)
	}
	go func() { wg.Wait(); close(ch) }()

	excluded := make(map[string]struct{}, len(excludeTools))
	for _, e := range excludeTools {
		excluded[strings.ToLower(e)] = struct{}{}
	}

	fetched := make([]*composio.ToolV3, len(listed))
	for r := range ch {
		if r.err != nil {
			log.Printf("[Composio] Failed to fetch %s: %v", listed[r.idx].Slug, r.err)
			continue
		}
		fetched[r.idx] = r.tool
	}

	tools := make([]ComposioToolMapping, 0, len(fetched))
	for i, t := range fetched {
		if t == nil {
			t = &listed[i]
		}

		localName := strings.ToLower(strings.TrimPrefix(t.Slug, prefix))
		if localName == "" || localName == strings.ToLower(t.Slug) {
			localName = strings.ToLower(t.Slug)
		}

		if _, skip := excluded[localName]; skip {
			continue
		}

		props := t.InputParameters.Properties
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
		for _, r := range t.InputParameters.Required {
			if r != "user_id" {
				required = append(required, r)
			}
		}

		desc := t.Description
		if desc == "" {
			desc = t.Name
		}

		tools = append(tools, ComposioToolMapping{
			LocalName:   localName,
			Slug:        t.Slug,
			Version:     t.LatestVersion(),
			Description: desc,
			InputSchema: mcp.ToolInputSchema{
				Type:       "object",
				Properties: filtered,
				Required:   required,
			},
		})
	}

	if len(tools) == 0 {
		return nil, fmt.Errorf("no tools found for %s on Composio", appName)
	}

	log.Printf("[Composio] Discovered %d tools for %s (latest versions)", len(tools), displayName)

	return NewComposioAdapter(client, ComposioAdapterConfig{
		Platform:     platform,
		DisplayName_: displayName,
		AuthConfigID: authConfigID,
		AppName:      appName,
		Tools_:       tools,
	}), nil
}

func (a *ComposioAdapter) Name() string              { return a.config.Platform }
func (a *ComposioAdapter) DisplayName() string       { return a.config.DisplayName_ }
func (a *ComposioAdapter) AuthScheme() AuthScheme    { return AuthComposio }
func (a *ComposioAdapter) OAuthConfig() *OAuthConfig { return nil }

// ComposioAuthProvider methods (used by auth manager)
func (a *ComposioAdapter) AuthConfigID() string             { return a.config.AuthConfigID }
func (a *ComposioAdapter) ComposioClient() *composio.Client { return a.client }
func (a *ComposioAdapter) ComposioAppName() string          { return a.config.AppName }

// AddExtraTool attaches an additional non-Composio tool to this adapter.
func (a *ComposioAdapter) AddExtraTool(t ExtraTool) {
	a.config.ExtraTools = append(a.config.ExtraTools, t)
}

// Tools returns MCP tool definitions prefixed with the platform name.
func (a *ComposioAdapter) Tools() []mcp.Tool {
	tools := make([]mcp.Tool, 0, len(a.config.Tools_)+len(a.config.ExtraTools))
	for _, t := range a.config.Tools_ {
		tools = append(tools, mcp.Tool{
			Name:        fmt.Sprintf("%s_%s", a.config.Platform, t.LocalName),
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	for _, t := range a.config.ExtraTools {
		tools = append(tools, mcp.Tool{
			Name:        fmt.Sprintf("%s_%s", a.config.Platform, t.LocalName),
			Description: t.Description,
			InputSchema: t.Schema,
		})
	}
	return tools
}

// Execute runs a tool via the Composio v3 API, or an extra tool directly.
func (a *ComposioAdapter) Execute(ctx context.Context, toolName string, args map[string]any, accessToken, userID string) (*mcp.CallToolResult, error) {
	for _, t := range a.config.ExtraTools {
		if t.LocalName == toolName {
			return t.Execute(ctx, args)
		}
	}

	var mapping *ComposioToolMapping
	for i := range a.config.Tools_ {
		if a.config.Tools_[i].LocalName == toolName {
			mapping = &a.config.Tools_[i]
			break
		}
	}
	if mapping == nil {
		return mcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", toolName)), nil
	}

	if args == nil {
		args = map[string]any{}
	}

	resp, err := a.client.ExecuteTool(ctx, mapping.Slug, composio.ExecuteToolRequest{
		UserID:    userID,
		Arguments: args,
		Version:   mapping.Version,
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Composio error: %s", err)), nil
	}
	if !resp.Successful {
		errMsg := resp.Error
		if errMsg == "" {
			errMsg = "action failed"
		}
		return mcp.NewToolResultError(errMsg), nil
	}

	cleaned := stripBulkFields(resp.Data)
	data, err := json.Marshal(cleaned)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("%v", resp.Data)), nil
	}

	const maxResponseBytes = 12_000
	if len(data) > maxResponseBytes {
		data = append(data[:maxResponseBytes], []byte("...(truncated)")...)
	}
	return mcp.NewToolResultText(string(data)), nil
}

// stripBulkFields recursively removes fields that bloat responses
// (e.g. base64 attachment IDs, raw HTML bodies) to keep tool results
// small enough for LLM context windows.
func stripBulkFields(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, child := range val {
			switch k {
			case "attachmentId", "raw", "htmlBody":
				continue
			case "attachmentList":
				if list, ok := child.([]any); ok {
					slim := make([]any, 0, len(list))
					for _, item := range list {
						if m, ok := item.(map[string]any); ok {
							slim = append(slim, map[string]any{
								"filename": m["filename"],
								"mimeType": m["mimeType"],
							})
						}
					}
					out[k] = slim
				}
			default:
				out[k] = stripBulkFields(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, child := range val {
			out[i] = stripBulkFields(child)
		}
		return out
	default:
		return v
	}
}
