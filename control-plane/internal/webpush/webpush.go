// Package webpush sends Web Push messages (RFC 8030) with VAPID (RFC 8292) and aes128gcm
// payload encryption (RFC 8291), using only the standard library.
//
// The Control Plane is the sender so that a push still arrives while the member's workspace is
// stopped. The VAPID key pair belongs to the deployment: a native app would need the publisher's
// APNs/FCM keys, which is the relay this product does not run.
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/idna"
)

// MaxPayload is the plaintext limit of RFC 8291 §4: a push service must accept a 4096-byte body,
// of which 86 bytes are the header (salt, rs, idlen, 65-byte key) and 17 the delimiter and tag.
const MaxPayload = 3993

const recordSize = 4096

// ErrGone means the push service says the subscription no longer exists (404 or 410). The
// caller deletes it: retrying a gone endpoint only wastes the push service's goodwill.
var ErrGone = errors.New("webpush: subscription is gone")

// Subscription is what the browser's PushManager.subscribe() returns.
type Subscription struct {
	Endpoint string
	P256dh   string // base64url, 65-byte uncompressed point
	Auth     string // base64url, 16 bytes
}

// Keys is a deployment's VAPID key pair.
type Keys struct {
	priv *ecdsa.PrivateKey
}

// ParsePrivateKey reads a VAPID private key as the unpadded base64url of its 32-byte scalar,
// the form `web-push generate-vapid-keys` and most libraries print.
func ParsePrivateKey(s string) (*Keys, error) {
	raw, err := b64.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != 32 {
		return nil, errors.New("webpush: VAPID private key must be 32 bytes of unpadded base64url")
	}
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		return nil, fmt.Errorf("webpush: VAPID private key: %w", err)
	}
	return &Keys{priv: priv}, nil
}

// GenerateKeys makes a fresh pair and returns it with its private scalar in the form
// ParsePrivateKey reads.
func GenerateKeys() (*Keys, string, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", err
	}
	raw, err := priv.Bytes()
	if err != nil {
		return nil, "", err
	}
	return &Keys{priv: priv}, b64.EncodeToString(raw), nil
}

var b64 = base64.RawURLEncoding

// PublicKey is the applicationServerKey the browser subscribes with: the unpadded base64url of
// the 65-byte uncompressed public point.
func (k *Keys) PublicKey() string {
	pub, _ := k.priv.PublicKey.Bytes()
	return b64.EncodeToString(pub)
}

// authHeader builds the VAPID Authorization header value for a push service origin.
func (k *Keys) authHeader(endpoint, subject string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("webpush: bad endpoint")
	}
	hdr, _ := json.Marshal(map[string]string{"typ": "JWT", "alg": "ES256"})
	// RFC 8292 §2 caps exp at 24 hours; 12 leaves room for clock skew.
	claims, _ := json.Marshal(map[string]any{
		"aud": origin(u), "exp": now.Add(12 * time.Hour).Unix(), "sub": subject})
	signing := b64.EncodeToString(hdr) + "." + b64.EncodeToString(claims)
	h := sha256.Sum256([]byte(signing))
	der, err := ecdsa.SignASN1(rand.Reader, k.priv, h[:])
	if err != nil {
		return "", err
	}
	var sig struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(der, &sig); err != nil {
		return "", err
	}
	raw := make([]byte, 64)
	sig.R.FillBytes(raw[:32])
	sig.S.FillBytes(raw[32:])
	return "vapid t=" + signing + "." + b64.EncodeToString(raw) + ", k=" + k.PublicKey(), nil
}

// origin serialises u as RFC 6454 §6.1 does, which RFC 8292 §2 requires for aud: the Unicode
// serialization (each punycode label turned back into Unicode, label by label so that one label
// IDNA refuses does not keep the others in punycode), a lower-case host, and no port when it is
// the scheme's default. Every default push service is plain ASCII, where both serializations agree.
func origin(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	} else {
		labels := strings.Split(host, ".")
		for i, l := range labels {
			if strings.HasPrefix(l, "xn--") {
				if uni, err := idna.Lookup.ToUnicode(l); err == nil {
					labels[i] = uni
				}
			}
		}
		host = strings.Join(labels, ".")
	}
	scheme := strings.ToLower(u.Scheme)
	if port := u.Port(); port != "" && !(scheme == "https" && port == "443") && !(scheme == "http" && port == "80") {
		host += ":" + port
	}
	return scheme + "://" + host
}

