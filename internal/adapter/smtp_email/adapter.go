// Package smtpemail implements authenticated email sending through a user's
// SMTP server. It intentionally provides sending only; mailbox reads require
// IMAP and are outside this adapter.
package smtpemail

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/DINQ-labs/dinq-connector/internal/adapter"
)

const smtpTimeout = 15 * time.Second

type Adapter struct{}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Host     string `json:"smtp_host"`
	Port     int    `json:"smtp_port"`
	Username string `json:"username,omitempty"`
	Security string `json:"security"`
}

type emailAttachment struct {
	Filename    string
	Content     []byte
	ContentType string
}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Name() string                      { return "smtp_email" }
func (a *Adapter) DisplayName() string               { return "Other Email (SMTP)" }
func (a *Adapter) AuthScheme() adapter.AuthScheme    { return adapter.AuthCredentials }
func (a *Adapter) OAuthConfig() *adapter.OAuthConfig { return nil }

func (a *Adapter) Tools() []mcp.Tool {
	return []mcp.Tool{
		mcp.NewTool("smtp_email_send_email",
			mcp.WithDescription(
				"[WRITE - confirm before calling] Send an email from the user's connected SMTP mailbox. "+
					"Confirm recipients, subject, and body before sending.",
			),
			mcp.WithString("to", mcp.Required(), mcp.Description("Recipient email address(es), comma-separated")),
			mcp.WithString("subject", mcp.Required(), mcp.Description("Email subject line")),
			mcp.WithString("body", mcp.Required(), mcp.Description("Email body; HTML is supported")),
			mcp.WithString("cc", mcp.Description("CC recipients, comma-separated")),
			mcp.WithString("bcc", mcp.Description("BCC recipients, comma-separated")),
			mcp.WithString("from_name", mcp.Description("Optional sender display name")),
			mcp.WithString("from_email", mcp.Description("Optional sender email; must equal the connected mailbox")),
		),
	}
}

func (a *Adapter) ValidateCredentials(ctx context.Context, raw map[string]any) (map[string]any, string, error) {
	creds, err := parseCredentials(raw)
	if err != nil {
		return nil, "", err
	}

	if creds.Host != "" {
		client, err := connect(ctx, creds)
		if err != nil {
			return nil, "", fmt.Errorf("SMTP authentication failed: %w", err)
		}
		if err := client.Quit(); err != nil {
			client.Close()
		}
		return normalizedCredentials(creds), creds.Email, nil
	}

	endpoints, err := discoverSMTPEndpoints(ctx, creds.Email)
	if err != nil {
		return nil, "", err
	}
	for _, endpoint := range endpoints {
		candidate := creds
		candidate.Host = endpoint.Host
		candidate.Port = endpoint.Port
		candidate.Security = endpoint.Security
		client, connectErr := connect(ctx, candidate)
		if connectErr != nil {
			continue
		}
		if err := client.Quit(); err != nil {
			client.Close()
		}
		return normalizedCredentials(candidate), candidate.Email, nil
	}
	return nil, "", fmt.Errorf("unable to connect to this mailbox; check the app password or SMTP authorization code")
}

func normalizedCredentials(creds credentials) map[string]any {
	return map[string]any{
		"email":     creds.Email,
		"password":  creds.Password,
		"smtp_host": creds.Host,
		"smtp_port": creds.Port,
		"username":  creds.Username,
		"security":  creds.Security,
	}
}

