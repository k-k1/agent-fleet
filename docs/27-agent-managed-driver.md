# 27. エージェント制御の Managed Driver 化（TUI スクレイプ → 共有 runtime＋構造化 RPC） — 設計

**Status: ✅ 設計確定・P1/P1.5/P2/P3 実装済み（2026-07-15）** — 2026-07-15 起草。並行設計セッション（sol=A / fable=B）の成果を比較し、
B の骨格に A の部品を移植する形でユーザー裁定により統合・確定した（経緯は [decisions/0015](decisions/0015-agent-managed-driver.md)）。
P1（Codex 観測拡張）実装時の実測で判明した事実は §12.1 に記録（**通知はスレッドアタッチ必須**という
発見により、P1 は「switch への 5 イベント追加」から「observer のアタッチ機構＋5 イベント」に拡大した）。
P1.5（Console managed セッション UI の受け皿）の実装記録は §10.1、pane 前提機能の棚卸しと置き換え設計は §10.2。
P2（OpenCode managed 化 — Driver/Supervisor/turn 状態機械/Interaction/reconciliation の初出）の実装記録と
実測事実は §12.2（**opencode の v1/v2 二重 API と directory スコープ**という 2 つの発見が実装を規定した）。
P3（Codex managed 化 — 第2 Driver、daemon drain、双方向排他切替）の実装記録と実測事実は §12.3
（**質問requestの再配送、resume後のpolicy再表明、server作成threadのTUI互換**が実装を規定した）。

> 発端は Codex TUI のモデル勝手切替バグ（週次利用率 93〜99% で `ThreadSettings` が 3 件連続送信され
> `gpt-5.6-sol` → `gpt-5.4-mini` へ意図せず切替→直後にコンテキスト圧縮。複数セッションで再現）。
> 暫定対処の `[notice] hide_rate_limit_model_nudge` トグルは main マージ済み（`2a6fe25`）。
> 本書はその根本対処＝「端末スクレイプ＋キー入力エミュレーション」からの脱却を、
> Codex 単体でなく 3 エージェント（codex / opencode / claude）横断のアーキテクチャとして設計する。
> （追記 2026-07-21: 第3の Driver 実装として copilot が加わった — 共有 daemon でなく
> **per-session child**（`copilot --acp`、ACP JSON-RPC over stdio）という新しい
> ProcessModel。[docs/36](36-copilot-agent-kind.md) / [decisions/0019](decisions/0019-copilot-agent-kind.md)）
> （追記 2026-07-24: 同じ per-session child ACP ProcessModel の実装として kiro（Kiro、
> 旧 Amazon Q Developer CLI）が加わった — `kiro-cli acp`（ACP JSON-RPC over stdio）。
> [docs/43](43-kiro-agent-kind.md) / [decisions/0026](decisions/0026-kiro-agent-kind.md)）

---

## 0. 要旨

- **確定したドライバ方針**: **Codex / OpenCode は managed（共有 runtime＋構造化 RPC）を既定の第 1 ドライバ**とし、
  **ユーザーが明示的に選択できる CLI（TUI）ルートを常設**する。**Claude は現状のターミナル CLI を維持**
  （compact への応答など、いざという時の CLI 操作が必要という運用判断。Session Manager 案は凍結し付録 A に温存）。
- **骨格はボトムアップ増築**: 既存の read 正規化層（`internal/agents` の `Agent` IF＋`TranscriptData`/`LiveInfo`）を
  無傷で温存し、その上に **Driver 層**（thread 単位の制御・購読）と **RuntimeSupervisor 層**（プロセス生涯管理）を増築する。
  「Event journal / read model」のような新設層で read 層を**置き換えない**。会話イベントの二重永続化はしない。
- **記録の三層分担**: read＝ネイティブストア正本（rollout JSONL / SQLite / `<sid>.jsonl`）、
  live＝runtime イベント（WS 通知 / SSE / hooks）、write＝構造化 API。履歴互換は正本を動かさないことで自動成立。
- **A から移植した機構**: Driver/RuntimeSupervisor 分離、turn 状態機械＋`ClientMessageID`、
  切断後 reconciliation 共通手順、`Interaction` 一般化、runtime generation＋旧世代 drain、
  Capabilities 詳細化、ControllerLease（縮退版）。
- **メモリ効果**（CLI 0.144.3 実測、付録 B）: Codex TUI 1 セッション約 280 MiB → 共有 app-server 約 129 MiB＋thread 分。
  既定を managed に置くことで大勢を取り、CLI 選択者のみが TUI 分（約 +230 MiB）を明示的に払う。

---

## 1. 背景 — 現状実装と限界

### 1.1 3 エージェントは同型（コード実測 2026-07-15）

現状、3 エージェントとも「tmux 内 TUI＋承認バイパス＋ネイティブストア読み＋hooks/plugin 状態通知」という同じ構造で動く。

| | Codex | OpenCode | Claude |
|---|---|---|---|
| 起動 | `codex (--remote) resume <id>`＋bypass（`internal/agents/codex/program.go:37`） | `opencode --auto --session <id>`（`internal/agents/opencode/program.go:43`） | `claude --session-id/--resume <sid>`＋skip-permissions（`internal/agents/claude/program.go:32`） |
| session id | codex 自己発番 → hook で捕捉（`RememberSid`） | plugin が捕捉（`AF_SESSION_SID` 鍵） | **AF が決定的 sid をピン留め**（`--session-id`） |
| 会話正本 | rollout JSONL（`~/.codex/sessions/**`） | SQLite（`~/.local/share/opencode`、RO 読み） | `<sid>.jsonl`（`CLAUDE_CONFIG_DIR/projects/*`） |
| 状態通知 | `-c` 注入 hooks（UserPromptSubmit/Stop） | plugin | settings 注入 hooks |
| 認証 | `~/.codex/auth.json`（codex 自身が管理・env 無視） | **AF 暗号化ストア → env 注入**（`internal/agents/opencode/auth.go`）＋opencode 自前ログイン | `.credentials.json`（claude 自身が管理） |
| サーバ化の口 | app-server（WS JSON-RPC）**既に常駐・観測専用**（下記） | `opencode serve`（HTTP＋SSE、TUI 併用が公式サポート） | **集約不可**。Agent SDK / stream-json は session 毎子プロセス。Remote Control は公開ローカル API なし |

read 側は既に正規化済み: `internal/agents/agents.go` の `Agent` IF が 3 種のネイティブストアを共通
`transcript.Turn` モデル（＋`Tasks`/`Mode`/`Pending`/`Queued`/`Compacting`）へ畳んでいる。**統一化とはこの層に
write（制御）と subscribe（イベント）を足すこと**であり、read 層の再設計ではない。

### 1.2 Codex 共有 app-server は既に居る（観測専用）

`workspace/agent/codex_appserver.go:52` `startCodexAppServer` が Workspace Agent 起動時に
`codex app-server --listen ws://127.0.0.1:7798` を 1 プロセス起動する。TUI は `--remote` でそこに接続し
（`program.go:51-54`）、AF は第 2 の WebSocket 接続で **read-only オブザーバ**として観測する。
書き手は TUI だけなので競合はない。app-server 起動失敗時は従来の直接 TUI へフォールバックする（可用性優先）。

P1 実装（2026-07-15）で観測対象を `contextCompaction` の item lifecycle（`b5ee735`）から
`account/rateLimits/updated`・`model/rerouted`・`thread/settings/updated`・`warning`・
`thread/status/changed` へ拡張した。その際、**thread スコープ通知はスレッドをロードした接続にしか
配送されない**（§12.1-1）と判明したため、observer が `thread/resume` で各スレッドに read-only
アタッチする `codexObserver` を追加した——**b5ee735 の圧縮検知はアタッチなしでは本番で発火しない
実装だった**（P1 で修正）。

### 1.3 何が限界か

- **制御が端末経由**: 入力は tmux `send-keys`/`paste-buffer`、footer/update メニューは `capture-pane` スクレイプ。
  TUI 固有の選択画面（モデル切替ナッジ等）に Enter を誤送信するリスクが構造的に消せない——発端バグの根因。
- **状態がヒューリスティクス**: `WireLive`（`internal/agents/codex/codex.go:105`）は hooks の status store を基に、
  Stop hook 取りこぼしを rollout の `task_complete` で自己治癒（`rolloutCompletedAfter`）、
  `request_user_input` は rollout tail probe（`HasPendingQuestion`）で検出、という積み重ね。
- **メモリ**: TUI がセッション毎にフルの Codex 本体（約 232 MiB）を抱える。待機セッションを多数抱える
  Agent Fleet では支配的なコスト（付録 B）。

