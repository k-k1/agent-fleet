package mcpreg

// materialize for the P5 JSON-config kinds (opencode / copilot / cursor / kiro / agy).
//
// The first test pins the same non-destructiveness contract as claude (docs/log/48 §13) across
// every kind. They all run through the common engine (materialize_json.go), so the assertions
// are the same, but what differs per kind is the file written and the map key inside it — and
// getting that wrong means either "registered, yet nothing happens" (written to the wrong file)
// or a wrecked user config.
//
// The remaining tests pin the shape of an entry per kind: of the measured contract (the header
// comment of each materialize_<kind>.go), the keys whose loss silently disables the feature.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// jsonKindCases are the P5 kinds, each with the writer, the file it owns and the
// member that holds the server map.
var jsonKindCases = []struct {
	kind string
	fn   writer
	path func() string
	key  string
}{
	{"opencode", materializeOpencode, opencodeConfigPath, "mcp"},
	{"copilot", materializeCopilot, copilotMCPConfigPath, "mcpServers"},
	{"cursor", materializeCursor, cursorMCPConfigPath, "mcpServers"},
	{"kiro", materializeKiro, kiroMCPConfigPath, "mcpServers"},
	{"agy", materializeAgy, agyMCPConfigPath, "mcpServers"},
}

func p5Defs() []ServerDef {
	return []ServerDef{
		sessionDef(ServerDef{Name: "wiki", Origin: OriginUser, Transport: TransportStdio,
			Command: "npx", Args: []string{"-y", "wiki-mcp"}, Env: map[string]string{"TOKEN": "s3cret"}}),
		sessionDef(ServerDef{Name: "tickets", Origin: OriginUser, Transport: TransportHTTP,
			URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer t"},
			TimeoutMS: 12000}),
	}
}

// serverMap decodes the server map out of a kind's config file.
func serverMap(t *testing.T, path, key string) map[string]any {
	t.Helper()
	root := map[string]any{}
	if err := json.Unmarshal([]byte(readFile(t, path)), &root); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	m, _ := root[key].(map[string]any)
	return m
}

func TestMaterializeJSONKindsKeepUserState(t *testing.T) {
	for _, tc := range jsonKindCases {
		t.Run(tc.kind, func(t *testing.T) {
			withTempCLIHomes(t)
			path := tc.path()
			// A server the user added by hand (or via `<cli> mcp add`), plus another key
			// af knows nothing about.
			writeFile(t, path, `{
  "someOtherSetting": {"keep": true},
  "`+tc.key+`": {"mine": {"command": "/usr/bin/mine"}}
}`)

			defs := p5Defs()
			written, removed, changed, err := tc.fn(defs, nil)
			if err != nil || !changed {
				t.Fatalf("= %v, changed=%v", err, changed)
			}
			if len(written) != 2 || len(removed) != 0 {
				t.Fatalf("written=%v removed=%v", written, removed)
			}
			srv := serverMap(t, path, tc.key)
			for _, want := range []string{"mine", "wiki", "tickets"} {
				if srv[want] == nil {
					t.Fatalf("%s is missing %q: %v", tc.key, want, srv)
				}
			}
			root := map[string]any{}
			if err := json.Unmarshal([]byte(readFile(t, path)), &root); err != nil {
				t.Fatal(err)
			}
			if root["someOtherSetting"] == nil {
				t.Fatal("dropped an unrelated setting key af does not own")
			}

			// Idempotent: the second run writes nothing. The CLI writes this file too, so
			// rewriting it on an unchanged start widens the window for clobbering its write.
			if _, _, changed, err := tc.fn(defs, written); err != nil || changed {
				t.Fatalf("second run = changed %v, err %v (not idempotent)", changed, err)
			}

			// Clear the registry: only the 2 entries af wrote go, the user's "mine" stays.
			_, removed, changed, err = tc.fn(nil, written)
			if err != nil || !changed {
				t.Fatalf("removal = %v, changed=%v", err, changed)
			}
			if len(removed) != 2 {
				t.Fatalf("removed=%v, want 2 entries", removed)
			}
			srv = serverMap(t, path, tc.key)
			if len(srv) != 1 || srv["mine"] == nil {
				t.Fatalf("the user's hand-written entry was removed too: %v", srv)
			}
		})
	}
}

