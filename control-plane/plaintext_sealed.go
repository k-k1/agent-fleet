package main

// Values the Control Plane stored unsealed (empty key_ref) while no master key was set stay
// that way after the key is configured: every reader takes an empty key_ref to mean "not
// sealed" and returns the value as it is. This file counts them, so the Console and the
// startup log can say so once a key exists (the plaintext_secrets warning ends exactly then),
// and seals them with `af-cp rewrap-keys --seal-plaintext`.

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// deploymentWarnPlaintextSealed is the code for "a master key is configured but some sealed
// values are still stored unsealed"; the Console renders it as
// admin.deploy_warn_plaintext_sealed.
const deploymentWarnPlaintextSealed = "plaintext_sealed"

const plaintextSealedLog = "WARNING: AF_MASTER_KEY is set but %d stored value(s) (client secrets, MCP headers, " +
	"tokens) were written without a key and are still unencrypted. Run `af-cp rewrap-keys --seal-plaintext` and " +
	"rotate those secrets at their issuer (guide/operate/04-secure.md)"

// plaintextSealedTTL bounds how stale the Console's view is after the command ran.
const plaintextSealedTTL = 5 * time.Minute

// plaintextSealedState caches the count for deploymentWarnings. The zero value counts nothing
// until init gives it a store, which is how a keyless deployment (plaintext_secrets already
// says everything) and the tests without a database stay quiet.
type plaintextSealedState struct {
	mu   sync.Mutex
	st   *store.SQL
	n    int
	last time.Time
}

// init arms the state; call it only when a master key and a custodian are configured.
func (s *plaintextSealedState) init(st *store.SQL) {
	s.mu.Lock()
	s.st = st
	s.mu.Unlock()
}

// count returns the cached count, refreshing it when older than plaintextSealedTTL. A failed
// count keeps the previous answer: a warning must not flap on a database hiccup.
func (s *plaintextSealedState) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st == nil {
		return 0
	}
	if s.last.IsZero() || time.Since(s.last) >= plaintextSealedTTL {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if n, err := countPlaintextSealed(ctx, s.st); err != nil {
			log.Printf("plaintext sealed values: count failed: %v", err)
		} else {
			s.n = n
		}
		s.last = time.Now()
	}
	return s.n
}

// countPlaintextSealed counts every non-empty sealed value with an empty key reference, in
// the tables of store.SealedTables and the settings rows rewrapTargets knows.
func countPlaintextSealed(ctx context.Context, st *store.SQL) (int, error) {
	total := 0
	tables := store.SealedTables()
	for _, t := range tables {
		n, err := st.CountPlaintextSealed(ctx, t)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", t, err)
		}
		total += n
	}
	for _, t := range rewrapTargets(st)[len(tables):] {
		ids, err := t.ids(ctx)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", t.name, err)
		}
		for _, id := range ids {
			it, ok, err := t.load(ctx, id)
			if err != nil {
				return 0, fmt.Errorf("%s: %w", t.name, err)
			}
			if ok && it.sealed != "" && it.keyRef == "" {
				total++
			}
		}
	}
	return total, nil
}

// sealCounts is one target's tally for --seal-plaintext. Plaintext is what the pass found
// unsealed; Sealed, Changed and Failed split it after a real run.
type sealCounts struct {
	Name                               string
	Plaintext, Sealed, Changed, Failed int
}

// sealPlaintext walks targets and, unless dryRun, seals every unsealed value under c with
// the key reference its writer would have used, one row at a time: the sealed value is
// opened again and must equal the original bytes before the row is written, and the write
// is conditioned on the row still holding what was read. A row that cannot be decoded or has
// no key reference is counted as failed and left as it is; a custodian error, or a read-back
// that differs, stops the run before that row is written. logf never receives a value.
func sealPlaintext(ctx context.Context, c KeyCustodian, targets []rewrapTarget, dryRun bool, logf func(string, ...any)) ([]sealCounts, error) {
	var out []sealCounts
	for _, t := range targets {
		if t.plain == nil {
			continue
		}
		n := sealCounts{Name: t.name}
		err := sealPlaintextRows(ctx, c, t, dryRun, &n, logf)
		out = append(out, n)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func sealPlaintextRows(ctx context.Context, c KeyCustodian, t rewrapTarget, dryRun bool, n *sealCounts, logf func(string, ...any)) error {
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
			logf("seal-plaintext: %s %s: read failed, left as it is: %v", t.name, id, err)
			continue
		}
		if !ok || it.sealed == "" || it.keyRef != "" {
			continue
		}
		n.Plaintext++
		if dryRun {
			continue
		}
		if err := sealPlaintextOne(ctx, c, t, id, it, n, logf); err != nil {
			return err
		}
	}
	return nil
}

