---
audience: "Workspace Agent、またはエージェント種別を変える人"
source_of_truth: "コード（本書は地図と設計意図）"
updated: "2026-09"
---

# 04. Workspace Agent と Workspace イメージ

[English](04-agent.md) | 日本語

## 4.1 位置づけ

各 Workspace の中で常駐する Go プロセス。非特権ユーザー（`USER dev`）として、ゾンビを回収する init の
配下で動く（docker は `--init`、ECS 系は `InitProcessEnabled`。`native` はコンテナが無いので init も無い）。
**CP から見た唯一の実行主体**で、runtime・tmux・作業コピー・ファイルシステム・CLI エージェントに触るのは
必ず Agent。CP は中継するだけ（[05](05-api.ja.md)）。

- `GET /healthz` を除く全エンドポイントは `httpx.RequireToken`（`AGENT_TOKEN` との Bearer 照合）の
  内側にある（[07 §7.5](07-security.ja.md)）。`AGENT_TOKEN` が未設定だと照合は無効になる。これは
  手元の開発専用。
- Workspace のネットワーク名前空間を共有しているので、Workspace 内のサービスへ loopback で届く。
  使っているのはプレビューの中継（`/proxy/{port}/…`、`handlePreview`）と BrowserManager の
  ナビゲーション（§4.10）。

## 4.2 セッションモデル

**1 セッション = 会話・作業ディレクトリ・設定・実行状態を束ねる論理スロット**。`kind` はエージェント種別、
`driver` は制御経路。

- **tmux セッションを持つのは `driver=tui` だけ**。名前は kind を問わず `claude_<name>`
  （`session.TmuxName`）。
- `driver=managed` のセッションはペインを持たない。どのプロセスで動くかは kind の managed driver
  次第（§4.3）で、Workspace で共有するデーモン、セッションごとの子プロセス、Agent 内のコードの
  いずれか。
- 内部 id は決定的で、ディレクトリと名前から `session.UUID` で作る。**エージェント自身の会話 id は
  別に保存する。**
- **名前はサーバーが割り当てる**（`allocSessionName`）。作成要求の名前は無視される。二重作成を
  避けたい呼び出し側は `idempotency_key` を送り、`GET /sessions-idempotency/{key}` で引ける。

### メタデータと一覧

- **メタデータは `~/.local/state/agent-fleet/sessions` に保存する**（`session.MetaDir`。
  `AF_SESSIONS_DIR` で上書き可）。home ボリューム上にあり、ファイルブラウザからは隠れている。
  だから **Stop → Start を跨いで一覧と再開が生きる**。
- **停止したセッションは TTL を過ぎると自動でアーカイブへ移る。消しはしない**
  （[decisions/0097](../decisions/0097-session-retention.ja.md)）。
  - TTL は利用者の設定（`sessionStoppedArchiveDays`。「しない」も選べる）、無ければ
    `AF_SESSION_STOPPED_TTL`、無ければ 7 日（`session.StoppedTTL`）。
  - ロックされたセッションは対象外。
  - 掃引は一覧ハンドラの中で走る。タイマーは無い。
- **セッションには予算を付けられる**（`Meta.SpendCapUSD`、#1054）。報告リコンサイラの tick が、
  予算付きで動いているセッションの会話記録を値付けし（`usage_spend.go`、10 秒キャッシュ）、推定が
  予算に達したら stop-after-turn を arm し、2 倍（`SpendCapHardFactor`）で即座に止める。起動時に
  指定が無いときの既定は ui-prefs の `sessionSpendCapUsd`
  （[ADR 0029 追記](../decisions/0029-usage-accounting.ja.md)）。
- **一覧はメタデータ駆動で、driver ごとの生存状態を重ねる**（`HandleListSessions`）。managed は
  runtime のハンドル、tui は tmux を見る。
  - メタデータの無い `claude_*` の tmux セッション（孤児）も列挙する。kind はペインの起動コマンドから
    推定する（`tmuxx.PaneKind`。分かるのは claude・codex・opencode で、それ以外は `shell` 扱い）。
  - 「動いているのに一覧に出ない」手詰まりを意図して封じている。
- **CP は一覧を DB へミラーする**（`sessionsPayload`、`store.ReplaceSessions`）。Workspace が停止中か
  Agent に届かないときは、CP がミラーを `alive:false` で返す（[06 §6.3](06-data.ja.md)）。

### 止める・消す・戻す

| 操作 | エンドポイント | 効果 |
|---|---|---|
| halt | `POST /sessions/{name}/halt` | driver を止め、メタデータは残す。停止中として一覧に残り、再開できる |
| 削除 | `DELETE /sessions/{name}[?stop=1]`、または旧名の `POST /sessions/{name}/stop` | セッションを**ごみ箱へ移す**（`trashSession`、[decisions/0101](../decisions/0101-session-delete-via-trash.ja.md)）。下記 |
| archive / restore | `POST /sessions/{name}/archive`・`…/restore` | 行を隠す／停止中として戻す |
| recreate | `POST /sessions/{name}/recreate` | 旧スロットをアーカイブし、同じディレクトリ・kind・driver・設定で新しい会話を始める。起動に失敗したら旧スロットを戻す |
| lock | `POST /sessions/{name}/lock` | 削除と TTL アーカイブを断る（403）。archive はできる |

- **ごみ箱**はメタデータと転写のアーカイブを書き、それからメタデータを消す。
  - 動いているセッションは `?stop=1`（と `/stop`）なら先に halt する。付けないと
    `409 session_running` で断る。
  - ロック中は断る（403）。移している最中に再開したものは `session_resumed` で、何も消さない。
  - `POST /cleanup/archives/{id}/restore` でごみ箱から戻す。
  - **削除は作業コピーに触らない**。だから戻したセッションが消えたフォルダへ戻ることはない。
- **セッションを畳む経路はどれも、持ち越し中の作業を先に昇格させる**（`PromoteCarriedFor`。
  kill やハンドル破棄より前）。そういう経路を新しく足すときも同じにすること。

### 再開

- `POST /sessions/{name}/start` は、停止中のセッションを接続せずに再開する。両 driver 共通。
- managed のセッションは driver の `Resume` で開き直す。やり方はプロセスモデルに従う。
  - codex と opencode は、native の id を共有デーモンで resume する。
  - copilot・cursor・kiro・muse は、セッションごとの子を起動し、スロットに記録した会話を開き直す。
  - lcpp は、Agent の中でスロット自身のストアを開く。

  Agent の起動時には、各 kind の `ReconcileManaged` が生きているハンドルを組み直す。
- **エージェント種別は、作業ディレクトリが消えていると再開できない**。home へはフォールバックしない。
  `shell` はフォールバックし、`ssm` は常に home で始まる。
- ⚠️ **claude が再開できるのは、JSONL に本物の user か assistant の行があるときだけ**
  （`claude.JSONLResumable`）。Remote Control が ON だと、**会話の前に `bridge-session` の行が 1 行
  書かれる**。だから「ファイルがある＝再開できる」とすると `--resume` が即死する。再開できない
  ファイルは捨てて、`--session-id` で新しく始める。
  - 関連: entrypoint は新規 Workspace に **Remote Control OFF**（`remoteControlAtStartup: false`）を
    置く。既存の設定ファイルにキーが無ければ一度だけ補う。**利用者が明示した値はそのまま尊重する**。
