package httpapi

import (
	"strings"
	"testing"
)

func TestConnectedRedirectURLPreservesExistingQuery(t *testing.T) {
	got, err := connectedRedirectURL("https://dev.dinq.me/settings?tab=email", "smtp_email")
	if err != nil {
		t.Fatalf("connectedRedirectURL: %v", err)
	}
	for _, expected := range []string{"tab=email", "status=connected", "platform=smtp_email"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("redirect URL %q does not contain %q", got, expected)
		}
	}
}

func TestConnectedRedirectURLSupportsAppScheme(t *testing.T) {
	got, err := connectedRedirectURL("dinq://connector/callback", "smtp_email")
	if err != nil {
		t.Fatalf("connectedRedirectURL: %v", err)
	}
	if !strings.HasPrefix(got, "dinq://connector/callback?") {
		t.Fatalf("redirect URL = %q", got)
	}
}
