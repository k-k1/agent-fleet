# docs/46 — 使用量アカウンティング（機能別トークン計測とグラフ化）

フリート内で **何にトークンが使われているか** を機能ごとに計測し、Console でグラフ化する。
対話セッションだけでなく、フリート自身が裏で撃っている補助 LLM 呼び出し（アシスタントチャット、
要約引き継ぎ、タイトル提案、返信サジェスト、報告への自動ターン…）を **同じ物差しで並べる** のが主眼。

決定は ADR 0029（本 doc の確定後に起票）。ステータス: **設計のみ・実装未着手**。

## 0. なぜ要るのか（実測が先）

「補助呼び出しは haiku だから誤差」という直感は **外れている**。フリートの
`oneShotHeadless`（タイトル提案）と同じ引数を実測した（claude-code 2.1.x / haiku / 2026-07-25）:

```
$ claude -p --no-session-persistence --output-format json --dangerously-skip-permissions \
    --append-system-prompt "<titleSuggestPersona>" --model haiku \
    --disallowedTools Agent Task Workflow Bash Edit Write MultiEdit NotebookEdit
  ← プロンプト本体は会話ログ2行（~50 tokens）
  → cost $0.0233 / in 9 + cache_create 10,015 + cache_read 6,002 + out 533 / 6.2s
```

**プロンプト本体が 50 トークンでも、1回あたり入力側 16k トークン・$0.023 かかる**（CLI のシステム
プロンプトとツールスキーマが固定オーバーヘッドとして毎回乗る）。タイトル自動提案はセッション毎に
自動発火し、報告への自動ターンやブリッジ経由の応答はループしうる。**「どの機能がいくら食ったか」を
測る価値がある** というのが本設計の出発点。

同時に判明した重要な事実:

- `claude -p --output-format json` は **`total_cost_usd` と `modelUsage[model].costUSD` を実測で返す**。
  claude 経路はコストが推定ではなく **実測** で取れる（価格表を持たなくてよい）。
- `duration_ms` / `duration_api_ms` も返る（レイテンシも同じ台帳で扱える）。

## 1. 消費源の全数（コード上の実在箇所）

### 1-a. 補助 LLM 呼び出し（フリート自身が撃つ = 現状ゼロ計測）

| feature | trigger | 実在箇所 | 発火 |
|---|---|---|---|
| `assistant.chat` | user / schedule / operator | `chat_handlers.go:437`(send) / `:429`(stream) | 利用者のチャット1ターン |
| `assistant.ask` | user | `chat_handlers.go:203` | 単発アドバイザリ（非永続） |
| `assistant.autoturn` | auto | `chat_report.go:490,496` | セッション完了報告への自動ターン（連鎖しうる） |
| `assistant.bridge` | bridge | `bridge_operator.go:70,76` | Discord/Slack からのオペレーター応答 |
| `compact` | manual / auto / recovery | `chat_compact.go:65` | 要約引き継ぎ（docs/33 第2〜4段） |
| `title.session` | auto / manual | `session_title.go:149` | セッション件名の自動提案 |
| `title.chat` | auto / manual | `chat_title.go:71` | 会話タイトル提案 |
| `branch.suggest` | manual | `session_title.go:417` | ブランチ名提案 |
| `suggest.session` | manual | `session_suggest_reply.go:113` | ミラーの ✨ 返信候補 |
| `suggest.chat` | manual | `chat_suggest_reply.go:45` | チャットの ✨ 返信候補 |

`assistant.chat` は `SeedVerb`（`translate` / `summarize`）を **サブ次元** として持つ（Files 由来の
翻訳/要約チャットを独立カテゴリとして見たいので、feature を増やさず verb で割る）。

### 1-a-2. 補助呼び出しの固定オーバーヘッド（実測・**測る前に塞げる穴**）

タイトル提案の1回を条件を変えて実測した（haiku・同一プロンプト・2026-07-25 / claude-code 2.1.x）。
「入力側」= `in + cache_create + cache_read`。

