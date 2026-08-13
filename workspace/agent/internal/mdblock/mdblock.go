// Package mdblock owns the marker-delimited regions agent-fleet writes into
// markdown files that belong to an agent CLI (docs/60 §60.7).
//
// なぜ共通化したか: 同じ strip/append の実装が codex/rtk.go と agy/rtk.go に複製され、
// ユーザー指示（docs/60）が 3 つ目の書き手になった。マーカーの綴りと「消え方」が
// ファイルごとにズレると、片方の版が残り続けたり利用者の記述を巻き込んで消したりする。
//
// 契約:
//   - 1 つのブロックは <!-- agent-fleet:<name> --> … <!-- /agent-fleet:<name> --> で囲む。
//   - Set は「既存ブロックを剥がしてから末尾に積む」。よって同じファイルへ複数の
//     ブロックを書くときは、呼ぶ順序がそのままファイル内の並び順になる。
//   - **マーカーの外は決して触らない。** 利用者が同じファイルへ書いた文章は残る
//     （AF が毎起動 cp -f で全消ししていた挙動の置き換え — docs/60 実害①）。
package mdblock

import "strings"

// Markers returns the start/end marker pair for a named agent-fleet block.
func Markers(name string) (start, end string) {
	return "<!-- agent-fleet:" + name + " -->", "<!-- /agent-fleet:" + name + " -->"
}

// Has reports whether the named block is present.
func Has(s, name string) bool {
	start, _ := Markers(name)
	return strings.Contains(s, start)
}

// Strip removes the named block and rejoins the remainder. A missing end marker
// (hand-mangled file) drops everything from the start marker onward — the block is
// unbounded, so keeping the tail would keep an arbitrary amount of AF's old text.
func Strip(s, name string) string {
	start, end := Markers(name)
	i := strings.Index(s, start)
	if i < 0 {
		return s
	}
	rest := s[i+len(start):]
	k := strings.Index(rest, end)
	if k < 0 {
		return strings.TrimRight(s[:i], "\n") + "\n"
	}
	head := strings.TrimRight(s[:i], "\n")
	tail := strings.TrimLeft(rest[k+len(end):], "\n")
	switch {
	case head == "":
		return tail
	case tail == "":
		return head + "\n"
	default:
		return head + "\n\n" + tail
	}
}

// Get returns the body of the named block (without the markers, trimmed of the
// blank lines that Set adds), and whether it was present.
func Get(s, name string) (string, bool) {
	start, end := Markers(name)
	i := strings.Index(s, start)
	if i < 0 {
		return "", false
	}
	rest := s[i+len(start):]
	k := strings.Index(rest, end)
	if k < 0 {
		return strings.Trim(rest, "\n"), true
	}
	return strings.Trim(rest[:k], "\n"), true
}

// StripLegacyPrefix removes a leading *unmarked* copy of text agent-fleet used to
// write before it started marking its regions — the era when the entrypoint `cp -f`'d
// the whole workspace guide over $CODEX_HOME/AGENTS.md and opencode's AGENTS.md
// (docs/60 実害①). Without this, the first run of the marker-based writer would treat
// 30 KB of stale guide as "the user's own text", preserve it, and append a second copy.
//
// 判定は legacy の**先頭行**でだけ行う。イメージの版が違えば本文は一致しないので
// バイト比較では移行できず、逆に先頭行より弱い判定（部分一致など）にすると利用者が
// 書いた文章を巻き込んで消しかねない。マーカーが1つでも見つかったらそこで止め、
// AF が管理していたブロックは残す。
func StripLegacyPrefix(s, legacy string) string {
	head := firstNonEmptyLine(legacy)
	if head == "" || !strings.HasPrefix(strings.TrimLeft(s, " \t\n"), head) {
		return s
	}
	if i := strings.Index(s, "<!-- agent-fleet:"); i >= 0 {
		return strings.TrimLeft(s[i:], "\n")
	}
	return ""
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// Set replaces (or removes, when body is empty) the named block, appending it at
// the end of the document. Everything outside agent-fleet's markers is preserved.
func Set(s, name, body string) string {
	out := Strip(s, name)
	if strings.TrimSpace(body) == "" {
		return out
	}
	start, end := Markers(name)
	block := start + "\n" + strings.Trim(body, "\n") + "\n" + end + "\n"
	if strings.TrimSpace(out) == "" {
		return block
	}
	return strings.TrimRight(out, "\n") + "\n\n" + block
}
