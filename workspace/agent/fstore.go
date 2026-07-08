package main

// docs/23 P1-W2: 「キー1件 = 小さな値1ファイル」の汎用ストア。
// ~/.config/agent-fleet/<subdir>/<key><ext> に置く（denylist 配下なのでファイル
// ブラウザには出ない）。dir は homeDir() をその都度解決する — テストが HOME を
// 差し替えるためキャッシュしない。session-status / pending-{question,plan,perm,text} /
// last-tool / {opencode,codex}-sid のコピペ 7 家系をこの 1 実装に畳んだもの。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type fileStore[T any] struct {
	subdir string
	ext    string
	enc    func(T) []byte
	dec    func([]byte) (T, bool)
}

func (s fileStore[T]) dir() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", s.subdir)
}

func (s fileStore[T]) path(key string) string { return filepath.Join(s.dir(), key+s.ext) }

// write persists v under key. The error is surfaced for the one caller that logs
// it (session-status); everyone else discards it, as before.
func (s fileStore[T]) write(key string, v T) error {
	if err := os.MkdirAll(s.dir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.path(key), s.enc(v), 0o600)
}

// read returns the stored value; ok=false when the file is missing, empty, or
// fails to decode.
func (s fileStore[T]) read(key string) (T, bool) {
	var zero T
	b, err := os.ReadFile(s.path(key))
	if err != nil || len(b) == 0 {
		return zero, false
	}
	return s.dec(b)
}

func (s fileStore[T]) remove(key string) { _ = os.Remove(s.path(key)) }

// stringStore stores a plain string per key.
func stringStore(subdir, ext string) fileStore[string] {
	return fileStore[string]{
		subdir: subdir, ext: ext,
		enc: func(v string) []byte { return []byte(v) },
		dec: func(b []byte) (string, bool) { return string(b), true },
	}
}

// jsonStore stores a JSON-marshalled T per key.
func jsonStore[T any](subdir, ext string) fileStore[T] {
	return fileStore[T]{
		subdir: subdir, ext: ext,
		enc: func(v T) []byte { b, _ := json.Marshal(v); return b },
		dec: func(b []byte) (T, bool) {
			var v T
			if json.Unmarshal(b, &v) != nil {
				return v, false
			}
			return v, true
		},
	}
}

// rawStore stores raw bytes per key (a payload passed through verbatim).
func rawStore(subdir, ext string) fileStore[[]byte] {
	return fileStore[[]byte]{
		subdir: subdir, ext: ext,
		enc: func(v []byte) []byte { return v },
		dec: func(b []byte) ([]byte, bool) { return b, true },
	}
}

// trimmedStringStore is stringStore with whitespace-trimmed reads (the *-sid
// stores, whose files may carry a trailing newline from external writers).
func trimmedStringStore(subdir string) fileStore[string] {
	return fileStore[string]{
		subdir: subdir,
		enc:    func(v string) []byte { return []byte(v) },
		dec:    func(b []byte) (string, bool) { return strings.TrimSpace(string(b)), true },
	}
}
