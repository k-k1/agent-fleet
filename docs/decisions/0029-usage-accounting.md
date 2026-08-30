# 0029. 使用量アカウンティング — 機能別トークン台帳を1本持つ

- 状態: **採用（P0.5〜P4 実装済み。P5 MCP ツール以降は未着手）**（2026-07-26）。
- 関連: 設計・実測の本体は [docs/46](../log/46-usage-accounting.md)。
  [0016](0016-i18n.md)（Console 文言は ja/en 両方）、[0021](0021-scheduled-execution.md)（`source=schedule`
  — 本 ADR の `origin=schedule` の一次ソース）、[0022] はエージェントメモリ版管理（未マージ
  `temp/s7in3bh`）、[0027] はオペレーター↔セッション相互作用図（未マージ `temp/sjoad3a`）が
  使用中のため 0029 を採番。

## 背景

フリートは対話セッションの外側でも LLM を撃っている（アシスタントチャット、要約引き継ぎ、
タイトル提案、ブランチ名提案、返信サジェスト、完了報告への自動ターン、ブリッジ応答）。
これらは **現状ゼロ計測** で、実測すると「haiku だから誤差」という直感が外れていた
（タイトル提案1回で入力側 16k トークン・$0.023 — docs/46 §0）。
どの機能がいくら食っているかを1本の物差しで並べる台帳を持つ。

## 決定

### 1. 台帳1行 = LLM 呼び出し1回（または折り込んだセッションの論理ターン1回）

**本文は一切記録しない**（トークン数とメタのみ）。これは非交渉。
保存は `~/.local/share/agent-fleet/usage/raw/YYYY-MM-DD.jsonl`（追記のみ・日次ローテ）。
`~/.local` は Workspace の recreate を跨いで残る。

行のワイヤ形（凍結）— フィールドの意味は docs/46 §2:

```jsonc
{"ts","call","feature","trigger","origin","origin_conv","kind",
 "model","model_raw","model_req","model_src","ref","verb","sidechain","idx",
 "in","out","cread","ccreate","spend","cost_usd","ms","ok","measured"}
```

- **`spend` = in + ccreate + out**（cache_read を含めない）。既存の `get_session_usage` /
  ミラーの ContextBar と同じ定義 — 二つの画面が食い違わないことを優先する。
- **`kind` は「要求」ではなく「実行結果」を書く**。`chatProviderFor` / `oneShotHeadless` は
  使えるバックエンドへフォールバックするので、要求値を書くと claude-less ワークスペースの
  消費が全部 claude に化ける。
- **1呼び出しが複数モデルに割れる場合はモデル毎に1行**、`call`（呼び出し ID）で束ねる。
  集計の `calls` は distinct `call` で数え、**その呼び出しで最も spend の大きいモデル行**に
  1回だけ付ける（同点は生 id 昇順で決定的に）。行順（生 id の綴り順）に付けると `by=model`
  で主力モデルが 0 回と出る。按分にしないのは `calls` を整数の回数として凍結しているから
  — 代表以外の行は spend>0 / calls=0 になるので、平均は `—` で出す（docs/46 §7-5）。
- **`measured` で「0」と「未計測」を区別する**（`exact` | `partial` | `none`）。
  トークンを報告しない CLI でも **回数だけは必ず数える**。

### 2. enum（凍結。Console 側で i18n する）

| 次元 | 値 |
|---|---|
| `feature` | `assistant.chat` / `assistant.ask` / `assistant.autoturn` / `assistant.bridge` / `compact` / `title.session` / `title.chat` / `branch.suggest` / `suggest.session` / `suggest.chat` / `suggest.edit` / `session` / `unknown` |
| `trigger` | `user` / `auto` / `manual` / `schedule` / `operator` / `bridge` / `recovery` |
| `origin` | `user` / `operator` / `schedule` / `handoff` / `unknown` |
| `model_src` | `reported` / `requested` / `default_unknown` |
| `measured` | `exact` / `partial` / `none` |

