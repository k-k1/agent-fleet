package main

// engine_table.go — reading and parsing the engine table (SSM parameter, env rows) and the
// per-row API key lookups (split out of engines.go; the registry that consumes them stays there).

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
)

// engineSSMAPI is the narrow SSM port, so a test can answer with a table of its own.
type engineSSMAPI interface {
	GetParameter(context.Context, *ssm.GetParameterInput, ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

// loadEngineTable reads the table. AF_ENGINES_JSON is the inline form, for a dev CP with no
// AWS at all; AF_ENGINES_SSM_PARAM is what a real deployment is given.
func loadEngineTable(ctx context.Context, api engineSSMAPI) (engineTable, error) {
	if raw := strings.TrimSpace(envx.Or("AF_ENGINES_JSON", "")); raw != "" {
		return parseEngineTable(raw)
	}
	name := strings.TrimSpace(envx.Or("AF_ENGINES_SSM_PARAM", ""))
	if name == "" || api == nil {
		return engineTable{}, nil
	}
	out, err := api.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(name)})
	if err != nil {
		return engineTable{}, fmt.Errorf("reading %s: %w", name, err)
	}
	return parseEngineTable(aws.ToString(out.Parameter.Value))
}

func parseEngineTable(raw string) (engineTable, error) {
	var t engineTable
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return engineTable{}, fmt.Errorf("parsing the engine table: %w", err)
	}
	for i, d := range t.Engines {
		if d.Key == "" || d.URL == "" {
			return engineTable{}, fmt.Errorf("engine %d: key and url are both required", i)
		}
		// A service is the thing a desired count moves on, so only a row this deployment
		// manages owes one (ADR 0076 decision 1). It stays required for every other row: an
		// empty service name would leave the controller driving nothing while the launch menu
		// still offered the model.
		if d.Service == "" && !d.notManagedHere() {
			return engineTable{}, fmt.Errorf("engine %d: service is required unless lifecycle is %q or %q",
				i, engineLifecycleExternal, engineLifecycleRemote)
		}
	}
	return t, nil
}

// engineBoxRole is the `af-role` attribute value this row's boxes carry, "" for a row that has
// none (ADR 0077 decision 3). Derived from the key and the launch template together: a role that
// buys no box has no box to recognise, and an engine that claimed container instances it never
// bought would be reading the other role's GPU — or a workspace slot — as its own.
func engineBoxRole(d engineDef) string {
	if strings.TrimSpace(d.LaunchTemplate) == "" {
		return ""
	}
	return engineRoleAttr(d.Key)
}

