# 0136. pi をセッション種別（`pi`）にする — マネージドのみ、セッションごとに RPC の子プロセス 1 つ、すべてのセッションにルート

[English](0136-pi-agent-kind.md) | 日本語

- 状態: **提案**（2026-11-14）。pi を種別として採用することはオーナーが 2026-11-12 に決めた（#1585）。下の決定は、
  オーナーが受け入れるまではこの記録の提案である。オーナーにしか答えられない問いは「未決の問い」に挙げ、
  `needs-decision` の Issue にした。1 つ目にはオーナーが 2026-11-14 に答えた（この PR のレビューで、#4138）。
  まだ何も作っていない。`file:line` はすべて `7ab3e1141` で読んだ。
  **測ったものと測っていないもの。** 実測（pi 1.0.0、2026-10-03、`docs/log/129`）: RPC の操作、`agent_settled` と
  abort の順序、stdio 経由の af MCP、`get_session_stats`、承認の拡張、codex とのトークン比較。pi 1.5.2（現在の
  `latest`、2026-11-14 に取得、実行はしていない）のドキュメントとパッケージのソースから読んだもの: プロバイダの
  変数、Bedrock のベアラーと `baseUrl`、エージェントディレクトリ、`auth.json` のロック、セッションの保存先、MCP の
  ファイル。後者で決定を支えている記述には *(docs)* を付けた。Stage 0b（実リポジトリの課題）は走らせていない。
  もう関門ではなく（オーナー、2026-11-12）、この記録のどこもそれに依存しない。
  2026-11-14 に別セッション（codex `gpt-6.1-sol`）がレビューした。1 巡で指摘 8 件。どれもツリーと pi 1.5.2 の
  ソースに照らして確かめ、本文に取り込んだ。
- Follow-ups: #4140、#4141、#4142、#4143（P0〜P3）。未決の問い #4139（#4138 は回答済み）
- 参照: [0128](0128-harness-route-maker.ja.md)（ハーネス × ルート。pi はその P3）/
  [0129](0129-provided-model-routes.ja.md)（提供される Bedrock ルート）/
  [0095](0095-muse-agent-kind.ja.md)（最も近い先例: ベンダーのプロトコルで動く、マネージドのみ・セッションごとの子
  プロセスの種別）/ [0093](0093-lcpp-agent-kind.ja.md)（マネージドのみ）/ [0019](0019-copilot-agent-kind.ja.md)
  （stdio JSON-RPC のセッションごとの子プロセス）/ [0015](0015-agent-managed-driver.ja.md)（マネージドドライバの
  契約）/ [0124](0124-second-refactor.ja.md) Phase 3（「Phase 3 は pi や rovo を作る前に終える」。終わっている）/
  `docs/log/129-pi-agent-kind.md`（Stage 0）

## 背景

pi（`@earendil-works/pi-coding-agent`、MIT）は小さなコーディングエージェントである。組み込みツールは 4 つ、
システムプロンプトは約 1k トークン、拡張は TypeScript、プロバイダは多数。Stage 0 では同じ ChatGPT のモデル
（`gpt-5.5`、medium、小課題 3 つ × 2 回）で codex と比べた。両方とも 6/6 正解で、pi の総入力トークンは codex の約
1/16、定価換算の費用は約 1/3（pi にワークスペースのガイドを足した対照では約 1/2.9）。これはおもちゃの課題での
実測で、実リポジトリでの品質については何も言えない。

pi を種別にする価値は、値段よりも形にある。`pi --mode rpc` は stdin/stdout の JSONL プロトコルで、マネージド
ドライバに要る操作をすべて持つ。Stage 0（G1〜G4）で実測した: `prompt`、`steer`、`follow_up`、`abort`。低レベルの
実行ごとの `agent_end` と、pi がもう自分からは続けないときの `agent_settled`。トークンと `contextUsage` を返す
`get_session_stats`。そして `extension_ui_request` / `extension_ui_response`。拡張はこれを通じて、ツール呼び出しの
確認をクライアントに求められる。1 プロセスが 1 セッションを受け持つ。

ADR 0128 はすでに pi の行（決定 5: `chatgpt-subscription`、`openai-api`、`bedrock`、`af-engine-llm`）と段階（P3）を
確保している。その後、セッションごとのルートに要る部品が opencode のために作られた。Unix ソケットを話せない
ハーネスのための、ループバックのリスナーと `/proc` による呼び出し元の特定を持つルートゲートウェイ（P2c）と、
ルートを持つマネージドセッションごとの専用ランタイムで、セッションの根として登録されるもの
（`agents.RegisterRuntimeRoots`、`internal/agents/route_gateway.go:95`。#4004、PR #4006。そのライブゲートはまだ
開いている）。pi はそのゲートの後に来る。

この記録が守らなければならない事実:

- **Claude のサブスクリプションは pi のルートではない。** Anthropic は OAuth によるサインインを Claude Code と
  自社のアプリケーションに限っており、サードパーティのハーネスは API キーを使う（#1585、2026-10-03 のコメント）。
  ADR 0128 決定 4 はすでに `anthropic-subscription` を claude 以外のすべてのハーネスから外している。