`feature=unknown` を enum に含めるのは、**新しい補助機能がタグを付け忘れても必ず1行残す**ため。
無記録（＝見えない消費）を作らないことを、タグの正しさより優先する。

### 3. 収集は「ctx タグ ＋ プロバイダ層1点記録」

usage を解析しているのはプロバイダ実装の内側で、そこは既にモデルもトークンも持っている。
足りないのは「何のための呼び出しか」だけなので、`context.Context` に
`usageTag{feature, trigger, ref, verb}` を載せ、**消費源は1箇所1行だけ変える**。
記録はプロバイダ側の解析地点に集約する（claude send/sendStream・codex・opencode・cursor・agy・
`oneShotHeadless`）。

- 記録は各プロバイダ関数の先頭で `defer` に積み、**成功・失敗・早期 return の全経路で必ず1回**走る。
  失敗行は `ok:false` / `measured:"none"` で残る（回数は数える）。
- `oneShotHeadless` は**戻り値を広げず、内部で記録する**（docs/46 §3-a は kind と usage を返す案
  だったが、記録点が関数の内側にある以上、呼び出し側4箇所を触る理由がない）。

### 4. モデル次元は取れた粒度を自己申告する（実測で確定）

実 CLI プローブ（2026-07-26・本ワークスペース）:

| kind | トークン | モデル | コスト | `model_src` |
|---|---|---|---|---|
| claude | `usage.{input,output,cache_read_input,cache_creation_input}_tokens` ◎ | `modelUsage` の**キーが生 id**、値に `canonicalModel` ◎ | `total_cost_usd` / `modelUsage[].costUSD` ◎ **実測** | `reported` |
| codex | `turn.completed.usage.{input,cached_input,cache_write_input,output,reasoning_output}_tokens` ◎ | **どのイベントにも無し**（`thread.started` にも無い） | 無し | `requested` / `default_unknown` |
| cursor | `result.usage.{inputTokens,outputTokens,cacheReadTokens,cacheWriteTokens}` ◎ | **`result` に無し** | 無し | `requested` / `default_unknown` |
| opencode | `step_finish` の `part.tokens` ○ | `modelID` を拾えれば `reported`、無ければ縮退 | 要実測 | 未確定 |
| agy | 無し（素のテキスト出力） | — | 無し | `requested` |

- **表示は `canonicalModel` 相当で束ね、生 id は `model_raw` に残す**（版が上がっても系列が
  分断されない）。claude では `modelUsage` のキーが版込みの生 id、値の `canonicalModel` が正規名。
- **`model_req` を別に持つ**。要求と報告の食い違い（過負荷時のフォールバック、alias 解決先の
  変更、設定ミス）が1列の差分として出る。
- opencode はこのワークスペースが未ログイン（`opencode auth list` = 0 credentials）で
  ライブ検証できていない。**実装は `modelID` を拾い、取れなければ `requested`/`default_unknown`
  へ縮退する**（推測でスキーマを固めない）。

### 5. セッション本体は転写の差分折り込み（watermark）

セッション消費は別プロセス（CLI）が出すので、転写を読んで台帳へ折り込む。

- **論理ターンの通し番号（ordinal）を idx にする**。転写は追記のみなので kind に依らず安定で、
  `(session, idx)` が冪等キーになる。各 kind の `Turn.Idx`（行番号）を使わないのは、
  番号体系が kind ごとに違い watermark が混ざるため。
- **開いている末尾グループは折り込まない**。折り込み後に同じ論理ターンへイベントが追加されると
  入力スナップショットを二重に数えてしまう。次のユーザーターンが来て閉じた時、または
  **セッション削除・アーカイブ時（`includeTrailing`）** に確定させる。
- 契機は **fold-on-read**（`GET /sessions/usage` を 60 秒スロットルで間借り）＋
  **fold-on-delete**。**常駐タイマーは増やさない**（メモリ制約ホスト・docs/26 の教訓）。
