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
