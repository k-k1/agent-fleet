package imagegen

// The gate on a request's reference images and mask (ADR 0100 decision 4).
//
// A request names pictures by PATH, and until this gate a path went straight to the provider:
// ComfyUI read it with os.ReadFile, and the CLI routes hand it to a child process (codex `-i`,
// agy's ImagePaths) that opens it later still. A check on the string cannot hold against that —
// the name can be swapped for a symlink between the check and the read, and the child's read is
// not ours to guard at all. So the bytes are fixed BEFORE the provider is involved: every
// reference is opened once, beneath a trusted root and refusing every symlink, and copied into
// an input set only this Agent writes. The provider is given the copies and never sees the
// caller's own path.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BrowseRootDir and PathDenied are the Files pane's own browse root and denylist (fs.go),
// installed by the Agent's main package for the same reason BrowsePath is: a second copy of
// that list inside this package would drift from the one the file tree enforces. nil refuses
// every reference rather than reading it through a weaker check.
var (
	BrowseRootDir func() string
	PathDenied    func(rel string) bool
)

// inputsDirName is the folder under the Console's own that holds the input sets. It is NOT the
// member's upload folder: that is browse-root-relative `generated/console/inputs/`
// (InputPicker.tsx), which with the default browse root is ~/generated/console/inputs, while
// this is ~/.cache/agent-fleet/generated/console/inputs — never the same place, so wiping this
// one never touches a file the member dropped.
const inputsDirName = "inputs"

// ConsoleInputsDir is where the input sets live.
func ConsoleInputsDir() string { return filepath.Join(ConsoleDir(), inputsDirName) }

// inputMaxBytes is the ceiling on one reference, the same as the ComfyUI upload's: a file larger
// than any provider will accept is refused before it is copied rather than after.
const inputMaxBytes = comfyMaxUpload

// errBadInput marks a reference the gate refused. The edge answers it as a 400 with the path
// in the message, because "not a file this workspace may read" is something the caller can fix.
var errBadInput = errors.New("reference image refused")

// stagedInputs is one input set: where the copies are, and what the caller originally named.
// The originals are a RECORD only — they go to the sidecar and never to a provider.
type stagedInputs struct {
	Set        string
	Origins    []string
	MaskOrigin string
}

// stageRequestInputs reads every reference and the mask of req through the gate and copies
// them into a fresh input set. The returned Request names the copies; the originals come back
// separately. A request with no reference makes no set at all.
//
// On any failure the half-made set is removed before returning, so a refused request leaves
// nothing behind.
func stageRequestInputs(req Request) (Request, stagedInputs, error) {
	mask := strings.TrimSpace(req.Mask)
	if len(req.Inputs) == 0 && mask == "" {
		return req, stagedInputs{}, nil
	}
	id, err := newInputSetID()
	if err != nil {
		return req, stagedInputs{}, err
	}
	dir := filepath.Join(ConsoleInputsDir(), id)
	if err := os.MkdirAll(ConsoleInputsDir(), 0o700); err != nil {
		return req, stagedInputs{}, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return req, stagedInputs{}, err
	}
	fail := func(err error) (Request, stagedInputs, error) {
		_ = os.RemoveAll(dir)
		return req, stagedInputs{}, err
	}
	out := req
	out.Inputs = make([]string, 0, len(req.Inputs))
	st := stagedInputs{Set: id, Origins: append([]string(nil), req.Inputs...)}
	for i, in := range req.Inputs {
		copied, err := copyInputInto(dir, fmt.Sprintf("%02d-", i), in)
		if err != nil {
			return fail(err)
		}
		out.Inputs = append(out.Inputs, copied)
	}
	if mask != "" {
		copied, err := copyInputInto(dir, "mask-", mask)
		if err != nil {
			return fail(err)
		}
		out.Mask = copied
		st.MaskOrigin = req.Mask
	}
	return out, st, nil
}

// copyInputInto reads one caller path through the gate and writes it into dir. The copy keeps
// the original's base name after a prefix: ComfyUI's LoadImage filters its list by the
// extension, and a person reading the folder should recognise the file.
func copyInputInto(dir, prefix, origin string) (string, error) {
	raw, err := readInputOrigin(origin)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, prefix+filepath.Base(filepath.Clean(strings.TrimSpace(origin))))
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return dst, nil
}

