package main

// The Agent's own copy of the models.dev catalog (Issue #972). usage_catalog.go used to find a
// price catalog only where opencode happened to have left one, so a workspace that never ran
// opencode had no prices at all — and the recommended one-shot model (chatx's
// recommendedUtilityModel) now follows "the cheapest model this account lists", which needs a
// price for every kind, not only for members who use opencode.
//
// Strictly best-effort, like the reader: a tenant whose egress blocks models.dev, an upstream
// outage or a reshaped file only leaves the previous copy (or none) in place, and every caller
// already degrades to its fixed rule when no price is found. Nothing waits on this.

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// modelsDevDefaultURL is the public catalog (MIT licensed, the same file opencode caches).
const modelsDevDefaultURL = "https://models.dev/api.json"

// modelsDevMaxAge is how old the copy may get before it is fetched again. Prices move on the
// scale of weeks; once a day keeps a new model's price from lagging by more than that while
// costing each workspace one ~5MB download a day.
const modelsDevMaxAge = 24 * time.Hour

// modelsDevCheckEvery is how often the age is looked at. Checking hourly rather than sleeping
// a full day means an Agent restart never pushes the next fetch a day further out, and a boot
// right after a fetch does not fetch again.
const modelsDevCheckEvery = time.Hour

// modelsDevURL is where to fetch from. AF_MODELS_DEV_URL overrides it (a mirror inside a
// closed network), and "off" disables fetching entirely — the reader then falls back to
// opencode's cache exactly as before.
func modelsDevURL() string {
	v := strings.TrimSpace(os.Getenv("AF_MODELS_DEV_URL"))
	if strings.EqualFold(v, "off") {
		return ""
	}
	if v == "" {
		return modelsDevDefaultURL
	}
	return v
}

// modelsDevPath is where the fetched copy lives. Not usageDir()/catalog.json: that name is the
// slot an operator fills by hand (usageCatalogFiles), and overwriting it daily would silently
// discard their snapshot.
func modelsDevPath() string { return filepath.Join(usagex.Dir(), "models.dev.json") }

// startModelsDevRefresh runs the daily refresh for the Agent's lifetime.
func startModelsDevRefresh() {
	if modelsDevURL() == "" {
		return
	}
	go func() {
		for {
			refreshModelsDevIfStale(context.Background(), time.Now())
			time.Sleep(modelsDevCheckEvery)
		}
	}()
}

// refreshModelsDevIfStale fetches when the copy is missing or older than modelsDevMaxAge.
// A failure is logged and left for the next hourly check; it never removes a copy that is
// already there.
func refreshModelsDevIfStale(ctx context.Context, now time.Time) {
	url := modelsDevURL()
	if url == "" {
		return
	}
	path := modelsDevPath()
	if st, err := os.Stat(path); err == nil && now.Sub(st.ModTime()) < modelsDevMaxAge {
		return
	}
	if err := fetchModelsDev(ctx, url, path); err != nil {
		log.Printf("models.dev catalog: %v (keeping the previous copy, if any)", err)
	}
}

// fetchModelsDev downloads the catalog, checks that it parses into at least one indexed price
// (so an HTML error page or a reshaped file never replaces a good copy), and swaps it in
// atomically — the reader stats and reads this file concurrently.
func fetchModelsDev(ctx context.Context, url, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, usageCatalogMaxBytes+1))
	if err != nil {
		return err
	}
	if len(b) > usageCatalogMaxBytes {
		return fmt.Errorf("GET %s: larger than %d bytes", url, usageCatalogMaxBytes)
	}
	if parseUsageCatalog(b, usageCatalogOriginFetched, time.Now()) == nil {
		return fmt.Errorf("GET %s: not a models.dev catalog", url)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".models.dev-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
