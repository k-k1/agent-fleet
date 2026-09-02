package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// versionFakeFactory is a RuntimeFactory that also declares a workspace image — the
// shape the ECS factories have (WorkspaceImage is an optional capability, not part of
// the interface).
type versionFakeFactory struct{ image string }

func (f *versionFakeFactory) New(runtime.Workspace, string, []string) Runtime { return nil }
func (f *versionFakeFactory) WorkspaceImage() string                          { return f.image }

// versionPlainFactory has no image to declare (docker / native).
type versionPlainFactory struct{}

func (f *versionPlainFactory) New(runtime.Workspace, string, []string) Runtime { return nil }

// resetCPImageCache clears the process-wide cache so each test probes for itself.
func resetCPImageCache(t *testing.T) {
	t.Helper()
	cpImageCache.mu.Lock()
	cpImageCache.info, cpImageCache.tried = nil, false
	cpImageCache.mu.Unlock()
	t.Cleanup(func() {
		cpImageCache.mu.Lock()
		cpImageCache.info, cpImageCache.tried = nil, false
		cpImageCache.mu.Unlock()
	})
}

func TestParseImageRef(t *testing.T) {
	cases := []struct {
		name, ref, digest string
		want              *imageInfo
	}{
		{
			name:   "ecr ref drops the registry host (and with it the AWS account id)",
			ref:    "123456789012.dkr.ecr.us-east-1.amazonaws.com/af-control-plane:0.6.0",
			digest: "sha256:abc",
			want:   &imageInfo{Repo: "af-control-plane", Tag: "0.6.0", Digest: "sha256:abc"},
		},
		{
			name: "digest reference carries its own content id",
			ref:  "123456789012.dkr.ecr.us-east-1.amazonaws.com/af-workspace@sha256:def",
			want: &imageInfo{Repo: "af-workspace", Digest: "sha256:def"},
		},
		{
			name: "bare name keeps its namespace: there is no registry host to strip",
			ref:  "agent-fleet/workspace:m3",
			want: &imageInfo{Repo: "agent-fleet/workspace", Tag: "m3"},
		},
		{
			name: "a registry PORT is not a tag",
			ref:  "localhost:5000/af-workspace",
			want: &imageInfo{Repo: "af-workspace"},
		},
		{name: "empty is unknown", ref: "  ", want: nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseImageRef(c.ref, c.digest)
			if c.want == nil {
				if got != nil {
					t.Fatalf("parseImageRef(%q) = %+v, want nil", c.ref, got)
				}
				return
			}
			if got == nil || *got != *c.want {
				t.Fatalf("parseImageRef(%q) = %+v, want %+v", c.ref, got, c.want)
			}
		})
	}
}

// The ECS deployment answers both questions: which version is running, and which image
// it (and the workspace) came from.
func TestVersionPayloadOnECS(t *testing.T) {
	resetCPImageCache(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Image":"123456789012.dkr.ecr.us-east-1.amazonaws.com/af-control-plane:0.6.0","ImageID":"sha256:cafe"}`))
	}))
	defer srv.Close()
	t.Setenv("ECS_CONTAINER_METADATA_URI_V4", srv.URL)

	m := &manager{rtFactory: &versionFakeFactory{image: "123456789012.dkr.ecr.us-east-1.amazonaws.com/af-workspace:0.6.0"}}
	got := versionPayload(context.Background(), m)

	if got["version"] != buildVersion {
		t.Fatalf("version = %v, want %q", got["version"], buildVersion)
	}
	img, _ := got["image"].(*imageInfo)
	if img == nil || img.Repo != "af-control-plane" || img.Tag != "0.6.0" || img.Digest != "sha256:cafe" {
		t.Fatalf("image = %+v", img)
	}
	ws, _ := got["workspace_image"].(*imageInfo)
	if ws == nil || ws.Repo != "af-workspace" || ws.Tag != "0.6.0" {
		t.Fatalf("workspace_image = %+v", ws)
	}
}

// Every deployment that is not ECS must lose the keys entirely rather than report an
// empty image: the Console draws the block only when a key is there, and an empty one
// would print an image line naming nothing.
func TestVersionPayloadWithoutECS(t *testing.T) {
	resetCPImageCache(t)
	t.Setenv("ECS_CONTAINER_METADATA_URI_V4", "")
	t.Setenv("ECS_CONTAINER_METADATA_URI", "")

	got := versionPayload(context.Background(), &manager{rtFactory: &versionPlainFactory{}})
	if got["version"] != buildVersion {
		t.Fatalf("version = %v", got["version"])
	}
	if _, ok := got["image"]; ok {
		t.Fatalf("image must be absent off ECS: %v", got["image"])
	}
	if _, ok := got["workspace_image"]; ok {
		t.Fatalf("workspace_image must be absent without the capability: %v", got["workspace_image"])
	}
	if got["runtime"] != "local" {
		t.Fatalf("runtime = %v, want local", got["runtime"])
	}
}

// A metadata endpoint that fails is "unknown", not an error — and it must not be
// hammered once per menu open. The success side is cached forever (the image a running
// task launched from cannot change), which is what the second probe checks.
func TestControlPlaneImageProbeIsCached(t *testing.T) {
	resetCPImageCache(t)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"Image":"acct.dkr.ecr.rg.amazonaws.com/af-control-plane:dev","ImageID":"sha256:beef"}`))
	}))
	defer srv.Close()
	t.Setenv("ECS_CONTAINER_METADATA_URI_V4", srv.URL)

	first := controlPlaneImage(context.Background())
	second := controlPlaneImage(context.Background())
	if first == nil || second == nil || *first != *second {
		t.Fatalf("probe results differ: %+v vs %+v", first, second)
	}
	if hits != 1 {
		t.Fatalf("metadata endpoint hit %d times, want 1 (cached)", hits)
	}
}

func TestControlPlaneImageUnknownOnError(t *testing.T) {
	resetCPImageCache(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("ECS_CONTAINER_METADATA_URI_V4", srv.URL)

	if got := controlPlaneImage(context.Background()); got != nil {
		t.Fatalf("failed probe = %+v, want nil", got)
	}
}