| # | 構成 | 入力側 | out | cost | 所要 | 結果 |
|---|---|---|---|---|---|---|
| A | 素の `claude -p`（既定ツール・persona 無し） | 26,877 | 61 | $0.0228 | 1.8s | — |
| **B** | **現状のフリート**（7ツール disallow + `--append-system-prompt`） | **16,026** | **533** | **$0.0233** | **6.2s** | 使用量データの台帳設計 |
| C | B + `--tools ""`（全ツール無効） | 10,392 | 512 | $0.0233 | 6.2s | 使用量グラフの設計 |
| D | `--system-prompt`（**置換**）+ `--tools ""` | 4,352 | 492 | $0.0112 | 5.8s | 使用量グラフ作成 |
| **E** | **D + `MAX_THINKING_TOKENS=0`** | **4,278** | **13** | **$0.0086** | **1.2s** | 使用量グラフの設計 |

**B → E で入力側 −73% / 出力 −97% / コスト −63% / レイテンシ −80%。出力品質は同等**（どれも
妥当な件名を返している）。内訳の読み方:

- **`--append-system-prompt` は「足す」= Claude Code の既定システムプロンプト＋ツール定義が丸ごと
  乗ったまま**。`--tools ""` で 5.6k、`--system-prompt` 置換でさらに 6k 削れる。タイトル/ブランチ名/
  返信候補は **純粋な分類・整形タスク** で、コーディングエージェントの既定人格は要らない。
- **out 533 → 13 の正体は思考トークン**。18文字の件名を出すのに毎回 ~500 トークン考えていた
  （所要 6.2s の主因でもある）。`MAX_THINKING_TOKENS=0` で消える。
- cost 列は cache_create / cache_read の状態で振れる（B は cr 6,002 = キャッシュ有利な条件で計測）
  ので、**トークン数の比較が正**。コスト差はむしろ控えめな見積り。

適用範囲（重要・ここを外すと機能が壊れる）:

| 呼び出し | `--tools ""` | `--system-prompt` 置換 | 思考 0 |
|---|---|---|---|
| `oneShotHeadless`（title / branch / suggest） | ✅ | ✅ | ✅ |
| `assistant.chat` / `ask` / `autoturn` / `bridge` | ❌ ツールと af MCP が本体 | ❌ | ❌ 推論が要る |
| `compact`（要約） | — 現行セッション上で撃つので別 | — | 要検討 |

claude 固有の手札である点にも注意（codex は `-c` 設定、opencode / cursor はプロンプト前置きのみで
同等の「既定プロンプト置換」が無い）。**まず claude 経路だけ塞ぐ**のが費用対効果で最良。

### 1-b. 対話セッション本体（既に転写に記録されている）

`transcript.Turn` が `InTok/OutTok/CacheRead/CacheCreate` + `TS` + `Model` + `Sidechain` を持つ
（`session_usage.go` の累積集計と同じソース）。feature=`session`、サブ次元に
`sidechain`（サブエージェント / Workflow の消費）を持たせると「本体 vs 委譲」も割れる。

### 1-c. 計測できないもの（正直に出す）

| kind | 対話セッション | 補助呼び出し | 備考 |
|---|---|---|---|
| claude | ◎ 完全（in/out/cache） | ◎ 完全 + **コスト実測** | |
| codex | ◎ 完全（`last_token_usage`＝ターン差分） | ◎ tokens（コスト無し） | |
| opencode | ◎ 完全（message.data.tokens） | ◎ tokens（`cost` 列の有無は要実測） | |
| copilot | △ `outTok` のみ | △ 同左 | in/cache は転写に無い |
| kiro | △ ライブ ACP の context% + credits のみ | △ | 転写にトークン無し |
| cursor | ✕ 転写に無し（`-p` の `result.usage` のみ） | ◎ tokens（`-p` 経路なので取れる） | docs/40 §使用量表示プローブ |
| agy | ✕ | ✕（出力が素のテキスト） | 呼び出し回数のみ |

**「0」と「未計測」を絶対に混同させない。** 行に `measured` を持たせ、UI は未計測分を
グレーのハッチと注記で別枠表示する（回数だけは常に数える）。

## 2. データモデル（台帳1行 = LLM 呼び出し1回、または折り込んだセッションターン1回）

プロンプト本文・応答本文は **一切記録しない**（トークン数・メタのみ）。これは非交渉の原則。

