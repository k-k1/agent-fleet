package mcpreg

// OAuth discovery for the connection test (ADR 0119 decisions 2, 3 and 8). A 401 from the
// server is not always "wrong credentials": hosted MCP servers answer it to every client
// that has not signed in. The probe therefore reads what the server offers and reports it,
// without signing in. The order and the refusals follow the MCP authorization specification:
//
//   - the Protected Resource Metadata (RFC 9728) comes from the challenge's resource_metadata,
//     else from the well-known URL with the path, then the root;
//   - its `resource` must be the registered URL in canonical form, otherwise the document is
//     about some other resource and nothing in it is used;
//   - the authorization server's metadata is fetched in the fixed order (RFC 8414 path
//     insertion, OIDC path insertion, OIDC path appending) and a document whose `issuer`
//     differs byte-for-byte from the issuer the URL was built from is refused.
//
// Every metadata request is a bare GET: the definition's static headers go to the MCP server
// only, never to a host the server's own documents point at.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// maxMetadataBody bounds one metadata document. Real ones are a few KB; an endless body
	// from a hostile host would otherwise sit in memory for the whole probe.
	maxMetadataBody = 256 << 10
	// metadataTimeout bounds one metadata request, inside the probe's overall deadline, so a
	// single black-holed host cannot eat the whole budget before the others are tried.
	metadataTimeout = 4 * time.Second
	maxMetadataHops = 3 // same-origin redirects followed for one metadata URL
	// maxAuthServers caps how many entries of `authorization_servers` are examined.
	maxAuthServers = 3
	maxListEntries = 32 // scopes reported per list
)

// Problem codes of an OAuth report. They are the wire contract with the Console, which maps
// each one to its own text; add, never rename.
const (
	OAuthProblemPRMUnreachable   = "prm_unreachable"
	OAuthProblemPRMInvalid       = "prm_invalid"
	OAuthProblemPRMResource      = "prm_resource_mismatch"
	OAuthProblemNoAuthServers    = "prm_no_authorization_servers"
	OAuthProblemASIssuerUnusable = "as_issuer_unusable"
	OAuthProblemASUnreachable    = "as_unreachable"
	OAuthProblemASInvalid        = "as_invalid"
	OAuthProblemASIssuerMismatch = "as_issuer_mismatch"
	OAuthProblemASNoS256         = "as_no_s256"
)

// Client registration paths an authorization server advertises (ADR 0119 decision 3).
const (
	OAuthRegCIMD = "cimd"
	OAuthRegDCR  = "dcr"
)

// OAuthReport is what the connection test learned from an OAuth-protected server.
type OAuthReport struct {
	// Required is true whenever the server was recognised as OAuth-protected, even when the
	// discovery then failed: Problems say how far it got.
	Required    bool   `json:"required"`
	Resource    string `json:"resource,omitempty"`    // the registered URL in canonical form
	MetadataURL string `json:"metadataUrl,omitempty"` // where the protected-resource metadata came from
	// ChallengedScope is the `scope` of the 401 challenge, which is authoritative for the
	// first authorization request.
	ChallengedScope string `json:"challengedScope,omitempty"`
	// ScopesSupported is the protected-resource metadata's `scopes_supported`.
	ScopesSupported []string `json:"scopesSupported,omitempty"`
	// Servers lists the authorization servers examined, in the metadata's order.
	Servers []OAuthServer `json:"servers,omitempty"`
	// Selected is the issuer of the first server that validated; empty when none did.
	Selected string         `json:"selected,omitempty"`
	Problems []OAuthProblem `json:"problems,omitempty"`
}

// OAuthServer is one authorization server's validated offer.
type OAuthServer struct {
	Issuer      string `json:"issuer"`
	MetadataURL string `json:"metadataUrl,omitempty"`
	// Valid means the document was fetched, its issuer matched and it offers S256.
	Valid bool `json:"valid"`
	// Registration lists the client registration paths on offer: "cimd" (client ID metadata
	// documents), "dcr" (a registration endpoint). Empty means neither.
	Registration []string `json:"registration"`
	S256         bool     `json:"s256"`
	// IssParam is `authorization_response_iss_parameter_supported` (RFC 9207).
	IssParam        bool     `json:"issParam"`
	ScopesSupported []string `json:"scopesSupported,omitempty"`
}