一方 app-server / serve は `thread/start`・`turn/start`・`turn/steer`・`turn/interrupt`・
`thread/settings/update(d)`・`account/rateLimits/updated`・`thread/compacted`・`model/rerouted`・`warning`・
`thread/status/changed`・承認要求への構造化回答、を持つ（suqhrov セッションでの CLI 0.144.3 調査。
個別の網羅は §12 で追検証）。

---

## 2. 確定したドライバ方針

| | 第 1 ドライバ（既定） | CLI ルート | 切替 |
|---|---|---|---|
| **Codex** | app-server (managed) | **常設・ユーザー選択**: TUI in tmux＋`--remote`（AF は観測）。チャット⇔ターミナル維持 | **双方向の排他切替**（必ず stop→drain→resume 経由） |
| **OpenCode** | serve (managed) | **常設・ユーザー選択**: serve へ TUI アタッチ。チャット⇔ターミナル維持 | 排他不要（serve が直列化）。managed セッションへの TUIAttach も可 |
| **Claude** | ターミナル CLI（現状維持） | ＝第 1 ドライバそのもの | なし（Session Manager は凍結、付録 A） |

決定の理由:

- **選択は「TUI vs managed」の二択でなく 3 状態**: (A) TUI 駆動＋AF 観測（現状）/ (B) 同一 thread への
  二重 writer（rollout 書込・モデル設定・ターン状態が競合。**採らない**）/ (C) AF が唯一の writer。
  Codex/OpenCode の managed は (C)、CLI ルートは (A) であり、(B) を構造的に排除する。
- **メモリ削減と TUI 停止は同一決定**: 共有 daemon の得は per-session TUI を止めて初めて出る。
  既定＝managed で大勢を取り、CLI はユーザーの明示的なトレードオフ（+約 230 MiB）とする。
- **発端バグの根治は managed でのみ**: ナッジは TUI のキー入力プロンプト挙動であり、AF が唯一の writer に
  なれば構造的に消える。CLI ルートでは既存の `hide_rate_limit_model_nudge` トグルで抑止する二段構え
  （トグルは恒久残置）。
- **Claude を除外する理由**: Claude は 1 プロセスに N thread を集約できず（Agent SDK / stream-json は
  session 毎子プロセス）、managed 化しても共有によるメモリ削減がない。加えて運用上 CLI 直接操作
  （compact への応答等）が必要という判断。Remote Control と AF 駆動は同一 session を同時 resume できない
  排他でもある。eviction（待機 child を落として resume で再生成）による削減案は付録 A に凍結。

CLI ルートの定義（レガシー温存ではなく正規の第 2 ドライバ）: **TUI が writer、AF は共有 runtime 経由で
read-only 観測**。Codex は現行 `--remote` 構成そのもの。OpenCode も共有 serve へ TUI をアタッチする形へ寄せて
対称化する（スタンドアロン TUI は serve 障害時のフォールバック）。P1 の観測拡張（§11）の恩恵は CLI ルートにも乗る。

---

## 3. アーキテクチャ — 3 層＋RuntimeSupervisor

```
Console ──(既存の汎用 /sessions ワイヤ契約のまま。per-agent REST は作らない)── Workspace Agent
                                                            │
  ┌─ read 層（既存・無傷）──────────────────────────────────┤
  │   agents.Agent / TranscriptData / WireLive              │  正本＝ネイティブストア
  │   rollout JSONL・SQLite・<sid>.jsonl 読み               │  (rollout / SQLite / jsonl)
  ├─ Driver 層（新設・thread 単位の制御と購読）─────────────┤
  │   ThreadHandle: Send/Steer/Interrupt/UpdateSettings/    │
  │   Respond(Interaction)/Events()/Snapshot()              │
  ├─ RuntimeSupervisor 層（新設・プロセス生涯管理）─────────┤
  │   daemon 起動/監視/再起動・generation 管理・drain       │
  └──────────────────────────────────────────────────────────┘
```

責務分離（A①）: **Driver は「thread に何をさせるか」だけを知り、プロセスがどこで動いているかを知らない**。
Codex app-server / OpenCode serve という runtime の差、再起動・drain は RuntimeSupervisor に落ち、
Driver 実装は薄く保てる。

```go
type Driver interface {
    agents.Agent                      // read 層をそのまま継承（温存）
    Capabilities() Capabilities
    Resume(m session.Meta) (ThreadHandle, error)  // 無ければ新規 start
}

type ThreadHandle interface {
    Send(in TurnInput) error   // turn/start 相当。TurnInput = Prompt＋Attachments＋ClientMessageID（§4・§10.2-3）
    Steer(in TurnInput) error  // turn/steer 相当（実行中 turn への追撃入力）
    Interrupt() error
    UpdateSettings(s ThreadSettings) error   // モデル / effort / mode
    Respond(reply InteractionReply) error    // §5
    Events() <-chan Event
    Snapshot() (ThreadSnapshot, error)       // reconciliation 用（§6）
}

type RuntimeSupervisor interface {
    Ensure(kind session.Kind) (Runtime, error)   // daemon 起動（冪等）
    Restart(kind session.Kind, reason string)    // generation++ → drain → 再生成（§7）
    Generation(kind session.Kind) int
}
```

### 3.1 Capabilities — 固定知識を Console に埋め込まない

```go
type Capabilities struct {
    ProcessModel    // shared-daemon | per-session-child | tui
    Steer, Fork, DynamicModel, DynamicEffort, DynamicMode bool
    Permissions, Questions bool   // 対応する Interaction 種別
    EventReplay     bool          // 3 者とも false 想定 → 回復は snapshot 照合（§6）
    EphemeralThread bool          // 隔離ワンショット thread（chat 統合の将来余地、§9.3）
    TUIAttach       bool          // OpenCode のみ true
}
```

**Console は Capabilities から描画を決め、`kind == "codex"` 分岐を持たない**。`agents.go` が kind 分岐
50 箇所を `Caps` に畳んだ動機（`internal/agents/agents.go:27-40`）の延長であり、この規律を Console まで延ばす。

### 3.2 ワイヤ契約 — per-agent REST は作らない

suqhrov で出た `POST /claude/sessions/...` のようなエージェント別 REST 面は**採らない**。Workspace Agent の
ワイヤは既存の汎用 `/sessions` 面のままとし、Driver はその内部部品とする。エージェント別 API 面は 3 組に
増殖して統一 Driver の意義と矛盾する。

---

## 4. turn 状態機械と ClientMessageID（A②）

```
queued → starting → running ⇄ waiting_interaction
                       │ ↘ interrupting → cancelled
                       ├→ completed / failed
（切断・runtime 喪失時は全 thread を unknown に落とし、§6 の手順で解決）
```

- `queued`: `ClientMessageID` 採番済み・runtime 未投入（実行中 turn への追撃入力もここ）。ID は AF が採番し、
  再送・reconnect 後の二重投入を冪等化する。ミラーの「キュー済み」バッジ（既存 `TranscriptData.Queued`）の
  裏付けデータにもなる。
- `waiting_interaction`: Interaction（§5）待ち。既存 `question` 状態の一般化。
- `unknown`: **切断時の正直な状態**。現行は取りこぼすと `working` に張り付き `rolloutCompletedAfter` で
  治癒しているが、本設計では「unknown に落として手順で回復する」を正規の道にする。
- 既存の WireLive 状態語彙（working / idle / question / compacting）へは状態機械からの**射影**で供給し、
  ワイヤ契約は変えない。CLI ルートのセッションは従来の hooks/probe からの射影で同じ語彙に正規化する
  （Console からは同じに見える）。

## 5. Interaction — 承認・質問・plan 確認の一般化（A④）

```go
type Interaction struct { ID, Kind, Prompt string; Questions []transcript.Question }
type Decision string  // allow | deny | cancel | answer
type Scope    string  // once | turn | thread
type InteractionAnswer struct { Text string; Options []int }  // 質問 1 つ分の回答
type InteractionReply  struct { ID string; Decision Decision; Scope Scope; Answers []InteractionAnswer }
```

P1.5 での精緻化: 当初案の単一 `Options` でなく `Questions []transcript.Question` を持つ
（claude の AskUserQuestion は複数質問を 1 モーダルで出すため、応答も質問ごとの
`Answers` 列で返す）。既存 Pending UI のデータ形をそのまま流用できる。

**初期実装スコープは question 系のみ**: Codex `request_user_input` / OpenCode question tool /
Claude AskUserQuestion 系。3 者とも承認は自動化済み（`--dangerously-bypass-approvals-and-sandbox` /
`--auto` / `--dangerously-skip-permissions`——コンテナがサンドボックス）なので、実運用の対象は質問だけであり、
既存の Pending UI（`transcript.Question`）から薄いアダプタで正規化できる。名前とデータ形だけ先に一般形へ
合わせておくことで、将来 bypass を緩めて実承認を通す時にワイヤと UI を再設計せずに済む。