```jsonc
{
  "ts": "2026-07-25T09:31:07Z",   // 呼び出し完了時刻（UTC）
  "call": "9f2c…",                // 呼び出し ID（1呼び出しが複数モデル行に割れる時に束ねる）
  "feature": "title.session",     // 1-a/1-b の列挙値（固定 enum・Console 側で i18n）
  "trigger": "auto",              // user | auto | manual | schedule | operator | bridge | recovery
  "kind": "claude",               // 実際に実行したエージェント種別（要求ではなく実行結果）
  "model": "claude-haiku-4-5",    // 実行側が報告した正規モデル（§2-b）
  "model_raw": "claude-haiku-4-5-20251001", // 報告された生の id（版込み）
  "model_req": "haiku",           // 要求した値（model と食い違う時だけ＝フォールバック検知）
  "model_src": "reported",        // reported | requested | default_unknown
  "ref": "s7in3bh",               // セッション名 or 会話 id（無ければ空）
  "verb": "",                     // assistant.chat のサブ次元（translate|summarize）
  "sidechain": false,             // feature=session のサブ次元
  "in": 9, "out": 533, "cread": 6002, "ccreate": 10015,
  "spend": 10557,                 // = in + ccreate + out（session_usage.go の既存定義を踏襲）
  "cost_usd": 0.0233,             // 実測が取れた時だけ（claude）。無ければ省略
  "ms": 6241,
  "ok": true,
  "measured": "exact"             // exact | partial（outのみ等） | none（回数のみ）
}
```

**エージェント種別（`kind`）は「要求」ではなく「実行結果」を書く。** `chatProviderFor` は
指定エージェントが使えなければ他へフォールバックし、`oneShotHeadless` は claude → codex →
opencode → cursor → agy の順で **最初に使えるもの** を選ぶ。要求値を記録すると
「claude-less ワークスペースの消費が全部 claude に化ける」。実行分岐が返した kind を使う
（チャット側は既存の `chatProviderKind` がまさにこれ。`oneShotHeadless` は現在どれで撃ったかを
返さないので**戻り値に足す**）。

- **spend**（cache_read を含めない）を主指標にする。既存の `get_session_usage` / ミラーの
  ContextBar と同じ定義で、二つの画面が食い違わないため。cache_read は別系列で併記。
- **cost は補助指標**。サブスク定額の claude で $ を主役に出すと誤読を招くので、UI では
  「API 換算相当額（claude のみ実測）」と明記した副次表示に留める。

### 2-b. モデル次元の実力（provider ごとに違う）

「エージェント別」は分岐から確実に取れるが、**「モデル別」は provider によって精度が落ちる**。
台帳は取れた粒度をそのまま `model_src` で自己申告する。

| kind | 補助呼び出しでのモデル取得 | `model_src` | 備考 |
|---|---|---|---|
| claude | ◎ `modelUsage` が **モデル毎のトークン内訳＋`costUSD`＋`canonicalModel`** を返す | `reported` | 実測済み（§0）。1呼び出しが複数モデルに割れることがある → **モデル毎に1行**、`call` で束ねる |
| codex | △ `turn.completed` に model 無し。`-m` を渡した時はその値 | `requested` / `default_unknown` | 既定モデルは `~/.codex/config.toml` 側。要プローブ（`thread.started` に model が乗るか） |
| opencode | ○ DB は `message.data.modelID` を持つ。run ストリームの `step_finish` にも乗る可能性が高い | `reported` 見込み | 要実測。取れなければ requested に縮退 |
| cursor | ✕ `result` に model 無し（docs/40 probe 9）。`--model` を渡した時のみ | `requested` / `default_unknown` | `auto` 指定時は解決後モデル不明 |
| agy | ✕ | `requested` | トークン自体が取れない |
| 対話セッション | ◎ `transcript.Turn.Model`（claude/codex/opencode/copilot） | `reported` | cursor/agy/kiro は転写に無し |

**版ドリフト対策**: 表示は `canonicalModel` 相当（`claude-haiku-4-5`）で束ね、生 id
（`claude-haiku-4-5-20251001`）は `model_raw` に残す。版が上がっても系列が分断されない。

