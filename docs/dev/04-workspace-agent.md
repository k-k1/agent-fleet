# 04. Workspace Agent と Workspace イメージ

> 正: コード（本書は地図と設計意図）/ 主な更新トリガ: セッションモデル・kind 追加・イメージ同梱物の変更 / 最終確認: 2026-07

## 4.1 位置づけ

per-user コンテナ内で常駐する Go プロセス。コンテナの PID 1 は `--init`（tini、ゾンビ reap のため）で、
その配下で非特権ユーザーとして動く。CP から見た**唯一の実行主体**——runtime・tmux・git・fs・CLI エージェントに
触るのは必ず Agent（CP は中継のみ、[05](05-api-contracts.md)）。全 API は `requireToken`（Bearer
`AGENT_TOKEN`）で保護（[07 §7.5](07-security.md)）。コンテナ netns を共有するため、コンテナ内サービスへ
loopback で届く（preview の下請け `/proxy/{port}`とBrowserManagerの直接navigation）。

## 4.2 セッションモデル

**1 Session = 会話・作業 dir・設定・実行状態を束ねる論理スロット**。`kind` はエージェント種別、
`driver` は制御経路を表す。`driver=tui` だけが tmux session（プレフィクス `claude_`）を 1 本持ち、
`driver=managed` は Workspace 単位の共有 runtime 上の thread なので pane もセッション専用プロセスも持たない。
決定的 sid = uuidv5(dir, name) は status 等の AF 内部キーであり、エージェント自身の会話 ID は別に保存する。

- **メタ永続**: per-session メタ（kind/dir/model/repo/createdAt/stoppedAt/Archived）を
  `~/.config/agent-fleet/sessions`（denylist 配下・home volume、`AF_SESSIONS_DIR`）に保存。
  **Stop→Start を跨いで一覧と再開が生きる**。停止 TTL は `AF_SESSION_STOPPED_TTL`（既定 7d）で剪定。
- **一覧はメタ駆動 + driver ごとの live 状態マージ**。managed は runtime handle、tui は tmux を生存判定に使う。
  メタの無い生 `claude_*` tmux（孤児）も列挙し、ペインの起動コマンドから kind を sniff——
  「動いているのに一覧に出ない」手詰まりを封じる。
- **止め方の 3 すくみ + 1**:
  - `halt` = 現 driver を停止・メタ保持 → 停止中として一覧に残る（再開可）
  - `stop` = 現 driver を停止・メタ破棄 → 一覧から消える（エージェント native の会話履歴は残る）
  - `archive`/`restore` = メタ+履歴保持で非表示 ↔ stopped 復帰
  - `recreate` = 旧メタと履歴を archive し、同じ dir/kind/driver で新しい slug の会話を起動
- **再開**: managed は保存した native conversation ID を共有 runtime へ resume し、Agent / daemon 再起動時は
  reconciliation で live handle を再構築する。claude/codex/opencode は driver にかかわらず
  **作業 dir 消失で再開不可**（resumable=false、home フォールバックしない）。shell は home フォールバック。claude の resume 判定は
  jsonl に実会話行（user/assistant）があるか——
  ⚠️ Remote Control が ON のとき会話前でも `bridge-session` 1 行が書かれるため「jsonl 存在=resume 可」に
  すると `--resume` が即死する。non-resumable なら jsonl を捨てて `--session-id` 新規。（entrypoint の
  seed 既定は **新規 WS で Remote Control OFF**＝`remoteControlAtStartup: false`。既存 WS も
  settings.json に `remoteControlAtStartup` キーが無ければ起動時に一度だけ `false` を補って既定 OFF に揃える
  ——ただしユーザーが Console で明示設定した値（キー在り）は上書きせず尊重する。）
- ⚠️ **tmux の `-t` は前方一致**（exact→prefix→fnmatch）。`claude_foo` が `claude_foo-sh` に一致して
  誤判定・誤 kill しうるため、target 参照は全て `=name` の exact 形式で行うのが本リポジトリの規約。
- **DB ミラー（B 案）**: CP の `GET /api/sessions` は running 時に Agent から取得して DB を洗い替え、
  stopped 時は DB から `alive:false` 配信＝**Workspace 停止中でも一覧が見える**（[06 §6.3](06-data-model.md)）。