// engineSubnets is where a box may be launched: the deployment's private subnets, exactly as the
// workspace side is given them (ADR 0077 P1 — 30-ingress passes AF_ECS_SUBNETS on every flavour,
// so nothing new has to be declared for this).
func engineSubnets() []string {
	var out []string
	for _, s := range strings.Split(envx.Or("AF_ECS_SUBNETS", ""), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// engineComfyEnvRow synthesises the engine table row an operator gets from AF_COMFY_URL, plus
// the optional bearer from AF_COMFY_API_KEY (ADR 0076 decision 2).
//
// One variable, shaped like AF_VOICEVOX_URL, because that is the whole operator-facing surface
// of an engine somebody else runs. ComfyUI has no authentication of its own, so the key is
// there for a reverse proxy in front of it to check — and BOTH dial and engineHealthy present
// it, which means that proxy has to let the health path through with the same bearer or this
// engine is never healthy (decision 7).
//
// Read once, at startup: an environment variable cannot change under a running process. The
// admin panel is the way to change the URL without a restart, and its value wins over this one
// (engine_comfy_panel.go).
func engineComfyEnvRow() (engineDef, string, bool) {
	url := strings.TrimSpace(envx.Or("AF_COMFY_URL", ""))
	if url == "" {
		return engineDef{}, "", false
	}
	return engineComfyRow(url, engineOriginEnv), strings.TrimSpace(envx.Or("AF_COMFY_API_KEY", "")), true
}

// engineComfyRow is the external image row a LAN ComfyUI URL stands for, whichever source named
// it — the environment or the admin panel build the same row and differ only in origin.
func engineComfyRow(url, origin string) engineDef {
	return engineDef{
		Key:       "image",
		API:       engineAPIImages,
		Provider:  "comfy",
		URL:       url,
		Health:    "/system_stats",
		Lifecycle: engineLifecycleExternal,
		origin:    origin,
	}
}

// engineLlmEnvRow synthesises the engine table row an operator gets from AF_LLM_URL: the
// shortcut for pointing the llm role at a llama.cpp on the operator's own network (docs/log/106),
// the same shape engineComfyEnvRow already gave the image role (ADR 0076 decision 2).
//
// No AF_LLM_API_KEY. The bearer is engineEnvAPIKey's AF_ENGINE_API_KEY_LLM — the variable every
// OTHER external row already reads (ADR 0079 decision 11) — rather than a key of its own, so this
// function returns no key: AF_COMFY_API_KEY only carries one because it predates that variable,
// and giving llm a second named variable for the same bearer would be a fresh 401 waiting for
// whichever one an operator edits.
//
// warmPath is "/models", not empty: a llama.cpp router answers /health with ok while holding no
// weights at all (ADR 0072 P1, measured), so warmth has to be asked somewhere else.
//
// Read once, at startup, like AF_COMFY_URL: an environment variable cannot change under a running
// process, so changing it is a Control Plane restart.
func engineLlmEnvRow() (engineDef, bool) {
	url := strings.TrimSpace(envx.Or("AF_LLM_URL", ""))
	if url == "" {
		return engineDef{}, false
	}
	return engineDef{
		Key:       "llm",
		API:       engineAPIChat,
		Provider:  "llamacpp",
		URL:       url,
		Health:    "/health",
		WarmPath:  "/models",
		Lifecycle: engineLifecycleExternal,
	}, true
}

// engineTableWithEnvRow merges the synthesised row into the table on the key both claim
// (ADR 0076 decision 2). envVar names the URL variable the row came from — AF_COMFY_URL or
// AF_LLM_URL — purely so the three log lines below can say which one, instead of a literal that
// was only ever true for the image role.
//
// A MANAGED row wins over the environment, which is the opposite of what the draft said. The
// review turned it round: replacing a controlled row leaves the ECS service it named with
// nobody to stop it, and paying for a GPU box nothing can switch off is the more expensive of
// the two mistakes. An operator moving that role onto a LAN box takes it out of the stack.
// Either way one line says which row won, because the alternative is a URL in the panel that
// matches neither of the two places it could have come from.
func engineTableWithEnvRow(rows []engineDef, env engineDef, envVar string) []engineDef {
	for i, d := range rows {
		if d.Key != env.Key {
			continue
		}
		if !d.notManagedHere() {
			log.Printf("engines: %s is a managed row in the engine table, so %s is ignored (take the role out of the stack to move it onto the network)", d.Key, envVar)
			return rows
		}
		log.Printf("engines: %s comes from %s (%s), replacing the external row in the engine table", env.Key, envVar, env.URL)
		out := append([]engineDef(nil), rows...)
		out[i] = env
		return out
	}
	log.Printf("engines: %s comes from %s (%s), externally managed", env.Key, envVar, env.URL)
	return append(append([]engineDef(nil), rows...), env)
}

// engineTableNeedsAWS reports whether any row in this table has to be driven through ECS. It
// is what keeps a native or docker deployment — nothing but AF_COMFY_URL, no credentials, no
// region — from loading an AWS config for a feature it is not using (ADR 0076 decision 3).
func engineTableNeedsAWS(t engineTable) bool {
	for _, d := range t.Engines {
		if !d.notManagedHere() {
			return true
		}
	}
	return false
}

// engineEnvAPIKey is the bearer an EXTERNAL row presents upstream, declared in the environment as
// AF_ENGINE_API_KEY_<KEY> (ADR 0079 decision 11).
//
// Without it, "an engine somebody else runs, behind something that checks a bearer" was open to
// the image role alone: `apiKey` was filled only for the comfy row synthesised from AF_COMFY_URL,
// and an inline AF_ENGINES_JSON row — the only way to declare an external LLM engine at all — had
// nowhere to carry one, so anything checking answered it 401. A native, docker or ec2-single
// deployment could have an external image engine and no external chat engine whatsoever.
//
// EXTERNAL ONLY, and the narrow predicate is the point. A MANAGED row's key is an SSM
// SecureString (readEngineAPIKey), because a deployment that runs the engine has credentials to
// read one with. A REMOTE row mints its own per (key, session) and keeps it in no field at all
// (ADR 0079 decision 4) — a static bearer read here would be a second, staler answer to a
// question that already has one, and it would be the one `dial` never asks for.
//
// Read once, at registry build, like every other AF_ variable: an environment variable cannot
// change under a running process, so changing it is a Control Plane restart.
func engineEnvAPIKey(d engineDef) string {
	// 🔴 Never for the admin panel's row: its URL is one an administrator typed in, and a bearer
	// configured in the environment for some other host must not be sent to it (#957, ADR 0076
	// 2026-10-04 addendum). An empty panel key means no bearer at all.
	if !d.external() || d.origin == engineOriginPanel {
		return ""
	}
	return strings.TrimSpace(envx.Or(engineAPIKeyEnvName(d.Key), ""))
}

// engineAPIKeyEnvName is the variable one row's bearer is read from.
//
// The key is folded to a shape a shell can export — upper case, and every character outside
// [A-Z0-9] as `_` — because a table key is free text (`image`, `llm`, and nothing in
// parseEngineTable stops an operator writing `image-2`) while an environment variable name is
// not. Two keys can therefore fold together; that is a table nobody writes, and the alternative
// is a key whose bearer cannot be declared at all.
func engineAPIKeyEnvName(key string) string {
	var b strings.Builder
	b.WriteString("AF_ENGINE_API_KEY_")
	for _, r := range strings.ToUpper(strings.TrimSpace(key)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	return b.String()
}

// readEngineAPIKey fetches the engine's own --api-key. Not fatal when it fails: an engine
// started without one still answers, and refusing to serve because the SECOND lock could not
// be read would turn a defence-in-depth measure into a single point of failure. The
// reachability rule (the SG lets only the CP in) is the first lock and does not depend on
// this.
func readEngineAPIKey(ctx context.Context, api engineSSMAPI, d engineDef) string {
	if strings.TrimSpace(d.APIKeyParam) == "" || api == nil {
		return ""
	}
	out, err := api.GetParameter(ctx, &ssm.GetParameterInput{
		Name: aws.String(d.APIKeyParam), WithDecryption: aws.Bool(true),
	})
	if err != nil {
		log.Printf("engines: %s api key (%s) unreadable: %v", d.Key, d.APIKeyParam, err)
		return ""
	}
	return strings.TrimSpace(aws.ToString(out.Parameter.Value))
}
