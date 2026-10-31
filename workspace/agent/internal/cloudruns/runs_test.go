package cloudruns

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

func TestMain(m *testing.M) { os.Exit(testguard.Run(m, nil)) }

// useRoot moves the spool into a temporary directory and fixes the clock.
func useRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldRoot, oldNow := root, now
	root = func() string { return dir }
	clock := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { clock = clock.Add(time.Millisecond); return clock }
	t.Cleanup(func() { root, now = oldRoot, oldNow })
	return dir
}

func liveSession(t *testing.T, name string) {
	t.Helper()
	session.WriteMeta(session.Meta{Name: name, Kind: session.KindClaude, Dir: t.TempDir()})
	t.Cleanup(func() { os.Remove(session.MetaPath(name)) })
}

func TestDescribeKeepsOnlyKnownServiceAndOperation(t *testing.T) {
	for _, c := range []struct {
		argv               []string
		program, svc, oper string
	}{
		{[]string{"/usr/local/bin/aws", "s3", "ls"}, "aws", "s3", "ls"},
		{[]string{"aws", "sts", "get-caller-identity", "--query", "Arn"}, "aws", "sts", "get-caller-identity"},
		{[]string{"aws", "--profile", "x", "s3", "ls"}, "aws", "", ""},
		{[]string{"aws", "ssm", "not-an-operation-xyz"}, "aws", "", ""},
		{[]string{"aws", "nosuchservice", "ls"}, "aws", "", ""},
		{[]string{"aws", "s3"}, "aws", "", ""},
		{[]string{"terraform", "plan"}, "terraform", "", ""},
		{[]string{"./a b"}, "", "", ""},
		{nil, "", "", ""},
	} {
		p, s, o := describe(c.argv)
		if p != c.program || s != c.svc || o != c.oper {
			t.Errorf("describe(%q) = %q %q %q, want %q %q %q", c.argv, p, s, o, c.program, c.svc, c.oper)
		}
	}
}

func TestRowGoesToItsSessionOrToTheWorkspace(t *testing.T) {
	dir := useRoot(t)
	liveSession(t, "cr-live")
	Begin("aws", "cr-live", "prod", []string{"aws", "s3", "ls"})
	Begin("aws", "cr-unknown", "prod", []string{"aws", "s3", "ls"})
	Begin("aws", "", "prod", []string{"aws", "s3", "ls"})
	Begin("aws", "../x", "prod", []string{"aws", "s3", "ls"})

	if got := ForSession("cr-live"); len(got) != 1 || got[0].Session != "cr-live" {
		t.Fatalf("live session rows = %+v", got)
	}
	ws := rowFiles(filepath.Join(dir, workspaceDir))
	if len(ws) != 3 {
		t.Fatalf("workspace rows = %d, want 3", len(ws))
	}
	if _, err := os.Stat(filepath.Join(dir, "cr-unknown")); !os.IsNotExist(err) {
		t.Fatalf("an unknown session got a spool of its own: %v", err)
	}
	rows := Recent(10)
	if len(rows) != 4 {
		t.Fatalf("Recent = %d rows, want 4", len(rows))
	}
	for _, r := range rows {
		if r.Session == "../x" {
			t.Fatalf("an invalid session name was kept: %+v", r)
		}
	}
	if rows[0].StartedAt < rows[3].StartedAt {
		t.Fatalf("Recent is not newest first: %s before %s", rows[0].StartedAt, rows[3].StartedAt)
	}
}

func TestRowLifecycle(t *testing.T) {
	useRoot(t)
	liveSession(t, "cr-life")
	r := Begin("aws", "cr-life", "prod-admin", []string{"aws", "s3", "ls"})
	r.Resolved(Identity{Account: "123456789012", Role: "AdministratorAccess", Region: "ap-northeast-1", Source: "base"})
	got := ForSession("cr-life")
	if len(got) != 1 {
		t.Fatalf("rows = %+v", got)
	}
	g := got[0]
	if g.Profile != "prod-admin" || g.Account != "123456789012" || g.Role != "AdministratorAccess" ||
		g.Region != "ap-northeast-1" || g.Source != "base" || g.Service != "s3" || g.Operation != "ls" {
		t.Fatalf("row = %+v", g)
	}
	if g.EndedAt != "" || g.Refused != "" {
		t.Fatalf("a started run has an end: %+v", g)
	}

	Begin("aws", "cr-life", "prod", []string{"aws", "s3", "ls"}).Refuse(RefusedLogin)
	got = ForSession("cr-life")
	if len(got) != 2 || got[1].Refused != RefusedLogin || got[1].EndedAt == "" {
		t.Fatalf("refused row = %+v", got[len(got)-1])
	}
}

