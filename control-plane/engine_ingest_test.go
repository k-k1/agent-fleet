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

// Civitai publishes sizes in fractional KILOBYTES and its hashes in upper case, and the version
// carries files that are not the model (measured 2026-09-09, ADR 0072 open question 4).
func TestEngineResolveCivitai(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"baseModel":"SD 1.5","model":{"name":"DreamShaper","type":"Checkpoint"},
			"files":[{"name":"config.json","sizeKB":1.5,"type":"Config","downloadUrl":"https://x/c"},
			{"name":"dreamshaper_8.safetensors","sizeKB":2082642.474609375,"type":"Model",
			 "downloadUrl":"https://civitai.com/api/download/models/128713",
			 "hashes":{"SHA256":"879DB523C30D3B9017143D56705015E15A2CB5628762C11D086FED9538ABD7FD"}}]}`))
	}))
	defer srv.Close()
	old := engineCivitaiBase
	engineCivitaiBase = srv.URL
	t.Cleanup(func() { engineCivitaiBase = old })

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
	if m.LicenseAcceptedBy != "u1" || m.LicenseAcceptedAt == "" {
		t.Errorf("acceptance not recorded: %q / %q", m.LicenseAcceptedBy, m.LicenseAcceptedAt)
	}
	if m.CommercialUse != "yes" || m.ContextTokens != 32768 {
		t.Errorf("row = %+v", m)
	}
	if got, _, _ := st.GetEngineIngestJob(ctx, job.ID); got.State != store.EngineIngestDone {
		t.Errorf("job state = %q", got.State)
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
	row := engineResolvedRow(res, engineIngestDef{HasToken: false})
	if row["can_ingest"] != false {
		t.Error("a gated model read as ingestible with no token")
	}
	if row["commercial_use"] != "no" {
		t.Errorf("commercial_use = %v", row["commercial_use"])
	}
	if engineResolvedRow(res, engineIngestDef{HasToken: true})["can_ingest"] != true {
		t.Error("a gated model with a token configured was still refused")
	}
	// An ungated model needs no token at all.
	if engineResolvedRow(engineResolved{}, engineIngestDef{})["can_ingest"] != true {
		t.Error("an ungated model was refused")
	}
	b, _ := json.Marshal(row)
	if strings.Contains(string(b), "token\":\"") {
		t.Error("a token value reached the wire")
	}
}
