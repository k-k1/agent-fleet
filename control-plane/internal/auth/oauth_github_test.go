package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The GitHub adapter's authorization loop is driven from INSIDE the package,
// because judging it means reaching the per-subject membership cache — which stays
// unexported so the mutex's invariant does not leak (docs/log/61 §61.7).
//
// ★ This test used to live in control-plane/oauth_github_test.go and reached
// p.cache directly. When the adapter moved here it briefly became "set Grace to 0"
// instead, which takes the same branch but is NOT the same test: Grace == 0 makes
// the comparison false whichever timestamp it reads, so mistaking lastOK ("when
// GitHub last said yes") for at ("when we last asked") would have gone straight
// through — and that mistake keeps the door open for the whole outage, which is
// exactly what the grace window exists to prevent. Backdating lastOK with the
// grace left at its default is what pins the ORIGIN of the window, so the test
// came here rather than being rewritten.

// --- the smallest GitHub stub this test needs -------------------------------

type stubGitHubAPI struct {
	*httptest.Server
	apiDown bool // every API call answers 502
}

func newStubGitHubAPI(t *testing.T) *stubGitHubAPI {
	t.Helper()
	s := &stubGitHubAPI{}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.Server = srv

	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
			_, _ = w.Write([]byte("access_token=gho_stub&token_type=bearer"))
			return
		}
		writeJSON(w, map[string]any{"access_token": "gho_stub", "token_type": "bearer"})
	})
	authed := func(w http.ResponseWriter, r *http.Request) bool {
		if s.apiDown {
			w.WriteHeader(http.StatusBadGateway)
			return false
		}
		if r.Header.Get("Authorization") != "Bearer gho_stub" || r.Header.Get("User-Agent") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			writeJSON(w, map[string]any{"id": 4242, "login": "yamada", "email": "decoy@evil.example"})
		}
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			writeJSON(w, []map[string]any{
				{"email": "noreply@users.github.com", "primary": false, "verified": true},
				{"email": "yamada@acme.co.jp", "primary": true, "verified": true},
			})
		}
	})
	mux.HandleFunc("/user/memberships/orgs/", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			writeJSON(w, map[string]any{"state": "active", "role": "member"})
		}
	})
	return s
}

// stubGitHubAdapter wires the adapter at its defaults (10m TTL / 1h grace) against
// the stub, with acme.co.jp as the email gate.
func stubGitHubAdapter(gh *stubGitHubAPI) *GitHubProvider {
	return &GitHubProvider{
		ProviderID: GithubProviderID, LabelJA: "GitHub でサインイン", LabelEN: "Sign in with GitHub",
		ClientID: "client-id", ClientSecret: "client-secret",
		AllowedOrgs:  []string{"acme"},
		AllowDomains: domainSet("acme.co.jp"),
		TTL:          GithubDefaultTTL,
		Grace:        GithubDefaultGrace,
		WebBase:      gh.URL, APIBase: gh.URL, HTTPClient: gh.Client(),
	}
}

// GitHub 障害で全員が締め出されるのも、いつまでも通り続けるのも避ける — 最後の
// 肯定結果を猶予期間だけ延命する（§61.7）。
func TestGitHubOutageHonorsTheLastPositiveAnswerForTheGraceWindow(t *testing.T) {
	gh := newStubGitHubAPI(t)
	p := stubGitHubAdapter(gh)
	pr := Principal{Provider: GithubProviderID, Subject: "4242", Email: "yamada@acme.co.jp"}
	if _, err := p.Exchange(t.Context(), "code", "https://af.example.com/oauth2/callback"); err != nil {
		t.Fatalf("login: %v", err)
	}

	p.TTL = 0 // 毎回 stale 扱いにして再判定へ入れる
	gh.apiDown = true
	if ok, err := p.Allowed(t.Context(), pr); !ok || err != nil {
		t.Fatalf("猶予期間内なのに拒否された: ok=%v err=%v", ok, err)
	}

	// 猶予を超えたら閉じる。
	//
	// ★ 猶予は既定（1h）のままにして、**最後に肯定された時刻だけ**を巻き戻す。
	// 直前の再判定で at は「たった今」に更新されているので、猶予の起点を lastOK から
	// at に取り違えると、ここが通ってしまう＝障害の間ずっと開きっぱなしになる。
	// Grace を 0 にする駆動ではどちらの時刻でも偽になり、その取り違えを捕まえられない。
	p.mu.Lock()
	p.cache["4242"].lastOK = time.Now().Add(-2 * time.Hour)
	p.mu.Unlock()
	if ok, _ := p.Allowed(t.Context(), pr); ok {
		t.Fatal("猶予期間を過ぎても通り続けている")
	}
}
