package workitems

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Bitbucket half of the merge write: the head the panel displayed is checked against a fresh
// read before anything is sent, and Bitbucket's own refusals reach the Console with a code.

type bbMergeFake struct {
	srv       *httptest.Server
	mergeBody string // the POST body, "" when no merge was sent
	posted    bool
}

// fakeBitbucketMerge serves the pre-merge read (`pr`) and answers the merge with status/reply.
func fakeBitbucketMerge(t *testing.T, pr string, status int, reply string) *bbMergeFake {
	t.Helper()
	f := &bbMergeFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/merge") {
			b, _ := io.ReadAll(r.Body)
			f.mergeBody, f.posted = string(b), true
			w.WriteHeader(status)
			_, _ = w.Write([]byte(reply))
			return
		}
		_, _ = w.Write([]byte(pr))
	}))
	prev := BitbucketAPIBase
	BitbucketAPIBase = f.srv.URL
	t.Cleanup(func() { BitbucketAPIBase = prev; f.srv.Close() })
	return f
}

const bbOpenPR = `{"state":"OPEN","source":{"commit":{"hash":"ab37a6993eb1"}}}`

func TestBitbucketMergeSendsStrategyAndCloseSource(t *testing.T) {
	f := fakeBitbucketMerge(t, bbOpenPR, 200, `{"merge_commit":{"hash":"feedfacefeed"}}`)
	no := false
	// The panel shows a 12-character head; a full id from the same commit matches too.
	for _, sha := range []string{"ab37a6993eb1", "ab37a6993eb1ffffffffffffffffffffffffffff"} {
		got, err := bitbucketMergePullRequest("Bearer x", "ws/repo", 1, "ws/repo#1", sha, "squash", &no)
		if err != nil || got != "feedfacefeed" {
			t.Fatalf("%s: got %q, %v", sha, got, err)
		}
		var body map[string]any
		_ = json.Unmarshal([]byte(f.mergeBody), &body)
		if body["merge_strategy"] != "squash" || body["close_source_branch"] != false {
			t.Errorf("merge body = %s", f.mergeBody)
		}
	}
	// Left unset, the pull request's own setting applies: the field is not sent at all.
	if _, err := bitbucketMergePullRequest("Bearer x", "ws/repo", 1, "ws/repo#1", "ab37a6993eb1", "merge_commit", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.mergeBody, "close_source_branch") {
		t.Errorf("close_source_branch sent without a choice: %s", f.mergeBody)
	}
}

func TestBitbucketMergeRefusesBeforeSendingWhenThePanelIsStale(t *testing.T) {
	for name, tc := range map[string]struct{ pr, code string }{
		"head moved": {`{"state":"OPEN","source":{"commit":{"hash":"999999999999"}}}`, "head_changed"},
		"no head":    {`{"state":"OPEN"}`, "head_changed"},
		"merged":     {`{"state":"MERGED","source":{"commit":{"hash":"ab37a6993eb1"}}}`, "not_mergeable"},
		"declined":   {`{"state":"DECLINED","source":{"commit":{"hash":"ab37a6993eb1"}}}`, "not_mergeable"},
	} {
		f := fakeBitbucketMerge(t, tc.pr, 200, `{}`)
		_, err := bitbucketMergePullRequest("Bearer x", "ws/repo", 1, "ws/repo#1", "ab37a6993eb1", "merge_commit", nil)
		me, ok := err.(*githubMergeError)
		if !ok || me.code != tc.code {
			t.Errorf("%s: err = %v, want code %s", name, err, tc.code)
		}
		if f.posted {
			t.Errorf("%s: a merge was sent for a stale panel", name)
		}
	}
}

func TestBitbucketMergeOutcomes(t *testing.T) {
	for name, tc := range map[string]struct {
		status   int
		reply    string
		code     string // "" = a plain provider error
		contains string
	}{
		"conflict":         {409, `{"error":{"message":"You can't merge until you resolve all merge conflicts."}}`, "not_mergeable", "resolve all merge conflicts"},
		"failed check":     {400, `{"error":{"message":"Merge checks failed"}}`, "not_mergeable", "Merge checks failed"},
		"still processing": {202, `{}`, "merge_pending", "still processing"},
		"no write scope":   {403, `{}`, "", "write permission"},
		"gone":             {404, `{}`, "", "no ws/repo#1"},
	} {
		fakeBitbucketMerge(t, bbOpenPR, tc.status, tc.reply)
		_, err := bitbucketMergePullRequest("Bearer x", "ws/repo", 1, "ws/repo#1", "ab37a6993eb1", "merge_commit", nil)
		if err == nil || !strings.Contains(err.Error(), tc.contains) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.contains)
			continue
		}
		me, isMerge := err.(*githubMergeError)
		if (tc.code == "") == isMerge || (isMerge && me.code != tc.code) {
			t.Errorf("%s: err = %#v, want code %q", name, err, tc.code)
		}
	}
}

// The handler refuses a bad request before it reads the connection: nothing to send, no store.
func TestHandleBitbucketMergeValidatesFirst(t *testing.T) {
	for name, c := range map[string]struct{ key, sha, method string }{
		"bad key":        {"nope", "ab37a6993eb1", "merge_commit"},
		"short sha":      {"ws/repo#1", "ab37", "merge_commit"},
		"github method":  {"ws/repo#1", "ab37a6993eb1", "merge"},
		"unknown method": {"ws/repo#1", "ab37a6993eb1", "ff"},
		"not hex":        {"ws/repo#1", "zzzzzzzzzzzz", "merge_commit"},
		"empty strategy": {"ws/repo#1", "ab37a6993eb1", ""},
	} {
		w := httptest.NewRecorder()
		handleBitbucketMerge(w, c.key, c.sha, c.method, nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d %s, want 400", name, w.Code, w.Body.String())
		}
	}
}
