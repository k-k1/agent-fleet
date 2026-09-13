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
