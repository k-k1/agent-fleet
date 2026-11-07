package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// plainCols maps a rewrap target to table, value column, key reference column and id column,
// for the targets that can hold an unsealed value.
var plainCols = map[string][4]string{
	"mcp_server":                  {"mcp_server", "headers_enc", "key_ref", "id"},
	"mcp_server.oauth_secret_enc": {"mcp_server", "oauth_secret_enc", "oauth_key_ref", "id"},
	"tenant_idp":                  {"tenant_idp", "secret_enc", "key_ref", "id"},
	"tenant_git_oauth":            {"tenant_git_oauth", "secret_enc", "key_ref", "id"},
	"dev_idp_key":                 {"dev_idp_key", "private_enc", "key_ref", "kid"},
	"dev_idp_client":              {"dev_idp_client", "secret_enc", "key_ref", "workspace_id"},
	"session_share_proposal":      {"session_share_proposal", "ciphertext", "key_ref", "id"},
	"session_handoff_offer":       {"session_handoff_offer", "ciphertext", "key_ref", "id"},
	"inbox_item":                  {"inbox_item", "ciphertext", "key_ref", "id"},
	"inbox_attachment":            {"inbox_attachment", "ciphertext", "key_ref", "id"},
}

// plainTargets turns every seeded row of a plaintext-capable target into what a Control Plane
// without a master key would have written, and returns those targets.
func plainTargets(t *testing.T, st *store.SQL, seeds map[string]rewrapSeed) []rewrapTarget {
	t.Helper()
	ctx := context.Background()
	var out []rewrapTarget
	for _, tg := range rewrapTargets(st) {
		if tg.plain == nil {
			continue
		}
		out = append(out, tg)
		s := seeds[tg.name]
		val := string(s.plain)
		if tg.plain.encoding == store.PlainBase64 {
			val = base64.StdEncoding.EncodeToString(s.plain)
		}
		if c, ok := plainCols[tg.name]; ok {
			if _, err := st.DB().ExecContext(ctx, `UPDATE `+c[0]+` SET `+c[1]+`=?, `+c[2]+`='' WHERE `+c[3]+`=?`, val, s.id); err != nil {
				t.Fatalf("%s: %v", tg.name, err)
			}
			continue
		}
		switch tg.name {
		case engineHfTokenSetting, engineCivitaiTokenSetting:
			refKey := engineHfTokenRefSetting
			if tg.name == engineCivitaiTokenSetting {
				refKey = engineCivitaiTokenRefSetting
			}
			must(t, st.SetSetting(ctx, tg.name, val))
			must(t, st.SetSetting(ctx, refKey, ""))
		case engineComfySetting:
			rec, _ := json.Marshal(engineComfyRecord{URL: "http://10.0.0.5:8188", KeyEnc: val, By: "op@acme.co.jp"})
			must(t, st.SetSetting(ctx, engineComfySetting, string(rec)))
		default:
			t.Fatalf("no plaintext recipe for target %s", tg.name)
		}
	}
	return out
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Every column the command knows how to seal, sealed row by row under both custodians.
func TestSealPlaintextEveryTarget(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"local", "kms"} {
		for name, st := range ssmAPIStores(t) {
			t.Run(name+"/"+kind, func(t *testing.T) {
				local := newLocalCustodian(testMaster(t))
				seeds, _ := seedLegacyRows(t, st, local)
				targets := plainTargets(t, st, seeds)
				var c KeyCustodian = local
				var kmsOnly KeyCustodian
				if kind == "kms" {
					f := newFakeKMS(t)
					c = newKMSCustodian(f, f.keyID, local, 0)
					kmsOnly = newKMSCustodian(f, f.keyID, nil, 0)
				}
				n, err := countPlaintextSealed(ctx, st)
				if err != nil || n < len(targets) {
					t.Fatalf("startup count = %d, %v; want at least %d", n, err, len(targets))
				}
				before := map[string]rewrapItem{}
				for _, tg := range targets {
					before[tg.name] = openTarget(t, tg, seeds[tg.name].id)
				}
				// Dry run counts and changes nothing.
				dry, err := sealPlaintext(ctx, c, targets, true, discardLog)
				if err != nil {
					t.Fatal(err)
				}
				for _, d := range dry {
					if d.Plaintext < 1 || d.Sealed != 0 {
						t.Errorf("dry run %+v", d)
					}
				}
				for _, tg := range targets {
					if got := openTarget(t, tg, seeds[tg.name].id); got != before[tg.name] {
						t.Errorf("dry run changed %s", tg.name)
					}
				}

				var out bytes.Buffer
				if code := sealPlaintextRun(ctx, c, targets, false, &out, discardLog); code != 0 {
					t.Fatalf("exit %d\n%s", code, out.String())
				}
				open := c
				if kmsOnly != nil {
					open = kmsOnly
				}
				for _, tg := range targets {
					s := seeds[tg.name]
					it := openTarget(t, tg, s.id)
					if it.keyRef != s.keyRef || it.sealed == before[tg.name].sealed {
						t.Errorf("%s: key_ref %q (want %q) or value unchanged", tg.name, it.keyRef, s.keyRef)
						continue
					}
					pt, err := open.Unwrap(ctx, it.keyRef, it.sealed)
					if err != nil || !bytes.Equal(pt, s.plain) {
						t.Errorf("%s opens to %d bytes, %v; want the original", tg.name, len(pt), err)
					}
				}
				if n, err := countPlaintextSealed(ctx, st); err != nil || n != 0 {
					t.Errorf("count after sealing = %d, %v; want 0", n, err)
				}
				// The comfy record keeps its other fields.
				if it := openTarget(t, targets[len(targets)-1], engineComfySetting); !strings.Contains(it.raw, "http://10.0.0.5:8188") || !strings.Contains(it.raw, "op@acme.co.jp") {
					t.Errorf("comfy record lost fields: %s", it.raw)
				}
				// A second run has nothing to do.
				again, err := sealPlaintext(ctx, c, targets, false, discardLog)
				if err != nil {
					t.Fatal(err)
				}
				for _, a := range again {
					if a.Plaintext != 0 || a.Sealed != 0 {
						t.Errorf("second run %+v", a)
					}
				}
			})
		}
	}
}

