package usagex

import (
	"context"
	"testing"
	"time"
)

// A call's route and connection, resolved when it started, are on every row it records.
func TestRecordCallCarriesRouteAndConnection(t *testing.T) {
	t.Setenv("AF_USAGE_DIR", t.TempDir())
	t.Setenv("AF_USAGE_RECORD", "")
	c := Call{Kind: "codex", ModelReq: "m", Route: "openai-api", Connection: "codex-api-key@aa", OK: true,
		Models: []ModelRow{{Model: "a", Tokens: Tokens{Out: 1}}, {Model: "b", Tokens: Tokens{Out: 2}}}}
	RecordCall(context.Background(), &c, time.Now())
	rows := ReadRows()
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Route != "openai-api" || r.Connection != "codex-api-key@aa" {
			t.Errorf("row = route %q connection %q", r.Route, r.Connection)
		}
	}
}