## 6. 切断後 reconciliation の共通手順（A③）

observer ソケット断・daemon 死・Workspace Agent 再起動のすべてを 1 本の手順に統一する:

1. 影響 thread の turn 状態を `unknown` へ（必要なら generation 更新）
2. runtime へ再接続 / 再起動（Supervisor）
3. `Snapshot()` 取得 — Codex: `thread/read`＋rollout / OpenCode: session get＋SQLite
4. ネイティブ履歴（read 層）と照合し turn 状態を確定（completed 取りこぼしの解消）
5. Console へ snapshot 送信（ミラー再同期）
6. live 購読再開

**イベントの再生ではなく snapshot 照合で回復する**（EventReplay 能力を持つ runtime が現れない前提）。
現行 `monitorCodexAppServer` の再接続ループ（`codex_appserver.go:181`）が既にこの原型（再接続→再購読）を持つ。
「rollout 再読で回復できる自己治癒性」（現行の性質）の手順化である。

## 7. runtime generation と旧世代 drain（A⑤） — 認証・config 反映の実装機構

auth 変更・config 変更・daemon アップデート・クラッシュ時に generation N→N+1 とし、新規 turn は N+1 へ、
実行中 turn は N で drain（完走待ち、タイムアウトで interrupt）してから旧プロセスを止める。

- **認証の反映を「プロセス再生成パス」に畳む**: Codex は daemon 再起動→全 thread 再 resume
  （auth.json のホットリロード可否に賭けない。§12 で検証はする）。OpenCode は provider key が env 注入
  なので**再起動が必須**＝同じパスに乗る。
- 共有 daemon の drain は **workspace 全体の切替窓**（その kind の全セッションが一斉に generation を跨ぐ）。
  この非対称は仕様として明記する。
- logout / login は既存の Connections ハンドラ（`internal/agents/codex/auth.go`、`opencode/auth.go`）が
  起点になり、そこから `Supervisor.Restart` を叩く。

## 8. エージェント別マッピング

| | Codex (managed) | OpenCode (managed) | Claude（現状維持） |
|---|---|---|---|
| Runtime | 共有 app-server（WS JSON-RPC、既存 :7798） | 共有 `opencode serve`（HTTP＋SSE） | tmux 内 TUI（従来） |
| thread 対応 | AF セッション = Codex thread | AF セッション = opencode session | AF セッション = claude sid（`--session-id` ピン） |
| write | `turn/start`・`turn/steer`・`turn/interrupt`・`thread/settings/update` | serve API（prompt / abort / permission 応答） | tmux send-keys（従来） |
| live | app-server 通知（status / compaction / rateLimits / rerouted / warning） | SSE | hooks＋jsonl（従来） |
| read（正本） | rollout JSONL（`transcript.go` 温存） | SQLite（温存） | `<sid>.jsonl`（温存） |
| id 捕捉 | `thread/start` 戻り値（**hooks の `RememberSid` 不要化**） | API 戻り値 | 従来どおり |
| controller | `managed` \| `cli`、双方向排他切替（stop→drain→resume） | 追跡不要（serve が直列化） | — |
| 緊急操作 | CLI ルートへ排他切替 | TUIAttach（無停止で覗ける） | ターミナルそのもの |
| 認証 | auth.json（daemon 再起動で反映） | env 注入（再起動必須） | `.credentials.json`（従来） |

## 9. 5 論点の決定記録

### 9.1 認証

AF はトークンを翻訳せず各 CLI のネイティブ認証を継承する（OpenCode のみ AF が env 注入者）。
再認証の反映は **generation＋drain（§7）に一本化**。分離は既存のコンテナ隔離＋loopback で足りる:
1 コンテナ=1 ユーザ=1 `~/.codex` 等、`:7798`/serve ポートはコンテナの network namespace 内、
同一コンテナ内の別プロセスは認証なしで接続できるがその立場なら auth.json 自体が読めるため追加リスクなし
（`codex_appserver.go:60-62` の判断を維持）。将来、権限の異なるプロセスをコンテナ内に同居させる時だけ
bearer token を再検討。opencode serve のローカル API 認証有無は §12。

### 9.2 方式の選択・移行

`session.Meta` に driver フィールド（**kind は分けない**——transcript / settings / auth / models を共有する
ため。別 kind にすると重複が爆発する）。新規セッションの既定は managed、作成時にドライバ選択 UI
（CLI 選択時はメモリコストの表示を検討）。既存 TUI セッションは明示操作で managed へ排他切替
（Codex: TUI 停止→同 thread id を `thread/resume`）。ロールアウトは kind ごとに独立制御。

### 9.3 アシスタントチャット

現行の 3 種 one-shot（`codex exec --json` / `claude -p` / `opencode run`、別ホーム隔離）を**維持**。
共有 daemon の thread へ統合すると隔離 CODEX_HOME（`chat_providers.go:360` `chatCodexHome`——ユーザ MCP を
毎ターン起動させない・`~/.codex/sessions` を汚さない）が壊れるため、統合は当面見送り。将来の受け皿は
Capabilities の `EphemeralThread` として予約。

確認済み（Codex 0.144.5 / 0.144.6 の両方）: `thread/start` / `thread/resume` は任意 `config` を受け、
`config.mcp_servers` で MCP を thread ごとに構成できる。`mcpServerStatus/list {threadId}` を使う
credential-free drift test（`TestDriftCodexThreadMCPConfigIsScoped`）では、異なる MCP を持つ 2 本の
ephemeral thread 間で MCP が混入しないことを実 app-server で確認した。

**thread 単位 config は `-c` 由来のサーバを「マージ」ではなく「置換」する**（2026-07-20 実測・
`TestDriftCodexThreadMCPConfigReplacesGlobalServers`）。実 app-server での測定は次のとおり:

| thread に渡す `config.mcp_servers` | `mcpServerStatus/list` に見える MCP |
| --- | --- |
| 渡さない（未指定） | `-c` 設定を継承 |
| 空マップ `{}` | **0 件（`-c` のものは全て遮断される）** |
| 自前サーバのみ指定 | 自前のみ（`-c` のものは現れない） |

1 行目が成立することが重要で、これは「グローバル `-c` 設定自体が効いていないから 0 件に見えた」という
偽陽性を否定する。すなわち thread config は追加・thread 間分離の口であると同時に **allowlist / replace
の口でもあり、空マップ `{}` は deny 機構として機能する**。

> **⚠️ 適用範囲（2026-08-09 追記・0.147.0 実測）: この表は `-c` オプションで渡したサーバにしか
> 当てはまらない。** 上記テストは app-server を `-c mcp_servers.…` 付きで起動しており、測っていたのは
> その層だけだった。**ファイル由来の層（`$CODEX_HOME/config.toml` と、trust 済みプロジェクトの
> `.codex/config.toml`）は thread config を渡してもマージされて残る** — ephemeral / persistent ×
> 空マップ / 非空マップの 4 通り全てで継承された（`TestDriftCodexThreadConfigMergeMatrix`）。
> 従って **空マップはファイル由来のサーバに対する deny にはならない**。将来 thread 単位 config で
> 権限境界（`none` / `af_read` / `af_write`）を作る場合、この前提で設計してはいけない。

**この節は当初「遮断はできない（グローバル MCP は空マップを渡しても `mcpServerStatus/list` に残る）」と
記述していたが、これは誤りだった。** 当該アサーションを固定していたテストは、同一パッケージ内の先行
テストが cleanup で必ずハングしてパッケージごとタイムアウトしていたため、commit 以来一度も実行されて
いなかった（ハーネス側の潜在バグ・`drift_test.go` の `reapProcessGroup` で修正済）。2026-07-20 に
ハングを解消して初めて実行され、前提の誤りが判明した。**0.144.5 と 0.144.6 の両方で同一の結果**で
あり、CLI 側の挙動変更（ドリフト）ではなく、当初の記述が実測に基づいていなかったことによる。

従って `none` / `af_read` / `af_write` の権限境界は thread 単位 config で構成可能であり、upstream の
thread 単位 replace/deny API を待つ必要はない。ただし本節冒頭の one-shot 維持判断は MCP 遮断可否のみを
根拠にしたものではない（`~/.codex/sessions` を汚さない等、隔離 CODEX_HOME の他の性質も理由）ため、
統合を進める場合はそれらを別途評価すること。

2026-07-17 の利用者判断で、アシスタントチャットに対するグローバル MCP の透過は許容する方針へ変更した。
従って上記は権限隔離を要求するデプロイでは維持すべき制約だが、このワークスペースでの Codex managed
チャット統合を妨げる条件ではない。会話固有の AF MCP は追加設定として thread ごとに分離する。

#### 9.3.1 thread 単位 config は MCP 子プロセスへ「値」も届く（managed のセッション同定）

