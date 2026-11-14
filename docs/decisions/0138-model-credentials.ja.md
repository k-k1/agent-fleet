# 0138. モデルの認証情報とハーネス：棚卸し、共有の規則、配備とテナントによる制限、設定画面の構成

[English](0138-model-credentials.md) | 日本語

- Status: **proposed**（2026-11-14）。何も作っていない。`file:line` はすべて `1c92e98f9` で読んだ。ADR 0136 は開いている
  PR（#4137、2026-11-14 時点のブランチ先頭）から読んだ。**実測したものと、していないもの。** ここで「実測」とはツリーを
  読んで確かめたこと、つまり各認証情報がどこに保存され、どのコードが読み、どの検査があるかを指す。実際の Workspace や配備
  に対しては何も動かしておらず、AWS も呼んでいない。ベンダーの CLI やエンドポイントの振る舞い（ログインがどのホストと
  通信するか、CLI に別の認証情報ファイルを向けられるか）は、引用した ADR が実測していない限り**未実測**であり、決定が
  それに依る箇所ではそう記す。2026-11-14 に別のセッション（codex `gpt-6.1-sol`）がレビューした：round 1、指摘 13 件。
  round 2、指摘 5 件。それぞれをツリーと突き合わせて確かめ、本文に織り込んだ。
- Tracking: #4159。Refs #3282。
- Follow-ups: まだ起票していない。段階（§8）はオーナーがどの選択肢を採るかで変わるので、決定が受け入れられた時点で
  Issue にする。それまでは未決の問い（§10）を #4159 で追う。
- Related: [0128](0128-harness-route-maker.ja.md)（ハーネス、経路、メーカー。この記録はその未決の問い 1 と 2 に答える）/
  [0129](0129-provided-model-routes.ja.md)（提供経路、付与、予算。この記録はその D3 の最後の項目を改める）/
  ADR 0136（pi。proposed、PR #4137）/ [0084](0084-engine-indicator-and-tenant-gate.ja.md)（テナントの関門、三値の
  書き方、4 つの場所）/ [0069](0069-image-generation-providers.ja.md)（認証情報の層）/
  [0122](0122-egress-gateway.ja.md)（送信ゲートウェイ）/
  [0126](0126-aws-runs-in-the-mirror-and-confirm-before-use.ja.md)（使う前の確認）/
  [0029](0029-usage-accounting.ja.md)（トークン台帳）

## 背景

設定 > 接続 > エージェント（`console/src/features/settings/agents/AgentsTab.tsx:AgentsTab`）はハーネスごとに 1 枚の
カードで、各カードがそのハーネスのログインとキーを持つ。この形は「ハーネス 1 つに認証情報 1 つ」を前提にしており、
その前提はもう成り立たない。

- **Anthropic の API キー**は OpenCode のカードで入力され（`OC_PRESETS` の `anthropic`、`agents/OpencodeCard.tsx:31`）、
  opencode のキー表（`secrets.Data.Opencode`、`workspace/agent/internal/secrets/secrets.go:488`）に保存され、claude の
  `anthropic-api` 経路が読む（`loadAPIKey`、`workspace/agent/internal/agents/claude/route.go:80`）。Claude Code の
  カードにはキーが一切出ず（`agents/ClaudeCard.tsx:ClaudeCard`）、キーが無いときの拒否文は OpenCode のカードへ行けと
  案内する（`workspace/agent/internal/mcpx/mcp_fleet_spawn.go:283`）。
- **pi**（ADR 0136、proposed）は OpenAI の API キーと Anthropic の API キー、つまり codex・opencode・claude が使うのと
  同じ種類のキーで動き、さらに自前の ChatGPT ログインを持つ。
- **Bedrock** は AWS タブでモデルの経路として印を付けた AWS プロファイル（ADR 0128 決定 11）か、配備が提供する経路
  （ADR 0129）で動く。どちらもエージェントのタブには無い。
- エージェントのタブには認証情報でないものも同居している。インストールと更新、再開、通知、リモートコントロール、起動の
  既定値、セッション設定の塊、画像生成の順序。

利用者は、配備とテナントがどの認証情報を使ってよいかを**制限**できることも求めている。少なくとも次の構成。

- **サブスクリプション禁止**：Claude のサブスクリプション、ChatGPT ログイン、opencode の Zen/Go、muse・agy・copilot・
  cursor・kiro の CLI 固定のログインを、配備全体または 1 テナントで禁止する。
- **API か Bedrock だけ**：従量課金の API キーと Bedrock（個人のもの、または提供されたもの）だけを許す。変形として
  **Bedrock だけ**（データを顧客の AWS に留めなければならない場合）と、**提供経路だけ**（個人の認証情報を一切使わない）。
- **混在**：配備はすべて許し、テナントが絞り、メンバーは決して広げられない。

今日、このようなものは何も無い（§1.4）。

## 1. 棚卸し（実測）

パスは `console/`・`control-plane/`・`deploy/` で始まるもの以外は `workspace/agent/` からの相対。「Console UI」は特に
断らない限り 設定 > 接続 の下の場所を指す。

### 1.1 認証情報

