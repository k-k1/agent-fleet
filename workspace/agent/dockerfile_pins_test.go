package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// pinExemptions are the Dockerfile `ARG *_VERSION=` defaults that deliberately have no
// Toolchains row with a Pin. Each needs a reason; a new ARG with neither a row nor an entry
// here fails TestDockerfileVersionArgsAreCovered.
var pinExemptions = map[string]string{
	"NPM_VERSION":          "toolchain of the image build (npm for the global installs), not a member-facing tool; not in versions.json",
	"CHROMIUM_VERSION":     "Debian package version (`…-1~deb13u1`); `chromium --version` prints the upstream part only, so a Pin would read as drift forever",
	"CHROMIUM_CFT_VERSION": "Chrome for Testing build fetched on demand by install-chromium; no baked binary to probe",
	"CHROMIUM_DL_VERSION":  "snapshot revision number for the on-demand download, not a version a binary prints",
	"NOTO_CJK_VERSION":     "font release, not an executable",
	"POSTGRES_VERSION":     "has a row without a Pin on purpose: versions.json holds the Zonky server version, psql prints the Debian client version (see toolSpecs)",
}

// pinKeyAliases are the ARGs whose versions.json key is not the lower-cased ARG name without
// `_VERSION`. Pinning the expected key per ARG is what catches two printf arguments swapped.
var pinKeyAliases = map[string]string{
	"CLAUDE_CODE_VERSION":    "claude",
	"JIRA_CLI_VERSION":       "jira",
	"CONFLUENCE_CLI_VERSION": "confluence",
}

func expectedPinKey(arg string) string {
	if k, ok := pinKeyAliases[arg]; ok {
		return k
	}
	return strings.ToLower(strings.TrimSuffix(arg, "_VERSION"))
}

var (
	dockerArgDefaultRe = regexp.MustCompile(`(?m)^\s*ARG\s+([A-Z][A-Z0-9_]*_VERSION)=\S`)
	printfKeyRe        = regexp.MustCompile(`"([a-z0-9_]+)":"%s"`)
	printfArgRe        = regexp.MustCompile(`"([^"]*)"`)
)

// versionsJSONPositions maps each shell variable passed to the versions.json printf in
// workspace/Dockerfile to the JSON key at the same argument position.
func versionsJSONPositions(t *testing.T, dockerfile string) map[string]string {
	t.Helper()
	i := strings.Index(dockerfile, "printf '{")
	if i < 0 {
		t.Fatal("versions.json printf not found in workspace/Dockerfile")
	}
	rest := dockerfile[i+len("printf '"):]
	end := strings.Index(rest, "'")
	if end < 0 {
		t.Fatal("versions.json printf format is not terminated")
	}
	format := rest[:end]
	tail := rest[end+1:]
	if j := strings.Index(tail, "versions.json"); j >= 0 {
		tail = tail[:j]
	}
	var keys []string
	for _, m := range printfKeyRe.FindAllStringSubmatch(format, -1) {
		keys = append(keys, m[1])
	}
	var args []string
	for _, m := range printfArgRe.FindAllStringSubmatch(tail, -1) {
		args = append(args, m[1])
	}
	if len(keys) != len(args) {
		t.Fatalf("versions.json printf has %d keys but %d arguments", len(keys), len(args))
	}
	out := map[string]string{}
	for n, a := range args {
		if strings.HasPrefix(a, "$") {
			name := strings.TrimPrefix(a, "$")
			name = strings.TrimSuffix(strings.TrimPrefix(name, "{"), "}")
			out[name] = keys[n]
		}
	}
	return out
}

