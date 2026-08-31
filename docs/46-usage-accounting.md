# docs/46 — 使用量アカウンティング（機能別トークン計測とグラフ化）

フリート内で **何にトークンが使われているか** を機能ごとに計測し、Console でグラフ化する。
対話セッションだけでなく、フリート自身が裏で撃っている補助 LLM 呼び出し（アシスタントチャット、
要約引き継ぎ、タイトル提案、返信サジェスト、報告への自動ターン…）を **同じ物差しで並べる** のが主眼。

決定は [ADR 0029](decisions/0029-usage-accounting.md)。ステータス: **P0.5（是正）＋ P1（台帳＋計装）
＋ P2（出自＋折り込み）＋ P3（`/usage/series` + rollup + CP 両側登録）＋ P4（Console UI）実装済み。
P5（MCP ツール + 設定）以降は未着手**。

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
| `suggest.session` | manual | `session_suggest_reply.go:133` | ミラーの ✨ 返信候補 |
| `suggest.chat` | manual | `chat_suggest_reply.go:43` | チャットの ✨ 返信候補 |
| `suggest.edit` | manual | `fs_suggest_edit.go` | エディタの ✨ AI変更提案（docs/44 = [44-markdown-code-editor.md](44-markdown-code-editor.md) Phase 4） |

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

#### 実装済み（P0.5・2026-07-25）

`oneShotHeadless` を上記 E 相当へ（`claudeOneShotArgs` / `claudeOneShotEnv`）。codex 側も
同じ発想で2点（`codexOneShotArgs`）:

- **`-m <小型モデル>`** — カタログ（`codex debug models`）から `mini`/`flash`/`lite`/`small`/
  `nano`/`haiku` を含む id を拾う（`cheapOneShotModel`）。**モデル名を直書きしない**のは、
  名前は数週間で変わるがこの語彙は変わらないから。実カタログでは `gpt-5.4-mini`。
- **`-c model_reasoning_effort="low"`** — `MAX_THINKING_TOKENS=0` の codex 版。利用者の
  `config.toml` は `high` のことが多く（実機がまさにそう）、一発呼び出しにまでそれが効いていた。

実 CLI 契約テスト（`chat_oneshot_test.go`・opt-in）で3機能とも回帰確認済み:
title=「使用量グラフの台帳設計」/ branch=`usage-graph` / 返信候補3件、いずれも 1.7〜2.3s
（従来 6.2s）。codex 経路も `gpt-5.4-mini` + `low` で件名が返ることを実機確認。

> **★ 実測で判明した罠 — カタログに載っている ≠ そのアカウントで使える。**
> 同じ手を opencode にも当てて `opencode/claude-haiku-4-5`（`opencode models` に載っている）を
> 選ばせたら、実行が `Unexpected server error` で落ちた。`--model` 無しの既定は正常に応答する。
> **opencode では安価モデルの自動ピンをしない**（`AF_TITLE_MODEL_OPENCODE` の明示のみ）。
> codex 側は実機で動くことを確認済みだが、他アカウントで同じ罠を踏みうるので、**自前で選んだ
> 時に限りモデル指定なしで1回だけリトライ**する（`codexOneShotArgsNoModel`）。利用者が
> 環境変数で明示したモデルは決して外さない。タイトル提案が壊れることの方が、トークン代より高い。

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
  "trigger": "auto",              // ターン注入元: user | auto | manual | schedule | operator | bridge | recovery
  "origin": "operator",           // セッションの出自（§2-c）: user | operator | schedule | handoff | unknown
  "origin_conv": "a3f9k2p",       // 出自がオペレーターの時、作成元のアシスタント会話 slug
  "kind": "claude",               // 実際に実行したエージェント種別（要求ではなく実行結果）
  "model": "claude-haiku-4-5",    // 実行側が報告した正規モデル（§2-b）
  "model_raw": "claude-haiku-4-5-20251001", // 報告された生の id（版込み）
  "model_req": "haiku",           // 要求した値（model と食い違う時だけ＝フォールバック検知）
  "model_src": "reported",        // reported | requested | default_unknown
  "ref": "s7in3bh",               // セッション名 or 会話 id（無ければ空）
  "verb": "",                     // assistant.chat のサブ次元（translate|summarize）
  "sidechain": false,             // feature=session のサブ次元
  "idx": 12,                      // feature=session の論理ターン通し番号（1始まり）。(ref, idx) が冪等キー（§7-4）
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

### 2-c. セッションの出自（`origin`）— 誰が始めたセッションの消費か

**`trigger`（ターン注入元）とは別の軸**として、セッション自体の出自を記録する。
「自分で開いたセッション」と「オペレーター（アシスタント）が勝手に立てたセッション」では
消費の意味がまるで違う — 後者は自動走行・定時実行と組み合わさると**無人で増える**。

| 値 | 意味 | 記録点 |
|---|---|---|
| `user` | Console の起動モーダルから人が開始（既定） | `session_handlers.go` の create |
| `operator` | af_write アシスタントの `create_session`（＋作成元の会話 slug） | `mcp_stdio.go` の create_session が body に載せる |
| `schedule` | 定時実行が到来時に起こした新規セッション（docs/38） | スケジュール発火の create |
| `handoff` | 引き継ぎ（旧 fork）で生えたセッション | `session_handlers.go` の fork 経路 |
| `unknown` | この機能より前に作られた既存セッション | — |