上記は thread ごとに MCP の**在庫**を分離できるという測定で、**値の受け渡し**までは示していなかった。
managed セッションの同定にはこちらが要る。

背景: セッション側 MCP サーバは `AF_SESSION_NAME` で自分の持ち主を知る。TUI ルートは tmux 起動 env
（`session_tmux.go`）でこれが届くが、**managed ルートには届かない** — MCP の子は AF が起動した共有
daemon 1本（`codex app-server` / `opencode serve`）から spawn されるので、プロセス env に per-session の
名前を載せる場所が無い。2026-08-09 実測でも app-server と opencode serve の子は `AF_SESSION_NAME` を
持たず、claude(tmux) の子だけが持っていた。結果、`propose_session_handoff` は cwd 推定へ落ちて、
同じワークツリーを複数セッションが共有していると同定に失敗する（実障害）。

**実測（codex-cli 0.147.0、`drift_mcp_identity_test.go`）:**

| thread config に書いたもの | MCP 子プロセスが観測した値 |
| --- | --- |
| `mcp_servers.<name>.env = {AF_SESSION_NAME: "slot_aaa"}` | `slot_aaa`（**届く**） |
| 同じサーバ名で別 thread に `"slot_bbb"` | `slot_bbb`（thread 間で混ざらない） |
| `env` を書かない（対照） | 未設定（プローブが値を別経路から拾っていないことの担保） |
| `mcp_servers.<name>.env_vars = ["X"]` | daemon 自身の env の `X`（**thread scope でも転送は生きている**） |

プローブは `command="/bin/sh"` + `args` で env をファイルへ落とすだけのもので、MCP ハンドシェイクは
失敗する。既存 2 本と同じく「spawn されること」だけを測っており、モデルも課金も伴わない。
既存 2 本（scoped / replaces）も 0.147.0 で同一結果 — 0.144.5・0.144.6 からのドリフト無し。

従って **managed セッションの MCP 子へ per-session の識別子を注入することは可能**で、LLM に自分の
セッション名を言わせる必要はない（そもそも managed の LLM は shell からも `$AF_SESSION_NAME` を
読めないので、引数方式は `[agent-fleet]` 注記付きの指示でしか成立しない）。

#### 設定層の重なり方（当初の想定を実測で覆した点）

当初この節は「thread config は置換だから、af は**効いている全サーバを毎回出す**必要がある」と書いて
実装していた。前節の ⚠️ のとおり**それは誤りで、ファイル由来の層はマージされる**。0.147.0 で測り直した
結果は次のとおり:

| 層 | thread config を送ったとき |
| --- | --- |
| `-c` で渡したサーバ | 置換される（消える） |
| `$CODEX_HOME/config.toml` | **マージされて残る** |
| trust 済みプロジェクトの `.codex/config.toml` | **マージされて残る**（`TestDriftCodexTrustedProjectConfigContributesMCPServers` / `…ThreadConfigKeepsProjectServers`） |
| 同名サーバが両方にある場合 | **thread 側が勝つ**（`TestDriftCodexThreadConfigOverridesSameNamedFileServer`）。上書きは**エントリ丸ごと**で、フィールド単位のマージではない |

なお **codex は trust 済みプロジェクトの `.codex/config.toml` から MCP を読む**。`codex mcp list` は
user レベルしか表示しないため（openai/codex#13025）、これを probe に使うと「プロジェクトローカル設定は
無い」という誤った結論に達する — ランタイムの `mcpServerStatus/list` で測ること。

**実装済み**（`mcpreg.CodexThreadServers` ＋ codex driver の `threadConfig`）。設計上の要点:

1. **送るのは af のエントリ 1 個だけ。** マージされる以上、他を書き写す理由が無い。書き写せばユーザーの
   env / ヘッダ**値**を RPC ペイロードへ載せることになり、しかも af が管理していないサーバ（手で足した行、
   `codex mcp add`、trust 済みプロジェクト設定）はどのみち再現できない。継承させれば全部そのまま効く。
2. **af のエントリは丸ごと書き切る。** 同名の上書きはエントリ単位なので、env だけ送ると command を
   失ったサーバになる。`codexServerBlocks`（config.toml 側）と同じ command / args / `env_vars` /
   `startup_timeout_sec` を出す。
3. **何も上書きするものが無ければ键ごと省く**（完全継承）。レジストリを読めない・af が codex セッションに
   配られていない・slot 名が無い、のいずれでも `mcp_servers` を送らない。セッション名を失って cwd 推定へ
   縮退するだけで、他は何も変わらない。
4. **秘密は今日と同じ経路のまま。** `AGENT_TOKEN` / `AF_SECRET_KEY` は `env_vars` 転送（上表 4 行目）で、
   値は RPC ペイロードに載らない。
5. 注入点は `threadStart` / `threadResume` / `threadFork` の 3 箇所。af 以外にセッション名は渡さない。
6. ⚠️ **`thread/resume` は `config.mcp_servers` を適用しない**（実測 0.147.0・Tier 2
   `TestLiveDriftCodexThreadMCPConfigAppliesOnResume`。rollout はターンが始まって初めて生まれ、
   無認証のターンは rollout を残さないので Tier 1 では測れず、実ターン 1 回＝約 19k tokens を
   使って測った）。**thread はスタートした時の MCP 設定を持ち続ける。**

   従って識別子が生き残るかは「その thread を誰がスタートしたか」で決まる:

   | 復旧の形 | af エントリのセッション名 |
   | --- | --- |
   | Agent 再起動・daemon は生存（adopt） | **保つ**（thread は start 時の設定のまま） |
   | daemon 自体が入れ替わる（クラッシュ・Restart） | **失う** → cwd＋生存の推定へ縮退 |
   | 新規セッション（thread/start） | 保つ |

   修復口も探したが無い: `thread/settings/update` は `mcp_servers` を**受け取るが何も変わらない**
   （同テストで測定）。bypass ポリシーの再表明と同じ手は使えない。

   縮退先は「同じ作業フォルダで生きているセッションが 1 つなら当てられる」ので、実害が残るのは
   **daemon 入れ替え × 同一ワークツリーに生存セッションが複数**の場合だけ。そこは取り違えずに
   拒否する（`mcpOwningSession`）。

**opencode managed には同等の口が無い**（実測 1.18.15、`contract_mcp_identity_test.go`）:

- `POST /session` の body は `parentID` / `title` / `agent` / `model` / `metadata` /
  `permission` / `workspaceID` のみで、MCP・config を渡す口が無い。`/mcp` 面（`/mcp`・
  `/mcp/{name}/connect` 等）のスコープは `directory` と `workspace` だけで session を取らない。
  MCP 設定はグローバルの `~/.config/opencode/opencode.jsonc` 一箇所（`McpLocalConfig` に
  `environment` はあるが、その値は daemon 全体で一つ）。
- **spawn の粒度はプロジェクトディレクトリ**。2 ディレクトリに 3 セッション（うち 2 つは同一
  ディレクトリ）を作ると MCP の子プロセスは **2 個**で、いずれも同じグローバル値を渡されていた。
  つまり **同じワークツリーを共有するセッション同士は MCP 子を共有する** — これは今回の障害
  そのものの形で、per-session の識別子を置く場所が無い。

従って opencode の managed セッションは当面 cwd + 生存の推定に頼るしかない。上記 2 本は
「口が生えたら落ちる」向きに書いてあり（`POST /session` に config 系プロパティが増える／
`/mcp` が session スコープを取る／spawn がセッション数に比例する）、解禁の検知はテスト側に任せる。

### 9.4 config

ネイティブ設定ファイルと既存 settings API（`internal/agents/codex/settings.go` の regex ベース原子的更新、
GET/PUT `/codex/settings`）は温存。反映は 3 層に整理される:

1. **ファイル＝永続既定**（反映は次 generation / 次 thread 起動時。daemon が thread 毎に読み直すかは §12）
2. **thread 起動パラメータ**（現行 `-c` の後継。**Codex の hooks 注入は丸ごと不要化**——状態はイベントで来る）
3. **動的変更**（`thread/settings/update` 等。稼働中セッションのモデル/effort 変更が初めて可能になる＝純粋な改善）

`hide_rate_limit_model_nudge` は恒久残置（CLI ルート用）。managed では構造的に無関係。
なお CODEX_HOME 参照が `rtk.go` 以外 `~/.codex` ハードコードという既存の不整合は、daemon 起動を AF が握る
この機会に `paths` 側で一本化する（実装時の随伴タスク）。

### 9.5 記録先

