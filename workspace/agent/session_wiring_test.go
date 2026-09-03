package main

// session_wiring.go の配線が**生きているか**を通しで見る 1 本。
//
// 🔥 `sessionx.Configure` が捕まえるのは**未配線**（nil / 零値）だけで、**間違った配線**は
// 捕まえられない。しかも session 家系の Deps は**同じ型のフィールドが固まっている**:
//
//   - `string` が 11 本（エラーコード）
//   - `func() string` が 2 本（BrowseRoot / ToolchainShellPrefix）
//
// **同じ型どうしを入れ替えても、型検査も `Configure` の reflect 網羅検査も鳴らない。**
// 2026-09-03 に独立 3 例（#312→#319 / #333 / #332）が出て報告様式に格上げされた形で、
// 実測ではどれも**全テスト緑**だった。踏める形を具体的に書くと:
//
//   - `ErrCodePasteTooLarge` と `ErrCodePasteUnsupportedKind` を入れ替える
//     → 画像を貼ると **「大きすぎます」の代わりに「この種別では貼れません」** が出る。
//       どちらも実在するコードなので Console の i18n も引けてしまい、**画面は自然なまま嘘をつく**。
//   - `BrowseRoot` と `ToolchainShellPrefix` を入れ替える
//     → 添付の保存先が**シェルのプレフィクス文字列**になり、貼り付け画像が行方不明になる。
//   - `MaxUploadBytes` を別の `func() int64` にすり替える
//     → 上限が黙って変わる（0 なら全部拒否、巨大なら防波堤が消える）。
//
// 検査の形は 2 つ:
//
//   - 関数は**関数ポインタの同一性**（別の関数や閉包にすり替わっていれば落ちる）
//   - 値は本物の定数と同じであること
//
// そして **Deps のフィールド集合と検査の集合を突き合わせる**ので、フィールドが増えたのに
// 検査を足さなければここが落ちる。

import (
	"reflect"
	"runtime"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

func TestSessionWiringIsLive(t *testing.T) {
	w := sessionx.Wired()

	checks := map[string]func(t *testing.T){
		"EnvOr":            func(t *testing.T) { sameSessionFunc(t, w.EnvOr, envOr) },
		"FirstNonEmpty":    func(t *testing.T) { sameSessionFunc(t, w.FirstNonEmpty, firstNonEmpty) },
		"SplitFrontmatter": func(t *testing.T) { sameSessionFunc(t, w.SplitFrontmatter, splitFrontmatter) },

		"BrowseRoot":     func(t *testing.T) { sameSessionFunc(t, w.BrowseRoot, browseRoot) },
		"MaxUploadBytes": func(t *testing.T) { sameSessionFunc(t, w.MaxUploadBytes, maxUploadBytes) },

		"IsSvnRepo":       func(t *testing.T) { sameSessionFunc(t, w.IsSvnRepo, isSvnRepo) },
		"RepoJobsRunning": func(t *testing.T) { sameSessionFunc(t, w.RepoJobsRunning, repoJobsRunning) },

		"FinalizeSessionUsage":  func(t *testing.T) { sameSessionFunc(t, w.FinalizeSessionUsage, finalizeSessionUsage) },
		"MaybeFoldSessionUsage": func(t *testing.T) { sameSessionFunc(t, w.MaybeFoldSessionUsage, maybeFoldSessionUsage) },

		"RemoveTerminalHistory": func(t *testing.T) { sameSessionFunc(t, w.RemoveTerminalHistory, removeTerminalHistory) },
		"ToolchainShellPrefix":  func(t *testing.T) { sameSessionFunc(t, w.ToolchainShellPrefix, toolchainShellPrefix) },

		"RunOperatorTurn": func(t *testing.T) { sameSessionFunc(t, w.RunOperatorTurn, runOperatorTurn) },

		// MCPConvID だけは本物そのものではない —— var を読む閉包なので、
		// ポインタ同一性では意味が無い。**「今の値が読めること」を振る舞いで見る。**
		// 値で写す配線に戻すと（`MCPConvID: func() string { return "" }` のような固定や、
		// Deps に string を持たせる形）ここが落ちる。
		"MCPConvID": func(t *testing.T) {
			old := mcpConvID
			t.Cleanup(func() { mcpConvID = old })
			mcpConvID = "conv-wiring-probe"
			if got := w.MCPConvID(); got != "conv-wiring-probe" {
				t.Fatalf("MCPConvID() = %q, want %q（配線が値の写しになっていると "+
					"承認プロンプトが配線した瞬間の会話 ID で固まる）", got, "conv-wiring-probe")
			}
		},

		// エラーコードは errcodes.go の**本物と同一の綴り**であること。
		// ここが違うと Console の i18n が引けず、生のコードが画面に出る。
		// **11 本とも同じ型なので、入れ替えを止められるのはこの 11 行だけである。**
		"ErrCodeChatConversationNotFnd": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeChatConversationNotFnd, errCodeChatConversationNotFnd)
		},
		"ErrCodeForkAtUnsupported": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeForkAtUnsupported, errCodeForkAtUnsupported)
		},
		"ErrCodeForkBadAnchor": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeForkBadAnchor, errCodeForkBadAnchor)
		},
		"ErrCodeForkMissingDir": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeForkMissingDir, errCodeForkMissingDir)
		},
		"ErrCodeForkUnsupportedKind": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeForkUnsupportedKind, errCodeForkUnsupportedKind)
		},
		"ErrCodeLocked": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeLocked, errCodeLocked)
		},
		"ErrCodePasteTooLarge": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodePasteTooLarge, errCodePasteTooLarge)
		},
		"ErrCodePasteUnsupportedAgent": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodePasteUnsupportedAgent, errCodePasteUnsupportedAgent)
		},
		"ErrCodePasteUnsupportedKind": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodePasteUnsupportedKind, errCodePasteUnsupportedKind)
		},
		"ErrCodeTitleFeatureDisabled": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeTitleFeatureDisabled, errCodeTitleFeatureDisabled)
		},
		"ErrCodeTitleNoContent": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeTitleNoContent, errCodeTitleNoContent)
		},
	}

	// 検査の集合と Deps のフィールド集合を突き合わせる。**フィールドが増えたら必ずここが
	// 落ちる**ので、「配線は足したが検査は足さなかった」が起きない。
	typ := reflect.TypeOf(w)
	seen := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		seen[name] = true
		if _, ok := checks[name]; !ok {
			t.Errorf("sessionx.Deps.%s の配線を検査していない（フィールドを足したら検査も足すこと）", name)
		}
	}
	for name := range checks {
		if !seen[name] {
			t.Errorf("sessionx.Deps に %s は無い（検査だけが古い）", name)
		}
	}
	for name, run := range checks {
		t.Run(name, run)
	}
}