**現状 `session.Meta` に作成者フィールドは無い**（`CreatedAt` / `ForkFrom` はあるが出自は不明）。
→ `Meta.Origin` / `Meta.OriginConv` を追加する。実装上は create リクエストの1フィールドで、
**MCP 経路が `"operator"` を明示、スケジュールが `"schedule"`、Console は `"user"`**。
未指定は `user`（人の操作だけがラベル無しで通る経路）、**既存セッションは `unknown`**（0 でも
user でもない、を守る）。recreate は元の出自を引き継ぎ、handoff は `handoff` + 親を持つ。

**遡及（best-effort）**: 既存セッションでも、オペレーター注入台帳（`session_injections.go` の
`operatorInjections`）に最初のユーザーターンと一致する記録があれば `operator` と推定できる。
推定値は `origin_src:"inferred"` を立てて実測と区別する。docs/44
（[44-operator-interaction-graph.md](44-operator-interaction-graph.md) — オペレーター↔セッションの
ディスパッチ台帳。番号 44 は [44-markdown-code-editor.md](44-markdown-code-editor.md) と重複）が入れば、
そちらが一次ソースになる。

**補助呼び出しにも伝播させる**: あるセッションのタイトル提案・返信サジェストは、そのセッションの
`origin` を引き継いで記録する（`ref` から解決して**行に焼き込む**。セッション削除後も集計が壊れない
ため、他の次元と同じ思想）。これで「オペレーターが立てたセッション**群**の総消費（本体＋付随の
補助呼び出し）」が1本の系列として出る。

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
- **fold-on-read は非同期なので、応答は「折り込み中か」を必ず申告する**（`folding`）。
  非同期にした以上、要求した読み出し自体は折り込み前の値を返す＝**画面は常に1回ぶん古い**。
  黙って返すと利用者は最新になるまで再取得を押し続けることになる（実際にそう報告された）。
  Console は `folding` が落ちるまで自動で取り直し、明示的な再取得だけ `fold=force` で
  60 秒スロットルを飛ばす（自動の取り直しに付けると、終わるたび次を起動して走り続ける）。
- **初回バックフィル**: 転写は過去分がそのまま残っているので、導入時に一度全走査すれば
  **セッション消費の履歴は遡って復元できる**（補助呼び出しは記録が無いので遡れない＝導入日以降）。
- 二重計上の否定: チャットの claude は `~/.claude/projects` に転写を書くが、折り込み対象は
  **登録済みセッション（`session.Meta`）のみ** で会話は含まれない。重ならない。
- 追記と watermark は**別ファイルで原子的に書けない**（間で落ちれば再追記される）ので、
  **集計側が `(ref, idx)` で重複を落とす**。書き手の watermark と読み手の重複排除の二段構え
  （§7-4）。

### 3-c. 保存

```
~/.local/share/agent-fleet/usage/       # ~/.local は recreate を跨いで残る（Workspace Guide）
  raw/2026-07-25.jsonl                  # 追記のみ・日次ローテ・既定90日保持
  rollup/2026-07.json                   # 日×次元の集計（小さい・無期限）
  rollup/state.json                     # 版 + 畳み済みファイル日 + (ref,idx) 水位（§7-4）
  state.json                            # 折り込み watermark
```

サイズ試算: 1行 ≈ 200B。補助 100 呼び出し/日 + セッション 2,000 ターン/日 でも **~420KB/日**、
90日で ~38MB。rollup があるので通常のクエリは raw を読まない。

## 4. API

```
GET /usage/series?from=&to=&bucket=day|hour&by=feature|kind|model|trigger|origin
                 &split=<第2軸>&filter=kind:claude,feature:title.*&include=session,aux
                 &fold=force                          // 明示的な再取得だけ（§3-b）
 → { buckets:[{t, series:{<key>:{spend,in,out,cread,ccreate,calls,cost_usd}}}],
     totals:{...}, matrix:{<by>:{<split>:{…}}},        // split 指定時のみ（機能×モデル等）
     coverage:{<kind>:{tokens:"exact|partial|none", model:"reported|requested|none"}},
     unmeasured_calls:N, folding:true }                // 折り込み走行中＝直近ターン未反映
```

`by` と `split` の2軸で「機能 × モデル」「エージェント × モデル」を1リクエストで取る。
`calls` は **distinct `call`** で数える（claude のモデル別行を二重に数えない）。

サーバ側で集計して返す（Console に生ログを流さない）。`coverage` / `unmeasured_calls` は
**表示の正直さのための必須フィールド**。

⚠️ 新 REST は `workspace/agent/routes.go` と `control-plane/routes.go` の **両方** に登録する
（CP は明示許可リスト方式。片方漏れると backend 正常でも Console から 404）。

### 4-a. 実装済み（P3・2026-07-26）

