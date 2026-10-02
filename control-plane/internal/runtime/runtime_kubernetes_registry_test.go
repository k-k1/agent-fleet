package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestParseImageReference(t *testing.T) {
	d := "sha256:" + strings.Repeat("a", 64)
	for _, c := range []struct {
		in                       string
		host, repo, ref, written string
	}{
		{"ubuntu", "registry-1.docker.io", "library/ubuntu", "latest", "ubuntu:latest"},
		{"agentfleet/workspace:1.2", "registry-1.docker.io", "agentfleet/workspace", "1.2", "agentfleet/workspace:1.2"},
		{"us-docker.pkg.dev/p/r/workspace:dev", "us-docker.pkg.dev", "p/r/workspace", "dev", "us-docker.pkg.dev/p/r/workspace:dev"},
		{"localhost:5000/ws:t", "localhost:5000", "ws", "t", "localhost:5000/ws:t"},
		{"reg.example:5000/a/b", "reg.example:5000", "a/b", "latest", "reg.example:5000/a/b:latest"},
		{"reg.example/a/b:t@" + d, "reg.example", "a/b", d, "reg.example/a/b:t"},
	} {
		r, err := parseImageReference(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if r.host != c.host || r.repo != c.repo || r.ref != c.ref || r.written != c.written {
			t.Errorf("%s = %+v", c.in, r)
		}
	}
	for _, bad := range []string{"", "a@sha256:12", "a@md5:" + strings.Repeat("a", 64)} {
		if _, err := parseImageReference(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// fakeRegistry answers the manifest of one tag behind a Bearer challenge whose token
// endpoint is on the same host, and records what each request carried.
type fakeRegistry struct {
	srv         *httptest.Server
	manifest    string
	sendDigest  bool
	needCreds   bool
	tokenAuth   []string
	manifestReq []string
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	fr := &fakeRegistry{manifest: `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`, sendDigest: true}
	fr.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			fr.tokenAuth = append(fr.tokenAuth, r.Header.Get("Authorization"))
			if fr.needCreds && r.Header.Get("Authorization") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("scope") != "repository:team/ws:pull" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"token":"reg-token"}`))
		case r.URL.Path == "/v2/team/ws/manifests/1.0":
			fr.manifestReq = append(fr.manifestReq, r.Method+" "+r.Header.Get("Authorization"))
			if r.Header.Get("Authorization") != "Bearer reg-token" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+fr.srv.URL+`/token",service="reg",scope="repository:team/ws:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if !strings.Contains(r.Header.Get("Accept"), "image.index") {
				w.WriteHeader(http.StatusNotAcceptable)
				return
			}
			if fr.sendDigest {
				sum := sha256.Sum256([]byte(fr.manifest))
				w.Header().Set("Docker-Content-Digest", "sha256:"+hex.EncodeToString(sum[:]))
			}
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(fr.manifest))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fr.srv.Close)
	return fr
}

func (fr *fakeRegistry) host() string { return strings.TrimPrefix(fr.srv.URL, "https://") }

func (fr *fakeRegistry) client(creds registryCreds) *registryClient {
	rc := newRegistryClient(creds)
	rc.hc.Transport = fr.srv.Client().Transport // the redirect policy stays the product's
	rc.metadataToken = nil
	return rc
}

func manifestDigestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Start pins the tag's own digest: through the Bearer challenge, from the
// Docker-Content-Digest header of a HEAD, and from the body's hash when the registry
// sends no header.
func TestRegistryPinsTheTagDigest(t *testing.T) {
	fr := newFakeRegistry(t)
	ref := fr.host() + "/team/ws:1.0"
	want := ref + "@" + manifestDigestOf(fr.manifest)
	got, err := fr.client(nil).pin(context.Background(), ref)
	if err != nil || got != want {
		t.Fatalf("pin = %q, %v; want %q", got, err, want)
	}
	if fr.manifestReq[len(fr.manifestReq)-1] != "HEAD Bearer reg-token" {
		t.Fatalf("requests = %v; want the digest from a HEAD", fr.manifestReq)
	}
	fr.sendDigest = false
	got, err = fr.client(nil).pin(context.Background(), ref)
	if err != nil || got != want {
		t.Fatalf("pin without the header = %q, %v; want %q", got, err, want)
	}
	// A reference that already carries a digest is used as written, with no request.
	before := len(fr.manifestReq)
	pinned := fr.host() + "/team/ws@sha256:" + strings.Repeat("c", 64)
	if got, err := fr.client(nil).pin(context.Background(), pinned); err != nil || got != pinned {
		t.Fatalf("pin(digest) = %q, %v", got, err)
	}
	if len(fr.manifestReq) != before {
		t.Fatal("a pinned reference went to the registry")
	}
	// A registry that cannot be read is a Start error naming the escape hatch.
	_, err = fr.client(nil).pin(context.Background(), fr.host()+"/team/ws:missing")
	if err == nil || !strings.Contains(err.Error(), "@sha256") {
		t.Fatalf("unreadable tag = %v", err)
	}
}

// The pull secret's credentials go to the token endpoint of the same registry.
func TestRegistryUsesThePullSecret(t *testing.T) {
	fr := newFakeRegistry(t)
	fr.needCreds = true
	creds := func(_ context.Context, host string) (string, string, bool) {
		if host == fr.host() {
			return "robot", "pw", true
		}
		return "", "", false
	}
	if _, err := fr.client(creds).pin(context.Background(), fr.host()+"/team/ws:1.0"); err != nil {
		t.Fatal(err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("robot:pw"))
	if fr.tokenAuth[len(fr.tokenAuth)-1] != want {
		t.Fatalf("token request auth = %v", fr.tokenAuth)
	}
	if _, err := fr.client(nil).pin(context.Background(), fr.host()+"/team/ws:1.0"); err == nil {
		t.Fatal("pinned without the credentials the registry requires")
	}
}

func TestRegistryCredentialsStayWithTheirHost(t *testing.T) {
	for _, c := range []struct {
		realm, registry string
		ok              bool
	}{
		{"https://us-docker.pkg.dev/v2/token", "us-docker.pkg.dev", true},
		{"https://auth.docker.io/token", "registry-1.docker.io", true},
		{"https://reg.example:443/token", "reg.example", true},
		{"https://reg.example:5000/token", "reg.example:5000", true},
		{"https://evil.corp.example/token", "reg.corp.example", false}, // a sibling
		{"https://corp.example/token", "reg.corp.example", false},      // the parent
		{"https://reg.example:5001/token", "reg.example:5000", false},  // another port
		{"https://reg.example/token", "reg.example:5000", false},
		{"http://reg.example/token", "reg.example", false}, // not https
		{"https://auth.attacker.io/token", "registry-1.docker.io", false},
		{"https://auth.docker.io/token", "reg.example", false}, // the exception is Docker Hub's only
	} {
		u, _ := url.Parse(c.realm)
		if got := trustedTokenRealm(u, c.registry); got != c.ok {
			t.Errorf("trustedTokenRealm(%s, %s) = %v, want %v", c.realm, c.registry, got, c.ok)
		}
	}
	// The Workload Identity token is only for Google's registries.
	rc := newRegistryClient(nil)
	rc.metadataToken = func(context.Context) (string, error) { return "gcp-token", nil }
	if _, _, ok := rc.credentialsFor(context.Background(), "registry-1.docker.io"); ok {
		t.Fatal("the Workload Identity token was offered to Docker Hub")
	}
	if u, p, ok := rc.credentialsFor(context.Background(), "europe-west1-docker.pkg.dev"); !ok || u != "oauth2accesstoken" || p != "gcp-token" {
		t.Fatalf("Artifact Registry credentials = %q %q %v", u, p, ok)
	}
}

func TestDockerConfigCreds(t *testing.T) {
	auth := base64.StdEncoding.EncodeToString([]byte("u1:p:1"))
	raw := []byte(`{"auths":{"https://index.docker.io/v1/":{"auth":"` + auth + `"},"reg.example":{"username":"u2","password":"p2"}}}`)
	if u, p, ok := dockerConfigCreds(raw, "registry-1.docker.io"); !ok || u != "u1" || p != "p:1" {
		t.Fatalf("docker hub = %q %q %v", u, p, ok)
	}
	if u, p, ok := dockerConfigCreds(raw, "reg.example"); !ok || u != "u2" || p != "p2" {
		t.Fatalf("reg.example = %q %q %v", u, p, ok)
	}
	if _, _, ok := dockerConfigCreds(raw, "other.example"); ok {
		t.Fatal("credentials for a host the config does not name")
	}
}

func TestParseAuthChallenge(t *testing.T) {
	s, p := parseAuthChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/ubuntu:pull"`)
	if s != "bearer" || p["realm"] != "https://auth.docker.io/token" || p["service"] != "registry.docker.io" || p["scope"] != "repository:library/ubuntu:pull" {
		t.Fatalf("%s %v", s, p)
	}
	if s, _ := parseAuthChallenge(`Basic realm="x"`); s != "basic" {
		t.Fatal(s)
	}
}

// hostTransport answers requests by host and path without a network, and records
// what each one carried, so the tests can name any host — a sibling, a subdomain, a
// third party — and see exactly what reached it.
type hostTransport struct {
	mu     sync.Mutex
	seen   []string // "scheme://host/path auth"
	handle func(r *http.Request) *http.Response
}

func (h *hostTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	h.mu.Lock()
	h.seen = append(h.seen, r.URL.Scheme+"://"+r.URL.Host+r.URL.Path+" "+r.Header.Get("Authorization"))
	h.mu.Unlock()
	resp := h.handle(r)
	if resp == nil {
		return nil, errors.New("no route")
	}
	if resp.Body == nil {
		resp.Body = io.NopCloser(strings.NewReader(""))
	}
	resp.Request = r
	return resp, nil
}

func (h *hostTransport) authAt(prefix string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.seen {
		if strings.HasPrefix(s, prefix+" ") {
			return strings.TrimPrefix(s, prefix+" "), true
		}
	}
	return "", false
}

func reply(code int, hdr map[string]string, body string) *http.Response {
	h := http.Header{}
	for k, v := range hdr {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

// A Bearer challenge naming a sibling host under the registry's parent domain gets no
// credentials (the review's reproduction: reg.corp.example → evil.corp.example).
func TestRegistrySiblingRealmGetsNoCredentials(t *testing.T) {
	ht := &hostTransport{handle: func(r *http.Request) *http.Response {
		switch r.URL.Host {
		case "reg.corp.example":
			if r.Header.Get("Authorization") == "Bearer t" {
				return reply(200, map[string]string{"Docker-Content-Digest": "sha256:" + strings.Repeat("d", 64)}, "")
			}
			return reply(401, map[string]string{"WWW-Authenticate": `Bearer realm="https://evil.corp.example/token",service="s"`}, "")
		case "evil.corp.example":
			return reply(200, nil, `{"token":"t"}`)
		}
		return nil
	}}
	rc := newRegistryClient(func(context.Context, string) (string, string, bool) { return "robot", "pull-secret", true })
	rc.hc.Transport = ht
	rc.metadataToken = nil
	if _, err := rc.pin(context.Background(), "reg.corp.example/team/ws:1"); err != nil {
		t.Fatal(err)
	}
	if auth, ok := ht.authAt("https://evil.corp.example/token"); !ok || auth != "" {
		t.Fatalf("the sibling realm received %q (seen %v)", auth, ht.seen)
	}
}

// A redirect never carries the credentials to another origin, and never leaves https:
// for the manifest request and for the token exchange alike. The same-origin hop is the
// positive control — it keeps them.
func TestRegistryRedirectsKeepCredentialsHome(t *testing.T) {
	digest := map[string]string{"Docker-Content-Digest": "sha256:" + strings.Repeat("e", 64)}
	for _, stage := range []string{"manifest", "token"} {
		for _, c := range []struct {
			to       string
			reached  bool // the target is requested at all
			keepAuth bool
		}{
			{"https://reg.example/elsewhere", true, true},
			{"https://sub.reg.example/leak", true, false},
			{"https://reg.example:8443/leak", true, false},
			{"https://third.example/leak", true, false},
			{"http://reg.example/leak", false, false},
		} {
			t.Run(stage+" to "+c.to, func(t *testing.T) {
				target, _ := url.Parse(c.to)
				ht := &hostTransport{}
				ht.handle = func(r *http.Request) *http.Response {
					at := r.URL.Scheme + "://" + r.URL.Host + r.URL.Path
					auth := r.Header.Get("Authorization")
					switch {
					case at == target.String():
						if stage == "token" {
							return reply(200, nil, `{"token":"t"}`)
						}
						return reply(200, digest, "")
					case at == "https://reg.example/token":
						if stage == "token" && auth != "" {
							return reply(307, map[string]string{"Location": c.to}, "")
						}
						return reply(200, nil, `{"token":"t"}`)
					case at == "https://reg.example/v2/team/ws/manifests/1":
						if auth == "" {
							return reply(401, map[string]string{"WWW-Authenticate": `Bearer realm="https://reg.example/token"`}, "")
						}
						if stage == "manifest" {
							return reply(307, map[string]string{"Location": c.to}, "")
						}
						return reply(200, digest, "")
					}
					return nil
				}
				rc := newRegistryClient(func(context.Context, string) (string, string, bool) { return "robot", "pull-secret", true })
				rc.hc.Transport = ht
				rc.metadataToken = nil
				_, err := rc.pin(context.Background(), "reg.example/team/ws:1")
				auth, reached := ht.authAt(target.String())
				if reached != c.reached {
					t.Fatalf("target requested = %v, want %v (err %v, seen %v)", reached, c.reached, err, ht.seen)
				}
				if !c.reached {
					if err == nil {
						t.Fatal("a redirect off https did not fail the pin")
					}
					return
				}
				if (auth != "") != c.keepAuth {
					t.Fatalf("Authorization at %s = %q, want kept=%v", c.to, auth, c.keepAuth)
				}
			})
		}
	}
}
