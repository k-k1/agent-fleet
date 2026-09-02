// Package mcpreg is the MCP server registry (docs/log/48 + ADR0031): the definitions a
// user or tenant registers, composed into one effective list and handed to the
// assistant chat (per-invocation config) and to the interactive sessions (each CLI's
// native config file).
//
// This file holds the shared type and its validation. The type itself lives in
// internal/secrets because that IS the store for user-scope definitions (same as
// every other credential); mcpreg aliases it so callers read as registry code.
package mcpreg

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// ServerDef is one registered MCP server. Alias, not a new type: handlers decode
// straight into it and the user-scope store persists it verbatim.
type ServerDef = secrets.MCPServer

// Targets selects the consumers a definition is handed to.
type Targets = secrets.MCPTargets

// Origins. A definition's origin decides who may edit it: user rows are fully
// editable, tenant rows are read-only locally (only opt-out), builtin rows are code.
const (
	OriginUser    = "user"
	OriginTenant  = "tenant"
	OriginBuiltin = "builtin"
)

// Transports. v1 is stdio + Streamable HTTP only. The legacy HTTP+SSE transport is
// deprecated in the MCP spec and needs a separate two-channel client to even probe,
// so it is left out rather than half-supported (docs/log/48 §14).
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
)

// MaskedValue is what a secret value is replaced with on the wire. A PUT that sends
// it back unchanged keeps the stored value (see MergeSecrets).
const MaskedValue = "***"

