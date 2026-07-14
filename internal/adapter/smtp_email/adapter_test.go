package smtpemail

import (
	"encoding/base64"
	"net"
	"strings"
	"testing"
)

func TestParseCredentialsUsesExistingRequestFields(t *testing.T) {
	creds, err := parseCredentials(map[string]any{
		"email":     "user@example.com",
		"password":  "app-password",
		"smtp_host": "smtp.example.com",
		"smtp_port": float64(465),
	})
	if err != nil {
		t.Fatalf("parseCredentials: %v", err)
	}
	if creds.Username != "user@example.com" {
		t.Fatalf("Username = %q", creds.Username)
	}
	if creds.Security != "ssl" {
		t.Fatalf("Security = %q", creds.Security)
	}
}

func TestParseCredentialsDefaultsStartTLS(t *testing.T) {
	creds, err := parseCredentials(map[string]any{
		"email":     "user@example.com",
		"password":  "app-password",
		"smtp_host": "smtp.example.com",
		"smtp_port": "587",
	})
	if err != nil {
		t.Fatalf("parseCredentials: %v", err)
	}
	if creds.Security != "starttls" {
		t.Fatalf("Security = %q", creds.Security)
	}
}

func TestParseCredentialsRejectsUnsafeConfiguration(t *testing.T) {
	tests := []map[string]any{
		{"email": "user@example.com", "password": "x", "smtp_host": "127.0.0.1", "smtp_port": 465},
		{"email": "user@example.com", "password": "x", "smtp_host": "smtp.example.com", "smtp_port": 25},
		{"email": "user@example.com", "password": "x", "smtp_host": "smtp.example.com", "smtp_port": 465, "security": "starttls"},
	}
	for _, input := range tests {
		if _, err := parseCredentials(input); err == nil {
			t.Fatalf("expected validation failure for %#v", input)
		}
	}
}

func TestBuildMessageWithAttachment(t *testing.T) {
	message, err := buildMessage(
		"sender@example.com",
		"Sender",
		[]string{"to@example.com"},
		[]string{"cc@example.com"},
		"中文主题",
		"hello",
		[]emailAttachment{{Filename: "test.txt", Content: []byte("attachment"), ContentType: "text/plain"}},
	)
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}
	text := string(message)
	for _, expected := range []string{"multipart/mixed", "test.txt", base64.StdEncoding.EncodeToString([]byte("attachment"))} {
		if !strings.Contains(text, expected) {
			t.Fatalf("message does not contain %q:\n%s", expected, text)
		}
	}
	if strings.Contains(strings.ToLower(text), "bcc:") {
		t.Fatal("message must not expose Bcc recipients")
	}
}

func TestIsPublicIP(t *testing.T) {
	if isPublicIP(net.ParseIP("127.0.0.1")) || isPublicIP(net.ParseIP("10.0.0.1")) {
		t.Fatal("private IP accepted")
	}
	if !isPublicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public IP rejected")
	}
}
