package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// rewrapSeed is one legacy value the test stored: where it is and what it opens to.
type rewrapSeed struct {
	id, keyRef string
	plain      []byte
}

// seedLegacyRows stores one value sealed by the local custodian in every rewrap target, plus a
// plaintext MCP row (key_ref "") the command must leave alone. Keyed by target name.
func seedLegacyRows(t *testing.T, st *store.SQL, local *localCustodian) (map[string]rewrapSeed, store.Workspace) {
	t.Helper()
	ctx := context.Background()
	tn, err := st.CreateTenant(ctx, "sales", "Sales")
	if err != nil {
		t.Fatal(err)
	}
	member := func(userKey string) string {
		id, err := st.UpsertIdentity(ctx, userKey+"@acme.co.jp", userKey, "")
		if err != nil {
			t.Fatal(err)
		}
		m, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
		if err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	owner, other := member("a-acme-co-jp"), member("b-acme-co-jp")
	ws := store.Workspace{ID: "W1", TenantID: tn.ID, MembershipID: owner,
		ContainerName: "af-ws-W1", DataDir: "/srv/data/W1",
		AgentPort: "7731", AgentToken: "tok", State: "stopped", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.DB().ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	seal := func(keyRef string, plain []byte) string {
		t.Helper()
		ct, err := local.Wrap(ctx, keyRef, plain)
		if err != nil {
			t.Fatal(err)
		}
		return ct
	}
	now := store.NowTS()
	exec(`INSERT INTO shared_session_catalog(id, workspace_id, owner_membership_id, name, kind, dir, repo, created_at, state, last_seen)
		VALUES('C1', ?, ?, 's', 'claude', '/d', 'r', ?, 'running', ?)`, ws.ID, owner, now, now)

	out := map[string]rewrapSeed{}
	put := func(name, id, keyRef string, plain []byte) string {
		out[name] = rewrapSeed{id: id, keyRef: keyRef, plain: plain}
		return seal(keyRef, plain)
	}
	dek := local.master32 // any 32 bytes; resolveDEK below must return exactly these
	if err := st.PutWrappedDEK(ctx, ws.ID, put("wrapped_dek", ws.ID, tn.ID, dek), tn.ID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO mcp_server(id, tenant_id, name, headers_enc, key_ref, created_at, updated_at) VALUES('M1', ?, 'a', ?, ?, ?, ?)`,
		tn.ID, put("mcp_server", "M1", tn.ID, []byte(`{"Authorization":"Bearer x"}`)), tn.ID, now, now)
	exec(`INSERT INTO mcp_server(id, tenant_id, name, headers_enc, key_ref, created_at, updated_at) VALUES('M2', ?, 'b', '{"X":"plain"}', '', ?, ?)`,
		tn.ID, now, now)
	exec(`INSERT INTO tenant_idp(id, tenant_id, name, issuer, client_id, secret_enc, key_ref, trust, created_at, updated_at)
		VALUES('I1', ?, 'okta', 'https://idp.example', 'cid', ?, ?, 'issuer', ?, ?)`,
		tn.ID, put("tenant_idp", "I1", tn.ID, []byte("idp-secret")), tn.ID, now, now)
	exec(`INSERT INTO tenant_git_oauth(id, tenant_id, provider, client_id, secret_enc, key_ref, created_at, updated_at)
		VALUES('G1', ?, 'github', 'cid', ?, ?, ?, ?)`,
		tn.ID, put("tenant_git_oauth", "G1", tn.ID, []byte("git-secret")), tn.ID, now, now)
	exec(`INSERT INTO session_share_proposal(id, tenant_id, catalog_id, owner_membership_id, proposer_membership_id, action, ciphertext, key_ref, created_at, expires_at)
		VALUES('P1', ?, 'C1', ?, ?, 'turn', ?, ?, ?, ?)`,
		tn.ID, owner, other, put("session_share_proposal", "P1", tn.ID, []byte(`{"text":"hi"}`)), tn.ID, now, now)
	exec(`INSERT INTO session_handoff_offer(id, tenant_id, catalog_id, owner_membership_id, recipient_membership_id, ciphertext, key_ref, created_at, expires_at)
		VALUES('H1', ?, 'C1', ?, ?, ?, ?, ?, ?)`,
		tn.ID, owner, other, put("session_handoff_offer", "H1", tn.ID, bytes.Repeat([]byte("p"), 9000)), tn.ID, now, now)

	setting := func(k, v string) {
		t.Helper()
		if err := st.SetSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	setting(engineHfTokenSetting, put(engineHfTokenSetting, engineHfTokenSetting, engineHfTokenKeyRef, []byte("hf_tok")))
	setting(engineHfTokenRefSetting, engineHfTokenKeyRef)
	setting(engineCivitaiTokenSetting, put(engineCivitaiTokenSetting, engineCivitaiTokenSetting, engineCivitaiTokenKeyRef, []byte("cv_tok")))
	setting(engineCivitaiTokenRefSetting, engineCivitaiTokenKeyRef)
	rec, _ := json.Marshal(engineComfyRecord{URL: "http://10.0.0.5:8188",
		KeyEnc: put(engineComfySetting, engineComfySetting, engineComfyKeyRef, []byte("comfy-key")),
		KeyRef: engineComfyKeyRef, By: "op@acme.co.jp", At: now})
	setting(engineComfySetting, string(rec))
	return out, ws
}

// openTarget reads a target's stored value back through the command's own load.
func openTarget(t *testing.T, tg rewrapTarget, id string) rewrapItem {
	t.Helper()
	it, ok, err := tg.load(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("%s %s: load = %v, %v", tg.name, id, ok, err)
	}
	return it
}

func countsByName(cs []rewrapCounts) map[string]rewrapCounts {
	out := map[string]rewrapCounts{}
	for _, c := range cs {
		out[c.Name] = c
	}
	return out
}

func discardLog(string, ...any) {}

func TestRewrapKeysReSealsEveryTarget(t *testing.T) {
	ctx := context.Background()
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			local := newLocalCustodian(testMaster(t))
			seeds, ws := seedLegacyRows(t, st, local)
			targets := rewrapTargets(st)
			for _, tg := range targets {
				if _, ok := seeds[tg.name]; !ok {
					t.Fatalf("target %s has no seeded row; seedLegacyRows must cover every target", tg.name)
				}
			}
			f := newFakeKMS(t)
			c := newKMSCustodian(f, f.keyID, local, 0)

			before := map[string]rewrapItem{}
			for _, tg := range targets {
				before[tg.name] = openTarget(t, tg, seeds[tg.name].id)
			}
			dry, err := rewrapKeys(ctx, c, targets, true, discardLog)
			if err != nil {
				t.Fatal(err)
			}
			for _, tg := range targets {
				n := countsByName(dry)[tg.name]
				if n.Legacy != 1 || n.KMS != 0 || n.Rewrapped != 0 {
					t.Errorf("dry run %s = %+v, want one legacy value", tg.name, n)
				}
				if got := openTarget(t, tg, seeds[tg.name].id); got != before[tg.name] {
					t.Errorf("dry run changed %s", tg.name)
				}
			}
			if n := countsByName(dry)["mcp_server"]; n.Plaintext != 1 {
				t.Errorf("dry run mcp_server plaintext = %d, want 1", n.Plaintext)
			}
			if g, d := f.counts(); g+d != 0 {
				t.Fatalf("dry run called KMS %d times", g+d)
			}

			got, err := rewrapKeys(ctx, c, targets, false, discardLog)
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range got {
				if n.Rewrapped != 1 || n.Failed+n.Changed != 0 {
					t.Fatalf("run %s = %+v, want one rewrapped value", n.Name, n)
				}
			}
			// What is stored now opens with KMS alone: no local custodian behind it.
			kmsOnly := newKMSCustodian(f, f.keyID, nil, 0)
			for _, tg := range targets {
				s := seeds[tg.name]
				it := openTarget(t, tg, s.id)
				if !strings.HasPrefix(it.sealed, kmsSealPrefix) || it.keyRef != s.keyRef {
					t.Errorf("%s after rewrap = %.12q under %q, want a kms1: value under %q", tg.name, it.sealed, it.keyRef, s.keyRef)
					continue
				}
				pt, err := kmsOnly.Unwrap(ctx, it.keyRef, it.sealed)
				if err != nil || !bytes.Equal(pt, s.plain) {
					t.Errorf("%s after rewrap opens to %d bytes, %v; want the original", tg.name, len(pt), err)
				}
			}
			// The plaintext row is not sealed by the command.
			if v, _, _ := st.GetSealedValue(ctx, "mcp_server", "M2"); v.Value != `{"X":"plain"}` {
				t.Errorf("plaintext mcp row became %.12q", v.Value)
			}
			// The rest of the ComfyUI record survives the rewrite.
			if it := openTarget(t, targets[len(targets)-1], engineComfySetting); !strings.Contains(it.raw, "http://10.0.0.5:8188") || !strings.Contains(it.raw, "op@acme.co.jp") {
				t.Errorf("comfy record lost fields: %s", it.raw)
			}
			// The Control Plane's own read path agrees.
			mgr := p3Manager(t, st)
			mgr.master32 = testMaster(t)
			mgr.custodian = kmsOnly
			if dek, err := mgr.resolveDEK(ctx, ws, "a-acme-co-jp"); err != nil || dek != hex.EncodeToString(seeds["wrapped_dek"].plain) {
				t.Errorf("resolveDEK after rewrap = %v; want the stored DEK", err)
			}
			if tok, aerr := newEngineHfTokensForTest(st, mgr).plaintext(ctx); aerr != nil || tok != "hf_tok" {
				t.Errorf("hf token after rewrap = %q, %v", tok, aerr)
			}

			// A second run finds nothing to do.
			again, err := rewrapKeys(ctx, c, targets, false, discardLog)
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range again {
				if n.Legacy != 0 || n.Rewrapped != 0 || n.KMS != 1 {
					t.Errorf("second run %s = %+v, want one kms value and nothing to do", n.Name, n)
				}
			}
		})
	}
}

func newEngineHfTokensForTest(st *store.SQL, mgr *manager) *engineHfTokens {
	return &engineHfTokens{settings: st, sealer: mgr}
}

// denyDecryptKMS is a key the role may generate data keys under but not decrypt with.
type denyDecryptKMS struct{ *fakeKMS }

func (denyDecryptKMS) Decrypt(context.Context, *kms.DecryptInput, ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	return nil, errors.New("AccessDeniedException")
}

// Every way the run can go wrong leaves every row openable as before.
func TestRewrapKeysFailureLeavesRowsReadable(t *testing.T) {
	ctx := context.Background()
	st := ssmAPIStores(t)["sqlite"]
	local := newLocalCustodian(testMaster(t))
	seeds, _ := seedLegacyRows(t, st, local)
	targets := rewrapTargets(st)
	stillLegacy := func(t *testing.T) {
		t.Helper()
		for _, tg := range targets {
			s := seeds[tg.name]
			it := openTarget(t, tg, s.id)
			if pt, err := local.Unwrap(ctx, it.keyRef, it.sealed); err != nil || !bytes.Equal(pt, s.plain) {
				t.Errorf("%s no longer opens with the master key: %v", tg.name, err)
			}
		}
	}

	t.Run("kms unreachable", func(t *testing.T) {
		f := newFakeKMS(t)
		f.err = errors.New("dial tcp: i/o timeout")
		_, err := rewrapKeys(ctx, newKMSCustodian(f, f.keyID, local, 0), targets, false, discardLog)
		if !errors.Is(err, errRewrapAborted) {
			t.Fatalf("err = %v, want the run stopped", err)
		}
		stillLegacy(t)
	})
	t.Run("decrypt denied", func(t *testing.T) {
		f := denyDecryptKMS{newFakeKMS(t)}
		_, err := rewrapKeys(ctx, newKMSCustodian(f, f.keyID, local, 0), targets, false, discardLog)
		if !errors.Is(err, errRewrapAborted) || !strings.Contains(err.Error(), "does not open") {
			t.Fatalf("err = %v, want the run stopped before writing a value it cannot open", err)
		}
		stillLegacy(t)
	})
	t.Run("interrupted", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		f := newFakeKMS(t)
		_, err := rewrapKeys(cctx, newKMSCustodian(f, f.keyID, local, 0), targets, false, discardLog)
		if err == nil {
			t.Fatal("a cancelled run reported success")
		}
		stillLegacy(t)
	})
}

func TestRewrapKeysUnreadableAndChangedRows(t *testing.T) {
	ctx := context.Background()
	st := ssmAPIStores(t)["sqlite"]
	local := newLocalCustodian(testMaster(t))
	seeds, _ := seedLegacyRows(t, st, local)
	// A value no master key opens: counted, left as it is, and the run goes on.
	if _, err := st.DB().ExecContext(ctx, `UPDATE tenant_idp SET secret_enc='bm90LXNlYWxlZA==' WHERE id='I1'`); err != nil {
		t.Fatal(err)
	}
	targets := rewrapTargets(st)
	// The CP rewrites the git OAuth secret between our read and our write.
	for i, tg := range targets {
		if tg.name != "tenant_git_oauth" {
			continue
		}
		inner := tg.swap
		targets[i].swap = func(ctx context.Context, id string, it rewrapItem, sealed string) (bool, error) {
			if _, err := st.DB().ExecContext(ctx, `UPDATE tenant_git_oauth SET secret_enc='newer' WHERE id=?`, id); err != nil {
				return false, err
			}
			return inner(ctx, id, it, sealed)
		}
	}
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	f := newFakeKMS(t)
	got, err := rewrapKeys(ctx, newKMSCustodian(f, f.keyID, local, 0), targets, false, logf)
	if err != nil {
		t.Fatal(err)
	}
	by := countsByName(got)
	if n := by["tenant_idp"]; n.Failed != 1 || n.Rewrapped != 0 {
		t.Errorf("tenant_idp = %+v, want one failed row", n)
	}
	if n := by["tenant_git_oauth"]; n.Changed != 1 || n.Rewrapped != 0 {
		t.Errorf("tenant_git_oauth = %+v, want one changed row", n)
	}
	if v, _, _ := st.GetSealedValue(ctx, "tenant_git_oauth", "G1"); v.Value != "newer" {
		t.Errorf("the newer value was overwritten: %.12q", v.Value)
	}
	if n := by["mcp_server"]; n.Rewrapped != 1 {
		t.Errorf("the run did not go on past the failure: mcp_server = %+v", n)
	}
	check, err := rewrapKeys(ctx, newKMSCustodian(f, f.keyID, local, 0), targets, true, discardLog)
	if err != nil {
		t.Fatal(err)
	}
	if left := rewrapLeft(check); left != 2 {
		t.Errorf("left = %d, want 2 (the unreadable row and the changed one)", left)
	}
	// The log names rows, never values.
	for _, l := range lines {
		for _, s := range seeds {
			if strings.Contains(l, string(s.plain)) {
				t.Errorf("log line carries a plaintext value: %s", l)
			}
		}
	}
}

// The exit code is the contract an operator acts on: 0 only when a read-only look finds no
// legacy value, for --dry-run as for a real run.
func TestRewrapRunExitCode(t *testing.T) {
	ctx := context.Background()
	st := ssmAPIStores(t)["sqlite"]
	local := newLocalCustodian(testMaster(t))
	seedLegacyRows(t, st, local)
	f := newFakeKMS(t)
	c := newKMSCustodian(f, f.keyID, local, 0)
	run := func(targets []rewrapTarget, dry bool) int {
		return rewrapRun(ctx, c, targets, dry, &bytes.Buffer{}, discardLog)
	}
	if code := run(rewrapTargets(st), true); code != 1 {
		t.Fatalf("dry run over legacy values = %d, want 1", code)
	}

	// A Control Plane edit that carries the stored secret forward (tenant_git_oauth_api.save
	// with the secret left blank) read the legacy value before our swap and writes it back
	// after it. The walk has moved on; only the final check can see the row went back.
	targets := rewrapTargets(st)
	for i, tg := range targets {
		if tg.name != "tenant_git_oauth" {
			continue
		}
		inner := tg.swap
		targets[i].swap = func(ctx context.Context, id string, it rewrapItem, sealed string) (bool, error) {
			ok, err := inner(ctx, id, it, sealed)
			if _, werr := st.DB().ExecContext(ctx, `UPDATE tenant_git_oauth SET secret_enc=? WHERE id=?`, it.sealed, id); werr != nil {
				t.Fatal(werr)
			}
			return ok, err
		}
	}
	if code := run(targets, false); code != 1 {
		t.Fatalf("run with a legacy value written back = %d, want 1", code)
	}
	if code := run(rewrapTargets(st), false); code != 0 {
		t.Fatalf("second run = %d, want 0", code)
	}
	if code := run(rewrapTargets(st), true); code != 0 {
		t.Fatalf("dry run with nothing left = %d, want 0", code)
	}
}

func TestRewrapKeysMainConfig(t *testing.T) {
	t.Setenv("AF_DATABASE_URL", "")
	t.Setenv("AF_DB_HOST", "")
	t.Setenv("AF_DB", filepath.Join(t.TempDir(), "missing.db"))
	for _, tc := range []struct {
		name, custodian, master, keyID string
		args                           []string
		want                           string // in the log; "" for the usage line on stderr
	}{
		{name: "unknown flag", custodian: "kms", master: "m", keyID: "k", args: []string{"--force"}},
		{name: "local custodian", custodian: "local", master: "m", keyID: "k", want: "AF_KEY_CUSTODIAN=local"},
		{name: "kms without master", custodian: "kms", keyID: "k", want: "needs AF_MASTER_KEY"},
		{name: "kms without key id", custodian: "kms", master: "m", want: "needs AF_KMS_KEY_ID"},
		{name: "missing sqlite file", custodian: "kms", master: "m", keyID: "arn:aws:kms:ap-northeast-1:111122223333:key/x", want: "sqlite database"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AF_KEY_CUSTODIAN", tc.custodian)
			t.Setenv("AF_MASTER_KEY", tc.master)
			t.Setenv("AF_KMS_KEY_ID", tc.keyID)
			var out bytes.Buffer
			var logged strings.Builder
			logf := func(f string, a ...any) { fmt.Fprintf(&logged, f+"\n", a...) }
			if code := rewrapKeysMain(tc.args, &out, logf); code != 2 {
				t.Fatalf("exit = %d, want 2", code)
			}
			if !strings.Contains(logged.String(), tc.want) {
				t.Fatalf("log = %q, want it to say %q", logged.String(), tc.want)
			}
		})
	}
	if rewrapGetenv("AF_KMS_DATA_KEY_CACHE_TTL") != "0" {
		t.Fatal("the command must run with the data-key cache off")
	}
}

// rewrapSealSites maps every call that seals a value (a .Wrap call, or the shared
// sealTenantSecret) to the rewrap target that re-seals what it stores, by "file:function".
// "" marks a call that stores nothing itself: the shared sealer, whose callers are listed, and
// the command's own re-seal.
var rewrapSealSites = map[string]string{
	"dek.go:resolveDEK":                         "wrapped_dek",
	"internal/mcpsrv/mcp_server.go:sealHeaders": "mcp_server",
	"session_share.go:sealProposal":             "session_share_proposal",
	"session_handoff.go:seal":                   "session_handoff_offer",
	"tenant_idp_secret.go:sealTenantSecret":     "",
	"tenant_idp_api.go:upsert":                  "tenant_idp",
	"tenant_git_oauth_api.go:save":              "tenant_git_oauth",
	"engine_hf_token.go:set":                    engineHfTokenSetting,
	"engine_civitai_token.go:set":               engineCivitaiTokenSetting,
	"engine_comfy_panel.go:seal":                engineComfySetting,
	"rewrap_keys.go:rewrapOne":                  "",
}

// TestRewrapTargetsCoverSealCallSites fails when code anywhere in the module starts sealing a
// value somewhere rewrapTargets does not walk: a new seal site has to be mapped to a target
// here, and every target has to be reachable from a site.
func TestRewrapTargetsCoverSealCallSites(t *testing.T) {
	found := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); path != "." && (strings.HasPrefix(n, ".") || n == "testdata" || n == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var name string
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					name = fun.Sel.Name
				case *ast.Ident:
					name = fun.Name
				}
				if (name == "Wrap" && len(call.Args) == 3) || name == "sealTenantSecret" {
					found[filepath.ToSlash(path)+":"+fn.Name.Name] = true
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) < 5 {
		t.Fatalf("found only %d seal sites; the scan is not seeing the code", len(found))
	}
	targets := map[string]bool{}
	for _, tg := range rewrapTargets(nil) {
		targets[tg.name] = true
	}
	reached := map[string]bool{}
	var unmapped []string
	for site := range found {
		target, ok := rewrapSealSites[site]
		if !ok {
			unmapped = append(unmapped, site)
			continue
		}
		if target != "" {
			if !targets[target] {
				t.Errorf("%s maps to %q, which rewrapTargets does not have", site, target)
			}
			reached[target] = true
		}
	}
	sort.Strings(unmapped)
	for _, site := range unmapped {
		t.Errorf("%s seals a value that rewrap-keys does not know about: add the column to store.sealedColumns (or a settings target to rewrapTargets) and map the site in rewrapSealSites", site)
	}
	for site := range rewrapSealSites {
		if !found[site] {
			t.Errorf("rewrapSealSites lists %s, which no longer seals anything", site)
		}
	}
	for name := range targets {
		if !reached[name] {
			t.Errorf("rewrap target %s is reached from no seal site in rewrapSealSites", name)
		}
	}
}