// nameRe is the intersection of what the target CLIs accept as a server key. codex
// writes it as a TOML bare key, which is the narrowest, so: letters, digits, dash,
// underscore, starting with an alphanumeric.
var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,47}$`)

// reservedNames are the server names Agent Fleet itself occupies in the CLIs'
// configs. Letting a user register one of these would silently shadow the af tools.
var reservedNames = map[string]bool{"af": true, "agent-fleet": true}

var knownKinds = map[string]bool{
	session.KindClaude: true, session.KindCodex: true, session.KindOpencode: true,
	session.KindCursor: true, session.KindKiro: true, session.KindAgy: true,
	session.KindCopilot: true,
}

// ValidationError marks a refusal caused by the definition itself — the caller sent
// something unusable, so it maps to a 400. Typed rather than string-sniffed so a
// store/IO failure can never be misreported as the user's fault (and vice versa).
//
// Code is the stable machine code the handler puts on the wire; the Console resolves
// it through its "err.<code>" catalog and only falls back to Msg (a language-neutral
// developer string) when a code is unmapped. One code per REASON — reusing a single
// "invalid" for every rule is what docs/log/23 P0-3 set out to remove. 追加・改名時は
// console/src/lib/i18n/locales/{ja,en}/errors.ts の "err.<code>" も必ず同時に足すこと。
type ValidationError struct {
	Code string
	Msg  string
}

func (e *ValidationError) Error() string { return e.Msg }

// Validation codes. Prefixed so they never collide with another subsystem's codes.
const (
	CodeNameInvalid     = "mcp_name_invalid"
	CodeNameReserved    = "mcp_name_reserved"
	CodeTransport       = "mcp_transport_unsupported"
	CodeCommandRequired = "mcp_command_required"
	CodeStdioNoURL      = "mcp_stdio_no_url"
	CodeTenantStdio     = "mcp_tenant_stdio"
	CodeURLRequired     = "mcp_url_required"
	CodeURLInvalid      = "mcp_url_invalid"
	CodeURLScheme       = "mcp_url_scheme"
	CodeURLHost         = "mcp_url_host"
	CodeURLCredentials  = "mcp_url_credentials"
	CodeHTTPNoCommand   = "mcp_http_no_command"
	CodeEnvName         = "mcp_env_name_invalid"
	CodeHeaderName      = "mcp_header_name_invalid"
	CodeHeaderValue     = "mcp_header_value_invalid"
	CodeKindUnknown     = "mcp_kind_unknown"
	CodeTimeoutRange    = "mcp_timeout_range"
)

func invalid(code, format string, a ...any) error {
	return &ValidationError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// ErrTenantStdio is the refusal behind ADR0031 決定 2: a tenant-distributed stdio
// server would let an admin run an arbitrary command in every member's container.
var ErrTenantStdio error = &ValidationError{
	Code: CodeTenantStdio,
	Msg:  "tenant-distributed MCP servers cannot use stdio (remote only)",
}

// Validate checks a definition for internal consistency. It does NOT check
// cross-definition constraints (name collisions) — that is the store's job, since
// it needs the whole effective list.
func Validate(d ServerDef) error {
	if !nameRe.MatchString(d.Name) {
		return invalid(CodeNameInvalid, "name must be 1-48 chars of [a-zA-Z0-9_-] starting alphanumeric: %q", d.Name)
	}
	// The generated shape is reserved as well: af's own server rotates through it
	// (af_server_name.go), and a user row wearing that shape would either shadow af or
	// be swept as one of af's leftovers.
	if reservedNames[strings.ToLower(d.Name)] || afNameRE.MatchString(strings.ToLower(d.Name)) {
		return invalid(CodeNameReserved, "%q is a name reserved by Agent Fleet", d.Name)
	}
	switch d.Transport {
	case TransportStdio:
		if strings.TrimSpace(d.Command) == "" {
			return invalid(CodeCommandRequired, "a stdio server needs a command")
		}
		if d.URL != "" || len(d.Headers) > 0 {
			return invalid(CodeStdioNoURL, "a stdio server cannot carry a URL or headers")
		}
		if d.Origin == OriginTenant {
			return ErrTenantStdio
		}
	case TransportHTTP:
		if err := validateURL(d.URL); err != nil {
			return err
		}
		if d.Command != "" || len(d.Args) > 0 || len(d.Env) > 0 {
			return invalid(CodeHTTPNoCommand, "a remote server cannot carry a command, args or env")
		}
	default:
		return invalid(CodeTransport, "unsupported transport: %q (stdio / http)", d.Transport)
	}
	for k := range d.Env {
		if !envNameRe.MatchString(k) {
			return invalid(CodeEnvName, "invalid environment variable name: %q", k)
		}
	}
	for k := range d.Headers {
		if strings.TrimSpace(k) == "" || strings.ContainsAny(k, "\r\n:") {
			return invalid(CodeHeaderName, "invalid header name: %q", k)
		}
	}
	for _, v := range d.Headers {
		if strings.ContainsAny(v, "\r\n") {
			return invalid(CodeHeaderValue, "a header value cannot contain a newline")
		}
	}
	for _, k := range d.Kinds {
		if !knownKinds[k] {
			return invalid(CodeKindUnknown, "unknown agent kind: %q", k)
		}
	}
	if d.TimeoutMS != 0 && (d.TimeoutMS < 1000 || d.TimeoutMS > 120000) {
		return invalid(CodeTimeoutRange, "timeout must be between 1000 and 120000 ms: %d", d.TimeoutMS)
	}
	return nil
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateURL keeps a remote definition to an absolute http(s) URL. A URL with
// credentials in it is refused — it would land in every materialized config file in
// plain sight, where the masking contract can't reach it.
func validateURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return invalid(CodeURLRequired, "a remote server needs a URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return invalid(CodeURLInvalid, "cannot parse URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return invalid(CodeURLScheme, "URL must be http / https: %q", raw)
	}
	if u.Host == "" {
		return invalid(CodeURLHost, "URL has no host: %q", raw)
	}
	if u.User != nil {
		return invalid(CodeURLCredentials, "do not embed credentials in the URL (use a header)")
	}
	return nil
}

// AppliesTo reports whether d should be handed to a session of the given agent kind.
// An empty Kinds list means every kind.
func AppliesTo(d ServerDef, kind string) bool {
	return d.Enabled && d.Targets.Session && KindAllowed(d, kind)
}

// KindAllowed reports whether d is scoped to the given agent kind. An empty Kinds
// list means every kind. It gates BOTH consumers: an assistant runs on one backend
// CLI, so a definition scoped away from that CLI is no more usable in the chat than
// it is in an interactive session of the same kind.
func KindAllowed(d ServerDef, kind string) bool {
	if len(d.Kinds) == 0 {
		return true
	}
	for _, k := range d.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// HasSecrets reports whether a definition carries any secret VALUE. Callers use it to
// refuse a fallback path that would put the definition somewhere weaker than a 0600
// file (docs/log/48 §5.1) — a definition with no secrets has nothing to protect.
func HasSecrets(d ServerDef) bool {
	for _, v := range d.Env {
		if v != "" {
			return true
		}
	}
	for _, v := range d.Headers {
		if v != "" {
			return true
		}
	}
	return false
}

// Masked returns a copy safe to send to the Console: every secret VALUE becomes
// MaskedValue while the keys stay visible (the user needs to see which headers and
// env vars a server carries).
func Masked(d ServerDef) ServerDef {
	out := d
	out.Env = maskMap(d.Env)
	out.Headers = maskMap(d.Headers)
	return out
}

// maskMap replaces every value with MaskedValue — EXCEPT an empty one, which stays
// empty. An absent value is not a secret being withheld, it is a value nobody has
// entered yet (a tenant user_secret definition arrives exactly like this), and masking it
// would tell the Console a credential is stored when none is, leaving the member no way
// to see that they are the ones who have to supply it.
func maskMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if v == "" {
			out[k] = ""
			continue
		}
		out[k] = MaskedValue
	}
	return out
}

// MergeSecrets resolves an incoming (masked) definition against the stored one: any
// value still equal to MaskedValue keeps the stored value, everything else is taken
// as a new secret. A key absent from the incoming map is a deletion.
func MergeSecrets(incoming, stored ServerDef) ServerDef {
	out := incoming
	out.Env = mergeMap(incoming.Env, stored.Env)
	out.Headers = mergeMap(incoming.Headers, stored.Headers)
	return out
}

func mergeMap(incoming, stored map[string]string) map[string]string {
	if len(incoming) == 0 {
		return nil
	}
	out := make(map[string]string, len(incoming))
	for k, v := range incoming {
		if v == MaskedValue {
			if old, ok := stored[k]; ok {
				out[k] = old
				continue
			}
			// Masked with nothing behind it: drop rather than store a literal "***",
			// which would otherwise be sent to the server as a real credential.
			continue
		}
		out[k] = v
	}
	return out
}

// Ready reports whether a definition has everything it needs to actually start.
// A tenant definition marked user_secret arrives with header NAMES but no values;
// until the member fills them in, materializing it would only produce a server that
// fails to authenticate — so it is held back instead (docs/log/48 §5.2).
func Ready(d ServerDef) bool {
	if !d.Enabled {
		return false
	}
	switch d.Transport {
	case TransportStdio:
		return strings.TrimSpace(d.Command) != ""
	case TransportHTTP:
		if strings.TrimSpace(d.URL) == "" {
			return false
		}
		for _, v := range d.Headers {
			if strings.TrimSpace(v) == "" || v == MaskedValue {
				return false
			}
		}
		return true
	}
	return false
}
