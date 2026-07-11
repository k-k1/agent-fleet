# 04. Workspace Agent と Workspace イメージ

> 正: コード（本書は地図と設計意図）/ 主な更新トリガ: セッションモデル・kind 追加・イメージ同梱物の変更 / 最終確認: 2026-07

## 4.1 位置づけ

per-user コンテナ内で常駐する Go プロセス。コンテナの PID 1 は `--init`（tini、ゾンビ reap のため）で、
その配下で非特権ユーザーとして動く。CP から見た**唯一の実行主体**——tmux・git・fs・CLI エージェントに
触るのは必ず Agent（CP は中継のみ、[05](05-api-contracts.md)）。全 API は `requireToken`（Bearer
`AGENT_TOKEN`）で保護（[07 §7.5](07-security.md)）。コンテナ netns を共有するため、コンテナ内サービスへ
loopback で届く（preview の下請け `/proxy/{port}`）。

## 4.2 セッションモデル

**1 Session = tmux セッション 1 本**（プレフィクス `claude_`）。決定的 sid = uuidv5(dir, name)。

- **メタ永続**: per-session メタ（kind/dir/model/repo/createdAt/stoppedAt/Archived）を
  `~/.config/agent-fleet/sessions`（denylist 配下・home volume、`AF_SESSIONS_DIR`）に保存。
  **Stop→Start を跨いで一覧と再開が生きる**。停止 TTL は `AF_SESSION_STOPPED_TTL`（既定 7d）で剪定。
- **一覧はメタ駆動 + live tmux マージ**。メタの無い生 `claude_*` tmux（孤児）も列挙し、ペインの起動
  コマンドから kind を sniff——「動いているのに一覧に出ない」手詰まりを封じる。
- **止め方の 3 すくみ + 1**:
  - `halt` = tmux kill・メタ保持 → 停止中として一覧に残る（再開可）
  - `stop` = tmux kill・メタ破棄 → 一覧から消える（会話履歴 jsonl は残る）
  - `archive`/`restore` = メタ+履歴保持で非表示 ↔ stopped 復帰
  - `recreate` = 過去履歴を捨て**同一スロットで新規会話**
- **再開**: claude/opencode は**作業 dir 消失で再開不可**（resumable=false、home フォールバックしない）。
  shell は home フォールバック。claude の resume 判定は jsonl に実会話行（user/assistant）があるか——
  ⚠️ Remote Control 既定 ON では会話前でも `bridge-session` 1 行が書かれるため「jsonl 存在=resume 可」に
  すると `--resume` が即死する。non-resumable なら jsonl を捨てて `--session-id` 新規。
- ⚠️ **tmux の `-t` は前方一致**（exact→prefix→fnmatch）。`claude_foo` が `claude_foo-sh` に一致して
  誤判定・誤 kill しうるため、target 参照は全て `=name` の exact 形式で行うのが本リポジトリの規約。
- **DB ミラー（B 案）**: CP の `GET /api/sessions` は running 時に Agent から取得して DB を洗い替え、
  stopped 時は DB から `alive:false` 配信＝**Workspace 停止中でも一覧が見える**（[06 §6.3](06-data-model.md)）。
- **fork**: claude の会話履歴を引き継いだ新セッションを別スロットに分岐（`POST /sessions/{name}/fork`）。
- **ブランチ支援**: `suggest-branch`（セッション会話を要約して AI がブランチ名を提案）/ `rename-branch`。
- タイトル系（`/title/{suggest,accept,dismiss,regenerate,set}`）は会話からの表示名提案。

## 4.3 エージェント kind 統合パターン

kind = `claude` / `codex` / `opencode` / `shell` / `ssm`（+ 📋 agy、[decisions/0008](../decisions/0008-antigravity-cli-agent-kind.md)）。
**新 kind を足すときに埋める面**は毎回同じ（雛形は opencode 追加時に確立、codex で再利用）:

| 面 | claude | codex | opencode |
|----|--------|-------|----------|
| 起動コマンド構築 | `--session-id`/`--resume` + `--name` ラベル + `--model` | `codex` + `AGENT_CODEX_FLAGS`（bypass 系）+ resume は捕捉した id で `codex resume` | `opencode`（保存 sid あれば `--session <id>`）|
| resume 戦略 | 決定的 sid + jsonl 判定（§4.2）| **独自 sid をフック stdin から捕捉**して保存（ピン留め不可）| プラグインが `session.created` の sid を捕捉して per-slot 保存（`--continue` は同 dir 他スロットを掴むため不使用）|
| 状態通知 | settings.json の hooks（§4.4）| **claude と同型のフック**を起動時 `-c` で注入（per-slot sid をコマンドに埋める）| 同梱プラグインがイベント購読して通知 |
| 認証経路 | `claude auth login --claudeai`（[08 §8.5](08-integrations.md)）| `codex login`（API キー / device flow）| env キーを**コマンド前置**で注入（`secrets.enc` 保存）|
| 資格の置き場 | `CLAUDE_CONFIG_DIR`（home 外退避）| `~/.codex`（CLI 所有）| `secrets.enc`（Agent 所有）|
| fs denylist | `.claude`・`.claude.json` ほか | `~/.codex` | `~/.local/share/opencode` |
| Console 側 | registry に表示・capability（fork/imagePaste/headlessChat 等）| 同 | 同 |