// OAuthProblem is one validation failure, identified by a stable Code.
type OAuthProblem struct {
	Code   string `json:"code"`
	Issuer string `json:"issuer,omitempty"` // the authorization server it concerns, if any
	Detail string `json:"detail,omitempty"`
}

// bearerChallenge is one `Bearer` challenge of a WWW-Authenticate header.
type bearerChallenge struct {
	resourceMetadata string
	scope            string
}

// parseBearerChallenges extracts every Bearer challenge from the WWW-Authenticate header
// values. It tolerates what servers really send: several challenges in one header or in
// several headers, any case for the scheme and parameter names, quoted or bare values, and
// stray whitespace and commas. Other schemes are skipped over.
func parseBearerChallenges(values []string) []bearerChallenge {
	var out []bearerChallenge
	for _, v := range values {
		out = append(out, parseChallengeLine(v)...)
	}
	return out
}

func isTokenChar(c byte) bool {
	switch {
	case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

func parseChallengeLine(s string) []bearerChallenge {
	var out []bearerChallenge
	cur := -1 // index in out of the Bearer challenge being filled; -1 for another scheme
	i := 0
	skip := func() {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == ',') {
			i++
		}
	}
	token := func() string {
		st := i
		for i < len(s) && isTokenChar(s[i]) {
			i++
		}
		return s[st:i]
	}
	for {
		skip()
		if i >= len(s) {
			return out
		}
		word := token()
		if word == "" {
			i++ // a byte that belongs to nothing; step over it
			continue
		}
		j := i
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		if j < len(s) && s[j] == '=' {
			// word=value: a parameter of the current challenge. A token68 tail ("abc==")
			// lands here too and is harmless: the name matches nothing we read.
			i = j + 1
			for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
				i++
			}
			var val string
			if i < len(s) && s[i] == '"' {
				i++
				var b strings.Builder
				for i < len(s) && s[i] != '"' {
					if s[i] == '\\' && i+1 < len(s) {
						i++
					}
					b.WriteByte(s[i])
					i++
				}
				i++ // closing quote
				val = b.String()
			} else {
				st := i
				for i < len(s) && s[i] != ',' && s[i] != ' ' && s[i] != '\t' {
					i++
				}
				val = s[st:i]
			}
			if cur >= 0 {
				switch strings.ToLower(word) {
				case "resource_metadata":
					out[cur].resourceMetadata = val
				case "scope":
					out[cur].scope = val
				}
			}
			continue
		}
		// A bare word is a new scheme.
		if strings.EqualFold(word, "bearer") {
			out = append(out, bearerChallenge{})
			cur = len(out) - 1
		} else {
			cur = -1
		}
	}
}

// pickChallenge chooses the Bearer challenge to act on: the first that names a
// resource_metadata, else the first. Scope comes from the chosen one, falling back to any
// Bearer challenge that carries one.
func pickChallenge(cs []bearerChallenge) (c bearerChallenge, ok bool) {
	if len(cs) == 0 {
		return c, false
	}
	c = cs[0]
	for _, x := range cs {
		if x.resourceMetadata != "" {
			c = x
			break
		}
	}
	if c.scope == "" {
		for _, x := range cs {
			if x.scope != "" {
				c.scope = x.scope
				break
			}
		}
	}
	return c, true
}

// canonicalResource is the registered URL in the form RFC 8707 / RFC 9728 compare: lower-case
// scheme and host, default port dropped, no fragment. A bare "/" path is the root and is
// written with no path at all.
func canonicalResource(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	path := u.EscapedPath()
	if path == "/" {
		path = ""
	}
	out := strings.ToLower(u.Scheme) + "://" + host + path
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out, true
}

// resourceMatches reports whether a PRM `resource` is the registered URL. The comparison is
// exact against the canonical form; the one allowance is the root URL written with its "/".
func resourceMatches(prmResource, canonical string) bool {
	if prmResource == canonical {
		return true
	}
	u, err := url.Parse(canonical)
	return err == nil && u.Path == "" && u.RawQuery == "" && prmResource == canonical+"/"
}