- **fork**: claude/codex/opencode/copilot の会話履歴を引き継いだ新セッションを別スロットに分岐
  （`POST /sessions/{name}/fork`）。新スロットは元の kind と driver を引き継ぐ。任意ボディ
  `{"at": <anchorId>, "include": bool}` で**過去の発言時点**から分岐できる（docs/55）。分岐点の
  アンカーは `transcript.Turn.AnchorID`（kind 固有の不透明 ID）で、包含差の吸収と起動方式の
  可否は各 kind の `ForkAtResolver` が答える（`agents.ErrForkAtRoute` → `fork_at_unsupported`）。
  実現手段は kind で割れる: codex/opencode は runtime API の公式パラメータ（managed 必須）、
  claude/copilot は転写の切り詰め（TUI でも可）。
- **driver 切替**: Codex / OpenCode は `POST /sessions/{name}/driver` で同じ会話を `managed` ⇄ `tui` に
  stop→resume する。実行中 turn がある間は `409 busy_switch`。kind・dir・native conversation ID は維持する。
- **モデル解決（作成時）**: codex/opencode/copilot を明示モデル付きで作成する際、Agent は live カタログ
  （`codex debug models` / `opencode models` / copilot は `/model` ピッカーの PTY スクレイプ）に照らして、
  ピッカー表示名や一意な略称（例 `terra`）を完全な slug（`gpt-5.6-terra`）へ解決する（`resolveLiveModel`）。
  曖昧・利用不可なら **clone/worktree の副作用前に `400 bad_model` で拒否**——「起動後に無効モデルで落ちる」罠を
  封じる。カタログを読めない縮退時（オフライン / CLI 未導入）は指定値を保持して通常起動を続ける。MCP 経由の起動は
  先に read ツール `list_models` で候補 id を確認してから `create_session` に渡す。claude は起動時に自前の
  picker/`--model` に委ねるため対象外。
  - copilot は **Free プランだと Auto のみ**でモデル catalog が空（＝既定だけ）。かつ **Auto は `--effort` 非対応**
    （`Model "auto" does not support reasoning effort configuration`）なので、起動コード（`program.go`/`driver.go`）は
    **concrete な非 auto モデルの時だけ `--effort` を渡す**。auto/未指定で effort を付けると起動失敗する（Free で常時
    踏むフットガン）ため、フロントの `useEffortOptions` も copilot+auto/未指定では effort を既定のみにする。
- **ブランチ支援**: `suggest-branch`（セッション会話を要約して AI がブランチ名を提案）/ `rename-branch`。
- タイトル系（`/title/{suggest,accept,dismiss,regenerate,set}`）は会話からの表示名提案。

## 4.3 エージェント kind / driver 統合パターン

kind = `claude` / `codex` / `cursor` / `opencode` / `agy` / `copilot` / `kiro` / `shell` / `ssm`（agy は [32](../32-agy-agent-kind.md)、copilot は [36](../36-copilot-agent-kind.md)、kiro は [43](../43-kiro-agent-kind.md) — copilot / cursor / kiro は Terminal+Managed 両対応・per-session child の ACP driver、agy は Terminal 専用）。
Codex / OpenCode は managed が新規既定で、tui は明示選択。Claude / shell / SSM は tui のみ。
**新 kind を足すときに埋める面**は毎回同じ（雛形は opencode 追加時に確立、codex で再利用）:

| 面 | claude | codex | opencode |
|----|--------|-------|----------|
| 既定 driver | tui | managed（app-server）| managed（serve）|
| tui 起動 | `--session-id`/`--resume` + `--name` + `--model` | `codex --remote … resume`、旧会話 ID は hook で捕捉 | `opencode --session <id>`、ID は plugin で捕捉 |
| managed 起動 | — | 共有 app-server の `thread/start|resume` | 共有 serve の v1 session API |
| 会話正本 | JSONL | rollout JSONL（両 driver 共通）| SQLite `message` / `part`（両 driver 共通）|
| live 状態 | hooks + tmux probe | managed=RPC event、tui=hooks/probe＋observer | managed=SSE event、tui=plugin/probe |
| 認証経路 | `claude auth login --claudeai`（[08 §8.5](08-integrations.md)）| `codex login`（API キー / device flow）| env キーを**コマンド前置**で注入（`secrets.enc` 保存）|
| 資格の置き場 | `CLAUDE_CONFIG_DIR`（home 外退避）| `~/.codex`（CLI 所有）| `secrets.enc`（Agent 所有）|
| fs denylist | `.claude`・`.claude.json` ほか | `~/.codex` | `~/.local/share/opencode` |
| Console 側 | registry に表示・capability（fork/imagePaste/headlessChat 等）| 同 | 同 |

- ⚠️ **env は `tmux new-session -e` ではプロセスに届かない**（セッション環境止まり）。env 注入は
  **コマンド前置**（`NAME='v' … prog`）が本リポジトリの規約。claude は auth login 方式採用後
  前置も廃止（秘密の cmdline 露出を解消）。
- ⚠️ **子プロセスは必ず reap する**（workspace-agent は PID 1 ではない — init の自動回収がなく、
  Wait されない子は永久に `<defunct>` で残り PID をリークする）。`.Run()`/`.Output()`/
  `.CombinedOutput()` は内部で Wait するので安全。`cmd.Start()` / `pty.Start()` で自前管理する
  場合は**異常系を含む全経路**で `cmd.Wait()`（または waiter goroutine）に到達させること。
  漏れやすいのは「起動タイムアウトで `Process.Kill()` して return」する失敗経路
  （codex app-server / opencode serve で実例、2026-07 修正）。PTY ログインフロー共有の
  `agents.Flow.Close()` は Kill＋Wait まで面倒を見る（agy /usage スクレイプのゾンビ蓄積で顕在化、
  `internal/agents/flow_test.go` が回帰テスト。経緯は [32](../32-agy-agent-kind.md)）。
- ⚠️ codex のフックは claude と同じ**入れ子スキーマ**（`hooks.<Event>=[{hooks=[{type,command}]}]`）。
  フラットに書くと**パースは通るが無音で発火しない**（resume が新規化する既知の罠）。
- RTK（安全化ラッパー、vendor 時のみ）は 3 エージェントで機構が違う: claude=settings.json の
  PreToolUse/Bash フック（透過）/ opencode=プラグインでコマンド書換（透過）/ codex=AGENTS.md への
  指示ブロック（**ベストエフォート**）。codex/opencode の on/off 実体は artifact の有無で、永続 pref
  `~/.config/agent-fleet/rtk.json` を正として起動時と `GET/PUT /agents/rtk` が pref→artifact を適用。

managed の共通境界は `Driver` / `ThreadHandle` / `RuntimeSupervisor`。`/turn`・`/respond`・`/settings` は
意味論 API として driver 非依存に受け、managed は構造化 API、tui は既存のキー入力経路へ委譲する。
会話本文を AF 独自ストアへ複製せず、native store を read の正本として transcript を正規化する。
実装判断とプロトコル実測は [ADR 0015](../decisions/0015-agent-managed-driver.md) と
[実装記録](../27-agent-managed-driver.md) を参照。

## 4.4 状態バッジ機構

状態の外向き語彙は `working` / `idle` / `question` に正規化する。claude の hooks が
`workspace-agent session-status <state> <sid>` を発火し
`~/.config/agent-fleet/session-status/<sid>.json` に記録。状態は
**working**（UserPromptSubmit）/ **idle**（Stop=入力待ち）/ **question**（PreToolUse matcher
`AskUserQuestion`）。hooks はセッション起動毎に加算マージし、PreToolUse は **matcher 単位**で
RTK（`Bash`）と状態（`AskUserQuestion`）が共存できる（トグルが互いを壊さない）。
Codex / OpenCode の managed driver は runtime event から同じ status store と通知 seam へ書く。
tui では codex は同型フック＋observer、opencode はプラグイン通知を使う。
Console は 4 秒ポーリングで ● 進行中 / ❓ 質問 / ✓ 入力待ち / 停止中を描画し、idle/question 遷移で
ブラウザ通知。

