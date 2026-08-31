// Package fstore は「キー1件 = 小さな値1ファイル」の汎用ストア（docs/23 P1-W2、
// W5 で internal 化）。<base()>/<subdir>/<key><ext> に置く。base はストア生成時に
// 注入され、呼び出しの都度解決する — テストが HOME を差し替えるためキャッシュ
// しない。session-status / pending-* / last-tool / *-sid の 7 家系の共通実装。
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
func (s Store[T]) Write(key string, v T) error {
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.Path(key), s.enc(v), 0o600)
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

// ModTime は key が最後に書かれた時刻。値そのものではなく「いつ捕まえたか」で
// 鮮度を判断する呼び出し側のためにある（保留ペイロードが、転写に記録された決着
// より前に書かれたものかどうか）。missing は ok=false。
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
