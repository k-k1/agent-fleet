package main

// `af-cp rewrap-keys` re-seals every value the local custodian sealed before the deployment
// switched to AF_KEY_CUSTODIAN=kms, so disabling the KMS key shreds those rows too (ADR 0005
// addendum 2026-10-10, guide/operate/04-secure.md). It never decides what to do from a KMS
// failure: the format prefix says which rows are legacy, and any KMS error stops the run with
// the row it was on untouched.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/k-k1/agent-fleet/control-plane/internal/datalayout"
	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// rewrapItem is one stored sealed value. raw is the whole stored value when the sealed text is
// a field inside it (the ComfyUI record), so the write can be conditioned on all of it.
type rewrapItem struct {
	sealed, keyRef, raw string
}

// rewrapTarget is one place the custodian seals into. swap writes sealed in place of item only
// while the stored value is still item, and reports false otherwise.
type rewrapTarget struct {
	name string
	ids  func(ctx context.Context) ([]string, error)
	load func(ctx context.Context, id string) (rewrapItem, bool, error)
	swap func(ctx context.Context, id string, item rewrapItem, sealed string) (bool, error)
}

// rewrapTargets is every place the custodian seals into: the tables of store.SealedTables and
// the settings rows whose key reference lives beside them. TestRewrapTargetsCoverSealCallSites
// ties this list to every Wrap call in the module, so a new sealed value cannot be added
// without the command learning about it.
func rewrapTargets(st *store.SQL) []rewrapTarget {
	var out []rewrapTarget
	for _, table := range store.SealedTables() {
		out = append(out, rewrapTarget{
			name: table,
			ids:  func(ctx context.Context) ([]string, error) { return st.ListSealedIDs(ctx, table) },
			load: func(ctx context.Context, id string) (rewrapItem, bool, error) {
				v, ok, err := st.GetSealedValue(ctx, table, id)
				return rewrapItem{sealed: v.Value, keyRef: v.KeyRef}, ok, err
			},
			swap: func(ctx context.Context, id string, it rewrapItem, sealed string) (bool, error) {
				return st.SwapSealedValue(ctx, table, id, store.SealedValue{Value: it.sealed, KeyRef: it.keyRef}, sealed)
			},
		})
	}
	out = append(out,
		rewrapSettingPair(st, engineHfTokenSetting, engineHfTokenRefSetting),
		rewrapSettingPair(st, engineCivitaiTokenSetting, engineCivitaiTokenRefSetting),
		rewrapComfyRecord(st))
	return out
}

func settingIDs(st *store.SQL, key string) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		v, err := st.GetSetting(ctx, key)
		if err != nil || v == "" {
			return nil, err
		}
		return []string{key}, nil
	}
}

// rewrapSettingPair is a sealed setting whose key reference is a second setting (the engine
// tokens).
func rewrapSettingPair(st *store.SQL, valueKey, refKey string) rewrapTarget {
	return rewrapTarget{
		name: valueKey,
		ids:  settingIDs(st, valueKey),
		load: func(ctx context.Context, _ string) (rewrapItem, bool, error) {
			v, err := st.GetSetting(ctx, valueKey)
			if err != nil || v == "" {
				return rewrapItem{}, false, err
			}
			ref, err := st.GetSetting(ctx, refKey)
			if err != nil {
				return rewrapItem{}, false, err
			}
			return rewrapItem{sealed: v, keyRef: ref}, true, nil
		},
		swap: func(ctx context.Context, _ string, it rewrapItem, sealed string) (bool, error) {
			return st.SwapSetting(ctx, valueKey, it.sealed, sealed)
		},
	}
}

// rewrapComfyRecord is the ComfyUI panel record: one JSON setting with the sealed key inside.
func rewrapComfyRecord(st *store.SQL) rewrapTarget {
	return rewrapTarget{
		name: engineComfySetting,
		ids:  settingIDs(st, engineComfySetting),
		load: func(ctx context.Context, _ string) (rewrapItem, bool, error) {
			raw, err := st.GetSetting(ctx, engineComfySetting)
			if err != nil || raw == "" {
				return rewrapItem{}, false, err
			}
			var rec engineComfyRecord
			if err := json.Unmarshal([]byte(raw), &rec); err != nil {
				return rewrapItem{}, false, fmt.Errorf("parse the stored record: %w", err)
			}
			return rewrapItem{sealed: rec.KeyEnc, keyRef: rec.KeyRef, raw: raw}, true, nil
		},
		swap: func(ctx context.Context, _ string, it rewrapItem, sealed string) (bool, error) {
			var rec engineComfyRecord
			if err := json.Unmarshal([]byte(it.raw), &rec); err != nil {
				return false, err
			}
			rec.KeyEnc = sealed
			b, err := json.Marshal(rec)
			if err != nil {
				return false, err
			}
			return st.SwapSetting(ctx, engineComfySetting, it.raw, string(b))
		},
	}
}

