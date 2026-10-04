package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// kmsSealPrefix marks a value sealed by kmsCustodian. The prefix is what decides which
// custodian opens a stored value — never the outcome of a KMS call — so a KMS outage can
// not make the CP read a row with the local key instead (fail closed, ADR 0005 addendum
// 2026-10-04). ':' is outside the base64 alphabet, so no local-format value carries it.
const kmsSealPrefix = "kms1:"

// The encryption context every KMS call carries. The deploy templates key the CP task
// role's grant and the key policy's deny on these exact names and this purpose value
// (deploy/aws/ecs/cfn/10-data.yaml, 30-ingress.yaml; deploy/local/cfn-cp-kms-scope-test.py
// reads them from here), so renaming one turns every seal and open into AccessDenied.
const (
	kmsContextPurposeKey = "af:purpose"
	kmsContextPurpose    = "agent-fleet-custodian"
	kmsContextKeyRefKey  = "af:key_ref"
)

// Bounds of the unwrapped data-key cache. The TTL is also how long a disabled KMS key
// keeps opening values this process has already opened, so crypto-shredding takes effect
// within one TTL, not instantly. An expired key is zeroed and dropped by the sweep, which
// runs on every call and on a timer, so it stays in memory for at most two TTLs.
const (
	kmsDataKeyCacheTTLDefault = 5 * time.Minute
	kmsDataKeyCacheMax        = 1024
)

