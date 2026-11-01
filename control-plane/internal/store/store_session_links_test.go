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
		{Provider: "github", Key: "O/R#45", Role: "working", URL: "https://github.com/O/R/pull/45", At: "2026-11-01T00:00:00Z"},
		// The same item in another case: skipped, not a primary-key failure of the whole write.
		{Provider: "GitHub", Key: "o/r#45", Role: "watching"},
		{Provider: "github", Key: "o/r#7", Role: "watching"},
	}
	if err := st.ReplaceSessions(ctx, ws.ID, []SessionRow{
		{Name: "s1", Kind: "claude", State: "running", CreatedAt: "2", Links: first},
		{Name: "s2", Kind: "claude", State: "stopped", CreatedAt: "1"},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	rows, err := st.ListSessions(ctx, ws.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("list: %v %+v", err, rows)
	}
	got := rows[0].Links
	if rows[0].Name != "s1" || len(got) != 3 || got[0].Key != "PROJ-1" || got[1].Key != "O/R#45" || got[2].Key != "o/r#7" {
		t.Fatalf("s1 links = %+v, want PROJ-1, O/R#45, o/r#7 in the Agent's order", got)
	}
	if got[1] != (SessionLink{Provider: "github", Key: "O/R#45", Role: "working", URL: "https://github.com/O/R/pull/45", At: "2026-11-01T00:00:00Z"}) {
		t.Errorf("s1 PR link = %+v, want every field kept", got[1])
	}
	if got[0].Title != "Login" || rows[1].Links != nil {
		t.Errorf("title=%q s2 links=%+v", got[0].Title, rows[1].Links)
	}

	// A later list replaces the links with the rows: the dropped item goes.
	if err := st.ReplaceSessions(ctx, ws.ID, []SessionRow{
		{Name: "s1", Kind: "claude", State: "running", CreatedAt: "2", Links: first[:1]},
	}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.ListSessions(ctx, ws.ID); len(rows) != 1 || len(rows[0].Links) != 1 {
		t.Fatalf("after replace: %+v, want s1 with PROJ-1 only", rows)
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
	// One ASCII byte first, so the byte limit falls inside a three-byte rune.
	title := "a" + strings.Repeat("あ", 200)
	in := `[{"provider":"github","key":"o/r#1","role":"working","title":"` + title + `","url":"https://e/` + strings.Repeat("u", mirrorMaxURL) + `"},` +
		`{"provider":"","key":"o/r#2","role":"working"},` +
		`{"provider":"github","key":"` + long + `","role":"working"}]`
	got := SessionLinksFromJSON(json.RawMessage(in))
	if len(got) != 1 || got[0].Key != "o/r#1" {
		t.Fatalf("mirrored %+v, want only o/r#1 (no provider and an oversized key are dropped)", got)
	}
	if len(got[0].Title) > mirrorMaxTitle || !strings.HasPrefix(title, got[0].Title) || !utf8.ValidString(got[0].Title) {
		t.Errorf("title clipped to %d bytes or mid-rune: %q", len(got[0].Title), got[0].Title)
	}
	if got[0].URL != "" {
		t.Errorf("an oversized URL was kept (clipped it points elsewhere): %q", got[0].URL)
	}

	var many []sessionLinkWire
	for i := 0; i < mirrorMaxLinks+10; i++ {
		many = append(many, sessionLinkWire{Provider: "jira", Key: "P-" + strings.Repeat("1", i+1), Role: "watching"})
	}
	raw, _ := json.Marshal(many)
	if n := len(SessionLinksFromJSON(raw)); n != mirrorMaxLinks {
		t.Errorf("mirrored %d links, want the cap %d", n, mirrorMaxLinks)
	}
}
