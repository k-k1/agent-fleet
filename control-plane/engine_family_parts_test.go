package main

// Taking a SPLIT family in as one act (ADR 0072 decision 2, follow-up to the VAE remedy).
//
// The fault these cover is not a crash: it is three separate downloads, in an order nobody
// documents, ending in a row that is still marked — which is what taking Anima in actually cost
// before this existed.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// 🔴 The table is the thing a wrong entry ruins minutes later, so it is checked against the
// vocabulary it is written in: every part names a flag its family's template actually reads
// (engineComfyRequiredFlags), and names a Hugging Face repository rather than a bare file.
func TestFamilyPartsNameFilesTheirFamilyReads(t *testing.T) {
	for family, parts := range engineFamilyParts {
		want := map[string]bool{}
		for _, flag := range engineComfyRequiredFlags[family] {
			want[flag] = true
		}
		if len(want) == 0 {
			t.Errorf("%s declares parts but the provider requires no files for it", family)
			continue
		}
		for _, p := range parts {
			if !want[p.Flag] {
				t.Errorf("%s declares a %s part, which its template does not read (%v)",
					family, p.Flag, engineComfyRequiredFlags[family])
			}
			if !strings.Contains(p.Repo, "/") || p.File == "" || !strings.HasPrefix(p.S3Key, "image/") {
				t.Errorf("%s's %s part is not a repository, file and image key: %+v", family, p.Flag, p)
			}
		}
		// The diffusion model is the row's own file and is never a "part" — offering it would
		// mean downloading the thing that created the row a second time.
		for _, p := range parts {
			if p.Flag == "--diffusion-model" || p.Flag == "" {
				t.Errorf("%s offers its own checkpoint as a part", family)
			}
		}
	}
}

// 🔴 Two families, ONE file. Anima and Krea 2 both read the Qwen-Image VAE and it is the same
// bytes — but the reuse check compares the artifact identity, which carries the repository, so
// declaring them from different repositories would download it twice into two keys. This is the
// test that keeps the table honest about that.
func TestFamilyPartsShareOneSourceForOneKey(t *testing.T) {
	byKey := map[string]engineFamilyPart{}
	for family, parts := range engineFamilyParts {
		for _, p := range parts {
			if seen, ok := byKey[p.S3Key]; ok && (seen.Repo != p.Repo || seen.File != p.File) {
				t.Errorf("%s declares %s from %s/%s while another family declares it from %s/%s:"+
					" the identities differ, so it would be downloaded twice",
					family, p.S3Key, p.Repo, p.File, seen.Repo, seen.File)
			}
			byKey[p.S3Key] = p
		}
	}
}

// 🔴 A part is not a model, and since ADR 0085 decision 1 a REQUEST cannot claim otherwise: the
// role a file plays is the family's answer, not a field on the wire. What put encoders in the
// registered list beside the checkpoints — where they can never be enabled and help nothing — was
// a form that let somebody pick `--clip_l` and "new" in the same breath.
//
// So the claim is ignored rather than refused: the plan stages Anima's weights under the role its
// template reads, and the encoder it needs arrives as a part of that row.
func TestIngestIgnoresARoleTheRequestClaims(t *testing.T) {
	engineHFRepoStub(t, engineAnimaRepos())
	a, _, st, _ := enginePlanAPI(t)

	rec := httptest.NewRecorder()
	body := enginePressBody(t, a, "image", `{"kind":"checkpoint","base_model":"anima","file_flag":"--clip_l",
	  "license_accepted":true,"source":{"hf":{"repo":"circlestone-labs/Anima",
	  "file":"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"}}}`)
	r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", strings.NewReader(body))
	r.SetPathValue("key", "image")
	a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("the press = %d (%s)", rec.Code, rec.Body.String())
	}
	jobs := engineJobsOf(t, st, "image")
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d", len(jobs))
	}
	var spec engineIngestRequest
	if err := json.Unmarshal([]byte(jobs[0].Spec), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.FileFlag != "--diffusion-model" {
		t.Errorf("the request's claimed role won: %q", spec.FileFlag)
	}
	if jobs[0].S3Key != "image/diffusion_models/anima-aesthetic-v1.1.safetensors" {
		t.Errorf("staged at %q — a name no UNETLoader lists", jobs[0].S3Key)
	}
	// And the encoder is promised as a PART of this row rather than becoming a row of its own.
	var flags []string
	for _, fu := range spec.PartsFollowUp {
		flags = append(flags, fu.Flag)
	}
	if len(flags) != 2 {
		t.Errorf("the family's parts = %v, want the encoder and the VAE", flags)
	}
}

// 🔴 The mistake an operator fell into by DEFAULT, and the one that made every Anima row on
// af-sandbox useless: the form offered "whole checkpoint" first, and a split family reads no
// unflagged file at all. The row then held a 4 GB file under a role no template looks at and
// reported all three parts missing — including the one it was holding.
//
// Nobody is asked any more. The family says which of its files the weights are, and the negative
// control below is what keeps that from becoming a rule about every family: sdxl IS one whole
// checkpoint and stays one.
func TestIngestStagesASplitFamilysWeightsWhereItsLoaderLooks(t *testing.T) {
	engineHFRepoStub(t, map[string][]string{
		"circlestone-labs/Anima": {"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors",
			"split_files/text_encoders/qwen_3_06b_base.safetensors",
			"split_files/vae/qwen_image_vae.safetensors"},
		"stabilityai/sdxl": {"sd_xl_base_1.0.safetensors"},
	})
	a, _, st, _ := enginePlanAPI(t)

	post := func(repo, file string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		body := enginePressBody(t, a, "image", `{"kind":"checkpoint","license_accepted":true,
		  "source":{"hf":{"repo":"`+repo+`","file":"`+file+`"}}}`)
		r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", strings.NewReader(body))
		r.SetPathValue("key", "image")
		a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
		return rec.Code, rec.Body.String()
	}

	if code, body := post("circlestone-labs/Anima",
		"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"); code != http.StatusOK {
		t.Fatalf("taking anima in = %d (%s), want 200", code, body)
	}
	// 🔴 The negative control: sdxl is one whole checkpoint, its template reads the unflagged
	// slot, and taking one in must stay the ordinary act it has always been.
	if code, body := post("stabilityai/sdxl", "sd_xl_base_1.0.safetensors"); code != http.StatusOK {
		t.Fatalf("taking an sdxl checkpoint in = %d (%s), want 200", code, body)
	}
	want := map[string]string{
		"anima-aesthetic-v1.1": "image/diffusion_models/anima-aesthetic-v1.1.safetensors",
		"sd_xl_base_1.0":       "image/checkpoints/sd_xl_base_1.0.safetensors",
	}
	for _, j := range engineJobsOf(t, st, "image") {
		if key, ok := want[j.ModelID]; ok {
			if j.S3Key != key {
				t.Errorf("%s staged at %q, want %q", j.ModelID, j.S3Key, key)
			}
			delete(want, j.ModelID)
		}
	}
	if len(want) != 0 {
		t.Errorf("no job was written for %v", want)
	}
}