**read＝ネイティブストア正本 / live＝イベント / write＝API** の三層。managed 化しても rollout / SQLite の
書き手は各エージェント自身のままなので、`transcript.go` 群は無傷で温存され、過去 TUI セッションの履歴互換も
自動成立（`thread/resume` での実証は §12）。AF 側で会話イベントを二重永続化しない。永続化するのは
**会話内容を含まない運用メタデータ**のみ: turn 状態遷移・`ClientMessageID` 台帳・Interaction 監査・
generation 履歴。イベント欠落は §6 の手順で回復する。

置換されるヒューリスティクスの対応表:

| 現行 | managed での置換 |
|---|---|
| hooks の working/idle（status store） | `thread/status/changed`・`turn/completed` |
| `rolloutCompletedAfter` heal（`codex.go:120`） | 不要化（イベントが正） |
| `HasPendingQuestion` rollout tail probe（`codex.go:132`） | `request_user_input` イベント |
| usage.go の rollout rate-limit 解析 | `account/rateLimits/updated` |
| capture-pane スクレイプ（footer/update メニュー） | 不要化 |
| `RememberSid` hook 捕捉 | `thread/start` 戻り値 |

## 10. Console への影響 — paneless セッション（クリティカルパス）

managed が既定になるため、**ミラーが「読むだけ」から主 UI に昇格する**。P2 より前に必要（P1.5）。
承認 UI の新設は不要（§5——3 者とも bypass 運転で、扱うのは質問のみ）。CLI ルートのセッションは
既存のチャット⇔ターミナルがそのまま残る。

### 10.1 P1.5 実装記録（2026-07-15）

Console 側の受け皿・ワイヤ・Driver 層 IF を先行して確定した。**managed セッションは
まだ作れない**（作成 API は `driver:"managed"` を明示拒否し、P2 で解禁）。検証台は
OpenCode——といっても managed 接続はまだ無いので、実態は「意味論エンドポイントを
tui 委譲で 3 kind の実運用トラフィックに載せ、P2 が driver を差し込む前に型を枯らす」
という形。tui 委譲は kind 非依存なので claude / codex にも同じ経路が効いている。

- **Driver 層の型**（`internal/agents/driver.go`）: `Driver` / `ThreadHandle` /
  `Capabilities` / `TurnInput` / `ThreadSettings` / `Interaction` / `InteractionReply` /
  `TurnState` / `Event` / `ThreadSnapshot`。§3〜§5 からの精緻化は 2 点——`Send/Steer` が
  `TurnInput`（Prompt＋Attachments＋ClientMessageID）を取ること、`Interaction` が
  `Questions []transcript.Question` を持つこと（§5 追記）。`Event`/`ThreadSnapshot` は
  P2 の購読実装と同時に語彙を確定するため包括形に留めた。TUI ルートは ThreadHandle を
  **実装しない**（Events/Snapshot を持てない TUI に IF を無理に着せず、/turn ハンドラが
  tmux 経路へ直接委譲する）。
- **意味論エンドポイント**（`session_turn.go`）: `POST /sessions/{name}/turn`
  （op = start | steer | interrupt）と `POST /sessions/{name}/respond`（Interaction 応答）。
  per-agent REST は増やさず（§3.2）汎用 /sessions 面に追加、CP は素通しプロキシ。
  tui セッションは既存 tmux 経路（type+submit / Escape。opencode サブエージェント
  ビューの Up+Escape 特例含む）へ委譲し、ガード（not_running / question_pending /
  slash コマンドは working を付けない）は /input と同一に保つ。managed セッションは
  driver 未登録の間 501 `driver_unavailable` で正直に落ちる（P2 で `driverOf` に
  opencode を登録すると ThreadHandle へ流れる）。**/input（生 keys/seq）は CLI ルートの
  TUI モーダル駆動用として恒久に残る**——/turn = 意味論、/input = 生 TUI 駆動、という
  役割分担。
- **driver 軸のワイヤ化**: `session.Meta.Driver`（"" = tui。omitempty で既存メタと
  ディスク上バイト同一）→ wire `Session.driver` → Console `isManagedSession()`。
  作成 API に `driver` パラメータ（tui に正規化 / managed は `driver_unsupported` 拒否 /
  未知値は `bad_driver`）。`transcript.Question` に `id`（省略可）を追加——managed の
  pending question が応答先 Interaction id を運ぶ（tui 由来は空のまま＝従来動作不変）。
- **Console**: 送信を start / steer（送信時の実状態が working なら steer）に、停止
  ボタンを interrupt に接続（`core/api/client.ts` の `sessionTurn`）。**旧 Agent には
  /input へフォールバック**——フリート再ビルドのラグで新 Console↔旧 Agent の併存が
  実際に起きるため（404/405 時のみ legacy body で再送）。managed セッションでは
  Pane が TerminalView をマウントせず（存在しない PTY への WS を開かない）、ミラーを
  常時主 UI に、チャット⇔ターミナルのトグルは非表示。PendingQuestions に `onRespond`
  （semantic 経路）を追加——id 付き質問は keys/seq の TUI キー駆動を一切通らず
  /respond の構造化回答（質問ごとに text / options index）で答える。TUI モーダル由来の
  制約（multiPage 非対応 opencode の複数質問フォーム等）も semantic では消える。

### 10.2 pane 前提機能の棚卸しと置き換え設計

managed セッションに tmux pane は無い。pane に依存する機能の全量と置き換え:

| # | 機能（現状の pane 依存） | managed での置き換え | フェーズ |
|---|---|---|---|
| 1 | ターミナルビュー / ターミナル履歴（`/ws/pty`・`pipe-pane`→record-terminal のリング履歴） | 存在しない——ミラーが主 UI（Pane の非マウント化・トグル非表示は実装済み）。緊急の生操作は codex = CLI ルートへ排他切替（P3）/ opencode = TUIAttach（P2、無停止） | P1.5 ✅ |
| 2 | exit recording（docs/26・ADR 0014 の pane ラッパー `record-exit`。`$?` を wait status として解釈） | **supervisor 移設**: RuntimeSupervisor は daemon を自分の子プロセスに持つので `cmd.Wait()` の wait status が直接取れ、ラッパー自体が不要。daemon 死は kind 全体に波及するため記録は二層——(a) daemon レベル: wait status＋cgroup OOM 帰属（既存 `containerOOMKill` の baseline 比較を流用）を generation 履歴（§9.5）に、(b) thread レベル: `thread/status/changed` の `systemError` を該当セッションに。書き先は既存 `status.PersistExit`（session-exit ストア・reason enum 共用）のままにし、wire/Console（ExitReason chip）は不変 | P2 |
| 3 | 画像・ファイル添付（paste-image で保存→絶対パスを tmux 貼付） | 保存側（`~/.cache/agent-fleet/pasted/`）は pane 非依存で温存。**貼付だけ API 化**: `TurnInput.Attachments` で driver へ渡し、codex は `turn/start` の input items、opencode は serve API の添付形（§12-10 と併せて確認）。tui は従来どおり Console がプロンプトへパスを織り込む（受け口は実装済み・ワイヤの `attachments` は managed だけが解釈） | 受け口 ✅ / 実装 P2/P3 |
| 4 | TTS（ずんだもん/Polly のカラオケ朗読） | **置き換え不要**——トリガは転写ポーリング＋描画 DOM（turnTts）で pane 非依存。managed でもそのまま動く | — |
| 5 | mode chip（`paneMode` の capture-pane スクレイプ） | `thread/settings/updated`（P1 で観測済み）＋settings read からの射影。切替は `thread/settings/update`（§9.4-3。codex は experimentalApi 必須、§12.1-4） | P2/P3 |
| 6 | terminal-state（claude resume メニュー / 圧縮進捗バー / codex update メニューの capture-pane 検出） | managed では**発生しない**: resume = `thread/resume` RPC（メニューを出す TUI がいない）、圧縮 = item イベント（P1 観測済み）、update = supervisor の generation 更新（§7）に吸収 | 自然消滅 |
| 7 | status 自己治癒（`AtIdlePrompt` の capture-pane heal・`rolloutCompletedAfter`） | turn 状態機械（§4）が正になり不要化。切断時は unknown → reconciliation（§6） | P2 |
| 8 | graceful shutdown（`shutdown.go` が各 pane へ C-c） | ThreadHandle.Interrupt → daemon drain（§7）へ | P2 |
| 9 | 初回プロンプト配送（`deliverInitialPrompt` の composer 描画待ちスクレイプ＋二重 Enter） | 不要化——thread/start 直後に turn/start を投げられる（boot 画面に食われる readiness 問題が存在しない）。`ClientMessageID` で冪等 | P2 |
| 10 | queued steering の可視化（opencode session_input / claude queue-operation の再構成） | turn 状態機械の `queued`＋ClientMessageID 台帳（§9.5 の運用メタデータ）が正 | P2 |
| 11 | SSM ログイン検出（capture-pane 全 scrollback の regex） | 対象外——shell / ssm は pane 前提のまま（managed 化しない） | — |
| 12 | セッション開始・停止（tmux new-session / kill-session、Console の 再開して続ける → /start） | Driver.Resume / supervisor 経由の thread 管理へ。managed の /start・/stop・halt は P2 で driver 分岐を足す | P2 |

