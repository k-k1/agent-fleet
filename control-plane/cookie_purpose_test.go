package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"net/http/httptest"
	"testing"
	"time"
)

func purposeTestConfig() config {
	return config{cookieSecret: []byte("fake-cookie-secret-for-tests-only-0123456789"),
		cookiePurposeKeys: true, legacyCookieUntil: time.Now().Add(time.Hour)}
}

// previousRelease is the signer and verifier of the release before this one: one key, no
// labels, no knowledge of the "v2." format.
type previousRelease struct{ secret []byte }

func (r previousRelease) sign(payload []byte) string {
	mac := hmac.New(sha256.New, r.secret)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (r previousRelease) verify(s string) ([]byte, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			payload, err := base64.RawURLEncoding.DecodeString(s[:i])
			if err != nil {
				return nil, false
			}
			sig, err := base64.RawURLEncoding.DecodeString(s[i+1:])
			if err != nil {
				return nil, false
			}
			mac := hmac.New(sha256.New, r.secret)
			mac.Write(payload)
			return payload, hmac.Equal(sig, mac.Sum(nil))
		}
	}
	return nil, false
}

var allPurposes = []cookiePurpose{cookiePurposeSession, cookiePurposeState, cookiePurposePreview, cookiePurposePreviewToken}

func TestCookieVerifiesOnlyForItsPurpose(t *testing.T) {
	c := purposeTestConfig()
	for _, signed := range allPurposes {
		v := c.signCookie(signed, []byte(`{"x":1}`))
		for _, checked := range allPurposes {
			p, ok := c.verifyCookie(checked, v)
			if ok != (signed == checked) {
				t.Errorf("signed for %s, verified as %s: ok=%v", signed, checked, ok)
			}
			if ok && string(p) != `{"x":1}` {
				t.Errorf("payload = %q", p)
			}
		}
	}
}

func TestCookieTamperedOrWrongSecretRefused(t *testing.T) {
	c := purposeTestConfig()
	v := c.signCookie(cookiePurposeSession, []byte("p"))
	other := config{cookieSecret: []byte("a-different-fake-secret-0123456789abcdef"), cookiePurposeKeys: true}
	if _, ok := other.verifyCookie(cookiePurposeSession, v); ok {
		t.Error("verified under another secret")
	}
	if _, ok := c.verifyCookie(cookiePurposeSession, v+"x"); ok {
		t.Error("verified a tampered signature")
	}
}

// Stage one: with the switch off a replica issues what the previous release reads, and
// reads what it issues, in both directions, for every purpose.
func TestCookieStageOneMixesWithPreviousRelease(t *testing.T) {
	c := purposeTestConfig()
	c.cookiePurposeKeys = false
	old := previousRelease{c.cookieSecret}
	for _, p := range allPurposes {
		if got, ok := old.verify(c.signCookie(p, []byte("p"))); !ok || string(got) != "p" {
			t.Errorf("previous release refuses a stage-one %s cookie", p)
		}
		if _, ok := c.verifyCookie(p, old.sign([]byte("p"))); !ok {
			t.Errorf("stage-one replica refuses the previous release's %s cookie", p)
		}
	}
}

// Stage two, replicas switched one at a time: a switched replica and one still at stage one
// accept each other's cookies, so a person is not signed out whichever one answers.
func TestCookieStageTwoMixedReplicas(t *testing.T) {
	on := purposeTestConfig()
	off := on
	off.cookiePurposeKeys = false
	for _, p := range allPurposes {
		if _, ok := on.verifyCookie(p, off.signCookie(p, []byte("p"))); !ok {
			t.Errorf("switched replica refuses a stage-one %s cookie", p)
		}
		if _, ok := off.verifyCookie(p, on.signCookie(p, []byte("p"))); !ok {
			t.Errorf("stage-one replica refuses a switched replica's %s cookie", p)
		}
	}
}

func TestLegacyCookieWindowAfterSwitch(t *testing.T) {
	c := purposeTestConfig()
	old := previousRelease{c.cookieSecret}.sign([]byte("p"))
	for _, p := range allPurposes {
		if _, ok := c.verifyCookie(p, old); !ok {
			t.Errorf("unlabelled cookie refused for %s inside the window", p)
		}
	}
	c.legacyCookieUntil = time.Now().Add(-time.Second)
	if _, ok := c.verifyCookie(cookiePurposeSession, old); ok {
		t.Error("unlabelled cookie accepted after the window")
	}
	c.legacyCookieUntil = time.Time{}
	if _, ok := c.verifyCookie(cookiePurposeSession, old); ok {
		t.Error("unlabelled cookie accepted with no window configured")
	}
	if _, ok := c.verifyCookie(cookiePurposeSession, c.signCookie(cookiePurposeSession, []byte("p"))); !ok {
		t.Error("labelled cookie refused after the window")
	}
}

// The real session path across two replicas on different stages.
func TestSessionAcrossMixedReplicas(t *testing.T) {
	on := purposeTestConfig()
	on.sessionTTL = time.Hour
	off := on
	off.cookiePurposeKeys = false
	for name, pair := range map[string][2]config{"off-issues": {off, on}, "on-issues": {on, off}} {
		w := httptest.NewRecorder()
		pair[0].issueSession(w, auth.Principal{Email: "a@example.test", Provider: "google", Subject: "s"})
		r := httptest.NewRequest("GET", "/", nil)
		for _, ck := range w.Result().Cookies() {
			r.AddCookie(ck)
		}
		if cl, ok := pair[1].session(r); !ok || cl.Email != "a@example.test" {
			t.Errorf("%s: the other replica did not accept the session", name)
		}
	}
}