func TestIdentityOutsideTheAllowlistIsDropped(t *testing.T) {
	useRoot(t)
	liveSession(t, "cr-allow")
	r := Begin("aws", "cr-allow", "bad profile\n", []string{"aws"})
	r.Resolved(Identity{Account: "12x", Role: "a\nb", Region: "Not A Region", Source: "s p"})
	g := ForSession("cr-allow")[0]
	if g.Profile != "" || g.Account != "" || g.Role != "" || g.Region != "" || g.Source != "" {
		t.Fatalf("values outside the allowlist were kept: %+v", g)
	}
}

func TestPruneKeepsTheNewest(t *testing.T) {
	dir := useRoot(t)
	liveSession(t, "cr-prune")
	for i := 0; i < perSession+5; i++ {
		Begin("aws", "cr-prune", "p", []string{"aws"})
	}
	if n := len(rowFiles(filepath.Join(dir, "cr-prune"))); n != perSession {
		t.Fatalf("session spool holds %d rows, want %d", n, perSession)
	}

	// A workspace row older than workspaceKeep goes at the next write.
	old := Begin("aws", "", "p", []string{"aws"})
	clock := now()
	now = func() time.Time { clock = clock.Add(workspaceKeep + time.Hour); return clock }
	Begin("aws", "", "p", []string{"aws"})
	ws := rowFiles(filepath.Join(dir, workspaceDir))
	if len(ws) != 1 || ws[0] == filepath.Base(old.path) {
		t.Fatalf("workspace spool after a week = %v", ws)
	}
}

func TestDropRemovesTheSessionSpool(t *testing.T) {
	dir := useRoot(t)
	liveSession(t, "cr-drop")
	Begin("aws", "cr-drop", "p", []string{"aws"})
	Drop("cr-drop")
	if _, err := os.Stat(filepath.Join(dir, "cr-drop")); !os.IsNotExist(err) {
		t.Fatalf("spool still there: %v", err)
	}
	Drop("..") // never escapes the root
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("root gone: %v", err)
	}
}

// TestSentinelNeverReachesARowOrTheWire runs commands whose arguments hold a sentinel in
// every place a value can sit, including where a service or operation would be and as the
// value of the aws global options, and asserts it reaches no spool file and no answer.
func TestSentinelNeverReachesARowOrTheWire(t *testing.T) {
	dir := useRoot(t)
	liveSession(t, "cr-sentinel")
	const s = "zq-sentinel-7f3a"
	for _, argv := range [][]string{
		{"aws", "ssm", s},
		{"aws", s, "ls"},
		{"aws", "--profile", s, "s3", "ls"},
		{"aws", "--region", s, "s3", "ls"},
		{"aws", "--endpoint-url", "https://" + s, "s3", "ls"},
		{"aws", "s3", "cp", "s3://" + s + "/k", "."},
		{"aws", "secretsmanager", "get-secret-value", "--secret-id", s},
		{"sh", "-c", "echo " + s},
		{"/tmp/" + s + "/x y"},
	} {
		r := Begin("aws", "cr-sentinel", "prod", argv)
		r.Resolved(Identity{Account: "123456789012", Role: "Admin", Region: "us-east-1"})
	}
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if bytes.Contains(b, []byte(s)) {
			t.Errorf("%s holds the sentinel: %s", p, b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	HandleRecent(rec, httptest.NewRequest("GET", "/aws-runs", nil))
	if strings.Contains(rec.Body.String(), s) {
		t.Fatalf("GET /aws-runs holds the sentinel: %s", rec.Body.String())
	}
	var wire RecentWire
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil || len(wire.Runs) != 9 {
		t.Fatalf("GET /aws-runs = %v, %d rows; want 9", err, len(wire.Runs))
	}
	if b, _ := json.Marshal(ForSession("cr-sentinel")); bytes.Contains(b, []byte(s)) {
		t.Fatalf("ForSession holds the sentinel: %s", b)
	}
}

// TestAWSCommandsMatchPin fails when the embedded command table was read from another
// aws-cli than the one workspace/Dockerfile pins: rerun gen_aws_commands.py.
func TestAWSCommandsMatchPin(t *testing.T) {
	b, err := os.ReadFile("../../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^ARG AWSCLI_VERSION=(\S+)$`).FindSubmatch(b)
	if m == nil {
		t.Fatal("no AWSCLI_VERSION in workspace/Dockerfile")
	}
	cmds, version := awsCommands()
	if version != string(m[1]) {
		t.Fatalf("aws_commands.txt.gz is from aws-cli %q, the Dockerfile pins %q: run gen_aws_commands.py", version, m[1])
	}
	if len(cmds) < 300 || !cmds["s3"]["ls"] || !cmds["sts"]["get-caller-identity"] {
		t.Fatalf("command table looks wrong: %d services", len(cmds))
	}
}