// issuerUsable reports whether an issuer identifier may be used to build metadata URLs:
// https (plain http only for a loopback host, where tests and local servers live), a host,
// and no query or fragment (RFC 8414 §2).
func issuerUsable(issuer string) (*url.URL, bool) {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || strings.Contains(issuer, "#") {
		return nil, false
	}
	switch u.Scheme {
	case "https":
		return u, true
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return u, true
		}
	}
	return nil, false
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// prmCandidates are the protected-resource metadata URLs to try when the challenge named
// none (RFC 9728 §3): the well-known URL with the resource's path inserted, then the root.
func prmCandidates(resource string) []string {
	u, err := url.Parse(resource)
	if err != nil {
		return nil
	}
	origin := u.Scheme + "://" + u.Host
	const wk = "/.well-known/oauth-protected-resource"
	path := strings.TrimRight(u.EscapedPath(), "/")
	if path == "" {
		return []string{origin + wk}
	}
	return []string{origin + wk + path, origin + wk}
}

// asCandidates are the authorization-server metadata URLs for an issuer, in the order the
// MCP specification fixes. A trailing "/" of the issuer's path is dropped for building the
// URL only; the issuer itself is compared untouched.
func asCandidates(issuer *url.URL) []string {
	origin := issuer.Scheme + "://" + issuer.Host
	path := strings.TrimRight(issuer.EscapedPath(), "/")
	if path == "" {
		return []string{
			origin + "/.well-known/oauth-authorization-server",
			origin + "/.well-known/openid-configuration",
		}
	}
	return []string{
		origin + "/.well-known/oauth-authorization-server" + path,
		origin + "/.well-known/openid-configuration" + path,
		origin + path + "/.well-known/openid-configuration",
	}
}

var errMetadataStatus = errors.New("unexpected status")

// metadataGetter fetches metadata documents with a bare GET: no static headers, no cookies,
// the process's default transport (the same egress as the probe itself), and redirects only
// within the origin the request started at.
type metadataGetter struct{}

func (metadataGetter) get(ctx context.Context, raw string, out any) error {
	start, err := url.Parse(raw)
	if err != nil || (start.Scheme != "http" && start.Scheme != "https") || start.Host == "" {
		return fmt.Errorf("not an http(s) URL")
	}
	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > maxMetadataHops {
			return errors.New("too many redirects")
		}
		if r.URL.Scheme != start.Scheme || r.URL.Host != start.Host {
			return fmt.Errorf("redirect leaves %s://%s", start.Scheme, start.Host)
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: HTTP %d", errMetadataStatus, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxMetadataBody {
		return fmt.Errorf("document larger than %d bytes", maxMetadataBody)
	}
	return json.Unmarshal(body, out)
}

type protectedResourceMeta struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

type authServerMeta struct {
	Issuer                        string   `json:"issuer"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	CIMDSupported                 bool     `json:"client_id_metadata_document_supported"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
	IssParameterSupported         bool     `json:"authorization_response_iss_parameter_supported"`
	ScopesSupported               []string `json:"scopes_supported"`
}

func capList(l []string) []string {
	if len(l) > maxListEntries {
		l = l[:maxListEntries]
	}
	return l
}

