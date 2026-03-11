// Package dinq implements the PlatformAdapter for the Dinq platform.
// Unlike OAuth adapters, Dinq users are always authenticated — their user_id
// is passed directly as X-User-ID to dinq-server internal APIs. No token needed.
package dinq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/DINQ-labs/dinq-connector/internal/adapter"
)

// Adapter implements adapter.PlatformAdapter for the Dinq platform.
type Adapter struct {
	serverURL string // e.g. "https://api.dinq.me"
}

func New(serverURL string) *Adapter {
	return &Adapter{serverURL: strings.TrimRight(serverURL, "/")}
}

func (a *Adapter) Name() string                      { return "dinq" }
func (a *Adapter) DisplayName() string               { return "Dinq" }
func (a *Adapter) AuthScheme() adapter.AuthScheme    { return adapter.AuthDinqInternal }
func (a *Adapter) OAuthConfig() *adapter.OAuthConfig { return nil }

func (a *Adapter) Tools() []mcp.Tool {
	return []mcp.Tool{
		mcp.NewTool("dinq_get_flow_status",
			mcp.WithDescription(
				"[READ] Get the user's Dinq onboarding flow status, domain (username), and setup progress. "+
					"Call when: starting a new conversation to understand the user's Dinq setup; "+
					"user asks 'what's my dinq domain', 'am I fully set up', or 'what's my dinq username'. "+
					"Returns: current step, domain, social links, completion status.",
			),
		),
		mcp.NewTool("dinq_get_user_data",
			mcp.WithDescription(
				"[READ] Get the user's Dinq profile: name, bio, avatar, skills, and datasources (connected platforms). "+
					"Call when: user asks 'what's on my profile', 'show my dinq info', or before suggesting profile updates. "+
					"Also useful to understand what platforms the user has linked before recommending new cards.",
			),
		),
		mcp.NewTool("dinq_get_card_board",
			mcp.WithDescription(
				"[READ] Get the user's full Dinq card board — all cards with IDs, types, positions, and content. "+
					"Call when: user asks 'show my cards', 'what cards do I have', or before deleting/reordering cards. "+
					"Card IDs from this response are required for dinq_delete_card and dinq_update_card_layout.",
			),
		),
		mcp.NewTool("dinq_validate_card_url",
			mcp.WithDescription(
				"[READ] Validate a URL and detect its card type (e.g. GITHUB, LINKEDIN, TWITTER, SCHOLAR). "+
					"Call BEFORE dinq_add_card to get the correct type and normalized URL. "+
					"If the URL is invalid or format is wrong, return the error and ask the user to provide a correct URL. "+
					"Supported: GitHub, LinkedIn, Twitter/X, Google Scholar, HuggingFace, Instagram, YouTube, TikTok, Medium, Reddit, and more.",
			),
			mcp.WithString("url", mcp.Required(), mcp.Description("The profile URL to validate (e.g. https://github.com/username)")),
		),
		mcp.NewTool("dinq_add_card",
			mcp.WithDescription(
				"[WRITE] Add a new card to the user's Dinq board and generate its AI content. "+
					"BEFORE calling: use dinq_validate_card_url to get the correct type and normalized URL. "+
					"If the user hasn't provided a URL, ask: 'What's your [platform] profile URL?' "+
					"After adding, the AI generation runs in the background — tell the user to refresh in a moment.",
			),
			mcp.WithString("url", mcp.Required(), mcp.Description("Normalized profile URL (from dinq_validate_card_url)")),
			mcp.WithString("type", mcp.Required(), mcp.Description("Card type from dinq_validate_card_url (e.g. GITHUB, LINKEDIN, TWITTER)")),
		),
		mcp.NewTool("dinq_delete_card",
			mcp.WithDescription(
				"[WRITE — confirm before calling] Delete a card from the user's Dinq board. This is irreversible. "+
					"If card_id is unknown, call dinq_get_card_board first to list all cards and their IDs. "+
					"Confirm with the user: 'Are you sure you want to remove the [card type] card?' before calling.",
			),
			mcp.WithString("card_id", mcp.Required(), mcp.Description("Card ID to delete (get from dinq_get_card_board)")),
		),
		mcp.NewTool("dinq_update_card_layout",
			mcp.WithDescription(
				"[WRITE — confirm before calling] Update the layout/order of cards on the user's Dinq board. "+
					"BEFORE calling: get the current board with dinq_get_card_board. "+
					"Call when: user wants to reorder, resize, or rearrange their cards. "+
					"Pass the full board array with modified positions. Only cards matching the user's account are applied.",
			),
			mcp.WithObject("board", mcp.Required(), mcp.Description("Full board array with card objects and updated layout positions (from dinq_get_card_board, with positions modified)")),
		),
		mcp.NewTool("dinq_init_card_board",
			mcp.WithDescription(
				"[WRITE] Initialize the user's Dinq card board with their social profile links. "+
					"Call when: user is new with no cards, or wants to start fresh from their social profiles. "+
					"BEFORE calling, collect from the user: their profile URLs (GitHub, LinkedIn, Twitter, Google Scholar, etc.). "+
					"If no URLs provided, ask: 'Please share your social profile links to set up your Dinq board (e.g. GitHub, LinkedIn, Twitter).' "+
					"After init, AI card generation runs in the background.",
			),
			mcp.WithObject("social_links", mcp.Required(), mcp.Description(`Array of {type, url} objects, e.g. [{"type":"GITHUB","url":"https://github.com/username"},{"type":"LINKEDIN","url":"https://linkedin.com/in/username"}]`)),
		),
		mcp.NewTool("dinq_regenerate_cards",
			mcp.WithDescription(
				"[WRITE] Regenerate all AI cards on the user's Dinq board with the latest data. "+
					"Call when: user says 'my cards are outdated', 'refresh my cards', or 'update my dinq profile'. "+
					"AI generation runs in the background — returns immediately. Tell user to refresh in a minute.",
			),
		),
	}
}