## 4.5 チャット・アシスタント面（headless CLI）

設計の全容は [docs/19](../history/19-assistant-chat.md)。要点:

- **チャットは tmux セッションではない**。Agent 内の並列サブシステムで、`claude -p`（headless）を
  会話ストア（`~/.config/agent-fleet/chats/<id>.json`）と組で駆動。ストリームは SSE（[05 §5.3](05-api-contracts.md)）。
- **Claude config-dir**: OAuth 資格情報は対話セッションと同じ `CLAUDE_CONFIG_DIR` の
  単一ファイルを直接使う。旧 symlink + copy-back は refresh 時の tmp+rename でリンクが
  実ファイル化し、並行プロセスが異なる refresh token を持ち得たため廃止。チャットへの
  user/project 設定混入は `--setting-sources ""`、MCP 混入は `--strict-mcp-config` で遮断する。
  旧専用 config-dir の transcript は初回実行時に共有 projects へ create-only で移行する。
- **フォールバックの可視化**: 会話の `agent` は作成時の希望値として保持し、各 assistant
  message の `agent` と会話の `active_agent` に実際の実行 backend を記録する。SSE は最初に
  `agent` frame を返し、Console の live bubble/header も Claude→Codex 等へ即時追従する。
- **コンテナ内 stdio MCP**: チャットの claude には `workspace-agent mcp-stdio` を `--mcp-config` で
  付与（PAT 不要・egress 不要・身元=自コンテナ）。既定 read-only、`--write` 時のみ
  `create_session`・`send_to_session`・`list_assistants`・`ask_assistant`・
  `get_chat_plan`／`set_chat_plan`（作業計画 — [33](../33-chat-context-usage.md) 第5段）を**広告**する（権限プロンプトでなく
  「見えるツール集合」がゲート）。CP の `/mcp` とは**別実装・別スコープ**（意図的な二重管理、
  [03](03-control-plane.md)）。
- **対話セッション用 stdio MCP**: mcpreg builtin `af`を各CLIのnative設定へmaterializeし、
  `workspace-agent mcp-stdio --self-report --chromium-attach`で起動する。広告・callを
  `af_report`＋Chromium Attach View 7種だけに固定し、アシスタント用のフリートread/writeは渡さない。
  `--self-report`単独は後方互換として`af_report` 1本、`--chromium-attach`単独はscopeを拡張しない。
- **Codex の無人承認**: headless chat は承認 UI を持たないため `-a never` に加え、明示的に
  grant した AF MCP server を `default_tools_approval_mode="approve"` にする（未指定だと
  `user cancelled MCP tool call`）。一方 `-s read-only` は維持し、MCP は実行できても
  shell/file 経由の変更は許さない。
- **アシスタント**（`/assistants*`）: persona/model/knowledge/tools(af_read|af_write|none) を持つ
  テンプレート。`ask_assistant` は相手ターンを強制 tools=none で 1 ショット実行＝1 ホップで停止・
  副作用なしを構造で担保。
- **モデル解決**: 新規会話の作成時にテンプレートまたはリクエストの明示モデルを最優先し、空なら
  agent ごとの既定を会話メタへスナップショットする（Codex=`gpt-5.6-luna`、
  OpenCode=`opencode/nemotron-3-ultra-free`）。既存会話や明示指定を後から書き換えず、
  プロバイダ／カタログの既定変化から会話の再現性を守る。ただし会話が持つモデルは1本＝
  **作成時 agent 基準**なので、実際に回す backend が別のとき（認証フォールバック／途中切替）は
  `chatModelFor(conv, kind)` がその CLI の設定行（ui-prefs `assistantModels`）から解決し直す。
  会話の値をそのまま渡すと別 CLI に他社のモデル id を食わせることになる。
- **途中でのエージェント切替**: `PATCH /chat/conversations/{id}` は `title` と `agent` を受ける
  （両方省略は 400、headless chat 非対応 kind は 400、実行中ターンは 409）。設定の
  「エージェント優先順位」は新規会話と one-shot にしか効かないので、進行中の会話を動かす口が
  これ。切替はピン留めと Model の差し替え＋notice 1行だけで、backend 毎の resume ハンドルと
  メッセージカーソルは温存する（戻したとき native セッションを続きから使うため）。未知の履歴は
  次の送信で `syncProviderPrompt` が再生する＝フォールバックと同じ経路。