`usage_series.go`（ハンドラ＋集計）/ `usage_rollup.go`（rollup）。登録は
`workspace/agent/routes.go` の `GET /usage/series` と `control-plane/routes.go` の
`GET /api/usage/series`（CP は `rest` プロキシで `/api` を落として素通し）。

確定した契約（上のワイヤ形に加えて）:

- **軸の語彙**は `feature` / `kind` / `model` / `trigger` / `origin` / `origin_conv` /
  `verb` / `model_src` / `measured`。`by` と `split` と `filter` のキーは全部これ。
  **未知の軸は 400 で弾く**（黙って無視すると「指定したのに効かない」が静かに起きる）。
- **`filter` は同じ軸が OR・違う軸が AND**。`filter=kind:claude,kind:codex` は「claude か
  codex」、`filter=kind:claude,feature:title.*` は「claude かつ title 系」。末尾 `*` だけを
  前方一致として扱う。
- **`from`/`to` は RFC3339 と `YYYY-MM-DD` の両方**。日付だけの `to` は**その日いっぱい**
  （0 時と解釈すると `from=to=今日` の hour クエリが空になる）。既定は直近7日。
- **`bucket=hour` は raw の保持期間内だけ**。rollup は日粒度なので、畳んだ後に prune された
  期間は時間粒度で復元できない。その場合は **`truncated: true`** を返す — 黙って短い系列を
  返すと「その期間は消費が無かった」に見えるため。
- **`ref`（セッション名 / 会話 id）はクエリ軸に無く、応答にも出さない**。rollup のキーに
  入れると際限なく増えて「小さい」前提が壊れるのと、集計 API から個別の名前を出さない
  （プライバシー）を兼ねる。ref 単位の追跡は raw の保持期間内に台帳を直接読む。
- `ms`（レイテンシ）は raw には入っているが v1 の集計には出さない。読み手が決まってから。

**rollup の設計**（`usage/rollup/YYYY-MM.json` ＋ `usage/rollup/state.json`）:

- **バケットは行の `ts`（消費が起きた時刻）で刻む。追記先のファイル日ではない。**
  → 下の §7-3 の実機で踏んだ穴。
- 二重計上しない不変条件は1つ: **raw の各ファイル日は「畳み済み（行は rollup にある）」か
  「未畳み（raw を読む）」のどちらか一方**。当日は行がまだ増えるので必ず未畳み側。
- 畳んだ消費日ごとに**寄与元のファイル日（`src`）を残す**ので、途中で落ちて state を
  書けなくても、やり直しは skip されるだけで二重に足さない。
- 契機は fold-on-read と同じ（`/usage/series` と `/sessions/usage`）。常駐タイマーは無い。

## 5. Console

- **置き場 = 設定モーダルの新タブ「エージェント使用量」**（当初のラベルは「使用量」。
  同名が 3 か所ある問題（[67](67-member-cloud-cost.md) §67.2）を受けて 2026-08-31 に改称。
  キー `set.tab_usage` と API パスは触っていない＝ deep-link は生きる）（`SettingsDialog.tsx` の `GROUPS` の
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
  4. **出自の対比**（§2-c）: 「人が始めたセッション（`user`）」vs「オペレーター/定時が立てた
     セッション（`operator`/`schedule`）」の2系列を積み上げ棒に重ねる。無人で増える消費が
     いちばん見たい絵なので、時系列の**既定の割り方の切替候補**として feature と並べる。
     `origin=operator` を選ぶと `origin_conv` で会話別にも割れる（どのオペレーター会話が高い
     買い物をしたか）。
  5. 期間 24h / 7d / 30d、cache_read 表示トグル、feature・kind・model・origin フィルタ。
  6. **未計測バナー**: 「cursor/agy はトークンを報告しないため回数のみ」「codex/cursor は
     モデルが要求値ベース」を `coverage` から自動生成して常時明示（手書きしない＝ドリフトしない）。
- i18n: 文字列は ja/en 両方（`i18n:lint` が裸和文を落とす）。

### 5-a. 実装済み（P4・2026-07-26）

`console/src/features/usage/` に4本 — `UsageView.tsx`（描画・モーダル非依存）/ `series.ts`
（応答→描画形の純関数）/ `colors.ts`（系列→色）/ `api.ts`（型と取得）＋ `usage.css`。
設定モーダルは `GROUPS` の `workspace` に `["usage","set.tab_usage"]` を1行足して
`<UsageView />` を差すだけ。導線は WsBar の使用量チップ（claude / codex / agy / copilot）の
ポップオーバーに「機能別の内訳」→ `openSettings("usage")`。

**画面構成**: フィルタ1行（期間 24h/7d/30d・割り方・指標・再取得）→ KPI 5枚（消費／呼び出し／
キャッシュ読取／API 換算相当額／**未計測**）→ 積み上げ棒＋凡例＋「表」トグル → 内訳3枚
（機能・エージェント・モデル）→ 機能×モデル表（エージェント×モデルに切替可）→ 未計測バナー。
リクエストは3本（選択軸の時系列 / `by=feature&split=model` / `by=kind&split=model`）で、
**内訳3枚は matrix の行合計・列合計から起こす**（追加リクエストを撃たない）。
凡例・内訳の行・チップはクリックで絞り込みになり、フィルタ1行が下の全チャートと表を
同じスライスに揃える（チャートごとのフィルタは作らない）。