// TestMaterializeJSONKindsRefuseUnparseable: a config that cannot be read is left untouched.
// For the same reason as claude's onboarding flags, this is also what protects the comments
// in opencode.jsonc.
func TestMaterializeJSONKindsRefuseUnparseable(t *testing.T) {
	for _, tc := range jsonKindCases {
		t.Run(tc.kind, func(t *testing.T) {
			withTempCLIHomes(t)
			broken := "// 利用者のコメント\n{\"mcp\": {}}\n"
			writeFile(t, tc.path(), broken)
			if _, _, _, err := tc.fn(p5Defs(), nil); err == nil {
				t.Fatal("silently overwrote a config that is not plain JSON")
			}
			if got := readFile(t, tc.path()); got != broken {
				t.Fatalf("refused, yet the file was modified: %q", got)
			}
		})
	}
}

func TestMaterializeJSONKindsCreateFile0600(t *testing.T) {
	for _, tc := range jsonKindCases {
		t.Run(tc.kind, func(t *testing.T) {
			withTempCLIHomes(t)
			if _, _, changed, err := tc.fn(p5Defs(), nil); err != nil || !changed {
				t.Fatalf("= %v, changed=%v", err, changed)
			}
			fi, err := os.Stat(tc.path())
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o600 {
				t.Fatalf("mode = %v, want 0600 (the file holds secrets)", fi.Mode().Perm())
			}
		})
	}
}

// TestMaterializeJSONKindsLeaveMissingFileMissing: a kind with nothing to write creates no
// config file. Dropping an empty config leaves af's traces in CLIs the user never touched.
func TestMaterializeJSONKindsLeaveMissingFileMissing(t *testing.T) {
	for _, tc := range jsonKindCases {
		t.Run(tc.kind, func(t *testing.T) {
			withTempCLIHomes(t)
			if _, _, changed, err := tc.fn(nil, nil); err != nil || changed {
				t.Fatalf("= %v, changed=%v", err, changed)
			}
			if _, err := os.Stat(tc.path()); !os.IsNotExist(err) {
				t.Fatalf("created an empty config file: %v", tc.path())
			}
		})
	}
}

// --- opencode --------------------------------------------------------------------

// TestOpencodeConfigPathPrefersExisting: opencode reads and merges both opencode.jsonc and
// opencode.json (measured on 1.18.7), so writing to the other one lists the same server
// twice. Edit whichever exists, and create .jsonc when neither does.
func TestOpencodeConfigPathPrefersExisting(t *testing.T) {
	t.Run("neither", func(t *testing.T) {
		withTempCLIHomes(t)
		if got := filepath.Base(opencodeConfigPath()); got != "opencode.jsonc" {
			t.Fatalf("create target = %s, want opencode.jsonc", got)
		}
	})
	t.Run("json only", func(t *testing.T) {
		home := withTempCLIHomes(t)
		writeFile(t, filepath.Join(home, ".config", "opencode", "opencode.json"), "{}")
		if got := filepath.Base(opencodeConfigPath()); got != "opencode.json" {
			t.Fatalf("chose %s instead of the existing opencode.json (config ends up duplicated)", got)
		}
	})
	t.Run("both", func(t *testing.T) {
		home := withTempCLIHomes(t)
		dir := filepath.Join(home, ".config", "opencode")
		writeFile(t, filepath.Join(dir, "opencode.json"), "{}")
		writeFile(t, filepath.Join(dir, "opencode.jsonc"), "{}")
		if got := filepath.Base(opencodeConfigPath()); got != "opencode.jsonc" {
			t.Fatalf("with both present = %s, want opencode.jsonc (the one the CLI itself and the entrypoint create)", got)
		}
	})
}

func TestMaterializeOpencodeSeedsSchema(t *testing.T) {
	withTempCLIHomes(t)
	if _, _, _, err := materializeOpencode(p5Defs(), nil); err != nil {
		t.Fatal(err)
	}
	root := map[string]any{}
	if err := json.Unmarshal([]byte(readFile(t, opencodeConfigPath())), &root); err != nil {
		t.Fatal(err)
	}
	if root["$schema"] != "https://opencode.ai/config.json" {
		t.Fatalf("the newly created config has no $schema: %v", root)
	}
}

