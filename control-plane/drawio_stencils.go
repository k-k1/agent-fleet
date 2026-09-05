// drawio_stencils.go — the CP proxies the `.drawio` viewer's stencils and caches them on
// disk (docs/log/65 §65.5.3 / ADR 0046 decision 5).
//
// Why they are not bundled: the whole stencil set is 203 files / 40.8 MB (`aws4.xml` alone
// is 6.2 MB). One diagram uses one or two of those sets, and fetching was on demand
// already (`mxStencilRegistry` fetches each set appearing in the diagram exactly once), so
// the only problem is distribution size. Hence no bytes are shipped — only the 20 KB
// manifest.
//
// The manifest (assets/drawio-stencils.json) serves two purposes:
//  1. SSRF barrier. Set names come from untrusted `.drawio` content
//     (`shape=mxgraph.<set>.*`). Fetching a name that is not in the manifest would turn
//     "get someone to open a diagram" into "make the CP hit an arbitrary URL". So the
//     allowlist is nothing but "is it in the manifest", and the CP builds the upstream URL
//     from the manifest's base plus the set name — the request never carries a URL.
//  2. Integrity. Fetched bytes are matched against sha256 before they are stored.
//
// This route belongs inside authGate; do not exempt it. The fetch comes from the Console's
// parent window rather than the sandboxed iframe, so the session cookie is attached.
// Letting the frame fetch directly was rejected on measurement: a request from an
// origin-less frame counts as cross-site, carries no SameSite=Lax cookie and authGate
// rejects it with 401 (the same hole as docs/log/65 §65.11-7). Details in docs/log/65
// §65.5.4.
package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed assets/drawio-stencils.json
var drawioStencilManifestJSON []byte

type drawioStencilEntry struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type drawioStencilManifest struct {
	Version string                        `json:"version"`
	Base    string                        `json:"base"`
	Sets    map[string]drawioStencilEntry `json:"sets"`
}

var (
	drawioManifestOnce sync.Once
	drawioManifest     drawioStencilManifest
	drawioManifestErr  error
)

func loadDrawioManifest() (drawioStencilManifest, error) {
	drawioManifestOnce.Do(func() {
		if err := json.Unmarshal(drawioStencilManifestJSON, &drawioManifest); err != nil {
			drawioManifestErr = err
			return
		}
		if len(drawioManifest.Sets) == 0 || drawioManifest.Base == "" {
			drawioManifestErr = errors.New("drawio stencil manifest is empty")
		}
	})
	return drawioManifest, drawioManifestErr
}

// drawioStencilHTTP fetches from upstream (raw.githubusercontent). A single file runs to
// 6.2 MB, so the timeout has enough slack not to cut off a thin link.
var drawioStencilHTTP = &http.Client{Timeout: 60 * time.Second}

type drawioStencils struct {
	cacheDir string
	// Per-name lock, so concurrent requests for one set hit upstream only once.
	mu      sync.Mutex
	loading map[string]*sync.Mutex
}

func newDrawioStencils(cfg config) *drawioStencils {
	root := "/tmp/af-data"
	if cfg.mgr != nil && cfg.mgr.dataRoot != "" {
		root = cfg.mgr.dataRoot
	}
	return &drawioStencils{
		cacheDir: filepath.Join(root, "drawio-stencils"),
		loading:  map[string]*sync.Mutex{},
	}
}

func registerDrawioStencilRoutes(mux *http.ServeMux, cfg config) {
	d := newDrawioStencils(cfg)
	mux.HandleFunc("GET /api/drawio/stencils/{name...}", d.serve)
	// Version and counts only, for the harness and for operators; no stencil bytes.
	mux.HandleFunc("GET /api/drawio/stencils", d.index)
}

func (d *drawioStencils) index(w http.ResponseWriter, r *http.Request) {
	m, err := loadDrawioManifest()
	if err != nil {
		http.Error(w, "manifest unavailable", http.StatusInternalServerError)
		return
	}
	cached := 0
	for _, e := range m.Sets {
		if st, err := os.Stat(d.pathFor(e)); err == nil && st.Size() == e.Size {
			cached++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version": m.Version,
		"sets":    len(m.Sets),
		"cached":  cached,
	})
}

// pathFor is where a set lives in the cache. The set name is never used as the path:
// matched against the manifest or not, expanding a name that contains `/`
// (`rack/f5.xml`) into directories lets a manifest update change what a path means.
// Naming the file by sha256 makes different content a different file, so stale bytes
// cannot survive either.
//
// The argument is the manifest entry, not the name. Looking the embedded manifest up by
// name diverges from the manifest the caller passed in (tests, preseeding) and lets sets
// with different names land in the same file — written that way once, and a test caught it.
func (d *drawioStencils) pathFor(entry drawioStencilEntry) string {
	return filepath.Join(d.cacheDir, entry.SHA256+".xml")
}

func (d *drawioStencils) lockFor(name string) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	mu, ok := d.loading[name]
	if !ok {
		mu = &sync.Mutex{}
		d.loading[name] = mu
	}
	return mu
}

