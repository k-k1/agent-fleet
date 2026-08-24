package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// isolateSlot points ConfigDir()（jsonl の所在）と AgentConfigDir()（claude-sid 台帳）
// と MetaDir() を temp に向ける。実フリートの claude 設定・セッションを触らないため
// （テストが実 .claude.json を書いた事故が過去にある）。
func isolateSlot(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	t.Setenv("AF_SESSION_NAME", "")
	os.Unsetenv("AGENT_SESSION_CMD")
	return cfg
}

// writeSlotJSONL materializes a conversation log for id, the way claude would.
func writeSlotJSONL(t *testing.T, cfg, project, id string) string {
	t.Helper()
	dir := filepath.Join(cfg, "projects", project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, []byte(`{"type":"user","message":{"content":"hi"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const (
	testSlotSID = "b7000000-0000-5000-8000-00000000slot"
	testLiveSID = "47000000-0000-4000-8000-00000000live"
)

// 台帳が無ければ slot をそのまま使う — 普段の（ドリフトしていない）セッション。
func TestLiveSIDWithoutLedgerIsSlot(t *testing.T) {
	isolateSlot(t)
	if got := LiveSID(testSlotSID); got != testSlotSID {
		t.Fatalf("LiveSID = %q, want the slot sid %q", got, testSlotSID)
	}
}

// ドリフト後: 決定論 sid の jsonl は永久に現れないので、台帳の指す実ログを見に行く。
// これが無いとミラーは「まだ会話はありません」のまま固まる。
func TestJSONLPathsFollowsDriftedSID(t *testing.T) {
	cfg := isolateSlot(t)
	want := writeSlotJSONL(t, cfg, "-tmp-repo", testLiveSID)
	sids.Write(testSlotSID, testLiveSID)

	if got := LiveSID(testSlotSID); got != testLiveSID {
		t.Fatalf("LiveSID = %q, want the drifted id %q", got, testLiveSID)
	}
	got := jsonlPaths(testSlotSID)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("jsonlPaths = %v, want [%s]", got, want)
	}
	if !SessionJSONLExists(testSlotSID) {
		t.Fatal("SessionJSONLExists = false — 実ログがあるのに新規セッション扱いになる")
	}
}

// 台帳の指す先が消えていたら黙って slot に戻る。古い記載が残っていても、それが
// 「会話が無い」判定を狂わせてはいけない。
func TestLiveSIDIgnoresStaleLedger(t *testing.T) {
	isolateSlot(t)
	sids.Write(testSlotSID, testLiveSID) // 対応する jsonl は書かない

	if got := LiveSID(testSlotSID); got != testSlotSID {
		t.Fatalf("LiveSID = %q, want the slot sid %q when the ledger dangles", got, testSlotSID)
	}
	if SessionJSONLExists(testSlotSID) {
		t.Fatal("SessionJSONLExists = true — 実体の無い台帳を会話ありと誤認している")
	}
}

// 再起動は「claude が実際に書いている id」を resume しなければならない。slot sid を
// 渡すと "No conversation found" で落ち、毎回の再起動で会話が黙って消える。
func TestBuildProgramResumesDriftedSID(t *testing.T) {
	cfg := isolateSlot(t)
	writeSlotJSONL(t, cfg, "-tmp-repo", testLiveSID)
	sids.Write(testSlotSID, testLiveSID)

	got := buildProgram(testSlotSID, "", "", "", "", "", true)
	if !strings.Contains(got, "--resume '"+testLiveSID+"'") {
		t.Fatalf("program = %q, want --resume of the drifted id %q", got, testLiveSID)
	}
	if strings.Contains(got, testSlotSID) {
		t.Fatalf("program = %q, must not carry the slot sid claude no longer knows", got)
	}
}

// hook が名乗った id が我々のものと違えば、slot に引き戻したうえで対応を記録する。
// 記録が無いと、次のポーリングも再起動も決定論 sid を見続けて何も見つけられない。
func TestNormalizeHookSIDRecordsDrift(t *testing.T) {
	isolateSlot(t)
	m := session.Meta{Name: "s56ynzz", Dir: "/tmp/repo", Kind: session.KindClaude}
	session.WriteMeta(m)
	t.Setenv("AF_SESSION_NAME", m.Name)
	slot := session.UUID(m.Dir, m.Name)

	if got := NormalizeHookSID(testLiveSID); got != slot {
		t.Fatalf("NormalizeHookSID = %q, want the slot sid %q", got, slot)
	}
	if got := sids.Read(slot); got != testLiveSID {
		t.Fatalf("ledger = %q, want %q", got, testLiveSID)
	}
}

// ドリフトが解消したら（--session-id が効いた状態で起動し直された）台帳を畳む。
// 残したままだと、消えた古い会話を resume しようとする。
func TestNormalizeHookSIDClearsHealedDrift(t *testing.T) {
	isolateSlot(t)
	m := session.Meta{Name: "s56ynzz", Dir: "/tmp/repo", Kind: session.KindClaude}
	session.WriteMeta(m)
	t.Setenv("AF_SESSION_NAME", m.Name)
	slot := session.UUID(m.Dir, m.Name)
	sids.Write(slot, testLiveSID)

	if got := NormalizeHookSID(slot); got != slot {
		t.Fatalf("NormalizeHookSID = %q, want %q", got, slot)
	}
	if got := sids.Read(slot); got != "" {
		t.Fatalf("ledger = %q, want it cleared", got)
	}
}

// AF 管理外の claude（ユーザーが自分で起動したもの）の hook は素通りさせる。
// cwd 一致のような当て推量で他人のセッションに結びつけない。
func TestNormalizeHookSIDPassesThroughUnmanaged(t *testing.T) {
	isolateSlot(t)
	if got := NormalizeHookSID(testLiveSID); got != testLiveSID {
		t.Fatalf("NormalizeHookSID = %q, want it untouched without AF_SESSION_NAME", got)
	}
}
