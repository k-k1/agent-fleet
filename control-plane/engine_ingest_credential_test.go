package main

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// A plain url must not reach the fetch task if it is not https, carries userinfo, or names an
// internal destination; and it never carries a credential marker.
func TestEngineResolvePlainURLRefusesUnsafeDestinations(t *testing.T) {
	ctx := context.Background()
	sha := strings.Repeat("a", 64)
	for _, u := range []string{
		"http://example.com/m.gguf",
		"https://huggingface.co@evil.example/m.gguf",
		"https://user:pw@example.com/m.gguf",
		"https://169.254.169.254/latest/meta-data/",
		"https://127.0.0.1/m.gguf",
		"https://[::1]/m.gguf",
		"https://0.0.0.0/m.gguf",
	} {
		if _, aerr := engineIngestResolve(ctx, engineIngestSource{URL: u, SHA256: sha}); aerr == nil {
			t.Errorf("%q was accepted", u)
		}
	}
	got, aerr := engineIngestResolve(ctx, engineIngestSource{URL: "https://huggingface.co.evil.example/m.gguf", SHA256: sha})
	if aerr != nil {
		t.Fatalf("a public https url was refused: %s", aerr.message)
	}
	if got.CredentialFor != "" {
		t.Errorf("a plain url carries credential marker %q", got.CredentialFor)
	}
}

// Only the hf and civitai resolvers mark a fetch as credentialed, and the marker reaches the
// fetch container as CREDENTIAL_FOR; a plain url sends it empty.
func TestEngineIngestCredentialMarkerReachesTheFetchTask(t *testing.T) {
	hf, aerr := engineResolveHF(context.Background(), engineIngestHF{}) // refused: no repo
	_ = hf
	if aerr == nil {
		t.Fatal("an empty hf source was accepted")
	}

	srv := hfStub(t)
	old := engineIngestBase
	engineIngestBase = srv.URL
	t.Cleanup(func() { engineIngestBase = old })
	got, aerr := engineResolveHF(context.Background(), engineIngestHF{Repo: "Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF",
		File: "qwen2.5-coder-1.5b-instruct-q4_k_m.gguf"})
	if aerr != nil {
		t.Fatalf("resolve hf: %v", aerr.message)
	}
	if got.CredentialFor != "hf" {
		t.Errorf("hf CredentialFor = %q", got.CredentialFor)
	}

	civitaiStub(t, 200)
	cv, aerr := engineResolveCivitai(context.Background(), engineIngestCivitai{VersionID: 128713})
	if aerr != nil {
		t.Fatalf("resolve civitai: %v", aerr.message)
	}
	if cv.CredentialFor != "civitai" {
		t.Errorf("civitai CredentialFor = %q", cv.CredentialFor)
	}

	fetchEnv := func(r engineResolved) map[string]string {
		env := map[string]string{}
		for _, c := range engineIngestOverrides(engineIngestRequest{Resolved: r}) {
			if aws.ToString(c.Name) != "fetch" {
				continue
			}
			for _, kv := range c.Environment {
				env[aws.ToString(kv.Name)] = aws.ToString(kv.Value)
			}
		}
		return env
	}
	if e := fetchEnv(got); e["CREDENTIAL_FOR"] != "hf" {
		t.Errorf("hf fetch env = %v", e)
	}
	plain, _ := engineIngestResolve(context.Background(), engineIngestSource{URL: "https://example.com/m.gguf", SHA256: strings.Repeat("a", 64)})
	if e := fetchEnv(plain); e["CREDENTIAL_FOR"] != "" {
		t.Errorf("plain url fetch env = %v", e)
	}
}