- **ChatGPT のサブスクリプションは pi での利用が認められている。** OpenAI の投稿と Codex for Open Source のページで、
  個人が自分のアカウントで使う場合について。転売や複数利用者のサービスは含まない（#1585、同じコメント）。これは
  文言であって、契約の条項ではない。
- **pi 1.x では ChatGPT のサインインと OpenAI の API キーが同じプロバイダ（`openai`）にある**（`docs/log/129` §1）。
  一方で動くはずのセッションが、届く資格情報が 1 つだけになっていない限り、黙ってもう一方で動きうる。
- **pi は周囲のクラウド資格情報を読む** *(docs)*: Bedrock は `AWS_BEARER_TOKEN_BEDROCK` のほかに `AWS_PROFILE`、
  静的キー、ECS タスクの資格情報、IRSA を受け付ける。そのどれかを継いだ pi プロセスはゲートウェイを迂回できる。
- **pi のエージェントディレクトリにすべてがある** *(docs)*: `PI_CODING_AGENT_DIR`（既定は `~/.pi/agent`）に
  `settings.json`、`auth.json`、`models.json`、`mcp.json`、拡張、スキル、ユーザーの `AGENTS.md` がある。セッションは
  `--session-dir` の指定がなければ `sessions/` の下に置かれる。
- **`auth.json` はロックされる** *(source)*: pi 1.5.2 の `core/auth-storage.js` は、解決後のパスに
  `proper-lockfile` のロックを取る（「auth.json へのシンボリックリンクの別名が 1 つのロックを共有するように」）。
  したがって、あるプロセスのトークン更新は、同じファイルを通した別プロセスの読み取りと直列になる。
- **既定で外へ送る**（`docs/log/129` §1）: インストールのテレメトリとプロバイダへの帰属ヘッダが有効
  （`enableInstallTelemetry`、`PI_TELEMETRY`）、バージョン確認が `pi.dev` を呼び、`/share` がセッションを公開する。
- **プロジェクトの信頼**（`docs/log/129` §1。pi の `security.md` と `configuration.md` *(docs)*）: RPC モードで既定の
  `defaultProjectTrust: "ask"` のとき、pi は信頼が要るプロジェクトの資源を黙って読まない。信頼は全部か無しかで、
  対象は `.pi/settings.json`（pi のパッケージも宣言できる）、`.pi/mcp.json`、`.pi/extensions`、`.pi/skills`、
  `.pi/prompts`、`.pi/themes`、`.pi/SYSTEM.md`、`.pi/APPEND_SYSTEM.md`、そして作業ディレクトリかその祖先の
  `.agents/skills`。プロジェクトの `models.json` は存在しない。既存のプロバイダの `baseUrl` を変えられるのは
  プロジェクトの**拡張**（`pi.registerProvider`）で、拡張は資格情報・プロンプト・応答を見られる。`AGENTS.md` と
  `CLAUDE.md` は信頼と関係なく読み、プロジェクトの `sessionDir` の設定は信頼を解決する前に読む（コマンドラインの
  `--session-dir` が優先する）。
- **種別のレジストリは準備ができている。** ADR 0124 Phase 3 は閉じた（#2872、#2927）。スタブの種別を足すと、
  そのパッケージの外で 16 ファイルに触れると実測した（ADR 0124 の 2026-10-29 の追補）。
  `TestKindLiteralNewKindCounted`（`kind_literal_ceiling_test.go:234`）はすでに `KindPi` を模している。
  チェックリストは `docs/build/20-add-an-agent.md` §20.7。

## 決定

### 1. 種別は `pi`

`session.KindPi = "pi"`、ラベルは `pi`。起動の接尾辞、短いラベル、色、CSS クラスは §20.7 に従い、種別を登録する
ときに決める（色は ADR 0095 決定 1 と同じく、既存の種別に対して測る）。`pi` はメーカーではない。
`model_provider.go` はメーカーをモデル ID（`openai/…`、`amazon-bedrock/…`）から解決し、種別からは決して解決しない。

### 2. マネージドのみ

案:

- **(a) マネージドのみ**（`Caps().ManagedOnly`）。`lcpp` と `muse` の形。
- **(b) 両方**: マネージドと、pi の TUI を動かす tmux ペイン。
- **(c) ターミナルのみ。**

**採る: (a)。** RPC プロトコルはドライバに要る操作をすべて持つ（Stage 0 G1〜G4）。ペインを足しても、2 つ目の UI と、
誰も測っていない画面の契約（`docs/log/129` §5 は TUI を未測としている）を買うだけである: ペインのモード、モーダル、
プロンプトの遮り、割り込みのキー、進捗のシグナル（§20.7 のターミナルの行）。ペインはルートを保つのも難しくする。
メンバーがそこに `/login` や `/model` を打てば、セッションを別の資格情報へ移せてしまうが、マネージドの子プロセスは
それを決して提供しない。(c) は pi を足す理由になったプロトコルを捨てる。

マネージドのみの関門は共有され、すでに支払い済みである（ADR 0095 決定 2: 作成時の既定が `managed`、`tui` への
切替は拒否、Console は `terminalDriver: false` を導く）。メンバーが TUI を求め、**かつ**ペインがルートをどう保つかを
含めて TUI の契約を測ったときに再検討する。

