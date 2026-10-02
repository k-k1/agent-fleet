// runtime_kubernetes_registry.go — resolving the workspace image tag to a digest over
// the registry's v2 API, so that Start can pin it (ADR 0106 decision 9). A node's image
// cache can then never run an older image under the same tag, and the template records
// what each start launched.
//
// The CP reads the registry as itself, not as the node: with the deployment's pull
// secret when one is configured, with the pod's Workload Identity token for a Google
// registry, and anonymously otherwise. A credential is only ever sent to the host it
// belongs to.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// registryManifestAccept lists the four manifest kinds, index first. Without an Accept
// header a registry may answer with a schema-1 conversion whose digest names nothing the
// node pulls; with it, the reply's digest is the tag's own top-level object, which is
// what a node resolves the tag to as well.
var registryManifestAccept = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// imageRef is a parsed image reference.
type imageRef struct {
	written string // the reference as configured, without a digest
	host    string // registry host as the v2 API is reached (docker.io → registry-1.docker.io)
	repo    string // repository path (docker.io's single-name images get library/)
	ref     string // tag, or the digest when the reference carried one
	digest  string // sha256:… when the reference already pins one
}

// parseImageReference splits an image reference the way the container runtimes do: the
// first path element is a registry host only when it has a dot or a port or is
// localhost; otherwise the image is on Docker Hub.
func parseImageReference(s string) (imageRef, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return imageRef{}, errors.New("empty image reference")
	}
	r := imageRef{}
	name := s
	if i := strings.Index(name, "@"); i >= 0 {
		r.digest = name[i+1:]
		name = name[:i]
		if !strings.HasPrefix(r.digest, "sha256:") || len(r.digest) != len("sha256:")+64 {
			return imageRef{}, fmt.Errorf("image %q: malformed digest", s)
		}
	}
	r.written = name
	tag := ""
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		tag = name[i+1:]
		name = name[:i]
	}
	host, repo := "docker.io", name
	if i := strings.Index(name, "/"); i >= 0 {
		first := name[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			host, repo = first, name[i+1:]
		}
	}
	if host == "docker.io" {
		host = "registry-1.docker.io"
		if !strings.Contains(repo, "/") {
			repo = "library/" + repo
		}
	}
	if repo == "" {
		return imageRef{}, fmt.Errorf("image %q: no repository", s)
	}
	r.host, r.repo = host, repo
	switch {
	case r.digest != "":
		r.ref = r.digest
	case tag != "":
		r.ref = tag
	default:
		r.ref = "latest"
		r.written = name + ":latest"
	}
	return r, nil
}

// registryCreds returns a username and password for a registry host, or ok=false.
type registryCreds func(ctx context.Context, host string) (user, pass string, ok bool)

type registryClient struct {
	hc    *http.Client
	creds registryCreds
	// metadataToken returns a Workload Identity access token; nil = never. Used only for
	// Google's registries (googleRegistryHost), never sent anywhere else.
	metadataToken func(ctx context.Context) (string, error)
}

func newRegistryClient(creds registryCreds) *registryClient {
	return &registryClient{
		hc:            &http.Client{Timeout: 20 * time.Second, CheckRedirect: registryRedirectPolicy},
		creds:         creds,
		metadataToken: gkeMetadataToken,
	}
}

// pin returns the reference with its digest: `name:tag@sha256:…`. A reference that
// already carries a digest is returned as it is, without a registry call — the escape
// hatch for a registry the CP cannot read.
func (rc *registryClient) pin(ctx context.Context, image string) (string, error) {
	r, err := parseImageReference(image)
	if err != nil {
		return "", err
	}
	if r.digest != "" {
		return image, nil
	}
	d, err := rc.manifestDigest(ctx, r)
	if err != nil {
		return "", fmt.Errorf("resolve %s to a digest: %w (set the image to a name@sha256:… reference to pin it by hand)", image, err)
	}
	return r.written + "@" + d, nil
}