func TestOpencodeServersShape(t *testing.T) {
	got := OpencodeServers(p5Defs())
	// local folds command and args into a single array (measured); split apart it will not start.
	want := map[string]any{
		"type":        "local",
		"command":     []any{"npx", "-y", "wiki-mcp"},
		"environment": map[string]any{"TOKEN": "s3cret"},
		"enabled":     true,
	}
	if !reflect.DeepEqual(got["wiki"], want) {
		t.Fatalf("local = %#v, want %#v", got["wiki"], want)
	}
	rem, _ := got["tickets"].(map[string]any)
	if rem["type"] != "remote" || rem["url"] != "https://mcp.example.com/mcp" {
		t.Fatalf("remote = %#v", rem)
	}
	if _, ok := rem["timeout"]; ok {
		t.Fatal("opencode has no per-server timeout key (measured) - this is written where it has no effect")
	}
}

// --- copilot ---------------------------------------------------------------------

func TestCopilotServersShape(t *testing.T) {
	got := copilotServers(p5Defs())
	loc, _ := got["wiki"].(map[string]any)
	if loc["type"] != "local" || loc["command"] != "npx" {
		t.Fatalf("local = %#v", loc)
	}
	// Dropping tools departs from `copilot mcp add`'s default ("*" = every tool).
	if !reflect.DeepEqual(loc["tools"], []any{"*"}) {
		t.Fatalf("tools = %#v, want [\"*\"] (omitting it may leave no tools exposed)", loc["tools"])
	}
	rem, _ := got["tickets"].(map[string]any)
	if rem["type"] != "http" || rem["url"] != "https://mcp.example.com/mcp" {
		t.Fatalf("remote = %#v", rem)
	}
	// copilot's timeout is in milliseconds - unlike codex's startup_timeout_sec, do not convert.
	if rem["timeout"] != 12000 {
		t.Fatalf("timeout = %#v, want 12000 (kept in ms)", rem["timeout"])
	}
	if rem["headers"].(map[string]any)["Authorization"] != "Bearer t" {
		t.Fatalf("headers = %#v", rem["headers"])
	}
}

// --- kiro ------------------------------------------------------------------------

func TestKiroServersShape(t *testing.T) {
	got := kiroServers(p5Defs())
	loc, _ := got["wiki"].(map[string]any)
	// kiro has no type discriminator; it decides on the presence of command / url
	// (measured on 2.14.2).
	if _, ok := loc["type"]; ok {
		t.Fatalf("wrote a type for kiro: %#v", loc)
	}
	if loc["command"] != "npx" || !reflect.DeepEqual(loc["args"], []any{"-y", "wiki-mcp"}) {
		t.Fatalf("local = %#v", loc)
	}
	rem, _ := got["tickets"].(map[string]any)
	if rem["url"] != "https://mcp.example.com/mcp" || rem["timeout"] != 12000 {
		t.Fatalf("remote = %#v", rem)
	}
	// Headers are the whole authentication mechanism of a tenant-distributed server
	// (distribution is remote-only, ADR0031 decision 2). Drop them and the breakage takes the
	// shape of "only the servers the administrator handed out return 401".
	if rem["headers"].(map[string]any)["Authorization"] != "Bearer t" {
		t.Fatalf("headers = %#v", rem["headers"])
	}
}

// --- cursor / agy ----------------------------------------------------------------

func TestCursorServersShape(t *testing.T) {
	got := cursorServers(p5Defs())
	loc, _ := got["wiki"].(map[string]any)
	// cursor's parser decides stdio by `"command" in o` (measured from the bundle).
	if loc["command"] != "npx" || !reflect.DeepEqual(loc["env"], map[string]any{"TOKEN": "s3cret"}) {
		t.Fatalf("local = %#v", loc)
	}
	rem, _ := got["tickets"].(map[string]any)
	if rem["url"] != "https://mcp.example.com/mcp" {
		t.Fatalf("remote = %#v", rem)
	}
	if _, ok := rem["timeout"]; ok {
		t.Fatal("cursor's entry parser has no timeout (measured) - this is written where it has no effect")
	}
}

func TestMaterializeAgyWritesGeminiConfig(t *testing.T) {
	home := withTempCLIHomes(t)
	if _, _, _, err := materializeAgy(p5Defs(), nil); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	if agyMCPConfigPath() != want {
		t.Fatalf("path = %s, want %s (agy hard-codes ~/.gemini)", agyMCPConfigPath(), want)
	}
	if got := readFile(t, want); !strings.Contains(got, `"mcpServers"`) {
		t.Fatalf("no mcpServers:\n%s", got)
	}
}