### 3. セッションごとに `pi --mode rpc` の子プロセスを 1 つ

`Capabilities.ProcessModel = "per-session-child"`。pi は 1 プロセスにつき有効なセッションが 1 つで
（`switch_session` はそれを置き換える）、共有デーモンにしても子プロセスのプールになるだけである。それに、ルートは
プロセスごとに保たれる（決定 6）。

- 子プロセスは #4004 の専用 `opencode serve`（`agents/opencode/route_serve.go`）と同じやり方で起動する: 固定した
  OS スレッドの上で `Pdeathsig` を付けて起動し、ルートのトークンを唯一持っていた Agent のゲートウェイと一緒に
  死ぬようにする。失敗の経路も含めてすべての経路で回収する（§20.3）。`agents.RegisterRuntimeRoots` で登録し、
  ゲートウェイの呼び出し元検査がそれと、それが起動するツールをセッションの木として扱うようにする。
- `Resume` が起動し、`DropHandle` と `Shutdown` が止める（stdin を閉じて SIGTERM を送ると整然とした終了が始まり、
  2 つ目のシグナルで即座に終わる *(docs)*）。死んだものは reconcile が置き換える。ハンドルの足場は共有の
  `agents/managed` の `Lifecycle[H]`。
- 起動フラグ: `--mode rpc`、`--session-dir <共有>` と再開時の `--session <file>`（決定 5）、`--no-approve`
  （決定 7）、`-e <AF の承認拡張>`（決定 9）、ルートの `--model`（決定 6）。秘密はコマンドラインに載せない。
  `--api-key` は `/proc/*/cmdline` に出るので決して使わない。

使用量（セッションごとの Node プロセス）は測っていない。P0 でアイドル時とターン中を測る。問題になるほど大きければ、
そのときにプールを考え直す。今ではない。

### 4. プロトコルの駆動

クライアントは ACP クライアント（`agents/acp`）の形に倣う: 一方に ID で対応付けた応答、もう一方にイベント。
**待っている応答がイベントの読み取りを止めることは決してない。** `abort` は `agent_settled` の後にしか応答しない
（実測、G1）ので、応答を待ってからイベントを読むクライアントは行き詰まる。

| AF の操作 | pi |
|---|---|
| `Send` | `prompt`。`data.disposition: "handled"` は、拡張のコマンドや入力ハンドラがプロンプトを消費したことを表す。これはプロンプトについての説明で、拡張が別に始めた仕事については何も言わない *(docs)*。AF はコマンドを登録する拡張を読み込まない（決定 7）ので、ドライバはそのターンを応答だけでは終えず、`get_state` が pi のアイドルを示してから終える |
| `Steer`（ターン実行中） | `steer`: 次のモデル呼び出しの前に届く（実測） |
| ターン実行中に送られた、steer でないメッセージ | AF のキューに保持し、`agent_settled` の後に新しい `prompt` として送る。pi の `follow_up` は使わない: 追送を同じ実行に畳み込む（`agent_end` が 1 回、実測）ので、AF の 2 つのターンが状態ストアでも台帳でも 1 ターンになってしまう |
| ターンの終わり | **`agent_settled` のみ。** `agent_end` の後に再試行、コンパクション、steer が続きうる *(docs)* |
| `Interrupt` | `abort`。ターンは `agent_settled` で終わり、取り消しとして記録する。その後に来る応答は形式にすぎない。期限内に `agent_settled` が来なければ、子プロセスを殺してセッションファイルから照合し直す |
| `UpdateSettings` | `set_model`（ルートの中で、決定 6）、`set_thinking_level` |
| スナップショット | `get_state` と `get_messages` |
| `Respond` | `extension_ui_response`（決定 9） |

`DynamicModel` と `DynamicEffort` を宣言する。`DynamicMode` は宣言しない（pi のワイヤーにはプランモードがない。
別のものをそれと読むのは、能力表が嘘をつき始めるやり方である。ADR 0095 決定 13）。`Fork` は pi の `fork` /
`clone` で、P3 に回す（決定 13）。

### 5. 会話 ID は捕まえる。トランスクリプトは pi のセッションファイル

セッション ID とファイルは pi が作る。`get_state` が `sessionId` と `sessionFile` を返す *(docs)*。ドライバは
セッション作成時に両方を `session.Meta` に保存し（押し付けるのでなく捕まえる。§20.1 が勧める方なので、回復の経路は
要らない）、`--session <file>` で再開する。セッションは Workspace ごとに AF が決めた 1 つの `--session-dir` の下に
書かせ、決定 6 のセッションごとのエージェントディレクトリの下には置かない。そのディレクトリを作り直しても残るように。

正となる読み取り元は、休止中はセッションの JSONL（形式は pi の `session-format.md` に記述がある）、子プロセスの
動作中は RPC のイベントである。AF は会話を複製しない（ADR 0015 決定 3）。作り直しとフォークはフィールドの許可リスト
から新しい `Meta` を組み立てる（ADR 0095 決定 4）ので、保存したファイルがうっかり引き継がれることはない。

