package httpapi

import (
	"net/http"
	"net/http/httptest"
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

func TestCredentialsPageAllowsGatewayProxiedFormSubmission(t *testing.T) {
	recorder := httptest.NewRecorder()
	renderCredentialsPage(recorder, http.StatusOK, credentialsPageData{State: "state-token"})

	if csp := recorder.Header().Get("Content-Security-Policy"); strings.Contains(csp, "form-action") {
		t.Fatalf("CSP must not block the gateway-proxied form action: %q", csp)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `<form method="post">`) {
		t.Fatalf("credentials page does not contain the POST form")
	}
}
