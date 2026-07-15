# 27. エージェント制御の Managed Driver 化（TUI スクレイプ → 共有 runtime＋構造化 RPC） — 設計

**Status: 📋 設計確定・実装未着手** — 2026-07-15 起草。並行設計セッション（sol=A / fable=B）の成果を比較し、
B の骨格に A の部品を移植する形でユーザー裁定により統合・確定した（経緯は [decisions/0015](decisions/0015-agent-managed-driver.md)）。

> 発端は Codex TUI のモデル勝手切替バグ（週次利用率 93〜99% で `ThreadSettings` が 3 件連続送信され
> `gpt-5.6-sol` → `gpt-5.4-mini` へ意図せず切替→直後にコンテキスト圧縮。複数セッションで再現）。
> 暫定対処の `[notice] hide_rate_limit_model_nudge` トグルは main マージ済み（`ac8c202`）。
> 本書はその根本対処＝「端末スクレイプ＋キー入力エミュレーション」からの脱却を、
> Codex 単体でなく 3 エージェント（codex / opencode / claude）横断のアーキテクチャとして設計する。

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
（`program.go:51-54`）、AF は第 2 の WebSocket 接続で **read-only オブザーバ**として `contextCompaction` の
item lifecycle のみ検知する（`a6db76c`）。書き手は TUI だけなので競合はない。app-server 起動失敗時は
従来の直接 TUI へフォールバックする（可用性優先）。

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
    Send(prompt string, clientMessageID string) error   // 重複送信防止（§4）
    Steer(prompt string) error
    Interrupt() error
    UpdateSettings(s ThreadSettings) error               // モデル / effort / mode
    Respond(interactionID string, d Decision, s Scope) error  // §5
    Events() <-chan Event
    Snapshot() (ThreadSnapshot, error)                   // reconciliation 用（§6）
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
type Interaction struct { ID, Kind, Prompt string; Options []Option }
type Decision string  // allow | deny | cancel | answer
type Scope    string  // once | turn | thread
```

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
毎ターン起動させない・`~/.codex/sessions` を汚さない）が壊れるため、**thread 単位 config で隔離相当を
再現できると確認できるまで見送り**。将来の受け皿は Capabilities の `EphemeralThread` として予約。

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

managed が既定になるため、**ミラーが「読むだけ」から主 UI に昇格する**。P2 より前に必要（P1.5）:

- プロンプト送信を `turn/start` / `turn/steer` へ、中断ボタンを `turn/interrupt` へ接続
- Interaction（question）応答 UI——既存 Pending UI の流用
- モデル / effort 切替を `thread/settings/update` へ（Capabilities で出し分け）
- 添付（画像 / ファイル）を tmux 貼り付けから API 添付へ
- exit recording（docs/26 の pane ラッパー方式）の supervisor 移設
- ターミナルビュー / ターミナル履歴の扱い（managed セッションには無い）と「pane なしセッション」の見え方

承認 UI の新設は不要（§5——3 者とも bypass 運転で、扱うのは質問のみ）。CLI ルートのセッションは
既存のチャット⇔ターミナルがそのまま残る。

## 11. フェーズ計画

| フェーズ | 内容 | リスク |
|---|---|---|
| **P1** | **Codex 観測拡張（read-only）**: 既存 observer（`handleCodexAppServerEvent`）に `account/rateLimits/updated`・`model/rerouted`・`thread/settings/updated`・`warning`・`thread/status/changed` を追加。発端バグの「TUI 層ナッジ vs サーバ側 reroute」を切り分ける。CLI ルートにもそのまま効く | ゼロ（書き込みなし） |
| **P1.5** | **Console managed セッション UI**（§10）。OpenCode を検証台に | UI のみ |
| **P2** | **OpenCode managed 化**: Driver＋Supervisor＋turn 状態機械＋reconciliation＋`ClientMessageID`＋Interaction(question)＋generation の初出。serve は TUI 併用可＝最も安全に「AF が書く」を実証できる。CLI ルートの serve アタッチ化も同時（TUI 併用の実証を兼ねる） | 低（排他不要） |
| **P3** | **Codex managed 化**: 新規既定→ドライバ選択 UI＋既存セッションの排他切替。Driver 第 2 実装で型の妥当性検証。daemon drain 実装 | 中（単一 writer 排他） |
| （凍結） | Claude Session Manager（付録 A）。CLI 必須の運用が解消されたら再訪 | — |

TUI 経路（send-keys / hooks / probe）の**撤去はしない**——CLI ルートの実装として保守対象で残る
（既定 managed 化で負担は漸減）。read 層・status store・tmux プラミングは Claude 分も含め現役。

## 12. 要検証項目（実装前・CLI 0.144.3）

1. **server 経由で作成した thread を TUI で `codex resume` できるか**——双方向排他切替の成立条件（筆頭）。
   不可なら縮退案「managed 作成セッションは managed 固定、CLI 作成のみ双方向」
2. **TUI でしか起きない対話の棚卸し**: ログイン失効・アップデート案内・確認プロンプト等を列挙し、
   app-server / serve での対応物（イベント化 / 発生しない / auth エラー化）を確認。対応物なしの発見時の
   保険が CLI ルート（Codex）/ TUIAttach（OpenCode）
3. `--remote` 接続時の TUI クライアントの実 RSS（現状 A 構成の実コスト）
4. 旧 TUI rollout の `thread/resume` 互換（履歴互換の実証）
5. daemon の auth.json / config.toml 読み直しタイミング（ホットリロード可否。§7 の設計は可否に依存しないが最適化余地）
6. `thread/start` で承認バイパス相当ポリシー・モデル・workdir を指定できる範囲
7. `model/rerouted` が managed でも発生するか（ナッジ根治の裏付け）
8. daemon kill→再起動→`thread/resume` で実行中 turn がどうなるか（§6 の実挙動）
9. `codex fork` 相当の RPC 有無（`Caps.CanFork` 維持）
10. opencode serve のローカル API 認証有無・TUI アタッチの起動形態（フラグ・tmux 内挙動）・serve 障害時の
    スタンドアロン TUI フォールバック判定

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
