package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
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
	rc.hc = fr.srv.Client()
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
		{"us-docker.pkg.dev", "us-docker.pkg.dev", true},
		{"auth.docker.io", "registry-1.docker.io", true},
		{"reg.example", "reg.example:5000", true},
		{"evil.example", "reg.corp.example", false},
		{"auth.attacker.io", "registry-1.docker.io", false},
	} {
		if got := sameRegistrySite(c.realm, c.registry); got != c.ok {
			t.Errorf("sameRegistrySite(%s, %s) = %v, want %v", c.realm, c.registry, got, c.ok)
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