func (a *Adapter) Execute(ctx context.Context, toolName string, args map[string]any, token, _ string) (*mcp.CallToolResult, error) {
	if toolName != "send_email" {
		return mcp.NewToolResultError("unknown tool: " + toolName), nil
	}

	var creds credentials
	if err := json.Unmarshal([]byte(token), &creds); err != nil {
		return mcp.NewToolResultError("invalid stored SMTP credentials"), nil
	}
	if err := validateNormalizedCredentials(creds); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	fromEmail := strings.TrimSpace(argString(args, "from_email"))
	if fromEmail != "" && !strings.EqualFold(fromEmail, creds.Email) {
		return mcp.NewToolResultError("from_email must match the connected SMTP mailbox"), nil
	}
	fromEmail = creds.Email

	to, err := parseAddressList(argString(args, "to"), true)
	if err != nil {
		return mcp.NewToolResultError("invalid to address: " + err.Error()), nil
	}
	cc, err := parseAddressList(argString(args, "cc"), false)
	if err != nil {
		return mcp.NewToolResultError("invalid cc address: " + err.Error()), nil
	}
	bcc, err := parseAddressList(argString(args, "bcc"), false)
	if err != nil {
		return mcp.NewToolResultError("invalid bcc address: " + err.Error()), nil
	}
	recipients := append(append(append([]string{}, to...), cc...), bcc...)

	subject := strings.TrimSpace(argString(args, "subject"))
	body := argString(args, "body")
	if subject == "" || body == "" {
		return mcp.NewToolResultError("subject and body are required"), nil
	}

	attachments, err := parseAttachments(args["attachments"])
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	message, err := buildMessage(fromEmail, argString(args, "from_name"), to, cc, subject, body, attachments)
	if err != nil {
		return mcp.NewToolResultError("build email: " + err.Error()), nil
	}

	client, err := connect(ctx, creds)
	if err != nil {
		return mcp.NewToolResultError("SMTP connection failed: " + err.Error()), nil
	}
	defer client.Close()

	if err := client.Mail(fromEmail); err != nil {
		return mcp.NewToolResultError("SMTP MAIL FROM failed: " + err.Error()), nil
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("SMTP recipient %s rejected: %v", recipient, err)), nil
		}
	}
	w, err := client.Data()
	if err != nil {
		return mcp.NewToolResultError("SMTP DATA failed: " + err.Error()), nil
	}
	if _, err := w.Write(message); err != nil {
		w.Close()
		return mcp.NewToolResultError("SMTP message write failed: " + err.Error()), nil
	}
	if err := w.Close(); err != nil {
		return mcp.NewToolResultError("SMTP send failed: " + err.Error()), nil
	}
	if err := client.Quit(); err != nil {
		return mcp.NewToolResultError("SMTP send completed but session close failed: " + err.Error()), nil
	}

	result, _ := json.Marshal(map[string]any{
		"status":     "sent",
		"from":       fromEmail,
		"recipients": recipients,
		"subject":    subject,
	})
	return mcp.NewToolResultText(string(result)), nil
}

func parseCredentials(raw map[string]any) (credentials, error) {
	creds := credentials{
		Email:    strings.TrimSpace(argString(raw, "email")),
		Password: argString(raw, "password"),
		Host:     strings.TrimSpace(argString(raw, "smtp_host")),
		Username: strings.TrimSpace(argString(raw, "username")),
		Security: strings.ToLower(strings.TrimSpace(argString(raw, "security"))),
	}
	if creds.Username == "" {
		creds.Username = creds.Email
	}
	if err := validateMailboxCredentials(creds); err != nil {
		return credentials{}, err
	}
	if creds.Host == "" {
		return creds, nil
	}
	port, err := intValue(raw["smtp_port"])
	if err != nil {
		return credentials{}, fmt.Errorf("smtp_port must be 465 or 587")
	}
	creds.Port = port
	if creds.Security == "" {
		if creds.Port == 465 {
			creds.Security = "ssl"
		} else {
			creds.Security = "starttls"
		}
	}
	if err := validateNormalizedCredentials(creds); err != nil {
		return credentials{}, err
	}
	return creds, nil
}

func validateMailboxCredentials(creds credentials) error {
	addr, err := mail.ParseAddress(creds.Email)
	if err != nil || !strings.EqualFold(addr.Address, creds.Email) {
		return fmt.Errorf("a valid email is required")
	}
	if creds.Password == "" {
		return fmt.Errorf("password is required; use an app password when the provider supports it")
	}
	if creds.Username == "" {
		return fmt.Errorf("username is required")
	}
	return nil
}

