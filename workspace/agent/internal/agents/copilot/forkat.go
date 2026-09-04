package copilot

// Forking from a given message (docs/log/55 §55.5). copilot has no official fork command
// either, so, as with claude, the branch is made by truncating the transcript. What differs
// is the unit: claude's is one file, copilot's is the whole `session-state/<sid>/`
// directory (events.jsonl plus checkpoints, files, research, rewind-file-snapshots and
// workspace.yaml; sessions created before 1.0.81 also contain a session.db).
//
// Measured: events.jsonl is what the restore reads from (docs/log/55 §55.5). Even with the
// SQLite that holds both turns still present (once the session.db beside it, now
// session-store.db directly under COPILOT_HOME), the truncated events.jsonl decided the
// context (re-measured on 1.0.81, and held by a contract test). So the DB can simply be
// copied and left alone — not rewriting what we do not understand is the line that keeps
// this surgery safe.
//
// Where the contents live moves at upstream's convenience, so do not write a check that
// depends on individual file names: when session.db disappeared, the contract test
// misreported it as "the copy is broken".
//
// copilot reads an unregistered session-state directory on resume without trouble and
// registers it in the root session-store.db by itself (measured). We never need to write
// the index.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// forkEvent is the slice of an events.jsonl line the cut logic reasons about. Lines
// travel to the branch verbatim; this is only for finding and validating the cut.
type forkEvent struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// cutIndexAt finds the line to cut BEFORE for anchor. The anchor must be a user.message
// — the mirror only offers those, but the value arrives from the client.
func cutIndexAt(lines [][]byte, anchor string) (int, error) {
	if anchor == "" {
		return 0, errors.New("分岐点が指定されていません")
	}
	for i, ln := range lines {
		var ev forkEvent
		if json.Unmarshal(ln, &ev) != nil || ev.ID != anchor {
			continue
		}
		if ev.Type != "user.message" {
			return 0, errors.New("この行からは分岐できません（ユーザーの発言を選んでください）")
		}
		return i, nil
	}
	return 0, errors.New("指定された分岐点がこの会話に見つかりません")
}

// nextPromptID returns the id of the first user.message after anchor — the cut point for
// "continue from this message" (この発言の続きから). "" (no error) when the anchor is
// the last exchange.
func nextPromptID(lines [][]byte, anchor string) (string, error) {
	at, err := cutIndexAt(lines, anchor)
	if err != nil {
		return "", err
	}
	for _, ln := range lines[at+1:] {
		var ev forkEvent
		if json.Unmarshal(ln, &ev) != nil {
			continue
		}
		if ev.Type == "user.message" && ev.ID != "" {
			return ev.ID, nil
		}
	}
	return "", nil
}

// forkEventLines returns the branch's events: the prefix before the anchored prompt, with
// the session id retargeted. Nothing else is rewritten — the id chain (id/parentId) is
// what makes an anchor still valid inside the branch.
// An empty anchor keeps everything — that is the whole-conversation fork, which copilot
// also has no native command for, so it comes through the same path.
func forkEventLines(lines [][]byte, anchor, srcSid, dstSid string) ([][]byte, error) {
	kept := lines
	if anchor != "" {
		cut, err := cutIndexAt(lines, anchor)
		if err != nil {
			return nil, err
		}
		kept = lines[:cut]
	}
	if !hasUserMessage(kept) {
		return nil, errors.New("この発言より前にやり取りがありません（新しいセッションを作ってください）")
	}
	out := make([][]byte, 0, len(kept))
	for _, ln := range kept {
		out = append(out, bytes.ReplaceAll(append([]byte(nil), ln...), []byte(srcSid), []byte(dstSid)))
	}
	return out, nil
}

// hasUserMessage reports whether the kept prefix holds a real exchange. An events file
// with only session bookkeeping resumes into an empty conversation.
func hasUserMessage(lines [][]byte) bool {
	for _, ln := range lines {
		var ev forkEvent
		if json.Unmarshal(ln, &ev) == nil && ev.Type == "user.message" {
			return true
		}
	}
	return false
}

// sessionStateExists reports whether sid already has a session-state directory — the
// "this fork is already materialized" test, so a restart never re-copies the source.
func sessionStateExists(sid string) bool {
	if sid == "" {
		return false
	}
	fi, err := os.Stat(sessionStateDir(sid))
	return err == nil && fi.IsDir()
}

// readEventLines reads a session's events.jsonl into non-blank lines.
func readEventLines(sid string) ([][]byte, error) {
	b, err := os.ReadFile(EventsPath(sid))
	if err != nil {
		return nil, err
	}
	var lines [][]byte
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, []byte(ln))
		}
	}
	return lines, nil
}

// MaterializeForkAt builds the branch's session-state directory: a copy of the source's,
// with events.jsonl truncated before the anchored prompt and the session id retargeted.
// Called on the fork's FIRST launch, before copilot starts, so the launch is an ordinary
// `--session-id <dst>` resume from there on.
func MaterializeForkAt(srcSid, dstSid, anchor string) error {
	if srcSid == "" || dstSid == "" {
		return errors.New("分岐元の会話が特定できません")
	}
	src, dst := sessionStateDir(srcSid), sessionStateDir(dstSid)
	if _, err := os.Stat(dst); err == nil {
		return errors.New("分岐先の会話が既に存在します")
	}
	lines, err := readEventLines(srcSid)
	if err != nil {
		return errors.New("分岐元の会話ログを読めません")
	}
	out, err := forkEventLines(lines, anchor, srcSid, dstSid)
	if err != nil {
		return err
	}
	// Build beside the target and rename in: a half-copied session-state directory that
	// copilot picks up would resume a truncated-by-accident conversation.
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".af-fork-*")
	if err != nil {
		return fmt.Errorf("分岐先を作成できません: %w", err)
	}
	defer os.RemoveAll(tmp) // no-op once renamed
	staged := filepath.Join(tmp, "state")
	if err := copyTree(src, staged); err != nil {
		return fmt.Errorf("分岐元をコピーできません: %w", err)
	}
	var buf bytes.Buffer
	for _, ln := range out {
		buf.Write(ln)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(staged, "events.jsonl"), buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("分岐先の会話ログを書けません: %w", err)
	}
	// workspace.yaml carries the session id too (measured), so rewriting events alone
	// would leave the two disagreeing.
	if err := retargetFile(filepath.Join(staged, "workspace.yaml"), srcSid, dstSid); err != nil {
		return err
	}
	if err := os.Rename(staged, dst); err != nil {
		return fmt.Errorf("分岐先を配置できません: %w", err)
	}
	return nil
}

// retargetFile rewrites srcSid → dstSid in a file, tolerating its absence.
func retargetFile(path, srcSid, dstSid string) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s を読めません: %w", filepath.Base(path), err)
	}
	return os.WriteFile(path, bytes.ReplaceAll(b, []byte(srcSid), []byte(dstSid)), 0o600)
}

// copyTree copies a directory recursively (regular files and dirs only — the session
// state holds no symlinks or devices).
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !fi.Mode().IsRegular() {
			return nil // skip anything exotic rather than guessing at it
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, fi.Mode().Perm())
	})
}
