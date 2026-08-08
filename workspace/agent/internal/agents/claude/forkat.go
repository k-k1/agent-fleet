package claude

// 発言時点からの分岐（docs/55 §55.5）— claude だけが公式の口を持たないので、転写 jsonl を
// 自前で切り詰めて分岐先の会話を作る。
//
// なぜ手術が許されるか（ADR 0039）: claude 自身の `--fork-session` が書く jsonl と元ファイル
// の差分は **sessionId だけ**（uuid/parentUuid/cwd/version は元のまま）と実測できている。
// つまりここでやるのは「行の取捨」と「sessionId の書き換え」だけで、スキーマを自作しない。
// 公式フラグ `--resume-session-at` は print モード専用（TUI では黙って無視される・実測）で、
// AF の claude は TUI 起動しか無いため使えない。
//
// なぜ切断点の検査が重いか: 切り方を間違えた jsonl は**起動はする**。壊れるのは次のターンで、
// tool_use に対応する tool_result が無いと API が丸ごと弾く。ユーザーから見ると「分岐したら
// エージェントが動かなくなった」で、原因が分岐にあるとは分からない。だから疑わしい切断点は
// 作らずに断る。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// forkLine is the slice of a transcript line the cut logic reasons about. Everything else
// travels verbatim — the file is rewritten from the ORIGINAL bytes, not from this struct.
type forkLine struct {
	Type             string          `json:"type"`
	UUID             string          `json:"uuid"`
	IsMeta           bool            `json:"isMeta"`
	IsSidechain      bool            `json:"isSidechain"`
	IsCompactSummary bool            `json:"isCompactSummary"`
	Message          struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// cutIndex finds the line to cut BEFORE for anchor, and refuses anchors that are not a
// real user prompt. The Console only offers the affordance on user turns, but the anchor
// arrives from the client, so every rule is re-checked here.
func cutIndex(lines [][]byte, anchor string) (int, error) {
	if anchor == "" {
		return 0, errors.New("分岐点が指定されていません")
	}
	for i, ln := range lines {
		var fl forkLine
		if json.Unmarshal(ln, &fl) != nil || fl.UUID != anchor {
			continue
		}
		switch {
		case fl.Type != "user":
			return 0, errors.New("エージェントの発言からは分岐できません")
		case fl.IsMeta:
			return 0, errors.New("この行からは分岐できません")
		case fl.IsSidechain:
			// A subagent's prompt. Cutting the parent conversation here would cut at a
			// point that only exists inside the sidechain.
			return 0, errors.New("サブエージェントの発言からは分岐できません")
		case fl.IsCompactSummary:
			// 圧縮サマリはユーザーの発言として記録されるが、会話の内容ではなくその要約。
			return 0, errors.New("圧縮の要約からは分岐できません")
		case !isUserPrompt(fl.Message.Content):
			// **ここが一番踏みやすい罠**: ツール結果も type:"user" で載る。素朴に
			// type=="user" で拾うと、ツール呼び出しと結果の間で切って会話を壊す。
			return 0, errors.New("ツールの実行結果からは分岐できません")
		}
		return i, nil
	}
	return 0, errors.New("指定された分岐点がこの会話に見つかりません")
}

// isUserPrompt reports whether a user line's content is something the human typed, as
// opposed to a tool_result the CLI recorded under the same "user" type. A bare string is
// always a prompt; an array is one only if it has no tool_result block.
func isUserPrompt(content json.RawMessage) bool {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return false
	}
	if trimmed[0] != '[' {
		return true
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(trimmed, &blocks) != nil {
		return false
	}
	if len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return false
		}
	}
	return true
}

// danglingToolUse reports a tool_use in the kept prefix whose tool_result was left on the
// other side of the cut. The next turn would send an assistant message announcing a tool
// call that never returned, which the API rejects outright — the branch would launch and
// then fail to answer anything.
func danglingToolUse(kept [][]byte) string {
	pending := map[string]bool{}
	for _, ln := range kept {
		var fl forkLine
		if json.Unmarshal(ln, &fl) != nil {
			continue
		}
		switch fl.Type {
		case "assistant":
			for _, id := range blockIDs(fl.Message.Content, "tool_use", "id") {
				pending[id] = true
			}
		case "user":
			for _, id := range blockIDs(fl.Message.Content, "tool_result", "tool_use_id") {
				delete(pending, id)
			}
		}
	}
	for id := range pending {
		return id
	}
	return ""
}