// TestSessionWiringErrorCodesAreDistinct は、11 本のエラーコードが**互いに違う綴り**で
// あることを見る。
//
// 🔥 これが要る理由: 上の 1 本ずつの突き合わせは「本物と同じか」しか見ないので、
// **errcodes.go 側で 2 つの定数が同じ文字列になっていたら素通りする**。そうなると
// 入れ替えの検査そのものが無力になる（どちらに配線しても一致してしまう）。
// 実際、貼り付け系 3 本とフォーク系 4 本は綴りが似ており、コピー&ペーストで
// 同じ値になる事故が起こりやすい。
func TestSessionWiringErrorCodesAreDistinct(t *testing.T) {
	w := sessionx.Wired()
	seen := map[string]string{}
	typ := reflect.TypeOf(w)
	val := reflect.ValueOf(w)
	n := 0
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type.Kind() != reflect.String {
			continue
		}
		n++
		code := val.Field(i).String()
		if code == "" {
			t.Errorf("%s が空（Console へ空のコードが届き i18n が引けない）", f.Name)
			continue
		}
		if prev, dup := seen[code]; dup {
			t.Errorf("%s と %s が同じコード %q を持つ＝入れ替えの検査が効かなくなる",
				prev, f.Name, code)
		}
		seen[code] = f.Name
	}
	// 🔥 走査本数の下限（#320 の「1 件も見つからなければ何も検査しない」対策）。
	if n != 11 {
		t.Fatalf("string のフィールドを %d 本しか見ていない（want 11）＝この検査が無言化している", n)
	}
}

// sameSessionFunc は「その関数そのものが配線されている」ことを見る。閉包や別の関数に
// すり替わっていれば、コードポインタが違うので落ちる。
func sameSessionFunc(t *testing.T, got, want any) {
	t.Helper()
	g, w := reflect.ValueOf(got).Pointer(), reflect.ValueOf(want).Pointer()
	if g != w {
		t.Fatalf("配線先が違う: got %s, want %s", sessionFuncName(g), sessionFuncName(w))
	}
}

func sessionFuncName(pc uintptr) string {
	if f := runtime.FuncForPC(pc); f != nil {
		return f.Name()
	}
	return "?"
}

func sameSessionCode(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("エラーコード = %q, want %q", got, want)
	}
}