**`model_req` を別に持つ理由**: 要求と報告の食い違いは事故のシグナル。過負荷時のモデル
フォールバック、alias の解決先変更、設定ミスが「1列の差分」として出る。

> **この次元が最初に暴くはずの実在の穴**: `oneShotHeadless` は claude だけ既定 `haiku`
> （`titleModel()`）、agy だけ既定 Flash。**codex / opencode / cursor は
> `AF_TITLE_MODEL_{CODEX,OPENCODE,CURSOR}` が未設定なら `-m` を渡さない** ＝ その CLI の
> 既定（通常はフラッグシップ）でタイトル提案や返信サジェストが走る。claude-less
> ワークスペースでは補助呼び出しが最上位モデルに流れている可能性が高い。グラフの
> 「機能 × モデル」でこれが一目で出る（対処は測ってから: 安価モデルを既定ピンする）。

## 3. 収集アーキテクチャ

### 3-a. 補助呼び出し = ctx タグ + プロバイダ層1点記録

usage を解析しているのは **プロバイダ実装の内側**（`claudeChat.send` / `parseCodexExecEvents` /
`parseOpencodeRunEvents` / `cursorChat.send` / `oneShotHeadless`）。ここは既に model もトークンも
持っているので、**足りないのは「何のための呼び出しか」だけ**。

→ `context.Context` に `usageTag{feature, trigger, ref, verb}` を載せ、呼び出し側13箇所を
**1行ずつ**変更する。記録はプロバイダ側の usage 解析地点に集約（5箇所 + oneShotHeadless）。
新しい補助機能を足したときにタグを付け忘れても `feature="unknown"` として必ず1行残る
（無記録＝見えない、を作らない）。

必要な既存コードの微修正:

- `claudeUsage` に `output_tokens` フィールドを足す（現在はコンテキスト占有しか見ていないので無い）。
- `claudeResult` / `streamLine` に `total_cost_usd` を足し、`modelUsage` を**モデル別行**として出す
  （`claudeModelUsage` は今 `contextWindow` しか見ていないので、トークン4種＋`costUSD`＋
  `canonicalModel` を足す）。
- `oneShotHeadless` は現在 usage も選択バックエンドも捨てている（`_` で受け、戻り値は文字列だけ）。
  **実行した kind と usage を返すように広げる**（`model_src` の判定もここ）。
- `parseCodexExecEvents` / `parseOpencodeRunEvents` に model フィールドを足す（乗っていれば
  `reported`、無ければ `requested` へ縮退）。乗っているかは P1 のプローブで確定する。

### 3-b. セッションターン = 転写の差分折り込み（watermark）

セッション本体は別プロセス（CLI）が消費するので、転写を読んで台帳へ折り込む。

- 状態: `usage/state.json` に `{session: {path, size, lastIdx, lastTS}}`。
  **差分のみ**を折り込み、`(session, idx)` で冪等。
- 折り込み契機: **fold-on-read**（Console が系列を要求した時、最短60秒間隔でスロットル）
  + **fold-on-delete**（セッション削除時に取りこぼしを確定）+ 既存の完了報告 POST 契機。
  常駐タイマーを増やさない（メモリ制約ホスト。docs/26 の教訓）。
- **初回バックフィル**: 転写は過去分がそのまま残っているので、導入時に一度全走査すれば
  **セッション消費の履歴は遡って復元できる**（補助呼び出しは記録が無いので遡れない＝導入日以降）。
- 二重計上の否定: チャットの claude は `~/.claude/projects` に転写を書くが、折り込み対象は
  **登録済みセッション（`session.Meta`）のみ** で会話は含まれない。重ならない。

### 3-c. 保存

```
~/.local/share/agent-fleet/usage/       # ~/.local は recreate を跨いで残る（Workspace Guide）
  raw/2026-07-25.jsonl                  # 追記のみ・日次ローテ・既定90日保持
  rollup/2026-07.json                   # 日×次元の集計（小さい・無期限）
  state.json                            # 折り込み watermark
```

サイズ試算: 1行 ≈ 200B。補助 100 呼び出し/日 + セッション 2,000 ターン/日 でも **~420KB/日**、
90日で ~38MB。rollup があるので通常のクエリは raw を読まない。

## 4. API