### 6. すべての pi セッションにルートがあり、セッションごとのエージェントディレクトリと環境で保つ

pi は `agents.RouteAdapter`（`internal/agents/route.go:21`）、`RouteBinder`（`:102`）、`RouteChecker`、
`RouteModelOwner`（`:145`）を実装する。

**空の選択は、それ自体のルート規則ではない。** ADR 0128 決定 2 は既存の種別について「空は今日の規則」を保つが、
pi には今日がない。作成時、空の選択はメンバーがサインイン済みなら `chatgpt-subscription` に解決し、そうでなければ
拒否する（`route_required`）。解決した選択を保存するので、以後すべての pi セッションは明示的な選択を持つ。
従量課金のルートは、名指しされたときにしか選ばれない。

**隔離。** セッションごとに、子プロセスの起動前に AF が書くエージェントディレクトリを持たせる:
`PI_CODING_AGENT_DIR=<Agent の状態>/pi/sessions/<session>/agent`。中身は `settings.json`（決定 7）、`mcp.json`
（決定 10）、ルートが必要とする場合のみの `models.json`、指示（決定 11）、そして**保存したログインを使うルートの
場合のみの** `auth.json`。子プロセスの環境は、Agent の環境から、固定したバージョンの pi が読むすべてのプロバイダの
資格情報とクラウドの資格情報の変数（ドキュメントのプロバイダ表、`ANTHROPIC_OAUTH_TOKEN`、`ANTHROPIC_AUTH_TOKEN`、
フェデレーションの変数、`AWS_PROFILE`、静的な AWS キー、`AWS_CONTAINER_CREDENTIALS_*`、
`AWS_WEB_IDENTITY_TOKEN_FILE`、Vertex の変数）を除き、さらに pi のものではない Agent 自身の秘密（資格情報を発行する
トークン `AF_ROUTE_ISSUE_TOKEN` と `AF_ENGINE_ISSUE_TOKEN`、エンジンのキー、Workspace が保存するすべてのプロバイダの
キー。#4004 の専用 serve が除くのと同じ。`opencode/route_serve.go:447` の `routeServeSecretEnv`）を除き、ルート
自身のものとセッションの識別（決定 10）を足したものである。除く一覧の前半は固定したバージョンのドキュメントから
生成し、版上げのたびに契約ワークフローで確かめる。上流で増えたプロバイダは、ルートを迂回する新しい道だからである。
後半は AF のもので、その専用 serve と共有する。`AF_ROUTE_ISSUE_TOKEN` を持つプロセスは、コントロールプレーンに
直接ルートのトークンを求め、ローカルのゲートウェイの呼び出し元の束縛を飛ばせてしまう
（`control-plane/internal/routegateway/gateway.go`）。

**保存する選択は、それを選んだときの資格情報に束縛する**（ADR 0128 決定 3、`RouteBinder`）。資格情報を差し替えても、
保存済みのセッションが別の請求先へ移らないようにするためである。`openai-api` は、claude の `anthropic-api`
（`claude/route.go`）と同じくキーの指紋に束縛する。`chatgpt-subscription` はログインが属する ChatGPT のアカウントに
束縛する（ログインファイルのどこに安定したアカウント ID があるかは P0 の項目）。トークンの更新では束縛は保たれる。
キーの差し替えや別アカウントへのログインは、起動と再開のたびに目に見える拒否（claude の差し替えられたキーと同じ、`keyReplaced`）になり、
動作中のセッションはルートガードの巡回が止める。

| ルート | 資格情報と層（ADR 0128 決定 7） | 子プロセスへの届け方 | 段階 |
|---|---|---|---|
| `chatgpt-subscription` | メンバー自身の pi による ChatGPT へのサインイン（既存の接続）。Workspace ごとに 1 つのログインファイルを AF の接続フロー（決定 12）が書き、ファイルブラウザから隠す | セッションの `auth.json` はログインファイルへのシンボリックリンク。解決後のパスへの pi のロックが更新を直列にする *(source)*。環境に `OPENAI_API_KEY` はない | P1 |
| `openai-api` | メンバーの OpenAI API キー（メンバーキー、A1） | 子プロセスの環境だけに `OPENAI_API_KEY`。`auth.json` はないので、pi 自身の解決ではサブスクリプションのログインに届かない | P1 |
| `bedrock`、`bedrock-tenant`、`bedrock-deployment` | ADR 0128 C2 のプロファイル / ADR 0129 の提供ルート（A2） | ゲートウェイのループバックのリスナー（`IssueLoopback`、`route_gateway.go:31`）。pi は Node のクライアントで、Unix ソケットの転送はドキュメントにない。`AWS_BEARER_TOKEN_BEDROCK` = ルートのトークン、`AWS_REGION`、そして `amazon-bedrock` プロバイダの `baseUrl` をゲートウェイにする `models.json` *(docs: ベアラーと `baseUrl` は対応している。ワイヤーは `bedrock-converse-stream` で、ゲートウェイはすでに転送している)* | P2 |
| `anthropic-api` | メンバーの Anthropic API キー（メンバーキー、A1）。Anthropic の規約はサードパーティのハーネスにこれを認めている。ADR 0128 の pi の行にはなく、ここで候補として足す | `ANTHROPIC_API_KEY` のみ | P3 |
| `af-engine-llm` | デプロイメントのエンジン | エンジンのトークンがセッションに束縛されるまではしない: 今は 30 日生き、Workspace の範囲である（ADR 0128 の背景）。ゲートウェイ経由か、セッションに束縛されたトークンか、ADR 0128 の後続の作業が与える方で | P3 |
| `anthropic-subscription` | — | **決してしない**（背景） | — |
| pi のその他のプロバイダ（GitHub Copilot、OpenCode Zen/Go、OpenRouter など） | — | 提供しない。どれもそれぞれの規約の確認と行が要る | — |