- ⚠️ **env は `tmux new-session -e` ではプロセスに届かない**（セッション環境止まり）。env 注入は
  **コマンド前置**（`NAME='v' … prog`）が本リポジトリの規約。claude は auth login 方式採用後
  前置も廃止（秘密の cmdline 露出を解消）。
- ⚠️ codex のフックは claude と同じ**入れ子スキーマ**（`hooks.<Event>=[{hooks=[{type,command}]}]`）。
  フラットに書くと**パースは通るが無音で発火しない**（resume が新規化する既知の罠）。
- RTK（安全化ラッパー、vendor 時のみ）は 3 エージェントで機構が違う: claude=settings.json の
  PreToolUse/Bash フック（透過）/ opencode=プラグインでコマンド書換（透過）/ codex=AGENTS.md への
  指示ブロック（**ベストエフォート**）。codex/opencode の on/off 実体は artifact の有無で、永続 pref
  `~/.config/agent-fleet/rtk.json` を正として起動時と `GET/PUT /agents/rtk` が pref→artifact を適用。

## 4.4 状態バッジ機構

claude の hooks が `workspace-agent session-status <state> <sid>` を発火し
`~/.config/agent-fleet/session-status/<sid>.json` に記録。状態は
**working**（UserPromptSubmit）/ **idle**（Stop=入力待ち）/ **question**（PreToolUse matcher
`AskUserQuestion`）。hooks はセッション起動毎に加算マージし、PreToolUse は **matcher 単位**で
RTK（`Bash`）と状態（`AskUserQuestion`）が共存できる（トグルが互いを壊さない）。
codex は同型フック（question は出ない）、opencode はプラグイン通知（working/idle のみ）。
Console は 4 秒ポーリングで ● 進行中 / ❓ 質問 / ✓ 入力待ち / 停止中を描画し、idle/question 遷移で
ブラウザ通知。

## 4.5 チャット・アシスタント面（headless CLI）

設計の全容は [docs/19](../history/19-assistant-chat.md)。要点:

- **チャットは tmux セッションではない**。Agent 内の並列サブシステムで、`claude -p`（headless）を
  会話ストア（`~/.config/agent-fleet/chats/<id>.json`）と組で駆動。ストリームは SSE（[05 §5.3](05-api-contracts.md)）。
- **専用 config-dir**: チャットはセッションと `CLAUDE_CONFIG_DIR` を分離（`AF_CHAT_CLAUDE_DIR`）。
  `.credentials.json` だけ共有へ symlink + copy-back で単一化（OAuth refresh のローテートと
  tmp+rename 書込みに耐える設計。詳細は docs/19 Q3）。
- **コンテナ内 stdio MCP**: チャットの claude には `workspace-agent mcp-stdio` を `--mcp-config` で
  付与（PAT 不要・egress 不要・身元=自コンテナ）。既定 read-only、`--write` 時のみ
  `send_to_session`・`list_assistants`・`ask_assistant` を**広告**する（権限プロンプトでなく
  「見えるツール集合」がゲート）。CP の `/mcp` とは**別実装・別スコープ**（意図的な二重管理、
  [03](03-control-plane.md)）。
- **アシスタント**（`/assistants*`）: persona/model/knowledge/tools(af_read|af_write|none) を持つ
  テンプレート。`ask_assistant` は相手ターンを強制 tools=none で 1 ショット実行＝1 ホップで停止・
  副作用なしを構造で担保。
- 向き不向き: チャットは短〜中の翻訳/要約/Q&A。ファイル出力を伴う大規模作業はセッションへ
  （Files の「セッションに送る…」がパス参照で渡す）。

## 4.6 git / fs 面

- **repos**: clone（`GIT_TERMINAL_PROMPT=0` で fail-fast、name は正規表現で traversal 防御）/
  status（porcelain=v2 解析）/ branches / checkout / fetch / ff / delete。
  clone 後 submodule は best-effort（SSH URL を HTTPS へ書換えて update。親 clone には
  `--recurse-submodules` を付けない——SSH 登録 submodule で親ごと失敗するため）。
- **SCM（read/write git）**: changes / diff / log / graph / show / stage / unstage / discard / commit。
  sha は hex 検証、応答はサイズ上限でキャップ。
