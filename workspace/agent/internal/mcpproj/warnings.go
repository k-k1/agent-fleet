package mcpproj

// Name and secret warnings (docs/log/56 §7.2 / §7.4). Dialect warnings live in
// dialect.go; cross-file divergence in diff.go.

import (
	"math"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// nameWarnings flags a hijack (af / af_xxxxxxxx — docs/log/48 §8.4) or a name outside
// the shared CLI charset (docs/log/56 §7.4). Reuses mcpreg's own recognition
// (IsReservedName / IsValidServerName) so this can never drift from what
// af_server_name.go actually rotates through.
func nameWarnings(name, file string) []Warning {
	if mcpreg.IsReservedName(name) {
		return []Warning{{Severity: "red", Code: CodeNameHijack, File: file, Server: name}}
	}
	if !mcpreg.IsValidServerName(name) {
		return []Warning{{Severity: "yellow", Code: CodeNameInvalid, File: file, Server: name}}
	}
	return nil
}

// secretKeyRE matches an env/header key name that looks like it carries a secret
// (docs/log/56 §7.2's list).
var secretKeyRE = regexp.MustCompile(`(?i)token|key|secret|password|passwd|credential|authorization`)

// looksSecretValue flags a value as secret-shaped by simple Shannon entropy —
// docs/log/56 §7.2's "high-entropy value". Deliberately conservative (long, no
// whitespace, entropy above a plain-English/path threshold) since a false negative
// here just means one fewer warning, not a false sense of safety (the key-name
// check above catches the common case).
func looksSecretValue(v string) bool {
	if len(v) < 12 || strings.ContainsAny(v, " \t\n") {
		return false
	}
	return shannonEntropy(v) >= 3.5
}

func shannonEntropy(s string) float64 {
	counts := map[rune]int{}
	for _, r := range s {
		counts[r]++
	}
	n := float64(len(s))
	var h float64
	for _, c := range counts {
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}

// secretWarnings flags an env/header entry that already looks like a credential AND
// sits in a file whose tracked state means it either IS committed (yellow — docs/log/56
// §7.2's tracked→tracked row) or CANNOT be determined (yellow, svn/no-VCS — docs/log/57
// charter 6). An untracked/ignored file is not warned about here: nothing has reached
// git yet, which is the safe state this whole check exists to preserve.
func secretWarnings(s Server, file string, tracked, trackedUncertain bool) []Warning {
	if !tracked && !trackedUncertain {
		return nil
	}
	var out []Warning
	check := func(m map[string]string) {
		for k, v := range m {
			if v == "" {
				continue
			}
			if !secretKeyRE.MatchString(k) && !looksSecretValue(v) {
				continue
			}
			code := CodeSecretTracked
			if trackedUncertain {
				code = CodeSecretVCSUncertain
			}
			out = append(out, Warning{Severity: "yellow", Code: code, File: file, Server: s.Name, Key: k})
		}
	}
	check(s.Env)
	check(s.Headers)
	return out
}
