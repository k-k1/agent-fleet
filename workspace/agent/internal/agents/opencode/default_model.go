package opencode

// The launch route of a session that names no model (ADR 0138, "An opencode launch with no model
// named"). Nothing af reads from files proves which model opencode will run: a configured model
// that is unavailable falls back, an agent's own model overrides it, and the last used model and
// opencode's own priority depend on the providers the project and its credential store make
// available. The running `opencode serve` knows those providers for a directory, so it is asked.
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
//     settings.apiKey "public" when it runs keyless, and a different value when a key reached it.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// defaultModelBudget bounds one directory's answer. It is under routes.ObserveBudget so the probe
// ends about when the caller stops waiting, instead of polling on for a record nobody reads.
const defaultModelBudget = 450 * time.Millisecond

// settlePoll is the gap between two reads of /api/plugin while a cold directory loads.
const settlePoll = 20 * time.Millisecond

// keylessAPIKey is the value opencode gives the `opencode` provider's apiKey when no key reached
// it (the free models; measured).
const keylessAPIKey = "public"

// launchDefaultModelOf is the model opencode runs for a session in dir that names none, but only
// when the running serve proves it; "" otherwise. A seam for tests.
var launchDefaultModelOf = func(dir string) string {
	if strings.TrimSpace(dir) == "" || !filepath.IsAbs(dir) {
		return ""
	}
	addr, up := oauthProbe() // never starts a daemon for a launch record
	if !up {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultModelBudget)
	defer cancel()
	model, err := keylessDefaultModel(ctx, addr, filepath.Clean(dir))
	if err != nil {
		return ""
	}
	return model
}

type providerEntry struct {
	ID       string `json:"id"`
	Settings struct {
		APIKey string `json:"apiKey"`
	} `json:"settings"`
}

type locatedList[T any] struct {
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
	Data T `json:"data"`
}

// keylessDefaultModel returns the default model of dir ("opencode/<id>") when the directory's
// settled providers are exactly the keyless built-in `opencode` provider and the default model is
// a zero-cost model of it. Any other state is an error, never a guess: another provider, a key on
// the opencode provider (its payer is not the free one), no default, a paid default, a plugin that
// failed to load (the provider list may be missing what it adds), a daemon that is not a v2 one,
// an answer for another directory, or a directory that did not settle within ctx.
func keylessDefaultModel(ctx context.Context, addr, dir string) (string, error) {
	if err := waitDirectorySettled(ctx, addr, dir); err != nil {
		return "", err
	}
	var provs locatedList[[]providerEntry]
	if err := getLocated(ctx, addr, "/api/provider", dir, &provs); err != nil {
		return "", err
	}
	if len(provs.Data) != 1 || provs.Data[0].ID != "opencode" || provs.Data[0].Settings.APIKey != keylessAPIKey {
		return "", fmt.Errorf("providers of %s are not the keyless opencode provider alone", dir)
	}
	var def locatedList[*daemonModel]
	if err := getLocated(ctx, addr, "/api/model/default", dir, &def); err != nil {
		return "", err
	}
	m := def.Data
	if m == nil || m.ID == "" || m.ProviderID != "opencode" || m.Status == "deprecated" ||
		(m.Enabled != nil && !*m.Enabled) || len(m.Cost) == 0 || !freeCost(m.Cost) {
		return "", fmt.Errorf("the default model of %s is not a free opencode model", dir)
	}
	return m.ProviderID + "/" + m.ID, nil
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
					return fmt.Errorf("a plugin of %s is %q", dir, p.State.Status)
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
	if filepath.Clean(out.Location.Directory) != dir {
		return fmt.Errorf("opencode serve GET %s answered for %q, not %q", path, out.Location.Directory, dir)
	}
	return nil
}

// resolveDefaultModelLaunch is the launch record of a session in dir that names no model: the
// keyless free route when the serve proves the directory offers only opencode.ai's free models,
// else nothing. The model is resolved the way a named one is, so the stored keys, the account and
// the billing choice still apply; anything but the free route is dropped, because a key or login
// on the launch is not what the serve proved (it proved the provider is keyless).
func resolveDefaultModelLaunch(m session.Meta) routes.Launch {
	model := launchDefaultModelOf(m.Dir)
	if model == "" {
		return routes.Launch{}
	}
	keys, pref, acct, custom := credentialView()
	l := resolveModel(model, keys, pref, acct, engineProviderIDs(), custom)
	if l != (routes.Launch{Route: routes.RouteOpencodeZen, Binding: FreeBinding}) {
		return routes.Launch{}
	}
	return l
}
