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
		{Provider: "github", Key: "O/R#45", Role: "working", URL: "https://github.com/O/R/pull/45", At: "2026-11-01T00:00:00Z", State: "merged"},
		// The same item in another case: skipped, not a primary-key failure of the whole write.
		{Provider: "GitHub", Key: "o/r#45", Role: "watching"},
		{Provider: "github", Key: "o/r#7", Role: "watching"},
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
	if len(got) != 4 || got[0].Key != "PROJ-1" || got[1].Key != "O/R#45" || got[2].Key != "o/r#7" || got[3].Title != "ab" {
		t.Fatalf("s1 links = %+v, want PROJ-1, O/R#45, o/r#7, PROJ-9 (titled ab) in the Agent's order", got)
	}
	if got[1] != (SessionLink{Provider: "github", Key: "O/R#45", Role: "working", URL: "https://github.com/O/R/pull/45", At: "2026-11-01T00:00:00Z", State: "merged"}) {
		t.Errorf("s1 PR link = %+v, want every field kept", got[1])
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
