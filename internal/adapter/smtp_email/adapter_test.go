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

func TestParseCredentialsAllowsAutomaticServerDiscovery(t *testing.T) {
	creds, err := parseCredentials(map[string]any{
		"email":    "user@gmail.com",
		"password": "app-password",
	})
	if err != nil {
		t.Fatalf("parseCredentials: %v", err)
	}
	if creds.Host != "" || creds.Port != 0 {
		t.Fatalf("transport should be discovered later: %#v", creds)
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

func TestKnownSMTPDomainDiscovery(t *testing.T) {
	endpoints, err := discoverSMTPEndpoints(t.Context(), "user@gmail.com")
	if err != nil {
		t.Fatalf("discoverSMTPEndpoints: %v", err)
	}
	want := smtpEndpoint{Host: "smtp.gmail.com", Port: 587, Security: "starttls"}
	if len(endpoints) != 1 || endpoints[0] != want {
		t.Fatalf("endpoints = %#v, want %#v", endpoints, want)
	}
}

func TestMXProviderDiscovery(t *testing.T) {
	tests := map[string]smtpEndpoint{
		"aspmx.l.google.com.":                {Host: "smtp.gmail.com", Port: 587, Security: "starttls"},
		"tenant.mail.protection.outlook.com": {Host: "smtp.office365.com", Port: 587, Security: "starttls"},
		"mx1.mxhichina.com.":                 {Host: "smtp.qiye.aliyun.com", Port: 465, Security: "ssl"},
		"mx1.feishu.cn.":                     {Host: "smtp.feishu.cn", Port: 465, Security: "ssl"},
	}
	for host, want := range tests {
		got, ok := endpointForMXHost(host)
		if !ok || got != want {
			t.Fatalf("endpointForMXHost(%q) = %#v, %v; want %#v", host, got, ok, want)
		}
	}
}
