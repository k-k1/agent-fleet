package main

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// fakeKMS stands in for AWS KMS: one root key that seals data keys with the request's
// encryption context as AAD, so a Decrypt under another context fails as KMS's does.
type fakeKMS struct {
	keyID string
	root  cipher.AEAD
	err   error // returned by every call while set

	mu       sync.Mutex
	gens     int
	decrypts int
	contexts []map[string]string
	issued   [][]byte // every plaintext data key handed out, to check they get erased
}

func newFakeKMS(t *testing.T) *fakeKMS {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	g, err := kmsGCM(k)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeKMS{keyID: "arn:aws:kms:ap-northeast-1:111122223333:key/test", root: g}
}

func fakeContextAAD(ec map[string]string) []byte {
	keys := make([]string, 0, len(ec))
	for k := range ec {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%q=%q;", k, ec[k])
	}
	return []byte(b.String())
}

func (f *fakeKMS) GenerateDataKey(_ context.Context, in *kms.GenerateDataKeyInput, _ ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gens++
	f.contexts = append(f.contexts, in.EncryptionContext)
	if f.err != nil {
		return nil, f.err
	}
	if aws.ToString(in.KeyId) != f.keyID {
		return nil, errors.New("NotFoundException")
	}
	pt := make([]byte, 32)
	nonce := make([]byte, f.root.NonceSize())
	io.ReadFull(rand.Reader, pt)
	io.ReadFull(rand.Reader, nonce)
	blob := f.root.Seal(append([]byte(nil), nonce...), nonce, pt, fakeContextAAD(in.EncryptionContext))
	f.issued = append(f.issued, pt)
	return &kms.GenerateDataKeyOutput{Plaintext: pt, CiphertextBlob: blob, KeyId: in.KeyId}, nil
}

func (f *fakeKMS) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.decrypts++
	f.contexts = append(f.contexts, in.EncryptionContext)
	if f.err != nil {
		return nil, f.err
	}
	if aws.ToString(in.KeyId) != f.keyID {
		return nil, errors.New("IncorrectKeyException")
	}
	n := f.root.NonceSize()
	if len(in.CiphertextBlob) < n {
		return nil, errors.New("InvalidCiphertextException")
	}
	pt, err := f.root.Open(nil, in.CiphertextBlob[:n], in.CiphertextBlob[n:], fakeContextAAD(in.EncryptionContext))
	if err != nil {
		return nil, errors.New("InvalidCiphertextException")
	}
	f.issued = append(f.issued, pt)
	return &kms.DecryptOutput{Plaintext: pt, KeyId: in.KeyId}, nil
}

func (f *fakeKMS) counts() (gens, decrypts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gens, f.decrypts
}

// spyLegacy records whether the local custodian was consulted.
type spyLegacy struct {
	KeyCustodian
	unwraps int
}

func (s *spyLegacy) Unwrap(ctx context.Context, keyRef, ct string) ([]byte, error) {
	s.unwraps++
	return s.KeyCustodian.Unwrap(ctx, keyRef, ct)
}

func testMaster(t *testing.T) []byte {
	t.Helper()
	m := sha256.Sum256([]byte("test-master"))
	return m[:]
}

func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestKMSCustodianRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newFakeKMS(t)
	c := newKMSCustodian(f, f.keyID, newLocalCustodian(testMaster(t)), 0)

	// A DEK, and a payload past KMS Encrypt's 4 KiB limit (a session handoff body).
	for _, payload := range [][]byte{randBytes(t, 32), randBytes(t, 64<<10)} {
		ct, err := c.Wrap(ctx, "tenant-a", payload)
		if err != nil {
			t.Fatalf("wrap: %v", err)
		}
		if !strings.HasPrefix(ct, kmsSealPrefix) {
			t.Fatalf("sealed value %q lacks %q", ct[:10], kmsSealPrefix)
		}
		got, err := c.Unwrap(ctx, "tenant-a", ct)
		if err != nil {
			t.Fatalf("unwrap: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatal("round trip mismatch")
		}
	}
	for _, ec := range f.contexts {
		if ec[kmsContextPurposeKey] != kmsContextPurpose || ec[kmsContextKeyRefKey] != "tenant-a" || len(ec) != 2 {
			t.Fatalf("encryption context = %v, want purpose + key_ref only", ec)
		}
	}
}

