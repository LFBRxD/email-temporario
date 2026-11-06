package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"DOMAIN": "Mail.Example.com"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Domain != "mail.example.com" || c.TTL.Seconds() != 300 || c.MaxStoreBytes != 1<<30 || c.TrustProxy || c.ExposeStats {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestDomainRequiredAndValidated(t *testing.T) {
	if _, err := Load(env(nil)); err == nil || !strings.Contains(err.Error(), "DOMAIN is required") {
		t.Fatalf("missing DOMAIN must fail, got %v", err)
	}
	for _, bad := range []string{"a b.com", "x@y.com", "-a.com", "a..com", "http://x.com"} {
		if _, err := Load(env(map[string]string{"DOMAIN": bad})); err == nil {
			t.Errorf("DOMAIN %q must be rejected", bad)
		}
	}
}

func TestInvalidNumbersFailLoudly(t *testing.T) {
	_, err := Load(env(map[string]string{"DOMAIN": "a.com", "TTL_SECONDS": "abc", "MAX_STORE_MB": "0", "TRUST_PROXY": "maybe"}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, k := range []string{"TTL_SECONDS", "MAX_STORE_MB", "TRUST_PROXY"} {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("error should mention %s: %v", k, err)
		}
	}
}

func TestMessageLargerThanStoreRejected(t *testing.T) {
	_, err := Load(env(map[string]string{"DOMAIN": "a.com", "MAX_STORE_MB": "1", "MAX_MSG_BYTES": "9999999"}))
	if err == nil {
		t.Fatal("MAX_MSG_BYTES > store must fail")
	}
}
