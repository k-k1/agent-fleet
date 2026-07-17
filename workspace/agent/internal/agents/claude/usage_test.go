package claude

import (
	"net/http"
	"testing"
	"time"
)

func TestRetryAfter(t *testing.T) {
	resp := func(v string) *http.Response {
		h := http.Header{}
		if v != "" {
			h.Set("Retry-After", v)
		}
		return &http.Response{Header: h}
	}

	// delta-seconds
	if got := retryAfter(resp("668")); got != 668*time.Second {
		t.Errorf("668s: got %v, want 668s", got)
	}
	if got := retryAfter(resp("  30 ")); got != 30*time.Second {
		t.Errorf("padded 30s: got %v, want 30s", got)
	}
	// absent / non-positive / garbage → 0
	for _, v := range []string{"", "0", "-5", "soon"} {
		if got := retryAfter(resp(v)); got != 0 {
			t.Errorf("%q: got %v, want 0", v, got)
		}
	}
	// HTTP-date in the future → positive; in the past → 0
	future := time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)
	if got := retryAfter(resp(future)); got <= 0 || got > 2*time.Minute {
		t.Errorf("future date: got %v, want (0, 2m]", got)
	}
	past := time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)
	if got := retryAfter(resp(past)); got != 0 {
		t.Errorf("past date: got %v, want 0", got)
	}
}