// A value sealed for one key ref must not open under another, whether or not the data
// key is already cached.
func TestKMSCustodianRefusesAnotherKeyRef(t *testing.T) {
	ctx := context.Background()
	for _, ttl := range []time.Duration{0, time.Hour} {
		f := newFakeKMS(t)
		c := newKMSCustodian(f, f.keyID, nil, ttl)
		ct, err := c.Wrap(ctx, "tenant-a", randBytes(t, 32))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Unwrap(ctx, "tenant-a", ct); err != nil {
			t.Fatalf("ttl %v: own key ref: %v", ttl, err)
		}
		_, err = c.Unwrap(ctx, "tenant-b", ct)
		if err == nil {
			t.Fatalf("ttl %v: a value sealed for tenant-a opened as tenant-b", ttl)
		}
		if !strings.Contains(err.Error(), "InvalidCiphertextException") {
			t.Fatalf("ttl %v: refused by %v, want KMS's encryption-context check", ttl, err)
		}
	}
}

// KMS down: nothing is sealed, nothing is opened, and the local key is never tried.
func TestKMSCustodianFailsClosed(t *testing.T) {
	ctx := context.Background()
	f := newFakeKMS(t)
	legacy := &spyLegacy{KeyCustodian: newLocalCustodian(testMaster(t))}
	c := newKMSCustodian(f, f.keyID, legacy, 0)
	ct, err := c.Wrap(ctx, "tenant-a", randBytes(t, 32))
	if err != nil {
		t.Fatal(err)
	}

	f.err = errors.New("dial tcp: i/o timeout")
	if out, err := c.Wrap(ctx, "tenant-a", randBytes(t, 32)); err == nil || out != "" {
		t.Fatalf("wrap with KMS down = %q, %v; want an error and no value", out, err)
	} else if !strings.Contains(err.Error(), "GenerateDataKey") || !strings.Contains(err.Error(), "i/o timeout") {
		t.Fatalf("wrap error %q does not say what failed", err)
	}
	if out, err := c.Unwrap(ctx, "tenant-a", ct); err == nil || out != nil {
		t.Fatalf("unwrap with KMS down = %v, %v; want an error", out, err)
	} else if !strings.Contains(err.Error(), "Decrypt") || !strings.Contains(err.Error(), "i/o timeout") {
		t.Fatalf("unwrap error %q does not say what failed", err)
	}
	if legacy.unwraps != 0 {
		t.Fatalf("the local custodian was consulted %d times while KMS was down", legacy.unwraps)
	}

	// A different key than the configured one is refused by KMS too.
	f.err = nil
	other := newKMSCustodian(f, "arn:aws:kms:ap-northeast-1:111122223333:key/other", legacy, 0)
	if _, err := other.Unwrap(ctx, "tenant-a", ct); err == nil {
		t.Fatal("a custodian configured with another key opened the value")
	}
}

