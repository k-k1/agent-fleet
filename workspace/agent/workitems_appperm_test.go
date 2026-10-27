package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The Merge button for a GitHub App user token (#2753): drawn only on a positive answer from
// GET /user/installations and GET /user/installations/{id}/repositories.

const appPR = `,"private":true,"allow_merge_commit":true,"permissions":{"push":true}`

// fakeApp serves the PR read (no X-OAuth-Scopes, like an App token) plus the two installation
// endpoints. inst and repos are the raw bodies; status overrides them when non-zero.
type fakeAppCfg struct {
	inst, repos         string
	instCode, reposCode int
	delay               time.Duration
	scopes              *string // X-OAuth-Scopes on every answer; nil sends none
}

func fakeApp(t *testing.T, c fakeAppCfg) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.scopes != nil {
			w.Header().Set("X-OAuth-Scopes", *c.scopes)
		}
		write := func(code int, body string) {
			if code != 0 {
				w.WriteHeader(code)
			}
			_, _ = w.Write([]byte(body))
		}
		switch {
		case r.URL.Path == "/repos/acme/web/pulls/3":
			_, _ = w.Write(mergePR(appPR))
		case r.URL.Path == "/user/installations":
			n.Add(1)
			time.Sleep(c.delay)
			write(c.instCode, c.inst)
		case r.URL.Path == "/user/installations/7/repositories":
			n.Add(1)
			write(c.reposCode, c.repos)
		default:
			http.NotFound(w, r)
		}
	}))
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old; srv.Close() })
	return &n
}

func instBody(contents string) string {
	return fmt.Sprintf(`{"total_count":1,"installations":[{"id":7,"permissions":{"contents":%q,"metadata":"read"}}]}`, contents)
}

func reposBody(name, perms string) string {
	return fmt.Sprintf(`{"total_count":1,"repositories":[{"full_name":%q,"permissions":%s}]}`, name, perms)
}

func resetAppPermCache() {
	appPermMu.Lock()
	appPermCache = map[string]appPermEntry{}
	appPermMu.Unlock()
}

func TestAppUserTokenMergeButton(t *testing.T) {
	good := reposBody("acme/web", `{"push":true}`)
	for _, tc := range []struct {
		name  string
		token string
		cfg   fakeAppCfg
		want  bool
	}{
		{"contents:write, repo listed, push", "ghu_a", fakeAppCfg{inst: instBody("write"), repos: good}, true},
		{"repo name case differs", "ghu_a", fakeAppCfg{inst: instBody("write"), repos: reposBody("Acme/Web", `{"push":true}`)}, true},
		{"contents:read", "ghu_a", fakeAppCfg{inst: instBody("read"), repos: good}, false},
		{"no contents permission", "ghu_a", fakeAppCfg{inst: `{"installations":[{"id":7,"permissions":{"metadata":"read"}}]}`, repos: good}, false},
		{"repo not in installation", "ghu_a", fakeAppCfg{inst: instBody("write"), repos: reposBody("acme/other", `{"push":true}`)}, false},
		{"no installations", "ghu_a", fakeAppCfg{inst: `{"total_count":0,"installations":[]}`, repos: good}, false},
		{"user has no push", "ghu_a", fakeAppCfg{inst: instBody("write"), repos: reposBody("acme/web", `{"pull":true}`)}, false},
		{"no permissions block", "ghu_a", fakeAppCfg{inst: instBody("write"), repos: `{"repositories":[{"full_name":"acme/web"}]}`}, false},
		{"installations 403", "ghu_a", fakeAppCfg{inst: `{"message":"rate limit"}`, instCode: 403, repos: good}, false},
		{"installations 404", "ghu_a", fakeAppCfg{inst: `{}`, instCode: 404, repos: good}, false},
		{"installations 500", "ghu_a", fakeAppCfg{inst: `{}`, instCode: 500, repos: good}, false},
		{"installations garbage", "ghu_a", fakeAppCfg{inst: `<html>`, repos: good}, false},
		{"repositories 403", "ghu_a", fakeAppCfg{inst: instBody("write"), repos: `{}`, reposCode: 403}, false},
		{"repositories 404", "ghu_a", fakeAppCfg{inst: instBody("write"), repos: `{}`, reposCode: 404}, false},
		{"repositories 500", "ghu_a", fakeAppCfg{inst: instBody("write"), repos: `{}`, reposCode: 500}, false},
		{"installations null", "ghu_a", fakeAppCfg{inst: `{"installations":null}`, repos: good}, false},
		{"installations absent", "ghu_a", fakeAppCfg{inst: `{}`, repos: good}, false},
		{"repositories absent", "ghu_a", fakeAppCfg{inst: instBody("write"), repos: `{}`}, false},
		{"repo listed without permissions", "ghu_a", fakeAppCfg{inst: instBody("write"), repos: `{"repositories":[{"full_name":"acme/web"}]}`}, false},
		{"fine-grained token", "github_pat_x", fakeAppCfg{inst: instBody("write"), repos: good}, false},
		{"unknown token kind", "tok", fakeAppCfg{inst: instBody("write"), repos: good}, false},
	} {
		resetAppPermCache()
		fakeApp(t, tc.cfg)
		out, err := githubPullRequestDetail(tc.token, "acme/web#3")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if out.CanMerge != tc.want {
			t.Errorf("%s: canMerge = %v, want %v", tc.name, out.CanMerge, tc.want)
		}
		if len(out.MergeMethods) == 0 || out.HeadSHA == "" {
			t.Errorf("%s: the rest of the panel must be unaffected: %+v", tc.name, out)
		}
	}
}