| # | 認証情報 | 保存場所 | 今日使っているもの | 使えうるもの | CLI 固定 | 経路 id | Console UI |
|---|---|---|---|---|---|---|---|
| 1 | Claude のサブスクリプションのログイン | `CLAUDE_CONFIG_DIR` の下の claude 自身の `.credentials.json`（`internal/agents/claude/authexpiry.go:135`、`credsPath`）。CLI のログインが書く（`claude/auth.go:44`、`HandleStart`） | claude のセッション。claude バックエンドのアシスタントチャットと単発呼び出し（`internal/chatx/chat_backends.go:24`） | claude だけ。ベンダーは OAuth ログインを自社のアプリケーション用に限っている（ADR 0128 決定 4、ADR 0136 の背景） | はい | `anthropic-subscription`（`claude/route.go:39`）。明示的に選んだときだけ。選択が空なら経路は無い（ADR 0128 決定 2） | エージェント > Claude Code のカード：接続、コード貼り付け、読み取り専用のプラン表示、再認証、切断（`agents/ClaudeCard.tsx:36`、`:152`） |
| 2 | 旧来の Claude OAuth トークン | `secrets.Data.Claude`（`internal/secrets/secrets.go:482`）。切断で消されるだけ（`claude/auth.go:125`） | 見つからない | なし | — | なし | なし |
| 3 | Anthropic の API キー | 暗号化ストアの `secrets.Data.Opencode["ANTHROPIC_API_KEY"]`（`secrets.go:488`。書くのは `opencode/auth.go:231`、`HandlePutConn`） | `anthropic-api` の claude。そのペインにだけ入れる（`claude/route.go:193-201`）。opencode。保存済みのキーをすべて環境に受け取り、共有の `serve` も含む（`opencode/auth.go:50`、`keyEnv`） | `anthropic-api` の pi（ADR 0136 決定 6、その P3） | いいえ | claude では `anthropic-api`。`ANTHROPIC_API_KEY@<sha256 の先頭>` に束ねる（`claude/route.go:260`、`BindRoute`）。opencode では無し | エージェント > **OpenCode** のカード > API キー、「Anthropic」プリセット（`OpencodeCard.tsx:31`） |
| 4 | ChatGPT ログイン（codex） | `~/.codex/auth.json`、`auth_mode: chatgpt`。`codex login --device-auth` が書く（`codex/auth.go:179`） | codex。画像生成の codex プロバイダ（`internal/imagegen/codex.go:6`、`:139`）。アシスタントチャットの codex バックエンド（`chat_backends.go:25`） | codex だけ（ファイルは codex のもの） | はい | `chatgpt-subscription` はカタログにある（`internal/routes/routes.go:73`）が、codex は経路を宣言せず、記録もしない（`codex/transcript.go:291-295`） | エージェント > Codex のカード、「ChatGPT サブスクリプション」（`agents/CodexCard.tsx:51`、`:200`） |
| 5 | OpenAI の API キー（codex の複製） | 同じ `~/.codex/auth.json`、`auth_mode: apikey`。`codex login --with-api-key` による（`codex/auth.go:138`） | codex（と codex が動かすもの：画像生成、チャットのバックエンド） | codex だけ。codex は環境の `OPENAI_API_KEY` を無視する（`codex/auth.go:25-26`） | はい | `openai-api` はカタログにある（`routes.go:75`）が、宣言するハーネスは無い | エージェント > Codex のカード、「OpenAI API キー」（`CodexCard.tsx:85`、`:166-185`） |
| 6 | OpenAI の API キー（opencode の複製） | `secrets.Data.Opencode["OPENAI_API_KEY"]` | opencode（`keyEnv`） | `openai-api` の pi（ADR 0136 決定 6、P1） | いいえ | なし | エージェント > OpenCode のカード > API キー、「OpenAI」プリセット（`OpencodeCard.tsx:32`） |
| 7 | opencode.ai のアカウントのログイン | opencode 自身のデータベース。その `serve` が持つ（`opencode/oauth.go:3-6`、`:405-410`）。Agent は保存しない | opencode | opencode だけ | はい | なし | エージェント > OpenCode のカード、アカウントのログイン。選んだ課金が opencode.ai を使うときだけ出る（`OpencodeCard.tsx:320`、`:422-458`） |
| 8 | OpenCode Zen / Go のキー | `secrets.Data.Opencode["OPENCODE_API_KEY"]`（`opencode/auth.go:27`）。1 本のキーで Zen と Go の両方を払う（`opencode/catalog.go:5-9`）。課金の選択が `own` か `free` なら落とす（`opencode/auth.go:59-67`） | opencode | opencode（pi は Zen/Go をプロバイダとして持つが、ADR 0136 は提供しない） | いいえ | `opencode-zen`、`opencode-go`。opencode が書いたプロバイダ id から付ける台帳のラベル（`internal/usagex/ledger.go:146-148`）で、`Routes()` には宣言されていない | エージェント > OpenCode のカード：課金の選択 `off/own/free/go/zen`（`OC_ROUTES`、`OpencodeCard.tsx:59`）、「go」プリセット、ワークスペース id の行（`:195`） |
| 9 | その他のプロバイダのキー（OpenRouter、Gemini、Sakana、任意の名前） | `secrets.Data.Opencode`。`^[A-Z][A-Z0-9_]{1,63}$` に合う名前なら何でも（`opencode/auth.go:23`） | opencode | opencode | いいえ | なし | エージェント > OpenCode のカード > API キー（`OpencodeCard.tsx:33-36`、`:473-517`） |
| 10 | Muse のログイン（アカウントか Meta の API キー） | `~/.config/muse/auth.json`（`muse/auth.go:59`）。`mechanism` で区別（`muse/auth.go:16`）。API キーは `muse auth set --api-key-stdin` で（`:301`）。Agent 自身の環境にある `META_API_KEY` はログインに優先し、報告されるが取り除かれない（`:120-130`） | muse。チャットのバックエンド（`chat_backends.go:30`） | muse だけ | はい | なし | エージェント > Muse のカード：サインイン、従量課金の警告付きの API キー（`agents/MuseCard.tsx:117`、`:157`、`:258-259`） |
| 11 | Antigravity（agy）の Google ログイン | `~/.gemini/antigravity-cli/antigravity-oauth-token`（`agy/auth.go:36-38`） | agy。画像生成の agy プロバイダ（`imagegen/agy.go:113`）。チャットのバックエンド（`chat_backends.go:27`） | agy だけ | はい | なし | エージェント > Antigravity のカード（`agents/AgyCard.tsx:34`、`:48`）。「gcp-project」方式は無効（`:156-161`） |
| 12 | GitHub の接続（Copilot） | git の接続を `gh auth token` で読む（`copilot/auth.go:43`） | copilot（`COPILOT_GITHUB_TOKEN`、`copilot/driver.go:308`）。git ホスティング | モデルの認証情報としては copilot だけ | 実質そう。メンバーの GitHub ログインで、モデルのプランは GitHub のもの | なし | エージェント > Copilot のカードは状態を出し Git へリンクする（`agents/CopilotCard.tsx:39`、`:54`）。ログインは Git > GitHub（`connect/GitTab.tsx:320`） |
| 13 | Cursor のログイン | `~/.config/cursor/auth.json`（`cursor/auth.go:4`）。API キーは見送り（`:104`） | cursor。チャットのバックエンド（`chat_backends.go:28`） | cursor だけ | はい | なし | エージェント > Cursor のカード（`agents/CursorCard.tsx:27`） |
| 14 | Kiro のログイン | `~/.local/share/kiro-cli/data.sqlite3`（`kiro/auth.go:4`）。API キーは見送り（`:110`） | kiro | kiro だけ | はい | なし | エージェント > Kiro のカード。インストール後（`agents/KiroCard.tsx:52`、`:100`） |
| 15 | モデルの経路として印を付けた AWS プロファイル | シークレットは無い。プロファイルは CP の設定の記録（`ssm_profile.model_route`、`control-plane/internal/store/migrations-pg/0107_ssm_profile_model_route.sql`）で、Agent が読む（`internal/awsx/profiles.go:77`、`:179`）。短命の認証情報は経路ゲートウェイのメモリ内で解決する（`internal/awsx/route.go:67-110`） | `bedrock` の claude（`claude/route.go:338`、`bedrockPlan`）と `bedrock` の opencode（`opencode/route.go:83`、`:202-232`）。それぞれ受け入れ用のスイッチの後ろ（`AF_CLAUDE_BEDROCK_ROUTE`、`AF_OPENCODE_BEDROCK_ROUTE`） | pi（ADR 0136、P2）。codex は OpenAI 系のモデルに限る（ADR 0129 H） | いいえ | `bedrock`。`<プロファイル id>@<リージョン>#<指紋>` に束ねる（`routes.go:204-236`） | **AWS** タブ（`ssm` 節）の「モデルの経路」チェックボックス（`workspace/SsmTab.tsx:665-675`）。起動ダイアログが印付きのプロファイルを並べる（`console/src/features/repos/bedrockProfiles.ts:46`） |
| 16 | 配備が提供する Bedrock | 保存するシークレットは無い。CP 自身の ID が経路用のロールを引き受ける（`control-plane/internal/routegateway/sts.go:55`）。Agent は発行用トークン `AF_ROUTE_ISSUE_TOKEN` を持つ（`internal/routegw/cproutes.go:100`） | `bedrock-deployment` の claude。`AF_PROVIDED_ROUTES=1` と `AF_CLAUDE_BEDROCK_DEPLOYMENT_ROUTE` の後ろ（`claude/route.go:145`） | opencode、codex（ADR 0129 Q6、Q7）、pi（ADR 0136 P2） | いいえ | `bedrock-deployment`。`<出どころ id>#<指紋>` に束ねる（`control-plane/internal/providedroutes/providedroutes.go:102`） | Console には**無い**。super_admin と tenant_admin の REST はある（`control-plane/provided_routes.go:77-84`）が、画面は無い（ADR 0129、Q1 part 2） |
| 17 | テナントが提供する Bedrock | ストアだけ。`provided_route_source` の行（`control-plane/internal/store/migrations-pg/0111_provided_routes.sql:15`）。REST のハンドラは無く、ゲートウェイは拒否する（`control-plane/internal/routegateway/gateway.go:276-278`） | なし | claude、opencode、codex、pi（ADR 0129 Q5） | いいえ | `bedrock-tenant`（カタログだけ） | なし |
| 18 | 配備のエンジン（自前ホスト） | CP が発行するエンジンのトークン（`control-plane/engine_token.go:46`） | lcpp（`engines.go:1328-1336`）。opencode のエンジンのプロバイダ（`opencode/engine.go:42`）。画像生成のエンジンのプロバイダ | pi（ADR 0136 P3） | いいえ | `af-engine-llm`（台帳のラベル。lcpp が記録する） | 管理 > エンジン、エンジンのトークン（`console/src/features/settings/admin/AdminTab.tsx:87-88`）。テナント > エンジンの利用 |
| 19 | メンバーの llama.cpp | `secrets.Data.Lcpp`（`secrets.go:492`）。`PUT /connections/lcpp` が書く（`connections.go:651-654`）。エンジンより優先（`engines.go:1322-1326`） | lcpp（`lcpp/driver.go:642`）。チャットのバックエンド | lcpp と、OpenAI Chat Completions を話す任意のハーネス（設計していない） | いいえ | `member-llamacpp`（台帳のラベル、`lcpp/store.go:310-319`） | エージェント > llama.cpp のカード（`agents/LcppCard.tsx:170-190`） |
| 20 | pi の ChatGPT ログイン（提案中） | Workspace に 1 つのログインファイル。`pi auth login openai` を包む AF の手順が書く（ADR 0136 決定 12） | — | pi だけ | はい | `chatgpt-subscription`（ADR 0136 決定 6） | pi のカード（ADR 0136 決定 12） |
| 21 | Google Vertex | 何も無い。カタログの id だけ（`routes.go:78`）。Vertex のコードは見つからない | なし | claude（ADR 0128 決定 5） | — | `vertex` | なし |

行 4 と行 20 から一つ分かることがある。同じ ChatGPT のアカウントに、codex 用と pi 用で 2 度ログインすることになり、
その 2 つのファイルはどちらのハーネスも共有できない（ADR 0136 はログインの複製を退けている）。pi が codex のファイルを
読めるかどうかは**未実測**。

### 1.2 認証情報を使う、セッション以外のもの

呼び出し元はセッションだけではない。

| 機能 | 使う認証情報 | どこで選ぶか |
|---|---|---|
| アシスタントチャット、`ask_assistant`、タイトルとブランチ名の提案 | claude・codex・opencode・agy・cursor・lcpp・muse のチャットのバックエンド（`internal/chatx/chat_backends.go:24-30`）。使えないバックエンドに固定された会話は、送信時に別のものへ**フォールバック**する（`chat_providers.go:57-60`） | アシスタントの設定。使えるかどうかは `claude auth status` / `codex login status` で、1 分キャッシュする（`chat_providers.go:61-62`） |
| 画像生成 | codex の ChatGPT ログイン、agy のログイン、配備のエンジン（`imagegen/imagegen.go:464-478`、`:527-528`） | エージェントのタブの「画像生成」とプロバイダの順序（`agents/AgentsTab.tsx:253-278`） |

これらの機能のトークン台帳の行は `feature` の次元を持つ。経路を持つかどうかは**未実測**。

### 1.3 経路 id と、それを宣言するもの

`control-plane/internal/agentroutes/catalog.json`（`internal/routes/routes.go:66-79` から生成）より。

| 経路 id | 通信 | 認証情報の層 | 宣言するもの（`Routes()`） |
|---|---|---|---|
| `anthropic-subscription` | anthropic-messages | existing-connection | claude |
| `anthropic-api` | anthropic-messages | member-key | claude |
| `bedrock` | bedrock | aws-profile-ref | claude、opencode |
| `bedrock-deployment` | bedrock | deployment | claude |
| `bedrock-tenant` | bedrock | tenant-key | なし |
| `vertex` | vertex | tenant-key | なし |
| `chatgpt-subscription` | openai-responses | existing-connection | なし（pi、提案中） |
| `openai-api` | openai-responses | member-key | なし（pi、提案中） |
| `opencode-zen`、`opencode-go` | per-model | existing-connection | なし（台帳のラベル） |
| `af-engine-llm` | openai-chat | deployment | なし（台帳のラベル） |
| `member-llamacpp` | openai-chat | member-key | なし（台帳のラベル） |

agy・codex・copilot・cursor・kiro・lcpp・muse・shell・ssm は何も宣言しない。§1.1 の認証情報のうち 7 つ（行 7、9、10、
11、12、13、14）には**経路 id が一つも無い**。

### 1.4 今日ある制御

