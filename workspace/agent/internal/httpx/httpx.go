// Package httpx holds the Agent's HTTP helpers: writing and reading JSON, plus the token-gate
// and request-log middlewares.
package httpx

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// RequireToken enforces the CP↔Agent shared token (docs/07 §7.5): the Control
// Plane injects AGENT_TOKEN at container start and presents it as a Bearer on
// every request. /healthz stays open (used for the startup readiness probe).
// If AGENT_TOKEN is unset the gate is disabled — a safety valve for manual
// debugging; the CP always injects one in normal operation.
func RequireToken(next http.Handler) http.Handler {
	token := os.Getenv("AGENT_TOKEN")
	if token == "" {
		log.Printf("WARNING: AGENT_TOKEN unset — CP↔Agent auth disabled (dev only)")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			WriteErr(w, http.StatusUnauthorized, "unauthorized", "missing or invalid agent token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteErr(w http.ResponseWriter, status int, code, msg string) {
	WriteJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

// DecodeJSON parses the request body into v; on failure it writes the shared
// 400 bad_request response and returns false — the caller just returns.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		WriteErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	return true
}

// StrictJSONError is the stable application error produced by DecodeStrictJSON.
// It is intentionally small so callers can map it through the same WriteErr
// envelope as the rest of the Agent API.
type StrictJSONError struct {
	Status  int
	Code    string
	Message string
}

// DecodeStrictJSON reads one JSON object under a hard wire-size limit. In
// addition to encoding/json's syntax checks it rejects invalid wire UTF-8 and
// lone UTF-16 surrogate escapes before encoding/json can replace them with
// U+FFFD. Unknown fields and a second trailing JSON value are rejected.
func DecodeStrictJSON(r *http.Request, v any, maxBytes int64) *StrictJSONError {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		return &StrictJSONError{
			Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type",
			Message: "Content-Type must be application/json",
		}
	}
	if r.ContentLength > maxBytes {
		return &StrictJSONError{
			Status: http.StatusRequestEntityTooLarge, Code: "too_large",
			Message: "JSON body exceeds the size limit",
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		return &StrictJSONError{Status: http.StatusBadRequest, Code: "bad_request", Message: "cannot read JSON body"}
	}
	if int64(len(body)) > maxBytes {
		return &StrictJSONError{
			Status: http.StatusRequestEntityTooLarge, Code: "too_large",
			Message: "JSON body exceeds the size limit",
		}
	}
	if !utf8.Valid(body) {
		return &StrictJSONError{Status: http.StatusBadRequest, Code: "bad_request", Message: "JSON body is not valid UTF-8"}
	}
	if err := validateJSONSurrogates(body); err != nil {
		return &StrictJSONError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
	}

	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &StrictJSONError{Status: http.StatusBadRequest, Code: "bad_request", Message: "invalid JSON body"}
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return &StrictJSONError{Status: http.StatusBadRequest, Code: "bad_request", Message: "JSON body must contain exactly one value"}
	}
	return nil
}

func isJSONContentType(value string) bool {
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil || mediaType != "application/json" {
		return false
	}
	if len(params) == 0 {
		return true
	}
	charset, ok := params["charset"]
	return len(params) == 1 && ok && strings.EqualFold(charset, "utf-8")
}

// validateJSONSurrogates walks JSON string tokens without decoding them. A
// backslash escaped as "\\" is consumed as one escape, so text such as
// "\\ud800" is not mistaken for a Unicode escape.
func validateJSONSurrogates(body []byte) error {
	inString := false
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '"':
			inString = !inString
		case '\\':
			if !inString {
				continue
			}
			i++
			if i >= len(body) {
				return fmt.Errorf("invalid JSON string escape")
			}
			if body[i] != 'u' {
				continue
			}
			code, ok := jsonHex4(body, i+1)
			if !ok {
				return fmt.Errorf("invalid JSON Unicode escape")
			}
			i += 4
			switch {
			case code >= 0xd800 && code <= 0xdbff:
				if i+6 >= len(body) || body[i+1] != '\\' || body[i+2] != 'u' {
					return fmt.Errorf("lone high surrogate in JSON string")
				}
				low, ok := jsonHex4(body, i+3)
				if !ok || low < 0xdc00 || low > 0xdfff {
					return fmt.Errorf("lone high surrogate in JSON string")
				}
				i += 6
			case code >= 0xdc00 && code <= 0xdfff:
				return fmt.Errorf("lone low surrogate in JSON string")
			}
		}
	}
	return nil
}

func jsonHex4(body []byte, start int) (uint16, bool) {
	if start+4 > len(body) {
		return 0, false
	}
	var value uint16
	for _, c := range body[start : start+4] {
		value <<= 4
		switch {
		case c >= '0' && c <= '9':
			value += uint16(c - '0')
		case c >= 'a' && c <= 'f':
			value += uint16(c-'a') + 10
		case c >= 'A' && c <= 'F':
			value += uint16(c-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func LogRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(start).Round(time.Millisecond))
	})
}
