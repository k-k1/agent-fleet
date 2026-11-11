package skillcurator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// skillArchiveEnv makes a temp home with one skills root and returns the API over it and the root.
func skillArchiveEnv(t *testing.T) (API, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	api := API{Roots: func() ([]string, map[string][]string) {
		return []string{root}, map[string][]string{root: {"claude"}}
	}}
	return api, root
}
func writeAgentSkill(t *testing.T, root, name string, age time.Duration) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: x\ndescription: d\n---\n" + AgentMarker + "\nbody\n"
	p := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
}

func skillArchiveDo(t *testing.T, h http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w
}

func TestArchivedSkillsListAndRestore(t *testing.T) {
	api, root := skillArchiveEnv(t)
	writeAgentSkill(t, root, "afa-old", 40*24*time.Hour)
	if res := Run(root, time.Now(), DefaultConfig(), nil); len(res.Archived) != 1 {
		t.Fatalf("setup: %+v", res)
	}

	w := skillArchiveDo(t, api.HandleList, "GET", "/agents/skills/archive", "")
	var list archivedSkillsOut
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list.Skills) == 0 {
		t.Fatalf("list %d %s", w.Code, w.Body.String())
	}
	got := list.Skills[0]
	if got.Archived != "afa-old" || got.Name != "afa-old" || got.ArchivedAt == "" || !strings.HasPrefix(got.Root, "~/") || got.NameTaken {
		t.Fatalf("entry %+v", got)
	}

	body := `{"root":"` + got.Root + `","archived":"afa-old"}`
	w = skillArchiveDo(t, api.HandleRestore, "POST", "/agents/skills/archive/restore", body)
	if w.Code != 200 {
		t.Fatalf("restore %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "afa-old", "SKILL.md")); err != nil {
		t.Fatal("not restored:", err)
	}
	// Stamped as used: the next pass must not archive it again.
	if res := Run(root, time.Now(), DefaultConfig(), nil); len(res.Archived) != 0 {
		t.Fatalf("archived again at once: %+v", res)
	}
	// Gone from the list; a second restore finds nothing.
	w = skillArchiveDo(t, api.HandleRestore, "POST", "/agents/skills/archive/restore", body)
	if w.Code != http.StatusNotFound {
		t.Fatalf("second restore: %d %s", w.Code, w.Body.String())
	}
}

func TestRestoreArchivedSkillRefusals(t *testing.T) {
	api, root := skillArchiveEnv(t)
	writeAgentSkill(t, root, "afa-old", 40*24*time.Hour)
	Run(root, time.Now(), DefaultConfig(), nil)
	writeAgentSkill(t, root, "afa-old", time.Hour) // a live skill took the name
	tilde := "~/.claude/skills"

	cases := []struct {
		name, body string
		code       int
	}{
		{"name taken", `{"root":"` + tilde + `","archived":"afa-old"}`, http.StatusConflict},
		{"dotdot", `{"root":"` + tilde + `","archived":"../afa-old"}`, http.StatusNotFound},
		{"dot", `{"root":"` + tilde + `","archived":".."}`, http.StatusNotFound},
		{"absolute", `{"root":"` + tilde + `","archived":"` + filepath.Join(root, ".archive", "afa-old") + `"}`, http.StatusNotFound},
		{"nested", `{"root":"` + tilde + `","archived":"afa-old/../afa-old"}`, http.StatusNotFound},
		{"foreign root", `{"root":"/etc","archived":"afa-old"}`, http.StatusBadRequest},
		{"root dotdot", `{"root":"` + tilde + `/..","archived":"afa-old"}`, http.StatusBadRequest},
		{"empty", `{}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		w := skillArchiveDo(t, api.HandleRestore, "POST", "/agents/skills/archive/restore", c.body)
		if w.Code != c.code {
			t.Errorf("%s: %d %s, want %d", c.name, w.Code, w.Body.String(), c.code)
		}
	}
	// The refused restore left both skills where they were.
	if len(ListArchived(root)) != 1 {
		t.Error("archive changed by a refused restore")
	}
	list := skillArchiveDo(t, api.HandleList, "GET", "/agents/skills/archive", "")
	if !strings.Contains(list.Body.String(), `"name_taken":true`) {
		t.Errorf("list does not flag the taken name: %s", list.Body.String())
	}
}
