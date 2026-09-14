package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestHuggingFaceVersionsExposeTheCurrentMainRevision(t *testing.T) {
	var path string
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.Query()
		_, _ = w.Write([]byte(`{"id":"Qwen/Qwen3-GGUF","createdAt":"2025-07-31T10:27:38Z",` +
			`"lastModified":"2026-01-30T09:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)
	old := engineIngestBase
	engineIngestBase = srv.URL
	t.Cleanup(func() { engineIngestBase = old })

	versions, aerr := engineHFVersions(t.Context(), engineVersionsReq{
		Source: "hf", Ref: "Qwen/Qwen3-GGUF", ModelRef: "Qwen/Qwen3-GGUF",
	})
	if aerr != nil {
		t.Fatalf("versions: %v", aerr.message)
	}
	if path != "/api/models/Qwen/Qwen3-GGUF" {
		t.Errorf("path = %q", path)
	}
	if strings.Join(query["expand[]"], ",") != "createdAt,lastModified" {
		t.Errorf("expansions = %v", query["expand[]"])
	}
	if len(versions) != 1 || versions[0] != (engineVersion{
		Ref: "main", Name: "main", PublishedAt: "2025-07-31T10:27:38Z", UpdatedAt: "2026-01-30T09:00:00Z",
	}) {
		t.Errorf("versions = %+v", versions)
	}
}

func TestHuggingFaceVersionsRejectMixedOrInvalidRepositoryRefs(t *testing.T) {
	for _, req := range []engineVersionsReq{
		{Source: "hf", Ref: "owner/a", ModelRef: "owner/b"},
		{Source: "hf", Ref: "owner/repo/extra", ModelRef: "owner/repo/extra"},
	} {
		if _, aerr := engineHFVersions(t.Context(), req); aerr == nil || aerr.status != http.StatusBadRequest {
			t.Errorf("request %+v = %v, want 400", req, aerr)
		}
	}
}

func TestCivitaiVersionsExposeEveryPublishedVersion(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"id":133005,"modelVersions":[
		  {"id":1759168,"name":"Ragnarok","publishedAt":"2025-05-07T21:02:16.940Z"},
		  {"id":501240,"name":"RunDiffusion Photo 2","publishedAt":"2024-01-10T12:00:00Z",` +
			`"updatedAt":"2024-02-11T12:00:00Z"},
		  {"id":0,"name":"draft"}]}`))
	}))
	t.Cleanup(srv.Close)
	old := engineCivitaiBase
	engineCivitaiBase = srv.URL
	t.Cleanup(func() { engineCivitaiBase = old })

	versions, aerr := engineCivitaiVersions(t.Context(), engineVersionsReq{
		Source: "civitai", Ref: "1759168", ModelRef: "133005",
	})
	if aerr != nil {
		t.Fatalf("versions: %v", aerr.message)
	}
	if path != "/api/v1/models/133005" {
		t.Errorf("path = %q", path)
	}
	if len(versions) != 2 || versions[0].Ref != "1759168" || versions[1].Ref != "501240" ||
		versions[1].UpdatedAt != "2024-02-11T12:00:00Z" {
		t.Errorf("versions = %+v", versions)
	}
	if _, aerr := engineCivitaiVersions(t.Context(), engineVersionsReq{
		Source: "civitai", Ref: "999", ModelRef: "133005",
	}); aerr == nil || aerr.status != http.StatusBadRequest {
		t.Errorf("foreign version = %v, want 400", aerr)
	}
}

func TestVersionsRouteUsesTheEngineRoleAndWireShape(t *testing.T) {
	a, e, _ := engineModelAdminAPI(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":133005,"modelVersions":[{"id":1759168,"name":"Ragnarok"}]}`))
	}))
	t.Cleanup(srv.Close)
	old := engineCivitaiBase
	engineCivitaiBase = srv.URL
	t.Cleanup(func() { engineCivitaiBase = old })

	call := func(body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest/versions", strings.NewReader(body))
		r.SetPathValue("key", "image")
		a.versionsIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, out := call(`{"source":"civitai","ref":"1759168","model_ref":"133005"}`)
	if code != http.StatusOK {
		t.Fatalf("versions = %d %v", code, out)
	}
	rows, _ := out["versions"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["ref"] != "1759168" {
		t.Errorf("wire answer = %v", out)
	}

	e.def.API = engineAPIChat
	if code, _ := call(`{"source":"civitai","ref":"1759168","model_ref":"133005"}`); code != http.StatusBadRequest {
		t.Errorf("Civitai versions on LLM engine = %d, want 400", code)
	}
}

func TestLLMRejectsCivitaiAcrossMetadataReads(t *testing.T) {
	a, e, _ := engineModelAdminAPI(t)
	e.def.API = engineAPIChat
	body := `{"source":{"civitai":{"versionId":1759168,"file":"model.safetensors"}}}`
	for _, tc := range []struct {
		path string
		call func(http.ResponseWriter, *http.Request, engineIngestGrant)
	}{
		{"/api/admin/engines/image/ingest/files", a.listIngestFiles},
		{"/api/admin/engines/image/ingest/resolve", a.resolveIngest},
	} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", tc.path, strings.NewReader(body))
		r.SetPathValue("key", "image")
		tc.call(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_source") {
			t.Errorf("%s = %d %s, want bad_source 400", tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestCivitaiFileSelectionResolvesTheChosenModelFile(t *testing.T) {
	const firstHash = "1111111111111111111111111111111111111111111111111111111111111111"
	const secondHash = "2222222222222222222222222222222222222222222222222222222222222222"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/v1/model-versions/77":
			_, _ = w.Write([]byte(`{"modelId":9,"baseModel":"SDXL 1.0","model":{"type":"Checkpoint"},"files":[
			  {"name":"full.safetensors","sizeKB":10,"type":"Model","downloadUrl":"` + srv.URL + `/full","hashes":{"SHA256":"` + firstHash + `"}},
			  {"name":"pruned.safetensors","sizeKB":5,"type":"Model","downloadUrl":"` + srv.URL + `/pruned","hashes":{"SHA256":"` + secondHash + `"}},
			  {"name":"config.json","sizeKB":1,"type":"Config","downloadUrl":"` + srv.URL + `/config"}]}`))
		case r.URL.Path == "/api/v1/models/9":
			_, _ = w.Write([]byte(`{"allowCommercialUse":["Image"]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	old := engineCivitaiBase
	engineCivitaiBase = srv.URL
	t.Cleanup(func() { engineCivitaiBase = old })

	source := engineIngestSource{Civitai: &engineIngestCivitai{VersionID: 77}}
	files, aerr := engineIngestList(t.Context(), source, "checkpoint")
	if aerr != nil {
		t.Fatalf("files: %v", aerr.message)
	}
	if len(files) != 2 || files[0].Name != "full.safetensors" || files[1].Name != "pruned.safetensors" {
		t.Fatalf("files = %+v", files)
	}

	resolved, aerr := engineResolveCivitai(t.Context(), engineIngestCivitai{
		VersionID: 77, File: "pruned.safetensors",
	})
	if aerr != nil {
		t.Fatalf("resolve selected file: %v", aerr.message)
	}
	if resolved.SHA256 != secondHash || resolved.Bytes != 5*1024 || resolved.DownloadURL != srv.URL+"/pruned" {
		t.Errorf("selected file = %+v", resolved)
	}
	if resolved.Source != "civitai:77" {
		t.Errorf("source = %q, want the existing version provenance", resolved.Source)
	}
}
