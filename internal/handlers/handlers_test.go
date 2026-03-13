package handlers

import (
	"net/url"
	"testing"
)

// ─── requestBaseURL ───────────────────────────────────────────────────────────

func TestRequestBaseURL(t *testing.T) {
	tests := []struct {
		urlStr string
		host   string
		tls    bool
		want   string
	}{
		{"http://example.com/path", "example.com", false, "http://example.com"},
		{"http://example.com/path", "example.com", true, "https://example.com"},
		// Host header takes precedence when url.Host is empty
		{"", "myhost:8090", false, "http://myhost:8090"},
		// url.Host overrides host parameter when set
		{"http://fromurl.com/api", "ignored", false, "http://fromurl.com"},
		// https scheme in URL → https even without TLS flag
		{"https://secure.example.com/api", "secure.example.com", false, "https://secure.example.com"},
	}

	for _, tt := range tests {
		u, _ := url.Parse(tt.urlStr)
		got := requestBaseURL(u, tt.host, tt.tls)
		if got != tt.want {
			t.Errorf("requestBaseURL(%q, %q, %v) = %q, want %q",
				tt.urlStr, tt.host, tt.tls, got, tt.want)
		}
	}
}

// ─── intParam ─────────────────────────────────────────────────────────────────

func TestIntParam(t *testing.T) {
	tests := []struct {
		s    string
		def  int
		want int
	}{
		{"42", 0, 42},
		{"0", 99, 0},
		{"-5", 0, -5},
		{"", 7, 7},
		{"abc", 3, 3},
		{"1.5", 1, 1},
	}

	for _, tt := range tests {
		got := intParam(tt.s, tt.def)
		if got != tt.want {
			t.Errorf("intParam(%q, %d) = %d, want %d", tt.s, tt.def, got, tt.want)
		}
	}
}

// ─── clamp ────────────────────────────────────────────────────────────────────

func TestClamp(t *testing.T) {
	tests := []struct {
		v, lo, hi int
		want      int
	}{
		{5, 1, 10, 5},
		{0, 1, 10, 1},
		{11, 1, 10, 10},
		{1, 1, 10, 1},
		{10, 1, 10, 10},
	}

	for _, tt := range tests {
		got := clamp(tt.v, tt.lo, tt.hi)
		if got != tt.want {
			t.Errorf("clamp(%d, %d, %d) = %d, want %d", tt.v, tt.lo, tt.hi, got, tt.want)
		}
	}
}