// Encrypt produces the aes128gcm request body (RFC 8291 §3.4) for one subscription.
func Encrypt(sub Subscription, plaintext []byte) ([]byte, error) {
	if len(plaintext) > MaxPayload {
		return nil, fmt.Errorf("webpush: payload is %d bytes, the limit is %d", len(plaintext), MaxPayload)
	}
	uaRaw, err := b64.DecodeString(sub.P256dh)
	if err != nil {
		return nil, errors.New("webpush: bad p256dh")
	}
	authSecret, err := b64.DecodeString(sub.Auth)
	if err != nil || len(authSecret) != 16 {
		return nil, errors.New("webpush: bad auth secret")
	}
	curve := ecdh.P256()
	uaPub, err := curve.NewPublicKey(uaRaw)
	if err != nil {
		return nil, errors.New("webpush: bad p256dh point")
	}
	asPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	asPub := asPriv.PublicKey().Bytes()
	secret, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	cek, nonce, err := deriveKeys(secret, authSecret, uaRaw, asPub, salt)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// A single, last record: the 0x02 delimiter marks it as the last one.
	record := append(append([]byte{}, plaintext...), 0x02)
	var out bytes.Buffer
	out.Write(salt)
	var rs [4]byte
	rs[0], rs[1], rs[2], rs[3] = 0, 0, byte(recordSize>>8), byte(recordSize&0xff)
	out.Write(rs[:])
	out.WriteByte(byte(len(asPub)))
	out.Write(asPub)
	out.Write(gcm.Seal(nil, nonce, record, nil))
	return out.Bytes(), nil
}

func deriveKeys(secret, authSecret, uaPub, asPub, salt []byte) (cek, nonce []byte, err error) {
	prkKey, err := hkdf.Extract(sha256.New, secret, authSecret)
	if err != nil {
		return nil, nil, err
	}
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPub...), asPub...)
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(keyInfo), 32)
	if err != nil {
		return nil, nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, nil, err
	}
	if cek, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16); err != nil {
		return nil, nil, err
	}
	nonce, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	return cek, nonce, err
}

// Sender posts encrypted messages to push services.
type Sender struct {
	Keys    *Keys
	Subject string // mailto: or https: contact the push service can reach (RFC 8292 §2.1)
	// HTTP must not follow redirects: an endpoint is member-supplied, and a redirect would walk
	// the Control Plane to a host the endpoint check never saw. NewSender sets that up.
	HTTP *http.Client
	Now  func() time.Time
}

func NewSender(keys *Keys, subject string) *Sender {
	return &Sender{Keys: keys, Subject: subject, Now: time.Now, HTTP: &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// classify names a transport failure without any text from the error.
func classify(err error) string {
	var dns *net.DNSError
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &dns):
		return "dns"
	case errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ECONNRESET):
		return "connection"
	}
	if strings.Contains(err.Error(), "Location") {
		return "bad redirect"
	}
	return "transport error"
}

// Options of one message. TTL is how long the service may hold it for an offline device.
type Options struct {
	TTL     time.Duration
	Urgency string // very-low | low | normal | high; empty = normal
	Topic   string // replaces a queued message with the same topic (at most 32 URL-safe characters)
}

// Send delivers payload to sub. It returns ErrGone for a subscription the service has dropped;
// any other non-2xx answer is a plain error, and the caller must not delete the subscription
// on it (a 5xx or 429 says nothing about the subscription).
func (s *Sender) Send(ctx context.Context, sub Subscription, payload []byte, o Options) error {
	body, err := Encrypt(sub, payload)
	if err != nil {
		return err
	}
	auth, err := s.Keys.authHeader(sub.Endpoint, s.Subject, s.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	ttl := o.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.FormatInt(int64(ttl/time.Second), 10))
	if o.Urgency != "" {
		req.Header.Set("Urgency", o.Urgency)
	}
	if o.Topic != "" {
		req.Header.Set("Topic", o.Topic)
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		// Neither *url.Error nor net/http's inner errors can be printed: both may carry the
		// endpoint or a redirect's Location, and the endpoint is a bearer URL for the member's
		// browser. Only a class leaves this function.
		return errors.New("webpush: request failed: " + classify(err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrGone
	default:
		return fmt.Errorf("webpush: push service answered %d", resp.StatusCode)
	}
}