```
GET /usage/series?from=&to=&bucket=day|hour&by=feature|kind|model|trigger
                 &split=<第2軸>&filter=kind:claude,feature:title.*&include=session,aux
 → { buckets:[{t, series:{<key>:{spend,in,out,cread,ccreate,calls,cost_usd}}}],
     totals:{...}, matrix:{<by>:{<split>:{…}}},        // split 指定時のみ（機能×モデル等）
     coverage:{<kind>:{tokens:"exact|partial|none", model:"reported|requested|none"}},
     unmeasured_calls:N }
```

`by` と `split` の2軸で「機能 × モデル」「エージェント × モデル」を1リクエストで取る。
`calls` は **distinct `call`** で数える（claude のモデル別行を二重に数えない）。

サーバ側で集計して返す（Console に生ログを流さない）。`coverage` / `unmeasured_calls` は
**表示の正直さのための必須フィールド**。

⚠️ 新 REST は `workspace/agent/routes.go` と `control-plane/routes.go` の **両方** に登録する
（CP は明示許可リスト方式。片方漏れると backend 正常でも Console から 404）。

## 5. Console

- **置き場 = 設定モーダルの新タブ「使用量」**（`SettingsDialog.tsx` の `GROUPS` の
  `workspace` グループへ1行追加）。**専用ペインは v1 では採らない**。判断根拠:
  1. **前例と一貫性**: 使用量ダッシュボード（showback）は既に `AdminTab`＝設定モーダル内にある。
     使用量の画面が2箇所に割れると「どっちを見るんだっけ」が生まれる。
  2. **十分な広さ**: モーダルは 1000×760（settings-modal-refactor で拡大済み）。積み上げ棒＋
     内訳3枚＋表は収まる。
  3. **「見て直す」が同一面で閉じる**: 是正対象（補助呼び出しのモデル・ツール設定）は設定項目。
     測定と是正が同じタブにあるのが最短動線 — これが本機能の主目的（§1-a-2）。
  4. **配線コストが桁違い**: タブ追加＝`GROUPS` 1行 + コンポーネント + i18n キー。ペインは
     `PaneKind` の型・`Pane.tsx`・`paneTitle`・`LayoutMap`・`popout`・レイアウト永続化・
     コマンドパレットに波及する。v1 の価値に対して割高。
  5. **スマホ**: 設定モーダルはスマホ対応済み（現在タブ見出し `.settings-crumb`）。ペインは弱い。
- **ただしペイン昇格の余地は構造で担保する**: 描画は `features/usage/UsageView.tsx` に
  **モーダル非依存の純粋コンポーネント**として置き、設定タブは薄いラッパにする。「仕事の横に
  常時置きたい／別タブに切り離したい」が現実になったら `PaneKind` を足して同じ View を差すだけ。
  逆はできない（ペイン前提で書くと設定に入らない）ので、この順序で作る。
- 導線: 設定 > 使用量。加えて WsBar の使用量チップのメニューから「機能別の内訳」で直接そのタブへ
  ディープリンク（`settingsSection` は既に deep-link 対応）。
- **チャート構成**（dataviz スキルの規約に従って実装する）:
  1. **積み上げ棒 × 時系列**（日/時バケット・色 = feature）— 主役。
  2. **内訳**（期間合計）: feature 別 / kind 別 / **model 別** の横棒3枚。kind の色は
     `tokens.css --kind-*` を流用（agent-display-naming の1ソース規約）。feature と model 用の
     カテゴリ色は新規に定義する（model は「同系統モデルは同系色の濃淡」＝ haiku/sonnet/opus を
     1色相の階調にすると系列が増えても読める）。
  3. **表 = 機能 × モデル**（`by=feature&split=model`）: calls / spend / 1回あたり平均 / cost。
     **「この機能はどのモデルで走っているか」が本命のビュー**（§2-b の既定モデル問題がここに出る）。
     エージェント × モデルへの切替も同じ表で。
  4. 期間 24h / 7d / 30d、cache_read 表示トグル、feature・kind・model フィルタ。
  5. **未計測バナー**: 「cursor/agy はトークンを報告しないため回数のみ」「codex/cursor は
     モデルが要求値ベース」を `coverage` から自動生成して常時明示（手書きしない＝ドリフトしない）。
