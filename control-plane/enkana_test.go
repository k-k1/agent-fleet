package main

import "testing"

func TestEnglishToKana(t *testing.T) {
	// 代表語の実出力をログしつつ、辞書ロード・変換が動くことを確認する。
	words := []string{
		"hello", "world", "cat", "dog", "coffee", "google", "python",
		"apple", "computer", "banana", "music", "language", "voice",
		"the", "through", "water", "orange", "session", "agent",
	}
	for _, w := range words {
		got := englishToKana(w)
		if got == "" || got == w {
			t.Errorf("%q -> %q (変換されていない)", w, got)
		}
		t.Logf("%-12s -> %s", w, got)
	}
}

func TestEnglishToKanaMixed(t *testing.T) {
	// 日英混在: 日本語・記号・数字はそのまま、英単語のみ変換。
	got := englishToKana("これは pen です。 iPhone を3個。")
	t.Logf("mixed -> %s", got)
	if !containsJP(got, "これは") || !containsJP(got, "です") {
		t.Errorf("日本語が壊れた: %q", got)
	}
	if containsASCIILetters(got) {
		t.Errorf("英字が残っている: %q", got)
	}
}

func TestAcronymAndCamel(t *testing.T) {
	cases := map[string]string{
		"AWS":         "acronym",
		"API":         "acronym",
		"getUserById": "camel",
	}
	for w := range cases {
		got := englishToKana(w)
		t.Logf("%-12s -> %s", w, got)
		if got == w || got == "" {
			t.Errorf("%q が変換されていない: %q", w, got)
		}
		if containsASCIILetters(got) {
			t.Errorf("%q に英字が残っている: %q", w, got)
		}
	}
}

func TestTechTerms(t *testing.T) {
	// AWS/開発ジャルゴンのオーバーライド＋略語＋数字ルールの網羅確認。
	want := map[string]string{
		"EC2":        "イーシーツー",
		"S3":         "エススリー",
		"Dao":        "ダオ",
		"nginx":      "エンジンエックス",
		"json":       "ジェイソン",
		"IAM":        "アイアム",
		"lambda":     "ラムダ",
		"DynamoDB":   "ダイナモディービー",
		"kubernetes": "クーバネティス",
		"IPv6":       "アイピーブイシックス",
		"Google":     "グーグル",
		"Figma":      "フィグマ",
		"Jira":       "ジラ",
		"GitHub":     "ギットハブ",
		"Slack":      "スラック",
		"Excel":      "エクセル",
	}
	for in, exp := range want {
		if got := englishToKana(in); got != exp {
			t.Errorf("%q -> %q, want %q", in, got, exp)
		}
	}
	// 単独の数字は日本語読みのまま（英字と地続きの数字だけ英語読み）。
	if got := englishToKana("3個"); got != "3個" {
		t.Errorf("単独数字 3個 -> %q, want 3個", got)
	}
}

func TestCorpusTerms(t *testing.T) {
	// 過去セッションのコーパスから追加した未カバー語（enkana_dict.go 後半）。
	want := map[string]string{
		"config":   "コンフィグ",
		"grep":     "グレップ",
		"tmux":     "ティーマックス",
		"worktree": "ワークツリー",
		"refactor": "リファクター",
		"handoff":  "ハンドオフ",
		"opencode": "オープンコード",
		"codex":    "コーデックス",
		"voicevox": "ボイスボックス",
		"zundamon": "ずんだもん",
		// 小文字略語は大文字表記でも同じ読み（convertWord が小文字化して techKana を引く）。
		"mcp": "エムシーピー",
		"MCP": "エムシーピー",
		"css": "シーエスエス",
		"CSS": "シーエスエス",
		"svg": "エスブイジー",
	}
	for in, exp := range want {
		if got := englishToKana(in); got != exp {
			t.Errorf("%q -> %q, want %q", in, got, exp)
		}
	}
}

func TestProtocolAndBizAcronyms(t *testing.T) {
	// ネットワーク・開発/ビジネス略語の追加分。CMUdict に実在語として載っていて誤読される
	// もの（nat/sap/roi/seo 等）が上書きで正しく読めているかも確認する。
	want := map[string]string{
		"https":  "エイチティーティーピーエス",
		"tcp":    "ティーシーピー",
		"vpn":    "ブイピーエヌ",
		"nat":    "エヌエーティー",
		"sap":    "エスエーピー",
		"sip":    "エスアイピー",
		"arp":    "エーアールピー",
		"roi":    "アールオーアイ",
		"seo":    "エスイーオー",
		"devops": "デブオプス",
		"saas":   "サース",
		"ci":     "シーアイ",
	}
	for in, exp := range want {
		if got := englishToKana(in); got != exp {
			t.Errorf("%q -> %q, want %q", in, got, exp)
		}
	}
}

