package webhookpoke

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func sig(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestVerifyHMAC256(t *testing.T) {
	body := `{"a":1}`
	if err := VerifyHMAC256(sig("s", body), []byte(body), []byte("s")); err != nil {
		t.Fatalf("good signature refused: %v", err)
	}
	for name, c := range map[string]struct{ header, secret string }{
		"wrong secret":  {sig("s", body), "t"},
		"empty secret":  {sig("", body), ""},
		"no header":     {"", "s"},
		"no prefix":     {strings.TrimPrefix(sig("s", body), "sha256="), "s"},
		"sha1 prefix":   {"sha1=" + strings.TrimPrefix(sig("s", body), "sha256="), "s"},
		"short hex":     {"sha256=ab", "s"},
		"not hex":       {"sha256=" + strings.Repeat("z", 64), "s"},
		"other body":    {sig("s", body+" "), "s"},
		"upper-cased x": {"SHA256=" + strings.TrimPrefix(sig("s", body), "sha256="), "s"},
	} {
		if err := VerifyHMAC256(c.header, []byte(body), []byte(c.secret)); err != ErrBadSignature {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestParseGitHub(t *testing.T) {
	cases := []struct {
		name, event, body string
		want              []string
		private           bool
	}{
		{"pull_request", "pull_request", `{"number":7,"pull_request":{"number":7},"repository":{"full_name":"Acme/Repo","private":false}}`, []string{"github:acme/repo#7"}, false},
		{"review", "pull_request_review", `{"pull_request":{"number":8},"repository":{"full_name":"a/b","private":true}}`, []string{"github:a/b#8"}, true},
		{"check_suite with two PRs", "check_suite", `{"check_suite":{"pull_requests":[{"number":1},{"number":2},{"number":1}]},"repository":{"full_name":"a/b","private":false}}`, []string{"github:a/b#1", "github:a/b#2"}, false},
		{"check_suite on a fork has no PR", "check_suite", `{"check_suite":{"pull_requests":[]},"repository":{"full_name":"a/b","private":false}}`, nil, false},
		{"issue comment on a PR", "issue_comment", `{"issue":{"number":3,"pull_request":{"url":"x"}},"repository":{"full_name":"a/b","private":false}}`, []string{"github:a/b#3"}, false},
		{"issue comment on an issue", "issue_comment", `{"issue":{"number":3},"repository":{"full_name":"a/b","private":false}}`, nil, false},
		{"push names no PR", "push", `{"repository":{"full_name":"a/b","private":false}}`, nil, false},
		{"ping", "ping", `{"zen":"x"}`, nil, true},
		{"privacy not stated counts as private", "pull_request", `{"number":1,"repository":{"full_name":"a/b"}}`, []string{"github:a/b#1"}, true},
		{"hostile repo name", "pull_request", `{"number":1,"repository":{"full_name":"a/b\n#9"}}`, nil, true},
		{"dot repo", "pull_request", `{"number":1,"repository":{"full_name":"../b"}}`, nil, true},
		{"zero number", "pull_request", `{"number":0,"repository":{"full_name":"a/b"}}`, nil, true},
	}
	for _, c := range cases {
		got, err := ParseGitHub(c.event, []byte(c.body))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if strings.Join(got.Subjects, ",") != strings.Join(c.want, ",") || got.Private != c.private {
			t.Errorf("%s: %+v, want %v private=%v", c.name, got, c.want, c.private)
		}
	}
	if _, err := ParseGitHub("pull_request", []byte("{{")); err == nil {
		t.Error("garbage parsed")
	}
}

func TestParseGitHubCapsSubjects(t *testing.T) {
	var prs []string
	for i := 1; i <= 50; i++ {
		prs = append(prs, fmt.Sprintf(`{"number":%d}`, i))
	}
	body := `{"workflow_run":{"pull_requests":[` + strings.Join(prs, ",") + `]},"repository":{"full_name":"a/b","private":false}}`
	got, _ := ParseGitHub("workflow_run", []byte(body))
	if len(got.Subjects) != MaxSubjects {
		t.Fatalf("%d subjects from one delivery, want %d", len(got.Subjects), MaxSubjects)
	}
}

func TestParseBitbucket(t *testing.T) {
	body := `{"pullrequest":{"id":9},"repository":{"full_name":"Acme/Repo","is_private":true}}`
	got, err := ParseBitbucket("pullrequest:updated", []byte(body))
	if err != nil || len(got.Subjects) != 1 || got.Subjects[0] != "bitbucket:acme/repo#9" || !got.Private {
		t.Fatalf("%+v %v", got, err)
	}
	if got, _ := ParseBitbucket("repo:push", []byte(body)); len(got.Subjects) != 0 {
		t.Fatalf("a non pull request event named a subject: %v", got.Subjects)
	}
}

func TestValidSubject(t *testing.T) {
	for s, want := range map[string]bool{
		"github:acme/repo#7": true, "bitbucket:a/b#1": true, "github:../x#1": false, "github:a/..#1": false,
		"github:Acme/repo#7": false, "github:a/b#0": false, "gitlab:a/b#1": false, "github:a/b#1\n": false, "github:a/b": false,
	} {
		if ValidSubject(s) != want {
			t.Errorf("ValidSubject(%q) = %v", s, !want)
		}
	}
}

func TestParseGitHubWorkflowRunNamesRunAndWorkflowKeys(t *testing.T) {
	run := func(action, path, branch string) string {
		return `{"action":"` + action + `","workflow_run":{"id":4242,"run_attempt":2,"path":"` + path + `","head_branch":"` + branch +
			`","pull_requests":[{"number":7}]},"repository":{"full_name":"Acme/Repo","private":false}}`
	}
	got, err := ParseGitHub("workflow_run", []byte(run("completed", ".github/workflows/Nightly.yml", "release/1.x")))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"github:acme/repo/actions/runs/4242",
		"github:acme/repo/actions/runs/4242/attempts/2",
		"github:acme/repo/actions/workflows/Nightly.yml",
		"github:acme/repo/actions/workflows/Nightly.yml/branch/release/1.x",
		"github:acme/repo#7",
	}
	if strings.Join(got.Subjects, "\n") != strings.Join(want, "\n") {
		t.Fatalf("subjects\n got %q\nwant %q", got.Subjects, want)
	}
	for _, s := range got.Subjects {
		if !ValidSubject(s) {
			t.Errorf("a subject the delivery names does not pass the registration check: %q", s)
		}
	}
	// A ref suffix on the path is not part of the file name.
	got, _ = ParseGitHub("workflow_run", []byte(run("completed", ".github/workflows/ci.yml@refs/pull/7/merge", "main")))
	if len(got.Subjects) < 3 || got.Subjects[2] != "github:acme/repo/actions/workflows/ci.yml" {
		t.Fatalf("path with a ref suffix: %q", got.Subjects)
	}
	// Only a completed run is a change the Actions source tells; the other actions name no run key.
	for _, action := range []string{"requested", "in_progress", ""} {
		got, _ = ParseGitHub("workflow_run", []byte(run(action, ".github/workflows/ci.yml", "main")))
		if len(got.Subjects) != 1 || got.Subjects[0] != "github:acme/repo#7" {
			t.Errorf("action %q: %q", action, got.Subjects)
		}
	}
	// Other events never name a run, even with the same shape.
	got, _ = ParseGitHub("check_run", []byte(run("completed", ".github/workflows/ci.yml", "main")))
	if len(got.Subjects) != 0 {
		t.Errorf("check_run named %q", got.Subjects)
	}
}

func TestParseGitHubWorkflowRunIgnoresForgedFields(t *testing.T) {
	body := func(id, path, branch string) []byte {
		return []byte(`{"action":"completed","workflow_run":{"id":` + id + `,"run_attempt":1,"path":"` + path + `","head_branch":"` + branch +
			`","pull_requests":[{"number":3}]},"repository":{"full_name":"a/b","private":false}}`)
	}
	for name, b := range map[string][]byte{
		"path outside workflows": body("5", "other/ci.yml", "main"),
		"not a yaml file":        body("5", ".github/workflows/ci.sh", "main"),
		"traversal in the path":  body("5", ".github/workflows/../x.yml", "main"),
		"branch with dots":       body("5", ".github/workflows/ci.yml", "a..b"),
		"branch with a space":    body("5", ".github/workflows/ci.yml", "a b"),
	} {
		got, err := ParseGitHub("workflow_run", b)
		if err != nil {
			t.Fatal(name, err)
		}
		// A bad path or branch costs only its own key: the run keys and the PR subject stay.
		have := strings.Join(got.Subjects, " ")
		for _, k := range []string{"github:a/b/actions/runs/5 ", "github:a/b/actions/runs/5/attempts/1", "github:a/b#3"} {
			if !strings.Contains(have+" ", k) {
				t.Errorf("%s: %q lost from %q", name, k, got.Subjects)
			}
		}
		for _, s := range got.Subjects {
			if strings.Contains(s, "/workflows/") && !strings.HasSuffix(s, "/workflows/ci.yml") {
				t.Errorf("%s: forged workflow key %q", name, s)
			}
			if strings.Contains(s, "/branch/") || strings.Contains(s, "..") || strings.Contains(s, " ") {
				t.Errorf("%s: forged key %q", name, s)
			}
		}
	}
	for _, id := range []string{"0", "-3", "1234567890123456789"} {
		if got, _ := ParseGitHub("workflow_run", body(id, ".github/workflows/ci.yml", "main")); len(got.Subjects) != 1 || got.Subjects[0] != "github:a/b#3" {
			t.Errorf("run id %s: want only the pull request subject, got %q", id, got.Subjects)
		}
	}
}

func TestValidSubjectActionsKeys(t *testing.T) {
	for s, want := range map[string]bool{
		"github:acme/my_repo/actions/runs/1": true, "github:acme/my_repo/actions/workflows/ci.yml": true,
		"github:a/b/actions/runs/1":                                true,
		"github:a/b/actions/runs/1/attempts/3":                     true,
		"github:a/b/actions/workflows/ci.yml":                      true,
		"github:a/b/actions/workflows/Ci-1.yaml/branch/feat/x":     true,
		"github:A/b/actions/runs/1":                                false,
		"github:../b/actions/runs/1":                               false,
		"github:a/b/actions/runs/0":                                false,
		"github:a/b/actions/runs/1\n":                              false,
		"github:a/b/actions/workflows/ci.yml/branch/a..b":          false,
		"github:a/b/actions/workflows/ci.yml/branch/-x":            false,
		"github:a/b/actions/workflows/ci.yml/after/1-2":            false,
		"github:a/b/actions/workflows/ci.txt":                      false,
		"bitbucket:a/b/actions/runs/1":                             false,
		"github:a/b/actions/workflows/ci.yml/branch/trailing/":     false,
		"github:a/b/actions/workflows/ci.yml/branch/double//slash": false,
	} {
		if ValidSubject(s) != want {
			t.Errorf("ValidSubject(%q) = %v", s, !want)
		}
	}
}
