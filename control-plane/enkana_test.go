package main

import "testing"

func TestEnglishToKana(t *testing.T) {
	// Logs the actual output for a spread of words while checking that the dictionary
	// loads and the conversion runs at all.
	words := []string{
		"hello", "world", "cat", "dog", "coffee", "google", "python",
		"apple", "computer", "banana", "music", "language", "voice",
		"the", "through", "water", "orange", "session", "agent",
	}
	for _, w := range words {
		got := englishToKana(w)
		if got == "" || got == w {
			t.Errorf("%q -> %q (not converted)", w, got)
		}
		t.Logf("%-12s -> %s", w, got)
	}
}

func TestEnglishToKanaMixed(t *testing.T) {
	// Mixed input: Japanese, punctuation and digits pass through untouched; only the
	// English words are converted.
	got := englishToKana("これは pen です。 iPhone を3個。")
	t.Logf("mixed -> %s", got)
	if !containsJP(got, "これは") || !containsJP(got, "です") {
		t.Errorf("the Japanese was mangled: %q", got)
	}
	if containsASCIILetters(got) {
		t.Errorf("ASCII letters are left over: %q", got)
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
			t.Errorf("%q was not converted: %q", w, got)
		}
		if containsASCIILetters(got) {
			t.Errorf("%q still has ASCII letters left: %q", w, got)
		}
	}
}

func TestTechTerms(t *testing.T) {
	// Covers the AWS / dev-jargon overrides together with the acronym and digit rules.
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
	// A standalone digit keeps its Japanese reading; only a digit that runs straight into
	// letters is read as English.
	if got := englishToKana("3個"); got != "3個" {
		t.Errorf("standalone digit: 3個 -> %q, want 3個", got)
	}
}

func TestCorpusTerms(t *testing.T) {
	// Words the base dictionary does not cover (second half of enkana_dict.go).
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
		// A lower-case acronym reads the same in upper case: convertWord lower-cases
		// before looking up techKana.
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
	// Network and dev/business acronyms. Some of them (nat, sap, roi, seo) also exist in
	// CMUdict as ordinary words and are misread without an override, so the override is
	// checked here as well.
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
	// Acronym-plus-digit forms match on the whole token, the same as EC2 / route53.
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
	// Company names, cloud/infrastructure tools and language frameworks.
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
	// Everyday development words whose CMUdict transcription drifts away from the katakana
	// people actually use; each is pinned to the conventional reading.
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
	// Capacity units (binary MiB, decimal MB) and signal names. Keys are lower-cased on
	// lookup, so an upper-case spelling resolves to the same entry.
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
	// UNIX commands and operator jargon, pinned to how they are actually pronounced rather
	// than how they are spelled.
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
	// A standalone upper-case letter (option A, pattern B) otherwise hits a real CMUdict
	// entry — a, i and o are in there as an article and as pronouns — and is read as that
	// word instead of as a letter, which makes a label like "option A" unintelligible. So
	// single upper-case letters are pinned to the letter's own name.
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
	// A standalone lower-case letter (the "a" of ordinary prose) is out of scope and keeps
	// its CMUdict word reading rather than becoming a letter name.
	if got := englishToKana("a"); got == "エー" {
		t.Errorf("lower-case a was read as a letter name: %q", got)
	}
}

func TestAgentManageFamily(t *testing.T) {
	// CMUdict's own reading of "agent" and "managed" is not the katakana anyone recognizes,
	// so both are overridden; the rest of the manage family is checked alongside them.
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
	// Three misreadings pinned to the conventional form: DOM collides with the real CMUdict
	// word "dom", good loses the geminate before its final D, and well picks up a variant
	// with a doubled L.
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
	// "dom" matches only as a whole token, so a substring match cannot break kingdom,
	// random or freedom.
	for _, w := range []string{"kingdom", "random", "freedom"} {
		if got := englishToKana(w); got == "ドム" || containsASCIILetters(got) {
			t.Errorf("%q was mis-converted: %q", w, got)
		}
	}
}

func TestReportedWebTermReadings(t *testing.T) {
	// Pins the conventional reading of the common Web/API terms.
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
	// Contractions: the apostrophe must not split the word into two tokens, which reads the
	// contraction out as its two halves. The ASCII ' and the typographic one are treated
	// as the same character.
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
	// A possessive 's falls back to the base word's reading plus the plural-style suffix,
	// with the base still resolved through techKana.
	poss := map[string]string{
		"React's": "リアクトズ",
		"user's":  "ユーザーズ",
		"API's":   "エーピーアイズ", // API from techKana, plus the suffix
	}
	for in, exp := range poss {
		if got := englishToKana(in); got != exp {
			t.Errorf("possessive %q -> %q, want %q", in, got, exp)
		}
	}
	// Inside a sentence it is not split either, and the surrounding words and punctuation
	// survive.
	if got := englishToKana("It's good, don't worry."); containsASCIILetters(got) {
		t.Errorf("ASCII letters were left behind: %q", got)
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
