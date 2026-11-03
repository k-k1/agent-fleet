# 0128. ハーネス・ルート・メーカー — セッション種別の横に、モデルのルートを第 2 の軸として置く

[English](0128-harness-route-maker.md) | 日本語

- 状態: **提案**（2026-11-03）。まだ何も作っていない。本記録は設計のみで、下の決定 A〜C はユーザーの判断待ち。
  `file:line` はすべて `c08c6a4b5` で読んだ。Claude Code の Bedrock / Vertex / ゲートウェイ用として挙げる環境変数と
  設定はベンダーのドキュメントによるもので、AF の起動フラグの下では**測っていない**。決定 4 のハーネス × ルートの行は、
  ライブゲート（決定 11）を通るまでどれも候補である。
- 関連: [0069](0069-image-generation-providers.ja.md)（資格情報の層・`AWSProfileRef`）/
  [0071](0071-self-hosted-inference-engines.ja.md)（エンジンのゲートウェイとセッション単位のトークン）/
  [0093](0093-lcpp-agent-kind.ja.md)（`lcpp` — ルート 1 本に溶接されたハーネス）/
  [0126](0126-aws-runs-in-the-mirror-and-confirm-before-use.ja.md)（使う前に確認を求める AWS プロファイル）/
  [docs/log/129](../log/129-pi-agent-kind.md)（pi の Stage 0）/ #1585（pi を種別にする件）

## 背景

セッション種別（`claude`・`opencode`・`codex` …）は、セッションを回す CLI か自前のループを指す。今はこれに別の事実が
2 つ畳み込まれていて、コードにはもう無理が出ている。

- **どの接続先で、どの請求か。** `usageCatalogProviders`（`workspace/agent/usage_catalog.go:45`）は種別だけで単価表を
  選ぶので、Bedrock 上の `claude` セッションは Anthropic API の単価で計算される。`usagex.Record`
  （`workspace/agent/internal/usagex/ledger.go:83`）が持つのは `Kind` と `Model` だけなので、サブスクリプションに含まれる
  ターンとトークン従量で請求されるターンが同じ形で記録される。
- **誰がモデルを作ったか。** `model_provider.go` はモデル選択欄のブランドマークのためにメーカーをすでに解決していて、
  その冒頭で「id の接頭辞はメーカーではなく課金ルートを示す」とはっきり書いている。それでも `modelKindVendor`
  （`model_provider.go:122`）は `kiro` を `amazon-bedrock` に対応させていて、ルートがメーカーの代わりをしている。
- **Bedrock の応急処置。** `keepInMenu`（`workspace/agent/internal/agents/opencode/catalog.go:146-165`）は、セッションに
  届く資格情報が無いために `amazon-bedrock/…` の 121 モデルを隠しており、足りない部品を「Bedrock に自分の接続と資格情報を
  与える」プロバイダ層だと名指ししている。
- **`lcpp`**（ADR 0093）は自前のループと自前エンジンを 1 つの種別にしたもの。同じエンジンはすでにプロバイダとして
  opencode にも届いている（`syncEngineProviders`、`workspace/agent/engines.go:323`）。つまりルートはもうハーネスをまたげる。
  名前が無いだけである。
- **サブスクリプションと従量の制限はすでに区別されている**: `LimitWindow` / `LimitSpend`
  （`workspace/agent/internal/agents/usagelimit.go`）。足りないのは、その違いに応じて動く手段である。
- **pi**（docs/log/129）は多くのプロバイダと話すハーネスである。第 2 の軸が無いと、プロバイダごとに 1 種別として
  加わるしかない。

場当たりに変えると危ない罠が 2 つある。

- Claude Code は、自分の OAuth ファイルより環境の `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` を優先する
  （`workspace/agent/internal/agents/claude/authexpiry.go:138-142`）。Workspace 自身の環境に API キーがあると、
  すべての `claude` セッションが黙って従量請求に移る。
