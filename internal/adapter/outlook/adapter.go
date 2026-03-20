// Package outlook implements the PlatformAdapter for Microsoft Outlook/Hotmail
// using direct OAuth 2.0 and Microsoft Graph API.
package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/DINQ-labs/dinq-connector/internal/adapter"
)

const graphBase = "https://graph.microsoft.com/v1.0/me"

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Name() string                   { return "outlook" }
func (a *Adapter) DisplayName() string            { return "Outlook" }
func (a *Adapter) AuthScheme() adapter.AuthScheme { return adapter.AuthOAuth2 }

func (a *Adapter) OAuthConfig() *adapter.OAuthConfig {
	return &adapter.OAuthConfig{
		AuthorizeURL: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		TokenURL:     "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		Scopes: []string{
			"Mail.Send",
			"Mail.Read",
			"User.Read",
			"offline_access",
		},
	}
}

func (a *Adapter) Tools() []mcp.Tool {
	return []mcp.Tool{
		mcp.NewTool("outlook_send_email",
			mcp.WithDescription(
				"[WRITE — confirm before calling] Send an email from the user's Outlook account. "+
					"Confirm recipient(s), subject, and body before sending — emails cannot be unsent.",
			),
			mcp.WithString("to", mcp.Required(), mcp.Description("Recipient email address(es), comma-separated")),
			mcp.WithString("subject", mcp.Required(), mcp.Description("Email subject line")),
			mcp.WithString("body", mcp.Required(), mcp.Description("Email body in plain text")),
			mcp.WithString("cc", mcp.Description("CC recipients, comma-separated")),
		),
		mcp.NewTool("outlook_list_messages",
			mcp.WithDescription(
				"[READ] List Outlook messages. Returns subject, sender, date, and preview. "+
					"Default returns the 10 most recent messages.",
			),
			mcp.WithString("filter", mcp.Description("OData filter (e.g. \"isRead eq false\")")),
			mcp.WithNumber("top", mcp.Description("Max messages to return (default 10, max 50)")),
		),
		mcp.NewTool("outlook_get_message",
			mcp.WithDescription(
				"[READ] Get the full content of an Outlook message by ID.",
			),
			mcp.WithString("message_id", mcp.Required(), mcp.Description("Outlook message ID")),
		),
		mcp.NewTool("outlook_search_messages",
			mcp.WithDescription(
				"[READ] Search Outlook messages by keyword. "+
					"Searches across subject, body, and sender.",
			),
			mcp.WithString("q", mcp.Required(), mcp.Description("Search query")),
			mcp.WithNumber("top", mcp.Description("Max results (default 10)")),
		),
		mcp.NewTool("outlook_reply_to_email",
			mcp.WithDescription(
				"[WRITE — confirm before calling] Reply to an Outlook message. "+
					"Confirm reply content before sending.",
			),
			mcp.WithString("message_id", mcp.Required(), mcp.Description("Message ID to reply to")),
			mcp.WithString("body", mcp.Required(), mcp.Description("Reply body in plain text")),
		),
		mcp.NewTool("outlook_create_draft",
			mcp.WithDescription(
				"[WRITE-SAFE] Save an email as a draft without sending.",
			),
			mcp.WithString("to", mcp.Required(), mcp.Description("Recipient email address(es), comma-separated")),
			mcp.WithString("subject", mcp.Required(), mcp.Description("Email subject line")),
			mcp.WithString("body", mcp.Required(), mcp.Description("Email body in plain text")),
		),
		mcp.NewTool("outlook_list_folders",
			mcp.WithDescription(
				"[READ] List Outlook mail folders.",
			),
		),
	}
}

func (a *Adapter) Execute(ctx context.Context, toolName string, args map[string]any, token, _ string) (*mcp.CallToolResult, error) {
	switch toolName {
	case "send_email":
		return a.sendEmail(ctx, args, token)
	case "list_messages":
		return a.listMessages(ctx, args, token)
	case "get_message":
		return a.getMessage(ctx, args, token)
	case "search_messages":
		return a.searchMessages(ctx, args, token)
	case "reply_to_email":
		return a.replyToEmail(ctx, args, token)
	case "create_draft":
		return a.createDraft(ctx, args, token)
	case "list_folders":
		return a.listFolders(ctx, token)
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", toolName)), nil
	}
}

// --- Tool implementations ---

func (a *Adapter) sendEmail(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	to := argStr(args, "to")
	subject := argStr(args, "subject")
	body := argStr(args, "body")
	cc := argStr(args, "cc")

	payload := map[string]any{
		"message": map[string]any{
			"subject": subject,
			"body": map[string]any{
				"contentType": "Text",
				"content":     body,
			},
			"toRecipients": buildRecipients(to),
		},
	}
	if cc != "" {
		msg := payload["message"].(map[string]any)
		msg["ccRecipients"] = buildRecipients(cc)
	}

	data, err := graphPost(ctx, "/sendMail", payload, token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if len(data) == 0 {
		return mcp.NewToolResultText(`{"status":"sent"}`), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) listMessages(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	top := intArg(args, "top", 10)
	filter := argStr(args, "filter")

	path := fmt.Sprintf("/messages?$top=%d&$select=id,subject,from,receivedDateTime,isRead,bodyPreview", top)
	if filter != "" {
		path += "&$filter=" + filter
	}

	data, err := graphGet(ctx, path, token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) getMessage(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	msgID := argStr(args, "message_id")
	data, err := graphGet(ctx, "/messages/"+msgID, token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) searchMessages(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	q := argStr(args, "q")
	top := intArg(args, "top", 10)

	path := fmt.Sprintf("/messages?$search=\"%s\"&$top=%d&$select=id,subject,from,receivedDateTime,bodyPreview", q, top)
	data, err := graphGet(ctx, path, token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) replyToEmail(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	msgID := argStr(args, "message_id")
	body := argStr(args, "body")

	payload := map[string]any{
		"comment": body,
	}

	_, err := graphPost(ctx, "/messages/"+msgID+"/reply", payload, token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(`{"status":"replied"}`), nil
}

func (a *Adapter) createDraft(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	to := argStr(args, "to")
	subject := argStr(args, "subject")
	body := argStr(args, "body")

	payload := map[string]any{
		"subject": subject,
		"body": map[string]any{
			"contentType": "Text",
			"content":     body,
		},
		"toRecipients": buildRecipients(to),
	}

	data, err := graphPost(ctx, "/messages", payload, token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) listFolders(ctx context.Context, token string) (*mcp.CallToolResult, error) {
	data, err := graphGet(ctx, "/mailFolders?$select=id,displayName,totalItemCount,unreadItemCount", token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

// --- Helpers ---

func buildRecipients(emails string) []map[string]any {
	var recipients []map[string]any
	for _, email := range splitComma(emails) {
		recipients = append(recipients, map[string]any{
			"emailAddress": map[string]any{"address": email},
		})
	}
	return recipients
}

func graphGet(ctx context.Context, path, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", graphBase+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Graph API %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return body, nil
}

func graphPost(ctx context.Context, path string, payload any, token string) ([]byte, error) {
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", graphBase+path, strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Graph API %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return body, nil
}

func argStr(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key].(float64); ok && v > 0 {
		return int(v)
	}
	return def
}

func splitComma(s string) []string {
	var result []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