func TestNumeronymLikeCombos(t *testing.T) {
	// 略語＋数字のトークン全体一致（EC2/route53 と同じ扱い）。
	want := map[string]string{
		"b2b":  "ビーツービー",
		"p2p":  "ピーツーピー",
		"web3": "ウェブスリー",
		"gpt4": "ジーピーティーフォー",
	}
	for in, exp := range want {
		if got := englishToKana(in); got != exp {
			t.Errorf("%q -> %q, want %q", in, got, exp)
		}
	}
}

func TestCompanyAndToolNames(t *testing.T) {
	// 企業名・クラウド/インフラツール・言語フレームワークの追加分。
	want := map[string]string{
		"salesforce":  "セールスフォース",
		"ubuntu":      "ウブントゥ",
		"mercari":     "メルカリ",
		"paypay":      "ペイペイ",
		"jaeger":      "イェーガー",
		"circleci":    "サークルシーアイ",
		"django":      "ジャンゴ",
		"webassembly": "ウェブアセンブリ",
		"wasm":        "ワズム",
	}
	for in, exp := range want {
		if got := englishToKana(in); got != exp {
			t.Errorf("%q -> %q, want %q", in, got, exp)
		}
	}
}

func TestGeneralDevWords(t *testing.T) {
	// 実機フィードバックの読み希望（version/message/session/setting/status/daemon/console 等）。
	// CMUdict の音写が慣用カタカナから外れる語を慣用読みで固定できているか。
	want := map[string]string{
		"version":  "バージョン",
		"revision": "リビジョン",
		"minor":    "マイナー",
		"major":    "メジャー",
		"thread":   "スレッド",
		"messages": "メッセージズ",
		"session":  "セッション",
		"settings": "セッティングス",
		"status":   "ステータス",
		"daemon":   "デーモン",
		"resume":   "レジューム",
		"docs":     "ドックス",
		"approval": "アプルーバル",
		"tests":    "テスツ",
		"console":  "コンソール",
		"httptest": "エイチティーティーピーテスト",
	}
	for in, exp := range want {
		if got := englishToKana(in); got != exp {
			t.Errorf("%q -> %q, want %q", in, got, exp)
		}
	}
}

func TestUnitsAndSignals(t *testing.T) {
	// 容量単位（二進 MiB / 十進 MB）とシグナル名。キーは小文字化されるので大文字表記でも引く。
	want := map[string]string{
		"MiB":     "メビバイト",
		"GiB":     "ギビバイト",
		"MB":      "メガバイト",
		"GB":      "ギガバイト",
		"sigkill": "シグキル",
		"sigterm": "シグターム",
		"sigint":  "シグイント",
		"SIGKILL": "シグキル",
		"sigusr1": "シグユーザーワン",
	}
	for in, exp := range want {
		if got := englishToKana(in); got != exp {
			t.Errorf("%q -> %q, want %q", in, got, exp)
		}
	}
}

func TestUnixOperatorTerms(t *testing.T) {
	// UNIX コマンド / オペレータ用語（提案分）。慣用の発音で固定できているか。
	want := map[string]string{
		"chmod":     "チェンジモッド",
		"chown":     "チャウン",
		"sudo":      "スードゥー",
		"sudoers":   "スードゥアーズ",
		"bashrc":    "バッシュアールシー",
		"awk":       "オーク",
		"sed":       "セド",
		"curl":      "カール",
		"rsync":     "アールシンク",
		"systemctl": "システムコントロール",
		"mkdir":     "メイクディレクトリ",
		"rmdir":     "アールエムディーアイアール",
		"init":      "イニット",
		"ripgrep":   "リップグレップ",
	}
	for in, exp := range want {
		if got := englishToKana(in); got != exp {
			t.Errorf("%q -> %q, want %q", in, got, exp)
		}
	}
}