- codex は環境変数で渡した `OPENAI_API_KEY` を無視し、`CODEX_HOME` ごとに `auth.json` を 1 つ持つ
  （`workspace/agent/internal/agents/codex/auth.go:25`）。だから codex では「このセッションはサブスク、あれは API」を
  環境変数では表せない。

## 決定

### 1. 3 つの言葉

| 言葉 | 意味 | 例 |
|---|---|---|
| **ハーネス** | エージェントのループを回すプログラム。今のセッション種別そのもので、変えない。 | Claude Code、opencode、codex、pi、`lcpp` |
| **ルート** | モデル呼び出しがどこへ行き、どの資格情報で、どう請求されるか。 | Anthropic サブスクリプション、Anthropic API、Amazon Bedrock、Google Vertex、OpenCode Zen、ChatGPT サブスクリプション、配備のエンジン |
| **メーカー** | 誰がモデルを作ったか。`model_provider.go` がすでに解決しており、変えない。 | Anthropic、OpenAI、Zhipu |

新しい軸には「プロバイダ」を使わない。`model_provider.go` とモデル選択欄がすでにメーカーの意味で使っている。

### 2. `kind` はそのまま。ルートは新しい任意フィールド

`session.Meta` に `Route string` を足す。空は種別の既定ルートを意味し、それが今のすべての種別の動きなので、既存の
セッション・API の呼び出し元・スケジュールはどれも挙動が変わらない。種別の文字列は分割せず、`claude-bedrock` のような
種別も足さない。ツリーは約 60 か所の `case` / `==` で種別により分岐し、約 525 か所で種別を参照している。組み合わせの
種別を 1 つ足すたびに、それら全部の写しが 1 つ増える。

### 3. ルートはカタログの 1 項目

ルートが記録するもの:

- `id`（`anthropic-subscription`・`anthropic-api`・`bedrock`・`vertex`・`opencode-zen`・`chatgpt-subscription`・
  `openai-api`・`af-engine-llm` …）
- 通信方式（Anthropic Messages、OpenAI Responses、OpenAI Chat Completions、Bedrock、Vertex）
- 請求の種類: `subscription`・`metered`・`self-hosted`・`free`
- 資格情報の層。ADR 0069 の層を使う: 既存の接続、メンバーのキー、`AWSProfileRef`、テナントのキー

カタログは Agent が持つ。Control Plane は生成したスナップショットを埋め込む。`agentkinds/catalog.json` を生成し、
テストで等しく保っているのと同じやり方である。

### 4. ハーネス × ルートは、ハーネスが宣言する許可リスト

複数のルートを提供するハーネスは、任意のインターフェースを実装する。

```go
type RouteAdapter interface {
	Routes() []string                                        // routes this harness may run on
	ApplyRoute(m session.Meta, route string) (RoutePlan, error) // env, args, config patch
}
```

このインターフェースを持たない種別は暗黙のルートをちょうど 1 つ持ち、Console はその種別にルート選択欄を出さない。
許可リストは直積ではない。技術だけでなく規約の理由もある: Claude のサブスクリプションのログインは Claude Code 専用
（docs/log/129 §0）なので、`anthropic-subscription` を opencode や pi に提供することはない。

候補の行（どれも未測定）:

| ハーネス | 候補のルート | 適用のしかた |
|---|---|---|
| claude | `anthropic-subscription`（既定）、`anthropic-api`、`bedrock`、`vertex`、ゲートウェイ | OAuth ファイル、`ANTHROPIC_API_KEY`、`CLAUDE_CODE_USE_BEDROCK=1`、`CLAUDE_CODE_USE_VERTEX=1`、`ANTHROPIC_BASE_URL` / `ANTHROPIC_BEDROCK_BASE_URL`（ベンダーのドキュメント） |
| codex | `chatgpt-subscription`（既定）、`openai-api` | セッションごとの `CODEX_HOME` か `model_providers` の項目。要測定 |
| opencode | opencode 自身のプロバイダ、`af-engine-llm` | すでにモデル id に入っている。リクエスト単位なので、共有の `serve` デーモンは障害にならない |
| pi | `chatgpt-subscription`、`openai-api`、`bedrock`、`af-engine-llm` | pi 自身のプロバイダ設定（未構築、#1585） |
| agy、kiro、cursor、copilot、muse | 各 1 本 | 変更なし |

