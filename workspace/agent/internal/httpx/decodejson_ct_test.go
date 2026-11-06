package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONContentType(t *testing.T) {
	cases := []struct {
		ct   string
		want bool
	}{
		{"application/json", true},
		{"application/json; charset=utf-8", true},
		{"", true},
		{"text/plain", false},
		{"text/plain;charset=UTF-8", false},
		{"application/x-www-form-urlencoded", false},
		{"multipart/form-data; boundary=x", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"a":1}`))
		if c.ct != "" {
			r.Header.Set("Content-Type", c.ct)
		}
		w := httptest.NewRecorder()
		var v map[string]any
		if got := DecodeJSON(w, r, &v); got != c.want {
			t.Errorf("%q: ok=%v want %v", c.ct, got, c.want)
		}
		if !c.want && w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%q: status %d, want 415", c.ct, w.Code)
		}
	}
}