| 層 | 制御 | 場所 | 対象 |
|---|---|---|---|
| operator | 受け入れ用のスイッチ `AF_WS_CLAUDE_BEDROCK_ROUTE`、`AF_WS_OPENCODE_BEDROCK_ROUTE` | `control-plane/workspace_lifecycle.go:572`、`:576` | ハーネス × 経路の行を 1 つずつ開く。リリースの設定ではない |
| operator | `AF_PROVIDED_ROUTES` / CFN の `ProvidedRoutes` | `control-plane/internal/routegateway/gateway.go:48`、`deploy/aws/ecs/cfn/30-ingress.yaml:310` | 提供経路がそもそもあるか |
| operator、super_admin | 送信の許可リスト | `control-plane/egress_policy.go:21`（`defaultEgressAllowlist`）、`:103`（`egressBuiltin`） | ホスト単位、**配備全体だけ**（`control-plane/egress_member.go:188`: "EffectiveAllowlist ignores scope"）。Anthropic のホストと `.amazonaws.com` は組み込みで、外せない |
| super_admin | テナントごとのエンジン利用。`*bool`、nil = 許可 | `control-plane/limits.go:135-136`（ADR 0084 決定 7） | 自前ホストのエンジンだけ |
| super_admin | 提供経路の付与と上限。`bool`、既定はオフ | `control-plane/provided_routes.go:82`（ADR 0129 D3） | `bedrock-deployment` だけ |
| tenant_admin | 提供経路の方針、メンバーへの付与と予算 | `control-plane/provided_routes.go:83-84` | `bedrock-deployment` だけ |
| tenant_admin | メンバーごとのエンジン利用 | `control-plane/engine_access.go:70` | エンジンだけ |
| tenant_admin | メンバーの既定値（エージェント、モデル、実行方式、予算） | `console/src/features/settings/tenant/tenantMemberDefaults.tsx:43` | 既定値であって制限ではない。経路は無い |
| member | 種類のオン/オフ、`CreatePolicy.EnabledPref` | `internal/agents/createpolicy.go:18`。設定しているのは lcpp だけ（`lcpp/create_policy.go:13`） | 作成を 403 で拒否する |
| member | opencode の課金 `off` | `opencode/catalog.go:44-63`、`console/src/agents/registry.ts:586` | opencode 全体 |
| member | 隠すモデル | `internal/sessionx/model_deny.go:82`（`HiddenModelsRaw`）、`console/src/lib/settings/prefs.ts:201` | モデル。起動時と MCP の `list_models` で効く |
| — | **配備やテナント単位で、種類・経路・認証情報の区分を許可または拒否するもの** | **見つからない** | — |

制限が埋めなければならない隙間。いずれも実測。

- Console は、サブスクリプションのログインが接続済みのときだけ claude を出す（`console/src/agents/registry.ts:266`）。
  Anthropic の API キーか印付きの AWS プロファイルしか持たないメンバーは、Agent なら動かせるのに claude を選べない。
- af の MCP ツールでセッションを起こすセッションは、Agent 自身の `POST /sessions` を呼ぶ
  （`internal/mcpx/mcp_client.go:270`）。CP の起床前の検査（`control-plane/workspace_handlers.go:1171-1180`）は
  それを見ない。
- 空の経路選択は起動後に証明できない（ADR 0128、2026-11-05 の P4a の注記）。空の選択で起動したペインがどの認証情報を
  受け継いだかを示す、読めるものは無い。
- 台帳の記録は `Route` と `Billing` を持つが接続の欄を持たず（`internal/usagex/ledger.go:172-173`）、codex・muse・
  agy・copilot・cursor・kiro のターンには経路が付かない。
- CP はメンバーのモデルの認証情報を一切持たず、それを使った経路ごとのトークン使用量も持たない。CP の使用量の表は
  Workspace の稼働時間（`control-plane/usage.go`）と提供経路の呼び出し（`provided_route_call`）である。トークン台帳は
  Workspace にある（ADR 0029）。

## 2. 言葉

| 言葉 | 意味 | 他の 3 つとの関係 |
|---|---|---|
| **認証情報**（credential） | モデルへの呼び出しを通し、誰が払うかを決めるもの。ログイン（サブスクリプションや CLI のアカウント）、API キー、AWS プロファイル、提供された出どころ、自前ホストのエンドポイント。画面では既にある場所では「接続」のまま（用語集） | メンバーが持つ（提供された出どころならテナントか配備が持つ）。**経路**は認証情報からモデルへの道筋を名付け、**ハーネス**は自分が宣言した経路を通してだけそれを使い、**ポリシー**が使ってよいかを決める |
| **ハーネス**（harness） | エージェントのループを回すプログラム。今日のセッションの種類（ADR 0128 決定 1）。画面では「エージェント」 | 自分が動ける経路を宣言する。このモデルで持つものは、自分で保持する CLI 固定のログインだけ |
| **経路**（route） | モデルへの呼び出しがどこへ、どの認証情報で行くか（ADR 0128 決定 1）。経路 id で識別する | 他の 3 つの結び目。1 つの認証情報（またはその枠）を、1 つ以上のハーネスが宣言し、ポリシーが判定する。ポリシーの検査はすべて経路 id の上で評価する |
| **ポリシー**（policy） | どの経路を使ってよいか。operator、super_admin、tenant_admin、member が層で決める | 経路 id だけを、その id の区分と宛先（D1）を通して読む。ハーネスを名指しすることは無い。ハーネスは、宣言した経路のうち少なくとも 1 つが許され、その認証情報を持っているときに使える |

経路の 2 つの属性が、人がポリシーを書けるようにする。

- **認証情報の区分**：`subscription`（プランやアカウントのログイン。CLI 固定かどうかは問わない：行 1、4、7、8 の Go、
  10 のアカウントのログイン、11、12、13、14、20）、`member-key`（従量課金のキー：行 3、5、6、8 の Zen、9、10 の API
  キー）、`member-cloud`（行 15）、`provided`（行 16、17）、`self-hosted`（行 18。行 19 はエンドポイントが承認されている
  ときだけ。下を参照）。行 8 は経路で分ける。Zen は呼び出しごとにキーへ
  課金し（台帳は料金の付いた Zen の呼び出しを `metered` と書く。`usagex/ledger.go:137`、`BillingOfOpencodeCall`）、
  Go はサブスクリプションである。
- **宛先**：プロンプトを受け取る者。ベンダー（Anthropic、OpenAI、Google、Meta、GitHub、Cursor、Kiro、opencode.ai、
  OpenRouter、Sakana）、ある AWS アカウントの Amazon Bedrock、配備のエンジン、承認された自前ホストのエンドポイント、
  または `unclassified`（AF が保証できない宛先）。
- **宛先は、それを固定したエンドポイントの分しか当てにならない。** メンバーの llama.cpp の接続は任意の `http(s)` の
  URL を受け付け、キーは任意である（`connections.go:629-641`）。そこにベンダーやプロキシを入れられ、`self-hosted` と
  誤って区分されうる。そこで、宛先の制限が何か一つでもあるとき、メンバーのエンドポイントは `unclassified` とする。
  ただし、そのホストが**承認済みの自前ホストのエンドポイント**であれば、その宛先の `self-hosted` とし、接続は承認された
  ホストに束ねる。承認はポリシーと同じ共通部分に従う（D7）。operator の上限が、そもそも承認してよいホストのパターンを
  決め（宛先を制限する上限のもとで未設定なら無し）、super_admin がその集合を配備とテナントごとに絞り、tenant_admin は
  残ったものの内側のホストだけを承認する。カタログでベンダーの宛先のホストに当たるホストは、どの層でも自前ホストとして
  承認できない。したがって除かれたベンダーに別の名札を付けることはできない。エンドポイントを上書きできる認証情報にも同じ規則を当てる。
  ベンダーのキーがそのベンダーの宛先を保つのは、ハーネスがベンダーのエンドポイントを固定している間だけ（claude の経路が
  `ANTHROPIC_BASE_URL` を固定し、それを設定する設定ファイルを拒むように。ADR 0128 の 2026-11-04 と 2026-11-05 の注記）
  で、上書きされれば `unclassified` になる。

**メーカー**（ADR 0128）は変わらず、ポリシーの軸ではない。Bedrock の Claude と Anthropic API の Claude はメーカーが
同じで宛先が違い、データの所在の規則が問題にするのは宛先である。**出どころ**（ADR 0129 決定 1：member、tenant、
deployment）は区分に畳み込む。**課金**（ADR 0128 決定 8）は呼び出しごとの事実のままでポリシーの入力にはしない。
呼び出しの後でしか分からないからである。

**ADR 0128 の未決の問い 1（経路の名前）。** 画面の言葉はその P1b の注記で決まっている（"Model route" / モデルの
ルート。日本語の地の文では 経路、用語集）。この記録は **id** を決める：D2。
**ADR 0128 の未決の問い 2（テナントによる許可・拒否）。** 答え：はい。メンバー自身の認証情報の経路も含め、すべての
経路について（D7）。これは **ADR 0129 D3 の最後の項目を改める**。そこではメンバー自身の認証情報の経路をテナントの
制御の外に置いていた。

## 3. 認証情報と共有についての決定

### D1. 制御の単位は経路で、区分 × 宛先として書く

- **D1a. ハーネス。** 単純で、起動ダイアログが既に絞り込んでいる種類の一覧と合う。「claude は良いがそのサブスクリプ
  ションは駄目」を言えない。これは利用者が最初に求めた構成である。
- **D1b. 経路 id。** 正確で、すべての検査が評価するもの。管理者の単位としては、ハーネスの行が増えるたびに伸びる十数個
  の id の一覧になり、新しい id は思いがけず許されるか、思いがけず拒まれる。
- **D1c. 認証情報の区分 × 宛先を、経路 id の上で評価する**（*推奨*）。カタログはすべての経路 id にちょうど 1 つの区分と
  1 つの宛先を割り当てる（D2）。ただしカタログが**接続ごと**と印を付けた経路（`member-llamacpp`、`custom-api`、
  メンバーがエンドポイントを設定できるすべての経路）は除き、それらの区分と宛先は、接続が束ねられた検証済みの
  エンドポイントから来る（§2）。したがって評価する単位は経路 id と、それらの経路ではそのエンドポイントの束ねである。
  ポリシーの 1 つの層は、許す区分の集合と許す宛先の集合で、どちらも既定は「すべて」。
  経路は、その区分と宛先がすべての層で許されているときに許される。構成は短く書ける。「サブスクリプション禁止」は
  区分を 1 つと宛先を 1 つ外し（§6）、「Bedrock だけ」は宛先を 1 つだけ残し、「提供経路だけ」は区分を 1 つだけ残す。後から足した経路 id は
  区分と宛先を通じて規則を受け継ぐ。割り当ての無いものは `unclassified` で、何か一つでも制限があればどこでも拒む
  （閉じる側に倒す）。代償：カタログを正直に保つ必要があり、新しい経路 id のレビューでは属性を 2 つ確かめる。

