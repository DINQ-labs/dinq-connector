// Package smtp_email implements a credentials-based email adapter
// using IMAP for reading and SMTP for sending.
package smtp_email

import (
	"context"
	"encoding/json"
	"fmt"
	"net/smtp"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/DINQ-labs/dinq-connector/internal/adapter"
)

// Credentials holds SMTP/IMAP connection details, stored as JSON in access_token.
type Credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	SMTPHost string `json:"smtp_host"`
	SMTPPort int    `json:"smtp_port"`
}

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Name() string                   { return "smtp_email" }
func (a *Adapter) DisplayName() string            { return "Email (SMTP)" }
func (a *Adapter) AuthScheme() adapter.AuthScheme { return adapter.AuthCredentials }
func (a *Adapter) OAuthConfig() *adapter.OAuthConfig { return nil }

func (a *Adapter) Tools() []mcp.Tool {
	return []mcp.Tool{
		mcp.NewTool("smtp_email_send_email",
			mcp.WithDescription(
				"[WRITE — confirm before calling] Send an email via SMTP using the user's configured email account. "+
					"Confirm recipient(s), subject, and body before sending — emails cannot be unsent.",
			),
			mcp.WithString("to", mcp.Required(), mcp.Description("Recipient email address(es), comma-separated")),
			mcp.WithString("subject", mcp.Required(), mcp.Description("Email subject line")),
			mcp.WithString("body", mcp.Required(), mcp.Description("Email body in plain text")),
			mcp.WithString("cc", mcp.Description("CC recipients, comma-separated")),
		),
	}
}

func (a *Adapter) Execute(ctx context.Context, toolName string, args map[string]any, token, _ string) (*mcp.CallToolResult, error) {
	switch toolName {
	case "send_email":
		return a.sendEmail(ctx, args, token)
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", toolName)), nil
	}
}

func (a *Adapter) sendEmail(_ context.Context, args map[string]any, token string) (*mcp.CallToolResult, error) {
	var creds Credentials
	if err := json.Unmarshal([]byte(token), &creds); err != nil {
		return mcp.NewToolResultError("invalid credentials: " + err.Error()), nil
	}

	to := argStr(args, "to")
	subject := argStr(args, "subject")
	body := argStr(args, "body")
	cc := argStr(args, "cc")

	var allRecipients []string
	for _, addr := range splitComma(to) {
		allRecipients = append(allRecipients, addr)
	}
	for _, addr := range splitComma(cc) {
		allRecipients = append(allRecipients, addr)
	}

	// Build RFC 5322 message
	var msg strings.Builder
	msg.WriteString("From: " + creds.Email + "\r\n")
	msg.WriteString("To: " + to + "\r\n")
	if cc != "" {
		msg.WriteString("Cc: " + cc + "\r\n")
	}
	msg.WriteString("Subject: " + subject + "\r\n")
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)

	addr := fmt.Sprintf("%s:%d", creds.SMTPHost, creds.SMTPPort)
	auth := smtp.PlainAuth("", creds.Email, creds.Password, creds.SMTPHost)

	if err := smtp.SendMail(addr, auth, creds.Email, allRecipients, []byte(msg.String())); err != nil {
		return mcp.NewToolResultError("SMTP send failed: " + err.Error()), nil
	}

	return mcp.NewToolResultText(`{"status":"sent"}`), nil
}

// ValidateCredentials tests SMTP connectivity. Called during connect flow.
func ValidateCredentials(creds *Credentials) error {
	addr := fmt.Sprintf("%s:%d", creds.SMTPHost, creds.SMTPPort)
	auth := smtp.PlainAuth("", creds.Email, creds.Password, creds.SMTPHost)

	// Try connecting and authenticating
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("SMTP connect failed: %v", err)
	}
	defer c.Close()

	if err := c.Auth(auth); err != nil {
		return fmt.Errorf("SMTP auth failed: %v", err)
	}
	return nil
}

func argStr(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
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
