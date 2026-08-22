package cursor

import (
	"os"
	"path/filepath"
	"testing"
)

// writeChat lays out one cursor chat the way the CLI does:
// projects/<cwdSlug>/agent-transcripts/<chatID>/<chatID>.jsonl（実機の形）。
func writeChat(t *testing.T, home, dir, chatID string, withTranscript bool) string {
	t.Helper()
	d := filepath.Join(home, ".cursor", "projects", cwdSlug(dir), "agent-transcripts", chatID)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if withTranscript {
		if err := os.WriteFile(filepath.Join(d, chatID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// cwd はパスに入っているので、帰属はディレクトリを読むだけで足りる。別 cwd のチャットが
// 混ざらないこと（ここが崩れると他プロジェクトの会話を拾う）。
func TestCliSessionsScopedToCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeChat(t, home, "/tmp/repo", "aaaaaaaa-0000-4000-8000-000000000001", true)
	writeChat(t, home, "/tmp/other", "bbbbbbbb-0000-4000-8000-000000000002", true)

	got := cliSessions("/tmp/repo")
	if len(got) != 1 || got[0].ID != "aaaaaaaa-0000-4000-8000-000000000001" {
		t.Fatalf("cliSessions = %+v, want only the /tmp/repo chat", got)
	}
	if got[0].Created.IsZero() {
		t.Fatal("Created が空 — スロット作成時刻との突き合わせができない")
	}
}

// 転写ファイルの無いディレクトリは会話として数えない。cursor は起動時に器だけ作るので、
// これを候補にすると「まだ何も話していないチャット」を掴んで会話を失う。
func TestCliSessionsIgnoresEmptyChatDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeChat(t, home, "/tmp/repo", "cccccccc-0000-4000-8000-000000000003", false)

	if got := cliSessions("/tmp/repo"); len(got) != 0 {
		t.Fatalf("cliSessions = %+v, want none", got)
	}
}

// cwd スラグの規則（先頭/末尾の "/" を除いて残りを "-" に）— transcriptPath と同じ
// 写像を使っているので、片方だけ変えたら候補が空になる、を固定する。
func TestCliSessionsUsesTranscriptPathSlug(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const chat = "dddddddd-0000-4000-8000-000000000004"
	writeChat(t, home, "/home/dev/repos/proj", chat, true)

	got := cliSessions("/home/dev/repos/proj")
	if len(got) != 1 {
		t.Fatalf("cliSessions = %+v, want 1", got)
	}
	// 同じ id を transcriptPath でも引けること（読み経路と探索経路の写像が一致）。
	if p := transcriptPath("/home/dev/repos/proj", chat); filepath.Base(p) != chat+".jsonl" {
		t.Fatalf("transcriptPath = %q", p)
	} else if _, err := os.Stat(p); err != nil {
		t.Fatalf("transcriptPath が実ファイルを指していない: %v", err)
	}
}