// kmsAPI is the slice of the KMS client the custodian calls; tests substitute a fake.
type kmsAPI interface {
	GenerateDataKey(ctx context.Context, in *kms.GenerateDataKeyInput, opts ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error)
	Decrypt(ctx context.Context, in *kms.DecryptInput, opts ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

// kmsCustodian is the AWS KeyCustodian (ADR 0005). Each Wrap asks KMS for a fresh
// AES-256 data key under the deployment's KMS key and seals the payload with it locally,
// because the custodian also seals values larger than KMS Encrypt's 4 KiB limit
// (session handoff and share bodies). The encryption context binds the key ref, so a
// sealed value moved to another tenant's row does not open.
//
// Values without kmsSealPrefix were sealed by the local custodian before the switch and
// are opened by legacy; they stay protected by AF_MASTER_KEY alone until rewritten.
type kmsCustodian struct {
	api    kmsAPI
	keyID  string
	legacy KeyCustodian // nil: a value in the local format is refused
	ttl    time.Duration
	now    func() time.Time

	mu    sync.Mutex
	cache map[[sha256.Size]byte]kmsCachedKey
	sweep *time.Timer // pending sweep while the cache holds entries; guarded by mu
}

type kmsCachedKey struct {
	key     []byte
	expires time.Time
}

var _ KeyCustodian = (*kmsCustodian)(nil)

func newKMSCustodian(api kmsAPI, keyID string, legacy KeyCustodian, ttl time.Duration) *kmsCustodian {
	return &kmsCustodian{
		api: api, keyID: keyID, legacy: legacy, ttl: ttl, now: time.Now,
		cache: map[[sha256.Size]byte]kmsCachedKey{},
	}
}

func kmsEncryptionContext(keyRef string) map[string]string {
	return map[string]string{kmsContextPurposeKey: kmsContextPurpose, kmsContextKeyRefKey: keyRef}
}

func (c *kmsCustodian) Wrap(ctx context.Context, keyRef string, dek []byte) (string, error) {
	if keyRef == "" {
		return "", errors.New("kms custodian: empty key ref")
	}
	out, err := c.api.GenerateDataKey(ctx, &kms.GenerateDataKeyInput{
		KeyId:             aws.String(c.keyID),
		KeySpec:           kmstypes.DataKeySpecAes256,
		EncryptionContext: kmsEncryptionContext(keyRef),
	})
	if err != nil {
		return "", fmt.Errorf("kms custodian: GenerateDataKey for key ref %q failed, nothing was sealed: %w", keyRef, err)
	}
	// The plaintext data key is ours to erase; the cache keeps its own copy.
	defer clear(out.Plaintext)
	if len(out.Plaintext) != 32 || len(out.CiphertextBlob) == 0 || len(out.CiphertextBlob) > 0xffff {
		return "", errors.New("kms custodian: GenerateDataKey returned a malformed data key")
	}
	g, err := kmsGCM(out.Plaintext)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	buf := make([]byte, 2, 2+len(out.CiphertextBlob)+len(nonce)+len(dek)+g.Overhead())
	binary.BigEndian.PutUint16(buf, uint16(len(out.CiphertextBlob)))
	buf = append(buf, out.CiphertextBlob...)
	buf = append(buf, nonce...)
	buf = g.Seal(buf, nonce, dek, []byte(keyRef))
	c.remember(keyRef, out.CiphertextBlob, out.Plaintext)
	return kmsSealPrefix + base64.StdEncoding.EncodeToString(buf), nil
}

func (c *kmsCustodian) Unwrap(ctx context.Context, keyRef, ciphertext string) ([]byte, error) {
	body, ok := strings.CutPrefix(ciphertext, kmsSealPrefix)
	if !ok {
		if c.legacy == nil {
			return nil, errors.New("kms custodian: value is in the local custodian's format and no AF_MASTER_KEY is configured to open it")
		}
		return c.legacy.Unwrap(ctx, keyRef, ciphertext)
	}
	if keyRef == "" {
		return nil, errors.New("kms custodian: empty key ref")
	}
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("kms custodian: %w", err)
	}
	if len(raw) < 2 {
		return nil, errors.New("kms custodian: sealed value too short")
	}
	n := int(binary.BigEndian.Uint16(raw))
	raw = raw[2:]
	if n == 0 || len(raw) < n {
		return nil, errors.New("kms custodian: sealed value too short")
	}
	blob, rest := raw[:n], raw[n:]
	key, err := c.dataKey(ctx, keyRef, blob)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	g, err := kmsGCM(key)
	if err != nil {
		return nil, err
	}
	if len(rest) < g.NonceSize() {
		return nil, errors.New("kms custodian: sealed value too short")
	}
	out, err := g.Open(nil, rest[:g.NonceSize()], rest[g.NonceSize():], []byte(keyRef))
	if err != nil {
		return nil, fmt.Errorf("kms custodian: open sealed value for key ref %q: %w", keyRef, err)
	}
	return out, nil
}

// dataKey returns a copy of the plaintext data key inside blob, from the cache or from
// KMS; the caller erases it. KMS checks the encryption context, so a blob generated under
// another key ref is refused there (InvalidCiphertextException) before the AAD check gets
// a say.
func (c *kmsCustodian) dataKey(ctx context.Context, keyRef string, blob []byte) ([]byte, error) {
	id := kmsCacheID(keyRef, blob)
	if c.ttl > 0 {
		c.mu.Lock()
		c.sweepExpiredLocked(c.now())
		if e, ok := c.cache[id]; ok {
			// Copied under the lock: an eviction erases the cached slice in place.
			key := append([]byte(nil), e.key...)
			c.mu.Unlock()
			return key, nil
		}
		c.mu.Unlock()
	}
	out, err := c.api.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob:    blob,
		KeyId:             aws.String(c.keyID),
		EncryptionContext: kmsEncryptionContext(keyRef),
	})
	if err != nil {
		return nil, fmt.Errorf("kms custodian: Decrypt of the data key for key ref %q failed, the value stays sealed: %w", keyRef, err)
	}
	if len(out.Plaintext) != 32 {
		clear(out.Plaintext)
		return nil, errors.New("kms custodian: Decrypt returned a malformed data key")
	}
	c.remember(keyRef, blob, out.Plaintext)
	return out.Plaintext, nil
}

func (c *kmsCustodian) remember(keyRef string, blob, key []byte) {
	if c.ttl <= 0 {
		return
	}
	now := c.now()
	id := kmsCacheID(keyRef, blob)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepExpiredLocked(now)
	c.dropLocked(id)
	// Still full of live entries: drop the one closest to expiry.
	if len(c.cache) >= kmsDataKeyCacheMax {
		var oldest [sha256.Size]byte
		var at time.Time
		first := true
		for k, e := range c.cache {
			if first || e.expires.Before(at) {
				oldest, at, first = k, e.expires, false
			}
		}
		c.dropLocked(oldest)
	}
	c.cache[id] = kmsCachedKey{key: append([]byte(nil), key...), expires: now.Add(c.ttl)}
	if c.sweep == nil {
		c.sweep = time.AfterFunc(c.ttl, c.sweepTick)
	}
}

