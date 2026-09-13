package main

// engine_ingest_test.go — resolving a source, and the life of an ingest job (ADR 0072 P4).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// hfStub answers like Hugging Face does, including the two things that decide this design:
// a GATED repository still publishes its metadata, and the licence a non-commercial model
// carries is in `license_name` while `license` says "other" (measured 2026-09-09).
func hfStub(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "FLUX.1-dev"):
			w.Write([]byte(`{"gated":"auto","cardData":{"license":"other",
				"license_name":"flux-1-dev-non-commercial-license"},
				"siblings":[{"rfilename":"flux1-dev.safetensors","size":23802932552,
				"lfs":{"sha256":"4610115bb0c89560703c892c59ac2742fa821e60ef5871b33493ba544683abd7"}}]}`))
		// The diffusers layout, measured 2026-09-09 on black-forest-labs/FLUX.1-dev: nine
		// .safetensors, of which only the top-level two are anything a person can take in. Five
		// are shards of two larger files; the rest are pipeline components in subdirectories.
		case strings.Contains(r.URL.Path, "FLUX-full"):
			w.Write([]byte(`{"gated":"auto","cardData":{"license":"other",
				"license_name":"flux-1-dev-non-commercial-license"},"siblings":[
				{"rfilename":"alt/model.safetensors","size":1000,"lfs":{"sha256":"7777777777777777777777777777777777777777777777777777777777777777"}},
				{"rfilename":"vae/diffusion_pytorch_model.safetensors","size":167666902,"lfs":{"sha256":"1111111111111111111111111111111111111111111111111111111111111111"}},
				{"rfilename":"transformer/diffusion_pytorch_model-00001-of-00003.safetensors","size":9982000000,"lfs":{"sha256":"2222222222222222222222222222222222222222222222222222222222222222"}},
				{"rfilename":"text_encoder_2/model-00001-of-00002.safetensors","size":4994000000,"lfs":{"sha256":"4444444444444444444444444444444444444444444444444444444444444444"}},
				{"rfilename":"text_encoder/model.safetensors","size":246144152,"lfs":{"sha256":"5555555555555555555555555555555555555555555555555555555555555555"}},
				{"rfilename":"flux1-dev.safetensors","size":23802932552,"lfs":{"sha256":"4610115bb0c89560703c892c59ac2742fa821e60ef5871b33493ba544683abd7"}},
				{"rfilename":"ae.safetensors","size":335304388,"lfs":{"sha256":"6666666666666666666666666666666666666666666666666666666666666666"}}]}`))
		// A real GGUF repository: several quantisations to choose between, files that are not
		// the model at all, and the context length Hugging Face parses out of the GGUF header
		// (`gguf.context_length`) and publishes on this very call — measured 2026-09-09 against
		// four repositories from three publishers.
		case strings.Contains(r.URL.Path, "Qwen2.5-Coder-0.5B"):
			w.Write([]byte(`{"gated":false,"cardData":{"license":"apache-2.0"},
				"gguf":{"context_length":32768,"architecture":"qwen2"},
				"siblings":[
				{"rfilename":"README.md","size":9000},
				{"rfilename":"qwen2.5-coder-0.5b-instruct-q8_0.gguf","size":675710848,
				 "lfs":{"sha256":"e1a77721fa97d4120000000000000000000000000000000000000000000000ff"}},
				{"rfilename":"qwen2.5-coder-0.5b-instruct-q4_k_m.gguf","size":491400064,
				 "lfs":{"sha256":"1d9614638d18024d0fbb36575a15f1302a3adf044df10345688ec4f6e1c4ff32"}},
				{"rfilename":"unversioned.gguf","size":123},
				{"rfilename":"qwen2.5-coder-0.5b-instruct-q2_k.gguf","size":415182720,
				 "lfs":{"sha256":"f9bddf294ef15c800000000000000000000000000000000000000000000000aa"}}]}`))
		case strings.Contains(r.URL.Path, "Qwen2.5-Coder"):
			w.Write([]byte(`{"gated":false,"cardData":{"license":"apache-2.0"},
				"siblings":[{"rfilename":"qwen2.5-coder-1.5b-instruct-q4_k_m.gguf","size":1117320768,
				"lfs":{"sha256":"cc324af070c2ecbfd324a30884d2f951a7ff756aba85cb811a6ec436933bb046"}}]}`))
		case strings.Contains(r.URL.Path, "no-checksum"):
			w.Write([]byte(`{"gated":false,"cardData":{"license":"mit"},
				"siblings":[{"rfilename":"plain.bin","size":10}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func TestEngineResolveHuggingFace(t *testing.T) {
	srv := hfStub(t)
	old := engineIngestBase
	engineIngestBase = srv.URL
	t.Cleanup(func() { engineIngestBase = old })
	ctx := context.Background()

	got, aerr := engineResolveHF(ctx, engineIngestHF{Repo: "Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF",
		File: "qwen2.5-coder-1.5b-instruct-q4_k_m.gguf"})
	if aerr != nil {
		t.Fatalf("resolve: %v", aerr.message)
	}
	if got.SHA256 != "cc324af070c2ecbfd324a30884d2f951a7ff756aba85cb811a6ec436933bb046" || got.Bytes != 1117320768 {
		t.Errorf("sha256/bytes = %s / %d", got.SHA256, got.Bytes)
	}
	if got.Gated {
		t.Error("an ungated repository read as gated")
	}
	if engineCommercialUse(got) != "yes" {
		t.Errorf("apache-2.0 read as commercial_use=%q", engineCommercialUse(got))
	}

	// ⚠️ The whole reason the CP can resolve without holding the operator's token: a gated
	// repository answers the metadata call anonymously and refuses only the DOWNLOAD.
	flux, aerr := engineResolveHF(ctx, engineIngestHF{Repo: "black-forest-labs/FLUX.1-dev",
		File: "flux1-dev.safetensors"})
	if aerr != nil {
		t.Fatalf("gated resolve: %v", aerr.message)
	}
	if !flux.Gated {
		t.Error("a gated repository did not read as gated — the ingest would start and 401 nine minutes in")
	}
	// `license: other` with the real terms in `license_name` (review R8). Keeping only the
	// first shows the two non-commercial models in the ADR's table as "other" and nothing else.
	if flux.License != "other" || flux.LicenseName != "flux-1-dev-non-commercial-license" {
		t.Errorf("licence = %q / %q", flux.License, flux.LicenseName)
	}
	if engineCommercialUse(flux) != "no" {
		t.Errorf("a non-commercial licence read as commercial_use=%q", engineCommercialUse(flux))
	}

	// A file with no LFS pointer is refused rather than taken in unverified: the download would
	// work and nothing could ever say the bytes are the bytes.
	if _, aerr := engineResolveHF(ctx, engineIngestHF{Repo: "x/no-checksum", File: "plain.bin"}); aerr == nil {
		t.Error("a file with no sha256 was accepted")
	}
	if _, aerr := engineResolveHF(ctx, engineIngestHF{Repo: "x/no-checksum", File: "absent.bin"}); aerr == nil {
		t.Error("a file the repository does not list was accepted")
	}
}

// The file list exists because a filename is something a person copies between two windows, and
// 🔴 one letter short (`flux1-dev.safetensor`, measured 2026-09-09) is refused correctly and
// looks exactly like a file that is not there.
//
// What it must NOT offer is as much of the point as what it offers: a README is not a model, and
// a .gguf with no LFS pointer would be refused by the resolve a moment later, so putting either
// in front of somebody is offering a dead end.
func TestEngineIngestListOffersOnlyWhatCanBeTakenIn(t *testing.T) {
	srv := hfStub(t)
	old := engineIngestBase
	engineIngestBase = srv.URL
	t.Cleanup(func() { engineIngestBase = old })

	got, aerr := engineIngestList(context.Background(),
		engineIngestSource{HF: &engineIngestHF{Repo: "Qwen/Qwen2.5-Coder-0.5B-Instruct-GGUF"}}, "gguf")
	if aerr != nil {
		t.Fatalf("list: %v", aerr.message)
	}
	// Sorted by name, which is the order the quantisations read in.
	want := []string{
		"qwen2.5-coder-0.5b-instruct-q2_k.gguf",
		"qwen2.5-coder-0.5b-instruct-q4_k_m.gguf",
		"qwen2.5-coder-0.5b-instruct-q8_0.gguf",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d candidates, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("candidate %d = %q, want %q", i, got[i].Name, w)
		}
	}
	// The size rides along, because "which quantisation" is a question about size and the
	// answer is otherwise a second trip to the model card.
	if got[1].Bytes != 491400064 || got[1].SHA256 != "1d9614638d18024d0fbb36575a15f1302a3adf044df10345688ec4f6e1c4ff32" {
		t.Errorf("q4_k_m = %d bytes / %s", got[1].Bytes, got[1].SHA256)
	}

	// An image engine asks the same repository a different question, and gets nothing rather
	// than a list of GGUFs it could not load.
	if c, _ := engineIngestList(context.Background(),
		engineIngestSource{HF: &engineIngestHF{Repo: "Qwen/Qwen2.5-Coder-0.5B-Instruct-GGUF"}}, "checkpoint"); len(c) != 0 {
		t.Errorf("a checkpoint engine was offered %d gguf files", len(c))
	}

	// A plain url addresses one file; there is nothing to list, and saying so beats an empty
	// list that reads as "this repository is empty".
	if _, aerr := engineIngestList(context.Background(),
		engineIngestSource{URL: "https://example.invalid/m.gguf"}, "gguf"); aerr == nil {
		t.Error("a plain url was listed as though it had files")
	}
}

// 🔴 Measured on the dev deployment (2026-09-09): the picker on FLUX.1-dev offered NINE files,
// five of them shards (`…-00001-of-00003.safetensors`). A shard is not a model — taking one in
// downloads ten gigabytes and writes a catalogue row nothing can load, which is precisely the
// dead end the list was built to remove.
//
// What survives is ordered, not just filtered: the single-file checkpoint is at the top level
// and the diffusers components are in subdirectories, so top level comes first. The components
// stay on the list because ADR 0072 decision 2 stages `text_encoders/` and `vae/` in their own
// right — they are simply never what somebody opening this picker came for.
func TestEngineIngestListDropsShardsAndPutsTheCheckpointFirst(t *testing.T) {
	srv := hfStub(t)
	old := engineIngestBase
	engineIngestBase = srv.URL
	t.Cleanup(func() { engineIngestBase = old })

	got, aerr := engineIngestList(context.Background(),
		engineIngestSource{HF: &engineIngestHF{Repo: "black-forest-labs/FLUX-full"}}, "checkpoint")
	if aerr != nil {
		t.Fatalf("list: %v", aerr.message)
	}
	names := []string{}
	for _, c := range got {
		names = append(names, c.Name)
	}
	// 🔴 `alt/…` sorts before both top-level files by name, so this order is reachable ONLY by
	// the top-level-first rule — without it the checkpoint somebody came for is third.
	want := []string{
		"ae.safetensors",
		"flux1-dev.safetensors",
		"alt/model.safetensors",
		"text_encoder/model.safetensors",
		"vae/diffusion_pytorch_model.safetensors",
	}
	if len(names) != len(want) {
		t.Fatalf("offered %d files, want %d: %v", len(names), len(want), names)
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("candidate %d = %q, want %q (full list %v)", i, names[i], w, names)
		}
	}
	for _, n := range names {
		if engineIngestShard(n) {
			t.Errorf("%q is one shard of a split file and cannot be taken in on its own", n)
		}
	}
}

// Where a row came from outlives the job that created it.
//
// 🔴 The id is deliberately short — unique only within one role in one deployment, because it is
// what a member reads in the launch menu and every character also rides in the 4,096-character
// SSM active set. Two vendors publishing a model of the same name is therefore a collision the
// ingest refuses, not one the id prevents. What refusing does NOT do is say which vendor the
// row that is already there came from, and engine_ingest_jobs.source answers that only while
// the job row lives.
func TestEngineIngestRowRemembersWhereItCameFrom(t *testing.T) {
	api := &fakeIngestECS{}
	ing, st := testIngester(t, api, nil)
	ctx := context.Background()
	req := ingestReq()
	req.Resolved.Source = "hf:black-forest-labs/FLUX.1-dev/flux1-dev.safetensors"
	job, aerr := ing.start(ctx, req)
	if aerr != nil {
		t.Fatalf("start: %v", aerr.message)
	}
	api.tasks = []ecstypes.Task{{
		TaskArn: aws.String(job.TaskArn), LastStatus: aws.String("STOPPED"),
		Containers: []ecstypes.Container{
			{Name: aws.String("fetch"), ExitCode: aws.Int32(0)},
			{Name: aws.String("upload"), ExitCode: aws.Int32(0)},
		},
	}}
	ing.reconcile(ctx)

	rows, err := st.ListEngineModels(ctx, req.Role)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %d (%v)", len(rows), err)
	}
	if rows[0].Source != "hf:black-forest-labs/FLUX.1-dev/flux1-dev.safetensors" {
		t.Errorf("source = %q — the catalogue cannot say which vendor this model is", rows[0].Source)
	}
	// And the panel says it. A column nothing renders is a column nobody can use.
	if engineAdminModelRow(rows[0])["source"] != rows[0].Source {
		t.Error("the provenance is stored but never shown")
	}
	// Absent, not "unknown", for a row that came from the stack rather than a URL.
	if _, ok := engineAdminModelRow(store.EngineModel{ID: "seeded"})["source"]; ok {
		t.Error("a seeded row claims a provenance nobody recorded")
	}
}

// 🔴 And every PART remembers its own, which the row above cannot say for a split model.
//
// A FLUX.1 row is four files and they arrive as four downloads: the first creates the row, the
// other three are attaches, and an attach writes only the file. So the row's own source
// describes file one — while a reader takes one `hf:…/FLUX.1-dev/…` line under a four-file row
// as the provenance of all four. The parts really do come from different repositories: the
// text encoders in ADR 0072's table are not published by whoever published the diffusion model.
func TestEngineIngestAttachedPartRemembersItsOwnSource(t *testing.T) {
	api := &fakeIngestECS{}
	ing, st := testIngester(t, api, nil)
	ctx := context.Background()
	finish := func(req engineIngestRequest) {
		t.Helper()
		job, aerr := ing.start(ctx, req)
		if aerr != nil {
			t.Fatalf("start: %v", aerr.message)
		}
		api.tasks = []ecstypes.Task{{
			TaskArn: aws.String(job.TaskArn), LastStatus: aws.String("STOPPED"),
			Containers: []ecstypes.Container{
				{Name: aws.String("fetch"), ExitCode: aws.Int32(0)},
				{Name: aws.String("upload"), ExitCode: aws.Int32(0)},
			},
		}}
		ing.reconcile(ctx)
	}
	first := ingestReq()
	first.Resolved.Source = "hf:black-forest-labs/FLUX.1-dev/flux1-dev.safetensors"
	first.S3Key = "image/diffusion_models/flux1-dev.safetensors"
	finish(first)

	part := ingestReq()
	part.Attach, part.FileFlag = true, "--t5xxl"
	part.S3Key = "image/text_encoders/t5xxl_fp8_e4m3fn.safetensors"
	part.Resolved.Source = "hf:comfyanonymous/flux_text_encoders/t5xxl_fp8_e4m3fn.safetensors"
	finish(part)

	rows, err := st.ListEngineModels(ctx, first.Role)
	if err != nil || len(rows) != 1 || len(rows[0].Files) != 2 {
		t.Fatalf("rows = %d (%v)", len(rows), err)
	}
	for i, want := range []string{first.Resolved.Source, part.Resolved.Source} {
		if got := rows[0].Files[i].Source; got != want {
			t.Errorf("file %d source = %q, want %q — a part taken in from another repository"+
				" has no provenance of its own once the job row is gone", i, got, want)
		}
	}
	// The row still says where IT came from: the two facts are both kept, because the licence
	// that was accepted belongs to the repository the row was created from.
	if rows[0].Source != first.Resolved.Source {
		t.Errorf("the row's own source = %q, want %q", rows[0].Source, first.Resolved.Source)
	}
	// And the panel is told, per file. A column nothing renders is a column nobody can use.
	fileRows, _ := engineAdminModelRow(rows[0])["file_rows"].([]map[string]any)
	if len(fileRows) != 2 || fileRows[1]["source"] != part.Resolved.Source {
		t.Errorf("file_rows = %+v — the per-file provenance is stored but never shown", fileRows)
	}
	// Absent, not empty: a file staged by hand has no upstream, and an empty string in the
	// answer is a claim that somebody looked and found nothing.
	bare := engineModelFileRows(store.EngineModel{Files: []store.EngineModelFile{{S3Key: "llm/x.gguf"}}})
	if _, ok := bare[0]["source"]; ok {
		t.Error("a hand-staged file claims a provenance nobody recorded")
	}
}

// engineSourceURL is the inverse of the `Source:` lines the resolvers write, and the panel draws
// a link ONLY when it answers — the rest stay text, which is what an operator can still read.
func TestEngineSourceURL(t *testing.T) {
	for _, c := range []struct{ what, source, want string }{
		{"a Hugging Face file", "hf:Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf",
			"https://huggingface.co/Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF/blob/main/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf"},
		{"a file in a subdirectory", "hf:unsloth/Qwen3-30B-GGUF/Q4_K_M/qwen3-30b-Q4_K_M.gguf",
			"https://huggingface.co/unsloth/Qwen3-30B-GGUF/blob/main/Q4_K_M/qwen3-30b-Q4_K_M.gguf"},
		// 🔴 The whole reason this is composed in the Control Plane. `civitai:<id>` is a model
		// VERSION id, and the model id in the page's URL is a different number — so
		// `civitai.com/models/<id>` opens A DIFFERENT MODEL. This form is the one Civitai
		// resolves, and it is already what the resolver hands to LicenseURL.
		{"a Civitai version", "civitai:1759168", "https://civitai.com/models/?modelVersionId=1759168"},
		// 🔴 A url source is the direct download of the weights (22 GB in ADR 0072's table).
		// A link in a panel that says "where this came from" must not start one.
		{"a plain url", "https://example.invalid/m.safetensors", ""},
		// Two segments cannot be told apart: `hf:gpt2/model.gguf` is either a legacy
		// single-segment repository plus a file, or an owner and a repository with no file.
		{"a two-segment hf source", "hf:gpt2/model.gguf", ""},
		{"an hf source with no file", "hf:Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF/", ""},
		{"a civitai source that is not a number", "civitai:v3", ""},
		{"a seeded row", "", ""},
		{"something nobody has written yet", "ollama:qwen3", ""},
	} {
		if got := engineSourceURL(c.source); got != c.want {
			t.Errorf("%s (%q) = %q, want %q", c.what, c.source, got, c.want)
		}
	}
	// And the row carries it beside the text, absent when it could not be composed — the panel
	// branches on the field arriving rather than parsing the string a second time.
	row := engineAdminModelRow(store.EngineModel{ID: "x", Source: "civitai:1759168"})
	if row["source_url"] != "https://civitai.com/models/?modelVersionId=1759168" {
		t.Errorf("source_url = %v", row["source_url"])
	}
	plain := engineAdminModelRow(store.EngineModel{ID: "x", Source: "https://example.invalid/m.safetensors"})
	if _, ok := plain["source_url"]; ok {
		t.Error("a url source was turned into a link — that click is a 22 GB download")
	}
	if plain["source"] != "https://example.invalid/m.safetensors" {
		t.Error("the source text was dropped along with the link — the operator can read it")
	}
}

// 🔴 The context length is a CEILING, not a setting. Hugging Face answers what the architecture
// allows (the 30B in this deployment says 262144); what fits in an L4 is a different question
// and the deployment runs that model at 32768. So it rides as its own field for the panel to
// OFFER, and nothing here or below ever writes it into a row by itself.
func TestEngineResolveCarriesTheModelsOwnContextLength(t *testing.T) {
	srv := hfStub(t)
	old := engineIngestBase
	engineIngestBase = srv.URL
	t.Cleanup(func() { engineIngestBase = old })

	got, aerr := engineResolveHF(context.Background(), engineIngestHF{
		Repo: "Qwen/Qwen2.5-Coder-0.5B-Instruct-GGUF", File: "qwen2.5-coder-0.5b-instruct-q4_k_m.gguf"})
	if aerr != nil {
		t.Fatalf("resolve: %v", aerr.message)
	}
	if got.ContextLength != 32768 {
		t.Errorf("context_length = %d, want 32768", got.ContextLength)
	}
	if row := engineResolvedRow(got, false, "", engineKVGeometry{}); row["context_length"] != 32768 {
		t.Errorf("the panel is not told the context length: %v", row["context_length"])
	}

	// A repository with no GGUF has no such field, and an absent one must not surface as 0 —
	// the panel would offer a window of zero tokens as though it had been measured.
	flux, aerr := engineResolveHF(context.Background(), engineIngestHF{
		Repo: "black-forest-labs/FLUX.1-dev", File: "flux1-dev.safetensors"})
	if aerr != nil {
		t.Fatalf("gated resolve: %v", aerr.message)
	}
	if _, ok := engineResolvedRow(flux, false, "", engineKVGeometry{})["context_length"]; ok {
		t.Error("a repository with no gguf metadata reported a context length")
	}
}

// 🔴 The KV cache is the half of the VRAM answer that used to arrive four minutes and one
// purchased GPU too late: a borrowed llm engine took 17 GB of weights onto an L4 (24 GB) and
// then died with `cudaMalloc failed: out of memory ... failed to allocate buffer for kv cache`
// for the 16 GB its window wanted. So the resolve — the answer the panel draws BEFORE the
// button — carries what the cache costs per 1024 tokens, and the panel multiplies.
//
// Per 1024 tokens rather than at some assumed window, because the window is still being typed
// when this is read; the cache is linear in it, so one number answers every value.
func TestEngineResolvedRowCarriesTheKVCostPerThousandTokens(t *testing.T) {
	// The 30B this deployment runs: 48 layers, 4 KV heads, 128/128 — 3072 MiB at 32768 tokens
	// (engine_gguf_test.go), so 96 MiB per 1024.
	row := engineResolvedRow(engineResolved{}, false, "", engineKVGeometry{48, 4, 128, 128})
	if row["kv_mib_per_1k_tokens"] != 96 {
		t.Errorf("kv_mib_per_1k_tokens = %v, want 96", row["kv_mib_per_1k_tokens"])
	}
	// 🔴 ABSENT, not zero. The header read is best-effort and silent by design, and a 0 on the
	// wire is a model whose window is free — which is the lie this whole field exists to stop.
	// A partial geometry is the same case: three numbers out of four answer nothing.
	for _, g := range []engineKVGeometry{{}, {Layers: 48, HeadsKV: 4, KeyLen: 128}} {
		if v, ok := engineResolvedRow(engineResolved{}, false, "", g)["kv_mib_per_1k_tokens"]; ok {
			t.Errorf("geometry %+v reported a KV cost of %v — unread must not read as measured", g, v)
		}
	}
}

// civitaiStub answers a model VERSION and the HEAD on its download URL. The download answer is
// the caller's, because THAT is the split this API has: the metadata is 200 for everybody and
// the bytes are per uploader (ADR 0072 P2 欠落 5).
func civitaiStub(t *testing.T, download int) *httptest.Server {
	t.Helper()
	var base string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/download/") {
			if r.Method != http.MethodHead {
				t.Errorf("the probe used %s, want HEAD — a GET here downloads gigabytes", r.Method)
			}
			w.WriteHeader(download)
			return
		}
		w.Write([]byte(`{"baseModel":"SD 1.5","model":{"name":"DreamShaper","type":"Checkpoint"},
			"files":[{"name":"config.json","sizeKB":1.5,"type":"Config","downloadUrl":"` + base + `/c"},
			{"name":"dreamshaper_8.safetensors","sizeKB":2082642.474609375,"type":"Model",
			 "downloadUrl":"` + base + `/api/download/models/128713",
			 "hashes":{"SHA256":"879DB523C30D3B9017143D56705015E15A2CB5628762C11D086FED9538ABD7FD"}}]}`))
	}))
	base = s.URL
	t.Cleanup(s.Close)
	old := engineCivitaiBase
	engineCivitaiBase = s.URL
	t.Cleanup(func() { engineCivitaiBase = old })
	return s
}

// Civitai publishes sizes in fractional KILOBYTES and its hashes in upper case, and the version
// carries files that are not the model (measured 2026-09-09, ADR 0072 open question 4).
func TestEngineResolveCivitai(t *testing.T) {
	civitaiStub(t, http.StatusOK)

	got, aerr := engineResolveCivitai(context.Background(), engineIngestCivitai{VersionID: 128713})
	if aerr != nil {
		t.Fatalf("resolve: %v", aerr.message)
	}
	if got.SHA256 != "879db523c30d3b9017143d56705015e15a2cb5628762c11d086fed9538abd7fd" {
		t.Errorf("sha256 not lower-cased: %s", got.SHA256)
	}
	if got.Bytes != 2132625894 {
		t.Errorf("bytes = %d, want the KB figure times 1024", got.Bytes)
	}
	if got.BaseModel != "SD 1.5" {
		t.Errorf("baseModel = %q", got.BaseModel)
	}
	// 🔴 The positive control for the probe below: an asset anybody can download must not be
	// marked, or the panel refuses every Civitai ingest and the check is indistinguishable from
	// a check that never runs.
	if got.LoginRequired {
		t.Error("a downloadable asset was marked as needing an account")
	}
	if row := engineResolvedRow(got, false, "", engineKVGeometry{}); row["can_ingest"] != true {
		t.Errorf("can_ingest = %v for an asset with no wall at all", row["can_ingest"])
	}
}

// 🔴 ADR 0072 P2 欠落 5. Civitai's metadata call answers 200 with the hash, the size and the
// download URL for assets whose UPLOADER requires a logged-in account — so the resolve said
// `can_ingest: true`, a Fargate task ran for nine minutes and died with
// `curl: (22) The requested URL returned error: 401`, which is what reached the operator.
// Measured on af-sandbox: five assets, split 200 / 401 / 403.
//
// Hugging Face's equivalent is refused up front (`gated_no_token`) and this now is too — with
// its own code, because no token registered anywhere on this deployment would change the
// answer.
func TestEngineResolveCivitaiSpotsAnAssetThatNeedsAnAccount(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		civitaiStub(t, status)
		got, aerr := engineResolveCivitai(context.Background(), engineIngestCivitai{VersionID: 128713})
		if aerr != nil {
			// Not an error: everything the panel shows about the asset is still true, and the
			// licence has to be on screen before the refusal makes sense.
			t.Fatalf("resolve of a %d asset: %v", status, aerr.message)
		}
		if !got.LoginRequired {
			t.Errorf("a %d download resolved as freely fetchable", status)
		}
		row := engineResolvedRow(got, true, "", engineKVGeometry{})
		if row["can_ingest"] != false || row["login_required"] != true {
			t.Errorf("the panel is not told (%d): %v", status, row)
		}
		// NOT reported as gated: that word sends somebody to the Hugging Face token field,
		// which cannot help here. A registered token does not change can_ingest either.
		if row["gated"] == true {
			t.Errorf("a Civitai login wall was reported as a gated repository (%d)", status)
		}
	}

	// It fails OPEN in the directions it cannot read. A CDN that dislikes HEAD is not a login
	// wall, and neither is a probe that could not be made at all.
	civitaiStub(t, http.StatusMethodNotAllowed)
	got, aerr := engineResolveCivitai(context.Background(), engineIngestCivitai{VersionID: 128713})
	if aerr != nil || got.LoginRequired {
		t.Errorf("a 405 was read as a login wall (%v)", aerr)
	}
}

// 🔴 401 and 403 on a gated Hugging Face repository are two different failures with two
// different fixes, and the ingest task reports both as one line of curl.
//
// Measured on af-sandbox (ADR 0072 P5 実機検証): with ONE registered token, FLUX.1-dev came
// down and SD3.5 Medium died on `curl: (22) The requested URL returned error: 403`; accepting
// that repository's terms on Hugging Face with the token's account made the retry work. So 403
// is "the token arrived and that account has not accepted THIS repository" — nothing to do
// with registering a token, which is what 401 means.
//
// Both statuses are asserted: with only one, a classifier with no branch at all passes.
func TestEngineIngestFailureTellsTheTwoGatedRefusalsApart(t *testing.T) {
	const curl403 = "curl: (22) The requested URL returned error: 403"
	const curl401 = "curl: (22) The requested URL returned error: 401"
	for _, c := range []struct {
		source, msg, want, why string
	}{
		{"hf:stabilityai/stable-diffusion-3.5-medium/sd3.5_medium.safetensors", curl403,
			errCodeIngestGatedNotAccepted, "the token arrived; that account has not accepted the terms"},
		{"hf:black-forest-labs/FLUX.1-dev/flux1-dev.safetensors", curl401,
			errCodeIngestGatedNoToken, "no token reached the task at all"},
		{"hf:x/y/z.gguf", "HTTP/1.1 403 Forbidden", errCodeIngestGatedNotAccepted,
			"a verbose run prints the status line instead of curl's sentence"},
		{"civitai:128713", curl401, errCodeIngestCivitaiLogin,
			"Civitai has no token, so both statuses mean the same act — and a job started before" +
				" the resolve probe existed still ends up here"},
		{"https://example.com/m.gguf", curl403, "",
			"somebody's own server refusing is not something this can advise on"},
		{"hf:x/y/z.gguf", "sha256 mismatch: got aa… want bb…", "",
			"the failure that is not about access at all"},
		{"hf:x/y/model-403b.gguf", "curl: (56) connection reset", "",
			"a filename is not a diagnosis"},
	} {
		if got := engineIngestFailureCode(c.source, c.msg); got != c.want {
			t.Errorf("engineIngestFailureCode(%q, %q) = %q, want %q — %s", c.source, c.msg, got, c.want, c.why)
		}
	}

	// And it reaches the panel beside the task's own words rather than instead of them: the
	// curl line is sometimes the only detail there is.
	row := engineIngestJobRow(store.EngineIngestJob{
		ID: "j1", ModelID: "sd35-medium", State: store.EngineIngestFailed,
		Source: "hf:stabilityai/stable-diffusion-3.5-medium/sd3.5_medium.safetensors", Message: curl403,
	})
	if row["code"] != errCodeIngestGatedNotAccepted || row["message"] != curl403 {
		t.Errorf("job row = %v", row)
	}
	if _, ok := engineIngestJobRow(store.EngineIngestJob{ID: "j2", State: store.EngineIngestDone})["code"]; ok {
		t.Error("a job that did not fail carries a diagnosis")
	}
}

// A plain URL is the escape hatch, and the sha256 is not optional on it: no API published one,
// so nothing else in the path can tell a truncated download from a complete one.
func TestEngineResolvePlainURLNeedsASha(t *testing.T) {
	ctx := context.Background()
	if _, aerr := engineIngestResolve(ctx, engineIngestSource{URL: "https://example.com/m.gguf"}); aerr == nil {
		t.Error("a url with no sha256 was accepted")
	}
	if _, aerr := engineIngestResolve(ctx, engineIngestSource{URL: "http://example.com/m.gguf",
		SHA256: strings.Repeat("a", 64)}); aerr == nil {
		t.Error("a plain-http url was accepted")
	}
	got, aerr := engineIngestResolve(ctx, engineIngestSource{URL: "https://example.com/m.gguf",
		SHA256: strings.Repeat("A", 64)})
	if aerr != nil {
		t.Fatalf("resolve: %v", aerr.message)
	}
	if got.SHA256 != strings.Repeat("a", 64) {
		t.Error("the sha256 was not lower-cased")
	}
	if _, aerr := engineIngestResolve(ctx, engineIngestSource{}); aerr == nil {
		t.Error("a source naming nothing was accepted")
	}
}

// --- the job's life -----------------------------------------------------------

type fakeIngestECS struct {
	run   []ecs.RunTaskInput
	tasks []ecstypes.Task
	fail  string
}

func (f *fakeIngestECS) RunTask(_ context.Context, in *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	f.run = append(f.run, *in)
	if f.fail != "" {
		return &ecs.RunTaskOutput{Failures: []ecstypes.Failure{{Reason: aws.String(f.fail)}}}, nil
	}
	return &ecs.RunTaskOutput{Tasks: []ecstypes.Task{{TaskArn: aws.String("arn:aws:ecs:x:1:task/c/abc123")}}}, nil
}

func (f *fakeIngestECS) DescribeTasks(_ context.Context, _ *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	return &ecs.DescribeTasksOutput{Tasks: f.tasks}, nil
}

type fakeIngestLogs struct{ lines []string }

func (f fakeIngestLogs) GetLogEvents(context.Context, string, string) ([]string, error) {
	return f.lines, nil
}

func ingestStore(t *testing.T) *store.SQL {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func testIngester(t *testing.T, api *fakeIngestECS, logs engineIngestLogsAPI) (*engineIngester, *store.SQL) {
	t.Helper()
	st := ingestStore(t)
	return &engineIngester{
		def: engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"},
			SecurityGroups: []string{"sg-1"}, LogGroup: "/af/x/engines", HasToken: false},
		cluster: "c", ecs: api, logs: logs, store: st, models: st,
	}, st
}

func ingestReq() engineIngestRequest {
	return engineIngestRequest{
		Role: "llm", ModelID: "qwen2.5-coder-1.5b", Kind: "gguf",
		S3Key: "llm/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf", AcceptedBy: "u1",
		AcceptedTenant: "t-acme", AcceptedLicense: "apache-2.0",
		ContextTokens: 32768, MaxOutput: 4096,
		Resolved: engineResolved{
			DownloadURL: "https://huggingface.co/x/y/resolve/main/f.gguf",
			SHA256:      strings.Repeat("a", 64), Bytes: 1117320768,
			License: "apache-2.0", Source: "hf:x/y/f.gguf",
		},
	}
}

// The catalogue row appears only when the TASK succeeded, and it appears DISABLED.
func TestEngineIngestJobCreatesTheRowOnlyWhenTheTaskSucceeds(t *testing.T) {
	api := &fakeIngestECS{}
	ing, st := testIngester(t, api, nil)
	ctx := context.Background()

	job, aerr := ing.start(ctx, ingestReq())
	if aerr != nil {
		t.Fatalf("start: %v", aerr.message)
	}
	if job.State != store.EngineIngestRunning || job.TaskArn == "" {
		t.Fatalf("job = %+v", job)
	}
	// What the task was told: the resolved URL and the sha256 in the fetch container, the key
	// in the upload one. A missing sha256 here is a download nothing verifies.
	env := map[string]string{}
	for _, c := range api.run[0].Overrides.ContainerOverrides {
		for _, kv := range c.Environment {
			env[aws.ToString(c.Name)+"."+aws.ToString(kv.Name)] = aws.ToString(kv.Value)
		}
	}
	if env["fetch.SHA256"] != strings.Repeat("a", 64) || env["fetch.URL"] == "" {
		t.Errorf("fetch overrides = %v", env)
	}
	if env["upload.KEY"] != "llm/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf" {
		t.Errorf("upload overrides = %v", env)
	}

	// While it runs there is a job and NO catalogue row: a row now would offer a model whose
	// bytes are still coming down the wire.
	if rows, _ := st.ListEngineModels(ctx, "llm"); len(rows) != 0 {
		t.Fatalf("a catalogue row appeared before the task finished: %+v", rows)
	}

	api.tasks = []ecstypes.Task{{
		TaskArn: aws.String("arn:aws:ecs:x:1:task/c/abc123"), LastStatus: aws.String("STOPPED"),
		Containers: []ecstypes.Container{
			{Name: aws.String("fetch"), ExitCode: aws.Int32(0)},
			{Name: aws.String("upload"), ExitCode: aws.Int32(0)},
		},
	}}
	ing.reconcile(ctx)

	rows, _ := st.ListEngineModels(ctx, "llm")
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	m := rows[0]
	if m.Enabled {
		t.Error("the row was created enabled — 'staged' and 'offered' are different facts")
	}
	if len(m.Files) != 1 || m.Files[0].Bytes != 1117320768 {
		t.Errorf("files = %+v", m.Files)
	}
	// The whole tuple ADR 0072 open question 11 asks for — (tenant, member, when, licence) —
	// and it has to survive the round trip through job.Spec, which is where it actually lives
	// between the request and the row minutes later.
	if m.LicenseAcceptedBy != "u1" || m.LicenseAcceptedAt == "" {
		t.Errorf("acceptance not recorded: %q / %q", m.LicenseAcceptedBy, m.LicenseAcceptedAt)
	}
	if m.LicenseAcceptedTenant != "t-acme" || m.LicenseAcceptedLicense != "apache-2.0" {
		t.Errorf("the tenant axis of the acceptance was lost: tenant=%q licence=%q",
			m.LicenseAcceptedTenant, m.LicenseAcceptedLicense)
	}
	if m.CommercialUse != "yes" || m.ContextTokens != 32768 {
		t.Errorf("row = %+v", m)
	}
	if got, _, _ := st.GetEngineIngestJob(ctx, job.ID); got.State != store.EngineIngestDone {
		t.Errorf("job state = %q", got.State)
	}
}

// 🔴 ADR 0072 P2 欠落 6. The row an ingest wrote was always `[{S3Key, Bytes}]` with no flag —
// "one whole checkpoint" — so a SPLIT model could not be assembled by taking its parts in.
// Measured on af-sandbox: the four files of `flux1-dev-fp8` had to be staged as three throwaway
// rows and the real row re-typed through `POST /models`, which is also how two rows came to
// point at the same `clip_l.safetensors`.
//
// Both halves are pinned: the flag reaches the row, and a second download joins the row that is
// already there rather than replacing it.
func TestEngineIngestDeclaresWhatTheFileIsAndAttachesTheRest(t *testing.T) {
	api := &fakeIngestECS{}
	ing, st := testIngester(t, api, nil)
	ctx := context.Background()

	stopped := func(arn string) {
		api.tasks = []ecstypes.Task{{
			TaskArn: aws.String(arn), LastStatus: aws.String("STOPPED"),
			Containers: []ecstypes.Container{
				{Name: aws.String("fetch"), ExitCode: aws.Int32(0)},
				{Name: aws.String("upload"), ExitCode: aws.Int32(0)},
			},
		}}
		ing.reconcile(ctx)
	}

	unet := ingestReq()
	unet.Role, unet.ModelID, unet.Kind = "image", "flux1-dev-fp8", "checkpoint"
	unet.BaseModel, unet.FileFlag = "flux1", "--diffusion-model"
	unet.S3Key = "image/diffusion_models/flux1-dev-fp8.safetensors"
	unet.Resolved.Bytes = 11900000000
	job, aerr := ing.start(ctx, unet)
	if aerr != nil {
		t.Fatalf("start: %v", aerr.message)
	}
	stopped(job.TaskArn)

	rows, _ := st.ListEngineModels(ctx, "image")
	if len(rows) != 1 || len(rows[0].Files) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Files[0].Flag != "--diffusion-model" {
		t.Fatalf("the file's role was not recorded: %+v — the row reads as a whole checkpoint", rows[0].Files[0])
	}

	// The text encoder is a second download onto the SAME row.
	clip := unet
	clip.FileFlag, clip.S3Key = "--clip_l", "image/text_encoders/clip_l.safetensors"
	clip.Resolved.Bytes = 246144152
	clip.Attach = true
	clip.BaseModel = "" // an attach declares no family: the row settled that when it was created
	job2, aerr := ing.start(ctx, clip)
	if aerr != nil {
		t.Fatalf("attach start: %v", aerr.message)
	}
	stopped(job2.TaskArn)

	rows, _ = st.ListEngineModels(ctx, "image")
	if len(rows) != 1 {
		t.Fatalf("the attach created a second row: %+v", rows)
	}
	m := rows[0]
	if len(m.Files) != 2 || m.Files[1].Flag != "--clip_l" || m.Files[1].Bytes != 246144152 {
		t.Fatalf("files = %+v", m.Files)
	}
	// Everything the row already said is still what it says: an attach carries a file and
	// nothing else, which is the whole reason it is not an upsert.
	if m.BaseModel != "flux1" || m.Kind != "checkpoint" || m.LicenseAcceptedBy != "u1" {
		t.Errorf("the attach rewrote the row: %+v", m)
	}
	if m.Files[0].S3Key != "image/diffusion_models/flux1-dev-fp8.safetensors" {
		t.Errorf("the first file was disturbed: %+v", m.Files)
	}

	// A reconcile that sees the same finished task twice must not list the file twice — the
	// active set would then pass `--clip_l` to the engine two times.
	stopped(job2.TaskArn)
	rows, _ = st.ListEngineModels(ctx, "image")
	if len(rows[0].Files) != 2 {
		t.Errorf("a re-reconciled attach duplicated the file: %+v", rows[0].Files)
	}

	// And attaching to a row that is not there says so rather than writing one: the bytes are
	// in the bucket and the operator has to be told which id they were meant for.
	orphan := clip
	orphan.ModelID, orphan.S3Key = "forgotten", "image/text_encoders/t5xxl_fp8.safetensors"
	job3, aerr := ing.start(ctx, orphan)
	if aerr != nil {
		t.Fatalf("orphan start: %v", aerr.message)
	}
	stopped(job3.TaskArn)
	rows, _ = st.ListEngineModels(ctx, "image")
	if len(rows) != 1 {
		t.Errorf("an attach to a missing row created one: %+v", rows)
	}
}

// A CP replaced mid-download still writes the row: the spec is on the job, not in memory.
func TestEngineIngestSurvivesAControlPlaneRestart(t *testing.T) {
	api := &fakeIngestECS{}
	ing, st := testIngester(t, api, nil)
	ctx := context.Background()
	if _, aerr := ing.start(ctx, ingestReq()); aerr != nil {
		t.Fatalf("start: %v", aerr.message)
	}
	// A brand-new ingester over the same database is what a replaced task looks like.
	fresh := &engineIngester{def: ing.def, cluster: "c", ecs: api, store: st, models: st}
	api.tasks = []ecstypes.Task{{
		TaskArn: aws.String("arn:aws:ecs:x:1:task/c/abc123"), LastStatus: aws.String("STOPPED"),
		Containers: []ecstypes.Container{{Name: aws.String("upload"), ExitCode: aws.Int32(0)}},
	}}
	fresh.reconcile(ctx)
	if rows, _ := st.ListEngineModels(ctx, "llm"); len(rows) != 1 {
		t.Fatalf("the row was lost with the process that started the job: %+v", rows)
	}
}

// A failed container reports the task's OWN WORDS, not an exit code: "sha256 mismatch" and
// "401" need completely different things from the person reading them.
func TestEngineIngestFailureCarriesTheReason(t *testing.T) {
	api := &fakeIngestECS{}
	ing, st := testIngester(t, api, fakeIngestLogs{lines: []string{
		"ingest: fetched 12 bytes in 1s",
		"ingest: sha256 mismatch: got dead… want beef…",
	}})
	ctx := context.Background()
	job, _ := ing.start(ctx, ingestReq())
	api.tasks = []ecstypes.Task{{
		TaskArn: aws.String("arn:aws:ecs:x:1:task/c/abc123"), LastStatus: aws.String("STOPPED"),
		Containers: []ecstypes.Container{{Name: aws.String("fetch"), ExitCode: aws.Int32(1)}},
	}}
	ing.reconcile(ctx)

	got, _, _ := st.GetEngineIngestJob(ctx, job.ID)
	if got.State != store.EngineIngestFailed {
		t.Fatalf("state = %q", got.State)
	}
	if !strings.Contains(got.Message, "sha256 mismatch") {
		t.Errorf("message = %q, want the task's own line", got.Message)
	}
	if rows, _ := st.ListEngineModels(ctx, "llm"); len(rows) != 0 {
		t.Error("a failed ingest created a catalogue row")
	}
}

// RunTask can refuse without an error (200 with a failure list), and a job that never got a
// task must still be visible with the reason — otherwise the panel shows nothing and the
// operator presses again.
func TestEngineIngestRecordsARefusedRunTask(t *testing.T) {
	api := &fakeIngestECS{fail: "Capacity is unavailable"}
	ing, st := testIngester(t, api, nil)
	ctx := context.Background()
	job, aerr := ing.start(ctx, ingestReq())
	if aerr == nil {
		t.Fatal("a refused RunTask reported success")
	}
	got, ok, _ := st.GetEngineIngestJob(ctx, job.ID)
	if !ok || got.State != store.EngineIngestFailed || !strings.Contains(got.Message, "Capacity") {
		t.Errorf("job = %+v (found=%v)", got, ok)
	}
}

// The gate that costs nothing and saves a Fargate task: a gated repository on a deployment
// with no HF token is refused at the API, before anything is started.
func TestEngineResolvedRowRefusesGatedWithoutAToken(t *testing.T) {
	res := engineResolved{Gated: true, LicenseName: "flux-1-dev-non-commercial-license"}
	row := engineResolvedRow(res, false, "", engineKVGeometry{})
	if row["can_ingest"] != false {
		t.Error("a gated model read as ingestible with no token")
	}
	if row["commercial_use"] != "no" {
		t.Errorf("commercial_use = %v", row["commercial_use"])
	}
	if engineResolvedRow(res, true, "", engineKVGeometry{})["can_ingest"] != true {
		t.Error("a gated model with a token configured was still refused")
	}
	// An ungated model needs no token at all.
	if engineResolvedRow(engineResolved{}, false, "", engineKVGeometry{})["can_ingest"] != true {
		t.Error("an ungated model was refused")
	}
	b, _ := json.Marshal(row)
	if strings.Contains(string(b), "token\":\"") {
		t.Error("a token value reached the wire")
	}
}

// 🔴 The act the catalogue had no word for: the SAME model, a different file.
//
// `attach` refuses a flag the row already declares, and the unlabelled slot — the checkpoint
// itself — cannot be attached to at all, so moving a model to another quantisation meant
// forgetting the row and building it again. The row is where the licence acceptance lives (a
// record of a HUMAN act, ADR 0072 decision 10), with the family, the params, the enabled state
// and the provenance. All of it was thrown away for what a person thinks of as one file
// changing.
//
// So what this pins is what SURVIVES, plus the one thing that must NOT: the KV geometry, which
// describes the file that just left.
func TestEngineIngestReplaceKeepsTheRowAndRewritesTheGeometry(t *testing.T) {
	api := &fakeIngestECS{}
	ing, st := testIngester(t, api, nil)
	ctx := context.Background()

	// The row as it stands: enabled, licence accepted by a person, and carrying the geometry of
	// the q4 it was created from (28 layers — a 1.5B).
	if err := st.PutEngineModel(ctx, store.EngineModel{
		Role: "llm", ID: "qwen2.5-coder-1.5b", Kind: "gguf", Enabled: true,
		Files: []store.EngineModelFile{{S3Key: "llm/old-q4_k_m.gguf", Bytes: 1117320768,
			Source: "hf:x/y/old-q4_k_m.gguf"}},
		ContextTokens: 32768, MaxOutputTokens: 4096,
		License: "apache-2.0", LicenseAcceptedBy: "u1", LicenseAcceptedTenant: "t-acme",
		LicenseAcceptedLicense: "apache-2.0", BaseModel: "", Description: "the one in the menu",
		KVLayers: 28, KVHeadsKV: 2, KVKeyLen: 128, KVValueLen: 128,
	}); err != nil {
		t.Fatal(err)
	}

	req := ingestReq()
	req.Replace = true
	req.S3Key = "llm/new-q8_0.gguf"
	req.Resolved.Source = "hf:x/y/new-q8_0.gguf"
	req.Resolved.Bytes = 1_894_532_000
	// The new file's own header, read at the start of the job exactly as a creating ingest does.
	req.KVGeom = engineKVGeometry{48, 4, 128, 128}
	// None of these may reach the row: a replace changes one file.
	req.ContextTokens, req.MaxOutput = 1024, 128
	req.AcceptedBy, req.AcceptedTenant = "u2", "t-other"
	req.Description = "typed into a form nobody meant to edit"

	job, aerr := ing.start(ctx, req)
	if aerr != nil {
		t.Fatalf("start: %v", aerr.message)
	}
	api.tasks = []ecstypes.Task{{
		TaskArn: aws.String(job.TaskArn), LastStatus: aws.String("STOPPED"),
		Containers: []ecstypes.Container{
			{Name: aws.String("fetch"), ExitCode: aws.Int32(0)},
			{Name: aws.String("upload"), ExitCode: aws.Int32(0)},
		},
	}}
	ing.reconcile(ctx)

	rows, err := st.ListEngineModels(ctx, "llm")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %d (%v) — a replace must not create a second row", len(rows), err)
	}
	got := rows[0]
	if len(got.Files) != 1 || got.Files[0].S3Key != "llm/new-q8_0.gguf" {
		t.Fatalf("files = %+v, want the one file swapped", got.Files)
	}
	// 🔴 Per FILE, because the row's own source now names a file that is not there any more.
	if got.Files[0].Source != "hf:x/y/new-q8_0.gguf" {
		t.Errorf("file source = %q — the row points at bytes whose vendor nothing records", got.Files[0].Source)
	}
	if got.Files[0].Bytes != 1_894_532_000 {
		t.Errorf("bytes = %d — the size is what the VRAM floor is computed from", got.Files[0].Bytes)
	}
	// What the ingest knows nothing about and must leave alone. Enabled especially: the model is
	// in the launch menu, and an ingest that switched it off would take it out of every member's
	// picker for a file swap.
	if !got.Enabled {
		t.Error("the row was switched off by a file swap")
	}
	if got.LicenseAcceptedBy != "u1" || got.LicenseAcceptedTenant != "t-acme" {
		t.Errorf("the licence acceptance was rewritten: %q/%q — it records who agreed, and that person did",
			got.LicenseAcceptedBy, got.LicenseAcceptedTenant)
	}
	if got.ContextTokens != 32768 || got.MaxOutputTokens != 4096 || got.Description != "the one in the menu" {
		t.Errorf("the row's own settings were overwritten: ctx=%d out=%d desc=%q",
			got.ContextTokens, got.MaxOutputTokens, got.Description)
	}
	// 🔴 And the geometry IS rewritten. The row holds one set of KV numbers and only the create
	// path ever wrote them, so keeping the old ones would estimate this model's VRAM off a file
	// that no longer exists — 3072 MiB at 32k rather than 448, in the panel that decides whether
	// a GPU can hold it.
	if got.KVLayers != 48 || got.KVHeadsKV != 4 {
		t.Errorf("kv geometry = %d layers / %d kv heads, want the new file's 48/4",
			got.KVLayers, got.KVHeadsKV)
	}
	// The OLD object stays in the bucket. The CP has no s3:DeleteObject (ADR 0072 decision 7) and
	// this runs in the reconciler, with nobody to report a refusal to — and the keys are shared
	// (`clip_l.safetensors` was pointed at from two rows on af-sandbox), so a purge here breaks
	// models nobody touched. The download's own task was run; none of them is a delete.
	if len(api.run) == 0 {
		t.Fatal("no task was run at all, so the check below proves nothing")
	}
	for _, in := range api.run {
		for _, o := range in.Overrides.ContainerOverrides {
			for _, kv := range o.Environment {
				if aws.ToString(kv.Name) == "MODE" && aws.ToString(kv.Value) == "delete" {
					t.Error("a replace deleted bytes from the bucket: the keys may be shared, and nothing here could report what it refused to delete")
				}
			}
		}
	}
}

// --- forgetting a row of the history (the delete the table never had) ---------

// adminIngestDelete drives DELETE …/ingest/{id} the way the mux does, and answers the code and
// the body so a test can assert both the refusal and what the panel would then draw.
func adminIngestDelete(t *testing.T, a engineAdminAPI, g engineIngestGrant, key, id string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/api/admin/engines/"+key+"/ingest/"+id, nil)
	r.SetPathValue("key", key)
	r.SetPathValue("id", id)
	a.deleteIngest(rec, r, g)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func seedIngestJob(t *testing.T, st store.Store, id, role, state, tenant, s3key string) {
	t.Helper()
	if err := st.PutEngineIngestJob(t.Context(), store.EngineIngestJob{
		ID: id, Role: role, ModelID: id, S3Key: s3key, Source: "hf:x/" + id,
		State: state, TenantID: tenant,
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// 🔴 A job that is still going may NOT be forgotten, and this is the reason the state is
// checked at all: the row is not the task. Deleting it leaves the ECS task downloading, and
// minutes later that task writes its catalogue row with nothing on screen that says where the
// model came from — while the reconciler, which would have moved this row to `done`, finds
// nothing to update and the outcome (a sha256 mismatch, say) is lost.
func TestIngestHistoryKeepsAJobThatIsStillRunning(t *testing.T) {
	a, _, st := engineModelAdminAPI(t)
	super := engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true}
	seedIngestJob(t, st, "j-running", "image", store.EngineIngestRunning, "", "image/a.safetensors")
	seedIngestJob(t, st, "j-pending", "image", store.EngineIngestPending, "", "image/b.safetensors")

	for _, id := range []string{"j-running", "j-pending"} {
		code, out := adminIngestDelete(t, a, super, "image", id)
		if code != http.StatusConflict {
			t.Fatalf("delete %s = %d (%v), want 409 — the task outlives the row", id, code, out)
		}
		if err, _ := out["error"].(map[string]any); err == nil || err["code"] != errCodeIngestJobLive {
			t.Errorf("the refusal does not name itself: %v", out)
		}
		if _, ok, _ := st.GetEngineIngestJob(t.Context(), id); !ok {
			t.Errorf("%s was deleted anyway", id)
		}
	}

	// Positive control, in the same test: the SAME row, once it has stopped, goes. Without
	// this, an implementation that refused every delete would pass the assertions above.
	seedIngestJob(t, st, "j-running", "image", store.EngineIngestDone, "", "image/a.safetensors")
	code, out := adminIngestDelete(t, a, super, "image", "j-running")
	if code != http.StatusOK {
		t.Fatalf("delete of a finished job = %d (%v)", code, out)
	}
	if _, ok, _ := st.GetEngineIngestJob(t.Context(), "j-running"); ok {
		t.Error("a finished job survived its own deletion")
	}
	// And the answer is the remaining list, so the panel draws the server's list rather than
	// one it edited locally.
	jobs, _ := out["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("the answer's list = %v, want the one job that is left", out["jobs"])
	}
	if first, _ := jobs[0].(map[string]any); first["id"] != "j-pending" {
		t.Errorf("the remaining job = %v", jobs[0])
	}
	// A `failed` job is forgettable too: it created no row, and the whole point of the delete
	// is that a shelf of dead attempts can be cleared.
	seedIngestJob(t, st, "j-failed", "image", store.EngineIngestFailed, "", "image/c.safetensors")
	if code, out := adminIngestDelete(t, a, super, "image", "j-failed"); code != http.StatusOK {
		t.Fatalf("delete of a failed job = %d (%v)", code, out)
	}
}

// The delete is narrowed by the SAME rule the list is (ADR 0072 open question 11). Without it
// the reduced panel a granted tenant_admin gets is a read-only view of one tenant's jobs with a
// delete button for every tenant's — and the ids are in the answer they already hold.
//
// 🔴 The operator's own jobs carry NO tenant, so `j.TenantID != g.tenantID` has to be a
// comparison and never "narrow only when a tenant was resolved": written the second way, a
// tenant_admin deletes exactly the deployment-wide jobs they cannot see.
func TestIngestHistoryDeleteIsNarrowedToTheCallersTenant(t *testing.T) {
	a, _, st := engineModelAdminAPI(t)
	acme := engineIngestGrant{ident: store.Identity{ID: "u-acme"}, tenantID: "t-acme"}
	seedIngestJob(t, st, "j-acme", "image", store.EngineIngestDone, "t-acme", "image/acme.safetensors")
	seedIngestJob(t, st, "j-beta", "image", store.EngineIngestDone, "t-beta", "image/beta.safetensors")
	seedIngestJob(t, st, "j-operator", "image", store.EngineIngestDone, "", "image/op.safetensors")

	for _, id := range []string{"j-beta", "j-operator"} {
		code, out := adminIngestDelete(t, a, acme, "image", id)
		if code != http.StatusNotFound {
			t.Fatalf("acme deleting %s = %d (%v), want 404", id, code, out)
		}
		// 404 and not 403: "you may not touch job X" would confirm that job X exists, and the
		// id is the only thing a caller needs to guess.
		if err, _ := out["error"].(map[string]any); err == nil || err["code"] != errCodeIngestJobUnknown {
			t.Errorf("the refusal names the wrong thing: %v", out)
		}
		if _, ok, _ := st.GetEngineIngestJob(t.Context(), id); !ok {
			t.Errorf("%s was deleted by another tenant", id)
		}
	}
	// Positive control: their own job goes, so the 404s above are the narrowing and not a
	// delete that never works.
	if code, out := adminIngestDelete(t, a, acme, "image", "j-acme"); code != http.StatusOK {
		t.Fatalf("acme deleting their own job = %d (%v)", code, out)
	}
	if _, ok, _ := st.GetEngineIngestJob(t.Context(), "j-acme"); ok {
		t.Error("acme's own job survived")
	}
	// And the operator, who has no tenant to be acting for, reaches both of the others.
	super := engineIngestGrant{ident: store.Identity{ID: "u0"}, super: true}
	for _, id := range []string{"j-beta", "j-operator"} {
		if code, out := adminIngestDelete(t, a, super, "image", id); code != http.StatusOK {
			t.Fatalf("the operator deleting %s = %d (%v)", id, code, out)
		}
	}
}

// A replace whose slot is gone by the time the download finishes. The bytes are in the bucket
// and nothing points at them, which is the one outcome worth a log line rather than a silent
// success — and it must NOT fall back to creating a row or appending a second checkpoint.
func TestEngineIngestReplaceOfAForgottenRowWritesNothing(t *testing.T) {
	api := &fakeIngestECS{}
	ing, st := testIngester(t, api, nil)
	ctx := context.Background()

	req := ingestReq()
	req.Replace = true
	job, aerr := ing.start(ctx, req)
	if aerr != nil {
		t.Fatalf("start: %v", aerr.message)
	}
	api.tasks = []ecstypes.Task{{
		TaskArn: aws.String(job.TaskArn), LastStatus: aws.String("STOPPED"),
		Containers: []ecstypes.Container{
			{Name: aws.String("fetch"), ExitCode: aws.Int32(0)},
			{Name: aws.String("upload"), ExitCode: aws.Int32(0)},
		},
	}}
	ing.reconcile(ctx)

	if rows, err := st.ListEngineModels(ctx, "llm"); err != nil || len(rows) != 0 {
		t.Fatalf("rows = %d (%v) — a replace with no row to replace in must write nothing", len(rows), err)
	}
}

// A job of ANOTHER role is not this engine's to forget, and an id that never existed answers the
// same way. Both are 404 with one sentence: the caller cannot tell them apart and must not.
func TestIngestHistoryDeleteIsScopedToTheEngineInThePath(t *testing.T) {
	a, _, st := engineModelAdminAPI(t)
	super := engineIngestGrant{ident: store.Identity{ID: "u0"}, super: true}
	seedIngestJob(t, st, "j-llm", "llm", store.EngineIngestDone, "", "llm/x.gguf")
	if code, _ := adminIngestDelete(t, a, super, "image", "j-llm"); code != http.StatusNotFound {
		t.Errorf("deleting another role's job through this engine = %d, want 404", code)
	}
	if _, ok, _ := st.GetEngineIngestJob(t.Context(), "j-llm"); !ok {
		t.Error("the llm job was deleted through the image engine's path")
	}
	if code, _ := adminIngestDelete(t, a, super, "image", "j-nothing"); code != http.StatusNotFound {
		t.Errorf("deleting an id that never existed = %d, want 404", code)
	}
}

// 🔴 What the panel needs before it lets anybody press delete: whether the file this job took in
// is written down ANYWHERE else. While no catalogue row points at the key, the job row is the
// last written record of it — the CP cannot list the bucket at all (ADR 0072 review R3) — so the
// panel asks a second time before forgetting exactly those.
//
// The same field answers the opposite question when a key is registered again: a SHARED key is
// normal (decision 2 — `text_encoders/` is one file SD3.5 and FLUX.1 both read), so the panel
// names who has it instead of refusing.
func TestIngestHistorySaysWhichCatalogueRowStillUsesTheFile(t *testing.T) {
	a, e, st := engineModelAdminAPI(t)
	ctx := t.Context()
	if err := st.PutEngineModel(ctx, store.EngineModel{
		Role: "image", ID: "sd35-medium", Kind: "checkpoint",
		Files: []store.EngineModelFile{
			{S3Key: "image/checkpoints/sd3.5_medium.safetensors"},
			{Flag: "--clip_l", S3Key: "image/text_encoders/clip_l.safetensors"},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e.catalog.invalidate()
	// One job whose file a row points at, one whose file nothing does.
	seedIngestJob(t, st, "j-shared", "image", store.EngineIngestDone, "", "image/text_encoders/clip_l.safetensors")
	seedIngestJob(t, st, "j-orphan", "image", store.EngineIngestDone, "", "image/checkpoints/forgotten.safetensors")

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/admin/engines/image/ingest", nil)
	r.SetPathValue("key", "image")
	a.listIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u0"}, super: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d (%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]any{}
	for _, j := range out.Jobs {
		got[j["id"].(string)] = j["key_used_by"]
	}
	// 🔴 The row that keeps the file alive is NAMED, because "still used" with no name is an
	// answer nobody can act on — and it is named across roles, since the two rows that share a
	// text encoder need not be in the same role.
	if got["j-shared"] != "image/sd35-medium" {
		t.Errorf("the shared key's job says key_used_by=%v, want image/sd35-medium", got["j-shared"])
	}
	// And the orphan says NOTHING rather than "" or "nobody": absent is what makes the panel
	// ask twice, and it must not be produced by a row that simply forgot to look.
	if v, ok := got["j-orphan"]; ok && v != nil {
		t.Errorf("a key no row points at claimed a user: %v", v)
	}
}