切り分け: **実装まで必要**（managed セッションが動く条件、P2）= 2・5・7・8・9・10・12 — **P2 で全て実装済み**
（2 = supervisor の cmd.Wait ＋ daemon 死時の per-session PersistExit、5 = TranscriptData.Mode 射影＋
POST /sessions/{name}/settings、7 = turn 状態機械が正・unknown→reconcile、8 = AbortManaged＋
Supervisor.Shutdown、9 = 作成ハンドラが handle.Send を直接呼ぶ、10 = driver 内キューを Queued へ合流、
12 = create/start/stop/halt/archive/recreate/fork/list の driver 分岐）。
**インターフェース整備で足りた**（P1.5 完了）= 1・3（3 の実装も P2 で完了 — TurnInput.Attachments →
v1 file part）。**無変更で成立** = 4・11。**自然消滅** = 6。

## 11. フェーズ計画

| フェーズ | 内容 | リスク |
|---|---|---|
| **P1 ✅** | **Codex 観測拡張（read-only）済（2026-07-15）**: 既存 observer（`handleCodexAppServerEvent`）に `account/rateLimits/updated`・`model/rerouted`・`thread/settings/updated`・`warning`・`thread/status/changed` を追加（key=value の構造化ログ・連続重複抑止）。rate limits は `/codex/usage` が rollout 読みと鮮度比較して新しい方を採用。**追加で必要になったもの**: thread スコープ通知はアタッチ必須（§12.1-1）のため、observer が `thread/resume` でアタッチする `codexObserver`（thread/started・thread/status/changed・30 秒毎 `thread/loaded/list` sweep がトリガ）。発端バグの「TUI 層ナッジ vs サーバ側 reroute」を切り分ける（切り分け規準は §12.1-2）。CLI ルートにもそのまま効く | ゼロ（制御なし。observer の thread/resume は rollout 不変を実測確認） |
| **P1.5 ✅** | **Console managed セッション UI の受け皿**（§10.1）済（2026-07-15）: Driver 層 IF（`internal/agents/driver.go`）＋意味論エンドポイント `/turn`・`/respond`（tui は tmux 経路へ委譲・managed は driver 登録まで 501）＋driver 軸のワイヤ化（Meta/wire/作成 API、managed 作成は P2 まで拒否）＋Console の paneless 描画（TerminalView 非マウント・トグル非表示）と送信 start/steer・停止 interrupt・質問 onRespond 導線（旧 Agent へは /input フォールバック）。pane 前提機能の棚卸しと置き換え設計は §10.2 | UI のみ（tui は既存経路へ委譲、挙動不変） |
| **P2 ✅** | **OpenCode managed 化** 済（2026-07-15、§12.2）: Driver＋RuntimeSupervisor（`internal/agents/opencode` の driver.go / serve.go）＋turn 状態機械＋reconciliation＋`ClientMessageID` 台帳＋Interaction(question)＋generation の初出。§10.2 の 2・3・5・7〜10・12 と managed 作成の解禁・ドライバ選択 UI（opencode の新規既定 = managed、CLI はメモリコスト表示付きの明示選択）。E2E 実機検証済み: 作成→初回プロンプト→question（id 付き）→/respond→steer キュー→interrupt→halt/start→daemon SIGKILL→exit 記録(137)→自動 reconcile(gen++)。**TUI 併用は attach 実験で安全性実証**（§12.2-9）したが、**CLI ルートの serve アタッチ化自体は見送り P3 へ**（§12.2-11: plugin の sid 捕捉が attach では serve プロセス側で走り壊れる・attach に --model が無い、の 2 点の解決が先） | 低（排他不要） |
| **P3 ✅** | **Codex managed 化** 済（2026-07-15、§12.3）: app-server writer Driver＋RuntimeSupervisor（daemon所有・generation・drain・exit記録・自動reconcile）、native steer / interrupt / settings / Interaction(question)、kind共通の永続 `ClientMessageID` 台帳。Codex新規既定=managed、CLIメモリコスト表示、既存セッションの `POST /sessions/{name}/driver` 双方向排他切替（busy 409、stop→resume）。実バイナリE2Eで managed作成→settings→halt/start→managed⇄TUI→turn/interrupt→busy切替拒否→daemon SIGKILL→exit 137→gen++ reconcile を確認 | 中→解消（単一 writer排他と実機互換を実証） |
| （凍結） | Claude Session Manager（付録 A）。CLI 必須の運用が解消されたら再訪 | — |

TUI 経路（send-keys / hooks / probe）の**撤去はしない**——CLI ルートの実装として保守対象で残る
（既定 managed 化で負担は漸減）。read 層・status store・tmux プラミングは Claude 分も含め現役。

## 12. 要検証項目（実装前・CLI 0.144.3）

1. **server 経由で作成した thread を TUI で `codex resume` できるか**——双方向排他切替の成立条件（筆頭）。
   **P3 で成立を実証（§12.3-2）**。managed 作成 thread と旧 TUI rollout の双方で双方向に継続できる。
2. **TUI でしか起きない対話の棚卸し**: ログイン失効・アップデート案内・確認プロンプト等を列挙し、
   app-server / serve での対応物（イベント化 / 発生しない / auth エラー化）を確認。対応物なしの発見時の
   保険が CLI ルート（Codex）/ TUIAttach（OpenCode）
3. `--remote` 接続時の TUI クライアントの実 RSS（現状 A 構成の実コスト）
4. 旧 TUI rollout の `thread/resume` 互換（履歴互換の実証）——**P3 で実証（§12.3-2）**
5. daemon の auth.json / config.toml 読み直しタイミング（ホットリロード可否。§7 の設計は可否に依存しないが最適化余地）
6. `thread/start` で承認バイパス相当ポリシー・モデル・workdir を指定できる範囲——
   **P3 で実証（§12.3-1・5）**。cwd/model/approvalPolicy/sandboxPolicy は指定可能。ただし resume 後は
   policy の再表明が必要。
7. `model/rerouted` が managed でも発生するか（ナッジ根治の裏付け）——§12.1-2 で部分回答:
   0.144.4 に rate limit 起因の reroute はそもそも存在しない
8. daemon kill→再起動→`thread/resume` で実行中 turn がどうなるか（§6 の実挙動）——
   **P3 で実証（§12.3-6）**。実行中 turn は interrupted で確定し、同じ thread を resume して継続可能。
9. `codex fork` 相当の RPC 有無（`Caps.CanFork` 維持）——回答: `thread/fork` が base スキーマに存在（0.144.4）
10. opencode serve のローカル API 認証有無・TUI アタッチの起動形態（フラグ・tmux 内挙動）・serve 障害時の
    スタンドアロン TUI フォールバック判定 —— **P2 で回答済み（§12.2-1・§12.2-9）**: 認証は
    `OPENCODE_SERVER_PASSWORD`（未設定＝無認証・loopback 運用）、アタッチは
    `opencode attach <url> --session <id> --dir <dir>`（tmux 内で正常動作・履歴完全描画）。
    フォールバック判定は CLI ルートのアタッチ化と併せて P3 送り（§12.2-11）

### 12.1 P1 実装時の実測事実（2026-07-15、CLI 0.144.3 / 0.144.4 両方で確認）

P1 の実装前検証（`codex app-server generate-json-schema` スキーマ突き合わせ＋隔離 CODEX_HOME での
実 app-server プローブ）で判明した事実。**0.144.3 と 0.144.4 で配信規則・対象イベント名に差分はない**
（両バージョン実測。なお image は 0.144.3 焼き込みだが entrypoint が起動毎に latest へ更新するため、
実フリートは常に最新で動く）。

1. **thread スコープ通知はアタッチ必須（本設計全体に効く最重要事実）**。`item/*`・`turn/*`・
   `thread/settings/updated`・thread 宛 `warning` は「そのスレッドをロードした接続」にのみ配送され、
   受動的な接続に届く broadcast は `thread/started`（新規作成時のみ。**resume によるロードでは発火しない**）と
   `thread/status/changed`（ロード時 notLoaded→idle 等）程度。観測には observer 自身の `thread/resume` が
   必要で、実行中スレッドへの resume は in-memory インスタンスへの合流＝**rollout を変更しない**
   （sha256 実測）。TUI との二重アタッチも互いに影響しない（両接続が全通知を受信）。
   帰結: b5ee735 の圧縮検知はアタッチなしでは不発だった（P1 で修正）。P2/P3 の Driver 購読も
   「resume＝subscribe」を前提にできる。
