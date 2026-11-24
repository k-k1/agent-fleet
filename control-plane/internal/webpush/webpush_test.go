package webpush

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newSub makes a browser-side subscription and returns its private key for decrypting.
func newSub(t *testing.T, endpoint string) (Subscription, *ecdh.PrivateKey, []byte) {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return Subscription{Endpoint: endpoint, P256dh: b64.EncodeToString(priv.PublicKey().Bytes()),
		Auth: b64.EncodeToString(auth)}, priv, auth
}

// decrypt is the user agent's half of RFC 8291, written from the RFC rather than from Encrypt.
func decrypt(t *testing.T, body []byte, uaPriv *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	salt, idlen := body[:16], int(body[20])
	asPubRaw := body[21 : 21+idlen]
	asPub, err := ecdh.P256().NewPublicKey(asPubRaw)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := uaPriv.ECDH(asPub)
	if err != nil {
		t.Fatal(err)
	}
	cek, nonce, err := deriveKeys(secret, auth, uaPriv.PublicKey().Bytes(), asPubRaw, salt)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	pt, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if pt[len(pt)-1] != 0x02 {
		t.Fatalf("missing last-record delimiter")
	}
	return pt[:len(pt)-1]
}

func TestEncryptRoundTrip(t *testing.T) {
	sub, priv, auth := newSub(t, "https://fcm.googleapis.com/x")
	body, err := Encrypt(sub, []byte(`{"kind":"question"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := decrypt(t, body, priv, auth); string(got) != `{"kind":"question"}` {
		t.Fatalf("got %q", got)
	}
	// Two messages never share a salt or an ephemeral key.
	body2, _ := Encrypt(sub, []byte("x"))
	if string(body[:16]) == string(body2[:16]) {
		t.Fatal("salt reused")
	}
}

func TestEncryptRefusals(t *testing.T) {
	sub, _, _ := newSub(t, "https://fcm.googleapis.com/x")
	if _, err := Encrypt(sub, make([]byte, MaxPayload+1)); err == nil {
		t.Fatal("oversize payload accepted")
	}
	bad := sub
	bad.Auth = b64.EncodeToString([]byte("short"))
	if _, err := Encrypt(bad, []byte("x")); err == nil {
		t.Fatal("short auth secret accepted")
	}
	bad = sub
	bad.P256dh = b64.EncodeToString([]byte("not a point"))
	if _, err := Encrypt(bad, []byte("x")); err == nil {
		t.Fatal("bad point accepted")
	}
}

func TestVAPIDHeaderVerifies(t *testing.T) {
	keys, priv, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParsePrivateKey(priv)
	if err != nil || again.PublicKey() != keys.PublicKey() {
		t.Fatalf("private key does not round-trip: %v", err)
	}
	now := time.Unix(1_800_000_000, 0)
	h, err := keys.authHeader("https://fcm.googleapis.com/fcm/send/abc", "mailto:ops@example.invalid", now)
	if err != nil {
		t.Fatal(err)
	}
	rest, ok := strings.CutPrefix(h, "vapid t=")
	if !ok {
		t.Fatalf("header %q", h)
	}
	jwt, k, _ := strings.Cut(rest, ", k=")
	if k != keys.PublicKey() {
		t.Fatal("k is not the public key")
	}
	parts := strings.Split(jwt, ".")
	sig, _ := b64.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(&keys.priv.PublicKey, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("signature does not verify")
	}
	claims, _ := b64.DecodeString(parts[1])
	for _, want := range []string{`"aud":"https://fcm.googleapis.com"`, `"sub":"mailto:ops@example.invalid"`, `"exp":1800043200`} {
		if !strings.Contains(string(claims), want) {
			t.Fatalf("claims %s lack %s", claims, want)
		}
	}
	if _, err := ParsePrivateKey("short"); err == nil {
		t.Fatal("short key accepted")
	}
	_ = elliptic.P256
}

func TestSend(t *testing.T) {
	keys, _, _ := GenerateKeys()
	var gotHdr http.Header
	status := http.StatusCreated
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHdr = r.Header.Clone()
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
	}))
	defer srv.Close()
	s := NewSender(keys, "mailto:ops@example.invalid")
	s.HTTP = srv.Client()
	s.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	sub, _, _ := newSub(t, srv.URL+"/p")

	if err := s.Send(context.Background(), sub, []byte("hi"), Options{TTL: 90 * time.Second, Urgency: "high"}); err != nil {
		t.Fatal(err)
	}
	if gotHdr.Get("TTL") != "90" || gotHdr.Get("Urgency") != "high" || gotHdr.Get("Content-Encoding") != "aes128gcm" ||
		!strings.HasPrefix(gotHdr.Get("Authorization"), "vapid t=") {
		t.Fatalf("headers %v", gotHdr)
	}
	for _, code := range []int{404, 410} {
		status = code
		if err := s.Send(context.Background(), sub, []byte("hi"), Options{}); !errors.Is(err, ErrGone) {
			t.Fatalf("%d: %v", code, err)
		}
	}
	// A 5xx or 429 says nothing about the subscription, so it must not read as gone.
	for _, code := range []int{429, 500, 403} {
		status = code
		if err := s.Send(context.Background(), sub, []byte("hi"), Options{}); err == nil || errors.Is(err, ErrGone) {
			t.Fatalf("%d: %v", code, err)
		}
	}
}

func TestSendDoesNotFollowRedirects(t *testing.T) {
	keys, _, _ := GenerateKeys()
	hit := false
	inner := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer inner.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, inner.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	s := NewSender(keys, "mailto:ops@example.invalid")
	c := srv.Client()
	c.CheckRedirect = s.HTTP.CheckRedirect // NewSender's policy, with the test server's trust roots
	s.HTTP = c
	sub, _, _ := newSub(t, srv.URL+"/p")
	if err := s.Send(context.Background(), sub, []byte("x"), Options{}); err == nil {
		t.Fatal("a 307 counted as delivered")
	}
	if hit {
		t.Fatal("redirect was followed")
	}
}

func TestCheckEndpoint(t *testing.T) {
	ok := []string{"https://fcm.googleapis.com/fcm/send/x", "https://updates.push.services.mozilla.com/wpush/v2/x",
		"https://web.push.apple.com/x", "https://wns2-par02p.notify.windows.com/w"}
	for _, u := range ok {
		if err := CheckEndpoint(u, DefaultEndpointHosts); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	bad := []string{"http://fcm.googleapis.com/x", "https://127.0.0.1/x", "https://localhost/x",
		"https://fcm.googleapis.com.evil.example/x", "https://evilpush.apple.com.example/x",
		"https://notpush.apple.com/x", "https://fcm.googleapis.com:8443/x", "https://u:p@fcm.googleapis.com/x",
		"https://169.254.169.254/latest", "", "ftp://fcm.googleapis.com/x"}
	for _, u := range bad {
		if err := CheckEndpoint(u, DefaultEndpointHosts); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

func TestAudIsAnOrigin(t *testing.T) {
	keys, _, _ := GenerateKeys()
	for endpoint, want := range map[string]string{
		"https://FCM.GoogleAPIs.com:443/x": "https://fcm.googleapis.com",
		"https://fcm.googleapis.com/x":     "https://fcm.googleapis.com",
		"https://push.example:8443/x":      "https://push.example:8443",
		// RFC 8292 asks for the Unicode serialization, whichever form the endpoint was written in.
		"https://xn--bcher-kva.example/x": "https://bücher.example",
		"https://BÜCHER.example/x":        "https://bücher.example",
		"https://bu\u0308cher.example/x":  "https://bücher.example",  // decomposed: NFC as net/http connects
		"https://\u0130.example/x":        "https://i\u0307.example", // mapped before lower-casing, which would give "i"
		"https://[::1]:8443/x":            "https://[::1]:8443",
		// A label IDNA refuses does not keep a valid A-label beside it in punycode.
		"https://under_score.xn--bcher-kva.example/x": "https://under_score.bücher.example",
		"https://192.0.2.1/x":                         "https://192.0.2.1",
	} {
		h, err := keys.authHeader(endpoint, "mailto:ops@example.invalid", time.Unix(1, 0))
		if err != nil {
			t.Fatal(err)
		}
		claims, _ := b64.DecodeString(strings.Split(strings.TrimPrefix(h, "vapid t="), ".")[1])
		if !strings.Contains(string(claims), `"aud":"`+want+`"`) {
			t.Errorf("%s: claims %s, want aud %s", endpoint, claims, want)
		}
	}
}

func TestBodyLimitAndRecordSize(t *testing.T) {
	sub, _, _ := newSub(t, "https://fcm.googleapis.com/x")
	body, err := Encrypt(sub, make([]byte, MaxPayload))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 4096 {
		t.Fatalf("a full payload makes a %d-byte body; services accept 4096", len(body))
	}
	// rs is the record size, big-endian, at bytes 16..20.
	if got := int(body[16])<<24 | int(body[17])<<16 | int(body[18])<<8 | int(body[19]); got != 4096 {
		t.Fatalf("rs = %d", got)
	}
	if _, err := Encrypt(sub, make([]byte, MaxPayload+1)); err == nil {
		t.Fatal("payload over the limit accepted")
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection reset")
}

func TestSendErrorHasNoURL(t *testing.T) {
	keys, _, _ := GenerateKeys()
	s := NewSender(keys, "mailto:ops@example.invalid")
	s.HTTP = &http.Client{Transport: failingTransport{}}
	sub, _, _ := newSub(t, "https://fcm.googleapis.com/fcm/send/SECRET-TOKEN?q=1")
	err := s.Send(context.Background(), sub, []byte("x"), Options{})
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN") || !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("err = %v", err)
	}
}