- ⚠️ **claude は自分で起動し直すことがあり、そのとき session id を落とす。**
  フルスクリーン TUI への切替、サインイン後の再起動、モデル切替などで起きる。再起動の argv は
  **設定系フラグだけ**から組み直されるので、構造上 `--session-id` も `--name` も入らない
  （2.1.239 で実測: 起動コマンドには両方あったのに、生きているプロセスにはどちらも無かった）。
  - **id を失った claude は、ランダムな id でまっさらな会話を始める**。決定的な id の転写は二度と
    現れない。
  - 手当てが無いと、ミラーは「まだ会話はありません」のまま固まり、状態も別の id で書かれる。
    **Console からセッションが丸ごと消え**、使用量・中断検知・報告も一緒に落ちる。
  - 対処は `claude-sid` **台帳**（`internal/agents/claude/sid.go`、`agents.SidStore`）で、
    スロットを claude の実 id へ対応づける。hook が名乗ったときに `AF_SESSION_NAME` を手掛かりに
    記録する。
  - **この変数は tmux セッションの環境にあるので、再起動を跨いで残る**。作業ディレクトリの一致の
    ような当て推量ではなくこれを選んだのはそのため。転写の場所も `--resume` の相手も `LiveSID()` を
    通して決める。

### 会話 id: 捕捉型と押し付け型

会話 id の持ち方は 2 系統あり、壊れ方が違う。

- **捕捉型**: CLI が採番した id を**イベントのたびに**記録し直す。CLI が別のセッションへ移っても、
  次のイベントで追従する。
  - codex（hook）、opencode（plugin）、agy と kiro（ディスク探索）。
  - 実測でドリフト 0（codex 58/58、opencode 16/16）。
- **押し付け型**: 我々が採番した id を渡し、以後はそれが使われている前提ですべてを引く。
  **CLI がその id を使わなくなった瞬間に、静かに壊れる**（上の claude の例）。
  - claude と copilot（`--session-id`）、cursor（`--resume`）。
- muse は開始時は押し付け型。AF が UUIDv7 を採番し、MSP の `session/start` がそれをそのまま採用する。
  ただし AF が記録するのはその応答が返した id とパスで（`internal/agents/muse` の `museSession`）、
  保存される id は前提ではなくホストの答えである。それを持つストアのキーはスロット。
- lcpp はどちらでもない。ストアのキーがスロットそのもの。

**id を押し付ける kind を足すときは、回収経路も必ず一緒に出すこと。**

- claude には上の hook 由来の台帳がある。状態 hook を持たない copilot と cursor はディスクから拾い
  直す（`ResolveImposedSID`、`internal/agents/imposedsid.go`）。
- 回収が走るのは、押し付けた id が CLI 側のどこにも無いときだけ。
- 候補を採るのは、ディレクトリが一致し、スロットより後に作られ、他のスロットが取っていない候補が
  **ちょうど 1 つ**のときだけ。**曖昧なら何もしない**。他人の会話を映すのは、固まったままより悪い。
- 手掛かりは、copilot が `session-state/<id>/workspace.yaml` の `cwd` と `created_at`。cursor は
  `projects/<cwd のスラグ>/agent-transcripts/<chatId>/` で、作成時刻はそのディレクトリの mtime
  （追記では動かない。実測）。

### その他のセッション操作

- ⚠️ **tmux のターゲット指定は前方一致**。`claude_foo` が `claude_foo-sh` に一致し、取り違えや
  誤 kill が起きうる。**このリポジトリのターゲット参照はすべて完全一致形**（`session.ExactTarget`、
  `=name`）。`capture-pane` や `send-keys` のようなペイン単位のコマンドは先にペイン id を引く
  （`tmuxx.SessionPaneID`）。`=name` ではペインを指せないため。
- **fork**（`POST /sessions/{name}/fork`）は会話を引き継いだ新しいスロットを分岐させる。kind と driver
  は元と同じ。
  - 任意のボディ `{"at": <anchorId>, "include": bool}` を付けると、**過去の発言の時点**から分岐する。
    アンカーは `transcript.Turn.AnchorID`（kind ごとの不透明な id）で、できるかどうかは各 kind の
    `ForkAtResolver` が答える（`agents.ErrForkAtRoute` → `fork_at_unsupported`）。
  - 実現手段は kind で割れる。
    - codex と opencode は runtime の公式パラメータを使うので、managed が要る。
    - muse は runtime の切り取り点を使う。
    - claude と copilot は転写の写しを切り詰める。TUI でも動く。
    - lcpp は自分のストアを切り詰める。
  - どの kind が fork できるかは [ref/agents](../../guide/ref/agents.ja.md)。
- **driver の切替**（`POST /sessions/{name}/driver`）は、同じ会話を止めてもう一方の driver で再開する。
  両方の driver を持つ kind が対象。ターンの実行中は断る（`409 busy_switch`）。kind・ディレクトリ・
  native の id は保つ。
  - codex を managed から Terminal へ移すのは停止したセッションだけ（`409 codex_stop_first`）。直接起動の
    codex TUI は共有 app-server が読み込んでいるスレッドを開けず、サーバーは最後の購読者が離れて約 70 秒後に
    スレッドを下ろす。そこで `DropHandle` は writer に加えて読み取り専用のオブザーバにもスレッドを手放させ、
    まだ読み込まれているスレッドを resume する Terminal 起動は `409 codex_releasing` で断る
    （`codex/release.go`。`thread/loaded/list` を 1 回読む）。
- **作成時のモデル解決**（`resolveLiveModel`。codex・copilot・opencode・lcpp）は、指定されたモデルを
  live のカタログに照らし、ピッカーの表示名や一意な略称を完全な識別子へ展開する。
  - **曖昧か使えないモデルは、clone や worktree の前に `400 bad_model` で断る**。「起動してから無効な
    モデルで落ちる」罠を封じるため。lcpp は空のモデルも断る。
  - 利用者が隠したモデルは、kind を問わず断る（`model_hidden`）。
  - カタログを読めないとき（オフライン、CLI 未導入）は指定値をそのまま使い、起動を続ける。
  - ⚠️ Free プランの copilot は**カタログが無く auto だけ**で、**auto は `--effort` を受け付けない**。
    だから起動コードは**具体的なモデルのときだけ**このフラグを渡す。auto に付けると起動に失敗し、
    Free の利用者が毎回踏む。Console の `useEffortOptions` もその場合は既定だけを出す。
- **タイトルとブランチ**: `POST /sessions/{name}/title/{suggest,accept,dismiss,set}` は会話から表示名を
  提案し、設定する（作り直しも `suggest`）。`suggest-branch` は会話からブランチ名を提案し、
  `rename-branch` がそれを適用する。

## 4.3 エージェント種別を統合する型

kind は `internal/session/session.go` の `Kind*` 定数で、claude・codex・cursor・opencode・agy・copilot・
kiro・lcpp・muse と、shell・ssm。どれが Managed・ターミナル（CLI）・両方に対応するかは
[ref/agents](../../guide/ref/agents.ja.md)。**足し方は [20 エージェント種別の追加](20-add-an-agent.ja.md)**。
この節は型の説明。

### managed driver と既定の出どころ

managed driver は `managedDrivers`（`internal/sessionx/session_turn.go`）に登録され、それぞれ
`Capabilities.ProcessModel`（`internal/agents/<kind>/driver.go`）でプロセスモデルを宣言する。

| `ProcessModel` | kind | 何が動くか |
|---|---|---|
| `shared-daemon` | codex、opencode | Workspace に 1 つのデーモン（codex の app server、`opencode serve`）。セッションはその上のスレッド |
| `per-session-child` | copilot、cursor、kiro、muse | セッションごとに子プロセス 1 つ。copilot・cursor・kiro は ACP（`acp.go`）、muse は MSP（`internal/msp`） |
| `in-process` | lcpp | Agent 内のコード。子プロセスは無い |

- **Agent 自身は、driver 未指定を `tui` にする**。例外は `Caps().ManagedOnly` の kind（lcpp、muse）で、
  こちらは `managed` になる（`HandleCreateSession`）。