func (a *Adapter) Execute(ctx context.Context, toolName string, args map[string]any, _ string, userID string) (*mcp.CallToolResult, error) {
	switch toolName {
	case "get_flow_status":
		return a.getFlowStatus(ctx, userID)
	case "get_user_data":
		return a.getUserData(ctx, userID)
	case "get_card_board":
		return a.getCardBoard(ctx, userID)
	case "validate_card_url":
		return a.validateCardURL(ctx, args, userID)
	case "add_card":
		return a.addCard(ctx, args, userID)
	case "delete_card":
		return a.deleteCard(ctx, args, userID)
	case "update_card_layout":
		return a.updateCardLayout(ctx, args, userID)
	case "init_card_board":
		return a.initCardBoard(ctx, args, userID)
	case "regenerate_cards":
		return a.regenerateCards(ctx, userID)
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", toolName)), nil
	}
}

// --- Tool implementations ---

func (a *Adapter) getFlowStatus(ctx context.Context, userID string) (*mcp.CallToolResult, error) {
	data, err := a.get(ctx, "/api/v1/flow", userID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) getUserData(ctx context.Context, userID string) (*mcp.CallToolResult, error) {
	data, err := a.get(ctx, "/api/v1/user-data?user_id="+userID, userID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) getCardBoard(ctx context.Context, userID string) (*mcp.CallToolResult, error) {
	// Step 1: resolve domain from user data (card-board API requires domain, not user_id)
	userData, err := a.get(ctx, "/api/v1/user-data?user_id="+userID, userID)
	if err != nil {
		return mcp.NewToolResultError("failed to get user data: " + err.Error()), nil
	}
	// Response shape: {"code":0,"data":{"domain":"xxx",...},"message":"..."}
	var ud map[string]any
	if err := json.Unmarshal(userData, &ud); err != nil {
		return mcp.NewToolResultError("failed to parse user data"), nil
	}
	dataObj, _ := ud["data"].(map[string]any)
	domain, _ := dataObj["domain"].(string)
	if domain == "" {
		return mcp.NewToolResultError("user has no domain set — call dinq_get_flow_status to check onboarding status"), nil
	}

	// Step 2: fetch card board by domain
	data, err := a.get(ctx, "/api/v1/card-board?username="+domain, userID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) validateCardURL(ctx context.Context, args map[string]any, userID string) (*mcp.CallToolResult, error) {
	rawURL, _ := args["url"].(string)
	if rawURL == "" {
		return mcp.NewToolResultError("url is required"), nil
	}
	data, err := a.post(ctx, "/api/v1/card/validate-url", map[string]any{"url": rawURL}, userID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) addCard(ctx context.Context, args map[string]any, userID string) (*mcp.CallToolResult, error) {
	cardURL, _ := args["url"].(string)
	cardType, _ := args["type"].(string)
	if cardURL == "" || cardType == "" {
		return mcp.NewToolResultError("url and type are required"), nil
	}
	normalizedType := strings.ToUpper(cardType)

	// Step 1: add board entry (creates datasource + placeholder card)
	addBody := map[string]any{
		"type": normalizedType,
		"data": map[string]any{
			"metadata": map[string]any{
				"url":       cardURL,
				"card_type": normalizedType,
			},
		},
	}
	addResp, err := a.post(ctx, "/api/v1/card-board/add", addBody, userID)
	if err != nil {
		return mcp.NewToolResultError("failed to add card: " + err.Error()), nil
	}

	// Step 2: extract datasource_id and trigger AI generation
	var addResult map[string]any
	if err := json.Unmarshal(addResp, &addResult); err == nil {
		datasourceID := extractDatasourceID(addResult)
		if datasourceID != "" {
			genBody := map[string]any{
				"type":          normalizedType,
				"url":           cardURL,
				"datasource_id": datasourceID,
			}
			// Fire and forget — generation is async on server side (returns 202)
			_, _ = a.post(ctx, "/api/v1/card/generate", genBody, userID)
		}
	}

	return mcp.NewToolResultText(string(addResp)), nil
}

func (a *Adapter) deleteCard(ctx context.Context, args map[string]any, userID string) (*mcp.CallToolResult, error) {
	cardID, _ := args["card_id"].(string)
	if cardID == "" {
		return mcp.NewToolResultError("card_id is required"), nil
	}
	data, err := a.get(ctx, "/api/v1/card-board/delete/"+cardID, userID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) updateCardLayout(ctx context.Context, args map[string]any, userID string) (*mcp.CallToolResult, error) {
	board, ok := args["board"]
	if !ok || board == nil {
		return mcp.NewToolResultError("board is required"), nil
	}
	data, err := a.post(ctx, "/api/v1/card-board", map[string]any{"board": board}, userID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) initCardBoard(ctx context.Context, args map[string]any, userID string) (*mcp.CallToolResult, error) {
	socialLinks, ok := args["social_links"]
	if !ok || socialLinks == nil {
		return mcp.NewToolResultError("social_links is required"), nil
	}
	data, err := a.post(ctx, "/api/v1/card-board/init", map[string]any{"social_links": socialLinks}, userID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) regenerateCards(ctx context.Context, userID string) (*mcp.CallToolResult, error) {
	data, err := a.post(ctx, "/api/v1/card/regenerate/all", nil, userID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

// --- HTTP helpers ---

func (a *Adapter) get(ctx context.Context, path, userID string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", a.serverURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-User-ID", userID)
	return a.do(req)
}

func (a *Adapter) post(ctx context.Context, path string, body any, userID string) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", a.serverURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-User-ID", userID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return a.do(req)
}

func (a *Adapter) do(req *http.Request) ([]byte, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("dinq-server %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	return data, nil
}

// extractDatasourceID extracts the datasource_id from an AddBoard response.
// Actual response shape: {"code":0,"data":{"board":{"id":"...","data":{"id":"datasource-id",...}}},...}
// The datasource_id (for /card/generate) is at data.board.data.id.
func extractDatasourceID(result map[string]any) string {
	dataObj, _ := result["data"].(map[string]any)
	if dataObj == nil {
		return ""
	}
	board, _ := dataObj["board"].(map[string]any)
	if board == nil {
		return ""
	}
	inner, _ := board["data"].(map[string]any)
	if inner == nil {
		return ""
	}
	id, _ := inner["id"].(string)
	return id
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