// brokenReadback seals fine but opens to other bytes.
type brokenReadback struct{ KeyCustodian }

func (b brokenReadback) Unwrap(ctx context.Context, ref, ct string) ([]byte, error) {
	pt, err := b.KeyCustodian.Unwrap(ctx, ref, ct)
	if err != nil {
		return nil, err
	}
	return append(pt, 'x'), nil
}

type failingWrap struct{ KeyCustodian }

func (failingWrap) Wrap(context.Context, string, []byte) (string, error) {
	return "", errors.New("custodian unavailable")
}

func TestSealPlaintextStopsBeforeWritingABadRow(t *testing.T) {
	ctx := context.Background()
	st := ssmAPIStores(t)["sqlite"]
	local := newLocalCustodian(testMaster(t))
	seeds, _ := seedLegacyRows(t, st, local)
	targets := plainTargets(t, st, seeds)
	before := map[string]rewrapItem{}
	for _, tg := range targets {
		before[tg.name] = openTarget(t, tg, seeds[tg.name].id)
	}
	for _, c := range []KeyCustodian{brokenReadback{local}, failingWrap{local}} {
		var out bytes.Buffer
		if code := sealPlaintextRun(ctx, c, targets, false, &out, discardLog); code != 1 {
			t.Errorf("%T: exit %d, want 1", c, code)
		}
		for _, tg := range targets {
			if got := openTarget(t, tg, seeds[tg.name].id); got != before[tg.name] {
				t.Errorf("%T: %s was written", c, tg.name)
			}
		}
	}
}

// A row edited between the read and the write is left to the newer value.
func TestSealPlaintextLeavesAChangedRow(t *testing.T) {
	ctx := context.Background()
	st := ssmAPIStores(t)["sqlite"]
	local := newLocalCustodian(testMaster(t))
	seeds, _ := seedLegacyRows(t, st, local)
	targets := plainTargets(t, st, seeds)
	for i := range targets {
		if targets[i].name != "tenant_idp" {
			continue
		}
		real := targets[i].plain.seal
		targets[i].plain.seal = func(ctx context.Context, id string, it rewrapItem, sealed, ref string) (bool, error) {
			_, err := st.DB().ExecContext(ctx, `UPDATE tenant_idp SET secret_enc='newer' WHERE id=?`, id)
			must(t, err)
			return real(ctx, id, it, sealed, ref)
		}
	}
	got, err := sealPlaintext(ctx, local, targets, false, discardLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range got {
		if n.Name == "tenant_idp" && (n.Changed != 1 || n.Sealed != 0) {
			t.Errorf("tenant_idp = %+v, want changed", n)
		}
	}
	if v, _, _ := st.GetSealedValue(ctx, "tenant_idp", "I1"); v.Value != "newer" || v.KeyRef != "" {
		t.Errorf("row = %+v, want the newer plaintext untouched", v)
	}
}

// The warning follows the master key: none without one, and it clears after sealing.
func TestPlaintextSealedWarning(t *testing.T) {
	ctx := context.Background()
	st := ssmAPIStores(t)["sqlite"]
	local := newLocalCustodian(testMaster(t))
	seeds, _ := seedLegacyRows(t, st, local)
	targets := plainTargets(t, st, seeds)

	m := &manager{authMode: "dev", nativeRuntime: true}
	if w := m.deploymentWarnings(); len(w) != 0 {
		t.Fatalf("unarmed manager warns: %v", w)
	}
	m.plaintextSealed.init(st)
	if w := strings.Join(m.deploymentWarnings(), ","); !strings.Contains(w, deploymentWarnPlaintextSealed) {
		t.Fatalf("warnings = %q, want %s", w, deploymentWarnPlaintextSealed)
	}
	if _, err := sealPlaintext(ctx, local, targets, false, discardLog); err != nil {
		t.Fatal(err)
	}
	m.plaintextSealed.last = m.plaintextSealed.last.Add(-2 * plaintextSealedTTL)
	if w := m.deploymentWarnings(); len(w) != 0 {
		t.Errorf("warnings after sealing = %v, want none", w)
	}
}

func TestSealPlaintextMainNeedsAKey(t *testing.T) {
	t.Setenv("AF_MASTER_KEY", "")
	var out bytes.Buffer
	if code := rewrapKeysMain([]string{"--seal-plaintext"}, &out, discardLog); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
}