- **利用者が見る「既定は Managed」は呼び出し側が決めている**。Console の起動 UI は、registry の項目が
  `managedDriver: true` の kind（`console/src/agents/registry.ts`）を managed で起動する。コンテナ内
  MCP の `create_session` は codex・opencode・copilot・cursor・kiro に `managed` を送る（`mcpStdioCall`）。
  CP 側の呼び出し元も同じで、CP の MCP の `create_session`（`control-plane/internal/mcpsrv/mcp.go`）と
  スケジューラの `injectDriver`（`control-plane/scheduler_wake.go`）がこれらに `managed` を送る。
  driver 無しの素の `POST /sessions` は `tui` になる。
- 両方の driver を持つ kind を足すときは、4 つの呼び出し側すべてに足すこと。

### 新しい kind が埋める面

**kind は対応する driver の面を埋める**。面は毎回同じ。どれもコード上の契約で、以下では claude・
codex・opencode を実例に使う。
どの kind がどの driver に対応するか、利用者が各 kind にどうサインインするかは
[ref/agents](../../guide/ref/agents.ja.md)（サインインは
[サインインの仕方](../../guide/ref/agents.ja.md#サインインの仕方)の節）。サインインのフローは
[08](08-integrations.ja.md)。

- **tmux での起動と id の持ち方**（Terminal の経路を持つ kind）: kind の `BuildLaunch` が
  `agents.LaunchPlan` を返す。id を捕捉するか押し付けるかを先に決めること（§4.2）。managed 専用の kind
  （`Caps().ManagedOnly`: lcpp、muse）にこの面は無く、`BuildLaunch` は常に `ErrNoTerminalRoute` で
  断る。
  - claude は押し付ける。新しいスロットは `--session-id`、既存のスロットは `LiveSID()` を通した
    `--resume`。ほかに `--name`・`--model`・`--fork-session`。
  - codex は捕捉する。`codex resume <id>` か `codex fork <id>` を直接起動し、共有の app server は
    経由しない。id は hook が記録し直す。
  - opencode は捕捉する。`opencode --session <id>` で、id は plugin が記録し直す。
- **managed での起動**: `managedDrivers` に登録した `Driver`。何を動かすかは `ProcessModel`（上の表）
  に従う。
  - codex: 共有 app server の `thread/start` / `thread/resume`
  - opencode: 共有サーバーの v1 session API とイベントストリーム
  - lcpp は外部の API を呼ばない。`Resume` が Agent の中でハンドルを作り、自分のストアを開く。
- **会話の正本と転写の読み元**: どちらも kind ごとに決め、読み元には kind 自前の転写リーダーを
  付ける（§4.7）。
  - claude・codex・opencode: 両 driver とも、CLI の native のストアが正本で読み元でもある。
    claude の JSONL、codex の rollout JSONL、opencode の SQLite（`message` / `part`）。
  - lcpp: 自分のストアが正本で読み元でもある。会話の写しはほかに無い。
  - muse: **会話の正本は muse のホスト**。転写は、live の item ストリームから我々のストアへ書いた
    **表示用のミラー**から読む（`muse/transcript.go`）。ミラーを置くのは、転写がローカルのディスク
    読みでなければならず、ホストを起動してはいけないため。ホストのディスク上のログは内部の
    runtime 形式で、安定の約束も無い。ミラーの書き込みに失敗しても、セッションは止めない。
    Agent が見ていない間に走ったターン（ターンの途中で Agent が落ちた場合）はホストには残るが、
    ミラーには無い。ホストの `session/read` から埋めるのは #1197。
- **live 状態**は kind が出すものを状態ストアへ正規化する（§4.4）。
  - claude: hook と tmux のプローブ
  - codex: managed では runtime のイベント。TUI では working / idle を hook で、取りこぼしたターン
    終了と保留中の質問を rollout で拾う。
  - opencode: managed ではサーバーのイベント、TUI では plugin
- **資格情報の置き場**は、kind が状態を書くほかの場所と合わせて、ファイルシステムの denylist
  （`fsDeny`、§4.6）に入れること。
  - claude: `CLAUDE_CONFIG_DIR`（閲覧できる home の外へ退避）
  - codex: `~/.codex`
  - opencode: プロバイダキーは暗号化ストアに置き、opencode 自身の OAuth サインインは
    `~/.local/share/opencode` の下。キーは TUI のセッションへは `LaunchPlan.Env` で届く。共有の
    `opencode serve` はプロセスの環境（`cmd.Env`）として受け取り、起動時に一度だけ読む。だから
    キーの変更はデーモンの再起動を待つ。再起動は Console が勧める（設定 → エージェント、
    `POST /connections/opencode/serve/restart`）。実行中のターンを drain し、タイムアウト時点で
    まだ動いているものは打ち切るので、Agent が自分の判断で行うことはない。

- ⚠️ **環境変数は `tmux new-session -e` でプロセスへ届ける**（`agents.LaunchPlan.Env`、適用は
  `startSessionTmux`）。**秘密をコマンドの前置にしてはいけない**。前置は `/proc/*/cmdline` と tmux の
  `pane_start_command` に載り、Workspace の中の何からでも読める。前置にするのは秘密でない
  ツールチェーンの export（`toolchainShellPrefix`: `JAVA_HOME`・node・`TZ`）だけ。
- ⚠️ **子プロセスは必ず回収する。** Agent は PID 1 ではないので誰も回収してくれず、Wait されない子は
  `<defunct>` のまま PID を永久に漏らす。
  - `Run`・`Output`・`CombinedOutput` は中で Wait するので安全。
  - **自分で起動する（`cmd.Start`、`pty.Start`）なら、失敗を含むすべての経路で Wait に到達させること。**
  - 漏れやすいのは「起動タイムアウトで kill して return」する経路で、codex と opencode のデーモンで
    実際に起きた。
  - ログインフロー共通の `agents.Flow.Close()` は kill も Wait もする（`internal/agents/flow_test.go`）。
- ⚠️ **codex の hook は claude と同じ入れ子のスキーマ**（`hooks.<Event>=[{hooks=[{type,command}]}]`）。
  平らに書くと**パースは通るが黙って発火しない**。その結果 resume が新しい会話を始めてしまう。
- **rtk（トークン節約のプロキシ）の配線は kind ごとに違う**（`agent_rtk.go`）。イメージに rtk が
  あるときだけ。
  - claude: `PreToolUse`/`Bash` の hook
  - opencode: コマンドを書き換える plugin
  - copilot: `preToolUse` の hook
  - codex と agy: **`AGENTS.md` の指示ブロックで、ベストエフォートでしかない**

  オン・オフはその成果物の有無で表す。codex・opencode・agy・copilot は永続設定（`rtk.json`、
  `GET/PUT /agents/rtk`）が正で、起動時に適用し直す。claude は設定ファイルの hook そのもの。

### managed の境界

- 境界は `internal/agents/driver.go` の `Driver`・`ThreadHandle`・`Capabilities` 型。
- `POST /sessions/{name}/turn` は driver に依存しない。managed は構造化 API へ、TUI はキー入力の経路へ
  振り分ける。
- `/respond` と `/settings` は managed だけ。TUI のセッションには
  `501 respond_unsupported` / `settings_unsupported` を返す。
- **CLI が読める native のストアを持つなら、会話の本文を独自のストアへ複製しない。** native の
  ストアが読みの正本のままで、転写は出口で正規化する
  （[decisions/0015](../decisions/0015-agent-managed-driver.ja.md)）。例外は上に書いた lcpp（ストアが
  会話そのもの）と muse（ストアはホストの会話の表示用ミラー）
  （[decisions/0093](../decisions/0093-lcpp-agent-kind.ja.md)、
  [0095](../decisions/0095-muse-agent-kind.ja.md)）。

## 4.4 状態バッジ

- 保存される状態（`internal/status`）は **working / idle / question**。question を細かくした `plan`
  （承認待ちの計画）と `permission`（承認待ちのツール）もある。
- ターンの終わりには遷移ラベルも付く。通知には使うが、保存される状態ではない: `failed`・`aborted`・
  `blocked`・`auth`・`limited`・`spend_limit`（`internal/agents/notify.go`）。
- 保存先はセッションごとに 1 ファイルで、`~/.local/state/agent-fleet/session-status/` の下。

**hook は `session-status` サブコマンドを呼ぶ**（`RunSessionStatusHook`）。

- claude は状態を渡し、id は hook の stdin から読む。
- opencode の plugin は状態と id を渡す。
- codex は状態・id・`codex` を渡す。

**claude の hook**:

- `UserPromptSubmit` → working
- `Stop` → idle
- `PreToolUse` の matcher `AskUserQuestion` → question、`ExitPlanMode` → plan
- `permission_prompt` の通知 → permission
- `MessageDisplay` → `message`（状態は変えない）。流れてくる返答を行ごとに記録する。保留中の質問カードが質問の上に出す文章と、
  ターンの実行中に `/messages?live=1` が返す「書いている途中の返答」の元になる（`status/livetext.go`、#1250）。

**hook は加算でマージする**。起動時と、claude を起動する直前に行う（`EnsureStatusHooks`）。
**`PreToolUse` は matcher 単位で登録する**ので、rtk の hook（`Bash`）と状態の hook が共存し、
**片方を切り替えてももう片方を壊さない**。

managed driver はどれも runtime のイベントから同じストアへ書く。Console は 4 秒ごとにポーリングして
バッジを描き、人の手が要る遷移（working → idle、question への遷移）でブラウザ通知を出す。

### 端末通知（OSC 9 / 99 / 777）

プログラムは OSC 9（iTerm2）・OSC 99（kitty）・OSC 777 `notify`（rxvt / Ghostty）で端末にデスクトップ
通知を頼める。**そのバイト列を見られるのは `pipe-pane` の記録係（`record-terminal`）だけ**。接続用の
WebSocket が運ぶのは tmux の再描画で、tmux はこのシーケンスを飲み込む。記録係はストリームを走査し
（`internal/oscnotify`）、送信箱に `terminal-notification` イベントを置く（`sessionx.TerminalNotifier`）。
tmux 3.5a で実測: 素の OSC も tmux のパススルー包み（`ESC P tmux; … ESC \`）も、`pipe-pane` へバイト
単位でそのまま届く。

- **通知ではないもの:** `OSC 9;<数字>…` は ConEmu の制御群（`9;4` は agy と opencode が毎ターン出す
  進捗バー、`9;9` はシェルの cwd）。kitty の `p=?` は機能の問い合わせ。
- **hook で状態を出す kind では捨てる**（claude・codex・opencode。`terminalNotifyHasHooks`）。
  回答完了・質問・承認はすでに hook が送信箱に入れているので、OSC の通知は同じ瞬間を二度知らせる
  ことになる。cmux も同じ規則で、claude の `preferredNotifChannel` を `notifications_disabled` に
  している。
- **セッションごとに間引く**: 30 秒以内の同じ文面は捨て、1 分に 5 件まで。
- 解釈するのは 7 ビットの導入子と終端子だけ。0x9c/0x9d は日本語の UTF-8 の継続バイトで、C1 制御
  ではない。

各 kind が出しうるもの（バイナリを読んだ結果。2026-09-27。実機での捕捉はしていない）:

| kind（版） | 出すもの | 備考 |
|---|---|---|
| claude 2.1.283 | 既定では何も出さない | `preferredNotifChannel=auto` は端末から方式を選び、tmux の下では何も見つけない。`iterm2` / `kitty` / `ghostty` を選ぶと OSC 9 / 99 / 777 を出し、`$TMUX` があれば tmux のパススルーで包む。こちらでは変えない（hook が正）。 |
| codex 0.157.1 | 既定では何も出さない | `tui.notifications` と `notification_method = osc9 \| bel`。有効にしない（hook が正）。 |
| opencode | OSC 9;4 の進捗。OSC 99 は `p=?` の問い合わせの後ろにある | tmux は問い合わせに答えない。 |
| agy | OSC 9;4 の進捗だけ | 入力待ちの間は通知しない（`agents/agy/pending.go`）。 |
| copilot、cursor、kiro | 見つからない | — |
| shell | 利用者が動かすもの次第 | 一番の受益者。 |

**claude の `PushNotification` ツールは hook から受ける。** このツール（2.1.286。
`tengu_kairos_push_notifications` フラグの裏で既定はオフ）は手元の通知を `preferredNotifChannel`
経由でしか出さず（＝OSC。上のとおり捨てる）、claude の Notification hook もこれには発火しない。
そこで `EnsureStatusHooks` が matcher `PushNotification` の `PostToolUse` に
`workspace-agent session-push-notification` を足し、`tool_input.message` を
`proto: "claude-push"` の `terminal-notification` として送信箱に入れる
（`sessionx.recordPushNotification`）。cmux と同じやり方。

- `tool_response.disabledReason` が `user_present` か `config_off` なら**送らない**。claude 自身が
  何も通知せずに戻る場合だから。`no_transport` は*モバイル*プッシュが無いだけで手元の通知は出て
  いるので、転送する。
- **一度だけ届く。** OSC 経路は claude を捨てるので二経路が両方届けることはない。1 回の呼び出しで
  hook が二度発火しても（別プロセスで並行しても）、`tool_use_id` を鍵にした `notice.PutOnce` が
  吸収する（確認・Put・印の書き込みをファイルロックの下で行い、印は Put の後に書く。途中で
  殺されたプロセスは再送の余地を残し、通知を失わせない）。
- **`session-status` の状態ではなく専用のサブコマンド。** `settings.json` が古い Agent を指すことが
  ある（`paths.ConfigExePath` はインストール済みのバイナリを選ぶ）。古い `session-status` は知らない語も
  そのまま状態として書く（実測: `state:"push"`）。古いディスパッチャは知らないサブコマンドを
  exit 2 で断り、何も書かない。
- **想定外のペイロードでは何もしない**（文面が無ければイベントも無い）。形（`tool_input`
  `{message, status}`、`tool_response` `{message, pushSent, localSent, disabledReason, sentAt}`）は
  バイナリから読んだもので、実際の呼び出しを捕捉したものではない。
- セッションの状態は変えない。同じツールにも、ほかのツールと同様に `PostToolUse` の心拍が発火する。

## 4.5 チャットとアシスタント（headless CLI）

- **チャットは tmux セッションではない。** Agent 内の並列サブシステムで、CLI を headless で自前の
  会話ストア（`~/.config/agent-fleet/chats/<id>.json`）と組にして動かし、SSE で流す
  （`POST /chat/conversations/{id}/stream`、[05](05-api.ja.md)）。バックエンドは `chatx.ChatProviders`
  で、claude・codex・opencode・agy・cursor・lcpp・muse。
- **claude の資格情報は、対話セッションと同じ 1 ファイル**（`CLAUDE_CONFIG_DIR`）。
  - 以前の symlink と書き戻しの方式は廃止した。refresh は一時ファイルと rename で書くので、**リンクが
    実ファイルに化け**、2 つのプロセスが別々の refresh token を持ちえた。
  - 利用者とプロジェクトの設定は `--setting-sources ""` で、ほかの MCP の項目はサーバーを付けるとき
    `--strict-mcp-config` で締め出す。
  - 旧専用 config ディレクトリの転写は、一度だけ作成のみで移す（`migrateLegacyChatClaudeProjects`）。
- **フォールバックは見える。** 会話の `agent` は希望した値。各メッセージの `agent` と会話の
  `active_agent` には**実際に動いたバックエンド**を記録する。ストリームは最初に `agent` フレームを
  送るので、UI は切替に即座に追従し、嘘をつかない。
- **コンテナ内の stdio MCP サーバー**（`workspace-agent mcp-stdio`）をチャットに付ける。トークンも
  egress も要らず、身元は Workspace そのもの。
  - claude には `--mcp-config`、codex には `-c mcp_servers.af.*`、opencode には `OPENCODE_CONFIG`、
    agy にはサーバー一覧で渡す。cursor・lcpp・muse のチャットには付かない。
  - **既定は読み取りだけ**（`mcpStdioTools`）。`--write` で書き込み系（`mcpStdioWriteTools`）が加わる。
    セッションの起動・操作・停止・削除、メモ、スケジュール、ブラウザ操作、掃除など。
  - **ゲートは見えるツールの集合で、権限プロンプトではない。** CP の MCP エンドポイントとは、意図して
    実装もスコープも分けている。
- **対話 CLI には、もっと狭い 2 本目のサーバーを置く**。`mcpreg` の組み込み `af` で、
  `mcp-stdio --self-report --chromium-attach` を起動する。
  - 自己報告のツール（`af_report`・`af_stop_after_turn`・`propose_session_handoff`）は常に広告する。
  - 小さな観測用のツール（セッションの状態と使用量、メモ）も常に広告する。`branch_name` も常に
    広告し、ブランチ名リゾルバー（`POST /repos/{name}/branch-name`）に、既定では呼び出し元自身の
    作業コピーについて尋ねる。`memory_*` の 5 本（ADR 0108）も常に広告する。呼び出し元のセッション名を
    付けて Agent のループバック専用ルート `/agents/memory/entries` を呼び、その名前が記録する書き手と
    プロジェクトの範囲を決める。Chromium アタッチの 7 本は `--chromium-attach` で付く。
  - 利用者の設定で `--peer-messaging`・`--image-gen`・`--fleet-spawn`・`--session-search` が加わる
    （`builtinRunArgsFor`）。最後のものは既定でオンで、`search_sessions` を広告する（ADR 0110）。
  - 広告していないツールは、呼ばれても断る（`mcpAdvertised`）。
- **codex の無人承認**: headless のチャットには承認 UI が無い。`-a never` に加えて、付けた MCP
  サーバーを `default_tools_approval_mode="approve"` にする。無いと呼び出しがすべて取り消される。
  **読み取り専用のサンドボックス（`-s read-only`）は保つ**ので、MCP は動くがシェルやファイルの変更は
  できない。
- **アシスタント**（`/assistants*`）は、persona・モデル・知識・ツールの範囲（`af_read`・`af_write`・
  `none`）を持つテンプレート。問い合わせ（`ask_assistant`）は**ツールを強制的に切った** 1 ターンで
  動く。1 ホップで副作用なし、を指示でなく構造で保証する。
- **モデル解決**（`ResolveChatModel`）は作成時にモデルを会話へスナップショットし、以後書き換えない。
  プロバイダが既定を変えても再現性を守るため。
  - 順序は、明示のモデル、利用者のエージェント別の行（ui-prefs の `assistantModels`）、live の
    カタログから選ぶ推奨モデル（`recommendedAssistantModel`）。
  - ただし**会話が持つモデルは 1 本で、作成時のエージェント向け**。実際に別のバックエンドが動くとき
    （フォールバック、途中切替）は、`chatModelFor` が*その* CLI の設定から選び直す。**保存値を
    そのまま渡すと、他社のモデル id を食わせることになる。**
- **会話の途中でのエージェント切替**は `PATCH /chat/conversations/{id}` の `agent`（`title` も受ける）。
  ターンの実行中は断り（409）、headless チャットの無い kind も断る（400）。
  - ピン留めとモデルを差し替え、お知らせを 1 行足すだけ。**バックエンドごとの resume ハンドルと
    メッセージカーソルは残す**ので、戻せば native のセッションが続きから使える。
  - 相手のバックエンドが見ていない履歴は次の送信で再生する（`syncProviderPrompt`）。フォールバックと
    同じ経路。

### prompt をどの言語で書くか

決めるのは読み手です（[decisions/0033](../decisions/0033-stored-text-locale.ja.md)）。
**生成物を人が読む** prompt だけを表示言語で分岐します。

| prompt の性質 | 扱い |
|---|---|
| 生成物を利用者が読む（回答・要約・タイトル・返信候補・報告） | ロケールで分岐し、両言語をそれぞれの言語で書く（persona と指示文の関数は `lang` を受ける） |
| モデルだけが読む（ツールの説明・内部の判定指示） | そのまま。モデルはどちらの言語でも解する。2 本目を同期し続ける手間のほうが高くつく |
| 表示と指示を兼ねる（報告カードの本文） | 先に分離する。丸ごと訳すとオペレーターへの指示まで変わる |

- **生成物の言語は別の軸です。** 分岐で変わるのは指示文で、生成物を何語で書くかではありません。
  返信候補は会話の言語で書きます——候補はそのままそのセッションへ送られるので、言語を変えると
  相手セッションの言語まで反転します。要約と計画は会話の主要言語を保ち、チャットブリッジは
  Console ではなく接続の通知言語に従います。これらを一律に表示言語へ倒すと、元の不具合を
  逆向きに作ります。
- **オペレーターの persona は機械翻訳しません。** プロンプトインジェクション対策の条項を含むので、
  誤訳はそのまま防御の穴になります。両言語で段落の数と順序をそろえ、
  `TestOperatorPersonaInjectionGuardParity` が防御条項を日英のペアで固定しています。
- **Console の語をそのまま使います。** 計画の英語見出しはチャット入力のプレースホルダ
  （`chat.plan.placeholder`）と同じです。食い違うと、計画を更新するたびに見出しが入れ替わります。
- **新しく分岐した prompt は `workspace/agent/prompt_lang_test.go` に 1 行足します。** 英語側に
  ひらがな・全角カタカナ・CJK 統合漢字・和文の約物・全角の英数字や記号が 1 文字でもあれば
  落ちる検査で、つい書いてしまう `・` や全角括弧も拾います。足し忘れると、英語の Console に
  だけ日本語が残ります。

## 4.6 git とファイルシステム

- **リポジトリ**: clone は `GIT_TERMINAL_PROMPT=0` で即座に失敗させ、名前はパターンで検査する。ほかに
  status（`git status --porcelain=v2`）・branches・checkout・fetch・fast-forward・delete。
  - clone 後の submodule はベストエフォートで、SSH の URL を HTTPS へ書き換える。
  - **親の clone は意図して再帰しない**。SSH で登録された submodule があると clone ごと失敗するため。
- **submodule の同期**（`internal/gitx/git_submodule.go`）は、worktree 自身の git ディレクトリ
  （`.git/worktrees/<wt>/modules/…`）へ取得する。親とは別のクローンになる。
  - **実測（git 2.39）: 取得中の submodule update を kill すると、submodule は恒久的に固まる。**
    git ディレクトリに HEAD が無く、作業ツリーは空。以後の update は "Unable to find current
    revision" で失敗し続け、**しかも何も知らせない**。status はクリーンで、submodule の一覧も健全に
    見える。
  - だから規則はこうなる。
    1. 起動の予算（`submoduleWaitTimeout`、10 秒）を過ぎたら、Agent は**待つのをやめるが kill は
       しない**。update は裏で続き、結果はログと通知に出る。git を kill するのは 60 分の
       `submoduleHardTimeout` だけで、そのとき残る固まりは次の起動が直す。
    2. submodule 無しで起動したら、ログに加えて**通知もする**（`submodule-sync`）。
    3. すでに固まった submodule は、実測で唯一効いた手順で直す。`fetch` で転送を終わらせ、記録された
       リビジョンを `checkout --detach --force` する。**作業ツリーが空のものしか触らないので、
       ローカルの変更は壊さない。** 再利用する worktree も同じように同期し直す。
- **親からの種付け**（`git_submodule_seed.go`）が、その別クローンのコストをほぼ消す。
  - **実測（git 2.47）: submodule の remote を到達不能にすると、新しい worktree の update は失敗
    する**。同じディスクの親が全オブジェクトを持っていても使われない。2 つのストアをつなぐものが
    無いため。
  - そこで update の前に、各 submodule を親の `.git/modules/<name>` からローカルに clone する。
    **実測: 41 MB の submodule が remote オフラインのまま 0.23 秒**。オブジェクトは親とハードリンク
    なので、worktree のストアは 41 MB でなく 168 KB。worktree を N 本作っても submodule の容量は
    N 倍にならない。
  - `protocol.file.allow=always` は**その 1 回の呼び出しにだけ**、しかも Agent が自分で組み立てた
    パスにだけ付ける。`.gitmodules` 由来の URL には決して付けない。CVE-2022-39253 はそちらの話。
  - URL の差し替えは設定に書かず `-c` で渡す。**設定ファイルは親と兄弟の worktree 全部の共有物**
    なので、そこにローカルパスを書くと、ほかのコピーの fetch まで向きが変わる。
  - 終わったら submodule の origin を本来の remote へ戻す。値は設定から取る。`git submodule sync` は
    `.gitmodules` を読み直し、SSH→HTTPS の書き換えを潰してしまう。
  - 親が持っていないものは、そのあとの通常の update に任せる。
  - **入れ子の submodule も降りて種付けする**（最大 8 段）。入れ子のオブジェクトは親 submodule 自身の
    ストアの下にある。1 回では済まない。入れ子は親 submodule の中でしか宣言されておらず、その親を
    clone するまで存在しないため。
  - submodule は `--jobs 4` で取得する。git の既定は 1 で、完全に直列。
- **再帰の update は、`--init` が無いと入れ子に届かない。**
  - 実測（git 2.47）: 付けないと、`update --recursive` はトップレベルを clone して中へ降りる。そこで
    入れ子が未 init なのを見て、**exit 0・出力なしでスキップする**。空のディレクトリが残り、
    `submodule status --recursive` を見るまで誰も気づかない。
  - 先に `submodule init` を走らせても代わりにならない。展開できるのはトップレベルの `.gitmodules`
    だけで、入れ子のものは親 submodule を checkout するまで読めない。
  - 入れ子向けの SSH→HTTPS 書き換え規則（`submoduleInsteadOfArgs`）が届くようになったのも、これの
    おかげ。
- **SCM の読み書き**: changes・diff・log・graph・show・stage・unstage・discard・commit。リビジョンは
  16 進で検査し、応答にはサイズ上限がある。
- **ファイルシステム API**（home を根にしたツリー・ファイル・アップロード・リネームなど）は、
  トラバーサルを防ぎ、シンボリックリンクを解決した後のパスも検査し、サイズに上限を設け、バイナリを
  判定する。
- **denylist**（`fs.go` の `fsDeny`）は、資格情報やエージェントの状態を持つ場所を一覧から隠し、直接の
  アクセスも断る（400）。
  - エージェント各自のディレクトリ（claude・codex・opencode・agy・copilot・cursor・kiro・muse）
  - この製品の設定と状態（`.config/agent-fleet`・`.local/state/agent-fleet`・
    `.local/share/agent-fleet`）
  - `.ssh`・`.git-credentials`・`.aws`（SSO のトークンキャッシュと生成した設定）

  例外は 1 つで、codex が生成した画像だけは読める。
- **LFS** はイメージにシステム全体で入っているので、clone と checkout は普通に smudge する。smudge
  されずに残ったポインタはファイル API が検出し、ビュアーがバッジを出す。
- git の認証は 1 本の資格情報ヘルパー（`workspace-agent cred`）が、その都度復号して渡す
  （[07 §7.6](07-security.ja.md)）。

## 4.7 転写と使用量

- 転写は**末尾のウィンドウと、後ろ向きのページング**で返す（`GET /sessions/{name}/messages`、
  [decisions/0009](../decisions/0009-transcript-paging.ja.md)）。
- kind ごとに自分の保存形式を読むリーダーがあり、どれも共通のターン形に揃える。**パーサは意図して
  統合しない。**
- 使用量の出どころは kind ごとに違う。
  - `GET /claude/usage` と `/codex/usage`: CLI のローカル記録と、claude は捕捉したステータスライン
  - `/copilot/usage`: GitHub の API
  - `/muse/usage`: runtime が最後に報告した値
  - `/connections/agy/usage`

  セッションを横断する台帳は `/sessions/usage` と `/usage/series`。

## 4.8 秘密情報（Agent の責務）

Agent は暗号化ストア `secrets.enc`（AES-256-GCM、0600。ロックの下で一時ファイルと rename で書く）を
持つ。

- **資格情報は、平文のファイルを作らないサブコマンドで渡す**: git の資格情報ヘルパー
  `workspace-agent cred`。`bitbucket-cred` はその別名。
- 鍵 `AF_SECRET_KEY` は CP が起動時に注入する。どう用意されたかに Agent は関心を持たない
  （[07 §7.6](07-security.ja.md)）。
- 鍵が無いとき（マスターキーの無い CP は注入しない）は、同じ経路で平文の `secrets.json` を書く。
- 古い平文の資格情報は、起動時にストアへ取り込んで消す（`migrateLegacySecrets`）。

**ここに意図して置かない物が 1 つある: git プロバイダの OAuth クライアントシークレット**
（[decisions/0052](../decisions/0052-tenant-git-oauth.ja.md) の決定 7）。

- これは*テナントの*資格情報。CP に置けば、**メンバー全員の**ストアへ写さずに済む。
- Agent は CP に refresh を代行させる（`POST /internal/git-oauth/bitbucket/refresh`。Jira も同じ）。
  **利用者本人の refresh token はここに残る。**
- ブリッジの座標（`AF_CP_BASE_URL`・`AF_GIT_OAUTH_TOKEN`）は、環境変数から読まず、起動時に暗号化
  ストアへ写す（`seedGitOAuthBridge`）。**資格情報ヘルパーは git が起動する別プロセスで、その環境
  変数は保証できない。** 内部 git プロバイダのトークンも同じ扱い（`seedInternalGit`）。

## 4.9 Workspace イメージと entrypoint

`workspace/Dockerfile` はマルチステージで、`golang:*-trixie` でビルドし、`node:22-trixie-slim` に載せる
（Debian 13、[decisions/0068](../decisions/0068-debian-13-base.ja.md)）。**イメージと Agent は全デプロイ
ターゲット共通**で、それが分割の要点（[09](09-deploy.ja.md)）。

- **エージェント CLI の入り方は 2 通り**で、**配布の既定は lean**（`BAKE_AGENT_CLIS=0`、
  [decisions/0037](../decisions/0037-registry-policy.ja.md)）。
  - **lean**: claude・opencode・codex・copilot・cursor・agy・rtk を**イメージに焼かない**。
    プロプライエタリなソフトを再配布しない安全な既定。
    - entrypoint が初回起動時に、**ピンした版を公式の配布元から `~/.local` へ入れる**。
    - home は永続なので、2 回目以降は黙ってスキップする。
    - ネットワークが無いのは警告で、失敗ではない。次の起動で入れ直す。
    - 利用者が自己更新を選んでいなければ、勝手に更新した CLI をピンへ戻す。
    - kiro（展開後約 855 MB）と muse はそこからも外し、必要になったときに入れる。kiro は起動時の
      ガード（`install-kiro --if-needed`）が、muse は接続カード（`install-muse`）が入れる。
  - **焼き込み**（`BAKE_AGENT_CLIS=1`）: 初回起動を速くしたいデプロイ向けの明示的なノブ。
- **版のピンはどちらの経路でも同じビルド引数**（`CLAUDE_CODE_VERSION`・`CODEX_VERSION`…。上げる手順は
  [10 §10.2.1](10-development.ja.md)）。
  - **ノブに関係なく、すべてのピンを `/usr/local/share/agent-fleet/versions.json` に書き出す**。
    エージェント CLI、ツールチェーン、データベースサーバーまで含む。
  - これを読むのは `GET /env/tool-versions`（設定 → ツールチェーン「ツールのバージョン」）、
    スモークテスト、初回導入。
  - 運用ツール系の MCP サーバーのうち 2 つは、**実行して版を訊けない**。片方は `--version` で
    サーバーが起動してしまい、もう片方には版のフラグが無い。だから版は導入済みパッケージの
    メタデータから読む（`toolSpec.PyDist`、`uvToolVersion`）。**同じ性質のサーバーを足すときも同じ
    扱いにすること。**
- **共通ツール**（`BAKE_OPTIONAL_TOOLS=1`、既定）:
  - Go ツールチェーン（`GO_VERSION`。`go.mod` と歩調を合わせる）
  - build-essential と python3（pip は利用者の領域へ入れられる）
  - git-lfs・tzdata と定番のコマンド
  - 版を固定した Debian の Chromium と日本語フォント

  `BAKE_OPTIONAL_TOOLS=0` は `native` 向けの軽いルートファイルシステムで、これらを必要時に入れる
  （`install-chromium` など）。
- **Chromium はサンドボックスを保つ。**
  - setuid のサンドボックスヘルパーはビルド時に検証する。ほかの setuid・setgid ビットはイメージから
    すべて外す。
  - Chromium は `--disable-dev-shm-usage` で起動する。
  - docker の runtime は、ヘルパーが名前空間を作れるよう bounding set に `SYS_ADMIN` を足す。`dev` に
    実効ケーパビリティは無い。ECS の runtime は何も足さない。
- **JDK はイメージの外にあり、置き場は 2 つ。**
  - Temurin の JDK を集めた共有ディレクトリ。docker では `/usr/lib/jvm` に読み取り専用でマウントする
    （⚠️ そのディレクトリを作るとき、JDK の `cacerts` のシンボリックリンクを実体化しておかないと、
    トラストストアが空になる）。
  - `~/.local/share/agent-fleet/jvm`。`workspace-agent install-jdk` が入れる。ECS では何もマウント
    しないので、ここが唯一の置き場。

  `JAVA_HOME` は 設定 → ツールチェーン の選択に従い、探索は Workspace 自身のアーキテクチャを優先する
  （`jvmSearchDirs`）。Node は `workspace-agent install-node` が版ごとに nvm の配置へ入れる。
- **レイヤの順序**: 重くてめったに変わらないレイヤを前に置く。よく変わるコピー（Agent のバイナリ・
  entrypoint・plugin・notes）は最後にまとめ、小さな修正でキャッシュを壊さない。`CMD` は絶対パスの
  `/usr/local/bin/workspace-agent` なので、`~/.local/bin` のコピーには乗っ取られない。
- **entrypoint は無いものだけを置く**:
  - claude の `settings.json`（権限スキップの確認、Remote Control、通知、rtk の hook）
  - `~/.gradle/gradle.properties`（メモリの限られたホスト向けの控えめな値）

  そのあとは設定 UI が正。毎回の起動で値を押し付けると UI と喧嘩する。
- **entrypoint が毎回の起動で当て直すもの**:
  - opencode の plugin
  - opencode の `permission=allow`
  - cursor の更新チャネル
  - kiro の自動更新スイッチ
- **Workspace の利用ガイドは entrypoint でなく Agent が配る**
  （[decisions/0042](../decisions/0042-user-instructions.ja.md)）。
  - claude はイメージの managed policy `/etc/claude-code/CLAUDE.md` を読む。
  - `reconcileAgentInstructions()` が codex・opencode・agy・muse の `AGENTS.md` へ、**マーカーの間に**
    ガイドを合成する。マーカーの外には触らない。以前の単純なコピーは毎回の起動でファイルを丸ごと
    上書きし、利用者が書き足した内容を消していた。
  - copilot と kiro には専用のファイル（`agent-fleet-guide.*`）で渡す。
  - **cursor にはローカルの利用者スコープが無く、渡せない。**
  - トピックファイル（`workspace/notes/`）は `/usr/local/share/agent-fleet/notes/` に入り、claude・
    codex・opencode・muse ではスキルとしても登録する（`fleetskills.Apply`）。
  - ⚠️ `workspace/.dockerignore` は `**/*.md` を除外している。ガイド・トピックファイル・アシスタントの
    知識（唯一の `//go:embed` の入力）には、それぞれ `!` の例外が要る。
- **タイムゾーン**: ツールチェーン設定の `timezone`（既定 `Asia/Tokyo`）を entrypoint が `TZ` として
  export する。知らないゾーンは警告して UTC にする。反映は次の Stop → Start。
- **利用ガイドはマウントするか、取得する**（[decisions/0064](../decisions/0064-docs-three-audiences.ja.md)）。
  - 全メンバーが同じツリーを受け取る。`guide/` の棚とルートの README（`control-plane/workspace_docs.go`
    の `guideRoots`）。開発者向けの文書は配らない。
  - docker と native では、CP がそのツリーを用意し（`stageWorkspaceDocs`）、
    `/usr/local/share/agent-fleet/docs` に読み取り専用でマウントする。
  - ECS のタスクには CP が書けるホストのパスが無いので、Agent が起動時に同じツリーを取得する。
    メンバーシップごとの `AF_DOCS_TOKEN` で `GET /internal/docs`（`control-plane/docs_bridge.go`）を
    呼ぶ（`docs_sync.go`）。
  - **マウントが常に勝つ**。docs ディレクトリが空でなければ取得で上書きしない。
  - アーカイブは信用しない。
    - 通常ファイルだけ
    - 絶対パスと `..` は拒否
    - ファイル数と総サイズに上限
    - 展開はステージングのディレクトリへ行い、gzip を最後まで読めたときだけ本番へ rename する。
      途中で切れたダウンロードが「半分だけの docs」として見えることはない。
  - そのため、イメージのマウント点は `dev` の所有にしてある。
- claude の自己更新は `~/.local` の中だけで、焼き込んだ版は固定。古い home パスで宙に浮いた起動
  リンクは entrypoint が直す。
- **変更の反映**: イメージか entrypoint に触れたら、イメージを作り直し、Workspace を Stop → Start する
  （[10](10-development.ja.md)）。

## 4.10 BrowserManager

`BrowserManager`（`internal/browserx`）は Workspace ごとに Chromium のプロセスを 1 つ、必要になった
ときに起動し、CDP のパイプで動かす。ブラウザ id ごとに独立したブラウザコンテキストとページを持つ。

- **面**: `POST /browser/pages`、`GET` と `DELETE /browser/pages/{id}`、WebSocket の
  `GET /ws/browser?id=`。
- **断るもの**: Agent 自身のポートと、loopback 以外へのトップレベルのナビゲーション
  （`allowedTopLevelBrowserURL`）。サブリソースは Workspace の egress ポリシーの下で普通の外部ホストへ
  出てよいが、管理用のエンドポイントには決して出さない。コンテナホストの別名、クラウドの
  メタデータホスト、CP、リンクローカルのアドレスがそれにあたる（`forbiddenBrowserResource`）。
- **ワイヤプロトコル**（バージョン 1）は最初に `ready` フレームを送る。以後は状態・ナビゲーション・
  コンソール・エラーをテキストで、**生の JPEG をバイナリで**送る。
- **Console から受け付けるのは** viewport（ピンチズーム付き）・ポインタ・ホイール・キーとテキスト・
  ナビゲーション・可視性・コピーだけ。**生のデバッグプロトコルは決して出さない。**
- 利用者から見える上限は [ref/limits.md](../../guide/ref/limits.ja.md)。既定値は Workspace ごとに
  調整できる（`AF_BROWSER_MAX_FPS`・`AF_BROWSER_PAGE_LIMIT`・`AF_BROWSER_DETACHED_GRACE_SEC`・
  `AF_BROWSER_JPEG_QUALITY`）。使われていない Chromium は `AF_BROWSER_IDLE_SEC` 後に止める。
- **フレームレートは送信の間引きでなく、キャプチャの時点で効かせる。** 容量 1 フレームのワーカーが
  確認応答を遅らせるので、Chromium はキャプチャとエンコードの段階で抑えられ、捨てるフレームを作ら
  ない。
- パイプにはメッセージとキューの固定上限がある（1 メッセージ 8 MiB、キューは 256 件か 32 MiB）。
  **必須のイベントで上限に達したら、キューを伸ばさずにブラウザを終了し、ページを `crashed` にする。**

詳細は [decisions/0018](../decisions/0018-container-browser-pane.ja.md)。

**他者が起動した Chromium へのアタッチ**は別のマネージャー
（[decisions/0038](../decisions/0038-chromium-attach-view.ja.md)）。

- 面は `/browser/attach-targets`・`/browser/attachments*`・`GET /ws/browser-attachments` で、MCP の
  `attach_chromium` 系ツールの裏にある。
- 操作モードは `view-only`・`user-control`・`locked`。MCP ツールは view-only で始まり、利用者の
  クリックは効かない。HTTP のハンドオフは `user-control` で始まる。
- 対象はポートでなく、Chromium の `DevToolsActivePort` の 2 行目にある GUID で識別する。使い回された
  ポートで、他セッションのブラウザへ黙ってアタッチしないため。
- デタッチしても対象は閉じない。

## 4.11 tmux サーバーのスコープと第 2 インスタンスの隔離

> **この節がある理由。** 統合テスト（[decisions/0008](../decisions/0008-antigravity-cli-agent-kind.ja.md)）で、
> 別ポートで起動した 2 つ目の Agent が、終了時に**共有のデフォルトソケットへ `kill-server` を実行**した。
> **無関係に動いていたセッションが 4 回全滅し**、開発者自身のものも含まれていた。

**恒久対応:**

- 本番は 1 Workspace に 1 Agent で、デフォルトソケットの tmux サーバーを作るのは Agent だけ。以前の
  終了処理はそれを前提にしていた。**同じ環境に 2 つ目のインスタンスがいる瞬間に、その前提は崩れる。**
- **`kill-server` は Agent の製品コードで全面禁止。**
  - 終了時に kill するのは**このインスタンスが所有するセッションだけ**。自分のメタデータと生きている
    ものの積（`ownedLiveSessions`）を、完全一致のターゲットで。
  - **自分のメタデータが無い生きたセッションには触らない。** 「別インスタンスの作業」と「メタデータを
    失った孤児」を見分けられないため。
  - 本番では、所有セッションを消せばサーバーは自然に終わり、以前と同じ終状態になる。
- **tmux の実行はすべて `tmuxx.Cmd` に集める。** `AF_TMUX_SOCKET=<name>` を設定すると、全呼び出しが
  `tmux -L <name>` になる。デフォルトソケットからも、**継承した `$TMUX` からも**切り離された専用
  サーバー。
- この 2 つは `workspace/agent/tmux_guard_test.go` の仕掛け線テストが守る。禁止した呼び出しと、
  集約の迂回を検出する。

**第 2 インスタンスを安全に起動する方法**（コンテナ内のテスト、手元のデバッグ）。**ソケット・
メタデータのディレクトリ・ポートを分けること。どれかを共有すると本物と衝突する。**
さらに `HOME` を分け、共有の CLI デーモンに繋がせず、空の環境から起動する。

```sh
d=$(mktemp -d) && mkdir "$d/home"
env -i PATH="$PATH" TERM="${TERM:-xterm-256color}" LANG=C.UTF-8 \
  HOME="$d/home" \
  AF_TMUX_SOCKET=af-e2e-$$ \
  AF_SESSIONS_DIR="$d/sessions" \
  AGENT_ADDR=:7710 AGENT_TOKEN=test-token \
  AF_CODEX_APP_SERVER_DISABLE=1 AF_OPENCODE_SERVE_DISABLE=1 \
  ./workspace-agent
```

- **ソケット**: ⚠️ `AF_TMUX_SOCKET` 無しで tmux のペインの中（いつもの開発セッション）から起動すると、
  **`$TMUX` を継承して確実に共有サーバーへ向く**。事故の直接の原因。
- **メタデータのディレクトリ**: 共有すると、2 つ目のインスタンスが本物のセッションを自分のものと
  思い込み、**終了時に止めてしまう**（所有の判定はメタデータが根拠）。
- **ポート**: Agent は起動処理の前に `AGENT_ADDR` を bind し、使用中なら終了する。衝突は中途半端に
  起動せず、すぐ失敗する。
- **home**: 起動処理は home のあちこちのファイルを書き換える。状態の移行、全 CLI の指示ファイルの
  合成、状態 hook の再登録、全 CLI の設定にある `af` MCP サーバーの名前の付け替え。本物の home で
  2 つ目を動かすと、本物のセッションの設定を書き換えてしまう。
- **共有デーモン**: Agent は起動時、既定のアドレス（`ws://127.0.0.1:7798`）で待ち受けている codex
  app-server があればそれを採用する。書き手の接続を開き、そのデーモンが読み込んでいる全スレッドに
  観察役を付ける。本物のインスタンスのセッションも例外ではない（実測: フラグ無しで起動した第 2
  インスタンスが 5 本を観察した）。opencode serve のデーモン（`http://127.0.0.1:7799`）も、
  マネージドの opencode セッションが 1 つでもあれば同じように届く。`AF_CODEX_APP_SERVER_DISABLE=1`
  と `AF_OPENCODE_SERVE_DISABLE=1` で両方から切り離す。ターミナル（CLI）の codex・opencode
  セッションはこれらが無くても動く。
- **環境変数**: `env -i` は列挙した変数だけを渡す。セッションのシェルから起動すると、そうしなければ
  第 2 インスタンスはそのシェルの Control Plane の URL とトークン（`AF_CP_BASE_URL`・
  `AF_MCP_TOKEN` など）や、`CLAUDE_CONFIG_DIR`・`CODEX_HOME` のような CLI の状態ディレクトリを
  継承する。これらは `HOME` に関係なく本物のインスタンスの状態を指す。
- 後片付けは `tmux -L af-e2e-$$ kill-server` で、**自分のソケットに対してだけ**行う。共有のソケットへ
  `kill-server` を打つのは禁止。
- テストも同じように隔離する。tmux を直接叩くテストは専用の `-L` ソケットを使い、製品コードを通す
  テストは `t.Setenv` で `AF_TMUX_SOCKET` を設定する。
