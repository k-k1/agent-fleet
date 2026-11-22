package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

// A store written before CustomAPI existed reads as no connection, and one carrying it reads back
// under its JSON name. The store is a fixed plaintext file, so a changed tag fails here.
func TestCustomAPIConnStoredShape(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	write := func(body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(Path(), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"opencode":{"MY_KEY":"v"}}`)
	d, err := Load()
	if err != nil || d.CustomAPI != nil {
		t.Fatalf("older store: err=%v custom=%+v", err, d.CustomAPI)
	}
	write(`{"customApi":{"provider":"mine","url":"http://10.0.0.5:8080/v1","apiKey":"k"}}`)
	d, err = Load()
	if err != nil || d.CustomAPI == nil || *d.CustomAPI != (CustomAPIConn{Provider: "mine", URL: "http://10.0.0.5:8080/v1", APIKey: "k"}) {
		t.Fatalf("stored store: err=%v custom=%+v", err, d.CustomAPI)
	}
	if err := Update(func(d *Data) error { d.Claude = "y"; return nil }); err != nil {
		t.Fatal(err)
	}
	if d, _ = Load(); d.CustomAPI == nil || d.CustomAPI.URL == "" {
		t.Fatal("an unrelated update dropped the connection")
	}
}