// blockIDs collects the idField of every content block of kind from a message content
// array. Returns nothing for the bare-string form (no blocks to inspect).
func blockIDs(content json.RawMessage, kind, idField string) []string {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(trimmed, &blocks) != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		var typ string
		if json.Unmarshal(b["type"], &typ) != nil || typ != kind {
			continue
		}
		var id string
		if json.Unmarshal(b[idField], &id) == nil && id != "" {
			out = append(out, id)
		}
	}
	return out
}

// buildForkLines returns the lines the branch's own jsonl should contain: the source's
// prefix up to (not including) the anchored prompt, with sessionId retargeted to dstSid.
//
// Only sessionId changes. uuid/parentUuid/cwd/version travel as-is — that is exactly what
// claude's own fork does (実測), and keeping the uuids is what makes an anchor still valid
// inside the branch (so a branch can be branched again at the same point).
func buildForkLines(lines [][]byte, anchor, dstSid string) ([][]byte, error) {
	cut, err := cutIndex(lines, anchor)
	if err != nil {
		return nil, err
	}
	kept := lines[:cut]
	if len(kept) == 0 {
		return nil, errors.New("この発言より前のやり取りがありません（新しいセッションを作ってください）")
	}
	if !HasConversation(kept) {
		return nil, errors.New("この発言より前に会話がありません（新しいセッションを作ってください）")
	}
	if id := danglingToolUse(kept); id != "" {
		return nil, fmt.Errorf("この地点では会話が壊れます（ツール呼び出し %s の結果が分岐先に入りません）", id)
	}
	out := make([][]byte, 0, len(kept))
	for _, ln := range kept {
		var obj map[string]json.RawMessage
		if json.Unmarshal(ln, &obj) != nil {
			// 解釈できない行は素通しする。落とすと uuid チェーンが切れうるし、そもそも
			// 読めない行を「読めた」ことにするより原文のまま運ぶほうが安全。
			out = append(out, append([]byte(nil), ln...))
			continue
		}
		if _, ok := obj["sessionId"]; ok {
			b, err := json.Marshal(dstSid)
			if err != nil {
				return nil, err
			}
			obj["sessionId"] = b
		}
		b, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// MaterializeForkAt writes the branch's transcript for dstSid: srcSid's history up to,
// but not including, the anchored prompt. Called on the fork's FIRST launch, before the
// pane starts — buildProgram then sees a jsonl for dstSid and resumes it like any other
// session, so nothing downstream needs to know a fork happened.
//
// The source file is only ever read. Refuses to overwrite an existing destination: that
// would mean a live conversation is already there.
func MaterializeForkAt(srcSid, dstSid, anchor string) error {
	if srcSid == "" || dstSid == "" {
		return errors.New("分岐元の会話が特定できません")
	}
	if SessionJSONLExists(dstSid) {
		return errors.New("分岐先の会話が既に存在します")
	}
	lines, srcPath, _ := TranscriptRead(srcSid)
	if len(lines) == 0 || srcPath == "" {
		return errors.New("分岐元の会話ログを読めません")
	}
	out, err := buildForkLines(lines, anchor, dstSid)
	if err != nil {
		return err
	}
	// Same project directory as the source: claude resolves a conversation by
	// <config>/projects/<project>/<sid>.jsonl, and the branch runs in the same working
	// copy, so it belongs in the same folder.
	dst := filepath.Join(filepath.Dir(srcPath), dstSid+".jsonl")
	var buf bytes.Buffer
	for _, ln := range out {
		buf.Write(ln)
		buf.WriteByte('\n')
	}
	// Write via a temp file in the same dir + rename: a half-written transcript would be
	// resumed as a truncated conversation, which is precisely the silent corruption this
	// whole feature is trying not to cause.
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".af-fork-*.jsonl")
	if err != nil {
		return fmt.Errorf("分岐先の会話ログを作成できません: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return fmt.Errorf("分岐先の会話ログを書けません: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("分岐先の会話ログを書けません: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("分岐先の会話ログの権限を設定できません: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("分岐先の会話ログを配置できません: %w", err)
	}
	return nil
}