各ハーネスのルート一覧は、行が測られた時点で `guide/ref/agents.ja.md` に置く。この表は計画であって、参照先ではない。

### 5. 資格情報の渡し方 — 決定 A

資格情報をどうやってセッションに届けるか。

- **A1: セッションごとの環境変数。** ルートのキーを起動時の環境に入れる。opencode のプロバイダキーがすでに
  `LaunchPlan.Env` に乗っているのと同じやり方（`opencode/opencode.go:127-131`）。単純だが、エージェントのシェルから
  キーが読める。AWS プロファイルの場合は、AF がセッションに AWS の資格情報を渡さないという規則
  （`catalog.go:156-158`）を破る。
- **A2: AF のゲートウェイ。** Agent（または Control Plane）が資格情報を持ち、セッションには AF のエンドポイント向けの
  短命でセッション単位のトークンを渡す。エンジンのトークンはすでにこの方式で動いている（`engineSessionEnv`、
  `engines.go:669`）。代償は、ストリーミングの通り道にプロキシが入ること。
- **推奨: 層によって両方。** メンバーが自分のセッション用に貼ったメンバーのキーには A1 を許す。今日の opencode のキーと
  同じ露出である。モデルを呼ぶ以上のことができる資格情報、つまり `AWSProfileRef` とテナントのキーには A2 を必須とする。
  グローバルな環境にはルートの資格情報を一切置かない（`authexpiry.go` の罠）。

### 6. 使用量の台帳がルートを記録する

`usagex.Record` に `Route` と `Billing` を足す。単価の検索はまずルートで引き、ルートが空のときの予備として
`usageCatalogProviders` を残す。`subscription` の行はトークン数を保ち、費用は「含まれる」と表示する。測った $0 として
表示することはしない。これは最初に、単独で入れる（P0）。動くものを変えずに、今の表示を正すからである。

### 7. サブスクリプションと従量の切替 — 決定 B

想定する場面: セッションが 5 時間枠に当たり、メンバーは API で続けたい。

- **B1: 手動。** `LimitWindow` で止まったとき、ハーネスが従量のルートを宣言していて、メンバーがその資格情報を持っていれば、
  Console が「<従量ルート> で続ける」を提案する。受け入れると、別のルートで `--resume` してセッションを起動し直す。
  `LimitSpend` では提案しない。別の請求に移ることは、そこでメンバーが求めたことではない。
- **B2: オプトインの自動。** B1 と同じだが確認なしで行う。メンバーごとの日次の支出上限を必須とし、空にはできない。
- **B3: しない。**
- **推奨: 今は B1。B2 は B1 が使われたあと、別の ADR でのみ。** B1 で守る規則:
  - ルートは起動時に固定されるので、切替は起動し直しであり、台帳に記録する
  - プロンプトキャッシュは引き継がれないので、最初の従量ターンは文脈全体を定価で読む。提案の文面でそれを伝える
  - アシスタントチャット（`ClaudeChat`、`workspace/agent/internal/chatx/chat_providers.go:341`）は、明示的にルートを
    与えない限り既定のルートのままにする

### 8. Bedrock と Vertex — 決定 C

Bedrock のルートは `AWSProfileRef`（ADR 0069）で動くので、A2（決定 5）を使う。

- **C1:** メンバーがサインインしている任意のプロファイル。
- **C2:** メンバーがモデルのルート用として印を付けたプロファイル。`bedrock:InvokeModel*` だけを持つことを文書で求め、
  プロファイルが求めるなら ADR 0126 の使用前確認の対象にする。
- **推奨: C2。** ルートは毎ターン使われるので、そこに広いプロファイルを置くと、Workspace で最も広い常設の権限になる。