func validateNormalizedCredentials(creds credentials) error {
	if err := validateMailboxCredentials(creds); err != nil {
		return err
	}
	if creds.Host == "" {
		return fmt.Errorf("smtp_host is required")
	}
	if net.ParseIP(creds.Host) != nil || strings.EqualFold(creds.Host, "localhost") || strings.HasSuffix(strings.ToLower(creds.Host), ".local") {
		return fmt.Errorf("smtp_host must be a public hostname")
	}
	if creds.Port != 465 && creds.Port != 587 {
		return fmt.Errorf("smtp_port must be 465 or 587")
	}
	if (creds.Port == 465 && creds.Security != "ssl") || (creds.Port == 587 && creds.Security != "starttls") {
		return fmt.Errorf("security must be ssl for port 465 or starttls for port 587")
	}
	return nil
}

func connect(ctx context.Context, creds credentials) (*smtp.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, smtpTimeout)
	defer cancel()

	address, err := resolvePublicAddress(ctx, creds.Host, creds.Port)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: smtpTimeout}
	tlsConfig := &tls.Config{ServerName: creds.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	if creds.Security == "ssl" {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, tlsConfig)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(smtpTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)

	client, err := smtp.NewClient(conn, creds.Host)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if creds.Security == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			client.Close()
			return nil, fmt.Errorf("SMTP server does not support STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			client.Close()
			return nil, err
		}
	}
	if err := authenticate(client, creds); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

func authenticate(client *smtp.Client, creds credentials) error {
	_, mechanisms := client.Extension("AUTH")
	upper := strings.ToUpper(mechanisms)
	var auth smtp.Auth
	if strings.Contains(upper, "PLAIN") {
		auth = smtp.PlainAuth("", creds.Username, creds.Password, creds.Host)
	} else if strings.Contains(upper, "LOGIN") {
		auth = &loginAuth{username: creds.Username, password: creds.Password}
	} else {
		return fmt.Errorf("SMTP server does not advertise PLAIN or LOGIN authentication")
	}
	return client.Auth(auth)
}

func resolvePublicAddress(ctx context.Context, host string, port int) (string, error) {
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return "", fmt.Errorf("resolve smtp_host: %w", err)
	}
	// Prefer IPv4 because some deployments resolve AAAA records without having
	// a working IPv6 route. Fall back to a public IPv6 address when needed.
	for _, ip := range ips {
		if ip.To4() != nil && isPublicIP(ip) {
			return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
		}
	}
	for _, ip := range ips {
		if isPublicIP(ip) {
			return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
		}
	}
	return "", fmt.Errorf("smtp_host does not resolve to a public IP")
}

func isPublicIP(ip net.IP) bool {
	return ip != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

type loginAuth struct {
	username string
	password string
	step     int
}

func (a *loginAuth) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	a.step = 0
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	prompt := strings.ToLower(string(fromServer))
	if strings.Contains(prompt, "user") || a.step == 0 {
		a.step++
		return []byte(a.username), nil
	}
	if strings.Contains(prompt, "pass") || a.step == 1 {
		a.step++
		return []byte(a.password), nil
	}
	return nil, fmt.Errorf("unexpected SMTP AUTH LOGIN challenge")
}