**モデルはルートに属する。** `RouteModelOwner` は `<provider>/<id>` を検査する: 2 つの OpenAI のルートは `openai/…`、
Bedrock は `amazon-bedrock/…`、`anthropic-api` は `anthropic/…`。プロバイダをまたぐ `set_model` は拒否する
（ADR 0128 決定 6）。モデルのメニューはルートのもの: ルートと接続のために作った一時のエージェントディレクトリで
短命の子プロセスを動かして `get_available_models` を取り、ルートと束縛した接続を含むキーでキャッシュし、失敗時は
古いものを使う。今の一覧の契約ではそれを尋ねられない: `agents.ModelQuery`（`internal/agents/modellist.go:14`）には
ルートも接続もなく、モデルのエンドポイントもどちらも渡さない。P1 で両方を足し、Console の起動ダイアログと MCP の
`create_session` がそれを送る。

**隔離が主張しないこと。** すべてのセッションは同じユーザーで動く。`openai-api` のセッションのエージェントが
`PI_CODING_AGENT_DIR` を別のディレクトリにしたり、ログインファイルを読んだりすれば、サブスクリプションに届く。
`pi auth print-bearer-token` はそれをコマンド 1 つで表示する。サブスクリプションのログインの露出は、今日の codex の
`auth.json` と同じである。セッションごとのディレクトリが保証するのは、**pi 自身が**選んだルートで動くことであり、
それが請求と台帳に要ることである。敵対的なエージェントがメンバーの他の資格情報に届かないことではない。ゲートウェイの
呼び出し元の束縛を持つのは A2 のルート（Bedrock）だけである。

### 7. エージェントディレクトリの設定は AF が持ち、外への報告は切る

各セッションのエージェントディレクトリの `settings.json` は AF が書き、次を固定する:

- `enableInstallTelemetry: false` と環境の `PI_TELEMETRY=0`（テレメトリとプロバイダへの帰属ヘッダを切る）、
  `PI_SKIP_VERSION_CHECK=1`。pi が自分で更新することはない: `pi update` はせず、版の固定は AF のもの（決定 14）。
- **リポジトリのテキストは読み込み、コードと設定は読み込まない。** pi のプロジェクトの信頼は決して与えない
  （`defaultProjectTrust: "never"`、`--no-approve`）ので、`.pi/extensions`、`packages`、`.pi/mcp.json`、
  `.pi/settings.json`、`.pi/SYSTEM.md` は読み込まれない。リポジトリのテキストの資源は参照で読ませる: セッション自身の
  `settings.json` が作業コピーの `.pi/skills`、`.agents/skills`、`.pi/prompts` を絶対パスで挙げ（ユーザー単位の設定の
  資源パスは信頼の対象外 *(docs)*）、`.pi/APPEND_SYSTEM.md` があればドライバがシステムプロンプトに足す。
  `AGENTS.md` / `CLAUDE.md` は pi がいつもそうするとおり読まれる。線をそこに引く理由:
  - プロジェクトの拡張やパッケージは pi の起動時に、モデルが何かする前に承認なしで、ルートの資格情報を持つプロセスの
    中で動く。プロバイダの `baseUrl` を向け直せ（ChatGPT のトークンと会話全体が外へ出るのに、AF はセッションの
    ルートを記録し続ける）、ツールの扱いを乗っ取れるので、承認を求めたセッションでも AF の承認拡張が無効になる。
  - リポジトリのファイルは居座る: 注入されたテキストに操られたエージェントが `.pi/extensions/x.ts` を書けば、その
    作業コピーでの以後の起動のたびに動き、コミットされれば他のメンバーのセッションにも届く。
  - テキストの資源の危険は pi がどのみち読む `AGENTS.md` と変わらず、`.agents/skills` は他のエージェントとも共有の
    置き場所なので、それを落とすとメンバーが実際に失うものがある。

  AF 自身の `--session-dir` が、pi が信頼より先に読む唯一のプロジェクト設定である `sessionDir` に優先する。この線で
  よいか、別の線にするかはオーナーが未決の問い 2 で決める。
- `/share` と `/bug` はセッションを公開する。どちらかで始まるプロンプトはドライバが理由を示して拒否する。pi の
  組み込みのスラッシュコマンドが RPC の `prompt` からそもそも届くかは P0 で測る。
- メンバー自身の `~/.pi/agent`（自分で pi を入れていた場合）は、セッションが読むことも AF が書くこともない。

### 8. 使用量と文脈