func (d *drawioStencils) serve(w http.ResponseWriter, r *http.Request) {
	m, err := loadDrawioManifest()
	if err != nil {
		http.Error(w, "manifest unavailable", http.StatusInternalServerError)
		return
	}
	name := r.PathValue("name")
	// A name absent from the manifest is never fetched. This is the only allowlist on this
	// route, and loosening it turns the route into arbitrary-URL fetching (SSRF). `..` and
	// absolute paths die here too, simply by not matching a manifest key.
	entry, ok := m.Sets[name]
	if !ok {
		http.Error(w, "unknown stencil set", http.StatusNotFound)
		return
	}

	body, err := d.fetch(r.Context(), m, name, entry)
	if err != nil {
		// Unreachable in a closed network. That is expected degradation rather than a
		// fault, so the Console quietly falls back to outline-and-color shapes
		// (docs/log/65 §65.5.3).
		log.Printf("drawio stencil %s unavailable: %v", name, err)
		http.Error(w, "stencil unavailable", http.StatusBadGateway)
		return
	}
	// The content is pinned by version and verified by sha256, and the name is a manifest
	// key, so a new version means a new manifest: safe to cache for a long time.
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}

func (d *drawioStencils) fetch(ctx context.Context, m drawioStencilManifest, name string, entry drawioStencilEntry) ([]byte, error) {
	path := d.pathFor(entry)
	if b, err := os.ReadFile(path); err == nil && verifyDrawioStencil(b, entry) == nil {
		return b, nil
	}

	mu := d.lockFor(name)
	mu.Lock()
	defer mu.Unlock()
	// Another request may have stored it while we waited for the lock.
	if b, err := os.ReadFile(path); err == nil && verifyDrawioStencil(b, entry) == nil {
		return b, nil
	}

	b, err := drawioFetchUpstream(ctx, m.Base+name, entry)
	if err != nil {
		return nil, err
	}

	// Failing to cache does not stop us answering this request.
	if err := d.store(path, b); err != nil {
		log.Printf("drawio stencil cache: %v", err)
	}
	return b, nil
}

// store puts one entry into the cache. Always through a temp name and a rename: the file
// name is the sha256 of its content, so the moment a half-written file is visible under
// the real name we serve broken bytes wearing a "verified" face. Preseeding
// (drawio_preseed.go) touches the same directory as a running CP, so it has to go through
// here too.
func (d *drawioStencils) store(path string, b []byte) error {
	if err := os.MkdirAll(d.cacheDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(d.cacheDir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	// Same directory, so the rename is atomic.
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// drawioFetchUpstream fetches one set from upstream and returns it once it matches the
// manifest.
//
// It retries on purpose: raw.githubusercontent really does return connection resets
// (measured — 8-way parallel fetches while baking the manifest, plus once on a real first
// fetch). Answering 502 on a single blip is unrecoverable from the Console's side, which
// marks the set as already requested and never asks for it again, so the diagram's icons
// stay missing for the whole life of that pane — the same failure that got the viewer's
// own lazy fetching rejected (§65.5.4-3).
func drawioFetchUpstream(ctx context.Context, url string, entry drawioStencilEntry) ([]byte, error) {
	var last error
	for attempt := 1; attempt <= drawioFetchTries; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt-1) * 400 * time.Millisecond):
			}
		}
		b, err := drawioFetchOnce(ctx, url, entry)
		if err == nil {
			return b, nil
		}
		last = err
		// A manifest mismatch and a 404 give the same answer every time; give up now.
		if errors.Is(err, errDrawioPermanent) {
			break
		}
	}
	return nil, last
}

const drawioFetchTries = 3

// Marks a failure retrying cannot fix: content mismatch, or a URL that does not exist.
var errDrawioPermanent = errors.New("permanent")

func drawioFetchOnce(ctx context.Context, url string, entry drawioStencilEntry) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errDrawioPermanent, err)
	}
	req.Header.Set("User-Agent", "agent-fleet")
	res, err := drawioStencilHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone {
		return nil, fmt.Errorf("%w: upstream HTTP %d", errDrawioPermanent, res.StatusCode)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream HTTP %d", res.StatusCode)
	}
	// The manifest carries the size, so the read itself can be capped (+1 to spot overrun).
	b, err := io.ReadAll(io.LimitReader(res.Body, entry.Size+1))
	if err != nil {
		return nil, err
	}
	if err := verifyDrawioStencil(b, entry); err != nil {
		// A truncated response has a different length every time, so it is worth retrying;
		// bytes that arrived in full but do not match never will.
		if int64(len(b)) == entry.Size {
			return nil, fmt.Errorf("%w: %v", errDrawioPermanent, err)
		}
		return nil, err
	}
	return b, nil
}

func verifyDrawioStencil(b []byte, entry drawioStencilEntry) error {
	if int64(len(b)) != entry.Size {
		return fmt.Errorf("size %d, want %d", len(b), entry.Size)
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, entry.SHA256) {
		return fmt.Errorf("sha256 %s, want %s", got, entry.SHA256)
	}
	return nil
}
