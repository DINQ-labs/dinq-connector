// Package nylas implements the PlatformAdapter for email via Nylas v3 API.
// Supports any IMAP/SMTP provider (Yahoo, iCloud, corporate mail, etc.)
// through Nylas's unified email API with hosted OAuth.
package nylas

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/DINQ-labs/dinq-connector/internal/adapter"
)

const apiBase = "https://api.us.nylas.com/v3"

// Adapter implements adapter.PlatformAdapter for Nylas-backed email.
type Adapter struct {
	apiKey string // Server-side NYLAS_API_KEY for all API calls
}

// New creates a Nylas adapter with the given server-side API key.
func New(apiKey string) *Adapter {
	return &Adapter{apiKey: apiKey}
}

func (a *Adapter) Name() string                   { return "nylas" }
func (a *Adapter) DisplayName() string            { return "Email (IMAP)" }
func (a *Adapter) AuthScheme() adapter.AuthScheme { return adapter.AuthOAuth2 }

func (a *Adapter) OAuthConfig() *adapter.OAuthConfig {
	return &adapter.OAuthConfig{
		AuthorizeURL: apiBase + "/connect/auth",
		TokenURL:     apiBase + "/connect/token",
		Scopes:       []string{}, // IMAP doesn't support scopes
		ExtraParams: map[string]string{
			"response_type": "code",
			"access_type":   "online",
		},
		JSONTokenExchange: true,                   // Nylas requires JSON body for token exchange
		TokenExchangeExtra: map[string]string{     // Extra fields for token exchange
			"code_verifier": "nylas",
		},
		GrantIDField: "grant_id", // Response field to use as access_token
	}
}

func (a *Adapter) Tools() []mcp.Tool {
	return []mcp.Tool{
		mcp.NewTool("nylas_send_email",
			mcp.WithDescription(
				"[WRITE — confirm before calling] Send an email from the user's connected email account. "+
					"Confirm recipient(s), subject, and body before sending — emails cannot be unsent.",
			),
			mcp.WithString("to", mcp.Required(), mcp.Description("Recipient email address(es), comma-separated")),
			mcp.WithString("subject", mcp.Required(), mcp.Description("Email subject line")),
			mcp.WithString("body", mcp.Required(), mcp.Description("Email body (HTML supported)")),
			mcp.WithString("cc", mcp.Description("CC recipients, comma-separated")),
			mcp.WithString("bcc", mcp.Description("BCC recipients, comma-separated")),
			mcp.WithString("from_name", mcp.Description("Optional sender display name.")),
			mcp.WithString("from_email", mcp.Description("Optional sender email; must match the authenticated grant's email.")),
		),
		mcp.NewTool("nylas_list_messages",
			mcp.WithDescription(
				"[READ] List email messages, optionally filtered. "+
					"Returns message summaries — call nylas_get_message for full content. "+
					"Default returns the 10 most recent messages.",
			),
			mcp.WithString("from", mcp.Description("Filter by sender email address")),
			mcp.WithString("to", mcp.Description("Filter by recipient email address")),
			mcp.WithString("subject", mcp.Description("Filter by subject (partial match)")),
			mcp.WithBoolean("unread", mcp.Description("Filter for unread messages only")),
			mcp.WithNumber("limit", mcp.Description("Max messages to return (default 10, max 50)")),
		),
		mcp.NewTool("nylas_get_message",
			mcp.WithDescription(
				"[READ] Get the full content of an email message by ID: sender, recipients, subject, body, attachments. "+
					"Use a message ID obtained from nylas_list_messages or nylas_search_messages.",
			),
			mcp.WithString("message_id", mcp.Required(), mcp.Description("Message ID")),
		),
		mcp.NewTool("nylas_search_messages",
			mcp.WithDescription(
				"[READ] Search email messages using the provider's native search syntax. "+
					"Returns message summaries with IDs.",
			),
			mcp.WithString("q", mcp.Required(), mcp.Description("Search query string")),
			mcp.WithNumber("limit", mcp.Description("Max results (default 10, max 50)")),
		),
		mcp.NewTool("nylas_reply_to_email",
			mcp.WithDescription(
				"[WRITE — confirm before calling] Reply to an existing email message. "+
					"Confirm reply content before sending. The reply is threaded under the original message.",
			),
			mcp.WithString("message_id", mcp.Required(), mcp.Description("Message ID to reply to")),
			mcp.WithString("body", mcp.Required(), mcp.Description("Reply body (HTML supported)")),
		),
		mcp.NewTool("nylas_list_folders",
			mcp.WithDescription(
				"[READ] List all email folders/labels. "+
					"Useful to see available folders before filtering messages.",
			),
		),
	}
}