- i18n: 文字列は ja/en 両方（`i18n:lint` が裸和文を落とす）。

## 6. オペレーター連携（MCP）

`get_fleet_usage(from, to, by)` を追加。「今週いちばんトークンを食った機能は?」「自動ターンの
消費が増えてないか?」をオペレーターに聞けるようにする。既存 `get_agent_usage`（サブスク残枠）
/ `get_session_usage`（セッション単位）と役割が直交する第3のツール。

## 7. フェーズ

| P | 内容 | 完了条件 |
|---|---|---|
| P0 | 本 doc + ADR0029 確定（enum とワイヤ形の凍結） | レビュー済み |
| **P0.5** | **測る前に確定できる是正**（§1-a-2）: `oneShotHeadless` の claude 経路を `--tools ""` + `--system-prompt` 置換 + `MAX_THINKING_TOKENS=0` へ。codex/opencode/cursor に安価モデルの既定ピン（§2-b） | 3機能（title/branch/suggest）の出力品質を実 CLI で回帰確認・入力側トークンが実測で落ちること |
| P1 | 台帳 + 補助呼び出し計装（UI 無し）＋**モデル報告プローブ**（codex/opencode/cursor の実出力に model が乗るか実測して `model_src` を確定） | 13箇所タグ付け・go test 緑・実 CLI で1行記録を実測・claude はモデル別行が割れること |
| P2 | セッション折り込み（watermark + バックフィル + 削除時確定） | 冪等性テスト・既存 `get_session_usage` と突合一致 |
| P3 | `/usage/series` + CP 両側登録 | curl で系列取得 |
| P4 | Console ペイン + グラフ + i18n | typecheck/vitest/i18n:lint 緑・headless 実描画検証 |
| P5 | MCP ツール + 設定（保持日数 / 記録 OFF） | ライブでオペレーターに聞けること |
| P6（任意） | CP 横断集計（tenant/member 別・showback と同居） | admin から見えること |

## 8. 限界とリスク（先に言っておく）

- **サブスク枠 ≠ トークン**: claude Max の 5h/週枠は非線形で、トークン量から残枠は逆算できない。
  枠の正は既存の使用量チップ（statusline `rate_limits`）で、本機能は **配分の可視化** が役割。
- **rtk の効果は別軸**: 台帳が測るのは rtk 適用「後」の実消費。節約量は `rtk gain`（既存チップ）。
- **欠測 kind がある**（1-c）。回数だけは数える。
- **遡れるのはセッションだけ**: 補助呼び出しの履歴は導入日以降。
- **計測自体のコストは0**: 既存の CLI 出力を解析するだけで、追加の LLM 呼び出しはしない。
- **プライバシー**: 本文非記録。台帳はワークスペース内（`~/.local/share`）に閉じ、P6 で CP へ
  出す場合も集計値のみ（feature/kind/model/日次合計）。

## 9. 未決（要判断）

1. ~~UI の置き場~~ → **決定: 設定モーダルの新タブ**（§5 の5点）。View をモーダル非依存に切って
   ペイン昇格の余地だけ残す。
2. **コスト表示**: claude だけ実測 $ が出せる。「API 換算相当額（claude のみ）」として出すか、
   v1 はトークンのみに徹するか。
3. **セッション本体を含めるか**: 含めて feature フィルタで補助だけに絞れる形（推奨） /
   補助専用のダッシュボードにする。
4. **保持期間**: raw 90日・rollup 無期限（推奨）。
5. **フリート横断（P6）**: 自ワークスペース内で十分か、CP 集約まで要るか。
6. **是正の順序** → 推奨は **P0.5 を台帳より先に出す**。§1-a-2 は直接実測で効果が確定して
   おり、台帳の完成を待つ理由がない。「before」は本 doc の実測表が担うので、台帳は
   **after の定常状態**を見せる役になる。ただし codex/opencode/cursor の既定モデルピン
   （§2-b）は「どのモデルが実際に選ばれているか」が未確認なので、**P1 のプローブ後**でよい。
7. **`compact`（要約）に思考トークン抑制を効かせるか**: 要約は品質が引き継ぎに直結するので、
   一律 0 にはしない。実測してから判断。