- **課金されるレコードごとに、セッションファイルから。** 使用量を持つのは assistant メッセージだけではない: pi 1.5.2
  は `usage` エントリ、`compaction` と `branch_summary` のエントリ、ツールの結果にも使用量を書き、
  `get_session_stats` はそれらをすべて足す *(source: `core/agent-session.js`。docs: `session-format.md`)*。
  `UsageReader` はそうしたレコードをすべて、エントリ ID をキーにして読む。各 `agent_settled` の後の
  `get_session_stats` は照合用であって、源ではない。`UsageMeasured` は、ツールのループを含むターン**と**コンパクション
  するターンの両方で両者が一致することを P1 のゲートで示してから `exact` と宣言する。それまでは `partial`。
- **呼び出しごとのルートと請求**（ADR 0128 決定 8）: ルートはセッションに保存した選択である（claude の P1b と同じく、
  セッションが存在する間は変わらない）。請求: `openai-api` と `anthropic-api` は `metered`。`chatgpt-subscription` は
  未解決のまま残す。呼び出しの記録には、プランの枠とクレジットのどちらで払われたかを示すものがないからである。
  Bedrock の呼び出しはゲートウェイの行が数え、fold は pi 自身の行を落とす（`dropGatewayCounted`）。pi 自身の
  `cost` は価格付けに使わない。AF のルート優先の引き当てを使う。
- **文脈のゲージ。** 動作中: `get_session_stats` の `contextUsage{tokens, contextWindow, percent}`（実測、G3。
  モデル未選択のときはない）。コンパクションの後、pi は assistant が再び応答するまで `tokens` と `percent` を
  `null` で返し *(source)*、AF はその null を「不明」として保つ。休止中: 最新のコンパクションより後の、有効な最後の
  assistant 応答の使用量とモデルの窓。なければ不明で、コンパクション前の値は決して出さない。動作中の値は、P1 で
  作る汎用の任意インターフェース（種別が宣言する動作中の使用量オーバーレイ）を通じて使用量の読み取りに届ける。muse
  のオーバーレイの複製にはしない: あれは `sessionx` の中で `KindMuse` を判定しており（`session_usage.go:235`）、
  種別リテラルの上限がそれを禁じる。`ContextReporter` は `/messages` からしか読まれず、一括の使用量の読み取りでは
  呼ばれない（`agents.go:404`）。
- ChatGPT の枠の上限は、そのエラーを測ってから `UsageLimiter` にする。それまでは上限に当たるとターンが pi の
  メッセージで止まり、自動再開は予約しない。

### 9. 承認は AF が同梱する拡張で

AF は小さな拡張（TypeScript、ビルド不要。Stage 0 G4 は 8 行だった）を同梱し、毎回の起動で `-e` で読ませる。
拡張はツール呼び出しのたびに `ctx.ui.confirm` を出し、それが `extension_ui_request{method:"confirm"}` として
ドライバに届く。ドライバは種類 `approval` の `Interaction` を出し、`extension_ui_response` で答える（実測: 拒否すると
呼び出しが止まりモデルがそれを見る。承認すると実行される）。セッションが権限確認を省くときは、拡張は尋ねずに
許可する。

これを作った時点で `Driver.Capabilities().Permissions` を true にする。`Caps().PermissionChoice` は、往復の全体を
本物の CLI で Console から駆動してから立てる（§20.4）。他の `extension_ui_request` のメソッドは AF 自身の拡張からしか
来ない（決定 7 が他を締め出す）。未知のブロッキングな要求には cancel で答えて記録し、保留のまま放置しない。
`Questions` は false のまま: pi には組み込みの質問ツールがない。

### 10. stdio の af MCP、ファイルにトークンを書かない

セッションの `mcp.json` は af のサーバーを stdio の `workspace-agent mcp-stdio --self-report …`
（`mcpreg/builtin.go`）として、`exposure: "direct"` で挙げる。子プロセスの環境は
`agents.WithSessionEnv(env, m.Name)`（`internal/agents/sessionenv.go`）を通し、`AF_SESSION_NAME` と `AF_WORK_DIR` を
このセッションの値にする（Agent 自身の環境にはそれがないか、別のセッションの値がある）。`AGENT_TOKEN` と
`AGENT_ADDR` は継ぐ。したがってトークンは書かない（1.0.0 で実測、G2。固定する版で P0 に再測）。pi はそのファイルの
書き手を持って `writerTable`（`internal/mcpreg/materialize.go:98`）に加わる。メンバーとテナントの HTTP サーバーは
ヘッダを `${VAR}` の参照として書き、pi がそれを展開する *(docs)*。AF が書くすべてのサーバーを `exposure: "direct"`
にする: pi の既定の `codemode` はスクリプトを 1 つあたり最大 256 MB のワーカーで動かす *(docs)*。少数のツールの
ために共有ホストが払う必要のない費用である。`Caps().MCPClient = "yes"` は P1 のゲートの後。

### 11. 指示とスキル

フリートの指示はセッションのエージェントディレクトリの `AGENTS.md` に置き、pi はそれをユーザー単位の文脈として読む
*(docs)*。スキルはその `skills/` に置く。pi を `instrSupportedKinds` と `SkillsLayouter` に載せる。Stage 0 の
pi+guide の対照で、ワークスペースのガイドを足す費用（最初のリクエストで約 3.6k トークン）はすでに測ってある。

