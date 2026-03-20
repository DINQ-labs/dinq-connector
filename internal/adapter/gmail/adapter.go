// Package gmail implements the PlatformAdapter for Gmail using direct OAuth 2.0.
// Tools: send email, list messages, get message, search, reply, create draft, list labels.
package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/DINQ-labs/dinq-connector/internal/adapter"
)

const apiBase = "https://gmail.googleapis.com/gmail/v1/users/me"

// Adapter implements adapter.PlatformAdapter for Gmail.
type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Name() string                   { return "gmail" }
func (a *Adapter) DisplayName() string            { return "Gmail" }
func (a *Adapter) AuthScheme() adapter.AuthScheme { return adapter.AuthOAuth2 }

func (a *Adapter) OAuthConfig() *adapter.OAuthConfig {
	return &adapter.OAuthConfig{
		AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
		Scopes: []string{
			"https://www.googleapis.com/auth/gmail.modify",
			"https://www.googleapis.com/auth/gmail.send",
			"openid",
			"email",
		},
		ExtraParams: map[string]string{
			"access_type": "offline",
			"prompt":      "consent",
		},
	}
}

func (a *Adapter) Tools() []mcp.Tool {
	return []mcp.Tool{
		mcp.NewTool("gmail_send_email",
			mcp.WithDescription(
				"[WRITE — confirm before calling] Send an email from the user's Gmail account. "+
					"Confirm recipient(s), subject, and body before sending — emails cannot be unsent.",
			),
			mcp.WithString("to", mcp.Required(), mcp.Description("Recipient email address(es), comma-separated")),
			mcp.WithString("subject", mcp.Required(), mcp.Description("Email subject line")),
			mcp.WithString("body", mcp.Required(), mcp.Description("Email body in plain text")),
			mcp.WithString("cc", mcp.Description("CC recipients, comma-separated")),
			mcp.WithString("bcc", mcp.Description("BCC recipients, comma-separated")),
		),
		mcp.NewTool("gmail_list_messages",
			mcp.WithDescription(
				"[READ] List Gmail messages, optionally filtered by query. "+
					"Returns message IDs and snippet previews — call gmail_get_message to read full content. "+
					"Default returns the 10 most recent messages.",
			),
			mcp.WithString("q", mcp.Description("Gmail search query (e.g. 'from:alice subject:meeting is:unread')")),
			mcp.WithNumber("max_results", mcp.Description("Max messages to return (default 10, max 100)")),
		),
		mcp.NewTool("gmail_get_message",
			mcp.WithDescription(
				"[READ] Get the full content of a Gmail message by ID: sender, recipients, subject, body, attachments. "+
					"Use a message ID obtained from gmail_list_messages or gmail_search_messages.",
			),
			mcp.WithString("message_id", mcp.Required(), mcp.Description("Gmail message ID")),
		),
		mcp.NewTool("gmail_search_messages",
			mcp.WithDescription(
				"[READ] Search Gmail by query string. Supports Gmail search operators: "+
					"from:, to:, subject:, is:unread, has:attachment, after:2024/01/01, label:, etc. "+
					"Returns message IDs and snippets.",
			),
			mcp.WithString("q", mcp.Required(), mcp.Description("Gmail search query")),
			mcp.WithNumber("max_results", mcp.Description("Max results (default 10, max 100)")),
		),
		mcp.NewTool("gmail_reply_to_email",
			mcp.WithDescription(
				"[WRITE — confirm before calling] Reply to an existing Gmail message. "+
					"Confirm reply content before sending. The reply is threaded under the original message.",
			),
			mcp.WithString("message_id", mcp.Required(), mcp.Description("Message ID to reply to")),
			mcp.WithString("body", mcp.Required(), mcp.Description("Reply body in plain text")),
			mcp.WithBoolean("reply_all", mcp.Description("Reply to all recipients (default false)")),
		),
		mcp.NewTool("gmail_create_draft",
			mcp.WithDescription(
				"[WRITE-SAFE] Save an email as a draft without sending. "+
					"Use when the user wants to review before sending.",
			),
			mcp.WithString("to", mcp.Required(), mcp.Description("Recipient email address(es), comma-separated")),
			mcp.WithString("subject", mcp.Required(), mcp.Description("Email subject line")),
			mcp.WithString("body", mcp.Required(), mcp.Description("Email body in plain text")),
			mcp.WithString("cc", mcp.Description("CC recipients, comma-separated")),
		),
		mcp.NewTool("gmail_list_labels",
			mcp.WithDescription(
				"[READ] List all Gmail labels (folders/categories). "+
					"Useful before filtering searches by label.",
			),
		),
		mcp.NewTool("gmail_get_thread",
			mcp.WithDescription(
				"[READ] Get all messages in a Gmail thread. "+
					"Call to read the full conversation history for a given thread.",
			),
			mcp.WithString("thread_id", mcp.Required(), mcp.Description("Gmail thread ID")),
		),
		mcp.NewTool("gmail_modify_labels",
			mcp.WithDescription(
				"[WRITE-SAFE] Add or remove labels from a message. "+
					"Use to mark as read/unread, archive, star, or organize messages.",
			),
			mcp.WithString("message_id", mcp.Required(), mcp.Description("Gmail message ID")),
			mcp.WithString("add_labels", mcp.Description("Label IDs to add, comma-separated (e.g. 'STARRED,UNREAD')")),
			mcp.WithString("remove_labels", mcp.Description("Label IDs to remove, comma-separated (e.g. 'UNREAD,INBOX')")),
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
	case "list_labels":
		return a.listLabels(ctx, token)
	case "get_thread":
		return a.getThread(ctx, args, token)
	case "modify_labels":
		return a.modifyLabels(ctx, args, token)
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
	bcc := argStr(args, "bcc")

	raw := buildMIME(to, cc, bcc, subject, body)
	payload := map[string]any{"raw": raw}

	data, err := gmailPost(ctx, "/messages/send", payload, token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) listMessages(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	maxResults := intArg(args, "max_results", 10)
	q := argStr(args, "q")

	params := url.Values{"maxResults": {fmt.Sprintf("%d", maxResults)}}
	if q != "" {
		params.Set("q", q)
	}

	data, err := gmailGet(ctx, "/messages?"+params.Encode(), token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Enrich with snippets by fetching metadata for each message
	var listResp struct {
		Messages []struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"messages"`
		ResultSizeEstimate int `json:"resultSizeEstimate"`
	}
	if err := json.Unmarshal(data, &listResp); err != nil {
		return mcp.NewToolResultText(string(data)), nil
	}

	type msgSummary struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
		From     string `json:"from,omitempty"`
		Subject  string `json:"subject,omitempty"`
		Date     string `json:"date,omitempty"`
		Snippet  string `json:"snippet,omitempty"`
	}

	summaries := make([]msgSummary, 0, len(listResp.Messages))
	for _, m := range listResp.Messages {
		metaData, err := gmailGet(ctx, "/messages/"+m.ID+"?format=metadata&metadataHeaders=From&metadataHeaders=Subject&metadataHeaders=Date", token)
		if err != nil {
			summaries = append(summaries, msgSummary{ID: m.ID, ThreadID: m.ThreadID})
			continue
		}
		var msg struct {
			ID      string `json:"id"`
			Snippet string `json:"snippet"`
			Payload struct {
				Headers []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"headers"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(metaData, &msg); err != nil {
			summaries = append(summaries, msgSummary{ID: m.ID, ThreadID: m.ThreadID})
			continue
		}
		s := msgSummary{ID: m.ID, ThreadID: m.ThreadID, Snippet: msg.Snippet}
		for _, h := range msg.Payload.Headers {
			switch h.Name {
			case "From":
				s.From = h.Value
			case "Subject":
				s.Subject = h.Value
			case "Date":
				s.Date = h.Value
			}
		}
		summaries = append(summaries, s)
	}

	result := map[string]any{
		"messages":           summaries,
		"resultSizeEstimate": listResp.ResultSizeEstimate,
	}
	out, _ := json.Marshal(result)
	return mcp.NewToolResultText(string(out)), nil
}

func (a *Adapter) getMessage(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	msgID := argStr(args, "message_id")
	data, err := gmailGet(ctx, "/messages/"+msgID+"?format=full", token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Parse and extract readable content
	parsed := parseMessage(data)
	out, _ := json.Marshal(parsed)
	return mcp.NewToolResultText(string(out)), nil
}

func (a *Adapter) searchMessages(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	// search_messages is the same as list_messages with required q
	args["q"] = argStr(args, "q")
	if _, ok := args["max_results"]; !ok {
		args["max_results"] = float64(10)
	}
	return a.listMessages(ctx, args, token)
}

func (a *Adapter) replyToEmail(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	msgID := argStr(args, "message_id")
	body := argStr(args, "body")
	replyAll, _ := args["reply_all"].(bool)

	// Get original message to extract headers
	origData, err := gmailGet(ctx, "/messages/"+msgID+"?format=metadata&metadataHeaders=From&metadataHeaders=To&metadataHeaders=Cc&metadataHeaders=Subject&metadataHeaders=Message-ID", token)
	if err != nil {
		return mcp.NewToolResultError("failed to fetch original message: " + err.Error()), nil
	}

	var orig struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
		Payload  struct {
			Headers []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"headers"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(origData, &orig); err != nil {
		return mcp.NewToolResultError("failed to parse original message: " + err.Error()), nil
	}

	var from, to, cc, subject, messageID string
	for _, h := range orig.Payload.Headers {
		switch h.Name {
		case "From":
			from = h.Value
		case "To":
			to = h.Value
		case "Cc":
			cc = h.Value
		case "Subject":
			subject = h.Value
		case "Message-ID":
			messageID = h.Value
		}
	}

	replyTo := from
	if replyAll {
		if cc != "" {
			replyTo = from + ", " + to + ", " + cc
		} else {
			replyTo = from + ", " + to
		}
	}

	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	// Build MIME with In-Reply-To and References headers
	var mime strings.Builder
	mime.WriteString("To: " + replyTo + "\r\n")
	mime.WriteString("Subject: " + subject + "\r\n")
	if messageID != "" {
		mime.WriteString("In-Reply-To: " + messageID + "\r\n")
		mime.WriteString("References: " + messageID + "\r\n")
	}
	mime.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	mime.WriteString("\r\n")
	mime.WriteString(body)

	raw := base64.URLEncoding.EncodeToString([]byte(mime.String()))
	payload := map[string]any{
		"raw":      raw,
		"threadId": orig.ThreadID,
	}

	data, err := gmailPost(ctx, "/messages/send", payload, token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) createDraft(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	to := argStr(args, "to")
	subject := argStr(args, "subject")
	body := argStr(args, "body")
	cc := argStr(args, "cc")

	raw := buildMIME(to, cc, "", subject, body)
	payload := map[string]any{
		"message": map[string]any{"raw": raw},
	}

	data, err := gmailPost(ctx, "/drafts", payload, token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) listLabels(ctx context.Context, token string) (*mcp.CallToolResult, error) {
	data, err := gmailGet(ctx, "/labels", token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) getThread(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	threadID := argStr(args, "thread_id")
	data, err := gmailGet(ctx, "/threads/"+threadID+"?format=full", token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (a *Adapter) modifyLabels(ctx context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	msgID := argStr(args, "message_id")
	addLabels := argStr(args, "add_labels")
	removeLabels := argStr(args, "remove_labels")

	payload := map[string]any{}
	if addLabels != "" {
		payload["addLabelIds"] = splitComma(addLabels)
	}
	if removeLabels != "" {
		payload["removeLabelIds"] = splitComma(removeLabels)
	}

	data, err := gmailPost(ctx, "/messages/"+msgID+"/modify", payload, token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

// --- MIME builder ---

func buildMIME(to, cc, bcc, subject, body string) string {
	var mime strings.Builder
	mime.WriteString("To: " + to + "\r\n")
	if cc != "" {
		mime.WriteString("Cc: " + cc + "\r\n")
	}
	if bcc != "" {
		mime.WriteString("Bcc: " + bcc + "\r\n")
	}
	mime.WriteString("Subject: " + subject + "\r\n")
	mime.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	mime.WriteString("\r\n")
	mime.WriteString(body)
	return base64.URLEncoding.EncodeToString([]byte(mime.String()))
}

// --- Message parser ---

func parseMessage(raw []byte) map[string]any {
	var msg struct {
		ID       string   `json:"id"`
		ThreadID string   `json:"threadId"`
		LabelIDs []string `json:"labelIds"`
		Snippet  string   `json:"snippet"`
		Payload  struct {
			MimeType string `json:"mimeType"`
			Headers  []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"headers"`
			Body struct {
				Data string `json:"data"`
				Size int    `json:"size"`
			} `json:"body"`
			Parts []struct {
				MimeType string `json:"mimeType"`
				Body     struct {
					Data string `json:"data"`
					Size int    `json:"size"`
				} `json:"body"`
			} `json:"parts"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return map[string]any{"raw": string(raw)}
	}

	result := map[string]any{
		"id":        msg.ID,
		"thread_id": msg.ThreadID,
		"labels":    msg.LabelIDs,
		"snippet":   msg.Snippet,
	}

	for _, h := range msg.Payload.Headers {
		switch h.Name {
		case "From":
			result["from"] = h.Value
		case "To":
			result["to"] = h.Value
		case "Cc":
			result["cc"] = h.Value
		case "Subject":
			result["subject"] = h.Value
		case "Date":
			result["date"] = h.Value
		}
	}

	// Extract body text
	bodyText := ""
	if msg.Payload.Body.Data != "" {
		if decoded, err := base64.URLEncoding.DecodeString(msg.Payload.Body.Data); err == nil {
			bodyText = string(decoded)
		}
	}
	if bodyText == "" {
		for _, part := range msg.Payload.Parts {
			if part.MimeType == "text/plain" && part.Body.Data != "" {
				if decoded, err := base64.URLEncoding.DecodeString(part.Body.Data); err == nil {
					bodyText = string(decoded)
					break
				}
			}
		}
	}
	if bodyText == "" {
		for _, part := range msg.Payload.Parts {
			if part.MimeType == "text/html" && part.Body.Data != "" {
				if decoded, err := base64.URLEncoding.DecodeString(part.Body.Data); err == nil {
					bodyText = string(decoded)
					break
				}
			}
		}
	}
	result["body"] = bodyText

	return result
}

// --- HTTP helpers ---

func gmailGet(ctx context.Context, path, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", apiBase+path, nil)
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
		return nil, fmt.Errorf("Gmail API %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return body, nil
}

func gmailPost(ctx context.Context, path string, payload any, token string) ([]byte, error) {
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", apiBase+path, strings.NewReader(string(data)))
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
		return nil, fmt.Errorf("Gmail API %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return body, nil
}

// --- Util ---

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