**色の規約**（dataviz スキルの検証手順で決めた。破ると読めなくなる順に3つ）:

1. **色は順位ではなく実体に付く。** feature / trigger / origin などの列挙軸は固定表で、
   データに一切依存しない。フィルタで系列が減っても生き残った色は動かない。
2. **スロット順に積む。** 積み上げで触れ合うのは隣接スロット同士だけになるので、パレットの
   隣接ペアを検証しておけば実際の隣接も安全。`tokens.css` の `--viz-1..8` は検証済みの8色
   （light / dark 別ステップ・隣接 CVD ΔE 9.1 / 8.4・通常視 19.6 / 19.3。light は4色が
   コントラスト 3:1 未満＝**凡例のラベルと表ビューが必須の relief** として同梱）。
3. **9色目を作らない。** 8を超えた系列はグレーの「その他」へ畳む（内訳と表には個別に出る）。

**kind の色は `--kind-*` のまま（1ソース規約）**。ただし7色をそのまま積むと隣接ペアが CVD
検証に落ちる（agy 青 ↔ kiro 紫 が protan ΔE 2.0、copilot / opencode は無彩色に近い）。
**色は変えず並びだけ**を全順列から選び、`cursor → agy → claude → copilot → codex → kiro →
opencode` の固定順で積む＝隣接は dark ΔE 13.0 / light 17.3・通常視 19.8 / 19.4 で両テーマとも
隣接ゲートを通る。彩度・明度の帯は色そのものの性質なので通らないままで、その分は
凡例・ツールチップ・表（＝色だけに頼らせない）で担保する。

> 上の §5-2 からの**意図的な差分**: モデル別を「同系統は同色の濃淡」にしなかった。濃淡を
> 機械生成するとコントラストと明度帯の検証が効かなくなる。**モデルには検証済みの別色**を
> 割り当て（モデル名のハッシュ→スロットなので同じモデルは常に同じ色）、8色を超えた分は
> 「その他」へ畳む。実データが既に10モデル以上あり、この形の方が読める。

**実データでの描画検証**（headless Chromium ＋ 実 `/usage/series` 応答をフィクスチャ化。
memory: headless-ui-verification-harness の型2）: 11 バケット / 856 呼び出し / spend 19.7M /
10モデル。dark・light、ja・en、機能別／モデル別（畳み込みと「その他」凡例）、ホバーの
ツールチップ（全系列＋合計）、表ビュー、幅 380px のスマホ幅まで実描画で確認した。実データに
まだ無い経路（未計測 37件・`truncated`・coverage none・補助 feature 11種）は合成フィクスチャで
描かせて確認。**実バックエンドを繋いだ目視は未実施**（本ワークスペースの常駐 agent は P1 前の
ビルドなので `/usage/series` を持たない。要エージェント再ビルド）。

**この検証で直した実際の穴**: ① 期間内にコストが無いと `$0.0000` と出て「タダで動いた」に
読めた → 実測が無ければ「—」。② 目盛りの丸めが粗く最大 3.3M が 5M に飛んで棒が縦半分しか
使えていなかった → 刻みを 1/2/2.5/3/4/5/8/10 に。③ 幅 380px で日付ラベルが重なった →
プロット幅を実測して n 本おきに間引く（棒は全部描く＝値はツールチップと表で読める）。

### 5-b. API 換算相当額の推定（2026-08-31）

**苦情**: 「機能 × モデル」表の *換算額* が、セッション本体の行だけずっと「—」。実測コストを
返すのは claude の**補助呼び出し**だけ（`total_cost_usd`）で、セッション本体は転写から
折り込むためトークンしか無い＝§9-2 の決定どおりの挙動だが、画面としては「一番大きい消費に
金額が付かない」。**台帳は in / out / cread / ccreate をモデル別に持っている**ので、公表単価を
掛ければ推定は出せる。

実装は `workspace/agent/usage_price.go`（単価表と掛け算）＋ `usage_series.go`（応答に載せる）。

- **推定と実測は別の値。** `usageAgg.CostEstUSD`（`cost_est_usd`）と `CostUSD`（`cost_usd`）は
  足さない・埋め合わせない。1つの数字に2つの計測法が混ざると、どちらとしても読めなくなる。
  UI は推定を `≈` 付きで主表示し、実測はツールチップに併記（推定の答え合わせになる唯一の値）。
- **式**: `(in + ccreate×1.25 + cread×0.1) × 入力単価 + out × 出力単価`。**cache_read は
  `spend` の定義に入っていないが課金される**（0.1 倍）ので、`spend` から金額を起こすと長い
  会話ほど安く出る。転写は 5m / 1h TTL の内訳を残さないため書込は 1.25 倍固定＝1h で書かれた
  分は下振れする。
- **単価表に無いモデルは推定しない**（0 を出さない）。載せているのは Anthropic の公表単価だけで、
  他プロバイダ（`gpt-*` / `qwen*` / `glm-*` …）は確かな単価を持っていないので**意図的に空**。
  値付けできなかった消費は `priced_spend` / `unpriced_spend` で申告し、画面は「計測できている
  範囲」に「消費の N% は単価を持っていないモデル」と出す（§1-c の「0 と未計測を混同しない」の延長）。