### 12. サインイン

設定に pi の接続カードを置く: **ChatGPT** は Agent が駆動する `pi auth login openai --no-browser`（デバイスコード）で、
URL の貼り戻しを代替にする（pi のコールバックは `127.0.0.1:1455` に固定、`docs/log/129` §1）。決定 6 の API キーは
メンバーキーとして。接続状態はログインファイルに対する `pi auth check --provider openai --json`。ログインと状態の
ルートは codex と同じくコントロールプレーンが中継する（`restLogin`）。ログインファイルとすべてのセッションの
エージェントディレクトリは、ファイルブラウザの拒否リストに足す。ChatGPT のルートを複数メンバーのデプロイメントで
提供するかは未決の問い 1 だった。オーナーの答えは、すべてのデプロイメントで、各メンバーが自分のアカウントで
サインインして提供する、である（個人の利用である: ログインはメンバーのもので、codex のサインインとまったく同じく
その Workspace から出ない）。個人の利用であり続けるための不変条件: **ChatGPT のログインをメンバー間で共有しない**。
したがって AF はテナントやデプロイメントが提供する ChatGPT のサブスクリプションを用意しない。

### 13. 種別が宣言するもの

| 宣言 | 値 | いつ |
|---|---|---|
| `Caps()` | `ManagedOnly`、`CanTranscript`。`MCPClient: yes` | P1、そのゲートの後 |
| | `PermissionChoice` | P1、Console からの往復の後 |
| | `CanFork`、`CanForkAt` | P3（`fork` / `clone`、新しいファイルは決定 5 と同じく捕まえる） |
| `Capabilities()` | `per-session-child`、`Steer`、`DynamicModel`、`DynamicEffort`、`Permissions`。`DynamicMode`、`Questions`、`Fork`（P3 まで）、`EventReplay`、`EphemeralThread`、`TUIAttach` は宣言しない | P1 |
| 任意のインターフェース | `ManagedLifecycler`、`ConnectionStatuser`、`ModelLister`、`SkillsLayouter`、`Instructions`、`UsageReader`、`UsageMeasurer`、`ContextReporter`、動作中の使用量オーバーレイ（新設、決定 8）、`MoveTarget`、`RouteAdapter`、`RouteBinder`、`RouteModelOwner`、`RouteChecker` | P1 |
| | `UsageLimiter`、`AuthRecoverer` | それぞれが依存するエラーを測ったとき |
| | `NoHeadlessChat`（v1 ではアシスタントチャットのバックエンドなし） | P1 |
| レジストリとスナップショット | `Kind*`、`kindTable`、`agentreg`、`managedDrivers`、`kindTable.ts`、種別とルートのカタログのスナップショット、MCP ツールのゴールデン、`guide/ref/agents.md` | P1 |

この種別は `internal/agents` の外に種別のリテラルによる分岐を足さない（`kindLiteralCeiling` は今の値のまま）。
振る舞いは上のインターフェースの後ろに置く。

### 14. 導入と版の固定

pi は MIT なので、プロプライエタリの CLI と違い、配布するイメージに入れてよい。正確な版を
`npm install --ignore-scripts` で専用のプレフィックスに入れ、絶対パスで起動する（Stage 0 では nvm だけの導入が
ターミナルセッションの `PATH` に無かった）。版の固定は `workspace/Dockerfile` の ARG、`versions.json` の項目、CLI
マニフェストの行（#4135 がまだマージされていなければ手で保つ表）、そして版上げのときにライブの契約を走らせる
`pi-contract.yml` である。pi は Node ≥ 22.19 を要する *(package.json)*。イメージの `node:22` ベースは版を固定する
ときに確かめる。

## 段階