ルートが資格情報を供給するようになったら、opencode の `amazon-bedrock` の応急処置（`keepInMenu`）は
`AF_OPENCODE_SHOW_BEDROCK` ではなくルートの可用性で置き換える。

### 9. pi は 1 つのハーネスとして加わる

この軸が一番効くのが pi である: 種別は 1 つで、ルートは決定 4 を通して宣言する。pi をそもそも採るかどうかは #1585
（Stage 0b）に残す。本記録は、採るとしてもプロバイダごとに 1 種別にならないことだけを保証する。

### 10. `lcpp` は当面変えない

`lcpp` は種別のまま。配備のエンジンは `af-engine-llm` というルートになり、opencode は事実上すでにそれを使っている。
`lcpp` のループが将来ほかのルート（OpenAI 互換の任意のエンドポイント）を受け入れるべきかは、後の記録に残す。

### 11. どの行もライブのターンで関門を通す

ハーネス × ルートの行は、そのルートでのライブのターン 1 回が次を記録してから出す: 実際に応答したルート（トランスクリプト
かレスポンスヘッダから）、台帳が書いた使用量、そして陰性対照（資格情報を外した同じ起動が失敗し、それが目に見えること）。
これが決定 4 の候補表を「最初のメッセージで失敗するモデルの一覧」にしないための仕組みで、それこそ `keepInMenu` が防ぐために
存在しているものである。

## 段階

| 段階 | 内容 | 関門 |
|---|---|---|
| P0 | 台帳の `Route` / `Billing`、ルートによる単価検索、`guide/ref/glossary.ja.md` の用語 | 挙動の変化なし。台帳のテストが空のルートを扱う |
| P1 | `RouteAdapter`、claude の `anthropic-subscription` / `anthropic-api` をセッションごとの環境変数（A1）で | 両方の行で決定 11 |
| P2 | A2 のゲートウェイ、claude の `bedrock`、opencode の Bedrock を応急処置ではなくルートで | 決定 11、C2 のプロファイル確認 |
| P3 | #1585 が採るなら、pi を種別として | #1585 の Stage 0b、その後ルートごとに決定 11 |
| P4 | B1 の「従量ルートで続ける」提案 | 実際に枠で止まり、起動し直して再開できること |

## 退けた案

- **ハーネス × ルートごとに 1 種別**（`claude-bedrock`、`pi-openai`）。組み合わせが掛け算で増え、どれもが `kind` の
  分岐すべての写しになる。
- **種別の文字列を分割する**（`claude/bedrock`）。Control Plane・スケジュール・MCP ツール・保存済みセッションのすべてが
  読むフィールドの意味を変えることになる。任意フィールドなら何も壊さずに同じ利点が得られる。
- **Workspace ごとに 1 つのグローバルなルート設定。** 「対話はサブスク、スケジュールのバッチは API」を表せず、claude では
  環境変数の優先順位の罠をそのまま再現する。

## 未決の問い

1. 「ルート」の Console での文言（`console/src/lib/i18n/locales/` の i18n 用語。P1 で決める）。
2. テナント管理者がテナントごとにルートを許可・拒否できるようにするか。`allow_engine_llm` がエンジンを制限するのと
   同じやり方で（ADR 0084）。
3. Bedrock のスロットリングのエラーを `LimitWindow` / `LimitSpend` のどちらに対応させるか、それとも 3 つ目の種類が要るか。
4. B1 の提案以外の場面（たとえば明示的な再開）でルートを変えてよいか。

## 結果

- セッションに任意のフィールドが 1 つ増える。空のままのセッションでは何も変わらない。
- P0 が入った時点で、費用の報告がサブスクリプションと従量の違いについて正確になる。
- 新しい種別なしで claude の Bedrock / Vertex が可能になり、opencode の Bedrock モデルも同じ仕組みで戻ってくる。
- 新しいルートを 1 つのハーネスに足す費用は、アダプタの 1 行とライブの関門 1 つであって、新しい種別ではない。