- **保存しない。** rollup にも書かず、読み出しのたびに今の表で掛け直す（単価は改定される＝
  古い単価で焼いた金額がファイルに残る方が悪い）。rollup のキーはモデル次元を含むので、
  畳み済みの過去分も同じ精度で起こせる。モデル名の揺れ（版込み生 id・`provider/` 接頭辞・
  Vertex の `@date`）は**引く側**の `usageNormalizeModel` が吸収する。

## 6. オペレーター連携（MCP）

`get_fleet_usage(from, to, by)` を追加。「今週いちばんトークンを食った機能は?」「自動ターンの
消費が増えてないか?」をオペレーターに聞けるようにする。既存 `get_agent_usage`（サブスク残枠）
/ `get_session_usage`（セッション単位）と役割が直交する第3のツール。

## 7. フェーズ

| P | 内容 | 完了条件 |
|---|---|---|
| P0 | 本 doc + ADR0029 確定（enum とワイヤ形の凍結） | レビュー済み |
| ~~P0.5~~ **完了** | **測る前に確定できる是正**（§1-a-2）: claude 経路を `--tools ""` + `--system-prompt` 置換 + `MAX_THINKING_TOKENS=0`、codex 経路を小型モデル自動ピン + `model_reasoning_effort="low"`（opencode は罠のため見送り、cursor は `auto` のまま） | ✅ 3機能を実 CLI で回帰確認（1.7〜2.3s / 従来 6.2s）・go test 674 緑・契約テスト `chat_oneshot_test.go` 追加 |
| ~~P1~~ **完了** | 台帳 + 補助呼び出し計装（UI 無し）＋**モデル報告プローブ** | ✅ 下記 §7-1 |
| ~~P2~~ **完了** | セッション折り込み（watermark + バックフィル + 削除時確定）＋ **`Meta.Origin`/`OriginConv`** | ✅ 下記 §7-2 |
| ~~P3~~ **完了** | `/usage/series` + rollup + CP 両側登録 | ✅ 下記 §7-3 |
| ~~P4~~ **完了** | Console 設定タブ + グラフ + i18n（§5-a） | ✅ typecheck / vitest 464 / `i18n:lint` 緑・実データを headless で実描画検証 |
| P5 | MCP ツール + 設定（保持日数 / 記録 OFF） | ライブでオペレーターに聞けること |
| P6（任意） | CP 横断集計（tenant/member 別・showback と同居） | admin から見えること |

#### 7-1. P1 実装済み（2026-07-26）

`usage_ledger.go`（行の形＋raw jsonl 追記・日次ローテ・90日 prune）/ `usage_tag.go`（ctx タグと
プロバイダ層1点記録）。消費源はタグ1行ずつ、記録は provider の usage 解析地点に集約。
記録は各 provider 関数の先頭で `defer` に積むので、**成功・エラー result・exec 失敗・早期
return の全経路で必ず1回**残る（失敗行は `ok:false`）。

**モデル報告プローブの結果**（実 CLI・本ワークスペース）: 表は
[ADR 0029 §4](decisions/0029-usage-accounting.md)。要点は3つ —

- claude は `modelUsage` の**キーが版込みの生 id**、値に `canonicalModel` / `costUSD` /
  トークン4種。top-level に `usage.output_tokens` / `total_cost_usd` / `duration_ms`。
  §2-b の想定どおり **claude だけがモデル・トークン・コストを全部実測で返す**。
- **codex はどのイベントにもモデルを載せない**（`thread.started` にも無い＝要プローブだった
  点の決着）。`turn.completed.usage` には `cache_write_input_tokens` / `output_tokens` /
  `reasoning_output_tokens` も乗る。→ `requested` / `default_unknown`。
- **cursor も `result` にモデル無し**（docs/40 probe 9 の再確認）。→ 同上。
- **opencode は未検証**: このワークスペースが未ログイン（`opencode auth list` = 0 credentials）。
  実装は `modelID` を拾って `reported`、取れなければ縮退する形にしてある（推測でスキーマを
  固めない）。ログインのある環境で再プローブが要る。

実 CLI 検証（`TestUsageLedgerLive`・opt-in `AF_TITLE_LIVE=1`）で落ちた実際の1行:

```
feature=title.session trigger=manual ref=slot99 kind=claude
model=claude-haiku-4-5 model_raw=claude-haiku-4-5-20251001 model_req=haiku model_src=reported
in=3 out=13 cread=4412 ccreate=0 spend=16 cost_usd=0.0005092 ms=2964 ok=true measured=exact
```

#### 7-2. P2 実装済み（2026-07-26）

`usage_fold.go`（`foldTurnRows` は純関数）＋ `session.Meta.Origin/OriginConv`。

- **idx は論理ターンの通し番号**にした。転写は追記のみなので kind に依らず安定で、
  `(session, idx)` が冪等キーになる。各 kind の `Turn.Idx`（行番号）は番号体系が違い、
  watermark に混ぜると壊れる。
