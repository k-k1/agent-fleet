// Package httpx は Agent の HTTP ヘルパー（docs/23 P1-W5 で main から機械的に
// 抽出）。JSON の書き出し/読み込みと、トークンゲート・リクエストログの両ミドル
// ウェアを集約する。
package httpx

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
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

func LogRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(start).Round(time.Millisecond))
	})
}
