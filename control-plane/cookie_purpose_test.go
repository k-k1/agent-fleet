package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"
)

func purposeTestConfig() config {
	return config{cookieSecret: []byte("fake-cookie-secret-for-tests-only-0123456789"), legacyCookieUntil: time.Now().Add(time.Hour)}
}

// legacyCookie is the format the previous version issued: the raw secret signs the payload.
func legacyCookie(c config, payload []byte) string {
	mac := hmac.New(sha256.New, c.cookieSecret)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestCookieVerifiesOnlyForItsPurpose(t *testing.T) {
	c := purposeTestConfig()
	all := []cookiePurpose{cookiePurposeSession, cookiePurposeState, cookiePurposePreview, cookiePurposePreviewToken}
	for _, signed := range all {
		v := c.signCookie(signed, []byte(`{"x":1}`))
		for _, checked := range all {
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
	other := config{cookieSecret: []byte("a-different-fake-secret-0123456789abcdef")}
	if _, ok := other.verifyCookie(cookiePurposeSession, v); ok {
		t.Error("verified under another secret")
	}
	if _, ok := c.verifyCookie(cookiePurposeSession, v+"x"); ok {
		t.Error("verified a tampered signature")
	}
}

func TestLegacyCookieWindow(t *testing.T) {
	c := purposeTestConfig()
	old := legacyCookie(c, []byte("p"))
	for _, p := range []cookiePurpose{cookiePurposeSession, cookiePurposePreview} {
		if _, ok := c.verifyCookie(p, old); !ok {
			t.Errorf("legacy cookie refused for %s inside the window", p)
		}
	}
	for _, p := range []cookiePurpose{cookiePurposeState, cookiePurposePreviewToken} {
		if _, ok := c.verifyCookie(p, old); ok {
			t.Errorf("legacy cookie accepted for %s", p)
		}
	}
	c.legacyCookieUntil = time.Now().Add(-time.Second)
	if _, ok := c.verifyCookie(cookiePurposeSession, old); ok {
		t.Error("legacy cookie accepted after the window")
	}
	c.legacyCookieUntil = time.Time{}
	if _, ok := c.verifyCookie(cookiePurposeSession, old); ok {
		t.Error("legacy cookie accepted with no window configured")
	}
	// A labelled cookie never depends on the window.
	if _, ok := c.verifyCookie(cookiePurposeSession, c.signCookie(cookiePurposeSession, []byte("p"))); !ok {
		t.Error("labelled cookie refused after the window")
	}
}