経路 id ごとの例外（「Copilot 以外のサブスクリプションは禁止」）はこの記録に入れない（未決の問い 1）。

### D2. すべての認証情報の道筋に経路 id を付ける（ADR 0128 の未決の問い 1）

ポリシーは名前のあるものしか判定できない。今日は 7 つの認証情報に名前が無く（§1.3）、「サブスクリプション禁止」の
規則には muse も cursor も見えない。

- 各種類は、メンバーが選べる経路（`RouteAdapter.Routes()`）に加えて、自分の規則が解決する**固定の経路**
  （ADR 0128 決定 4 の `ResolveRoute`）を宣言する。アダプタの無い種類は固定の経路だけを宣言し、そのセッションは
  それで記録される。
- チャットのバックエンドと画像生成のプロバイダも同じように経路を宣言する。
- **名前の規則。** id は認証情報を名付け、ハーネスは名付けない。ベンダーのプランやキーなら
  `<宛先>-<subscription|api>`（`anthropic-subscription`、`openai-api`、`openrouter-api`、`gemini-api`、`sakana-api`、
  `meta-api`）。ログインが 1 つのハーネスのためだけにあり、そのプランが切り離せる製品でない場合に限って
  `<ハーネス>-login`（`muse-login`、`agy-login`、`copilot-login`、`cursor-login`、`kiro-login`、`opencode-account`）。
  opencode の任意のキーは `custom-api` で、宛先は `unclassified`。既存の id は名前を変えない。opencode の無料モデルは
  認証情報を持たないが、プロンプトは opencode.ai へ行く。台帳はそれを `opencode-zen`（プロバイダ `opencode`、
  `usagex/ledger.go:146-148`）に入れるので、Zen の区分と宛先に従う。無料の呼び出しが常にそのプロバイダを報告するか
  どうかは**未実測**。
- **ログインの方式を複数持つ種類は、実行のときに方式ごとに解決する。** muse は `mechanism` から `muse-login` か
  `meta-api` に解決する（`muse/auth.go:96-130`。Agent の環境にある `META_API_KEY` はログインに優先するので `meta-api`
  と数える）。codex は `auth_mode` から `chatgpt-subscription` か `openai-api` に解決する。方式はセッション、チャットの
  呼び出し、画像の呼び出しが始まるときに読み、種類から決めつけない。
- 1 つの経路 id を 2 つの認証情報の複製が担うことがある。codex 自身の API キー（行 5）と共有の OpenAI キー（行 6）は
  どちらも `openai-api` で、接続の参照が両者を分ける（D5）。

### D3. 保存した 1 つの認証情報が、それを使えるすべてのハーネスに仕える

- **D3a. ハーネスごとに自分の複製を持つ**（今日の OpenAI キー、行 5 と 6）。2 つのハーネスを使うメンバーはキーを
  2 度貼り、2 度失効させ、支出はキーではなくハーネスで分かれて見える。
- **D3b. プロバイダのアカウントにつき認証情報は 1 つ。その経路を宣言するすべてのハーネスが、それぞれ自分の隔離の
  仕組みで使う**（ADR 0128 決定 7：メンバーのキーならセッションごとの環境 A1、それより広いものはゲートウェイ A2）
  （*推奨*）。メンバーは認証情報ごとに、どのハーネスに使わせるかを決める（「使う先」、D6）。既定は使えるハーネス
  すべてで、opencode と claude が Anthropic のキーについて既にしていることと同じ。
- **D3c. ハーネスどうしで CLI のログインファイルを共有する**（pi が codex の `auth.json` を読む、opencode が claude の
  OAuth ファイルを読む）。退ける。Claude の OAuth ログインは Claude Code 専用（ADR 0128 決定 4）。リフレッシュトークン
  を回すプロバイダでは、1 つのプロセスの更新が別の複製を無効にしうる（ADR 0136、退けた案）。ファイル形式はそれぞれ
  ベンダーのもの。

D3b のもとで、CLI 固定のログイン（行 1、4、5、7、10、11、13、14、20）は CLI が保持する場所に留まり、ハーネスに 1 つ
ずつで、そうと分かるように表示する（D6）。codex は環境の `OPENAI_API_KEY` を無視する（`codex/auth.go:25-26`）ので、
codex に共有のキーをセッションごとに渡す方法（セッションごとの `CODEX_HOME` か `model_providers` の項目、ADR 0128
決定 5。**未実測**）が実測で示されるまで、codex の API キーは自分の複製のままとする。

画面で言うべき帰結が一つある。opencode の共有の `serve` は保存済みのプロバイダのキーを**すべて**受け取る
（`opencode/auth.go:50`）。D3b のもとでは、メンバーが「claude 用に」入れたキーも、「使う先」で外さない限り opencode に
届く。今日それは黙って起きている。

### D4. 失効：束ねたものは拒み、動いているものは止め、セッションを決して移さない

今あるもの：claude の `anthropic-api` のセッションはキーの指紋に束ねられ、キーを差し替えたり消したりすると次の起動が
拒まれる（`claude/route.go:260`、ADR 0128 の 2026-11-04 の注記、round 1 (5)）。動いているペインは起動したときのキーを
止まるまで使い続ける。opencode の `serve` は再起動するまで環境を保つ（`OpencodeRestartRow`、`OpencodeCard.tsx:157`）。

- **D4a. 今のまま。** 消したキーが、動いている opencode の serve と claude のペインで、それらが生きている限り使える。
- **D4b. 束ねたセッションは次の起動で拒み、動いているセッションは経路ガードの巡回で止め、共有のデーモンは認証情報
  無しで再起動する**（*推奨*）。認証情報を消すと `RouteWatcher`（`SweepRouteGuards`、
  `internal/sessionx/session_route.go:147`）が「束ねた認証情報が無いか差し替えられた」を見るように広がる。セッションは
  止められ（経路ガードが既にするように再開できる）、通知はその認証情報を名指す。共有の `serve` や app-server は
  通常の再起動ではなく、D12 の**保安のための再起動**で置き換える。
- 削除と差し替えのダイアログは、起きる前に何が起きるかを言う：「Claude Code（保存済みのセッション 2 件）、opencode
  （共有のサーバ）が使っています」。差し替えは新しい指紋なので、束ねたセッションは拒まれ、決して移されない
  （ADR 0128 決定 3）。
- ベンダー側での失効（ベンダーのコンソールでキーを無効にする）は、呼び出しが認証エラーで失敗するまで見えない。それを
  見に行くものは無い。

### D5. 支出は経路だけでなく認証情報に帰属させる

- **D5a. 経路と課金だけ**（今日）。2 本の Anthropic のキーや、codex のキーと共有の OpenAI キーは 1 行になる。
- **D5b. 台帳の各行に接続の参照を付ける**（*推奨*）。経路が既に計算しているシークレットでない束ね
  （`ANTHROPIC_API_KEY@<指紋>`、`<プロファイル>@<リージョン>#<指紋>`、`<出どころ id>#<指紋>`）。CLI 固定のログインや
  キーにも、同じ種類の**安定した、シークレットでない束ね**を付ける。ログインファイルがアカウント id を持つならその
  ハッシュ（ADR 0136 決定 6 が pi の ChatGPT ログインに求めるのと同じ）、API キーのログイン（codex の行 5、muse の API
  キー）ならキーの指紋、それ以外はログインがファイルを置き換えるたびに Agent が上げる世代番号。トークンの更新では束ねは
  変わらず、別のアカウントへのログインや新しいキーで変わる。どのログインファイルが安定したアカウント id を持つかは、
  claude・codex・muse・agy・cursor・kiro について**未実測**。束ねは呼び出しが動いたときに捕まえる（ADR 0128 決定 8）
  ので、失効や差し替えの後で遅れて書かれた台帳の記録も、動いたときの束ねを保つ。集計キーに `Route` と `Billing` と並んで入り、ADR 0128 決定 8 がそれらについて書いた移行を伴う
  （生の行が期限切れになった後は、それを持たないキーは二度と分けられない）。モデルの認証情報のページ（D16）は、今月の
  推定支出を認証情報ごとに、ハーネス別に分けて出す。
- **D5c. 認証情報ごとの支出カウンタ**を台帳と別に持つ。退ける：真実の源が 2 つになる。
- 提供経路は CP 自身の呼び出しごとの表（`provided_route_call`、ADR 0129 D4/D5）を保つ。その予算は CP のもの。

### D6. 「このログインはこのハーネスだけが使える」を画面でどう言うか

認証情報のカードにはすべて、カタログから計算した**使えるもの**の行を付ける。その認証情報の経路を宣言し、この認証情報を
受け取れるハーネスである。CLI 固定のログインは 1 つのハーネスと、その理由を言葉で出す（「このログインは Codex だけが
使えます。Codex の CLI が保持しています」）。その操作はそのカードに置く。1 つのアカウントに 2 度ログインしている場合
（codex と pi の ChatGPT ログイン）は、OpenAI の下に 2 つの項目として並べ、それぞれにハーネスを付ける。ページはそれらが
1 つだとは言わない。

## 4. 配備とテナントによる制限についての決定

### D7. 層と、その組み合わせ方

| 層 | 誰 | 範囲 | できること |
|---|---|---|---|
| 1 | operator | 配備 | **上限**を決める：許す区分と宛先（D9） |
| 2 | super_admin | 配備と、各テナント | 層 1 を配備全体とテナントごとに絞る。提供経路を付与する（ADR 0129 D3） |
| 3 | tenant_admin | 自分のテナント | 層 2 をテナントについて絞る。提供経路の予算とメンバーへの付与（ADR 0129 D3） |
| 4 | member | 自分 | ハーネスを切る、モデルを隠す、「使う先」を選ぶ（D3） |

- **D7a. 共通部分。拒否が勝つ**（*推奨*）。実効の集合は、すべての層の許す集合の共通部分。層は取り除くことしかできず、
  上の層が外したものを下の層が戻すことはできない。提供経路には ADR 0129 の付与とゼロより大きい予算も要るので、それらの
  経路にとってポリシーは外側のフィルタ、付与は内側のフィルタになる。
