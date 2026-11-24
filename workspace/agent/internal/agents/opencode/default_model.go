package opencode

// The launch record of a session that names no model (ADR 0138, "An opencode launch with no model
// named"). Nothing af reads from files proves which model opencode will run: a configured model
// that is unavailable falls back, an agent's own model overrides it, and the last used model and
// opencode's own priority depend on the providers the project and its credential store make
// available. The running `opencode serve` knows those providers for a directory, so it is asked.
//
// This is the RECORD only (agents.DefaultLaunchResolver). ResolveLaunch, which a policy judges a
// launch on, stays unresolved for such a launch: a create is judged before its working directory
// is known, and a resume must not wait on a probe.
//
// Measured (opencode 2.4.0, isolated HOME, a shared serve, a loaded host):
//   - GET /api/provider, /api/model/default and /api/plugin take `location[directory]` and answer
//     for that directory (a project config adding a provider shows up only for its own directory).
//     The answer echoes the directory in `location.directory`.
//   - The first request for a directory starts its instance, and until its plugins have loaded the
//     provider list is PARTIAL: [] first, then [opencode], then [opencode, <the project's>]. A
//     partial list of one is exactly the shape that must not resolve, so the answer is read only
//     after /api/plugin lists plugins (it is [] until they are loaded; a plugin is `active` or
//     `failed`). 30 fresh directories, plugins-listed-then-providers: the list was final every time.
//     Settling a cold directory took 0.3-0.7 s (median 0.43 s) at a load average of 14, a warm one
//     0.01-0.1 s. The launch budget is 500 ms (routes.ObserveBudget), so a cold directory can miss
//     it: that launch is recorded unresolved, as before, and the next one finds it warm.
//   - A key in the serve's environment activates its provider (`anthropic`, activation `auto`), and
//     /api/model/default still names a free opencode model then. The default alone therefore proves
//     nothing; the provider list does. The built-in `opencode` provider carries
//     settings.apiKey "public" and the baseURL https://opencode.ai/... when it runs keyless; the
//     default model carries the same settings.
//   - /api/info carries the serve's pid, which is how a serve replaced between two reads is seen.
//
// What the serve's answer cannot cover is covered by af's own view, which must hold no stored key
// at all: a key stored after the serve started reaches a launch's environment (a Terminal that
// runs its own opencode) without reaching the serve.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// defaultModelBudget bounds one directory's answer, the daemon health read included. It is under
// routes.ObserveBudget so the probe ends about when the caller stops waiting, instead of going on
// for a record nobody reads.
const defaultModelBudget = 450 * time.Millisecond

// settlePoll is the gap between two reads of /api/plugin while a cold directory loads.
const settlePoll = 20 * time.Millisecond

// keylessAPIKey is the value opencode gives the `opencode` provider's apiKey when no key reached
// it (the free models; measured).
const keylessAPIKey = "public"

// errNotFreeOnly is why a directory is not proven to offer opencode.ai's free models alone.
var errNotFreeOnly = errors.New("the directory is not proven to offer opencode.ai's free models alone")

// launchDefaultModelOf is the model opencode runs for a session in dir that names none, but only
// when the running serve proves it; "" otherwise. The budget runs from start, the moment the
// caller began, so a slow file system before the first request spends the budget instead of
// starting a fresh one after the caller has stopped waiting. A seam for tests.
var launchDefaultModelOf = func(dir string, start time.Time) string {
	real, ok := realDir(dir)
	if !ok {
		return ""
	}
	ctx, cancel := context.WithDeadline(context.Background(), start.Add(defaultModelBudget))
	defer cancel()
	// Never starts a daemon for a launch record: a daemon that is not there is an error.
	model, err := keylessDefaultModel(ctx, serveAddr(), real)
	if err != nil {
		return ""
	}
	return model
}

// realDir is dir with its symlinks resolved: the directory the OS opens, which the serve is asked
// about and must echo. A path that cannot be resolved is not proven to be the one that runs.
func realDir(dir string) (string, bool) {
	if strings.TrimSpace(dir) == "" || !filepath.IsAbs(dir) {
		return "", false
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", false
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", false
	}
	return real, true
}