// rewrapCounts is one target's tally. Legacy counts what was found in the local format;
// Rewrapped, Changed and Failed split it after a real run.
type rewrapCounts struct {
	Name                   string
	KMS, Legacy, Plaintext int
	Rewrapped, Changed     int
	Failed                 int

	unreadable int // the part of Failed that never reached the legacy count
}

// errRewrapAborted marks a stop that left the remaining rows untouched.
var errRewrapAborted = errors.New("rewrap stopped")

// rewrapKeys walks targets and, unless dryRun, re-seals each legacy value under c. A row that
// cannot be opened is counted as failed and left as it is; a KMS error, or a re-sealed value
// that does not open back to the same bytes, stops the whole run before that row is written,
// because the same error would follow on every row and a value written under a key the run
// cannot decrypt with is a value shredded by accident. logf never receives a value.
func rewrapKeys(ctx context.Context, c *kmsCustodian, targets []rewrapTarget, dryRun bool, logf func(string, ...any)) ([]rewrapCounts, error) {
	var out []rewrapCounts
	for _, t := range targets {
		n := rewrapCounts{Name: t.name}
		err := rewrapTargetRows(ctx, c, t, dryRun, &n, logf)
		out = append(out, n)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func rewrapTargetRows(ctx context.Context, c *kmsCustodian, t rewrapTarget, dryRun bool, n *rewrapCounts, logf func(string, ...any)) error {
	ids, err := t.ids(ctx)
	if err != nil {
		return fmt.Errorf("%w: %s: list rows: %v", errRewrapAborted, t.name, err)
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", errRewrapAborted, err)
		}
		it, ok, err := t.load(ctx, id)
		if err != nil {
			n.Failed++
			n.unreadable++
			logf("rewrap-keys: %s %s: read failed, left as it is: %v", t.name, id, err)
			continue
		}
		switch {
		case !ok || it.sealed == "":
			continue
		case it.keyRef == "":
			// Stored unsealed by a CP that had no master key; sealing it is not a rewrap.
			n.Plaintext++
			continue
		case strings.HasPrefix(it.sealed, kmsSealPrefix):
			n.KMS++
			continue
		}
		n.Legacy++
		if dryRun {
			continue
		}
		if err := rewrapOne(ctx, c, t, id, it, n, logf); err != nil {
			return err
		}
	}
	return nil
}

func rewrapOne(ctx context.Context, c *kmsCustodian, t rewrapTarget, id string, it rewrapItem, n *rewrapCounts, logf func(string, ...any)) error {
	plain, err := c.Unwrap(ctx, it.keyRef, it.sealed)
	if err != nil {
		n.Failed++
		logf("rewrap-keys: %s %s: the legacy value does not open with AF_MASTER_KEY, left as it is: %v", t.name, id, err)
		return nil
	}
	defer clear(plain)
	sealed, err := c.Wrap(ctx, it.keyRef, plain)
	if err != nil {
		return fmt.Errorf("%w at %s %s (not written): %v", errRewrapAborted, t.name, id, err)
	}
	back, err := c.Unwrap(ctx, it.keyRef, sealed)
	if err != nil {
		return fmt.Errorf("%w at %s %s (not written): the new value does not open: %v", errRewrapAborted, t.name, id, err)
	}
	same := bytes.Equal(back, plain)
	clear(back)
	if !same {
		return fmt.Errorf("%w at %s %s (not written): the new value opens to different bytes", errRewrapAborted, t.name, id)
	}
	swapped, err := t.swap(ctx, id, it, sealed)
	switch {
	case err != nil:
		n.Failed++
		logf("rewrap-keys: %s %s: write failed, the old value stays: %v", t.name, id, err)
	case !swapped:
		n.Changed++
		logf("rewrap-keys: %s %s: changed while being rewrapped, left to the newer value", t.name, id)
	default:
		n.Rewrapped++
	}
	return nil
}

// masterDigest is the key every master-derived key starts from: SHA-256 of AF_MASTER_KEY.
func masterDigest(masterKey string) []byte {
	sum := sha256.Sum256([]byte(masterKey))
	return sum[:]
}

// rewrapGetenv is the environment the command builds its custodian from: the CP's own, with
// the data-key cache off, so the check that a re-sealed value opens really asks KMS to
// Decrypt. A cache hit would let a role allowed GenerateDataKey but not Decrypt write rows
// nobody can read back.
func rewrapGetenv(k string) string {
	if k == "AF_KMS_DATA_KEY_CACHE_TTL" {
		return "0"
	}
	return os.Getenv(k)
}

// openRewrapStore opens the metadata store the CP would, without creating or migrating it: a
// mistyped SQLite path must not become a fresh empty database that reports nothing to do.
func openRewrapStore() (*store.SQL, string, error) {
	if u := store.PGURLFromEnv(); u != "" {
		st, err := store.OpenPostgres(u)
		return st, "postgres", err
	}
	path := envx.Or("AF_DB", filepath.Join(envx.Or("WS_DATA", "/tmp/af-data"), datalayout.DBFile))
	if _, err := os.Stat(path); err != nil {
		return nil, "", fmt.Errorf("sqlite database: %w", err)
	}
	st, err := store.OpenSQLite(path)
	return st, "sqlite " + path, err
}

// runRewrapKeys is `af-cp rewrap-keys [--dry-run]`. Exit 0, with or without --dry-run: the
// last read-only look found no legacy and no unreadable value. Exit 1: something was left or
// the run stopped; running it again picks up where it left off. Exit 2: usage or configuration.
func runRewrapKeys(args []string) {
	os.Exit(rewrapKeysMain(args, os.Stdout, log.Printf))
}

func rewrapKeysMain(args []string, stdout io.Writer, logf func(string, ...any)) int {
	fs := flag.NewFlagSet("rewrap-keys", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "count the values in each format and change nothing")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(fs.Output(), "usage: af-cp rewrap-keys [--dry-run]")
		return 2
	}
	if kind := envx.Or("AF_KEY_CUSTODIAN", "local"); kind != "kms" {
		logf("rewrap-keys: AF_KEY_CUSTODIAN=%s; the command re-seals under the KMS custodian, so run it with the CP's kms configuration", kind)
		return 2
	}
	var master32 []byte
	if mk := os.Getenv("AF_MASTER_KEY"); mk != "" {
		master32 = masterDigest(mk)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	kc, err := newKeyCustodian(ctx, "kms", master32, rewrapGetenv)
	if err != nil {
		logf("rewrap-keys: key custodian: %v", err)
		return 2
	}
	c, ok := kc.(*kmsCustodian)
	if !ok {
		logf("rewrap-keys: the configured custodian is not the KMS one")
		return 2
	}
	st, where, err := openRewrapStore()
	if err != nil {
		logf("rewrap-keys: open the metadata store: %v", err)
		return 2
	}
	defer st.Close()
	logf("rewrap-keys: %s, dry-run=%v", where, *dryRun)
	return rewrapRun(ctx, c, rewrapTargets(st), *dryRun, stdout, logf)
}

// rewrapRun runs the walk and decides the exit code. Exit 0 always rests on a read-only pass
// made after any rewriting: a Control Plane edit that carries a stored value forward (an IdP
// or Git OAuth app saved without retyping its secret, the ComfyUI panel saved without its
// key) can write back a legacy value read before our swap, and only a fresh look sees it.
// An edit still in flight after that pass can do the same, which is why the guide says to run
// it when no administrator is editing and to confirm with --dry-run.
func rewrapRun(ctx context.Context, c *kmsCustodian, targets []rewrapTarget, dryRun bool, stdout io.Writer, logf func(string, ...any)) int {
	counts, err := rewrapKeys(ctx, c, targets, dryRun, logf)
	printRewrapCounts(stdout, counts)
	if err != nil {
		logf("rewrap-keys: %v; rows not yet reached are unchanged and still open as before, run again once the cause is fixed", err)
		return 1
	}
	if !dryRun {
		if counts, err = rewrapKeys(ctx, c, targets, true, logf); err != nil {
			logf("rewrap-keys: final check: %v", err)
			return 1
		}
		fmt.Fprintln(stdout, "final check:")
		printRewrapCounts(stdout, counts)
	}
	if left := rewrapLeft(counts); left > 0 {
		logf("rewrap-keys: %d value(s) are still in the old format or unreadable, see the lines above; run again", left)
		return 1
	}
	return 0
}

// rewrapLeft counts, in the tally of a read-only pass, the values not yet on KMS: legacy ones
// and rows that could not be read.
func rewrapLeft(counts []rewrapCounts) int {
	left := 0
	for _, n := range counts {
		left += n.Legacy + n.unreadable
	}
	return left
}

// printRewrapCounts writes the per-target table.
func printRewrapCounts(w io.Writer, counts []rewrapCounts) {
	fmt.Fprintf(w, "%-24s %8s %8s %10s %10s %8s %7s\n", "target", "kms", "legacy", "plaintext", "rewrapped", "changed", "failed")
	for _, n := range counts {
		fmt.Fprintf(w, "%-24s %8d %8d %10d %10d %8d %7d\n", n.Name, n.KMS, n.Legacy, n.Plaintext, n.Rewrapped, n.Changed, n.Failed)
	}
}