- 向き不向き: チャットは短〜中の翻訳/要約/Q&A。ファイル出力を伴う大規模作業はセッションへ
  （Files の「セッションに送る…」がパス参照で渡す）。

## 4.6 git / fs 面

- **repos**: clone（`GIT_TERMINAL_PROMPT=0` で fail-fast、name は正規表現で traversal 防御）/
  status（porcelain=v2 解析）/ branches / checkout / fetch / ff / delete。
  clone 後 submodule は best-effort（SSH URL を HTTPS へ書換えて update。親 clone には
  `--recurse-submodules` を付けない——SSH 登録 submodule で親ごと失敗するため）。
- **submodule 同期（`git_submodule.go`）**: worktree/clone 起動は submodule を per-worktree の
  gitdir（`.git/worktrees/<wt>/modules/…`）へ取得する＝**親とは別に丸ごとクローンし直す**。
  実測（git 2.39）**取得中の `submodule update` を kill すると submodule は wedge する**——
  gitdir だけ残り HEAD が未生成、作業ツリーは空、以後の `submodule update` は
  "Unable to find current revision" で恒久的に失敗、しかも `git status` はクリーンで
  `git submodule status` も健全な空白プレフィクスを出すため誰も気づかない。1.4GB 級の
  submodule はこれを毎起動で踏む。よって同期は
  (1) 起動予算（60 秒）を過ぎても**待つのをやめるだけで kill しない**（背番で継続、
  完了/失敗はログと通知）、(2) 未取得のまま起動したらログ＋通知（kind `submodule-sync`）、
  (3) 既に wedge した submodule は実測の唯一効くレシピ——`fetch` で転送を完了させ、親が記録する
  sha を `checkout --detach --force`——で修復する。作業ツリーが空のものだけが対象なので
  ローカル変更を壊さない。worktree 再利用（再起動）時も未取得なら再同期する。
- **SCM（read/write git）**: changes / diff / log / graph / show / stage / unstage / discard / commit。
  sha は hex 検証、応答はサイズ上限でキャップ。
- **fs**: home ルートのツリー/ファイル/アップロード/リネーム等。traversal 防御・サイズ上限・
  バイナリ判定。**denylist**（一覧非表示 + 直アクセス 400）: `.claude`・`.claude.json`・
  `.config/agent-fleet`・`.ssh`・`.git-credentials`・`~/.local/share/opencode`・`~/.codex`・
  `~/.aws`（SSM ログインの SSO トークンキャッシュと生成 config）。
- **Git LFS**: image に git-lfs 同梱・system install。clone/checkout で smudge。残ポインタは
  fs が検出してビュアーにバッジ（既存 working copy は手動 `git lfs pull`）。
- git 認証は統一 cred helper が `secrets.enc` を都度復号して出力（[07 §7.6](07-security.md)、
  Bitbucket は refresh 内蔵の専用 helper。[08](08-integrations.md)）。

## 4.7 transcript / usage

- claude の会話 jsonl は**末尾ウィンドウ読み込み + 逆方向ページング**で返す
  （`GET /sessions/{name}/messages`、[decisions/0009](../decisions/0009-transcript-paging.md)）。
- codex / opencode にも各 CLI の保存形式（jsonl / SQLite store）を読む transcript リーダーがあり、
  driver にかかわらず出力を共通の turn 形に揃える（パーサは統合しない——docs/23 の方針）。
- usage: `GET /claude/usage`・`GET /codex/usage`（各 CLI のローカル記録から集計）。

## 4.8 secrets（Agent 側の責務）

`secrets.enc`（AES-256-GCM・0600）の所有と、`workspace-agent cred` / `workspace-agent bitbucket-cred`
サブコマンドによる**平文ファイルを作らない**資格供給。鍵 `AF_SECRET_KEY` は CP が起動時注入
（封筒暗号の全体像は [07 §7.6](07-security.md)。Agent は暗号 provisioning に無関心）。
起動時に旧平文資格の自動移行あり。`AF_MASTER_KEY` 未設定の dev では平文 `secrets.json`（同一経路）。