2. **発端バグの切り分け規準**: `ModelRerouteReason` は 0.144.4 でも enum `highRiskCyberActivity` のみ＝
   **rate limit 起因のサーバ側 reroute は存在しない**。したがって利用率 93〜99% で起きた発端バグは
   TUI 層ナッジでほぼ確定で、再発時は観測ログの「`thread/settings/updated` の model 変化あり＋直前に
   `model/rerouted` なし」で確証が取れる。
3. **rate limits の実データ形**（実 auth で `account/rateLimits/read` 実照会）: `resetsAt` は epoch 秒・
   `usedPercent` は整数・現アカウントは weekly ウィンドウが `primary` に入り `secondary: null`
   （usage.go が既に警告していた新型口座構成の実例）。`rateLimitReachedType`・`credits`・`planType` 付き。
   read 応答には `rateLimitResetCredits`（Full reset クレジット）も含まれる——将来 usage.go の
   chatgpt.com backend 直叩き（`fetchResetCredits`）を app-server 経由に置換できる候補。
   なお `account/rateLimits/updated` は**スパースな rolling update**（スキーマ明記: nullable は
   「変化なし」）なので、観測値は丸ごと上書きでなく non-nil マージで保持する（P1.5 レビューで修正）。
4. **`thread/settings/update`（write）は experimentalApi capability 必須**（initialize の
   `capabilities.experimentalApi: true` がないと -32600）。base スキーマ外（`--experimental` でのみ生成）。
   P3 の write 実装はこの capability を前提にする。`turn/start`・`turn/steer`・`turn/interrupt`・
   `thread/compact/start` は base に存在。
5. **rollout はスレッドの初回 turn まで書かれない**: 作成直後のスレッドへの `thread/resume` は
   "no rollout found" で失敗する。observer は sweep（30 秒毎 `thread/loaded/list`）でリトライして拾う。
6. **`ThreadStatus` の語彙**: `notLoaded | idle | systemError | active{activeFlags:
   [waitingOnApproval|waitingOnUserInput]}`。§9.5 の hooks 置換（working/idle/question）は
   `active`/`idle`/`activeFlags=waitingOnUserInput` からの射影で成立する見込み。
7. **未確認のまま残るもの**: `account/rateLimits/updated` が観測接続（turn を駆動していない接続）にも
   配送されるか——実 turn 消費なしでは検証できず、実フリートのログで確認する。届かない場合も
   `/codex/usage` は rollout 読みへ自然にフォールバックする（鮮度比較）。

### 12.2 P2 実装時の実測事実（2026-07-15、opencode 1.17.18）

P2 の実装前プローブ（隔離 XDG ホームで `opencode serve` を起動し OpenAPI spec `GET /doc` と
実呼び出しで確認）と、実装後の実バイナリ E2E（隔離 HOME の workspace-agent＋実 serve＋zero-auth
フリーティア実 turn）で判明した事実。**driver 実装（`internal/agents/opencode/driver.go`・`serve.go`）
の API 選定はすべてここに根拠がある。**

1. **serve の認証は `OPENCODE_SERVER_PASSWORD`**（basic auth）。未設定なら無認証で、起動ログに
   "server is unsecured" 警告が出る。コンテナ network namespace 内 loopback のみなので §9.1 の判断
   （codex :7798 と同じ）どおり無認証で運用。`opencode attach` も同じ資格（`-p` / env）で接続する。
2. **API は二世代が併存する（最重要）**: v1（`/session/...`）と v2（`/api/...`、`session.next` 系）。
   **v2 は新ストア（`session_message` / `session_input` テーブル）に書き、read 層の正本
   （`message` / `part`）には書かない**。TUI（1.17.18、attach 経由含む・実測）は v1 で `message`/`part`
   に書く。実フリートの opencode.db も message=多数/最新 vs session_message=残骸 2 行で一致。
   → **AF driver は v1 系で駆動する**（read 層温存の唯一の選択肢）。
3. **turn 駆動は blocking `POST /session/{id}/message` が唯一の口**。応答は AssistantMessage＋parts
   （完走まで返らない — driver は goroutine で包む）。`prompt_async` は user message を書くだけで
   turn が始まらない（loop step=0 で即 exit、実測）— **真因は turn ループが message id の辞書順に
   依存する**こと: 既存 id より辞書順で小さい messageID の user message は「処理済み」扱いになる。
   クライアント採番の `msg_af...` は serve 採番の `msg_f6...` より小さいため常に無視される
   （blocking でも同じ id を渡すと /message が返らなくなる — E2E で実証）。
   → **messageID は serve 採番に任せ、`ClientMessageID` の冪等化は driver の台帳で行う**。P2 時点では
   handle 生存中の in-memory 台帳だったが、P3 で kind 共通 `agents.MsgLedger`（セッション別・上限200件の
   ファイル台帳）へ移し、Agent / daemon 再起動を跨ぐ §9.5 の永続化まで完了した。
4. **v2 の `delivery:"steer"` は実行中の v1 turn に注入されない**（session_input に admit された後、
   v2 側で独自の turn を開始し session_message に書く — 実測）。v1 に mid-turn steer の口は無い。
   → **Steer は driver 内キュー**（実行中 turn の完走後に次 turn として投入 — §4 の queued 状態
   そのもの）で実装。キューは `TranscriptData.Queued` に合流し キュー済み バッジに出る。
   interrupt はキューも破棄する（停止の意思はキューに及ぶ）。
5. **interrupt = `POST /session/{id}/abort`**: blocking /message は 200＋部分結果で返り、assistant
   message に `time.completed`＋error が刻まれる → `sessionResumable` は真に戻る（resume 安全）。
   `session.error`＋`session.idle` イベントが飛ぶ。
6. **question 系は完全に構造化**: `question.asked/replied/rejected` イベント＋`GET /question`
   （QuestionRequest: id=`que_*`・questions[]{question,header,options{label,description},multiple,custom}）
   ＋`POST /question/{id}/reply {answers:[[label,…]]}`（**回答はラベル文字列**、index ではない —
   driver が InteractionAnswer の index→label 変換を持つ）/`{id}/reject`。実行中の question tool は
   part（state=running）として `message`/`part` にも見える → 既存 pending() reader がそのまま動き、
   Interaction id は driver が SSE から補完する（`transcript.Question.ID`）。reply 後 turn は続行。
7. **session/question/status/event 面はプロジェクト（directory）スコープ（第 2 の最重要事実）**:
   serve の cwd と別ディレクトリのセッションは、`?directory=<dir>` を付けないと `GET /question`・
   `GET /session/status` に載らず、素の `GET /event` にはそのセッションのイベントが届かない。
   → driver は全呼び出しに directory を併送し、**SSE はプロジェクト横断の `GET /global/event`**
   （イベントを `{"payload": {...}}` に包んで全プロジェクト分配信）を購読する。
   `/session/status` は idle のセッションを省略する（載っている＝busy/retry）。
8. **添付は v1 file part** `{type:"file", mime, url:"file:///abs"}` で動く（実測: モデルが添付
   ファイルの中身を読んで回答）。`TurnInput.Attachments` → file part 変換（§10.2-3 の実装）。
9. **TUI アタッチ（TUIAttach cap）実証**: `opencode attach http://127.0.0.1:7799 --session <id>
   --dir <dir>` で AF 駆動の会話が完全描画され、attach TUI からの送信も v1（message/part）に書く
   — **managed セッションと TUI の併用は安全**（相互に全ターンが見える）。RSS 実測: serve 本体
   557 MiB（共有・1 プロセス）、attach TUI 約 304 MiB（3 MiB ラッパー＋301 MiB 子）。
   managed セッション自体は per-session プロセス 0。
10. **permission**: serve 既定（1.17.18）では bash 実行も permission prompt なしで通った。保険として
    driver は `permission.asked` イベントへ `{"reply":"always"}` を自動応答する（ユーザー config が
    ask を足しても managed セッションが黙って固まらない — bypass 運転の維持、§5）。
11. **CLI ルートの serve アタッチ化は P2 では見送り**（P3 で再訪）: (a) AF_SESSION_SID による
    per-slot sid 捕捉は plugin が **serve プロセス側**で走るため attach では機能しない（AF が
    session を API で先に作り `--session` を渡せば解決可能 — 設計余地）、(b) `opencode attach` に
    `--model` フラグが無い（session 作成時の model 指定で代替できるかは未検証）。スタンドアロン
    TUI は従来どおり動くので可用性の後退は無い。
