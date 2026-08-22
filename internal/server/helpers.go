package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// maxPerPage caps a page. A larger value is clamped rather than refused: it is a
// request for more than the service will give, not a malformed one.
const maxPerPage = 200

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func badRequest(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
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

// intParam reads an integer query parameter. An absent or empty value takes the
// default; a value that is not a number, or is below the minimum this parameter
// can mean, is a client error — every other value is used as given.
func intParam(q url.Values, name string, def, minimum int) (int, error) {
	raw := q.Get(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", name, raw)
	}
	if n < minimum {
		return 0, fmt.Errorf("%s must be %d or greater, got %d", name, minimum, n)
	}
	return n, nil
}

// boolParam reads a tri-state flag: absent means "either", true and false mean
// what they say.
func boolParam(q url.Values, name string) (*bool, error) {
	raw := q.Get(name)
	if raw == "" {
		return nil, nil
	}
	switch raw {
	case "true":
		v := true
		return &v, nil
	case "false":
		v := false
		return &v, nil
	default:
		return nil, fmt.Errorf("%s must be true or false, got %q", name, raw)
	}
}