- **開いている末尾ターンは折り込まない**。折り込み後に同じ論理ターンへイベントが足されると
  入力スナップショット（置換セマンティクス）を二重に数えるため。次のユーザーターンで閉じた時か、
  **削除時（`finalizeSessionUsage`）** に確定する。
- **契機**: `GET /sessions/usage` を間借りした fold-on-read（60 秒スロットル）と削除時確定。
  全転写の読み直しは実測 ~20s（158 セッション）なので**非同期に回して応答を待たせない**。
  常駐タイマーは増やしていない。
  - **その代償を利用者に押し付けない。** 非同期＝要求した読み出しは折り込み前の値を返す
    ので、応答に `folding` を載せて Console に取り直させる（§3-b）。載せていなかった間、
    使用量タブは「再取得を何度か押すまで最新にならない」画面だった。**非同期にした関数の
    戻り値は「起動した」ではなく「まだ追いついていない」を返すべき**、が教訓。
- **バックフィルは自動**（watermark 0 から走る）。実データで **848 行**が一度に入り、
  2回目は 0 行（冪等）。
- **出自**: Console=`user` / MCP `create_session`=`operator`＋会話 slug / handoff=`handoff` /
  recreate=継承 / 既存メタ=`unknown`。**スケジュールは CP を触らず、既存の `source=schedule`
  から導出**した（docs/38 が既に送っている＝新しいワイヤ項目を増やさない）。

**突合検証**: 実ワークスペースの **158 セッション（claude / codex / copilot）全件**で、
折り込みの spend 合計・論理ターン数が `get_session_usage` の cumulative と**完全一致**
（`TestFoldMatchesSessionUsageLive`・opt-in `AF_USAGE_FOLD_LIVE=1`）。

#### 7-3. P3 実装済み（2026-07-26）

契約は §4-a。両側登録済み（agent `GET /usage/series` / CP `GET /api/usage/series`）。

> **★ 実機で最初に踏んだ穴 — バケットを raw ファイルの日で刻んでいた。**
> 実バイナリを起動して `/usage/series` を叩いたら、**11日分あるはずの系列が「今日」1本**に
> なった。原因は集計が追記先のファイル日で刻んでいたこと。セッション折り込みは過去の転写を
> 後から取り込む（バックフィルは導入日に数か月分が一度に入る）ので、ファイル日で刻むと
> **過去の消費が全部「導入日」に積み上がり、時系列として無意味になる**。行の `ts` で刻むよう
> rollup とクエリを作り直した（`usageRowTime`）。単体テストは全部緑のまま通っていた —
> 合成データはファイル日＝消費日で書いていたので、この差が出なかった。回帰は
> `TestUsageSeriesBucketsByConsumptionTimeNotFileDay` / `TestRollupKeysByConsumptionDay`。

もう1つ、テスト側で踏んだ穴: 集計ハンドラのテストが fold-on-read 経由で**実ワークスペースの
セッションを畳んで**しまい、期待値が実データで壊れた（合計が数百万トークンになった）。
`useIsolatedUsageDir` で `HOME` ごと隔離して解決（実 CLI を撃つライブテストは認証が HOME
配下なので `useTempUsageDir` のまま）。

**実機確認（実バイナリ + curl・実データ）**:

```
GET /usage/series?from=2026-01-01&by=kind
→ 11 バケット（2026-07-16〜07-26）/ calls=853 / spend=19,624,212
  coverage: claude{exact,reported} codex{exact,reported} copilot{partial,reported}
GET /usage/series?from=2026-01-01&by=feature&split=model   ← 本命ビュー
→ session×claude-opus-4-8 calls=461 spend=11,464,050 / ×claude-fable-5 201/5,056,597
  ×gpt-5.6-terra 71/939,073 / ×claude-opus-5 31/1,146,638 …
GET /usage/series?from=2026-01-01&by=origin        → {unknown: 全部}（既存セッション＝設計通り）
GET /usage/series?from=2026-01-01&filter=feature:title.*  → 0（補助呼び出しは導入日以降）
```

`copilot` が `partial` として自動で出ている（転写に `outTok` しかない）のが、`coverage` を
手書きせずデータから起こしている効果。`origin` が全部 `unknown` なのも正しい — 既存セッション
は出自を持たない（`0` でも `user` でもない、を守っている）。

#### 7-4. `(ref, idx)` 重複排除（2026-07-26・レビュー P1 の続き）

`usage_dedup.go`。折り込みの冪等性は watermark が担保しているが、**行の追記（`raw/*.jsonl`）と
watermark（`state.json`）は別ファイルで、原子的には書けない**。`commitSessionUsageFold` で窓は
1セッション分まで縮めた（`29bd9f2d`）が、窓そのものは消せない — 追記した直後に落ちれば、その
セッションの数ターンは次のパスで再追記される。**書き手側で閉じられない以上、読み手側で落とす。**