// probeOAuth runs the discovery for a 401 answer from the server at serverURL. ok is false
// when the answer does not look like an OAuth challenge at all (no Bearer challenge naming
// metadata and no protected-resource metadata at the well-known URLs), in which case the
// caller reports the plain 401 as before.
func probeOAuth(ctx context.Context, serverURL string, header http.Header) (OAuthReport, bool) {
	canon, ok := canonicalResource(serverURL)
	if !ok {
		return OAuthReport{}, false
	}
	var g metadataGetter
	rep := OAuthReport{Resource: canon}
	ch, haveChallenge := pickChallenge(parseBearerChallenges(header.Values("WWW-Authenticate")))
	rep.ChallengedScope = ch.scope

	var prm protectedResourceMeta
	switch {
	case haveChallenge && ch.resourceMetadata != "":
		rep.Required = true
		rep.MetadataURL = ch.resourceMetadata
		if err := g.get(ctx, ch.resourceMetadata, &prm); err != nil {
			code := OAuthProblemPRMUnreachable
			if isDecodeErr(err) {
				code = OAuthProblemPRMInvalid
			}
			rep.Problems = append(rep.Problems, OAuthProblem{Code: code, Detail: err.Error()})
			return rep, true
		}
	default:
		found := false
		for _, cand := range prmCandidates(canon) {
			prm = protectedResourceMeta{}
			if g.get(ctx, cand, &prm) == nil && prm.Resource != "" {
				rep.MetadataURL, found = cand, true
				break
			}
		}
		if !found {
			return OAuthReport{}, false
		}
		rep.Required = true
	}

	if !resourceMatches(prm.Resource, canon) {
		rep.Problems = append(rep.Problems, OAuthProblem{
			Code:   OAuthProblemPRMResource,
			Detail: fmt.Sprintf("resource %q is not %q", prm.Resource, canon),
		})
		return rep, true
	}
	rep.ScopesSupported = capList(prm.ScopesSupported)
	if len(prm.AuthorizationServers) == 0 {
		rep.Problems = append(rep.Problems, OAuthProblem{Code: OAuthProblemNoAuthServers})
		return rep, true
	}
	for i, issuer := range prm.AuthorizationServers {
		if i >= maxAuthServers {
			break
		}
		srv, problems := discoverAuthServer(ctx, g, issuer)
		rep.Problems = append(rep.Problems, problems...)
		rep.Servers = append(rep.Servers, srv)
		if srv.Valid && rep.Selected == "" {
			rep.Selected = srv.Issuer
		}
	}
	return rep, true
}

func isDecodeErr(err error) bool {
	var se *json.SyntaxError
	var ue *json.UnmarshalTypeError
	return errors.As(err, &se) || errors.As(err, &ue)
}

// discoverAuthServer fetches and validates one authorization server's metadata. The first
// URL that yields a document decides: an issuer mismatch there is a refusal, not a cue to
// try the next URL.
func discoverAuthServer(ctx context.Context, g metadataGetter, issuer string) (OAuthServer, []OAuthProblem) {
	srv := OAuthServer{Issuer: issuer, Registration: []string{}}
	iu, ok := issuerUsable(issuer)
	if !ok {
		return srv, []OAuthProblem{{Code: OAuthProblemASIssuerUnusable, Issuer: issuer}}
	}
	var lastErr error
	for _, cand := range asCandidates(iu) {
		var m authServerMeta
		err := g.get(ctx, cand, &m)
		if err != nil {
			lastErr = err
			if isDecodeErr(err) {
				// A 200 that is not JSON: this URL does answer, with rubbish.
				return srv, []OAuthProblem{{Code: OAuthProblemASInvalid, Issuer: issuer, Detail: cand}}
			}
			continue
		}
		srv.MetadataURL = cand
		if m.Issuer != issuer {
			return srv, []OAuthProblem{{
				Code: OAuthProblemASIssuerMismatch, Issuer: issuer,
				Detail: fmt.Sprintf("metadata at %s says issuer %q", cand, m.Issuer),
			}}
		}
		if m.CIMDSupported {
			srv.Registration = append(srv.Registration, OAuthRegCIMD)
		}
		if m.RegistrationEndpoint != "" {
			srv.Registration = append(srv.Registration, OAuthRegDCR)
		}
		for _, method := range m.CodeChallengeMethodsSupported {
			if method == "S256" {
				srv.S256 = true
			}
		}
		srv.IssParam = m.IssParameterSupported
		srv.ScopesSupported = capList(m.ScopesSupported)
		if !srv.S256 {
			return srv, []OAuthProblem{{Code: OAuthProblemASNoS256, Issuer: issuer}}
		}
		srv.Valid = true
		return srv, nil
	}
	detail := ""
	if lastErr != nil {
		detail = lastErr.Error()
	}
	return srv, []OAuthProblem{{Code: OAuthProblemASUnreachable, Issuer: issuer, Detail: detail}}
}
