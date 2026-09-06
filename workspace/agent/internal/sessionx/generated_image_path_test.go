package sessionx

import (
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/imagegen"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// A generated image's userfile part carries an ABSOLUTE path; the Console's file API speaks
// browse-root-relative. This is the one step between them (ADR 0069).
func TestResolveUserFilesMapsAGeneratedImage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("AF_BROWSE_ROOT", root)

	abs := filepath.Join(imagegen.GeneratedDir("sid-1"), "image-1.png")
	turns := []transcript.Turn{{Parts: []transcript.Part{{Kind: "userfile", Files: []string{abs}}}}}
	resolveUserFiles(turns)

	if got, want := turns[0].Parts[0].Files[0], ".cache/agent-fleet/generated/sid-1/image-1.png"; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}