// manifestDigest asks for the tag's manifest and returns its digest: the
// Docker-Content-Digest header when the registry sends one, otherwise the sha256 of the
// body, which is how the digest is defined.
func (rc *registryClient) manifestDigest(ctx context.Context, r imageRef) (string, error) {
	u := "https://" + r.host + "/v2/" + r.repo + "/manifests/" + r.ref
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		resp, body, err := rc.fetch(ctx, method, u, r.host)
		if err != nil {
			return "", err
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("registry %s %s: %s", method, u, resp.Status)
		}
		if d := resp.Header.Get("Docker-Content-Digest"); strings.HasPrefix(d, "sha256:") {
			return d, nil
		}
		if method == http.MethodGet {
			sum := sha256.Sum256(body)
			return "sha256:" + hex.EncodeToString(sum[:]), nil
		}
	}
	return "", errors.New("unreachable")
}

// fetch sends one manifest request, answering a 401 challenge once: Bearer through the
// realm the registry names, Basic directly.
func (rc *registryClient) fetch(ctx context.Context, method, u, host string) (*http.Response, []byte, error) {
	send := func(auth string) (*http.Response, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, u, nil)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Accept", registryManifestAccept)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := rc.hc.Do(req)
		if err != nil {
			return nil, nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return resp, body, err
	}
	resp, body, err := send("")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, body, err
	}
	scheme, params := parseAuthChallenge(resp.Header.Get("WWW-Authenticate"))
	user, pass, haveCreds := rc.credentialsFor(ctx, host)
	switch scheme {
	case "basic":
		if !haveCreds {
			return resp, body, nil
		}
		return send("Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	case "bearer":
		tok, err := rc.bearerToken(ctx, params, host, user, pass, haveCreds)
		if err != nil {
			return nil, nil, err
		}
		return send("Bearer " + tok)
	}
	return resp, body, nil
}

// credentialsFor picks the identity for host: the pull secret's entry first, then the
// Workload Identity token for a Google registry.
func (rc *registryClient) credentialsFor(ctx context.Context, host string) (string, string, bool) {
	if rc.creds != nil {
		if u, p, ok := rc.creds(ctx, host); ok {
			return u, p, true
		}
	}
	if rc.metadataToken != nil && googleRegistryHost(host) {
		if tok, err := rc.metadataToken(ctx); err == nil && tok != "" {
			return "oauth2accesstoken", tok, true
		}
	}
	return "", "", false
}

// bearerToken runs the token exchange of the registry's Bearer challenge. Credentials
// go to the realm only when it is the registry's own origin or one of
// registryTokenRealms: a challenge naming any other host, a sibling under the same
// parent domain included, gets an anonymous request.
func (rc *registryClient) bearerToken(ctx context.Context, params map[string]string, host, user, pass string, haveCreds bool) (string, error) {
	realm := params["realm"]
	ru, err := url.Parse(realm)
	if err != nil || ru.Scheme != "https" {
		return "", fmt.Errorf("registry %s: unusable token realm %q", host, realm)
	}
	q := ru.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	if s := params["scope"]; s != "" {
		q.Set("scope", s)
	}
	ru.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ru.String(), nil)
	if err != nil {
		return "", err
	}
	if haveCreds && trustedTokenRealm(ru, host) {
		req.SetBasicAuth(user, pass)
	}
	resp, err := rc.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry token %s: %s", ru.Host, resp.Status)
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil {
		return "", fmt.Errorf("registry token %s: %w", ru.Host, err)
	}
	if t.Token != "" {
		return t.Token, nil
	}
	if t.AccessToken != "" {
		return t.AccessToken, nil
	}
	return "", fmt.Errorf("registry token %s: empty token", ru.Host)
}

// registryTokenRealms are the token services that live on another origin than their
// registry and may still receive its credentials. Anything else must be same-origin.
var registryTokenRealms = map[string]string{
	"registry-1.docker.io:443": "auth.docker.io:443",
}

// originOf is host:port with the scheme's default port filled in, so that
// reg.example and reg.example:443 compare equal and reg.example:5000 does not.
func originOf(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	return strings.ToLower(u.Hostname()) + ":" + port
}