func sealPlaintextOne(ctx context.Context, c KeyCustodian, t rewrapTarget, id string, it rewrapItem, n *sealCounts, logf func(string, ...any)) error {
	plain := []byte(it.sealed)
	if t.plain.encoding == store.PlainBase64 {
		b, err := base64.StdEncoding.DecodeString(it.sealed)
		if err != nil {
			n.Failed++
			logf("seal-plaintext: %s %s: the stored value is not base64, left as it is", t.name, id)
			return nil
		}
		plain = b
	}
	defer clear(plain)
	ref, err := t.plain.ref(ctx, id)
	if err != nil {
		n.Failed++
		logf("seal-plaintext: %s %s: no key reference, left as it is: %v", t.name, id, err)
		return nil
	}
	sealed, err := c.Wrap(ctx, ref, plain)
	if err != nil {
		return fmt.Errorf("%w at %s %s (not written): %v", errRewrapAborted, t.name, id, err)
	}
	back, err := c.Unwrap(ctx, ref, sealed)
	if err != nil {
		return fmt.Errorf("%w at %s %s (not written): the new value does not open: %v", errRewrapAborted, t.name, id, err)
	}
	same := bytes.Equal(back, plain)
	clear(back)
	if !same {
		return fmt.Errorf("%w at %s %s (not written): the new value opens to different bytes", errRewrapAborted, t.name, id)
	}
	swapped, err := t.plain.seal(ctx, id, it, sealed, ref)
	switch {
	case err != nil:
		n.Failed++
		logf("seal-plaintext: %s %s: write failed, the old value stays: %v", t.name, id, err)
	case !swapped:
		n.Changed++
		logf("seal-plaintext: %s %s: changed while being sealed, left to the newer value", t.name, id)
	default:
		n.Sealed++
	}
	return nil
}

// sealPlaintextMain is `af-cp rewrap-keys --seal-plaintext [--dry-run]`. It seals under the
// custodian the Control Plane is configured with (AF_KEY_CUSTODIAN local or kms), which needs
// the same AF_MASTER_KEY. Exit codes as rewrapKeysMain: 0 only when a read-only pass made
// after any writing finds nothing unsealed and nothing unreadable.
func sealPlaintextMain(dryRun bool, stdout io.Writer, logf func(string, ...any)) int {
	kind := envx.Or("AF_KEY_CUSTODIAN", "local")
	mk := os.Getenv("AF_MASTER_KEY")
	if mk == "" {
		logf("seal-plaintext: AF_MASTER_KEY is not set; there is no key to seal with")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c, err := newKeyCustodian(ctx, kind, masterDigest(mk), rewrapGetenv)
	if err != nil || c == nil {
		logf("seal-plaintext: key custodian %s: %v", kind, err)
		return 2
	}
	st, where, err := openRewrapStore()
	if err != nil {
		logf("seal-plaintext: open the metadata store: %v", err)
		return 2
	}
	defer st.Close()
	logf("seal-plaintext: %s, custodian=%s, dry-run=%v", where, kind, dryRun)
	return sealPlaintextRun(ctx, c, rewrapTargets(st), dryRun, stdout, logf)
}

func sealPlaintextRun(ctx context.Context, c KeyCustodian, targets []rewrapTarget, dryRun bool, stdout io.Writer, logf func(string, ...any)) int {
	counts, err := sealPlaintext(ctx, c, targets, dryRun, logf)
	printSealCounts(stdout, counts)
	if err != nil {
		logf("seal-plaintext: %v; rows not yet reached are unchanged, run again once the cause is fixed", err)
		return 1
	}
	if !dryRun {
		if counts, err = sealPlaintext(ctx, c, targets, true, logf); err != nil {
			logf("seal-plaintext: final check: %v", err)
			return 1
		}
		fmt.Fprintln(stdout, "final check:")
		printSealCounts(stdout, counts)
	}
	left := 0
	for _, n := range counts {
		left += n.Plaintext + n.Failed
	}
	if left > 0 {
		logf("seal-plaintext: %d value(s) are still unsealed or unreadable, see the lines above", left)
		return 1
	}
	return 0
}

func printSealCounts(w io.Writer, counts []sealCounts) {
	fmt.Fprintf(w, "%-30s %10s %8s %8s %7s\n", "target", "plaintext", "sealed", "changed", "failed")
	for _, n := range counts {
		fmt.Fprintf(w, "%-30s %10d %8d %8d %7d\n", n.Name, n.Plaintext, n.Sealed, n.Changed, n.Failed)
	}
}