- **D7b. 上書き**（各層が上の層を置き換える）。退ける：メンバーがテナントの規則を広げられてしまう。
- **D7c. 各層に許可と拒否の一覧を持たせ、優先順位の規則を付ける。** 今は退ける：同じ構成は D7a で書け、優先順位の規則は
  監査する人が結果を読めなくなるところである。

層 3 はこの記録ではテナント全体に効く。メンバーごとやロールごとに絞ることは未決の問い 2。メンバーごとの制御は、
提供経路についての ADR 0129 のメンバーへの付与だけのままとする。

### D8. 既定値と、ADR 0084・ADR 0129 との関係

- **どの層でも、制限が無いことが既定**：すべての区分と宛先を許す。これは今日の振る舞いと同じ。ADR 0084 決定 7 の、
  **既にある能力を取り上げる**スイッチについての規則である。無いことは許すことを意味し、版を上げても何も変わらない。
- 提供経路は ADR 0129 D3 の逆の既定を保つ。それらは**新しい能力**だからで、super_admin が付与し tenant_admin が予算を
  与えるまでオフ。2 つの規則はぶつからない。ポリシーは何が許されるかを言い、付与は何が差し出されるかを言う。
- **ポリシーは `tenantLimits` に保存しない。** そのハンドラは塊ごと書き換え、監査の行を書かない（ADR 0084 決定 7、
  `control-plane/internal/tenantsrv/tenants.go`）。ポリシーは自分の行とエンドポイントを持ち、書き込みのたびに
  監査する。ADR 0129 の付与と予算の書き込みと同じ。**管理される 3 つの値は分けて持つ**：配備全体の規則（super_admin）、
  各テナントの上限（super_admin）、各テナント自身の規則（tenant_admin）は別々の行で、ロールで分けた別々のエンドポイントを
  持ち、§2 の承認済みの自前ホストのエンドポイントも層ごとに分ける。tenant_admin がテナントの規則を書いても消しても、
  そのテナントについての super_admin の上限には決して触れず、逆も同じ。実効のポリシーは読むときにそれらすべてから
  計算する。この分離が、D7 の共通部分を保存の上で成り立たせる。
- エンジンのテナントの関門（`allow_engine_llm`、`limits.go:135`）は今の場所に残す。配備のエンジンについては
  `self-hosted` 区分の一部として読み、両方が許すときだけ許す。

### D9. 配備はどこで設定するか

- **D9a. Control Plane の環境変数。** 各配備先の配備設定に出す（ECS では `ProvidedRoutes` の隣の CFN パラメータ、
  `deploy/aws/ecs/cfn/30-ingress.yaml:310`、`deploy/aws/ecs/cfn/PARAMETERS.md` に記す）。スタックから読め、Console
  からは変えられず、データベースを書き換えられても保たれる。変えるには配備し直す。
- **D9b. データベースの super_admin の設定。** 柔軟で監査されるが、super_admin が広げられ、監査する人は配備の規則を
  知るのにデータベースを読まねばならない。
- **D9c. 両方。変数が上限で、super_admin の設定がその内側で絞る**（*推奨*）。super_admin の画面は上限を読み取り専用で、
  出どころ（「配備で設定」）付きで出す。解釈できない値や知らない値は「すべて許す」と読まず、**CP の起動を拒む**。
  Docker とネイティブの配備先は、同じ変数を環境ファイルに書く。変数の形は D1 の区分と宛先の集合（例：
  `classes=provided`、`destinations=aws-bedrock`）。正確な綴りは段階 M1 で決める。

### D10. 経路がすべて禁じられたハーネス

ハーネスは名前ではなく、その認証情報が実際に解決する経路で判定する。agy・copilot・cursor・kiro は経路が 1 つずつで
（§1.1）、どれも `subscription`。muse と codex は 2 つずつ持つ。muse はアカウントのログイン（`muse-login`、
`subscription`）か Meta の API キー（`meta-api`、`member-key`）で、`mechanism` で区別する（`muse/auth.go:96-130`）。
codex は ChatGPT ログイン（`chatgpt-subscription`）か API キーのログイン（`openai-api`）で、`auth.json` の `auth_mode`
で区別する（`codex/auth.go:19-27`）。したがって「サブスクリプション禁止」のもとでも、API キーでサインインした muse と
API キーでサインインした codex は使えるままで、API キーのログインを受け付ける codex の画像プロバイダ
（`imagegen/codex.go:150-153`）も同じ。サブスクリプションだけの Workspace の claude は何も残らない。

- **D10a. 隠す。** すっきりするが、昨日使っていたメンバーはなぜ消えたのか分からず、管理者はメンバーの画面から効果を
  確かめられない。
- **D10b. 理由と、決めた層を添えて無効の状態で出す**（*層 2 と 3 について推奨*）：「テナントが許可していません：
  サブスクリプションのログイン」。起動ダイアログ、レポジトリの起動メニュー、スケジュールの編集、エージェントの
  ページで。そのカードからは禁じられた経路のログインの操作を外す（許された API キーの道筋は残す）。インストールは
  そのハーネスのどれかの経路が許されている間だけ残す。インストールしたのに起動できない CLI は、D13 が防げない手での
  ログインを誘うからである。
- **D10c. インストールはできるが起動できない状態で残す。** 同じ理由で退ける。
- **operator の上限（層 1）がそのすべての経路を外したものは隠す。** 配備の中の誰にも変えられないので、無効で出しても
  雑音になる。管理者のポリシーのページには並べる。
- claude は、経路のどれかが許され、その認証情報を持っている限り起動できる。そのためには Console の「使えるか」を
  サブスクリプションのログインではなく経路で判断するよう改める必要がある（`registry.ts:266`）。段階 M2 で行う。
- MCP：セッションから見える種類の一覧（`create_session` の列挙）は、許された経路の無い種類を省く。それを名指しした
  作成は `route_forbidden` と決めた層で拒む。

### D11. 制限のもとでの空の選択

ADR 0128 決定 2 は「空は今日の規則のまま」を保ち、その規則は起動後に証明できない（§1.4）。それに代わるものは、
メンバーが選んでいない従量課金へメンバーを移してはならない。

- **D11a. 空の選択はそのまま。** アダプタの無い種類と、経路無しで起動した claude のセッションがすべてポリシーをすり抜ける。
- **D11b. 何か制限があるときは、すべての作成を明示の経路に解決して保存する。誰が払うかは変えない**（*推奨*）。
  順に：そのハーネスのメンバーの**既定の経路**（メンバーが選んだ設定で、D16、経路を名指したものと数える）が許され、
  その認証情報を持っていればそれ。無ければ、認証情報がちょうど 1 つの経路に解決する種類（agy・copilot・cursor・kiro、
  今のログインの方式での muse と codex、接続での lcpp）ならその経路が許されていればそれ。それも無ければ、許された経路を
  並べた拒否（`route_required`）。最初の許された経路をメンバーに代わって選ぶことは**決してしない**。「サブスクリプション
  禁止」のもとで空の claude の作成は拒まれ、`anthropic-api` へ移されはしない。どの層にも制限が無ければ、今日の振る舞いは
  変わらない。
- **D11c. 空の選択をどこでも拒む。** 退ける：何も制限しない配備でも、既存の起動がすべて変わってしまう。
- **pi は ADR 0136 決定 6 を保つ**：空の選択は、メンバーがサインインしていて**その経路が許されているとき** に
  `chatgpt-subscription` に解決し、そうでなければ拒む（`route_required`）。従量課金の経路は名指されたとき（直接、
  またはメンバーの既定の経路として）だけ使う。この記録が足すのはポリシーの条件だけで、pi の規則の他の部分は変えない。

**既にあるセッションは、作成の規則をもう一度走らせるのではなく、起動したときの証拠で判定する。** 今日の認証情報や既定値から
解決し直すと、誰が払うかが変わりうる。経路無しで ChatGPT で動いた codex のセッションは、後でログインが API キーに
置き換えられると `openai-api` に解決してしまう。lcpp のセッションは接続が変わるとメンバーのエンドポイントと配備の
エンジンの間を移る。既定の経路を変えると古いセッションが移る。そこで：

- M0 以降、すべての起動は解決した経路とその束ね（D5）を、選択が空でもセッションに記録する。再開のときにポリシーが
  判定するのは今の認証情報ではなくその記録で、今の認証情報がそれと合わない再開は差し替え（D4）として拒み、決して
  移さない。
- M0 より前に起動したセッションにはその記録が無い。制限のもとではその再開を理由付きで拒み、メンバーは経路を明示して
  選ぶか（経路の切替、ADR 0128 決定 10 と同じ手順と同意を持つ、記録される移行）、セッションを作り直す。制限が無ければ
  今日のとおり再開する。
- **動いている**セッションも巡回で同じ規則に従う。記録した経路と束ねで判定し、記録が無ければ、証明できないので制限の
  もとでは止める。

### D12. ポリシーを強制する場所

CP は要求が見える場所での関門で、Agent はすべてについての関門である。CP を通らない作成があるからだ。Agent Fleet 自身が
モデルの認証情報で動かすもの（セッション、アシスタントチャットと単発呼び出し、画像生成、共有のデーモン）はすべてこの
規則の内側にある。D13 の例外が覆うのは、メンバーが手で動かすものだけ。

**届け方と鮮度。**

- CP はポリシーに、メンバーごとに増える**版**を付ける。Agent は今持っているものより大きい版のポリシーだけを受け入れ、
  持っている版をディスクに残す。再起動、順序の入れ替わった応答、古いキャッシュが、弱い規則を戻すことは決してない。
- Workspace は起動時に（環境で）実効のポリシーを受け取り、その後 Agent が新しい内部エンドポイントから、
  `/internal/route/check`（ADR 0129、Q1 part 2）と同じ形で、**遅くとも 30 秒ごとに**取り直す。CP は応答の中で、この
  メンバーについてどこかの層が何かを制限しているかも伝え、配備の上限は別に送る。
- **最大の制限の遅れ**は、CP に届く限り、新しい仕事については取り直しの間隔 1 回分（30 秒）、動いている仕事については
  取り直しの間隔 1 回分と reconciler の巡回 1 回分（巡回の間隔はここでは実測していない）。共有のデーモンでは、通常の
  再起動の 60 秒の排出ではなく、中止（下）の後に古いデーモンを止める時間を足す。ポリシーのページはこの数字を示す。
