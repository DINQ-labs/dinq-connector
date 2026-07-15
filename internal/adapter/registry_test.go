package adapter

import "testing"

func TestLegacyMailAliasesUseBuiltInAdapters(t *testing.T) {
	tests := map[string]string{
		"imap":      "smtp_email",
		"nylas":     "smtp_email",
		"microsoft": "outlook",
		"outlook":   "outlook",
	}
	for input, want := range tests {
		if got := ResolveName(input); got != want {
			t.Fatalf("ResolveName(%q) = %q, want %q", input, got, want)
		}
	}
}