## 4.9 Workspace イメージと entrypoint

`workspace/Dockerfile`（multi-stage golang→node:22-slim。サイズは BAKE ノブで大きく変わる）。
**イメージと Agent は全デプロイターゲット共通**（移植の肝、[09](09-deploy.md)）。

- **エージェント CLI は 2 経路**（`ARG BAKE_AGENT_CLIS`、**既定 0 = lean**。docs/35 §35.4.1）:
  - **lean（既定）**: claude / opencode / codex / copilot / cursor / agy / rtk を**イメージに焼かない**
    （プロプライエタリ CLI を再配布しない安全既定）。entrypoint の **boot-install** が初回起動時に
    `versions.json` のピン版を公式配布元（npm / GitHub Releases 等）から `~/.local` へ導入する
    （home 永続なので 2 回目以降は無音スキップ。ネット不通は WARN で続行し次回起動時に再試行。
    self-update opt-in が OFF の起動では進んだ版をピンへ戻す repin あり）。
    kiro だけは全ユーザー一律の boot-install をせず（展開後 ~855MB）、利用時にオンデマンド導入。
  - **`BAKE_AGENT_CLIS=1`**: 上記 CLI を焼き込み（初回起動を速くしたい自社デプロイ向けの明示ノブ）。
- **版ピンはどちらの経路でも同じ `ARG`**（`CLAUDE_CODE_VERSION` / `OPENCODE_VERSION` /
  `CODEX_VERSION` / `COPILOT_VERSION` / `CURSOR_VERSION` / `AGY_VERSION` / `KIRO_VERSION` /
  `RTK_VERSION`——bump 手順は [10 §10.2.1 の runbook](10-development.md)）。BAKE ノブに関わらず
  全ピンを `/usr/local/share/agent-fleet/versions.json` に書き出し、Agent の
  `GET /env/tool-versions`（設定→環境「ツールのバージョン」: 実効 / 焼き込み / ~/.local
  override / ピン差分の read-only 表示）と e2e-smoke と boot-install が参照する。
  この表にはエージェント CLI に加えて **AWS / ops MCP 系**（`awscli` / `mcp-grafana` /
  `cloudwatch-mcp` / `aws-mcp`）も並ぶ。後 2 つは `uv tool install` の Python サーバーで、
  **exec で版を訊けない**（cloudwatch は `--version` でサーバーが起動し、AWS MCP プロキシは
  `--version` を持たない）ため、`toolSpec.PyDist` を付けて venv の dist-info 名から読む
  （`uvToolVersion`）。新しい Python MCP サーバーを足すときは同じ扱いにすること。
- **焼き込み（共通ツール、`BAKE_OPTIONAL_TOOLS`=既定 1 ほか）**:
  Go toolchain（`ARG GO_VERSION`、go.mod と歩調）、
  build-essential + python3（+ `break-system-packages`、pip --user は home 永続）、vim・git-lfs・
  jq 等の定番、tzdata、amd64/arm64共通の固定版Debian Chromium（setuid sandbox helperをbuild時検証）と
  日本語font。Chromiumはsandbox有効、`--disable-dev-shm-usage`で起動する。Docker runtimeはsetuid helperのnamespace作成用に
  `SYS_ADMIN`をbounding setへ追加するが、`dev`にはeffective capabilityを付けず、helper以外のsetuid/setgid bitはimageから除く。
  **Java は image 外**:
  共有 JVM dir（Temurin 8/21/25）を `/usr/lib/jvm:ro` で
  マウント（イメージ 2.1G→1.0G の削減。⚠️ Temurin の cacerts symlink は抽出時に実体化しないと
  空トラストストアになる）。node は nvm（home・オンデマンド）。
- **レイヤ順の意図**: 重く変わらない RUN（toolchain・npm -g）を前段、頻繁に変わる COPY
  （agent バイナリ・entrypoint・plugin・notes）を最後尾に集約——小修正でキャッシュを壊さない。