type providerEntry struct {
	ID       string `json:"id"`
	Settings struct {
		APIKey  string `json:"apiKey"`
		BaseURL string `json:"baseURL"`
	} `json:"settings"`
}

// defaultModelEntry is /api/model/default's data. Every price field is a pointer: an absent one is
// not zero.
type defaultModelEntry struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Status     string `json:"status"`
	Enabled    *bool  `json:"enabled"`
	Settings   struct {
		APIKey  string `json:"apiKey"`
		BaseURL string `json:"baseURL"`
	} `json:"settings"`
	Cost []struct {
		Input  *float64 `json:"input"`
		Output *float64 `json:"output"`
		Cache  *struct {
			Read  *float64 `json:"read"`
			Write *float64 `json:"write"`
		} `json:"cache"`
	} `json:"cost"`
}

// freeOfCharge is true only when the model lists at least one price tier and every tier states a
// zero input and output price, and a zero cache read and write price where it has a cache entry.
func (m *defaultModelEntry) freeOfCharge() bool {
	if len(m.Cost) == 0 {
		return false
	}
	for _, t := range m.Cost {
		if t.Input == nil || t.Output == nil || *t.Input != 0 || *t.Output != 0 {
			return false
		}
		if t.Cache != nil && (t.Cache.Read == nil || t.Cache.Write == nil || *t.Cache.Read != 0 || *t.Cache.Write != 0) {
			return false
		}
	}
	return true
}

type locatedList[T any] struct {
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
	Data T `json:"data"`
}

// opencodeAIBaseURL reports whether u is opencode.ai's own inference endpoint. A provider that
// keeps the id `opencode` and the key `public` but has its baseURL overridden sends the prompts
// elsewhere, which is not the Zen route.
func opencodeAIBaseURL(u string) bool {
	p, err := url.Parse(u)
	return err == nil && p.Scheme == "https" && p.Host == "opencode.ai" && p.User == nil
}

// keylessDefaultModel returns the default model of dir ("opencode/<id>") when the directory's
// settled providers are exactly the keyless built-in `opencode` provider on opencode.ai's own
// endpoint and the default model is a zero-cost model of it on that endpoint. Any other state is
// an error, never a guess: another provider, a key or another endpoint on the opencode provider,
// no default, a paid or unpriced default, a plugin that failed to load (the provider list may be
// missing what it adds), a daemon that is not a v2 one or was replaced while being read, an answer
// for another directory, or a directory that did not settle within ctx. dir is the resolved path.
func keylessDefaultModel(ctx context.Context, addr, dir string) (string, error) {
	pid, err := serveGeneration(ctx, addr)
	if err != nil {
		return "", err
	}
	if err := waitDirectorySettled(ctx, addr, dir); err != nil {
		return "", err
	}
	var provs locatedList[[]providerEntry]
	if err := getLocated(ctx, addr, "/api/provider", dir, &provs); err != nil {
		return "", err
	}
	var def locatedList[*defaultModelEntry]
	if err := getLocated(ctx, addr, "/api/model/default", dir, &def); err != nil {
		return "", err
	}
	// One serve answered all of it: the readiness of a directory is the readiness of that
	// serve's instance of it.
	if again, err := serveGeneration(ctx, addr); err != nil || again != pid {
		return "", fmt.Errorf("%w: the serve changed while %s was read", errNotFreeOnly, dir)
	}
	if len(provs.Data) != 1 || provs.Data[0].ID != "opencode" || provs.Data[0].Settings.APIKey != keylessAPIKey ||
		!opencodeAIBaseURL(provs.Data[0].Settings.BaseURL) {
		return "", fmt.Errorf("%w: its providers are not the keyless opencode provider alone", errNotFreeOnly)
	}
	m := def.Data
	if m == nil || m.ID == "" || m.ProviderID != "opencode" || m.Status == "deprecated" ||
		(m.Enabled != nil && !*m.Enabled) || m.Settings.APIKey != keylessAPIKey || !opencodeAIBaseURL(m.Settings.BaseURL) ||
		!m.freeOfCharge() {
		return "", fmt.Errorf("%w: its default model is not a free opencode model", errNotFreeOnly)
	}
	return m.ProviderID + "/" + m.ID, nil
}