func buildMessage(fromEmail, fromName string, to, cc []string, subject, body string, attachments []emailAttachment) ([]byte, error) {
	var buf bytes.Buffer
	from := (&mail.Address{Name: sanitizeHeader(fromName), Address: fromEmail}).String()
	writeHeader(&buf, "From", from)
	writeHeader(&buf, "To", strings.Join(to, ", "))
	if len(cc) > 0 {
		writeHeader(&buf, "Cc", strings.Join(cc, ", "))
	}
	writeHeader(&buf, "Subject", mime.QEncoding.Encode("UTF-8", sanitizeHeader(subject)))
	writeHeader(&buf, "Date", time.Now().Format(time.RFC1123Z))
	writeHeader(&buf, "MIME-Version", "1.0")

	contentType := "text/plain"
	trimmedBody := strings.ToLower(strings.TrimSpace(body))
	if strings.HasPrefix(trimmedBody, "<!doctype html") || strings.HasPrefix(trimmedBody, "<html") {
		contentType = "text/html"
	}

	if len(attachments) == 0 {
		writeHeader(&buf, "Content-Type", contentType+`; charset="UTF-8"`)
		writeHeader(&buf, "Content-Transfer-Encoding", "quoted-printable")
		buf.WriteString("\r\n")
		writer := quotedprintable.NewWriter(&buf)
		if _, err := writer.Write([]byte(body)); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	mixed := multipart.NewWriter(&buf)
	writeHeader(&buf, "Content-Type", fmt.Sprintf("multipart/mixed; boundary=%q", mixed.Boundary()))
	buf.WriteString("\r\n")
	bodyHeader := textproto.MIMEHeader{}
	bodyHeader.Set("Content-Type", contentType+`; charset="UTF-8"`)
	bodyHeader.Set("Content-Transfer-Encoding", "quoted-printable")
	bodyPart, err := mixed.CreatePart(bodyHeader)
	if err != nil {
		return nil, err
	}
	qp := quotedprintable.NewWriter(bodyPart)
	if _, err := qp.Write([]byte(body)); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}

	for _, attachment := range attachments {
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", fmt.Sprintf("%s; name=%q", attachment.ContentType, attachment.Filename))
		header.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", attachment.Filename))
		header.Set("Content-Transfer-Encoding", "base64")
		part, err := mixed.CreatePart(header)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(part, wrapBase64(attachment.Content)); err != nil {
			return nil, err
		}
	}
	if err := mixed.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func parseAttachments(raw any) ([]emailAttachment, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("attachments must be an array")
	}
	result := make([]emailAttachment, 0, len(items))
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("attachment %d must be an object", i+1)
		}
		filename := sanitizeHeader(argString(obj, "filename"))
		content := strings.TrimSpace(argString(obj, "content"))
		contentType := sanitizeHeader(argString(obj, "content_type"))
		if filename == "" || content == "" {
			return nil, fmt.Errorf("attachment %d filename and content are required", i+1)
		}
		if comma := strings.Index(content, ","); comma >= 0 && strings.Contains(content[:comma], "base64") {
			content = content[comma+1:]
		}
		content = strings.ReplaceAll(strings.ReplaceAll(content, "\r", ""), "\n", "")
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, fmt.Errorf("attachment %s content must be base64", filename)
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		result = append(result, emailAttachment{Filename: filename, Content: decoded, ContentType: contentType})
	}
	return result, nil
}

func parseAddressList(value string, required bool) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return nil, fmt.Errorf("at least one recipient is required")
		}
		return nil, nil
	}
	addresses, err := mail.ParseAddressList(value)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(addresses))
	for _, address := range addresses {
		result = append(result, address.Address)
	}
	return result, nil
}

func writeHeader(buf *bytes.Buffer, name, value string) {
	buf.WriteString(name + ": " + sanitizeHeader(value) + "\r\n")
}

func sanitizeHeader(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r", ""), "\n", "")
}

func wrapBase64(data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for len(encoded) > 76 {
		b.WriteString(encoded[:76])
		b.WriteString("\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded)
	b.WriteString("\r\n")
	return b.String()
}

func argString(args map[string]any, key string) string {
	if value, ok := args[key].(string); ok {
		return value
	}
	return ""
}

func intValue(value any) (int, error) {
	switch v := value.(type) {
	case int:
		return v, nil
	case float64:
		return int(v), nil
	case json.Number:
		i, err := v.Int64()
		return int(i), err
	case string:
		return strconv.Atoi(v)
	default:
		return 0, fmt.Errorf("invalid integer")
	}
}