- **entrypoint の seed 方針 = 「無い時のみ」**: `settings.json`（skip-permissions / RC /
  通知 / rtk フック）、`~/.gradle/gradle.properties`（メモリ制約ホスト向けの保守的既定）。
  以後は設定 UI が真実（毎起動 force すると UI と喧嘩する）。**毎起動 refresh するもの**:
  opencode plugin。
  ⚠️ **利用ガイド（`workspace-notes.md`）の配布は entrypoint から agent へ移した**（docs/60 / ADR 0042）。
  claude=`/etc/claude-code/CLAUDE.md`（managed policy・image 焼込）は据え置き、codex・opencode・agy の
  `AGENTS.md` は agent の `reconcileAgentInstructions()` が**マーカー付きで合成**し、copilot・kiro へは
  AF 専用ファイル（`agent-fleet-guide.*`）で配る。**cursor はローカルに user スコープが無く配れない。**
  以前の `cp -f` はファイルを丸ごと上書きしており、利用者がそこへ書き足した文章が毎起動で
  消えていた（＝ユーザー層を作れない原因）。同じ 1 人の書き手がフリート方針・ユーザー指示・
  rtk ブロックを順に置き、マーカー外は温存する。
  ⚠️ `.dockerignore` は `**/*.md` 除外に `!workspace-notes.md` 例外が必要（`//go:embed` も同様）。
- **タイムゾーン**: toolchains 設定の `timezone`（既定 `Asia/Tokyo`）を entrypoint が `export TZ`。
  反映は Stop→Start。
- **ロール別 docs のマウント**: エージェントが環境仕様の QA に docs で根拠付き回答できるよう、
  `docs/` を **CP イメージ**に焼き込み（`control-plane/Dockerfile`、context=repo root）、コンテナ
  起動時に **CP が呼び出し元メンバーのロールで許可分だけ**を `<dataDir>/docs` へステージ
  （`control-plane/workspace_docs.go` `stageWorkspaceDocs`）→ `dockerRuntime.Start` が
  `/usr/local/share/agent-fleet/docs:ro` でマウント。共有イメージには docs を含めないので、
  member のコンテナは内部 docs をディスク上に一切持たない（＝ provisioning 時点でロール分離）。
  露出範囲: `member`→`guide/member` と `dev/`、`tenant_admin`→`guide/` と `dev/`、
  `super_admin`→全 docs。decision / history などの非公開資料は super_admin に限る。毎起動で
  再ステージ（ロール変更が次回起動で反映・イメージ版に追従）。ECS アダプタは未配線
  （`<dataDir>` が EFS AP でホスト経路が異なるため。未配線でも起動は壊れず docs 無しになるだけ）。
- claude の自己更新は `~/.local` 側のみ・焼き込み版は固定。壊れた symlink（旧 home パス）は
  entrypoint が検出して repair。
- 反映ルール: image / entrypoint に触れたら **image 再ビルド + 利用者の Stop→Start**（[10](10-development.md)）。

## 4.10 BrowserManager

`BrowserManager`はWorkspace当たり1つのChromium processをpipe CDPで遅延起動し、browserIdごとに独立
BrowserContext + Pageを所有する。公開内部面は`POST/GET/DELETE /browser/pages*`と`GET /ws/browser?id=`。
Agent自身の7700、外部top-level navigation、管理endpointを拒否し、PageのHTTP/WS/SSEはコンテナloopback内で完結する。

WS wire v1は最初のtext `ready`、以降の状態/navigation/console/error textと、生JPEG binaryを送る。
Consoleから受けるのはviewport、mouse/wheel/key/text、navigate/reload/history、visibilityだけでraw CDPは公開しない。
Page上限2、最大1600×1200/DPR 1、12fps/quality 70、latest-frame 1枚、非表示/切断猶予60秒が既定。
12fpsはWebSocket送信だけでなく、Pageごとの容量1 frame workerがCDP ACKを`1/maxFPS`遅延して
Chromiumのcapture/encode元から制限する。pipe CDPは1 message 8 MiB・event 256件/合計32 MiBで固定し、必須eventの飽和時は
waiter goroutineやqueue memoryを増やさずChromiumを終了してPageを`crashed`へ遷移させる。
詳細契約とW5検証結果は[設計31](../31-container-browser-pane.md)を参照。