func (a *Adapter) Execute(ctx context.Context, toolName string, args map[string]any, grantID, _ string) (*mcp.CallToolResult, error) {
	switch toolName {
	case "send_email":
		return a.sendEmail(ctx, args, grantID)
	case "list_messages":
		return a.listMessages(ctx, args, grantID)
	case "get_message":
		return a.getMessage(ctx, args, grantID)
	case "search_messages":
		return a.searchMessages(ctx, args, grantID)
	case "reply_to_email":
		return a.replyToEmail(ctx, args, grantID)
	case "list_folders":
		return a.listFolders(ctx, grantID)
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", toolName)), nil
	}
}

// --- Tool implementations ---

func (a *Adapter) sendEmail(ctx context.Context, args map[string]any, grantID string) (*mcp.CallToolResult, error) {
	to := parseRecipients(argStr(args, "to"))
	subject := argStr(args, "subject")
	body := argStr(args, "body")

	payload := map[string]any{
		"to":      to,
		"subject": subject,
		"body":    body,
	}

	if cc := argStr(args, "cc"); cc != "" {
		payload["cc"] = parseRecipients(cc)
	}
	if bcc := argStr(args, "bcc"); bcc != "" {
		payload["bcc"] = parseRecipients(bcc)
	}

	// Nylas v3: from is an array of participants. Email must match the grant's
	// authenticated mailbox; name is free-form display name.
	if fromEmail := argStr(args, "from_email"); fromEmail != "" {
		payload["from"] = []nylasParticipant{{
			Name:  argStr(args, "from_name"),
			Email: fromEmail,
		}}
	}

	data, err := a.nylasPost(ctx, grantID, "/messages/send", payload)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	// Return a clean summary
	var resp struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
	}
	if json.Unmarshal(data, &resp) == nil {
		out, _ := json.Marshal(map[string]string{"status": "sent", "id": resp.ID, "subject": resp.Subject})
		return mcp.NewToolResultText(string(out)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) listMessages(ctx context.Context, args map[string]any, grantID string) (*mcp.CallToolResult, error) {
	limit := intArg(args, "limit", 10)
	if limit > 50 {
		limit = 50
	}

	query := fmt.Sprintf("?limit=%d", limit)
	if from := argStr(args, "from"); from != "" {
		query += "&from=" + from
	}
	if to := argStr(args, "to"); to != "" {
		query += "&to=" + to
	}
	if subject := argStr(args, "subject"); subject != "" {
		query += "&subject=" + subject
	}
	if unread, ok := args["unread"].(bool); ok {
		query += fmt.Sprintf("&unread=%v", unread)
	}

	data, err := a.nylasGet(ctx, grantID, "/messages"+query)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Parse and return clean summaries
	var listResp struct {
		Data []struct {
			ID       string           `json:"id"`
			ThreadID string           `json:"thread_id"`
			Subject  string           `json:"subject"`
			From     []nylasParticipant `json:"from"`
			To       []nylasParticipant `json:"to"`
			Date     int64            `json:"date"`
			Unread   bool             `json:"unread"`
			Snippet  string           `json:"snippet"`
			Folders  []string         `json:"folders"`
		} `json:"data"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(data, &listResp); err != nil {
		return mcp.NewToolResultText(string(data)), nil
	}

	type msgSummary struct {
		ID       string `json:"id"`
		ThreadID string `json:"thread_id"`
		Subject  string `json:"subject"`
		From     string `json:"from"`
		Date     int64  `json:"date"`
		Unread   bool   `json:"unread"`
		Snippet  string `json:"snippet"`
	}

	summaries := make([]msgSummary, 0, len(listResp.Data))
	for _, m := range listResp.Data {
		from := ""
		if len(m.From) > 0 {
			from = m.From[0].Email
			if m.From[0].Name != "" {
				from = m.From[0].Name + " <" + m.From[0].Email + ">"
			}
		}
		summaries = append(summaries, msgSummary{
			ID:       m.ID,
			ThreadID: m.ThreadID,
			Subject:  m.Subject,
			From:     from,
			Date:     m.Date,
			Unread:   m.Unread,
			Snippet:  m.Snippet,
		})
	}

	out, _ := json.Marshal(map[string]any{"messages": summaries, "count": len(summaries)})
	return mcp.NewToolResultText(string(out)), nil
}

func (a *Adapter) getMessage(ctx context.Context, args map[string]any, grantID string) (*mcp.CallToolResult, error) {
	msgID := argStr(args, "message_id")
	if msgID == "" {
		return mcp.NewToolResultError("message_id is required"), nil
	}

	data, err := a.nylasGet(ctx, grantID, "/messages/"+msgID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	var msg struct {
		ID       string             `json:"id"`
		ThreadID string             `json:"thread_id"`
		Subject  string             `json:"subject"`
		From     []nylasParticipant `json:"from"`
		To       []nylasParticipant `json:"to"`
		Cc       []nylasParticipant `json:"cc"`
		Date     int64              `json:"date"`
		Unread   bool               `json:"unread"`
		Body     string             `json:"body"`
		Folders  []string           `json:"folders"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return mcp.NewToolResultText(string(data)), nil
	}

	result := map[string]any{
		"id":        msg.ID,
		"thread_id": msg.ThreadID,
		"subject":   msg.Subject,
		"from":      formatParticipants(msg.From),
		"to":        formatParticipants(msg.To),
		"cc":        formatParticipants(msg.Cc),
		"date":      msg.Date,
		"unread":    msg.Unread,
		"body":      msg.Body,
		"folders":   msg.Folders,
	}
	out, _ := json.Marshal(result)
	return mcp.NewToolResultText(string(out)), nil
}

func (a *Adapter) searchMessages(ctx context.Context, args map[string]any, grantID string) (*mcp.CallToolResult, error) {
	q := argStr(args, "q")
	if q == "" {
		return mcp.NewToolResultError("q (search query) is required"), nil
	}
	limit := intArg(args, "limit", 10)
	if limit > 50 {
		limit = 50
	}

	query := fmt.Sprintf("?search_query_native=%s&limit=%d", q, limit)
	data, err := a.nylasGet(ctx, grantID, "/messages"+query)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) replyToEmail(ctx context.Context, args map[string]any, grantID string) (*mcp.CallToolResult, error) {
	msgID := argStr(args, "message_id")
	body := argStr(args, "body")

	payload := map[string]any{
		"body":                body,
		"reply_to_message_id": msgID,
	}

	// Get original message to determine recipients
	origData, err := a.nylasGet(ctx, grantID, "/messages/"+msgID)
	if err != nil {
		return mcp.NewToolResultError("failed to fetch original message: " + err.Error()), nil
	}

	var orig struct {
		From    []nylasParticipant `json:"from"`
		Subject string             `json:"subject"`
	}
	if err := json.Unmarshal(origData, &orig); err == nil && len(orig.From) > 0 {
		payload["to"] = orig.From
		payload["subject"] = "Re: " + orig.Subject
	}

	data, err := a.nylasPost(ctx, grantID, "/messages/send", payload)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) listFolders(ctx context.Context, grantID string) (*mcp.CallToolResult, error) {
	data, err := a.nylasGet(ctx, grantID, "/folders")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

// --- HTTP helpers ---

func (a *Adapter) nylasGet(ctx context.Context, grantID, path string) ([]byte, error) {
	url := fmt.Sprintf("%s/grants/%s%s", apiBase, grantID, path)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Nylas API %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return body, nil
}

func (a *Adapter) nylasPost(ctx context.Context, grantID, path string, payload any) ([]byte, error) {
	url := fmt.Sprintf("%s/grants/%s%s", apiBase, grantID, path)
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Nylas API %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return body, nil
}

// --- Types and utils ---

type nylasParticipant struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func parseRecipients(s string) []nylasParticipant {
	var result []nylasParticipant
	for _, part := range splitComma(s) {
		result = append(result, nylasParticipant{Email: part})
	}
	return result
}

func formatParticipants(ps []nylasParticipant) string {
	var parts []string
	for _, p := range ps {
		if p.Name != "" {
			parts = append(parts, p.Name+" <"+p.Email+">")
		} else {
			parts = append(parts, p.Email)
		}
	}
	return joinComma(parts)
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
	for _, part := range split(s, ",") {
		part = trim(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func joinComma(parts []string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += ", "
		}
		result += p
	}
	return result
}

func split(s, sep string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for {
		i := indexOf(s, sep)
		if i < 0 {
			result = append(result, s)
			break
		}
		result = append(result, s[:i])
		s = s[i+len(sep):]
	}
	return result
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
