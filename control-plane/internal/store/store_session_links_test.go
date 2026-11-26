package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

// The session mirror keeps each session's links (#3147): in the Agent's order, replaced with
// the rows, one per item whatever its case, and gone with the workspace.
func TestSessionMirrorLinks(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	sessionMirrorLinksCase(t, st)
}

func TestPostgresSessionMirrorLinks(t *testing.T) {
	url, ok := pgtest.Schema(t)
	if !ok {
		t.Skip("set AF_TEST_DATABASE_URL to run the Postgres test")
	}
	st, err := OpenPostgres(url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	sessionMirrorLinksCase(t, st)
}

// sessionMirrorLinksCase is the dialect-independent body, also run against Postgres.
func sessionMirrorLinksCase(t *testing.T, st *SQL) {
	t.Helper()
	ctx := context.Background()
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "links@example.com", "links", "")
	member, _ := st.EnsureMembership(ctx, ident.ID, tenant.ID, "member")
	ws := Workspace{ID: NewID(), TenantID: tenant.ID, MembershipID: member.ID,
		ContainerName: "links", Network: "test", DataDir: "/data/links", AgentPort: "1", AgentToken: "tok",
		State: "running", CreatedAt: NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}

	first := []SessionLink{
		{Provider: "jira", Key: "PROJ-1", Role: "started", Title: "Login"},
		{Provider: "github", Key: "O/R#45", Role: "working", URL: "https://github.com/O/R/pull/45", At: "2026-11-01T00:00:00Z", State: "merged",
			Facts: PRFacts{Checks: "failure", ChangesRequested: true, ObservedAt: "2026-11-02T03:04:05Z"}},
		// The same item in another case: skipped, not a primary-key failure of the whole write.
		{Provider: "GitHub", Key: "o/r#45", Role: "watching"},
		// Each boolean column true on one link and false on another, so swapped columns show.
		{Provider: "github", Key: "o/r#7", Role: "watching", Facts: PRFacts{Checks: "success", Ready: true, ObservedAt: "2026-11-02T03:04:06Z"}},
		{Provider: "github", Key: "o/r#8", Role: "watching", Facts: PRFacts{Checks: "pending", Conflict: true, ObservedAt: "2026-11-02T03:04:07Z"}},
	}
	// A NUL from the Agent, through the same read the handler uses: Postgres refuses it in TEXT,
	// and the whole workspace's write would roll back.
	first = append(first, SessionLinksFromJSON([]byte(`[{"provider":"jira","key":"PROJ-9","role":"watching","title":"a\u0000b"}]`))...)
	if err := st.ReplaceSessions(ctx, ws.ID, []SessionRow{
		{Name: "s1", Kind: "claude", State: "running", CreatedAt: "2", Links: first},
		{Name: "s2", Kind: "claude", State: "stopped", CreatedAt: "1"},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	rows, err := st.ListSessions(ctx, ws.ID)
	if err != nil || len(rows) != 2 || rows[0].Links != nil {
		t.Fatalf("list: %v %+v (ListSessions leaves Links to ListSessionLinks)", err, rows)
	}
	byName, err := st.ListSessionLinks(ctx, ws.ID)
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	got := byName["s1"]
	if len(got) != 5 || got[0].Key != "PROJ-1" || got[1].Key != "O/R#45" || got[2].Key != "o/r#7" || got[4].Title != "ab" {
		t.Fatalf("s1 links = %+v, want PROJ-1, O/R#45, o/r#7, o/r#8, PROJ-9 (titled ab) in the Agent's order", got)
	}
	if got[1] != first[1] || got[2] != first[3] || got[3] != first[4] {
		t.Errorf("s1 PR link = %+v, want every field kept", got[1])
	}
	if got[0].Facts != (PRFacts{}) || got[4].Facts != (PRFacts{}) {
		t.Errorf("links with no observation read %+v / %+v, want no facts (not ready)", got[0].Facts, got[4].Facts)
	}
	if _, ok := byName["s2"]; got[0].Title != "Login" || ok {
		t.Errorf("title=%q s2 links=%+v", got[0].Title, byName["s2"])
	}

	// A later list replaces the links with the rows: the dropped item goes.
	if err := st.ReplaceSessions(ctx, ws.ID, []SessionRow{
		{Name: "s1", Kind: "claude", State: "running", CreatedAt: "2", Links: first[:1]},
	}); err != nil {
		t.Fatal(err)
	}
	if links, _ := st.ListSessionLinks(ctx, ws.ID); len(links) != 1 || len(links["s1"]) != 1 {
		t.Fatalf("after replace: %+v, want s1 with PROJ-1 only", links)
	}

	if err := st.DeleteWorkspace(ctx, ws.ID); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
	var n int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_work_link WHERE workspace_id=?`, ws.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("links left after the workspace delete: %d (%v)", n, err)
	}
}

func TestSessionLinksFromJSONBoundsWhatTheAgentSends(t *testing.T) {
	if got := SessionLinksFromJSON(json.RawMessage(`{"not":"an array"}`)); got != nil {
		t.Errorf("malformed links mirrored as %+v, want none", got)
	}
	long := strings.Repeat("x", mirrorMaxKey+1)
	// Counted in runes, as the Agent clips it: 400 three-byte runes keep 300 of them.
	title := strings.Repeat("あ", 400)
	in := `[{"provider":"github","key":"o/r#1","role":"working","title":"` + title + `","url":"https://e/` + strings.Repeat("u", mirrorMaxURL) + `"},` +
		`{"provider":"","key":"o/r#2","role":"working"},` +
		`{"provider":"github","key":"` + long + `","role":"working"}]`
	got := SessionLinksFromJSON(json.RawMessage(in))
	if len(got) != 1 || got[0].Key != "o/r#1" {
		t.Fatalf("mirrored %+v, want only o/r#1 (no provider and an oversized key are dropped)", got)
	}
	if n := utf8.RuneCountInString(got[0].Title); n != mirrorMaxTitle || !strings.HasPrefix(title, got[0].Title) {
		t.Errorf("title clipped to %d runes, want %d", n, mirrorMaxTitle)
	}
	// A title the Agent would send whole (250 runes, 750 bytes) passes whole.
	jp := strings.Repeat("日", 250)
	if got := SessionLinksFromJSON([]byte(`[{"provider":"jira","key":"P-1","role":"working","title":"` + jp + `"}]`)); len(got) != 1 || got[0].Title != jp {
		t.Errorf("a 250-rune title was changed: %+v", got)
	}
	if got := SessionLinksFromJSON([]byte(`[{"provider":"jira","key":"P-1\u0000","role":"work\u0000ing","title":"a\u0000b"}]`)); len(got) != 1 || got[0].Key != "P-1" || got[0].Role != "working" || got[0].Title != "ab" {
		t.Errorf("NUL bytes survived: %+v", got)
	}
	if got[0].URL != "" {
		t.Errorf("an oversized URL was kept (clipped it points elsewhere): %q", got[0].URL)
	}

	var many []sessionLinkWire
	for i := 0; i < mirrorMaxLinks+10; i++ {
		many = append(many, sessionLinkWire{Provider: "jira", Key: "P-" + strings.Repeat("1", i+1), Role: "watching"})
	}
	raw, _ := json.Marshal(many)
	// 51: the Agent's MaxLinks (50) plus the launch item LinksOf puts first.
	if n := len(SessionLinksFromJSON(raw)); n != mirrorMaxLinks || mirrorMaxLinks != 51 {
		t.Errorf("mirrored %d links, want the cap %d", n, mirrorMaxLinks)
	}
}

// A PR link's facts go through the mirror unchanged, so a stopped Workspace serves what a running
// one did (#5052); anything the Agent would not send reads as no observation, never as green.
func TestSessionLinksMirrorThePRFacts(t *testing.T) {
	in := `[{"provider":"github","key":"o/r#1","role":"working","state":"open","facts":{"checks":"success","changesRequested":true,"conflict":true,"ready":true,"observedAt":"2026-11-27T10:00:00Z"}},` +
		`{"provider":"github","key":"o/r#2","role":"working","facts":{"checks":"pending","observedAt":"2026-11-27T10:00:00Z"}},` +
		`{"provider":"jira","key":"P-1","role":"started"}]`
	got := SessionLinksFromJSON(json.RawMessage(in))
	if want := (PRFacts{Checks: "success", ChangesRequested: true, Conflict: true, Ready: true, ObservedAt: "2026-11-27T10:00:00Z"}); len(got) != 3 || got[0].Facts != want {
		t.Fatalf("mirrored %+v, want o/r#1's facts kept whole", got)
	}
	var back, sent []map[string]any
	if err := json.Unmarshal(SessionLinksJSON(got), &back); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(in), &sent)
	for i := range sent {
		if a, b := mustJSON(t, sent[i]), mustJSON(t, back[i]); a != b {
			t.Errorf("link %d served as %s, want what the Agent sent: %s", i, b, a)
		}
	}

	for _, bad := range []string{
		`{"checks":"success","ready":true}`,                          // no observation time
		`{"checks":"success","ready":true,"observedAt":"yesterday"}`, // not a time
	} {
		got := SessionLinksFromJSON([]byte(`[{"provider":"github","key":"o/r#1","role":"working","facts":` + bad + `}]`))
		if len(got) != 1 || got[0].Facts != (PRFacts{}) {
			t.Errorf("facts %s mirrored as %+v, want no observation", bad, got)
		}
		if strings.Contains(string(SessionLinksJSON(got)), "facts") {
			t.Errorf("facts %s served back: %s", bad, SessionLinksJSON(got))
		}
	}
	got = SessionLinksFromJSON([]byte(`[{"provider":"github","key":"o/r#1","role":"working","facts":{"checks":"green","observedAt":"2026-11-27T19:00:00.25+09:00"}}]`))
	if len(got) != 1 || got[0].Facts != (PRFacts{ObservedAt: "2026-11-27T10:00:00.25Z"}) {
		t.Errorf("mirrored %+v, want an unknown checks value as nothing ran and the time in UTC, sub-second kept", got)
	}
	// Facts of the wrong JSON shape cost that link its facts only: the session's other links, and
	// the PR link's own state, stay mirrored.
	got = SessionLinksFromJSON([]byte(`[{"provider":"jira","key":"P-1","role":"started"},` +
		`{"provider":"github","key":"o/r#1","role":"working","state":"open","facts":{"checks":"success","ready":true,"observedAt":1700000000}},` +
		`{"provider":"github","key":"o/r#2","role":"working","facts":"ready"}]`))
	if len(got) != 3 || got[0].Key != "P-1" || got[1].State != "open" || got[1].Facts != (PRFacts{}) || got[2].Facts != (PRFacts{}) {
		t.Errorf("mirrored %+v, want all three links, the malformed facts dropped", got)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