func TestKMSCustodianCacheTTL(t *testing.T) {
	ctx := context.Background()
	f := newFakeKMS(t)
	sealer := newKMSCustodian(f, f.keyID, nil, 0)
	ct, err := sealer.Wrap(ctx, "tenant-a", randBytes(t, 32))
	if err != nil {
		t.Fatal(err)
	}

	now := time.Unix(1_800_000_000, 0)
	c := newKMSCustodian(f, f.keyID, nil, time.Minute)
	c.now = func() time.Time { return now }
	unwrap := func() {
		t.Helper()
		if _, err := c.Unwrap(ctx, "tenant-a", ct); err != nil {
			t.Fatal(err)
		}
	}
	unwrap()
	unwrap()
	if _, d := f.counts(); d != 1 {
		t.Fatalf("two opens inside the TTL made %d Decrypt calls, want 1", d)
	}
	// While cached, a KMS outage does not stop an open — the documented TTL window.
	f.err = errors.New("down")
	unwrap()
	f.err = nil
	now = now.Add(time.Minute + time.Second)
	unwrap()
	if _, d := f.counts(); d != 2 {
		t.Fatalf("an open after the TTL made %d Decrypt calls in all, want 2", d)
	}
	// Expired and KMS refusing: the cache must not serve the stale key.
	now = now.Add(2 * time.Minute)
	f.err = errors.New("DisabledException")
	if _, err := c.Unwrap(ctx, "tenant-a", ct); err == nil {
		t.Fatal("an expired cache entry opened a value while KMS refuses the key")
	}

	// TTL 0 turns the cache off.
	f.err = nil
	_, before := f.counts()
	off := newKMSCustodian(f, f.keyID, nil, 0)
	for i := 0; i < 3; i++ {
		if _, err := off.Unwrap(ctx, "tenant-a", ct); err != nil {
			t.Fatal(err)
		}
	}
	if _, d := f.counts(); d-before != 3 {
		t.Fatalf("cache off: 3 opens made %d Decrypt calls, want 3", d-before)
	}
}

