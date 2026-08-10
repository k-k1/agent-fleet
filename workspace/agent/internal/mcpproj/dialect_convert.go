package mcpproj

// dialect_convert.go — the write-side counterpart to dialect.go's detection:
// docs/56 §5 "マージは利用者が決める…ただし方言変換の候補は AF が計算して見せる" /
// §9.2's copy panel (as-is / translate / expand). AF never picks FOR the user
// (§5), but it computes what "translate" would produce so choosing it is not a
// manual rewrite.

import (
	"os"
	"regexp"
)

// CanTranslate reports whether kind has a native dialect to translate INTO. codex
// expands nothing (docs/56 §2.1) — the ONE combination docs/56 §9.2 says must not
// even offer the option, rather than offer it and silently fail.
func CanTranslate(kind string) bool {
	return nativeDialect(kind) != ""
}

// nativeDialect is the placeholder syntax kind's OWN writer would use — the
// TRANSLATE target, as opposed to dialectByKind's full read-side support set
// (cursor also READS dollar_env_brace, but nothing writes it natively).
func nativeDialect(kind string) string {
	switch kind {
	case "claude", "copilot", "cursor":
		return DialectDollarBrace
	case "opencode":
		return DialectEnvBrace
	default:
		return ""
	}
}

var (
	reDollarEnvBraceCapture = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
	reDollarBraceCapture    = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	reEnvBraceCapture       = regexp.MustCompile(`\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// rewritePlaceholders replaces every occurrence of the three placeholder forms in
// v with render(varName), most-specific pattern first (dollar_env_brace's match
// text is a superset of env_brace's — replacing it first removes those bytes
// before the env_brace pass runs, so it can never double-match the same text).
func rewritePlaceholders(v string, render func(name string) string) string {
	apply := func(re *regexp.Regexp, s string) string {
		return re.ReplaceAllStringFunc(s, func(m string) string {
			sub := re.FindStringSubmatch(m)
			return render(sub[1])
		})
	}
	v = apply(reDollarEnvBraceCapture, v)
	v = apply(reDollarBraceCapture, v)
	v = apply(reEnvBraceCapture, v)
	return v
}

// translateValue rewrites every placeholder reference in v into dst's own syntax.
func translateValue(v, dst string) string {
	var render func(string) string
	switch dst {
	case DialectDollarBrace:
		render = func(name string) string { return "${" + name + "}" }
	case DialectEnvBrace:
		render = func(name string) string { return "{env:" + name + "}" }
	default:
		return v // no native dialect to translate into — caller must gate via CanTranslate
	}
	return rewritePlaceholders(v, render)
}

// expandValue replaces every placeholder reference in v with ITS OWN resolved
// environment value (docs/56 §9.2's non-recommended "実値へ展開して書く" — bakes a
// host-specific value into a file that will be committed).
func expandValue(v string) string {
	return rewritePlaceholders(v, os.Getenv)
}

// convertServer returns a copy of s with every string field passed through mode's
// transform: "translate" (into dstKind's native dialect — a no-op if dstKind has
// none), "expand" (real environment values), or anything else ("as-is": returns s
// unchanged).
func convertServer(s Server, mode, dstKind string) Server {
	var f func(string) string
	switch mode {
	case "translate":
		dst := nativeDialect(dstKind)
		if dst == "" {
			return s
		}
		f = func(v string) string { return translateValue(v, dst) }
	case "expand":
		f = expandValue
	default:
		return s
	}
	out := s
	out.Command = f(s.Command)
	if len(s.Args) > 0 {
		out.Args = make([]string, len(s.Args))
		for i, a := range s.Args {
			out.Args[i] = f(a)
		}
	}
	out.URL = f(s.URL)
	if len(s.Env) > 0 {
		out.Env = make(map[string]string, len(s.Env))
		for k, v := range s.Env {
			out.Env[k] = f(v)
		}
	}
	if len(s.Headers) > 0 {
		out.Headers = make(map[string]string, len(s.Headers))
		for k, v := range s.Headers {
			out.Headers[k] = f(v)
		}
	}
	return out
}
