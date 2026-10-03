// Package fstore is the generic "one key = one small value file" store (docs/log/23
// P1-W2). Values live at <base()>/<subdir>/<key><ext>. base is injected when the store
// is built and resolved on every call — never cached, because tests swap HOME out. It
// is the shared implementation behind the seven session-status / pending-* / last-tool
// / *-sid families.
package fstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Store[T any] struct {
	base   func() string // config root (e.g. ~/.config/agent-fleet), resolved per call
	subdir string
	ext    string
	enc    func(T) []byte
	dec    func([]byte) (T, bool)
}

func (s Store[T]) Dir() string { return filepath.Join(s.base(), s.subdir) }

func (s Store[T]) Path(key string) string { return filepath.Join(s.Dir(), key+s.ext) }

// Write persists v under key. The error is surfaced for the one caller that logs
// it (session-status); everyone else discards it.
//
// The value is written to a temp file and renamed over the key, so a concurrent Read sees
// the old record or the new one. os.WriteFile truncates first, and a reader landing between
// the truncate and the write got "no record" — for session-status that is "idle", which let
// the pending-peer loop deliver into a turn a permission answer had just resumed (measured:
// 169042 missing reads across 6000 rewrites). Writers include the session-status
// hook, a separate process, so no in-process lock could close that gap. The temp name starts
// with a dot and ends in .tmp, so a directory sweep matching on ext never takes it for a key.
func (s Store[T]) Write(key string, v T) error {
	dir := s.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+key+s.ext+".*.tmp") // mode 0600
	if err != nil {
		return err
	}
	_, werr := f.Write(s.enc(v))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(f.Name(), s.Path(key))
	}
	if werr != nil {
		_ = os.Remove(f.Name())
	}
	return werr
}

// Read returns the stored value; ok=false when the file is missing, empty, or
// fails to decode.
func (s Store[T]) Read(key string) (T, bool) {
	var zero T
	b, err := os.ReadFile(s.Path(key))
	if err != nil || len(b) == 0 {
		return zero, false
	}
	return s.dec(b)
}

func (s Store[T]) Remove(key string) { _ = os.Remove(s.Path(key)) }

// ModTime is when key was last written. It exists for callers that judge freshness by
// when a value was captured rather than by the value itself (was this pending payload
// written before the resolution recorded in the transcript?). Missing gives ok=false.
func (s Store[T]) ModTime(key string) (time.Time, bool) {
	fi, err := os.Stat(s.Path(key))
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// Strings stores a plain string per key.
func Strings(base func() string, subdir, ext string) Store[string] {
	return Store[string]{
		base: base, subdir: subdir, ext: ext,
		enc: func(v string) []byte { return []byte(v) },
		dec: func(b []byte) (string, bool) { return string(b), true },
	}
}

// JSON stores a JSON-marshalled T per key.
func JSON[T any](base func() string, subdir, ext string) Store[T] {
	return Store[T]{
		base: base, subdir: subdir, ext: ext,
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

// Raw stores raw bytes per key (a payload passed through verbatim).
func Raw(base func() string, subdir, ext string) Store[[]byte] {
	return Store[[]byte]{
		base: base, subdir: subdir, ext: ext,
		enc: func(v []byte) []byte { return v },
		dec: func(b []byte) ([]byte, bool) { return b, true },
	}
}

// TrimmedStrings is Strings with whitespace-trimmed reads (the *-sid stores,
// whose files may carry a trailing newline from external writers).
func TrimmedStrings(base func() string, subdir string) Store[string] {
	return Store[string]{
		base: base, subdir: subdir,
		enc: func(v string) []byte { return []byte(v) },
		dec: func(b []byte) (string, bool) { return strings.TrimSpace(string(b)), true },
	}
}