| 段階 | 内容 | 関門 |
|---|---|---|
| P0 | 固定する版での調査。製品コードはなく、ジャーナルにまとめる: 子プロセス 1 つのアイドル時とターン中の RSS。その版での決定 4 のプロトコル（abort の順序、`handled` のプロンプトが動いている仕事を残すことがあるか）。2 つの子プロセスが更新するときの `auth.json` のシンボリックリンクとロック。ログインと `OPENAI_API_KEY` の両方があるとき `openai` がどちらの資格情報を使うか。周囲の資格情報（`AF_ROUTE_ISSUE_TOKEN` と `AF_ENGINE_ISSUE_TOKEN` を含む）をそれぞれ試す子プロセスに対する除外一覧。決定 6 の束縛のため、ChatGPT のログインファイルのどこに安定したアカウント ID があるか。ループバックの代役ゲートウェイに対する Bedrock の `baseUrl` + ベアラー（パス、`Authorization`、呼び出したプロセス）。RPC 経由の `/share`。MCP の環境の継承。指示のパス。信頼を断った状態でユーザー単位のパスから読ませたリポジトリのスキルとプロンプト、`--session-dir` に上書きされるプロジェクトの `sessionDir`。課金されるすべてのレコードの使用量と `get_session_stats` の、コンパクションをまたいだ照合。コンパクション直後と子プロセス停止後の文脈の値 | すべての項目に答えるか、それが支える決定を日付付きの注記で改める |
| P1 | 種別、ドライバ、トランスクリプト、`chatgpt-subscription` と `openai-api` とその束縛、ルートを知るモデル一覧の契約、動作中の使用量オーバーレイのインターフェース、接続カード、承認、af MCP、使用量、指示、導入と版の固定、ガイドの行 | 両ルートについて ADR 0128 決定 14（答えたルート。台帳が書いた請求。否定の対照: 資格情報を外した同じ起動が目に見えて失敗し、ログイン済みのときの `openai-api` のセッションがなおキーに請求される）。キーを差し替えたり別の ChatGPT アカウントにログインしたりすると、保存済みのセッションが目に見えて拒否される。ツール実行中に割り込んだターンが子プロセスを残さない。Console から承認に答える。同じ作業コピーの 2 つの pi セッションがそれぞれ af MCP のツールを呼び、それぞれ自分に帰属する |
| P2 | ゲートウェイ経由の `bedrock`、`bedrock-tenant`、`bedrock-deployment` | ルートごとに決定 14。ゲートウェイの呼び出し元検査が子プロセスとそのツールを受け入れ、別セッションのプロセスを拒否する。Agent の環境に置いた周囲の AWS 資格情報と `AF_ROUTE_ISSUE_TOKEN` に子プロセスから届かない |
| P3 | `anthropic-api`。セッションに束縛された資格情報ができたら `af-engine-llm`。フォーク。P1 の測定が後回しにしたもの（`UsageLimiter`、`AuthRecoverer`） | ルートごとに決定 14。フォークが親の文脈を持って再開する |

どの段階のライブゲートも本物のアカウントを要し、`needs-live-run` を付ける。代役の pi に対する単体テストでは閉じない。

## 退けた案

- **pi の TUI のペインを唯一の経路に、または v1 で 2 つ目の経路にする。** 決定 2。
- **エージェントディレクトリを 1 つ共有し、セッションごとには環境とフラグだけを変える。** ChatGPT のログインと API
  キーは 1 つのプロバイダにあるので、セッションがどちらを使ったかが pi の内部の優先順位次第になり、それはどの
  リリースでも変わりうる。共有のディレクトリにあるメンバー自身の `models.json` や拡張もすべてのセッションにかかってしまう。
- **セッションごとに ChatGPT のログインを複製する。** 2 つの複製は別々に更新され、プロバイダがリフレッシュトークンを
  回転させる場合、先に更新した複製が他を無効にする。1 つのファイルをシンボリックリンクしてロックすれば、書き手は
  一度に 1 つになる。
- **ChatGPT のサブスクリプションをルートゲートウェイ（A2）経由にする。** トークンをセッションに束縛できるが、メンバー
  がすでに codex と同じくこの Workspace で持っている資格情報のために、ChatGPT の非公開のバックエンドをホットパスで
  中継することになる。提供される（テナントの）サブスクリプションのために考え直すこともない。決定 12 がそれを
  否定しているからである。
- **キューに入ったメッセージに pi の `follow_up` を使う。** 決定 4。
- **pi のプロジェクトの信頼を与える、または `.pi/` から何も読まない。** 決定 7: 前者は承認より前に動くコードを読み込み、
  後者は安全を少しも増やさずにリポジトリのスキルとプロンプトを落とす。

## 未決の問い

1. ~~ChatGPT のサブスクリプションのルートを、複数メンバーのデプロイメントで提供してよいか。~~ **回答済み（オーナー、
   2026-11-14、#4138）: はい、どこでも。** 各メンバーが自分の Workspace で自分のアカウントでサインインする。AF では
   個人の Workspace でしか使わない。OpenAI の文言が除くのは転売と複数利用者のサービスで、1 人のメンバーの
   Workspace にとどまるログインはそのどちらでもない。不変条件は決定 12 に記した。
2. **セッションはリポジトリの pi の資源のうちどれを読み込むか。** 案: (a) テキストの資源だけ — スキル、
   `.agents/skills`、プロンプト、`APPEND_SYSTEM.md` を参照で読み、信頼は決して与えない（決定 7、推奨）。(b) `.pi/`
   から何も読まない。(c) すべて。メンバーの求めに応じてリポジトリ単位で pi のプロジェクトの信頼を与える。
   オーナーの決定（#4139）。

## 結果

- 8 つ目のマネージドドライバ。固定のプロンプト費用は約 1.2k トークンと実測され（Stage 0）、メンバーがセッションごとに
  選ぶルートで動き、ルートは呼び出しごとに記録される。
- pi はルートのカタログで `chatgpt-subscription` と `openai-api` を宣言する最初のハーネスになる。
- pi のセッションはメンバー自身の pi の設定も、リポジトリの pi の拡張・パッケージ・MCP サーバー・設定も見ない。
  スキルとプロンプトは見る。pi の拡張やパッケージに頼るメンバーは、AF ではそれを使えない。
- 版上げのたびに除外一覧とプロトコルを確かめ直す。pi のリリースの速さがドリフトの危険であり、copilot の毎週の
  リリースと同じである。
