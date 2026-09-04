package copilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// trustMu serializes the config.json read-modify-write: if two concurrent launches lose
// one of the trustedFolders appends, that session stalls on the trust dialog.
var trustMu sync.Mutex

// Pre-appending trustedFolders to copilot's config.json ($COPILOT_HOME/config.json).
// Measured (v1.0.73): the TUI stops on a "Confirm folder trust" dialog for an untrusted
// folder, and "Yes, and remember" stores the folder in config.json's trustedFolders[] -
// writing the same state beforehand skips the dialog. The file is JSONC-ish, with leading
// // comment lines ("This file is managed automatically."), so we preserve those lines and
// read/write only the JSON body.

// readConfig parses config.json, returning the leading comment lines verbatim
// and the JSON object (empty map when absent/corrupt).
func readConfig() (comments []string, m map[string]any) {
	return readConfigAt(configPath())
}

func readConfigAt(path string) (comments []string, m map[string]any) {
	m = map[string]any{}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, m
	}
	s := string(b)
	for {
		line, rest, found := strings.Cut(s, "\n")
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			comments = append(comments, line)
			if !found {
				s = ""
				break
			}
			s = rest
			continue
		}
		break
	}
	_ = json.Unmarshal([]byte(s), &m)
	return comments, m
}

// writeConfig persists m atomically, restoring the original comment header.
// copilot writes the same file itself, so unknown keys survive through readConfig's map.
func writeConfig(comments []string, m map[string]any) {
	writeConfigAt(configPath(), comments, m)
}

func writeConfigAt(p string, comments []string, m map[string]any) {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	var sb strings.Builder
	for _, c := range comments {
		sb.WriteString(c)
		sb.WriteString("\n")
	}
	sb.Write(b)
	sb.WriteString("\n")
	tmp := p + ".af-tmp"
	if os.WriteFile(tmp, []byte(sb.String()), 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

// EnsureFolderTrusted pre-accepts copilot's folder-trust gate for dir — exactly
// what copilot writes when the user answers "Yes, and remember this folder"
// (measured). Best-effort and idempotent; called on every launch - the agy 00dacc5 lesson
// that a one-time fix peels off later.
func EnsureFolderTrusted(dir string) {
	if dir == "" {
		return
	}
	trustMu.Lock()
	defer trustMu.Unlock()
	comments, m := readConfig()
	list, _ := m["trustedFolders"].([]any)
	for _, v := range list {
		if s, ok := v.(string); ok && s == dir {
			return
		}
	}
	m["trustedFolders"] = append(list, dir)
	writeConfig(comments, m)
}

// ensureFolderTrustedIn is EnsureFolderTrusted against an explicit COPILOT_HOME
// — for probes that run copilot under a throwaway home (models.go), where the
// process-env home must not be touched.
func ensureFolderTrustedIn(home, dir string) {
	if dir == "" {
		return
	}
	trustMu.Lock()
	defer trustMu.Unlock()
	p := filepath.Join(home, "config.json")
	comments, m := readConfigAt(p)
	list, _ := m["trustedFolders"].([]any)
	m["trustedFolders"] = append(list, dir)
	writeConfigAt(p, comments, m)
}