// readInputOrigin is the gate itself: resolve the caller's path to (trusted root, relative
// path), refuse the denylist, then read beneath the root with no symlink anywhere on the way.
//
// Two roots and only two (decision 4): the browse root, which is what the member can see and
// upload into, and the generated-images root, because the default output folder is NOT under
// the browse root on a deployment whose AF_BROWSE_ROOT is not home, and "use this as a
// reference" must not refuse the Agent's own picture there.
func readInputOrigin(origin string) ([]byte, error) {
	root, rel, err := resolveInputOrigin(origin)
	if err != nil {
		return nil, err
	}
	raw, err := readBeneath(root, rel, inputMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", errBadInput, origin, err)
	}
	return raw, nil
}

func resolveInputOrigin(origin string) (root, rel string, err error) {
	p := strings.TrimSpace(origin)
	if p == "" {
		return "", "", fmt.Errorf("%w: an empty path", errBadInput)
	}
	if BrowseRootDir == nil || PathDenied == nil {
		return "", "", errNoBrowseRoot
	}
	browse := filepath.Clean(BrowseRootDir())
	if !filepath.IsAbs(p) {
		if !canonicalRel(p) {
			return "", "", fmt.Errorf("%w: %s is not a plain path under the browse root", errBadInput, origin)
		}
		if PathDenied(p) {
			return "", "", fmt.Errorf("%w: %s is on the list of folders the file browser never opens", errBadInput, origin)
		}
		return browse, p, nil
	}
	// The generated root first: it is the more specific of the two when it sits under the browse
	// root (the default, home), and it is under no denylisted folder either way.
	if r, ok := relUnder(p, generatedRootDir()); ok && canonicalRel(r) {
		return generatedRootDir(), r, nil
	}
	if r, ok := relUnder(p, browse); ok && canonicalRel(r) {
		if PathDenied(r) {
			return "", "", fmt.Errorf("%w: %s is on the list of folders the file browser never opens", errBadInput, origin)
		}
		return browse, r, nil
	}
	return "", "", fmt.Errorf("%w: %s is outside the browse root and the generated-images folder", errBadInput, origin)
}

// relUnder is the lexical "is p below root" test. The string is only a routing decision: what
// makes it safe is that the read afterwards is performed BENEATH root's own descriptor.
func relUnder(p, root string) (string, bool) {
	root = strings.TrimSuffix(filepath.Clean(root), "/")
	if root == "" || !strings.HasPrefix(p, root+"/") {
		return "", false
	}
	return strings.TrimPrefix(p, root+"/"), true
}

// canonicalRel refuses every spelling that is not a plain downward path — "..", ".", empty
// components, a backslash — rather than cleaning one into another, so the path that was checked
// against the denylist is the path that is opened.
func canonicalRel(rel string) bool {
	if rel == "" || strings.ContainsAny(rel, "\\\x00") || strings.HasPrefix(rel, "/") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// newInputSetID is the Agent's own id for a set. Random rather than sequential: the queue's
// counter restarts with the process, and a set left by a crash must not be reused by the next.
func newInputSetID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "s" + hex.EncodeToString(b[:]), nil
}

// removeInputSet deletes one set. The id is checked for shape first, because it is joined onto
// a path and RemoveAll is recursive.
func removeInputSet(id string) {
	if !validInputSetID(id) {
		return
	}
	_ = os.RemoveAll(filepath.Join(ConsoleInputsDir(), id))
}

func validInputSetID(id string) bool {
	if len(id) != 17 || id[0] != 's' {
		return false
	}
	_, err := hex.DecodeString(id[1:])
	return err == nil
}

// ClearInputSets removes every input set. Called once at Agent start: the queue lives in
// memory, so no job that could still need a set survives a restart.
func ClearInputSets() {
	_ = os.RemoveAll(ConsoleInputsDir())
}

// readRequestFile is the ONE way a provider reads a picture a Request names. By the time a
// provider runs, that path is a copy in an input set, and this refuses a symlink in its place
// as well — so a provider never follows a name, even its own. inputs_test.go holds the AST
// check that keeps os.ReadFile of a request path out of the providers.
func readRequestFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: %s is not an absolute path", errBadInput, path)
	}
	clean := filepath.Clean(path)
	// No ceiling here: each provider states its own limit with its own words, and a copy that
	// passed the gate is already under inputMaxBytes.
	raw, err := readBeneath(filepath.Dir(clean), filepath.Base(clean), 0)
	if err != nil {
		return nil, fmt.Errorf("could not read %s: %w", path, err)
	}
	return raw, nil
}