- **キーは `(ref, idx)`**。持つのは **ref ごとに1エントリ**（計上済みの最大 `idx` ＋ 観測した
  最大 `ts`）で、`(ref, idx)` の集合は持たない — 158 セッション × 数千ターンの集合を無期限に
  抱えると rollup の「小さい」前提が壊れる。集合が要らないのは、`idx` がセッションごとに 1 から
  単調増加で追記され、重複が必ず「追記済みの末尾を、後からもう一度」の形で現れるから。
  watermark を見失った（`state.json` の消失・破損）時の「idx 1 から全部やり直し」も同じ形なので
  丸ごと吸収される。
- **`ts` も見るのは slug 再利用への保険**。セッション名は 30bit の乱数 slug で、生存中のメタと
  しか衝突検査をしない（`session_name.go`）ため、削除済みの名前がいつか再び払い出されうる。
  その時 `idx` は 1 に戻るので、`idx` だけで判定すると**新しいセッションの消費を静かに落とす**
  （重複より悪い）。新しい incarnation の消費時刻は必ず後になるので、**`idx` が既計上以下
  かつ `ts` が既観測以下**の時だけ落とす。迷う側は「重複を残す」に倒す。
- **落とす場所は rollup の畳み込みと `/usage/series` の raw 走査の両方**。`bucket=day` は
  畳み済みファイルを読まないので、水位を rollup state から引き継ぐ（原本が rollup にあり重複
  だけが未畳みの raw に残る形を落とすため）。`bucket=hour` は畳み済みも含めて raw を全部読み
  直すので、水位は空から積む（引き継ぐと原本の側まで落ちる）。**期間で絞る前に**通すこと —
  どの行を「最初の1件」とみなすかがクエリ期間で変わると、期間を変えただけで合計が動く。
- **既存 rollup に紛れ込んでいる重複**: 集計は加算済みで特定の行だけ引き算できないので、
  `rollup/state.json` に版を持たせ（`v:2`）、**raw から作り直す**。寄与元の raw が1日でも
  prune 済みなら作り直さない — 消えた raw の分の集計を失う方が、残っているかもしれない重複より
  重い（その場合は見えている畳み済みファイルから水位だけ復元し、以後の重複は落とす）。本機能は
  導入直後で raw が全部残っているので、実際には作り直し側を通る。
- **distinct call との整合**: 重複行は**行ごとに別の `call` ID を持つ**（折り込みが行ごとに UUID
  を振るため）ので、`call` の重複排除では捕まらない。`(ref, idx)` は**行ごと落とす**ので
  `spend` と `calls` が同時に直る。既知の「`model` 軸で割ると `calls` の帰属が壊れる」（P2 指摘）
  は集計キー側の別問題で、この層とは干渉しない。
- **性能**: 158 セッション × 60 ターン（9,480 行）を1ファイルに置いて `collectUsageSamples` を
  計測。重複排除の有無で **119ms / 125ms（測定誤差以下）** — `readUsageDay` の JSON パースが
  支配的で、追加分は行あたり SHA-256 1回とマップ操作1回。索引は ref あたり ~40B（158 セッション
  で ~10KB）。
- 回帰は `usage_dedup_test.go` の8本（同一ファイル内の再追記 / ファイル日跨ぎ / rollup と raw の
  境界跨ぎ / 誤爆しないこと / 版上げの作り直し / prune 済みでの作り直し拒否 / クラッシュ窓の
  通し再現 / 索引に ref が平文で載らないこと）。**重複排除を戻すと落ちることを確認済み**。

#### 7-5. レビュー P2 / P3 の修正（2026-07-26）

10件。**「消えた消費」と「誤読させる表示」**の2種類に分かれる。

**集計が壊れる/消える（P2）**

- **月ファイルが書けなかったら state を進めない**（`usage_rollup.go`）。進めるとその月へ
  寄与するはずだった消費は「畳み済み」扱いのまま集計から消え、raw が prune された時点で
  戻らない。書けた月の分は `Src` が覚えているので、やり直しても二重に足さない。
- **呼び出し回数の帰属を実態に合わせた**。claude は1呼び出しがモデル別行に割れ、行の並びは
  生 id の綴り順でしかない。「最初の行」で数えていたので、`by=model` / `split=model` で
  **主力モデルが 0 回・脇役が 1 回**と出ていた。代表行＝**その呼び出しで最も spend の
  大きいモデル**（同点は生 id 昇順）に変更。按分にしないのは `calls` を整数の回数として
  凍結しているから（ADR0029 §1）。どの軸で足しても distinct call 数に一致する性質は保つ。
  代表以外の行は spend>0 / calls=0 になるので、表の「1回あたり」は `0` ではなく `—` を出し、
  理由をツールチップに置く。
- **止められた/落ちたターンのトークンを残す**（`chat_providers.go`）。`modelUsage` からしか
  採っていなかったので、**result イベント前の停止・異常終了が丸ごと「0 トークン・
  measured=none」**になっていた（止められるのは重いターンほど多い＝配分の絵が狂う）。
  result の `usage` と、stream では assistant イベントで見た最後のスナップショットへ縮退する
  （後者は `measured=partial`）。一発呼び出しも exec エラーより先に result を解析する。