12. **E2E 検証済みフロー**（実バイナリ・実 serve・実 turn）: managed 作成（POST /sessions
    driver=managed）→ 初回プロンプト直接 Send（§10.2-9、boot 待ちなし）→ ミラー転写（message/part
    経由）→ question（id 付き pendingQuestions）→ /respond 構造化回答 → turn 続行 → steer の
    キュー可視化と完走後投入 → interrupt（idle 復帰・resume 安全）→ halt→stopped / start→alive →
    **daemon SIGKILL → supervisor が exit 記録（code 137/sig 9・cgroup OOM 帰属判定つき）→ 自動
    reconcile（gen++・セッション復活）**。serve のコールドブート直後は最初の session create が
    10 秒を超えることがある（bun 起動・カタログ初期化）— 作成 API は 502 を返し、リトライで成功する。

### 12.3 P3 実装時の実測事実（2026-07-15、Codex CLI 0.144.4）

P3 の実装前に、隔離 `CODEX_HOME` へ実アカウントの auth だけを渡した app-server と専用プローブで
write 面・切断回復・TUI 互換を検証した。会話正本は実 rollout JSONL を使い、ダミー応答ではなく実モデルの
turn 完走まで確認した。以下が `internal/agents/codex` の Driver/Supervisor と排他切替の根拠である。

1. **write 面は構造化 RPC だけで完結する**: `thread/start` → `turn/start` で実 turn が完走し、応答は
   turn id を即返した後、`turn/started` / `turn/completed` 通知で状態が確定する。`turn/steer` は
   `expectedTurnId` が必須で、実行中の同一 turn へ追撃を注入する native mid-turn steer（別 turn 化しない）。
   `turn/interrupt {threadId,turnId}` は `turn/completed.status=interrupted` に確定する。
   `thread/settings/update` は initialize の `capabilities.experimentalApi=true` が必須で、無い接続では
   JSON-RPC -32600。宣言した接続では model / effort の動的変更が動作した。
2. **双方向の排他切替が成立する（§12-1 の筆頭条件を解消）**: app-server で作成・駆動した thread を
   TUI の `codex --remote <addr> resume <threadId>` で開くと全履歴が表示され、その TUI から次 turn を実行できた。
   TUI を停止した後、managed 接続で同じ thread を resume し次 turn を継続できた。逆方向も同じ正本を保つため、
   「managed → TUI → managed」の stop→resume 排他切替を実装可能。既存フリートの旧 TUI rollout
   （約199 KiB の実会話）も app-server の `thread/resume` と TUI `--remote resume` の双方で全履歴を読み、
   新しい turn を継続できた——read 正本を動かさない履歴互換が実証された。
3. **質問は server→client JSON-RPC request**: method は `item/tool/requestUserInput`、params は
   `threadId` / `turnId` / `itemId` / `questions[{id,header,question,isOther,options[]}]`。client は同じ request id へ
   `{answers:{<questionId>:{answers:[<label or free text>,…]}}}` を返すと turn が続行する。ただし app-server thread
   では request_user_input が既定無効で、`thread/start` / `thread/resume` の thread 単位 config に
   `features.default_mode_request_user_input=true` が必要（無い場合は tool が unavailable）。
   **追記（2026-07-17 実測・訂正）**: これは app-server 固有ではない。**TUI（CLI ルート）も Default mode
   では同じく既定無効**（"The request_user_input tool is unavailable in Default mode." と返り、モデルは
   質問せず自答する）で、当初の「TUI は自前で有効化している」という前提は誤りだった。Plan mode のみ
   自動で有効。よって CLI ルートでも `buildProgram` が同じ feature を `-c` で渡す（無いと
   `HasPendingQuestion` が拾う function_call が rollout に一切現れず、質問あり状態が永久に点かない）。
   codex 0.144.3（ピン）/ 0.144.5（実効）双方で確認。
4. **未応答質問の reconciliation は runtime 自身で成立する**: 質問 request を未応答のまま writer 接続を
   切り、別接続から同じ thread を feature config 付きで resume すると、未解決の request が新しい接続へ
   再配送された。AF 側で質問イベントを journal/replay する必要はなく、Interaction id は安定した `itemId`、
   JSON-RPC request id は接続世代ごとの値として持てばよい。回答済みになると `serverRequest/resolved` も届く。
5. **resume のたびに sandbox / approval policy を再表明する必要がある**: `dangerFullAccess` で作成した thread が、
   別接続からの resume 後に `readOnly` 相当へ落ちる例を実測した。`thread/resume` だけを信用せず、直後に
   `thread/settings/update {approvalPolicy:"never", sandboxPolicy:{type:"dangerFullAccess"}}` を送り直すと書き込みが
   再び通る。managed driver は start 時の指定に加え、全 resume / reconciliation でこの再表明を行う。
6. **daemon kill 後は曖昧な実行中 turn が残らない**: 実 turn 中の app-server を SIGKILL し、daemon を再起動して
   同じ thread を resume すると、kill 時の turn は rollout 上 `interrupted` に確定しており、thread は次 turn を
   受け付けた。したがって Supervisor は exit code 137 / signal 9 を記録し generation を進め、handle を一度
   `unknown` に落としてから resume snapshot で回復できる。会話の鋳直しや二重投入は不要。
7. **実装後の実バイナリE2E**: 隔離 HOME / `CODEX_HOME` と専用portの workspace-agent＋実 app-server で、
   managed作成 → effort / plan settings更新 → halt→start → managed→TUI→managed（tmux writerが排他的に消える）→
   turn/startの同一`ClientMessageID`再送dedupe → interruptでidle復帰 → 実行中のdriver切替が409 `busy_switch` →
   daemon SIGKILLで一時 exit code 137 / signal 9記録 → writer gen 2接続＋managed session自動復帰、を通した。
   この縦串は認証領域を読まない隔離環境なのでturnは401警告後にinterruptしたが、実モデル完走・質問応答・
   native steerは同じ0.144.4の認証付き事前プローブ（本節1〜4）で別途成立済み。検証後はAgent/app-server/tmuxを
   すべて停止・削除し、常駐プロセスを残していない。

実装上の帰結: Codex managed は app-server 接続を唯一の writer とし、TUI と同時駆動しない。既存セッションの
切替 API は実行中/queue 中を 409 で拒否し、旧 writer の stop→新 driver の resume の順を強制する。writer と
P1 observer は別 WebSocket のままにし、後者は CLI ルートを含む read-only 観測を継続する。

---

## 付録 A. Claude Session Manager 案（凍結・将来オプション）

> ユーザー判断（2026-07-15）: Claude は compact への応答等で CLI 操作が必要なため現状の TUI を維持。
> 以下は「CLI 必須の運用が解消されたら再訪」するための設計温存。

Workspace Agent 内に Claude Session Manager を置き、各セッションを `claude -p --input-format stream-json
--output-format stream-json`（Agent SDK 相当）の子プロセスとして管理する。1 プロセス集約は不可なので、
**メモリの得は共有でなく idle eviction から出る**:

- 状態: `starting | running | waiting_interaction | idle | evicted | recovering | stopped | failed`
- eviction ポリシー: soft idle TTL（例: 5 分）＋warm 上限（最近使った N 件のみ常駐）＋
  **メモリ圧駆動の即時 evict**（cgroup `memory.current`/`memory.events` 監視）＋LRU。
  **ガード**: `BackgroundBusy`（`LiveInfo` 既存）・pending question・実行中 turn は落とさない
- `recovering`: evicted→次入力→`--resume <sid>` respawn 中。この間の入力は `queued`（`ClientMessageID` 付き）へ
- AF が決定的 sid を握っている（`--session-id` ピン、fork も `--fork-session --session-id`）ため
  evict→resume の対応表管理は 3 エージェント中で最も簡単
- 子プロセスは短命なので**次 spawn が新 credentials / settings を自然に拾う**＝再認証・設定反映に最も強い
  （generation の drain が per-session で局所的）
- 実装注意（suqhrov）: stdin 書き込みのセッション毎直列化、stdout NDJSON の行単位デコード、
  子プロセス終了と最終 result 受信の区別、permission 待ちの別チャネル化、stderr は診断ログへ、
  graceful shutdown→session id resume、同一 sid の二重 resume 禁止
- Remote Control とは排他（`managed` | `rc-interactive` を作成時選択）。stream-json 子プロセスの RC 同時公開
  可否は未検証（できなくても設計は成立）

## 付録 B. メモリ実測（CLI 0.144.3、アイドル時 RSS、suqhrov 実測）

| 構成 | Node | Codex 本体 | 合計 |
|---|---:|---:|---:|
| 現在の TUI 1 セッション | 約 48 MiB | 約 232 MiB | 約 280 MiB |
| app-server 1 プロセス（共有基盤） | 約 48 MiB | 約 81 MiB | 約 129 MiB |

thread ごとの会話状態・実行中ツールプロセスのメモリは別途乗る。`--remote` 時の TUI クライアント RSS は
未実測（§12-3）。
