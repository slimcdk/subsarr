package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func requestBaseURL(u *url.URL, host string, tls bool) string {
	scheme := "http"
	if tls || u.Scheme == "https" {
		scheme = "https"
	}
	if h := u.Host; h != "" {
		host = h
	}
	return scheme + "://" + host
}

func intParam(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
