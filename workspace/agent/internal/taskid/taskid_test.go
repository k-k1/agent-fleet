package taskid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTaskIDFromARN(t *testing.T) {
	for arn, want := range map[string]string{
		"arn:aws:ecs:ap-northeast-1:123456789012:task/af-cluster/0123456789abcdef0123456789abcdef": "0123456789abcdef0123456789abcdef",
		"": "", "no-slash": "", "arn:x/": "", "arn:x/bad id": "", "arn:x/" + strings.Repeat("a", 129): "",
	} {
		if got := taskIDFromARN(arn); got != want {
			t.Errorf("taskIDFromARN(%q) = %q, want %q", arn, got, want)
		}
	}
}

func TestFetchTaskID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/task" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"TaskARN":"arn:aws:ecs:r:123456789012:task/c/abc123"}`))
	}))
	defer srv.Close()
	if got := fetchTaskID(context.Background(), srv.URL); got != "abc123" {
		t.Fatalf("got %q", got)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 500) }))
	defer bad.Close()
	if got := fetchTaskID(context.Background(), bad.URL); got != "" {
		t.Fatalf("500 gave %q", got)
	}
	if got := fetchTaskID(context.Background(), "http://127.0.0.1:1"); got != "" {
		t.Fatalf("unreachable gave %q", got)
	}
}
