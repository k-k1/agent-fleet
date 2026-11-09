package watchhook

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGitCredsReadsTheGitCredentialHelperAnswer(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null\nprintf 'username=x-token-auth\\npassword=tok123\\n'\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c, err := GitCreds(context.Background(), "bitbucket.org")
	if err != nil || c.Token != "tok123" || c.Basic || c.User != "x-token-auth" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestGitCredsEmptyAnswerIsNoCredential(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\ncat >/dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := GitCreds(context.Background(), "github.com"); err == nil {
		t.Fatal("an empty helper answer must not count as a credential")
	}
}

func TestGitCredsUsesBktCredForAnAPITokenConnection(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\ncat >/dev/null\nprintf 'username=x-bitbucket-api-token-auth\\npassword=gitside\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(bin, "workspace-agent")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n[ \"$1\" = bkt-cred ] || exit 1\nprintf 'username=me@example.com\\npassword=apitok\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := executable
	executable = func() (string, error) { return fake, nil }
	defer func() { executable = old }()
	c, err := GitCreds(context.Background(), "bitbucket.org")
	if err != nil || !c.Basic || c.User != "me@example.com" || c.Token != "apitok" {
		t.Fatalf("%+v %v", c, err)
	}
}
