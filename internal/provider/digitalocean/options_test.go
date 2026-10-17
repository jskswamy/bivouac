package digitalocean

import (
	"testing"
	"time"
)

func TestNew_DefaultsToTheRealAPI(t *testing.T) {
	p, err := New("tok")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := p.client.BaseURL.String(); got != "https://api.digitalocean.com/" {
		t.Errorf("BaseURL = %q, want the public API", got)
	}
	if p.pollInterval != 5*time.Second {
		t.Errorf("pollInterval = %v, want 5s", p.pollInterval)
	}
}

func TestWithBaseURL_AddsTrailingSlash(t *testing.T) {
	// godo resolves "v2/images" against the base, so without the slash
	// a path prefix would lose its last segment.
	for raw, want := range map[string]string{
		"http://127.0.0.1:8080":     "http://127.0.0.1:8080/",
		"http://127.0.0.1:8080/":    "http://127.0.0.1:8080/",
		"https://proxy.example/do":  "https://proxy.example/do/",
		"https://proxy.example/do/": "https://proxy.example/do/",
	} {
		p, err := New("tok", WithBaseURL(raw))
		if err != nil {
			t.Errorf("WithBaseURL(%q): %v", raw, err)
			continue
		}
		if got := p.client.BaseURL.String(); got != want {
			t.Errorf("WithBaseURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestWithBaseURL_RejectsURLWithoutSchemeOrHost(t *testing.T) {
	for _, raw := range []string{"", "::", "localhost:8080", "/v2", "http://", "api.digitalocean.com"} {
		if _, err := New("tok", WithBaseURL(raw)); err == nil {
			t.Errorf("WithBaseURL(%q) accepted, want an error", raw)
		}
	}
}

func TestWithPollInterval(t *testing.T) {
	p, err := New("tok", WithPollInterval(time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.pollInterval != time.Millisecond {
		t.Errorf("pollInterval = %v, want 1ms", p.pollInterval)
	}
	for _, d := range []time.Duration{0, -time.Second} {
		if _, err := New("tok", WithPollInterval(d)); err == nil {
			t.Errorf("WithPollInterval(%v) accepted, want an error", d)
		}
	}
}