- **初回バックフィルは自動**: watermark 0 から走るので、導入時の1回目で過去の全ターンが入る。
  補助呼び出しは記録が無いので遡れない＝導入日以降。
- 二重計上しない: 折り込み対象は**登録済みセッション（`session.Meta`）のみ**で、アシスタント
  会話（`~/.claude/projects` に転写を書く）は含まない。
- **冪等は二段構えにする**（レビュー P1 の続き・2026-07-26）。watermark は書き手側の担保だが、
  行の追記（`raw/*.jsonl`）と watermark（`state.json`）は別ファイルで原子的に書けない — 間で
  落ちれば再追記される。**集計側で `(ref, idx)` の重複を落とす**（docs/46 §7-4）。持つのは
  ref ごとに「計上済み最大 idx ＋観測最大 ts」の1エントリだけで、集合は持たない（idx は 1 から
  単調増加で追記され、重複は必ず末尾の再追記として現れるため）。ts を併せて見るのは slug 再利用
  への保険で、**判定に迷う側は「重複を残す」に倒す** — 重複は raw を見れば分かるが、落とした
  消費は戻らない。既に畳んだ rollup は加算済みで引き算できないので、**版を上げて raw から
  作り直す**（寄与元 raw が prune 済みなら作り直さない）。

### 6. セッションの出自（`origin`）を `session.Meta` に持つ

`trigger`（ターン注入元）とは別軸。「自分で開いたセッション」と「オペレーターが勝手に立てた
セッション」では消費の意味が違う — 後者は自動走行・定時実行と組み合わさると**無人で増える**。

- `Meta.Origin` / `Meta.OriginConv` を追加。**Console = `user`（既定）/ MCP `create_session` =
  `operator`＋作成元の会話 slug / スケジュール = `schedule` / handoff = `handoff`**。
  recreate は元の出自を継承し、**フィールドを持たない既存セッションは `unknown`**
  （`0` でも `user` でもない、を守る）。
- **スケジュールは CP を触らずに導出する**: CP scheduler は既に `source=schedule` /
  `schedule-manual` を create に載せている（docs/38）ので、サーバ側でそこから解決する。
  新しいワイヤ項目を CP に増やさない。
- **補助呼び出しにも焼き込む**: あるセッションのタイトル提案・返信サジェストは、`ref` から
  そのセッションの origin を解決して**行に焼き込む**（セッション削除後も集計が壊れない。
  他の次元と同じ思想）。会話スコープの機能（`assistant.*`）の origin は空 — 出自の軸は
  セッションのものだから。

### 7. §9 の未決 → 決定（すべて推奨案）

1. **コスト**: `cost_usd` は claude で実測が取れた時だけ記録する。UI では
   **「API 換算相当額（claude のみ実測）」と明記した副次表示**に留める（サブスク定額で $ を
   主役にすると誤読を招く）。主指標は `spend`。
2. **セッション本体を含める**: 含める（`feature=session`）。補助だけ見たい時は feature フィルタ。
3. **保持**: raw 90日（`AF_USAGE_RETENTION_DAYS`）・rollup 無期限。
4. **CP 横断集計**: v1 はワークスペース内で閉じる。P6（任意）で集計値のみ CP へ。
5. UI は設定モーダルの新タブ（docs/46 §5 で決着済み。`features/usage/UsageView.tsx` を
   モーダル非依存に切り、ペイン昇格の余地だけ残す）。

### 8. 段階と、rollup で確定した契約

rollup（`usage/rollup/YYYY-MM.json`）と `/usage/series` は **P3 で同時に入れた**
（読み手のいない集計を先に作らない）。P3 で凍結した点:

- **バケットは行の `ts`（消費が起きた時刻）で刻む。追記先のファイル日ではない。**
  セッション折り込みは過去の転写を後から取り込むので、ファイル日で刻むと過去の消費が全部
  「導入日」に積み上がる。実機で最初に踏んだ穴（docs/46 §7-3）。
