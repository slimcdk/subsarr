package server

import (
	"net/url"
	"testing"
)

func TestRequestBaseURL(t *testing.T) {
	tests := []struct {
		urlStr string
		host   string
		tls    bool
		want   string
	}{
		{"http://example.com/path", "example.com", false, "http://example.com"},
		{"http://example.com/path", "example.com", true, "https://example.com"},
		{"", "myhost:8090", false, "http://myhost:8090"},
		{"http://fromurl.com/api", "ignored", false, "http://fromurl.com"},
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

func TestIntParam(t *testing.T) {
	tests := []struct {
		raw     string
		def     int
		minimum int
		want    int
		wantErr bool
	}{
		{"42", 0, 0, 42, false},
		{"", 7, 1, 7, false},
		{"0", 99, 0, 0, false},
		{"0", 1, 1, 0, true},
		{"-5", 0, 0, 0, true},
		{"abc", 3, 0, 0, true},
		{"1.5", 1, 0, 0, true},
	}

	for _, tt := range tests {
		q := url.Values{}
		if tt.raw != "" {
			q.Set("n", tt.raw)
		}
		got, err := intParam(q, "n", tt.def, tt.minimum)
		if (err != nil) != tt.wantErr {
			t.Errorf("intParam(%q, min %d) error = %v, wantErr %v", tt.raw, tt.minimum, err, tt.wantErr)
			continue
		}
		if err == nil && got != tt.want {
			t.Errorf("intParam(%q) = %d, want %d", tt.raw, got, tt.want)
		}
	}
}

func TestBoolParam(t *testing.T) {
	tests := []struct {
		raw     string
		want    *bool
		wantErr bool
	}{
		{"", nil, false},
		{"true", ptr(true), false},
		{"false", ptr(false), false},
		{"TRUE", nil, true},
		{"1", nil, true},
		{"yes", nil, true},
	}

	for _, tt := range tests {
		q := url.Values{}
		if tt.raw != "" {
			q.Set("hi", tt.raw)
		}
		got, err := boolParam(q, "hi")
		if (err != nil) != tt.wantErr {
			t.Errorf("boolParam(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		switch {
		case tt.want == nil && got != nil:
			t.Errorf("boolParam(%q) = %v, want nil", tt.raw, *got)
		case tt.want != nil && (got == nil || *got != *tt.want):
			t.Errorf("boolParam(%q) = %v, want %v", tt.raw, got, *tt.want)
		}
	}
}

func TestDecodeReleases(t *testing.T) {
	tests := []struct {
		raw  string
		want int
	}{
		{`["a","b"]`, 2},
		{`[]`, 0},
		{``, 0},
		{`null`, 0},
		{`{"not":"an array"}`, 0},
		{`garbage`, 0},
	}
	for _, tt := range tests {
		got := decodeReleases(tt.raw)
		if got == nil {
			t.Errorf("decodeReleases(%q) = nil, want an array", tt.raw)
			continue
		}
		if len(got) != tt.want {
			t.Errorf("decodeReleases(%q) = %v, want %d entries", tt.raw, got, tt.want)
		}
	}
}