- **転写ファイルの入れ替わりに耐える**（`usage_fold.go`）。claude は1つの sid に兄弟 jsonl を
  持ちうり、読む1本は **mtime で選ばれる**。件数だけの watermark だと、短い方へ振れると
  **折り込みが永久に止まり**、長い方へ振れると**別の会話の先頭ターンを既折り込み扱いで
  落とす**。件数と時刻の両方で「まだ折り込んでいないターン」を判定するようにした
  （watermark は下げない・古い転写からは拾わない）。

**表示の誤読（P2/P3）**

- **色スロットの無い feature を見えなくしない**（`colors.ts` / `UsageView.tsx`）。enum 13 個
  （凍結12＋docs/44 = [44-markdown-code-editor.md](44-markdown-code-editor.md) Phase 4 の `suggest.edit`）に対しスロットは8つで、`assistant.ask` /
  `title.chat` / `branch.suggest` / `suggest.chat` / `suggest.edit` は必ず
  グレーの「その他」へ入る（9色目を作らない規約）。**畳みが1つでもあれば凡例を必ず出す**
  （系列が1本でも）／ツールチップに畳まれた実キーを並べる／**「その他」クリックで畳まれた
  キー全部の OR 絞り込み**が掛かるようにした。溢れる4つを選んだ理由は `FEATURE_SLOT` に明記。
- **空バケットをゼロ埋めして返す**（`usage_series.go`）。落とすと 30日プリセットで離れた日が
  隣接した棒として描かれ、時間軸として読めない。埋めるのが無意味な密度（>1,000 本＝90日 ×
  hour）では**埋めるのをやめる**（切り詰めない＝実データは必ず全部返る）。

**取りこぼし・競合（P3）**

- **codex 一発呼び出しのリトライを合算**（`codexOneShotWithRetry`）。1回目の消費を上書きして
  いたので、「安いモデルで失敗 → 既定のフラッグシップで撃ち直し」という**最も高くつく経路が
  1回分に見えていた**。テストのため runner を注入する形へ切り出した。
- **追記失敗後の回収を回帰で固定**。watermark を進めないので次のパスで差分として出てくる。
  部分的に書けていた分は §7-4 の `(ref, idx)` 重複排除が落とす（書き手と読み手の二段構えが
  噛み合っていることの回帰）。
- **UTC 日跨ぎの追記/畳み込み競合を塞いだ**。`usageMu` と `usageRollupMu` が別ロックなので、
  日跨ぎ直前に日を決めた追記が畳み込みの後にそのファイルへ着地すると、「畳み済み」判定で
  二度と読まれない（黙って消える）。畳む側が **`usageMu` を保持したまま「その日はもう
  伸びないか」を確かめて読む**ようにした（追記側は同じロック内で日を決めている）。

回帰は `usage_dedup_test.go` / `usage_provider_test.go` / `usage_series_test.go` /
`usage_ledger_test.go` / `series.test.ts` / `UsageView.test.tsx` に追加。**修正を戻すと落ちる
ことを1件ずつ確認済み**（止まったターンの回帰は stub CLI を PATH に置いて実際に
`sendStream` を撃つ形）。go test 740 / vitest 470 / typecheck / i18n:lint 緑。

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

1〜5 は **2026-07-26 に全て推奨案で決着**（ADR 0029 §7）。

1. ~~UI の置き場~~ → **決定: 設定モーダルの新タブ**（§5 の5点）。View をモーダル非依存に切って
   ペイン昇格の余地だけ残す。
2. ~~コスト表示~~ → **決定: `cost_usd` は claude で実測が取れた時だけ記録**し、UI では
   「API 換算相当額（claude のみ実測）」と明記した**副次表示**に留める。主指標は `spend`。
   **2026-08-31 追補**: 実測が取れない分（＝セッション本体＝一番大きい消費）に金額が付かない
   ままだったので、単価表 × トークンの**推定** `cost_est_usd` を別値で足した（§5-b）。主指標が
   `spend` であることと、推定を実測に混ぜないことは変えていない。
3. ~~セッション本体を含めるか~~ → **決定: 含める**（`feature=session`）。補助だけ見たい時は
   feature フィルタで絞る。
4. ~~保持期間~~ → **決定: raw 90日**（`AF_USAGE_RETENTION_DAYS`）・**rollup 無期限**。
5. ~~フリート横断（P6）~~ → **決定: v1 はワークスペース内で閉じる**。CP 集約は P6（任意）で
   集計値のみ。
6. ~~是正の順序~~ → **決着: P0.5 を台帳より先に実施済み**（2026-07-25）。「before」は本 doc の
   実測表が担い、台帳は after の定常状態を見せる役になる。残るモデル面の判断は cursor
   （`auto` のまま = 解決後モデル不明）と opencode（カタログ≠権利の罠で自動ピン見送り）の2つで、
   どちらも台帳が動き出してから実データで再考する。
7. **`compact`（要約）に思考トークン抑制を効かせるか**: 要約は品質が引き継ぎに直結するので、
   一律 0 にはしない。実測してから判断。**台帳が動いたので、`feature=compact` の
   `trigger`（manual/auto/recovery）別実測が出てから決める**。
8. **opencode のモデル報告**: 未検証のまま（本ワークスペースは未ログイン）。ログインのある
   環境で `opencode run --format json` に `modelID` が乗るかを実測し、`model_src` を確定する。
