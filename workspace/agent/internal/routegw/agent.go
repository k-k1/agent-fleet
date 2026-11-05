package routegw

import (
	"fmt"
	"log"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/awsx"
)

// StartForAgent starts the AF route gateway (ADR 0128 P2a) and returns its stop function. A
// failure to listen is logged and costs only the route: no session selects it until a harness
// wires it (later slices), and Issue refuses while the gateway is not listening. ensureAWSCLI is
// the Agent's own AWS CLI installer, which lives in package main.
func StartForAgent(ensureAWSCLI func() (string, error)) func() {
	stop, err := Start(agentDeps(ensureAWSCLI))
	if err != nil {
		log.Printf("route gateway: not started: %v", err)
		return func() {}
	}
	return stop
}

// agentDeps wires the gateway to the Agent's own profile machinery: the Settings list the
// bridge pulls, and the same credential resolution af-aws-exec runs, without its login prompt.
func agentDeps(ensureAWSCLI func() (string, error)) Deps {
	var mu sync.Mutex
	var last awsx.SyncResult
	src := &SettingsSource{
		MinInterval: 30 * time.Second,
		MaxAge:      15 * time.Minute,
		Pull: func() (map[string]awsx.Profile, bool) {
			res, _ := awsx.Sync()
			if !res.Fetched {
				return nil, false
			}
			mu.Lock()
			last = res
			mu.Unlock()
			return res.Settings, true
		},
	}
	return Deps{
		Settings: src.Get,
		Credentials: func(p awsx.Profile, region string, settings map[string]awsx.Profile) (awsx.RouteCredentials, error) {
			mu.Lock()
			res := last
			mu.Unlock()
			o := awsx.ExecOptions{
				Profile: p.Name, Region: region, Settings: settings, Conflicts: res.Conflicts,
				DefaultClash: res.DefaultClash, ChainBroken: res.ChainBroken, Exported: res.Usable(),
			}
			if slices.Contains(res.Withheld, p.Name) {
				o.GatedConfig = awsx.GatedConfigPath(p.Name)
				// The member's own files may define the profile, its sso-session or its source by
				// now, and theirs wins (ADR 0126 decision 6).
				usable := awsx.UsableNow()
				if !slices.Contains(usable, p.Name) {
					return awsx.RouteCredentials{}, fmt.Errorf("profile %q is no longer usable from Settings (your own ~/.aws files define it, or [DEFAULT] conflicts)", p.Name)
				}
				o.Exported = usable
			}
			if err := awsx.VetKeyOf(settings, p.Name); err != nil {
				return awsx.RouteCredentials{}, err
			}
			awsBin, err := ensureAWSCLI()
			if err != nil {
				return awsx.RouteCredentials{}, err
			}
			return awsx.ResolveCredentials(awsBin, os.Environ(), o)
		},
	}
}