// dropLocked erases and removes one cache entry, if present.
func (c *kmsCustodian) dropLocked(id [sha256.Size]byte) {
	if e, ok := c.cache[id]; ok {
		clear(e.key)
		delete(c.cache, id)
	}
}

func (c *kmsCustodian) sweepExpiredLocked(now time.Time) {
	for k, e := range c.cache {
		if !now.Before(e.expires) {
			c.dropLocked(k)
		}
	}
}

// sweepTick is the timer half of the sweep: without it a quiet deployment would keep an
// expired plaintext key until the next seal or open. It re-arms while entries remain.
func (c *kmsCustodian) sweepTick() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweep = nil
	c.sweepExpiredLocked(c.now())
	if len(c.cache) > 0 {
		c.sweep = time.AfterFunc(c.ttl, c.sweepTick)
	}
}

// kmsCacheID keys the cache on the key ref as well as the blob, so a hit can never
// skip the encryption-context check KMS would have made for another key ref.
func kmsCacheID(keyRef string, blob []byte) [sha256.Size]byte {
	h := sha256.New()
	var l [8]byte
	binary.BigEndian.PutUint64(l[:], uint64(len(keyRef)))
	h.Write(l[:])
	h.Write([]byte(keyRef))
	h.Write(blob)
	var id [sha256.Size]byte
	copy(id[:], h.Sum(nil))
	return id
}

func kmsGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// kmsCacheTTL parses AF_KMS_DATA_KEY_CACHE_TTL: unset = the default, "0" = no cache.
func kmsCacheTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return kmsDataKeyCacheTTLDefault, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("AF_KMS_DATA_KEY_CACHE_TTL=%q is not a non-negative duration", s)
	}
	return d, nil
}

// newKeyCustodian builds the custodian AF_KEY_CUSTODIAN selects. master32 nil means no
// AF_MASTER_KEY: local then yields no custodian (development, plaintext secrets), and
// kms is refused, because the master key still derives the legacy DEK, every bridge
// signing key, and opens the values sealed before the switch.
func newKeyCustodian(ctx context.Context, kind string, master32 []byte, getenv func(string) string) (KeyCustodian, error) {
	switch kind {
	case "", "local":
		if len(master32) == 0 {
			return nil, nil
		}
		return newLocalCustodian(master32), nil
	case "kms":
		if len(master32) == 0 {
			return nil, errors.New("AF_KEY_CUSTODIAN=kms needs AF_MASTER_KEY as well: it still derives the legacy DEK and the signing keys and opens values sealed before the switch")
		}
		keyID := strings.TrimSpace(getenv("AF_KMS_KEY_ID"))
		if keyID == "" {
			return nil, errors.New("AF_KEY_CUSTODIAN=kms needs AF_KMS_KEY_ID (the KMS key's ARN, or an alias ARN)")
		}
		ttl, err := kmsCacheTTL(getenv("AF_KMS_DATA_KEY_CACHE_TTL"))
		if err != nil {
			return nil, err
		}
		ac, err := awsConfigFor(ctx, kmsRegion(keyID, getenv))
		if err != nil {
			return nil, fmt.Errorf("AF_KEY_CUSTODIAN=kms: load AWS config: %w", err)
		}
		return newKMSCustodian(kms.NewFromConfig(ac), keyID, newLocalCustodian(master32), ttl), nil
	default:
		return nil, fmt.Errorf("AF_KEY_CUSTODIAN=%q: want local or kms", kind)
	}
}

// kmsRegion takes the region from a key or alias ARN (arn:aws:kms:<region>:…), so the
// key is found wherever the CP runs; a bare key id or alias name uses the SDK's region.
func kmsRegion(keyID string, getenv func(string) string) string {
	if p := strings.Split(keyID, ":"); len(p) >= 6 && p[0] == "arn" && p[2] == "kms" && p[3] != "" {
		return p[3]
	}
	for _, k := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if v := getenv(k); v != "" {
			return v
		}
	}
	return ""
}