func TestKMSCustodianCacheIsBounded(t *testing.T) {
	ctx := context.Background()
	f := newFakeKMS(t)
	c := newKMSCustodian(f, f.keyID, nil, time.Hour)
	for i := 0; i < kmsDataKeyCacheMax+50; i++ {
		if _, err := c.Wrap(ctx, fmt.Sprintf("tenant-%d", i), randBytes(t, 32)); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(c.cache); n > kmsDataKeyCacheMax {
		t.Fatalf("cache holds %d keys, bound is %d", n, kmsDataKeyCacheMax)
	}
}

// Migration (ADR 0005 addendum 2026-10-04): a deployment that switches to kms keeps
// opening what the local custodian sealed, without asking KMS, and seals anew under KMS.
func TestKMSCustodianOpensLocalSealedValues(t *testing.T) {
	ctx := context.Background()
	local := newLocalCustodian(testMaster(t))
	secret := randBytes(t, 32)
	old, err := local.Wrap(ctx, "tenant-a", secret)
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeKMS(t)
	c := newKMSCustodian(f, f.keyID, local, 0)
	got, err := c.Unwrap(ctx, "tenant-a", old)
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("local-sealed value under kms = %v, %v", got, err)
	}
	if g, d := f.counts(); g+d != 0 {
		t.Fatalf("opening a local-sealed value called KMS %d times", g+d)
	}
	// The key-ref binding of the local format still holds.
	if _, err := c.Unwrap(ctx, "tenant-b", old); err == nil {
		t.Fatal("a local-sealed value opened under another key ref")
	}
	// Without a local custodian the old format is refused, not guessed at.
	if _, err := newKMSCustodian(f, f.keyID, nil, 0).Unwrap(ctx, "tenant-a", old); err == nil {
		t.Fatal("a local-format value opened with no local custodian")
	}
	// And switching back to local names the cause instead of a base64 error.
	sealed, err := c.Wrap(ctx, "tenant-a", secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.Unwrap(ctx, "tenant-a", sealed); err == nil || !strings.Contains(err.Error(), "AF_KEY_CUSTODIAN=kms") {
		t.Fatalf("local custodian on a kms value = %v, want it to name AF_KEY_CUSTODIAN=kms", err)
	}
}

// The DEK a workspace already has survives the switch: resolveDEK under kms returns the
// value the local custodian stored, so the existing secrets.enc keeps decrypting.
func TestResolveDEKAfterSwitchToKMS(t *testing.T) {
	ctx := context.Background()
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			tn, err := st.CreateTenant(ctx, "sales", "Sales")
			if err != nil {
				t.Fatal(err)
			}
			mk := func(wid, userKey string) store.Workspace {
				id, _ := st.UpsertIdentity(ctx, userKey+"@acme.co.jp", userKey, "")
				mem, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
				if err != nil {
					t.Fatal(err)
				}
				ws := store.Workspace{ID: wid, TenantID: tn.ID, MembershipID: mem.ID,
					ContainerName: "af-ws-" + wid, DataDir: "/srv/data/" + wid,
					AgentPort: "7731", AgentToken: "tok", State: "stopped", CreatedAt: store.NowTS()}
				if err := st.CreateWorkspace(ctx, ws); err != nil {
					t.Fatal(err)
				}
				return ws
			}
			oldWS := mk("W-old", "a-acme-co-jp")

			mgr := p3Manager(t, st)
			mgr.master32 = testMaster(t)
			mgr.custodian = newLocalCustodian(mgr.master32)
			before, err := mgr.resolveWrappedDEK(ctx, oldWS, "a-acme-co-jp")
			if err != nil || before == "" {
				t.Fatalf("local resolveDEK = %q, %v", before, err)
			}

			f := newFakeKMS(t)
			mgr.custodian = newKMSCustodian(f, f.keyID, newLocalCustodian(mgr.master32), 0)
			after, err := mgr.resolveWrappedDEK(ctx, oldWS, "a-acme-co-jp")
			if err != nil || after != before {
				t.Fatalf("after the switch resolveDEK = %q, %v; want the stored %q", after, err, before)
			}

			// A workspace created after the switch is wrapped by KMS.
			newWS := mk("W-new", "b-acme-co-jp")
			dek, err := mgr.resolveWrappedDEK(ctx, newWS, "b-acme-co-jp")
			if err != nil {
				t.Fatal(err)
			}
			ct, kr, ok, err := st.GetWrappedDEK(ctx, newWS.ID)
			if err != nil || !ok || kr != tn.ID || !strings.HasPrefix(ct, kmsSealPrefix) {
				t.Fatalf("new workspace's wrapped DEK = %.12q, %q, %v, %v; want a kms1: value", ct, kr, ok, err)
			}
			if want := hex.EncodeToString(mgr.legacyDEK("b-acme-co-jp")); dek != want {
				t.Fatalf("new workspace's DEK = %s, want the legacy derivation %s", dek, want)
			}
		})
	}
}

func TestNewKeyCustodianConfig(t *testing.T) {
	ctx := context.Background()
	env := func(kv map[string]string) func(string) string {
		return func(k string) string { return kv[k] }
	}
	master := testMaster(t)
	keyARN := "arn:aws:kms:eu-west-1:111122223333:key/0000"

	if c, err := newKeyCustodian(ctx, "local", nil, env(nil)); err != nil || c != nil {
		t.Fatalf("local without master = %v, %v; want no custodian", c, err)
	}
	if c, err := newKeyCustodian(ctx, "local", master, env(nil)); err != nil {
		t.Fatal(err)
	} else if _, ok := c.(*localCustodian); !ok {
		t.Fatalf("local = %T", c)
	}
	for _, tc := range []struct {
		name   string
		kind   string
		master []byte
		env    map[string]string
		want   string
	}{
		{"kms without master", "kms", nil, map[string]string{"AF_KMS_KEY_ID": keyARN}, "AF_MASTER_KEY"},
		{"kms without key", "kms", master, nil, "AF_KMS_KEY_ID"},
		{"bad ttl", "kms", master, map[string]string{"AF_KMS_KEY_ID": keyARN, "AF_KMS_DATA_KEY_CACHE_TTL": "soon"}, "AF_KMS_DATA_KEY_CACHE_TTL"},
		{"unknown kind", "vault", master, nil, "want local or kms"},
	} {
		if c, err := newKeyCustodian(ctx, tc.kind, tc.master, env(tc.env)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s = %v, %v; want an error naming %s", tc.name, c, err, tc.want)
		}
	}
	c, err := newKeyCustodian(ctx, "kms", master, env(map[string]string{"AF_KMS_KEY_ID": keyARN, "AF_KMS_DATA_KEY_CACHE_TTL": "0"}))
	if err != nil {
		t.Fatal(err)
	}
	kc, ok := c.(*kmsCustodian)
	if !ok || kc.keyID != keyARN || kc.ttl != 0 || kc.legacy == nil {
		t.Fatalf("kms = %#v", c)
	}
	if r := kmsRegion(keyARN, env(map[string]string{"AWS_REGION": "us-east-1"})); r != "eu-west-1" {
		t.Errorf("region from key ARN = %q", r)
	}
	if r := kmsRegion("alias/af", env(map[string]string{"AWS_REGION": "us-east-1"})); r != "us-east-1" {
		t.Errorf("region for a bare alias = %q", r)
	}
}

func isZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// Plaintext data keys are erased once used, and a cached copy when it is evicted,
// replaced or expires — also on a quiet deployment, through the timer sweep.
func TestKMSCustodianErasesPlaintextKeys(t *testing.T) {
	ctx := context.Background()
	f := newFakeKMS(t)
	c := newKMSCustodian(f, f.keyID, nil, 0)
	ct, err := c.Wrap(ctx, "tenant-a", randBytes(t, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Unwrap(ctx, "tenant-a", ct); err != nil {
		t.Fatal(err)
	}
	for i, k := range f.issued {
		if len(k) != 32 || !isZero(k) {
			t.Fatalf("plaintext data key %d from KMS was not erased after use", i)
		}
	}

	now := time.Unix(1_800_000_000, 0)
	cached := newKMSCustodian(f, f.keyID, nil, time.Minute)
	cached.now = func() time.Time { return now }
	entry := func() []byte {
		t.Helper()
		if len(cached.cache) != 1 {
			t.Fatalf("cache holds %d entries, want 1", len(cached.cache))
		}
		for _, e := range cached.cache {
			return e.key
		}
		return nil
	}
	if _, err := cached.Unwrap(ctx, "tenant-a", ct); err != nil {
		t.Fatal(err)
	}
	first := entry()
	// Replaced: the same blob decrypted again after expiry.
	now = now.Add(2 * time.Minute)
	if _, err := cached.Unwrap(ctx, "tenant-a", ct); err != nil {
		t.Fatal(err)
	}
	if !isZero(first) {
		t.Fatal("an expired cache entry was replaced without being erased")
	}
	second := entry()
	// Expired while KMS refuses: dropped and erased, not left in the map.
	now = now.Add(2 * time.Minute)
	f.err = errors.New("DisabledException")
	if _, err := cached.Unwrap(ctx, "tenant-a", ct); err == nil {
		t.Fatal("an expired entry opened a value while KMS refuses")
	}
	if len(cached.cache) != 0 || !isZero(second) {
		t.Fatalf("expired entry left behind: %d entries, erased=%v", len(cached.cache), isZero(second))
	}
	// Nobody calls again: the timer sweep alone erases an expired entry.
	f.err = nil
	if _, err := cached.Unwrap(ctx, "tenant-a", ct); err != nil {
		t.Fatal(err)
	}
	third := entry()
	if cached.sweep == nil {
		t.Fatal("caching a key armed no sweep")
	}
	now = now.Add(2 * time.Minute)
	cached.sweepTick()
	if len(cached.cache) != 0 || !isZero(third) || cached.sweep != nil {
		t.Fatalf("sweep left %d entries, erased=%v, re-armed=%v", len(cached.cache), isZero(third), cached.sweep != nil)
	}
}
