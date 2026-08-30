package codex

// codex memories のフリート有効化配線（docs/log/39 P4・決着 #4）。
//
// codex の memories は feature flag が stable でありながら **既定 OFF** で、有効化すると
// rollout ごとの抽出（Phase1）と内部サブエージェントによるグローバル統合（Phase2）が
// バックグラウンドで走り、その分のトークンを消費する。決着 #4 が有効化そのものを P4 へ
// 送ったのはこのコストのためで、ここは「トグル 1 個」ではなく **有効化と同時に消費を
// 抑える既定を置く**までを担う。
//
// 有効化の実体は ~/.codex/config.toml の `features.memories = true` で、これは
// `codex features enable memories` と等価。codex 0.145.0 で、我々が書いたこの値を
// `codex features list` が `memories stable true` として読み返すことを実測で確認した。
// CLI を叩かず自前で TOML を編集するのは rate_limit_model_nudge と同じ理由 ——
// 利用者のコメントや [projects.*] の信頼設定をバイト単位で保つため、そして codex
// バイナリの有無・版に依存せず設定を書けるようにするため。
//
// 有効化しても ~/.codex/memories/ はすぐには生えない（次に codex セッションが走った
// ときに codex 自身が作る）。docs/log/39 のルート宣言は RequireDir なので、その間は
// メモリルートとして現れない —— UI はこの「有効だが未生成」を区別して見せる。

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// MemoriesDir は codex が memories ワークスペースを作る場所（docs/log/39 のルート宣言と
// 同じパス。両者がズレると「有効化したのにルートが現れない」になるので定義は 1 箇所）。
func MemoriesDir() string {
	return filepath.Join(paths.HomeDir(), ".codex", "memories")
}

// MemoriesEnabled は config.toml が memories 機能を ON にしているか。codex の既定は
// OFF なので、キーが無い＝無効。
func MemoriesEnabled() bool {
	b, _ := os.ReadFile(codexConfigPath())
	return memoriesEnabled(b)
}

func memoriesEnabled(b []byte) bool {
	v, found := tomlBool(b, "features", "memories")
	return found && v
}

// MemoriesMaterialized は codex が実際にワークスペースを作ったか（＝メモリルートが
// 現れ得るか）。有効化直後は false で、次の codex セッションが走ると true になる。
func MemoriesMaterialized() bool {
	st, err := os.Stat(MemoriesDir())
	return err == nil && st.IsDir()
}

// setMemories は features.memories を書き換える。有効化のときだけ、既に [memories] が
// 無ければ保守的なチューニングを seed する（無効化では触らない —— 一度調整した値を
// トグルの往復で消さないため）。
func setMemories(b []byte, on bool) []byte {
	b = tomlSetBool(b, "features", "memories", on)
	if on {
		b = seedMemoriesTuning(b)
	}
	return b
}

// seedMemoriesTuning は有効化時にだけ書く保守的な既定。[memories] が既にあれば
// 一切触らない —— 利用者（や将来の我々）が調整した値をトグルで上書きしないため。
//
// 振れ幅は「走る量が減る方向」にしか取らない。codex の既定は rollout の idle 6 時間
// 経過で抽出・起動ごと並列 8 本だが、共有ホストで複数ワークスペースが同居する本環境では
// 起動直後にまとめて走られると重い。値は利用者が config.toml で自由に上書きできる。
//
// キー名は codex 0.145.0 の `app-server --strict-config` が全て受理することを実測で
// 確認済み（同じ実行で存在しないキーは "unknown configuration field" で拒否されるので、
// 受理は綴りが実在することの証明になる）。本番の codex 起動は --strict-config を付けない
// ＝**未知のキーは黙って無視される**ので、上流がこれらを改名したら seed は無言で効かなく
// なる。それを CI で捕まえるのが drift_test.go の TestDriftCodexMemoriesTuningKeysValid。
func seedMemoriesTuning(b []byte) []byte {
	if tomlHasSection(b, "memories") {
		return b
	}
	lines := []string{
		"[memories]",
		"# agent-fleet が memories 有効化時に置いた保守的な既定（docs/log/39 P4）。自由に調整可。",
	}
	for _, kv := range memoriesTuning() {
		lines = append(lines, kv[0]+" = "+kv[1])
	}
	return append(b, []byte(tomlAppendPrefix(b)+strings.Join(lines, "\n")+"\n")...)
}

// memoriesTuning は seed する {キー, TOML 値} の表。seedMemoriesTuning が描画に使い、
// drift テストはここから -c memories.<key>=<value> を組み立てて実バイナリに当てる
// —— 期待値を手写しすると「テスト同士が一致するだけ」になるため（drift_test.go の約束）。
func memoriesTuning() [][2]string {
	out := [][2]string{
		{"min_rollout_idle_hours", "12"},
		{"max_rollouts_per_startup", "4"},
	}
	if m := cheapModelFn(); m != "" {
		out = append(out,
			[2]string{"extract_model", strconv.Quote(m)},
			[2]string{"consolidation_model", strconv.Quote(m)})
	}
	return out
}

// cheapModelFn は差し替え点（単体テストは実 codex を起動しない）。
var cheapModelFn = cheapModel

// cheapModel は抽出/統合に充てる安価なモデルを **その時点のカタログから** 選ぶ。
// スラッグを定数で焼き込まないのは、codex のモデルカタログがサーバー側で入れ替わり、
// 古い pin が「存在しないモデル」になってパイプラインごと壊れるため（同じ理由で
// 起動時のモデル選択も動的カタログを引いている）。該当が無ければ何も書かず codex の
// 既定に委ねる —— 自己無効化するのが、当たらない pin を残すより安全。
func cheapModel() string {
	list := Models()
	for _, want := range []string{"nano", "mini"} {
		for _, m := range list {
			if strings.Contains(strings.ToLower(m.ID), want) {
				return m.ID
			}
		}
	}
	return ""
}