## 4.11 tmux サーバのスコープと第 2 インスタンスの隔離（開発・E2E 必読）

> 経緯: agy 統合の M1 E2E（[32](../32-agy-agent-kind.md)、2026-07-20）で、テスト用に別ポートで
> 起動した agent の shutdown が**共有デフォルトソケットへ `tmux kill-server` を実行**し、
> 並行稼働中の無関係なセッション（開発者自身の claude CLI 含む）を計 4 回全滅させた。
> 本節の規約はその再発防止（恒久対応 + 開発時の安全手順）。

**設計上の前提と恒久対応**:

- 本番は 1 コンテナ 1 agent で、デフォルトソケットの tmux サーバは agent が唯一の作成者。
  旧 shutdown はこの前提に依拠して `kill-server`（サーバごと全滅・列挙不要で確実）を使っていたが、
  前提は**同一環境に第 2 インスタンスがいる瞬間に崩れる**（kind の問題ではない —
  ソケットは kind 間で分離されておらず、managed はそもそも pane を持たないため、
  通常運用では顕在化しなかっただけ）。
- **`kill-server` は agent 製品コードで全面禁止**。停止（graceful shutdown / halt / stop）は
  **自インスタンス管理下（自メタ ∩ live）のセッションへの `kill-session`（exact target）のみ**。
  自分のメタが無い live セッションは「他インスタンスの作業」か「メタ喪失の孤児」かを
  区別できないので触らない（孤児は C-c の礼儀を失うだけで、コンテナの SIGKILL と運命を共にする。
  本番では所有セッションを消せばサーバは exit-empty で自然終了し、旧挙動と同じ終状態になる）。
- **tmux の exec は `tmuxx.Cmd` に集約**（`exec.Command("tmux", …)` 直呼び禁止）。
  `AF_TMUX_SOCKET=<name>` を設定すると全 tmux 呼び出しが `tmux -L <name>` になり、
  デフォルトソケットにも**継承した `$TMUX` にも**届かない専用サーバへ完全隔離される。
- 以上 2 点は `workspace/agent/tmux_guard_test.go` の tripwire（`kill-server` のコード行検出・
  funnel 迂回検出）が回帰を止める。

**第 2 インスタンスを起動する安全なやり方（コンテナ内 E2E・手元デバッグ）**:

```sh
# 3 点セットが必須: ソケット・メタ dir・ポート。どれか 1 つでも共有すると本物と衝突する。
AF_TMUX_SOCKET=af-e2e-$$ \
AF_SESSIONS_DIR="$HOME/tmp/e2e-$$/sessions" \
AGENT_ADDR=:7710 AGENT_TOKEN=test-token \
./workspace-agent
```

- `AF_TMUX_SOCKET` — 専用 tmux サーバ（`-L`）。⚠️ これ無しで tmux pane 内（＝いつもの
  開発セッション）から起動すると `$TMUX` を継承し**確実に共有サーバへ向く**。インシデントの直接原因。
- `AF_SESSIONS_DIR` — メタの分離。共有すると本物のセッションを「自分の管理下」と誤認して
  停止対象にしてしまう（shutdown の owned 判定はメタが根拠）。
- `AGENT_ADDR` — 本物の `:7700` とのポート衝突回避。
- 資格・設定を汚したくなければ sandbox `HOME` も併用（docs/32 の M1 E2E 方式）。
- 後片付けは**専用ソケットに対してのみ** `tmux -L af-e2e-$$ kill-server` が許される
  （自分だけのサーバだから）。共有デフォルトソケットへの `kill-server` 手打ちは厳禁。
- Go テストも同様に隔離する: tmux を直接叩くテストは `tmux -L <専用>`、製品コード経路
  （`tmuxx.Cmd` 経由）を通すテストは `t.Setenv("AF_TMUX_SOCKET", …)`。従来の contract テスト群は
  「製品コードが素の tmux を叩くため -L を使えない」制約下で共有ソケット上に自前セッションを
  作っていた（`opencode_contract_test.go` 冒頭の注記）が、この制約は `AF_TMUX_SOCKET` で解消済み。