- **取り直しに失敗したとき**：最後に持っているポリシーが何かを制限しているか、配備に上限があり、取り直しが 15 分成功して
  いなければ（経路ゲートウェイが設定の取り込みについて持つ規則、ADR 0128 の 2026-11-05 の P2a の注記）、取り直しが成功
  するまで新しい仕事を「ポリシーを確かめられません」として拒む。動いているセッションはそのままにする。最後に持っている
  ポリシーが何も制限せず上限も無ければ、CP に届かなくても何も止めない。その間になされた締め付けは、最初に成功した取り
  直しで効く。その窓は障害の長さで、ポリシーのページはそう言う。
- **版を上げるとき**：ポリシーの能力を報告しない Agent は、制限のあるメンバーについて信頼しない。CP は制限のあるメンバーの
  その Agent への作成を起こす前に拒む。そうした Agent でのセッションからの作成の道筋は、イメージが上がるまで障害と同じ
  強制の効かない窓であり、リリースノートにそう書く。

**場所。**

| 場所 | 今日 | 提案 |
|---|---|---|
| CP の作成：Console と REST | 起こす前の静的な経路の検査と提供経路の拒否（`control-plane/workspace_handlers.go:1171-1180`） | 加えて、起こす前にポリシー |
| CP の MCP `create_session`（ホスト側） | 経路の引数の静的な検査（`control-plane/internal/mcpsrv/mcp_kinds.go:92`）。提供経路の拒否（`mcpsrv/mcp.go:763-767`、`control-plane/mcp_wiring.go:138`） | 加えてポリシー |
| スケジュール：新規、再利用、交代 | 保存時：種類（`checkScheduleKind`、`control-plane/schedule.go:198`）と静的な経路（`scheduleRouteError`、`:212`）。提供経路の付与と予算は**発火時だけ**（`scheduler_wake.go:90-93`） | 保存時にポリシー（禁じられた経路は保存しない）、発火時にも（保存後の締め付けは発火を拒み、通知を出す） |
| Agent の作成、すべての道筋 | `createRun.checkRoute`（`internal/sessionx/session_route.go:102`）。セッションの `create_session` はここにしか来ない（`internal/mcpx/mcp_client.go:270`） | 加えてポリシー。Workspace の中での**決め手の検査** |
| 保存済みセッションの起動と再開。ターミナルとマネージド、実行方式の切替 | `startRefusalOf`（`session_route.go:130`） | 加えてポリシー（空の選択は D11） |
| 既に動いているセッション | 設定ファイルについての `SweepRouteGuards`（`session_route.go:147`）。経路の無いセッションはすべて飛ばす（`:150`） | 巡回は保存された経路の有無にかかわらず、動いている**すべての**セッションを見る。経路の無いセッションは起動したときの記録（D11）で判定し、それが禁じられているか、制限のもとで記録が無ければ止める。止めたものは再開でき、決めた層を名指す `route-stopped` の通知を出す |
| 動いているマネージドの実行環境への入力 | 送信の関門 | 届ける各メッセージ（メンバー、ピア、スケジュール、キュー）でポリシーを検査し直す。禁じられた経路なら保留してメンバーに理由を伝え、止められようとしている実行環境に届けることはしない |
| 経路の切替（ADR 0128 決定 10、ADR 0129 D6） | `checkRouteSwitch`（`internal/sessionx/session_route_switch.go:142`）。行き先は `RouteSwitcher.SwitchTarget`（`claude/route.go:591`） | 行き先をポリシーで絞る。許された行き先が無ければ申し出を出さない。別の経路への**フォールバックは決してしない** |
| フォーク、移動、作り直し | 受け継いだ選択を検査し直す（ADR 0128 決定 13） | 検査し直しにポリシーを含める |
| 認証情報の登録 | どのログインもキーも足せる | 経路が禁じられたログインの開始やキーの保存を Agent が拒む。ポリシーが締まったときに動いているログインの手順は取り消し（Agent が起こしたログインのプロセスを止める）、それでも手順が書いた認証情報は使わずに保つ（D14）。モデル以外の用途を持つ接続（GitHub の接続：git **と** Copilot）は、その他の用途については決して拒まない。判定するのはそのモデルの経路だけ |
| 提供経路 | 呼び出しごとの CP のゲートウェイ：付与、予算、契約（ADR 0129） | 加えて、呼び出しごとにポリシー |
| 配備のエンジン | ADR 0084 の 4 つの場所 | 加えて `self-hosted` 区分 |

**Agent Fleet 自身のユーティリティ：実行のときに解決し、束ね、隔離する。** メニューを絞ったり環境からキーを外したり
しても、経路は保てない。D14 は禁じられたログインを意図してディスクに残す。claude のチャットは通常の `CLAUDE_CONFIG_DIR`
で動く（`chatx/chat_providers.go:2732-2742`）。codex のチャットは `~/.codex/auth.json` を共有する
（`chat_providers.go:663-677`）。opencode は自分のアカウントのログインを持ち、`small_model` と既定のプロバイダを自分で
選ぶ（`opencode/route.go:18-41` は、opencode の Bedrock 経路がメニューを絞るのでなくプロバイダを固定せねばならなかった
理由を記している）。そこで、ユーティリティの呼び出しはすべて起動として扱う。

- 呼び出しが実際に使う認証情報（ログインの方式や `auth_mode`、モデルと、小さなモデルやタイトル用モデルのプロバイダ）
  から経路を**解決**し、禁じられていれば呼び出しを拒む。
- それに**束ねる**：呼び出しはその認証情報だけで動く。セッションの起動と同じ（ADR 0128 の固定：他の認証情報の変数を
  外し、エンドポイントを固定する。opencode では `enabled_providers` に許されたプロバイダだけを並べた inline の設定を
  使い、`enabled_providers` を設定するファイルは、opencode の経路が既にしているように拒む）。
- 共有のデーモンも同じように**隔離**する：共有の opencode `serve` と codex app-server は、ポリシーと「使う先」が許す
  認証情報とプロバイダだけで動き、どちらかが狭まれば置き換える。
- **通常の再起動ではなく、保安のための再起動。** 通常の再起動は止める前に排出し、最大 60 秒待ってから、まだ動いている
  ターンをすべて中止する（`opencode/serve.go:569-601`、`drain` は `:641-676`）。Agent が起動していない引き継いだ
  デーモンはそもそも置き換えず（`cmd` が nil なら `Restart` は false を返す）、古い環境を保つ。どれも失効ではない。
  共有のデーモンから認証情報、プロバイダ、「使う先」の項目を取り下げるときは：そのデーモンへの新しいターンの受け付けを
  すぐに閉じる。取り下げた認証情報を使っているセッションのターンは、排出せずにすぐに中止する。デーモンを止め、狭めた
  認証情報で起動し、認証情報の世代を付けたものに置き換える。古い世代や分からない世代のデーモン（引き継いだものを含む）は、
  制限のもとでは引き継がず使い回さず、止める。止められないか置き換えに失敗したら、成功するまでそのハーネスを理由付きで
  使えなくする。1 つの `serve` は 1 組のキーを持つ（ADR 0128 決定 5）ので、**そのデーモンの他のセッションも中断される**。
  そのターンは認証情報の変更による中断として終わり、自分の経路のままで、新しいデーモンで続く。認証情報ごとのデーモンなら
  それらは影響を受けないが、それは ADR 0128 決定 5 が既に挙げている別案で、メモリの費用が未実測なのでここでは採らない。
- モデルやプロバイダが変わるたび、フォールバックのたびに**検査し直す**：チャットの送信時のフォールバック
  （`chat_providers.go:57-60`）と画像のプロバイダの順序（`imagegen/imagegen.go:527-528`）は、解決した経路が許された
  バックエンドにだけ移る。何も残らなければメンバーに見えるエラーにする。
- **締め付けのとき**、新しく禁じられた経路で進行中のユーティリティの呼び出しは取り消し、デーモンは再起動する。
- **認証情報の「使う先」からハーネスを外すこと**は、そのハーネスにとっての失効（D4）である。束ねたセッションは止めて
  拒み、デーモンはその認証情報抜きで再起動する。

### D13. 強制できないもの、と代わりに検出するもの

**Workspace の中で手でするログインは防げない。** Agent も、すべての CLI も、すべてのシェルも、メンバー自身のユーザーで
動く（ADR 0129 の背景：Workspace 全体がメンバーとして動く）。メンバーはシェルのセッションを開いて `codex login` を実行
でき、claude のペインに `/login` を打て、API キーを export でき、任意の CLI を落として動かせる。D12 の Agent 側の検査が
縛るのは Agent Fleet 自身の起動の道筋であって、メンバーではない。ポリシーのページはそれを一文で言い、この記録もそれ以外を
主張しない。

できること：

- **AF が知っている場所で検出する。** 接続の状態は既に CLI のログインファイルを読んでいる（`GET api/connections`、
  `console/src/features/settings/parts/useConnections.ts:68`）。Agent は CP に、定期的に、また変わるたびに、§1.1 の
  場所（自分のストア、CLI のログインファイル、`META_API_KEY` のような自分の環境）で見つけた認証情報をそれぞれ
  **経路 id、区分、宛先と状態**として報告する。状態は *保持*（制限の前からあり、D14 で使わずに保っているもの。規則に
  適っている）か *新規*（制限が効いている間に現れたもの。AF 自身の入口は拒むので、手のログインか、強制しない版の AF から
  来た）。シークレットもアカウント id もメールアドレスも送らない。所見として出すのは禁じられた *新規* のものだけ。
- **検出が見ないもの**：別のホームディレクトリに向けた CLI、シェルで export したキー、手で落として動かした CLI、
  シェルからのモデルの利用すべて。検出は兆候であって、目録ではない。
- **記録する。** Agent Fleet のセッションのターンはすべて経路を持つ（D2、D11）。制限のもとで、禁じられた経路や解決できない
  経路に記録されたターンは、同じ報告で印を付ける。経路は AF が起動したときのものであって、メンバーが手で変えた後（ペインの
  中での `/login`）に CLI が使った認証情報の証明ではない。セッションの外での利用は台帳にそもそも入らない。
