package sessionx

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// stubPane makes readPermissionMenu parse pane instead of capturing tmux.
func stubPane(t *testing.T, pane string) {
	t.Helper()
	prev := readPermissionMenu
	readPermissionMenu = func(string) (claude.PermissionMenu, bool) { return claude.ParsePermissionMenu(pane) }
	t.Cleanup(func() { readPermissionMenu = prev })
}

func permFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "agents", "claude", "testdata", "permmenu", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestPermissionKeysByLabel pins the keystroke per choice on each measured menu (#3354). The
// two-row menu is the regression: position-fixed keys answered "always" with No and "deny"
// with Yes there.
func TestPermissionKeysByLabel(t *testing.T) {
	type want struct {
		keys []string
		code string
	}
	cases := []struct {
		file string
		want map[string]want
	}{
		{"bash_ask_rule_two_options.txt", map[string]want{
			claude.PermAllow:  {[]string{"1"}, ""},
			claude.PermAlways: {nil, "choice_unavailable"},
			claude.PermDeny:   {[]string{"2"}, ""},
		}},
		{"bash_four_options.txt", map[string]want{
			claude.PermAllow:  {[]string{"1"}, ""},
			claude.PermAlways: {[]string{"2"}, ""},
			claude.PermDeny:   {[]string{"4"}, ""},
		}},
		{"write_three_options.txt", map[string]want{
			claude.PermAllow:  {[]string{"1"}, ""},
			claude.PermAlways: {[]string{"2"}, ""},
			claude.PermDeny:   {[]string{"3"}, ""},
		}},
	}
	for _, c := range cases {
		stubPane(t, permFixture(t, c.file))
		for choice, w := range c.want {
			keys, code := permissionKeys("s", choice)
			if !reflect.DeepEqual(keys, w.keys) || code != w.code {
				t.Errorf("%s %s: keys=%v code=%q, want %v %q", c.file, choice, keys, code, w.keys, w.code)
			}
		}
	}
}

// With no readable menu, deny still cancels (Escape needs no layout) and the others refuse.
func TestPermissionKeysUnreadableMenu(t *testing.T) {
	stubPane(t, "❯ \n  ⏸ manual mode on\n")
	if keys, code := permissionKeys("s", claude.PermDeny); !reflect.DeepEqual(keys, []string{"Escape"}) || code != "" {
		t.Errorf("deny: %v %q", keys, code)
	}
	for _, c := range []string{claude.PermAllow, claude.PermAlways} {
		if keys, code := permissionKeys("s", c); keys != nil || code != "menu_unreadable" {
			t.Errorf("%s: %v %q", c, keys, code)
		}
	}
	if got := permissionChoices("s"); got != nil {
		t.Errorf("choices = %v, want nil", got)
	}
}

// TestPermissionRespondGuards pins the preconditions that fail before any key is sent.
func TestPermissionRespondGuards(t *testing.T) {
	withTempHome(t)
	do := func(name, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/permission-respond", strings.NewReader(body))
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		HandleSessionPermissionRespond(rec, req)
		return rec
	}
	if rec := do("slot71", `{"choice":"allow"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session: %d %s", rec.Code, rec.Body.String())
	}
	session.WriteMeta(session.Meta{Name: "slot72", Dir: t.TempDir(), Kind: session.KindCodex})
	if rec := do("slot72", `{"choice":"allow"}`); rec.Code != http.StatusConflict {
		t.Fatalf("a kind without a pending permission payload: %d %s", rec.Code, rec.Body.String())
	}
	meta73 := session.Meta{Name: "slot73", Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(meta73)
	if rec := do("slot73", `{"choice":"maybe"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad choice: %d %s", rec.Code, rec.Body.String())
	}
	// The permission state alone, without the hook's payload, is not a dialog to answer.
	status.Persist(session.UUID(meta73.Dir, "slot73"), "permission")
	if rec := do("slot73", `{"choice":"allow"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no_permission") {
		t.Fatalf("no pending permission: %d %s", rec.Code, rec.Body.String())
	}
	// A plan approval raises "permission" too, but its dialog is not a tool-permission menu:
	// a digit there would answer the plan.
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: "slot74", Dir: dir, Kind: session.KindClaude})
	sid := session.UUID(dir, "slot74")
	status.Persist(sid, "permission")
	status.WritePendingPermission(sid, "ExitPlanMode")
	status.WritePendingPlan(sid, "# plan")
	if rec := do("slot74", `{"choice":"deny"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no_permission") {
		t.Fatalf("plan under permission: %d %s", rec.Code, rec.Body.String())
	}
}