// A slow endpoint ends in "no button" within the bound rather than hanging the panel.
func TestAppUserTokenTimeoutIsNoButton(t *testing.T) {
	resetAppPermCache()
	// The client's own timeout is long: only the overall context bound can end this early.
	old, oldBound := workItemHTTPClient, appPermTimeout
	workItemHTTPClient = &http.Client{Timeout: 10 * time.Second}
	appPermTimeout = 100 * time.Millisecond
	t.Cleanup(func() { workItemHTTPClient, appPermTimeout = old, oldBound })
	fakeApp(t, fakeAppCfg{inst: instBody("write"), repos: reposBody("acme/web", `{"push":true}`), delay: 1500 * time.Millisecond})
	start := time.Now()
	out, err := githubPullRequestDetail("ghu_a", "acme/web#3")
	if err != nil {
		t.Fatal(err)
	}
	if out.CanMerge {
		t.Error("a timed-out pre-check drew a button")
	}
	if time.Since(start) > 1200*time.Millisecond {
		t.Errorf("took %v", time.Since(start))
	}
}

// A definitive answer is cached briefly; a failure is not, so the next open tries again.
func TestAppUserTokenAnswerIsCachedBrieflyAndErrorsAreNot(t *testing.T) {
	resetAppPermCache()
	n := fakeApp(t, fakeAppCfg{inst: instBody("write"), repos: reposBody("acme/web", `{"push":true}`)})
	for i := 0; i < 3; i++ {
		if !githubAppUserCanMerge("ghu_a", "acme/web") {
			t.Fatal("want true")
		}
	}
	if n.Load() != 2 {
		t.Errorf("3 calls made %d requests, want 2 (one lookup)", n.Load())
	}
	now := time.Now()
	appPermNow = func() time.Time { return now.Add(2 * appPermTTL) }
	t.Cleanup(func() { appPermNow = time.Now })
	githubAppUserCanMerge("ghu_a", "acme/web")
	if n.Load() != 4 {
		t.Errorf("after the TTL, %d requests, want 4", n.Load())
	}

	resetAppPermCache()
	appPermNow = time.Now
	m := fakeApp(t, fakeAppCfg{inst: `{}`, instCode: 500})
	githubAppUserCanMerge("ghu_b", "acme/web")
	githubAppUserCanMerge("ghu_b", "acme/web")
	if m.Load() != 2 {
		t.Errorf("an error was cached: %d requests, want 2", m.Load())
	}
}