// Every pinned tool in the image shows up in Settings > Toolchains: each `ARG X_VERSION=<v>`
// default in workspace/Dockerfile has a versions.json key and a toolSpecs row whose Pin is
// that key, or an entry in pinExemptions. Without it a tool added to the image is silently
// missing from Toolchains (gitleaks was).
func TestDockerfileVersionArgsAreCovered(t *testing.T) {
	raw, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	df := string(raw)
	positions := versionsJSONPositions(t, df)
	pins := map[string]bool{}
	for _, s := range toolSpecs {
		if s.Pin != "" {
			pins[s.Pin] = true
		}
	}
	seen := map[string]bool{}
	for _, m := range dockerArgDefaultRe.FindAllStringSubmatch(df, -1) {
		arg := m[1]
		if seen[arg] {
			continue
		}
		seen[arg] = true
		if reason, ok := pinExemptions[arg]; ok {
			if reason == "" {
				t.Errorf("%s: exemption without a reason", arg)
			}
			continue
		}
		key, ok := positions[arg]
		if !ok {
			t.Errorf("%s: not passed to the versions.json printf in workspace/Dockerfile. Add a key and a \"$%s\" argument at the same position, add a toolSpecs row (workspace/agent/env_tool_versions.go) with Pin set to that key, or exempt it in pinExemptions with a reason", arg, arg)
			continue
		}
		if want := expectedPinKey(arg); key != want {
			t.Errorf("%s: the versions.json printf puts it under %q, want %q (arguments swapped or misplaced? add a pinKeyAliases entry if the key is named differently on purpose)", arg, key, want)
			continue
		}
		if !pins[key] {
			t.Errorf("%s: versions.json has %q but no toolSpecs row has Pin %q (workspace/agent/env_tool_versions.go); add the row or exempt the ARG in pinExemptions with a reason", arg, key, key)
		}
	}
	for arg := range pinExemptions {
		if !seen[arg] {
			t.Errorf("pinExemptions lists %s, which is no longer an ARG default in workspace/Dockerfile; drop the entry", arg)
		}
	}
}

// The two rows added for the pins that had no Toolchains row: the probe arguments and baked
// paths are what the Console shows, and gitleaks' version subcommand is not --version.
func TestGitleaksAndSessionManagerRows(t *testing.T) {
	want := map[string]toolSpec{
		"gitleaks":               {Cmd: "gitleaks", Baked: "/usr/local/bin/gitleaks", Pin: "gitleaks"},
		"session-manager-plugin": {Cmd: "session-manager-plugin", Baked: "/usr/local/bin/session-manager-plugin", Pin: "session_manager_plugin"},
	}
	for name, w := range want {
		found := false
		for _, s := range toolSpecs {
			if s.Name != name {
				continue
			}
			found = true
			if s.Cmd != w.Cmd || s.Baked != w.Baked || s.Pin != w.Pin {
				t.Errorf("%s row = %+v, want Cmd=%s Baked=%s Pin=%s", name, s, w.Cmd, w.Baked, w.Pin)
			}
			if name == "gitleaks" && (len(s.Args) != 1 || s.Args[0] != "version") {
				t.Errorf("gitleaks Args = %v, want [version]", s.Args)
			}
		}
		if !found {
			t.Errorf("toolSpecs has no %s row", name)
		}
	}
	for _, s := range toolSpecs {
		if s.Name == "session-manager-plugin" && (s.VerRe == nil || s.VerRe("1.2.835.0") != "1.2.835.0") {
			t.Error("session-manager-plugin row must carry a VerRe that keeps all four version parts")
		}
	}
	if v := extractVer("8.30.1"); v != "8.30.1" {
		t.Errorf("extractVer(gitleaks output) = %q", v)
	}
	if v := fullDottedVersion("1.2.835.0"); v != "1.2.835.0" {
		t.Errorf("extractVer(session-manager-plugin output) = %q", v)
	}
}

func TestVersionsJSONPositionsAcceptsBraces(t *testing.T) {
	df := "printf '{\"a\":\"%s\",\"b\":\"%s\"}\\n' \\\n  \"${X_VERSION}\" \"$Y_VERSION\" \\\n  > /x/versions.json\n"
	got := versionsJSONPositions(t, df)
	if got["X_VERSION"] != "a" || got["Y_VERSION"] != "b" {
		t.Errorf("positions = %v", got)
	}
}