- **配備単位で、ネットワークを狭める。** 強制モードの送信ゲートウェイ（ADR 0122）は、メンバーの手が届かない唯一の制御
  である。実測した限界：配備全体だけで、テナントの範囲は無い（`egress_member.go:188`）。クライアントの SNI の申告で
  ホストを判断する。Anthropic のホストと `.amazonaws.com` は組み込みの項目（`egress_policy.go:21`、`:103`）。実効の
  ポリシーは組み込みと有効な管理者の許可リストの項目すべての和で（`control-plane/egress.go:141-145`）、組み込みから
  ホストを外すだけでは締め出せない。super_admin が足し戻せる。提案：**operator の上限**から**最後の拒否のフィルタ**を
  作り、その和（組み込み、管理者の許可リスト、静的な初期値）の後と、プロキシに送るものに掛ける。管理者の項目はそれを
  上書きできない。各ベンダーのログインと推論がどのホストを使うかは**未実測**。Claude のサブスクリプションと Anthropic の
  API キーが同じ API ホストに行くかどうかは未実測で、もし同じなら、送信で拒めるのは「Anthropic」であって「Anthropic の
  サブスクリプション」ではない。広い項目は別の理由で開いたままになる（Bedrock と AWS の道具のための
  `.amazonaws.com`、`.api.aws`、git のための `.github.com`）。フィルタが変わったときに既に開いている接続についても未実測。
  **したがって送信は狭めるのであって、** 他のモデルが使われていないことを**証明はしない**。
- **提供経路だけ**は、認証情報そのものがメンバーの手の外にある（CP が持つ）唯一の構成である。配備やテナントの
  アカウントでの支出は制御できる。他のモデルの利用は、上の限界のもとで送信が狭められる分だけ狭まる。

### D14. ポリシーが締まったときに既に保存されている認証情報

- **D14a. 残す。使えない状態にし、印を付ける**（*推奨*）。メンバーにはその認証情報が「<層> が許可していません。残して
  いますが使いません」と見え、自分で消せる。禁じられた経路の新しい認証情報は、Console からも Agent のエンドポイントからも
  足せない（D12、「認証情報の登録」）。ポリシーを緩めれば、ログインし直さずにまた使える。
- **D14b. 隠す。** 退ける：メンバーが自分の持つ認証情報を見ることも消すこともできない。
- **D14c. 消す。** 退ける：認証情報はメンバーの Workspace（暗号化ストア、CLI 自身のファイル）にあり、CP には無い。
  消すことは、戻されるかもしれない規則のために、管理者がメンバーの Workspace に手を入れて取り返しのつかないことをする
  ことになる。

管理者が見るのは D13 の存在の報告から得た件数とメンバー（*保持* と *新規* を分けて）であって、認証情報そのものは見ない。

### D15. 監査：制限が効いていることを管理者がどう示すか

- ポリシーの書き込み（配備、テナント）はすべて、前後の値を持つ監査の行を書く。ADR 0129 の付与と予算の書き込みと同じ。
- 管理者のポリシーのページは、テナントごとに次を出す。実効のポリシーとその版、その各部分がどの層から来たか。それに
  よって残るハーネスと経路。示している最大の制限の遅れ（D12）。禁じられた認証情報があるメンバー。*保持* と *新規* を
  分けて（D13）。印の付いたターン（D13）。配備の上限は出どころ付きで出す。
- 証明は §8 の段階の関門であり、実際の配備で、それぞれ陰性対照付きで行う。「禁止」と表示する画面は証拠にならない。
  すべての入口で起動が拒まれたことが証拠になる。

## 5. 情報の構成

### D16. 1 つだったところを 3 つのページに

**設定 > 接続 > モデルの認証情報**（新規。個人）。宛先で分け、認証情報ごとに 1 枚のカード。状態、**使えるもの**（D6）、
**使う先**（D3）、ポリシーの状態（D10、D14）、今月の支出（D5）を持つ。

| まとまり | カード |
|---|---|
| Anthropic | Claude のサブスクリプションのログイン（Claude Code だけ）。Anthropic の API キー（Claude Code、opencode、pi） |
| OpenAI | Codex 用の ChatGPT ログイン（Codex だけ）。pi 用の ChatGPT ログイン（pi だけ）。Codex の API キーのログイン（Codex だけ）。OpenAI の API キー（opencode、pi） |
| opencode.ai | アカウントのログイン（opencode だけ）。Zen/Go のキーとワークスペース id。課金の選択 |
| Google | Antigravity のログイン（Antigravity だけ。画像生成にも使う）。Gemini のキー（opencode） |
| Meta | Muse のログインか API キー（Muse だけ） |
| GitHub、Cursor、Kiro | Copilot（GitHub の接続を通す。Git へリンク）。Cursor のログイン。Kiro のログイン |
| その他のプロバイダ | OpenRouter、Sakana、任意のキー（opencode） |
| Amazon Bedrock | 印付きの AWS プロファイル。読み取り専用で、印を付ける場所として残る AWS タブへリンク。提供経路。読み取り専用で、今月の予算と支出付き（ADR 0129 Q2） |
| 自前ホスト | 自分の llama.cpp。配備のエンジン（読み取り専用の状態） |

**設定 > 接続 > エージェント**（ハーネスだけ）。ハーネスごとに：インストールと更新。有効化（`CreatePolicy` のスイッチを
すべての種類に広げる）。**既定の経路**（新規。`agentLaunchDefaults` には経路が無い、`console/src/lib/settings/prefs.ts:190`）。
起動の既定値と隠すモデル。再開の振る舞い。通知。リモートコントロール。RTK。思考とストリーミング。「動ける経路」の 1 行が、
今使える経路を並べ、モデルの認証情報へリンクする。

**設定 > セッション**（新規。個人のまとまり）。今エージェントのタブにあるセッションの塊（`AgentsTab.tsx:171-250`）：
停止したセッションのアーカイブまでの期間、既定の支出上限、ピアへのメッセージ、子セッションとその上限、PR の反応、
セッションの検索。

**設定 > 画像生成**（新規。個人のまとまり）。今エージェントのタブにあるスイッチとプロバイダの順序（`AgentsTab.tsx:253-278`）。
プロバイダの各行に、その認証情報とポリシーの状態を付ける。これは認証情報を使う機能であって、認証情報でもハーネスでもない。

**テナントの設定 > モデルの利用**（新規。管理のまとまり）。テナントのポリシー（区分、宛先）と、その上に配備が許すもの。
配備の集合の内側での承認済みの自前ホストのエンドポイント（§2）。提供経路（ADR 0129 のテナントの画面、予算、メンバーへの付与）。存在の報告と印の付いたターン（D13、D15）。

**管理 > モデルの利用**（新規。配備のまとまり）。上限（読み取り専用、出どころ付き）。配備全体のポリシー。テナントごとの
上限と提供経路の付与。承認済みの自前ホストのエンドポイント。配備の出どころ（ADR 0129）。

**起動ダイアログ**は、ハーネス、経路、モデルの順に選ぶ。

1. 許された経路の無いハーネスは、理由を添えて無効にする（D10）。上限が外したものは隠す。
2. 経路の一覧は、ハーネスの宣言した経路 ∩ 実効のポリシー ∩ 持っている認証情報。理由があって外れた経路は、その理由を
   添えて無効で出す（「キーがありません：モデルの認証情報で足してください」「テナントが許可していません」）。最初に選ばれる
   経路は、そのハーネスのメンバーの既定の経路、無ければ最初の許された経路。実行方式の制限は経路ごとに出す（claude の経路は
   ターミナルだけ、ADR 0128 決定 5）。
3. モデルの一覧は経路のもの（既存の `routeModelFilter`、`console/src/features/repos/LaunchModal.tsx:836-850`）。

## 6. 構成を通して見る

#4159 は opencode の Zen をサブスクリプションと一緒に挙げているが、Zen は従量課金のキーである（D1）。そこで 2 つの
プリセットに名前を付ける。

- **サブスクリプション禁止**（#4159 が求めるプリセット）：区分 − {`subscription`} **かつ** 宛先 − {opencode.ai}。
  プランのログインと一緒に、Zen、Go、opencode.ai のアカウント、opencode の無料モデルを外す。どちらの層でも同じ 2 つを
  外す。
- **プランのログイン禁止**：区分 − {`subscription`} だけ。Zen は他と同じ従量課金のキーとして残る。

| 構成 | 設定 | メンバーに見えるもの | 強制するもの | 残るもの |
|---|---|---|---|---|
| サブスクリプション禁止（配備） | 上限：「サブスクリプション禁止」のプリセット | agy・copilot・cursor・kiro は隠れる。claude と codex は API か Bedrock の経路で出る。muse は API キーのログインで出る。opencode は自分のプロバイダのキーだけで、Zen・Go・アカウント無し。画像生成は API キーのログインの codex とエンジンで。チャットは解決した経路が許されたバックエンドで | 作成・起動・巡回・入力・切替・チャット・画像生成・共有のデーモンでの Agent。起こす前の CP | シェルでの手のログイン（D13）。*新規* として報告するが、防がない |
| サブスクリプション禁止（1 テナント） | テナントのポリシー：同じプリセット | 上と同じ。ただし隠すのではなく「テナントが許可していません」を添えて無効 | 同じ | 同じ |
| プランのログイン禁止 | どちらかの層：区分 − {`subscription`} | 上と同じで、Zen が使える | 同じ | 同じ |
| Bedrock だけ | 宛先 = {Amazon Bedrock} | `bedrock` / `bedrock-deployment` の claude と opencode（ADR 0129 Q7 が入れば、OpenAI 系の Bedrock のモデルで codex も）。ベンダーのキーとログインはすべて使えない。メンバーの llama.cpp は、エンドポイントが承認され宛先の一覧に入っていない限り使えない | 同じ。上限では、送信の最後の拒否のフィルタも（D13）。狭めるが証明はしない | `member-cloud` も外さない限り、メンバー自身の AWS アカウントは許される |
| 提供経路だけ | 区分 = {`provided`} | CP が持つ経路だけ。ADR 0129 の付与と予算の範囲で | 同じ。呼び出しごとの CP のゲートウェイ | 他のモデルの利用は、送信が狭められる分だけ狭まる（D13） |
| 混在 | 上限はすべて。テナントが絞る | 各テナントは自分の規則を見る | 同じ | — |