// serveGeneration is the pid of the v2 serve at addr, read within ctx (a serve that does not
// answer, or is not a v2 one, is an error).
func serveGeneration(ctx context.Context, addr string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/api/info", nil)
	if err != nil {
		return 0, err
	}
	res, err := oauthClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	var info struct {
		Version string `json:"version"`
		PID     int    `json:"pid"`
	}
	if res.StatusCode >= 300 || json.NewDecoder(res.Body).Decode(&info) != nil || info.PID <= 0 {
		return 0, fmt.Errorf("%s is not a running opencode serve", addr)
	}
	if !v2Version(info.Version) {
		return 0, fmt.Errorf("opencode serve %q is not a v2 daemon", info.Version)
	}
	return info.PID, nil
}

// v2Version is healthyV2's rule: a numeric major of 2 or later.
func v2Version(v string) bool {
	major, _, _ := strings.Cut(v, ".")
	n, err := strconv.Atoi(major)
	return err == nil && n >= 2 && major == strconv.Itoa(n)
}

// waitDirectorySettled returns once dir's instance has loaded its plugins, all of them. Until then
// the provider list is partial (see the file comment).
func waitDirectorySettled(ctx context.Context, addr, dir string) error {
	for {
		var plugins locatedList[[]struct {
			State struct {
				Status string `json:"status"`
			} `json:"state"`
		}]
		if err := getLocated(ctx, addr, "/api/plugin", dir, &plugins); err != nil {
			return err
		}
		if len(plugins.Data) > 0 {
			for _, p := range plugins.Data {
				if p.State.Status != "active" {
					return fmt.Errorf("%w: a plugin of %s is %q", errNotFreeOnly, dir, p.State.Status)
				}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s had not settled: %w", dir, ctx.Err())
		case <-time.After(settlePoll):
		}
	}
}

// getLocated reads one directory-scoped endpoint, and refuses an answer that is not for dir (a
// daemon that ignored the parameter would answer for its own directory).
func getLocated[T any](ctx context.Context, addr, path, dir string, out *locatedList[T]) error {
	q := url.Values{"location[directory]": {dir}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	res, err := oauthClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("opencode serve GET %s: %s", path, res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("opencode serve GET %s: %w", path, err) // a v1 daemon answers its web UI here
	}
	if out.Location.Directory != dir {
		return fmt.Errorf("opencode serve GET %s answered for %q, not %q", path, out.Location.Directory, dir)
	}
	return nil
}

// resolveDefaultModelLaunch is the launch record of a session that names no model: the keyless
// free route when the serve proves the session's working directory offers only opencode.ai's free
// models, else nothing. The directory is the one the session runs in (Meta.CWD), resolved the way
// the OS opens it. The model is resolved the way a named one is, so the account and the billing
// choice still apply; anything but the free route is dropped, since the serve proved only that
// the provider is keyless. A Terminal is also dropped (it does not run on the serve), and so is
// af holding any stored key: the launch's environment
// carries it, and the serve may have started before it was stored.
func resolveDefaultModelLaunch(m session.Meta) routes.Launch {
	start := time.Now()
	// Only a session the shared serve runs. A Terminal runs its own opencode in a tmux pane whose
	// environment (a key the pane inherited, the standalone setting, a fall back from a failed
	// Ensure) the serve's provider list says nothing about.
	if m.DriverKind() != session.DriverManaged {
		return routes.Launch{}
	}
	keys, pref, acct, custom := credentialView()
	for _, v := range keys {
		if v != "" {
			return routes.Launch{}
		}
	}
	model := launchDefaultModelOf(m.CWD(), start)
	if model == "" {
		return routes.Launch{}
	}
	l := resolveModel(model, keys, pref, acct, engineProviderIDs(), custom)
	if l != (routes.Launch{Route: routes.RouteOpencodeZen, Binding: FreeBinding}) {
		return routes.Launch{}
	}
	return l
}
