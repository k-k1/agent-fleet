package imagegen

// The notes editor (ADR 0100 decision 12): the pane's way to the four sections when the Files
// pane cannot reach ~/imagegen-knowledge.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Files pane's path is offered only when the file is under the browse root.
func TestKnowledgeFilesPathFollowsTheBrowseRoot(t *testing.T) {
	withStudios(t)
	k, err := readKnowledge(KnowledgeModel, "org/model")
	if err != nil {
		t.Fatal(err)
	}
	if k.FilesPath != "imagegen-knowledge/models/org%2Fmodel.md" {
		t.Errorf("files_path under a home browse root = %q", k.FilesPath)
	}
	withInputGate(t, filepath.Join(os.Getenv("HOME"), "repos"))
	if k, _ := readKnowledge(KnowledgeModel, "org/model"); k.FilesPath != "" {
		t.Errorf("files_path outside the browse root = %q, want none", k.FilesPath)
	}
}

// An edit replaces the four sections, keeps the title, the member's own sub-headings and the
// heading spelling the file uses, and the version moves with the content.
func TestKnowledgeEditReplacesTheSections(t *testing.T) {
	withStudios(t)
	path, _ := knowledgeFile(KnowledgeFamily, "sdxl")
	writeFile(t, path, []byte("# SDXL memo\nintro line\n\n## Summary\n\nold\n\n## Records\n\n- 2026-09-01 · user: kept\n"))
	k, _ := readKnowledge(KnowledgeFamily, "sdxl")
	if k.Version == "" {
		t.Fatal("no version for an existing file")
	}
	got, err := editKnowledge(KnowledgeEdit{Scope: KnowledgeFamily, Key: "sdxl", Version: k.Version,
		Summary: "tags, not sentences", Settings: "cfg 5–7\n\n### hands\nlower cfg", Records: k.Records})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	for _, want := range []string{"# SDXL memo\nintro line\n", "## Summary\n\ntags, not sentences\n", "## Settings\n\ncfg 5–7\n\n### hands\nlower cfg\n", "## Records\n\n- 2026-09-01 · user: kept\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("file lacks %q:\n%s", want, s)
		}
	}
	if got.Settings != "cfg 5–7\n\n### hands\nlower cfg" || got.Version == k.Version {
		t.Errorf("read back = %+v", got)
	}
}

// An edit over a version the file no longer has — the agent's Edit or an add_image_knowledge
// landed in between — is refused and the file is left as the other writer made it.
func TestKnowledgeEditRefusesAStaleVersion(t *testing.T) {
	withStudios(t)
	k, _ := readKnowledge(KnowledgeModel, "m")
	if k.Version != "" {
		t.Fatalf("version of a missing file = %q", k.Version)
	}
	if _, err := appendKnowledgeRecord(KnowledgeAdd{Scope: KnowledgeModel, Key: "m", Note: "the agent's", Session: "s1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err := editKnowledge(KnowledgeEdit{Scope: KnowledgeModel, Key: "m", Version: k.Version, Summary: "mine"})
	if !errors.Is(err, errKnowledgeChanged) {
		t.Fatalf("err = %v, want errKnowledgeChanged", err)
	}
	after, _ := readKnowledge(KnowledgeModel, "m")
	if !strings.Contains(after.Records, "the agent's") || after.Summary != "" {
		t.Errorf("the refused edit touched the file: %+v", after)
	}
}

// The editor reads the summary whole: saving a summary cut at the read limit would delete the
// rest of it.
func TestKnowledgeEditorReadsTheWholeSummary(t *testing.T) {
	withStudios(t)
	path, _ := knowledgeFile(KnowledgeFamily, "flux1")
	long := strings.Repeat("あ", knowledgeSummaryMax)
	writeFile(t, path, []byte("## 要約\n\n"+long+"\n"))
	if k, _ := readKnowledge(KnowledgeFamily, "flux1"); !k.SummaryTruncated {
		t.Fatal("the agent's read is not cut")
	}
	k, _ := readKnowledgeCut(KnowledgeFamily, "flux1", false)
	if k.SummaryTruncated || k.Summary != long {
		t.Errorf("the editor's read was cut: %d bytes", len(k.Summary))
	}
}