## 7. 既存の記録に対して変わること

- ADR 0128：未決の問い 1 と 2 にはここで答える（§2、D2、D7）。決定 2（「空は今日の規則のまま」）は、制限が掛からない
  ところでだけ成り立つ（D11）。
- ADR 0129 D3 の最後の項目（「メンバー自身の認証情報の経路はテナントの制御の外に置く」）を D7 が改める。
- ADR 0136：pi の経路は `subscription`（`chatgpt-subscription`）と `member-key`（`openai-api`、`anthropic-api`）に
  区分され、他と同じようにポリシーに入る。

## 8. 段階

各段階は、実際の配備で確かめることを陰性対照とともに並べる。単体テストだけでは段階は終わらず、どの段階も後の段階に
依らない。ページが、そのデータを作る段階より前に出すものは、仮置きせずにそのページから外しておく。

| 段階 | 内容 | 実機で確かめること | 陰性対照 |
|---|---|---|---|
| M0 | すべての認証情報の道筋に、ログインの方式ごとに解決する経路 id（D2）。カタログに区分と宛先、接続ごとの経路。台帳の行に安定した接続の束ね（D5）と集計の移行。すべてのセッションに起動したときの経路と束ねを記録（D11）。種類のカタログが、今使える経路を報告する。振る舞いは変えない | 開発用の Workspace で種類ごとに 1 ターン、期待どおりの経路と束ねが書かれ、セッションがそれを記録する。muse と codex は両方のログインの方式で。チャットの各バックエンドと画像の各プロバイダの 1 回も同じ。集計が 2 本の Anthropic のキーを分けたまま保つ | 別のアカウントへのログイン（や新しいキー）で束ねが変わり、トークンの更新では変わらない。以前起動できたものが拒まれない |
| M1 | ポリシー：CP のストア、REST、監査の行、版。operator の変数と CFN パラメータ（D9）。Agent の取り直しと鮮度の規則。D12 の「使う先」 **以外の** すべての場所。ユーティリティの解決・束ね・隔離と共有のデーモンを含む。認証情報の登録と進行中のログイン（D12、D14）。空の選択の規則（D11） | 開発用の配備で、テナント A を「サブスクリプション禁止」に：Claude のサブスクリプションの起動が、Console・REST・ホスト側の MCP ツール・セッションの `create_session`・スケジュール（保存時と、後の締め付けの後の発火時）のそれぞれで、層を名指して拒まれる。`anthropic-api` の claude は起動する。空の claude の作成は拒まれ、API へ移されない。API キーのログインの muse と codex は起動し、アカウントのログインでは拒まれる。アシスタントチャットは許されたバックエンドで答えるか、残っていないと言う。画像生成は ChatGPT の方式の codex と agy を飛ばす。動いていたサブスクリプションのセッションは、明示の経路のものも経路の無いものも、示した遅れのうちに止まり、再開は拒まれる。動いているマネージドの実行環境へのキューのメッセージは保留される。切替の申し出に禁じられた行き先が出ない。**保持した認証の隔離**：許された Anthropic の API キーと保持した claude のサブスクリプションのログインの両方、そして opencode.ai を禁じたうえで許された OpenRouter のキーと保持した opencode.ai のアカウントのログインを持つ Workspace で、チャット、タイトルと小さなモデルの呼び出し、バックエンドのフォールバックを強いて、台帳の行が毎回許された経路と束ねを示す。API キーの方式の codex で画像生成が成功する。A を締めたときに進行中のチャットの呼び出しと画像の呼び出しが、示した遅れのうちに取り消される。ログインが API キーに置き換えられた M0 以前の経路無しの codex のセッションは、再開を拒まれ、`openai-api` で再開されない | 制限の無いテナント B では同じセッションが起動する。CP に 15 分を超えて届かないと、A の新しい仕事は拒まれ、制限の無いメンバーのものは拒まれない。古い版のポリシーを再送しても無視される。A を緩め直すと、再起動せずに示した遅れのうちに戻る。上限を `classes=provided` にしたとき、super_admin が REST でそれを広げようとすると拒まれる。「サブスクリプション禁止」のもとでも GitHub の git のログインは使える。許されたキーを消すと、チャットは保持したログインを使わずに拒む。tenant_admin がテナントの規則を消しても super_admin の上限は効いたまま。上限が除いたホストやベンダーのホストを、tenant_admin が自前ホストとして承認しようとすると拒まれる |
| M2 | Console：モデルの認証情報、エージェント（既定の経路付き）、セッション、画像生成。テナントと管理のモデルの利用のページ（ポリシー、上限、提供経路。ADR 0129 の Q2 の画面はここに入れてよい）。起動ダイアログのハーネス → 経路 → モデルと理由の表示。claude の「使えるか」を経路で判断 | 開発用の配備で §6 の各構成について headless Chromium で撮り、スクリーンショットを PR に残す。Anthropic のキーしか持たないメンバーがダイアログから claude を起動できる | 無効で出した禁じられた経路は、要求を書き換えても通らない（Agent が拒む、M1）。制限の無いメンバーには理由の表示が出ない |
| M3 | 共有：認証情報ごとの「使う先」（D3）とその強制。Anthropic のキーを Anthropic の下に出す（まずストアの枠は変えず、その後にプロバイダをキーにしたストアへ移行付きで）。削除と差し替えのダイアログに「使っているもの」。D4 の失効 | claude と opencode のセッションを 1 つずつ動かしたまま Anthropic のキーを消す：claude は理由付きで止まり、再開は拒まれる。opencode の serve は保安のための再起動で置き換えられ、キーを使っていたセッションはすぐに中止されて拒まれ、消したキーは示した遅れのうちに使えなくなる。キーを差し替える：束ねたセッションは拒まれ、新しいものは新しい指紋に束ねられる。キーの「使う先」から opencode を外す：opencode はそれを失い、claude は保つ | 同じ serve での長い OpenRouter のターンは（黙って失われるのではなく）見える形で中断され、自分の経路のままで新しい serve で続く。他のキーの claude のセッションは影響されない。引き継いだ serve は使い回されず止められる。置き換えの失敗を起こすと、opencode は古いデーモンでなく、理由付きで使えない状態になる。何も別の経路へ移らない。認証情報ごとの支出が台帳と一致する |
| M4 | 検出とネットワーク：*保持* と *新規* の状態を持つ存在の報告（D13）、印の付いたターン、管理とテナントのページのその節。上限からの送信の最後の拒否のフィルタ | 「サブスクリプション禁止」のもとでシェルから手で `codex login` すると、報告の間隔 1 回のうちに *新規* として出る。送信を強制し、上限を Bedrock だけにしたサンドボックスで、手でのベンダーのログインがネットワークで失敗し、Bedrock の経路のターンは答える（実際の AWS が要る。CI ではなく受け入れの実行） | 制限の前からある認証情報は所見ではなく *保持* と出る。除いたベンダーのホストを足し戻す super_admin の許可リストの項目は効かない。フィルタの前に開いた接続がそれを越えて残るかを実測して書き留める |

pi の経路は ADR 0136 の P1 が入った時点で M0 の水準で入る。ここで独自の段階は要らない。

## 9. 退けた案

- **ポリシーの単位としてのハーネス**（D1a）と、**管理者の単位としての経路 id**（D1b）。
- **共通部分でなく上書き**（D7b）：下の層が広げられてしまう。
- **`tenantLimits` に置くポリシー**（D8）：ハンドラが塊を書き換え、何も監査しない。
- **CLI のログインファイルの共有**（D3c）。
- **ポリシーが締まったときに保存済みの認証情報を消す**（D14c）。
- **Console か CP だけでの強制。** セッションの `create_session` と、Agent のベアラーを持つ呼び出し元は、Agent の作成に
  直接届く（§1.4）。
- **手のログインを防げると言うこと。** Workspace の中からはできない（D13）。
- **空の選択を最初の許された経路に解決すること**（D11）：メンバーが選んでいない従量課金へ移してしまう。
- **ユーティリティとデーモンの強制を、メニューの絞り込みやキーを外すことで済ませること**（D12）：禁じられたログインは
  ディスクに残っており、CLI は自分でプロバイダを選ぶ。
- **除いたホストを送信の組み込みから外すだけにすること**（D13）：管理者の許可リストはその後に足される。
- **既にあるセッションを今日の認証情報や既定値から解決し直すこと**（D11）：誰が払うかが変わりうる。
- **通常のデーモンの再起動を失効として使うこと**（D12）：最大 60 秒排出し、引き継いだデーモンを古い環境のまま残す。

## 10. 未決の問い

1. ポリシーに経路ごとの例外を入れるか（「Copilot 以外のサブスクリプションは禁止」）。
2. tenant_admin は、ADR 0129 のメンバーへの付与を超えて、メンバーごとやロールごとにポリシーを絞れるか。
3. 宛先にリージョンが要るか（例：データの所在のため、Bedrock を 1 つのリージョンだけに）。ADR 0129 の出どころは
   リージョンの集合を持ち、メンバーのプロファイルはリージョンを持つ。
4. テナントごとの送信：ADR 0122 には無く、D13 のネットワークの制御は配備全体である。
5. 禁じられた認証情報があると分かったとき（D13）、報告だけにするか、そのメンバーのセッションも止めるか。
6. codex はセッションごとに共有の OpenAI キーを受け取れるか。pi は codex の ChatGPT ログインを使えるか。どちらも
   **未実測**（D3）。

## 結果

- CLI 固定のものも含め、すべての認証情報が経路 id を持ち、セッションのターンはすべてどれかに記録される。支出を認証情報
  ごとに読める。
- 配備とテナントは認証情報の区分と宛先を禁じられ、その下の誰も広げられない。Agent Fleet 自身の起動の道筋、アシスタント
  チャット、画像生成がすべてそれに従い、手のログインは無かったことにせず報告する。
- エージェントのタブはハーネスのページになる。認証情報は宛先で分けたページへ移る。セッション設定と画像生成は自分の
  ページを持つ。
- 制限を設定しない限り、メンバーが今日していることは何も変わらない。変わるのは、Anthropic のキーがメンバーの探す場所に
  出ることだけである。
