package sessionx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/spacex"
)

// errCode reads the refusal code of a create's answer.
func spaceErrCode(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Error.Code
}

func spaceFolders(t *testing.T) []string {
	t.Helper()
	sps, err := spacex.List()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range sps {
		out = append(out, s.Folder)
	}
	return out
}

func TestCreateInANewSpace(t *testing.T) {
	p := newCreateProbe(t)
	launchTmuxFn = func(session.Meta, bool) error { return nil }
	rec := p.post(map[string]any{"kind": "shell", "space": map[string]any{"name": "Research notes"}}, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	sps, _ := spacex.List()
	if len(sps) != 1 || sps[0].Name != "Research notes" {
		t.Fatalf("spaces = %+v", sps)
	}
	var made *session.Meta
	for _, m := range session.ListMetas() {
		if m.Dir == sps[0].Dir {
			m := m
			made = &m
		}
	}
	if made == nil {
		t.Fatalf("no session runs in %s", sps[0].Dir)
	}
	if got := wireSession(*made, false).Space; got != sps[0].Folder {
		t.Fatalf("wire space = %q, want %q", got, sps[0].Folder)
	}
}

func TestCreateInAnExistingSpace(t *testing.T) {
	p := newCreateProbe(t)
	launchTmuxFn = func(session.Meta, bool) error { return nil }
	sp, err := spacex.Create("kept")
	if err != nil {
		t.Fatal(err)
	}
	rec := p.post(map[string]any{"kind": "shell", "space": map[string]any{"folder": sp.Folder}}, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if got := spaceFolders(t); len(got) != 1 || got[0] != sp.Folder {
		t.Fatalf("a second space was made: %v", got)
	}
	found := false
	for _, m := range session.ListMetas() {
		found = found || m.Dir == sp.Dir
	}
	if !found {
		t.Fatal("no session runs in the existing space")
	}
}

// A create that made a space and then failed takes the space away again: a refused launch must
// not leave an empty space in the list.
func TestCreateInANewSpaceThatFailsLeavesNoSpace(t *testing.T) {
	p := newCreateProbe(t)
	rec := p.post(map[string]any{"kind": "shell", "space": map[string]any{"name": "doomed"}}, "")
	if rec.Code < 400 {
		t.Fatalf("the probe's launch was meant to fail: %d %s", rec.Code, rec.Body)
	}
	if got := spaceFolders(t); len(got) != 0 {
		t.Fatalf("the failed create left spaces behind: %v", got)
	}
}

// An existing space is never removed by a failed create, even an empty one.
func TestCreateInAnExistingSpaceThatFailsKeepsIt(t *testing.T) {
	p := newCreateProbe(t)
	sp, _ := spacex.Create("kept")
	rec := p.post(map[string]any{"kind": "shell", "space": map[string]any{"folder": sp.Folder}}, "")
	if rec.Code < 400 {
		t.Fatalf("the probe's launch was meant to fail: %d %s", rec.Code, rec.Body)
	}
	if _, ok := spacex.Lookup(sp.Folder); !ok {
		t.Fatal("a failed create removed a space it did not make")
	}
}

func TestCreateSpaceRefusals(t *testing.T) {
	cases := []struct {
		name string
		body func(p *createProbe) map[string]any
		code string
	}{
		{"with_dir", func(p *createProbe) map[string]any {
			return map[string]any{"dir": p.repo, "space": map[string]any{"name": "x"}}
		}, "bad_space"},
		{"with_worktree", func(p *createProbe) map[string]any {
			return map[string]any{"worktree": true, "space": map[string]any{"name": "x"}}
		}, "bad_space"},
		{"with_remote_url", func(p *createProbe) map[string]any {
			return map[string]any{"remote_url": "https://example.invalid/x.git", "space": map[string]any{"name": "x"}}
		}, "bad_space"},
		{"ssm", func(p *createProbe) map[string]any {
			return map[string]any{"kind": "ssm", "ssm_target": "i-0", "space": map[string]any{"name": "x"}}
		}, "bad_space"},
		{"folder_and_name", func(p *createProbe) map[string]any {
			sp, _ := spacex.Create("a")
			return map[string]any{"space": map[string]any{"folder": sp.Folder, "name": "b"}}
		}, "bad_space"},
		{"bad_name", func(p *createProbe) map[string]any {
			return map[string]any{"space": map[string]any{"name": "a\u0007b"}}
		}, "bad_space"},
		{"missing_folder", func(p *createProbe) map[string]any {
			return map[string]any{"space": map[string]any{"folder": "nope-abcdef"}}
		}, "space_not_found"},
		{"folder_outside_root", func(p *createProbe) map[string]any {
			return map[string]any{"space": map[string]any{"folder": "../repos/app"}}
		}, "space_not_found"},
		{"unmarked_folder", func(p *createProbe) map[string]any {
			_ = os.MkdirAll(filepath.Join(p.home, "spaces", "by-hand"), 0o755)
			return map[string]any{"space": map[string]any{"folder": "by-hand"}}
		}, "space_not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newCreateProbe(t)
			body := tc.body(p)
			before := spaceFolders(t)
			metas := len(session.ListMetas())
			rec := p.post(body, "")
			if got := spaceErrCode(t, rec.Body.Bytes()); got != tc.code {
				t.Fatalf("code = %q (%d %s), want %q", got, rec.Code, rec.Body, tc.code)
			}
			if after := spaceFolders(t); len(after) != len(before) {
				t.Fatalf("a refused create changed the spaces: %v -> %v", before, after)
			}
			if got := len(session.ListMetas()); got != metas {
				t.Fatalf("a refused create wrote a meta: %d -> %d", metas, got)
			}
		})
	}
}