- **fs**: home ルートのツリー/ファイル/アップロード/リネーム等。traversal 防御・サイズ上限・
  バイナリ判定。**denylist**（一覧非表示 + 直アクセス 400）: `.claude`・`.claude.json`・
  `.config/agent-fleet`・`.ssh`・`.git-credentials`・`~/.local/share/opencode`・`~/.codex`。
- **Git LFS**: image に git-lfs 同梱・system install。clone/checkout で smudge。残ポインタは
  fs が検出してビュアーにバッジ（既存 working copy は手動 `git lfs pull`）。
- git 認証は統一 cred helper が `secrets.enc` を都度復号して出力（[07 §7.6](07-security.md)、
  Bitbucket は refresh 内蔵の専用 helper。[08](08-integrations.md)）。

## 4.7 transcript / usage

- claude の会話 jsonl は**末尾ウィンドウ読み込み + 逆方向ページング**で返す
  （`GET /sessions/{name}/messages`、[decisions/0009](../decisions/0009-transcript-paging.md)）。
- codex / opencode にも各 CLI の保存形式（jsonl / SQLite store）を読む transcript リーダーがあり、
  出力は共通の turn 形に揃える（パーサは統合しない——docs/23 の方針）。
- usage: `GET /claude/usage`・`GET /codex/usage`（各 CLI のローカル記録から集計）。

## 4.8 secrets（Agent 側の責務）

`secrets.enc`（AES-256-GCM・0600）の所有と、`workspace-agent cred` / `workspace-agent bitbucket-cred`
サブコマンドによる**平文ファイルを作らない**資格供給。鍵 `AF_SECRET_KEY` は CP が起動時注入
（封筒暗号の全体像は [07 §7.6](07-security.md)。Agent は暗号 provisioning に無関心）。
起動時に旧平文資格の自動移行あり。`AF_MASTER_KEY` 未設定の dev では平文 `secrets.json`（同一経路）。

## 4.9 Workspace イメージと entrypoint

`workspace/Dockerfile`（multi-stage golang→node:22-slim、約 2.8G）。**イメージと Agent は
全デプロイターゲット共通**（移植の肝、[09](09-deploy.md)）。

- **焼き込み**: claude / opencode / codex（global npm、`ARG CLAUDE_CODE_VERSION` /
  `OPENCODE_VERSION` / `CODEX_VERSION` でピン止め——bump 手順は [10 §10.2](10-development.md)。
  ピンの写しを `/usr/local/share/agent-fleet/versions.json` に書き出し、Agent の
  `GET /env/tool-versions`（設定→環境「ツールのバージョン」: 実効 / 焼き込み / ~/.local
  override / ピン差分の read-only 表示）と e2e-smoke が参照する）、
  rtk（vendor 静的バイナリ、git 管理外——ビルド時にホストから vendor）、
  Go toolchain（`ARG GO_VERSION`、go.mod と歩調）、
  build-essential + python3（+ `break-system-packages`、pip --user は home 永続）、vim・git-lfs・
  jq 等の定番、tzdata。**Java は image 外**: 共有 JVM dir（Temurin 8/21/25）を `/usr/lib/jvm:ro` で
  マウント（イメージ 2.1G→1.0G の削減。⚠️ Temurin の cacerts symlink は抽出時に実体化しないと
  空トラストストアになる）。node は nvm（home・オンデマンド）。
- **レイヤ順の意図**: 重く変わらない RUN（toolchain・npm -g）を前段、頻繁に変わる COPY
  （agent バイナリ・entrypoint・plugin・notes）を最後尾に集約——小修正でキャッシュを壊さない。
- **entrypoint の seed 方針 = 「無い時のみ」**: `settings.json`（skip-permissions / RC /
  通知 / rtk フック）、`~/.gradle/gradle.properties`（メモリ制約ホスト向けの保守的既定）。
  以後は設定 UI が真実（毎起動 force すると UI と喧嘩する）。**毎起動 refresh するもの**:
  opencode plugin・各エージェントの利用ガイド（`workspace-notes.md` 単一ソース→
  claude=`/etc/claude-code/CLAUDE.md`（managed policy・image 焼込）/ codex・opencode=AGENTS.md へ cp）。
  ⚠️ `.dockerignore` は `**/*.md` 除外に `!workspace-notes.md` 例外が必要（`//go:embed` も同様）。
- **タイムゾーン**: toolchains 設定の `timezone`（既定 `Asia/Tokyo`）を entrypoint が `export TZ`。
  反映は Stop→Start。
- claude の自己更新は `~/.local` 側のみ・焼き込み版は固定。壊れた symlink（旧 home パス）は
  entrypoint が検出して repair。
- 反映ルール: image / entrypoint に触れたら **image 再ビルド + 利用者の Stop→Start**（[10](10-development.md)）。
