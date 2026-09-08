package imagegen

// The REST face. Both routes are driven end to end by imagegen_live_test.go against real CLIs;
// what is checked here is the part that decides WHICH provider a call may reach, because that
// is the part that spends somebody's plan.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// withImagegenSession installs a provider set (reusing imagegen_test.go's stub) plus a session
// for the request to belong to, and turns the feature on.
func withImagegenSession(t *testing.T, kind string, provs ...Provider) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	session.WriteMeta(session.Meta{Name: "slot01", Kind: kind, Dir: home})
	withStubProvider(t, provs...)
	oldEnabled := Enabled
	Enabled = func() bool { return true }
	t.Cleanup(func() { Enabled = oldEnabled })
}

func capsOf(ops []Op, ratios ...string) *Caps {
	return &Caps{Ops: ops, AspectRatios: ratios}
}

// The status endpoint reports EVERY ready provider with its own capabilities — the flat fields
// describe the first one only, which stopped being enough once the tool grew a provider
// argument: a tool that offers a choice has to say what each choice can do.
func TestStatusListsEveryReadyProvider(t *testing.T) {
	withImagegenSession(t, session.KindClaude,
		stubProvider{id: "agy", caps: capsOf([]Op{OpGenerate, OpEdit}, "1:1", "16:9")},
		stubProvider{id: "codex", caps: capsOf([]Op{OpGenerate})},
		stubProvider{id: "bedrock", notReady: true, caps: capsOf([]Op{OpInpaint})},
	)
	rec := httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/imagegen/status?session=slot01", nil))

	var got statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("status is not JSON: %v (%s)", err, rec.Body)
	}
	if !got.Ready || got.Provider != "agy" {
		t.Fatalf("effective provider = %+v, want the first ready one", got)
	}
	if len(got.Providers) != 2 || got.Providers[0].ID != "agy" || got.Providers[1].ID != "codex" {
		t.Fatalf("providers = %+v, want both ready ones in order and no unready one", got.Providers)
	}
	// Per-provider capabilities, not the first one's repeated: this is what makes the tool's
	// enums honest when the caller names a provider.
	if len(got.Providers[0].AspectRatios) != 2 || len(got.Providers[1].AspectRatios) != 0 {
		t.Fatalf("aspect ratios = %+v, want them per provider", got.Providers)
	}
	if got.Kind != session.KindClaude {
		t.Fatalf("kind = %q", got.Kind)
	}
}

// The status names the image SERVICE behind each route, not only the CLI id it is keyed by.
// The id is what the tool's enum carries, and a session asked for a picture "from Gemini"
// cannot map that onto `agy` on its own — the one that could not reported the service as
// unavailable while holding the only route to it.
func TestStatusNamesTheServiceBehindEachProvider(t *testing.T) {
	withImagegenSession(t, session.KindClaude,
		stubProvider{id: ProviderAgy, caps: capsOf([]Op{OpGenerate})},
		stubProvider{id: ProviderCodex, caps: capsOf([]Op{OpGenerate})},
	)
	rec := httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/imagegen/status?session=slot01", nil))

	var got statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("status is not JSON: %v (%s)", err, rec.Body)
	}
	want := map[string]string{ProviderAgy: "Gemini", ProviderCodex: "GPT Image"}
	for _, p := range got.Providers {
		if !strings.Contains(p.Service, want[p.ID]) {
			t.Fatalf("provider %s service = %q, want it to name %q", p.ID, p.Service, want[p.ID])
		}
	}
	// The flat field describes the effective provider, like every other flat field here.
	if !strings.Contains(got.Service, "Gemini") {
		t.Fatalf("effective service = %q, want the first ready provider's", got.Service)
	}
}

// A named provider may not be the caller's own CLI. The tool's enum already leaves it out, but
// the advertised set is a scope boundary — a guessed name in tools/call must not cross it and
// spend the plan twice for a picture this session can make with its own built-in tool.
func TestGenerateRefusesTheSessionsOwnCLI(t *testing.T) {
	withImagegenSession(t, session.KindCodex,
		stubProvider{id: "codex"},
		stubProvider{id: "agy"},
	)
	body := `{"session":"slot01","prompt":"a cat","provider":"codex"}`
	rec := httptest.NewRecorder()
	HandleGenerate(rec, httptest.NewRequest(http.MethodPost, "/imagegen/generate", strings.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want a refusal", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "imagegen_own_cli") {
		t.Fatalf("body = %s, want the reason on the wire", rec.Body)
	}
	// Naming the service is what stops the refusal being read as "GPT Image is unreachable from
	// here": it is reachable, through this session's own built-in tool.
	if !strings.Contains(rec.Body.String(), "GPT Image") {
		t.Fatalf("body = %s, want the service named so the refusal is actionable", rec.Body)
	}
}