// trustedTokenRealm reports whether a token realm may receive the credentials meant
// for registryHost (host or host:port, reached over https).
func trustedTokenRealm(realm *url.URL, registryHost string) bool {
	if realm.Scheme != "https" {
		return false
	}
	reg := originOf(&url.URL{Scheme: "https", Host: registryHost})
	got := originOf(realm)
	return got == reg || registryTokenRealms[reg] == got
}

// registryRedirectPolicy keeps a redirect from carrying credentials anywhere else.
// Go's own policy keeps the Authorization header for a subdomain of the first host,
// and follows https to http; here a hop off https is refused outright, and a hop to
// any other origin than the request's first one goes without the header. Registries
// do redirect, to blob stores and CDNs, so a cross-origin hop itself is allowed.
func registryRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("registry: too many redirects")
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("registry: refusing a redirect off https to %s", req.URL.Redacted())
	}
	if originOf(req.URL) != originOf(via[0].URL) {
		req.Header.Del("Authorization")
		req.URL.User = nil
	}
	return nil
}

// parseAuthChallenge reads `Bearer realm="…",service="…",scope="…"`.
func parseAuthChallenge(h string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params := map[string]string{}
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		k, after, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		var v string
		if strings.HasPrefix(after, `"`) {
			end := strings.Index(after[1:], `"`)
			if end < 0 {
				v, rest = after[1:], ""
			} else {
				v, rest = after[1:1+end], after[2+end:]
			}
		} else {
			v, rest, _ = strings.Cut(after, ",")
		}
		params[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return strings.ToLower(scheme), params
}

// googleRegistryHost is where the Workload Identity token may go: Artifact Registry
// and Container Registry.
func googleRegistryHost(host string) bool {
	host = strings.Split(host, ":")[0]
	return strings.HasSuffix(host, "-docker.pkg.dev") || host == "gcr.io" || strings.HasSuffix(host, ".gcr.io")
}

// gkeMetadataToken asks the GKE metadata server for the pod's Workload Identity access
// token. Off GKE the name does not resolve and this fails fast; the caller then reads
// the registry anonymously.
func gkeMetadataToken(ctx context.Context) (string, error) {
	host := envOr("GCE_METADATA_HOST", "metadata.google.internal")
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+host+"/computeMetadata/v1/instance/service-accounts/default/token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata token: %s", resp.Status)
	}
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

// pullSecretCreds reads the deployment's image pull secret (a
// kubernetes.io/dockerconfigjson Secret in the workspace namespace) at each call, so a
// rotated secret is picked up without a CP restart.
func pullSecretCreds(c *kubeClient, namespace, name string) registryCreds {
	if name == "" {
		return nil
	}
	return func(ctx context.Context, host string) (string, string, bool) {
		var s kSecret
		if err := c.get(ctx, "/api/v1/namespaces/"+namespace+"/secrets/"+name, &s); err != nil {
			return "", "", false
		}
		return dockerConfigCreds(s.Data[".dockerconfigjson"], host)
	}
}

// dockerConfigCreds finds host's entry in a docker config.json. Keys may be bare hosts or
// URLs; Docker Hub's entry is conventionally keyed https://index.docker.io/v1/.
func dockerConfigCreds(raw []byte, host string) (string, string, bool) {
	var cfg struct {
		Auths map[string]struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Auth     string `json:"auth"`
		} `json:"auths"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return "", "", false
	}
	want := map[string]bool{host: true}
	if host == "registry-1.docker.io" {
		want["docker.io"], want["index.docker.io"] = true, true
	}
	for k, a := range cfg.Auths {
		h := k
		if u, err := url.Parse(k); err == nil && u.Host != "" {
			h = u.Host
		}
		if !want[h] {
			continue
		}
		if a.Username != "" || a.Password != "" {
			return a.Username, a.Password, true
		}
		if b, err := base64.StdEncoding.DecodeString(a.Auth); err == nil {
			if u, p, ok := strings.Cut(string(b), ":"); ok {
				return u, p, true
			}
		}
	}
	return "", "", false
}
