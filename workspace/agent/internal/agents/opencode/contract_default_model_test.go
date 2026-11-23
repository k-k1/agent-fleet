//go:build contract

// The per-directory default-model answer (default_model.go) against the REAL opencode binary on
// PATH. No model turn is sent. In an isolated HOME with no credential the only provider is the
// keyless built-in one; a project config that adds a provider must make the answer refuse, however
// early after the directory's first request it is asked.
//
//	go test -tags contract -run ContractDefaultModel ./internal/agents/opencode/
package opencode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestContractDefaultModel(t *testing.T) {
	requireOpencode(t)
	home := t.TempDir()
	port := freePort(t)
	addr := "http://127.0.0.1:" + port
	t.Setenv(serveAddrEnv, addr)
	t.Setenv("HOME", home)
	password, err := newServePassword()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resetServeAuth("") })
	cmd := exec.Command("opencode", "serve", "--hostname", "127.0.0.1", "--port", port)
	cmd.Env = append(envWithHome(home), "OPENCODE_SERVER_USERNAME="+serveUser, "OPENCODE_SERVER_PASSWORD="+password)
	cmd.Dir = home
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	for deadline := time.Now().Add(30 * time.Second); !healthy(addr); time.Sleep(300 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("serve never became healthy through the production probe")
		}
	}
	plain := t.TempDir()
	withProvider := t.TempDir()
	cfg := `{"provider":{"acme":{"npm":"@ai-sdk/openai-compatible","name":"Acme","options":{"baseURL":"http://127.0.0.1:1/v1","apiKey":"k"},"models":{"m1":{"name":"M1"}}}}}`
	if err := os.WriteFile(filepath.Join(withProvider, "opencode.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	// A generous deadline: the first directory on a cold daemon can take seconds. The budget the
	// launch applies is a separate thing and is the unit tests' business.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	model, err := keylessDefaultModel(ctx, addr, filepath.Clean(plain))
	if err != nil || model == "" {
		t.Fatalf("a directory with the built-in provider alone: %q, %v (opencode %s)", model, err, opencodeVersion(t))
	}
	if got := model[:len("opencode/")]; got != "opencode/" {
		t.Errorf("default model %q is not an opencode one", model)
	}
	if m, err := keylessDefaultModel(ctx, addr, filepath.Clean(withProvider)); err == nil {
		t.Errorf("a directory whose project adds a provider resolved to %q", m)
	}
}