// The token is never written into a cache key as itself.
func TestAppUserTokenCacheKeyHoldsNoToken(t *testing.T) {
	resetAppPermCache()
	fakeApp(t, fakeAppCfg{inst: instBody("write"), repos: reposBody("acme/web", `{"push":true}`)})
	githubAppUserCanMerge("ghu_secretvalue", "acme/web")
	appPermMu.Lock()
	defer appPermMu.Unlock()
	for k := range appPermCache {
		if strings.Contains(k, "secretvalue") {
			t.Errorf("cache key holds the token: %q", k)
		}
	}
}

// An empty or present X-OAuth-Scopes header does not take a ghu_ token off the pre-check path.
func TestAppUserTokenIsChosenByPrefixNotByScopesHeader(t *testing.T) {
	for _, hdr := range []*string{nil, new(string), ptrStr("repo")} {
		resetAppPermCache()
		fakeApp(t, fakeAppCfg{inst: instBody("write"), repos: reposBody("acme/web", `{"push":true}`), scopes: hdr})
		out, err := githubPullRequestDetail("ghu_a", "acme/web#3")
		if err != nil || !out.CanMerge {
			t.Errorf("scopes header %v: canMerge=%v err=%v, want true", hdr, out != nil && out.CanMerge, err)
		}
		resetAppPermCache()
		fakeApp(t, fakeAppCfg{inst: instBody("read"), repos: reposBody("acme/web", `{"push":true}`), scopes: hdr})
		if out, err := githubPullRequestDetail("ghu_a", "acme/web#3"); err != nil || out.CanMerge {
			t.Errorf("scopes header %v with contents:read: canMerge=%v err=%v, want false", hdr, out != nil && out.CanMerge, err)
		}
	}
}

func ptrStr(s string) *string { return &s }

// An unexpected shape is unknown, not a cached "no": the next open asks again and gets the answer.
// A well-formed empty list, by contrast, is a definitive no and is cached.
func TestAppUserTokenUnexpectedShapeIsNotCached(t *testing.T) {
	resetAppPermCache()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/installations":
			if n.Add(1) == 1 {
				_, _ = w.Write([]byte(`{}`))
				return
			}
			_, _ = w.Write([]byte(instBody("write")))
		case "/user/installations/7/repositories":
			_, _ = w.Write([]byte(reposBody("acme/web", `{"push":true}`)))
		}
	}))
	defer srv.Close()
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
	if githubAppUserCanMerge("ghu_a", "acme/web") {
		t.Fatal("an unexpected shape drew a button")
	}
	if !githubAppUserCanMerge("ghu_a", "acme/web") {
		t.Error("the re-open after an unexpected shape did not ask again")
	}

	resetAppPermCache()
	e := fakeApp(t, fakeAppCfg{inst: `{"total_count":0,"installations":[]}`})
	can, definitive := githubAppUserCanWriteContents(context.Background(), "ghu_a", "acme/web")
	if can || !definitive || e.Load() != 1 {
		t.Errorf("empty installations list: can=%v definitive=%v (requests %d), want false/true", can, definitive, e.Load())
	}
}

// More writing installations than are checked leaves the rest unknown.
func TestAppUserTokenTruncatedInstallationsAreUnknown(t *testing.T) {
	var parts []string
	for i := 1; i <= appPermMaxInstal+1; i++ {
		parts = append(parts, fmt.Sprintf(`{"id":%d,"permissions":{"contents":"write"}}`, 100+i))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user/installations" {
			_, _ = w.Write([]byte(`{"installations":[` + strings.Join(parts, ",") + `]}`))
			return
		}
		_, _ = w.Write([]byte(`{"repositories":[]}`))
	}))
	defer srv.Close()
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
	if can, definitive := githubAppUserCanWriteContents(context.Background(), "ghu_a", "acme/web"); can || definitive {
		t.Errorf("can=%v definitive=%v, want false/false", can, definitive)
	}
}
