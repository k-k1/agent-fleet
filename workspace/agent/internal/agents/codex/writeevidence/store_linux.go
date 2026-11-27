package writeevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const maxTurnBytes = 2 << 20

// The Agent is the store's only writer. A turn can launch several commands in parallel.
var saveMu sync.Mutex

// Save unions a completed observation into the turn's durable set. The launcher calls it
// inside the Agent with the scope it captured at launch. Replaying a result adds no hits.
func Save(evidence Evidence) error {
	if !validEvidence(evidence) {
		return errors.New("invalid shell write evidence")
	}
	if len(evidence.Paths) == 0 {
		return nil
	}

	saveMu.Lock()
	defer saveMu.Unlock()

	current, err := load(evidence.Scope)
	if err != nil {
		return err
	}
	if current.Root != "" && current.Root != evidence.Root {
		return errors.New("shell write observation workspace changed within the turn")
	}
	before := len(current.Paths)
	current.Root = evidence.Root
	current.Paths = append(current.Paths, evidence.Paths...)
	slices.Sort(current.Paths)
	current.Paths = slices.Compact(current.Paths)
	if len(current.Paths) == before {
		return nil
	}
	data, err := json.Marshal(current)
	if err != nil {
		return err
	}
	if len(data) > maxTurnBytes {
		return ErrLimit
	}
	return agents.WriteFileAtomic(turnPath(evidence.Scope), data, 0o600)
}

func load(scope Scope) (Evidence, error) {
	if !validScope(scope) {
		return Evidence{}, errors.New("invalid shell write evidence scope")
	}
	file, err := os.Open(turnPath(scope))
	if errors.Is(err, fs.ErrNotExist) {
		return Evidence{Scope: scope}, nil
	}
	if err != nil {
		return Evidence{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxTurnBytes+1))
	if err != nil {
		return Evidence{}, err
	}
	if len(data) > maxTurnBytes {
		return Evidence{}, ErrLimit
	}
	var evidence Evidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		return Evidence{}, err
	}
	if evidence.Scope != scope || !validEvidence(evidence) || !slices.IsSorted(evidence.Paths) {
		return Evidence{}, errors.New("invalid stored shell write evidence")
	}
	evidence.Paths = slices.Compact(evidence.Paths)
	return evidence, nil
}

func turnPath(scope Scope) string {
	// Native IDs are opaque. Hashing keeps them out of path syntax and bounds the filename.
	key := sha256.Sum256([]byte(scope.ThreadID + "\x00" + scope.TurnID))
	return filepath.Join(paths.AgentStateDir(), "codex-write-evidence", scope.SessionID, hex.EncodeToString(key[:])+".json")
}

func validScope(scope Scope) bool {
	return session.ValidName(scope.SessionID) && validNativeID(scope.ThreadID) && validNativeID(scope.TurnID)
}

func validNativeID(id string) bool {
	return len(id) > 0 && len(id) <= 128 && utf8.ValidString(id) && !strings.ContainsRune(id, 0)
}

func validEvidence(evidence Evidence) bool {
	root := evidence.Root
	if !validScope(evidence.Scope) || !filepath.IsAbs(root) || filepath.Clean(root) != root ||
		root == string(filepath.Separator) || !utf8.ValidString(root) || strings.ContainsRune(root, 0) {
		return false
	}
	prefix := root + string(filepath.Separator)
	for _, path := range evidence.Paths {
		if path == "" || path == "." || !utf8.ValidString(path) || strings.ContainsRune(path, 0) ||
			workspacePath(root, prefix+path) != path {
			return false
		}
	}
	return true
}