func TestSingleUpperLetter(t *testing.T) {
	// 単独の大文字 1 文字（案A・パターンB 等）は CMUdict の実在語（a/i/o が冠詞・代名詞
	// として載っている）に化けて誤読される（例: "A"→"ア"）ため、英字名読みに固定する。
	// 実機フィードバックで "案A" が「あんあ」と読まれる不具合として発覚。
	want := map[string]string{
		"A": "エー",
		"B": "ビー",
		"I": "アイ",
		"O": "オー",
	}
	for in, exp := range want {
		if got := englishToKana(in); got != exp {
			t.Errorf("%q -> %q, want %q", in, got, exp)
		}
	}
	// 小文字の単独文字（英文中の "a" 等）は対象外＝CMUdict の英単語読みのまま
	// （文字名読みに変わらないことを確認）。
	if got := englishToKana("a"); got == "エー" {
		t.Errorf("小文字 a が文字名読みになった: %q", got)
	}
}

func TestAgentManageFamily(t *testing.T) {
	// 実機フィードバック: "agent"/"managed" が CMUdict のまま (エイジャント/マナジド) で
	// 誤読されるため上書き。同根の manage 系も併せて確認。
	want := map[string]string{
		"agent":      "エージェント",
		"Agent":      "エージェント",
		"managed":    "マネージド",
		"manage":     "マネージ",
		"manager":    "マネージャー",
		"management": "マネジメント",
		"image":      "イメージ",
		"images":     "イメージズ",
		"Image":      "イメージ",
	}
	for in, exp := range want {
		if got := englishToKana(in); got != exp {
			t.Errorf("%q -> %q, want %q", in, got, exp)
		}
	}
}

func TestReadingOverrides(t *testing.T) {
	// 実機フィードバックの誤読修正。DOM は CMUdict 実在語 dom→ダム に化けるので慣用読み、
	// good は末尾 D が促音化せず グド になるので固定、well は変異形 "ウェルル" 対策。
	want := map[string]string{
		"DOM":  "ドム",
		"dom":  "ドム",
		"Dom":  "ドム",
		"good": "グッド",
		"Good": "グッド",
		"well": "ウェル",
		"Well": "ウェル",
	}
	for in, exp := range want {
		if got := englishToKana(in); got != exp {
			t.Errorf("%q -> %q, want %q", in, got, exp)
		}
	}
	// "dom" は完全トークン一致のみ（部分一致で kingdom/random/freedom を壊さない）。
	for _, w := range []string{"kingdom", "random", "freedom"} {
		if got := englishToKana(w); got == "ドム" || containsASCIILetters(got) {
			t.Errorf("%q が誤変換された: %q", w, got)
		}
	}
}

func TestReportedWebTermReadings(t *testing.T) {
	// 実機メモで報告された Web/API 用語の慣用読みを固定する。
	want := map[string]string{
		"body":      "ボディ",
		"query":     "クエリ",
		"parameter": "パラメータ",
		"iframe":    "アイフレーム",
		"preview":   "プレビュー",
		"origin":    "オリジン",
		"cookie":    "クッキー",
		"httpapi":   "エイチティーティーピーエーピーアイ",
	}
	for in, exp := range want {
		if got := englishToKana(in); got != exp {
			t.Errorf("%q -> %q, want %q", in, got, exp)
		}
	}
}

func TestContractions(t *testing.T) {
	// アポストロフィ入りの短縮形。従来は ' で語が分断され "イット'エス" 等に誤読されていた。
	// ASCII(') とタイプグラフィ(’) の両方を同一視する。
	want := map[string]string{
		"It's":     "イッツ",
		"it's":     "イッツ",
		"It’s":     "イッツ", // U+2019
		"don't":    "ドント",
		"can't":    "キャント",
		"won't":    "ウォント",
		"isn't":    "イズント",
		"doesn't":  "ダズント",
		"I'm":      "アイム",
		"let's":    "レッツ",
		"that's":   "ザッツ",
		"we'll":    "ウィール",
		"wouldn't": "ウドゥント",
	}
	for in, exp := range want {
		if got := englishToKana(in); got != exp {
			t.Errorf("%q -> %q, want %q", in, got, exp)
		}
	}
	// 所有格 's は「元の語＋ズ」でフォールバック（辞書 techKana も経由する）。
	poss := map[string]string{
		"React's": "リアクトズ",
		"user's":  "ユーザーズ",
		"API's":   "エーピーアイズ", // API=エーピーアイ(techKana) + ズ
	}
	for in, exp := range poss {
		if got := englishToKana(in); got != exp {
			t.Errorf("possessive %q -> %q, want %q", in, got, exp)
		}
	}
	// 文中でも分断されず、周囲の語・句読点は保たれる。
	if got := englishToKana("It's good, don't worry."); containsASCIILetters(got) {
		t.Errorf("英字が残った: %q", got)
	}
}

func containsASCIILetters(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}

func containsJP(s, sub string) bool {
	return len(sub) > 0 && (len(s) >= len(sub)) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