- 二重計上しない不変条件は1つ: **raw の各ファイル日は「畳み済み」か「未畳み」のどちらか
  一方**。当日は必ず未畳み側。加えて畳んだ消費日ごとに寄与元ファイル日（`src`）を残すので、
  途中で落ちてやり直しても足し込まない。
- **`ref` は rollup のキーにも応答にも入れない**。際限なく増えて「小さい」前提が壊れるのと、
  集計 API から個別の名前を出さない（プライバシー）を兼ねる。ref 単位は raw の保持期間内のみ。
  例外は `(ref, idx)` 重複排除の内部索引（`rollup/state.json`）で、**ref は SHA-256 の前半に
  して平文で残さない** — 重複排除には等値比較しか要らない。集計エントリにも応答にも出ない。
- **`bucket=hour` は raw の保持期間内だけ**。復元できない期間は `truncated: true` で言う
  （黙って短い系列を返すと「消費が無かった」に見える）。同じ理由で **消費の無いバケットも
  ゼロで埋めて返す** — 落とすと離れた日が隣接した棒になり、空白期間が絵から消える。
- **月ファイルが1つでも書けなければ state を進めない**。「畳み済みだが集計が無い」を作らない
  （raw が prune された時点で戻らない）。畳む側は `usageMu` を保持して「その日はもう追記
  されないか」を確かめてから読む — 別ロックのままだと UTC 日跨ぎ直前の追記が黙って消える。
- `coverage` は**データから自動生成**する（手書きの表はドリフトする）。

### 9. Console の色は「検証済みパレット＋固定スロット順」で持つ（P4）

グラフの色は好みではなく検証対象として扱う（dataviz スキルの6チェック）。凍結した規約:

- **カテゴリカルは `tokens.css` の `--viz-1..8`**（light / dark それぞれ別ステップで検証済み）。
  `--viz-other` はグレーの「その他」で、実体には決して割り当てない。**1スロットだけ手で
  いじらない** — 変えるならセット全体で検証をやり直す。
- **色は実体に付く（順位ではなく）**。列挙軸は固定表、無限に増えうる軸（model / origin_conv）は
  **キー名のハッシュ**で決める。フィルタで系列が減っても生き残りの色は動かない。
- **描画は必ずスロット順**。積み上げで触れ合うのは隣接スロットだけになるので、隣接ペアの
  検証がそのまま実際の隣接の保証になる。**8を超えたら「その他」へ畳む**（9色目を作らない）。
- **kind は `--kind-*` のまま塗り替えない**（agent-display-naming の1ソース規約が優先）。
  代わりに**積み順を固定**して隣接ゲートを通す（docs/46 §5-a）。彩度・明度の帯は通らないので、
  **凡例のラベル・ツールチップ・表ビュー**を relief として常設する（色だけに頼らせない）。
- UI は `features/usage/UsageView.tsx` に**モーダル非依存**で置き、設定タブは薄いラッパ。
  ペイン昇格の余地を構造で残す（逆はできない）。

## 結果

- 「どの機能・どのエージェント・どのモデルが食っているか」が1つの物差しで並ぶ。
  最初に暴くはずの穴は docs/46 §2-b の**既定モデル問題**（`AF_TITLE_MODEL_{CODEX,OPENCODE,CURSOR}`
  未設定なら CLI 既定＝通常フラッグシップで補助呼び出しが走る）。
- 計測自体のコストは 0 — 既存の CLI 出力を解析するだけで、追加の LLM 呼び出しはしない。
- **限界**: サブスク枠はトークンから逆算できない（枠の正は使用量チップ＝statusline `rate_limits`）。
  rtk の節約量は別軸（台帳が測るのは rtk 適用「後」の実消費）。
  copilot は `outTok` のみ・kiro/cursor/agy は転写にトークンが無い＝`measured` で正直に出す。
- **プライバシー**: 本文非記録。台帳はワークスペース内に閉じる。
