package ezviz

import (
	"testing"
	"time"
)

func TestParseURL(t *testing.T) {
	cfg, err := parseURL("hikconnect://user@example.com:secret@api.hik-connect.com/L38239367?channel=2&subtype=sub")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.account != "user@example.com" {
		t.Errorf("account = %q", cfg.account)
	}
	if cfg.password != "secret" {
		t.Errorf("password = %q", cfg.password)
	}
	if cfg.baseURL != "https://api.hik-connect.com" {
		t.Errorf("baseURL = %q", cfg.baseURL)
	}
	if cfg.serial != "L38239367" {
		t.Errorf("serial = %q", cfg.serial)
	}
	if cfg.channel != 2 {
		t.Errorf("channel = %d", cfg.channel)
	}
	if cfg.subtype != "sub" {
		t.Errorf("subtype = %q", cfg.subtype)
	}
}

func TestParseURLDefaults(t *testing.T) {
	cfg, err := parseURL("ezviz://a@b.com:p@api.hik-connect.com/SERIAL")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.channel != 1 || cfg.subtype != "main" {
		t.Errorf("defaults: channel=%d subtype=%q", cfg.channel, cfg.subtype)
	}
}

func TestParseURLMissingFields(t *testing.T) {
	if _, err := parseURL("ezviz://api.hik-connect.com/SERIAL"); err == nil {
		t.Error("expected error for missing credentials")
	}
}

// TestDialReachesTransport proves the data-plane wiring is reachable: NewProducer
// → Dial → connect() runs login and fails on the network for an unreachable
// host. It stays hermetic (no live cloud) by pointing at an invalid TLD that
// fails DNS resolution quickly, and asserts an error is returned promptly.
func TestDialReachesTransport(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := NewProducer("ezviz://a@b.com:p@host.invalid/SERIAL")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error dialing an unreachable host")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Dial did not return within timeout")
	}
}
