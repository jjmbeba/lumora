package config

import "testing"

func TestResendFromAddressPrefersCanonicalNameAndSupportsLegacyName(t *testing.T) {
	t.Setenv("RESEND_EMAIL_FROM", "canonical@example.com")
	t.Setenv("RESEND_FROM_EMAIL", "legacy@example.com")
	got, err := ResendFromAddress()
	if err != nil || got != "canonical@example.com" {
		t.Fatalf("ResendFromAddress() = %q, %v; want canonical value", got, err)
	}

	t.Setenv("RESEND_EMAIL_FROM", "")
	got, err = ResendFromAddress()
	if err != nil || got != "legacy@example.com" {
		t.Fatalf("ResendFromAddress() = %q, %v; want legacy fallback", got, err)
	}
}
