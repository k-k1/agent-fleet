# 75. アイドル自動停止の条件整理と、保留中の対話の持ち越し

> 状態: **設計検討（未実装）**。実装に着手する時点で ADR 0055 を起票する。
> 前提となる既存実装は [history/p3-9-idle-stop](history/p3-9-idle-stop.md)（二段構えの導入）と
> `control-plane/reaper.go`（現在は四段）。関連: [47](47-turn-abort-auto-resume.md)（limited /
> spend_limit / auth の live 状態）、[64](64-ec2-persistent-workspace.md)（停止＝スロット解放）、
> [38](38-scheduled-execution.md)（停止中を起こす既存経路）。

## 75.1 何が起きているか

**症状**: AskUserQuestion（以下 AUQ）が出たまま放置されたセッションがあると、その Workspace は
**永久に停止しない**。ECS / EC2 プールでは、止まらない Workspace はそのまま課金される
（[64](64-ec2-persistent-workspace.md) §64.26 の実測では、誰も触っていない Workspace が
一晩 9.4 時間 m7i.large を占有していた）。

原因は 1 行である。`reaper.go:368`（tier2 の再判定は `:489`）:

```go
if s.Alive && (s.State == "working" || s.State == "question") {
    busy = true
}
```

`question` は **busy** に数えられる。したがって

- tier1（セッション halt）は `reapableIdle` に `question` を含まないので畳まない（`reaper.go:646`）
- tier2（Workspace 停止）は busy を見て止めない

の両方が閉じ、**人が答えるまでコンテナが起き続ける**。これは 2026-07-31 に
利用上限メニュー（`blocked`）で実測された「1 セッションが busy を立て続けて約 16 時間コンテナを
占有した」障害と**同じ形**であり、そのときは `blocked` を busy から外すことで塞いだ。
question は当時「すぐ答えが返ってくる対話」として意図的に busy のまま残された。実運用では
質問は夜間をまたぐ。前提が違っていた。

**課金に効くのは tier2 だけ**である点も押さえておく。tier1 の halt はコンテナ内の RAM を返すだけで、
ECS のタスクも EC2 スロットも保持されたままになる（reaper 冒頭の設計コメントどおり）。
「止まらないので高い」の対象は常に tier2 である。

## 75.2 現状の条件（コードからの棚卸し）

### 75.2.1 状態の一覧

live 状態は 9 つ（`internal/agents/notify.go` と `internal/status/status.go`）。
`permission` は AUQ / ExitPlanMode 自身の permission_prompt で上書きされるため、
読む側は必ず `status.EffectiveModal`（question > plan > permission）を通す。

| state | 意味 | 機械は動くか |
|---|---|---|
| `working` | ターン実行中 | 動く |
| `idle` | 入力待ち | 動かない |
| `question` | AUQ のモーダルが出ている | 人待ち |
| `plan` | ExitPlanMode の承認待ち | 人待ち |
| `permission` | ツール許可待ち | 人待ち |
| `blocked` | 利用上限メニューが出ている（人がキーで消すまで動かない） | 人待ち |
| `auth` | claude のログイン期限切れ（Workspace 単位の事実） | 人待ち |
| `limited` | 利用上限のリセット待ち | 時計待ち |
| `spend_limit` | 支出上限（増枠されるまで動かない） | 人待ち |

これに wire 上のフラグ `backgroundBusy`（`run_in_background` のジョブ・in-process の
サブエージェント / Workflow・S 状態の背景シェル）が直交する。

### 75.2.2 reaper が実際に見ているもの

| state | tier1 が halt するか | tier2 の停止を止めるか | 結果 |
|---|---|---|---|
| `working` | しない | **止める** | 正しい |
| `idle` | **する** | 止めない | 正しい |
| `limited` / `spend_limit` | **する** | 止めない | 正しい（docs/47 §4-9/§4-10 で追加） |
| `question` | しない | **止める** | **止まらない＝本件** |
| `plan` | しない | 止めない | **黙ってコンテナごと落ちる** |
| `permission` | しない | 止めない | 同上 |
| `blocked` / `auth` | しない | 止めない | tier2 でしか回収されない（tier1 の安い回収を逃す） |
| `backgroundBusy` の idle | **する** | 止めない | **走っている背景作業を殺す** |
| kind != claude | **しない**（`reaper.go:376`） | question なら止める | managed / 他 kind の人待ちは回収経路が無い |
| shell / ssm | しない（state は空） | 止めない | 走行中のビルドは tier2 で落ちる（p3-9 で承知の割り切り） |

### 75.2.3 「起きている」を作るもう一つの入口＝在席シグナル

tier2 は state のほかに `connRegistry` を見る（`reaper.go:400`）。

- `wsConns > 0`（端末 WS / preview / browser ペインの長命接続が 1 本でもある）→ **停止しない**
- `lastSeen`（**mutating な REST だけ**が更新する。背景 GET ポーリングは触らない — `proxy.go:143`）

ここに **TTL が無い**。端末ペインを開いた Console のタブを 1 枚閉じ忘れると、
`proxy.go:346` の `trackWorkspaceConnection` が生き続け、5 秒ごとに presence lease を更新し、
**Workspace は永久に温まったまま**になる。Console 側にも可視性による切断は無い
（`console/src/terminal/term.ts` — ソケットはペインが存在する限り開いたまま）。
「自動停止が動いていない」の第二の主因はこれで、こちらは question とは独立に効く。

### 75.2.4 分類のテストがゼロ

`reaper_test.go` にあるのはリース／フェンス／`idleBase` ／タイムアウト解決のテストだけで、
**どの state が busy でどれが reapable かを固定するテストは 1 本も無い**。実際この分類は
`blocked`（2026-07-31）と `limited` / `spend_limit`（2026-08-19 / 08-20）で 2 回ドリフトしており、
そのたびに 2 箇所（`:368` と `:489`）を手で合わせている。

## 75.3 現状の欠陥（本件を含む 8 件）

- **D1 — question が両段を無効化する（本件・課金）**。§75.1。
- **D2 — plan / permission は逆に、黙って落ちる**。busy に数えられないので tier2 は
  `ws_idle_timeout` で Workspace ごと止める。しかも保留ペイロードは**再開時に消える**:
  SessionStart フックが `applyPendingPayloads(sid, "idle", h)` を呼ぶ（`session_status.go:82`）。
  つまり「人の判断待ちだったこと」が無言で失われる。question を単に busy から外すと、
  **本件の質問がこの穴に落ちる**。条件だけ直すのが危険なのはこのため。
- **D3 — `backgroundBusy` を誰も見ていない**。CP まで届いている（`workspace_handlers.go:532`）のに
  reaper は state しか見ない。背景ビルドや in-process の Workflow を抱えた idle セッションは
  tier1 で halt され、Workspace も止まる。
- **D4 — tier1 は `kind == "claude"` 限定**（`reaper.go:376`）。managed（codex / opencode / …）や
  kiro / agy の人待ちは安い回収経路を持たず、question を名乗る kind では tier2 も効かない。
- **D5 — 在席シグナルに TTL が無い**。§75.2.3。
- **D6 — 分類がインライン 2 箇所・テスト無し**。§75.2.4。
- **D7 — 「なぜ止まらないか」を観測できない**。reaper はログを出すだけで、
  「この Workspace を起こし続けているのは誰か」を返す API も画面も無い。
  障害時に他人のコンテナへ `docker exec` して status ファイルを読む、が現在の唯一の手段。
- **D8 — 停止中は保留対話が見えない**。CP の DB ミラーは
  `name/kind/dir/repo/label/created_at/state/last_seen` しか持たず（`store_sqlite.go:1622`）、
  停止中は全セッションが `stopped` として並ぶだけ。通知センターには question 通知が残るが、
  Agent のアウトボックスは **Console が見に来たときにしか drain されない**
  （`notification.go:63` の `drainAgent` は `State != "running"` で即 `offline`）ので、
  停止直前に発生した通知は次に Workspace を起こすまで届かない。
- **D9 — 保留ファイルに寿命が無い**。実開発機の `~/.config/agent-fleet/pending-question/` には
  **5〜6 週間前の未回答ペイロードが 2 件**残っていた（どちらも `state:"permission"` のまま、
  対応する転写は既に無い。sid は v4＝claude が自分を起動し直したときの id＝
  [claude-self-relaunch] の系統）。掃除する経路が無いので、同じ sid を持つセッションが
  現れれば亡霊の質問が surface しうる。持ち越しストアを足すなら TTL と GC を最初から付ける。

## 75.4 設計の 3 原則

1. **コンテナを起こし続ける理由は「機械が動いていること」だけ。** 人待ちは理由にならない。
   人待ちは何日でも続きうるからで、これは `blocked` で一度確定させた判断と同じ
   （[47](47-turn-abort-auto-resume.md) §4-9 の「促す次の一手で分ける」の延長）。
2. **人待ちを止めてよいのは、止めても失われないときだけ。** よって
   「条件を直す」より先に「保留中の対話を持ち越す」を入れる。順序を逆にすると、
   費用と引き換えに利用者の判断が無言で消える（D2 の穴を広げるだけ）。
3. **在席は「接続があること」ではなく「直近に人が操作したこと」。** 接続の存在で判定する限り、
   閉じ忘れたタブ 1 枚が一晩分の課金になる。

## 75.5 提案する条件表

分類を **1 つの純関数**に集約する（CP 側 `sessionActivity(s sessionWire) activity`）。

```
activity = machineBusy | humanWait | idleWait | unknown
```

| state / フラグ | 分類 | tier1 | tier2 |
|---|---|---|---|
| `working` | machineBusy | 対象外 | **止めない** |
| `backgroundBusy`（state 不問） | machineBusy | 対象外 | **止めない**（新規） |
| `idle` | idleWait | `session_idle_timeout` で halt | 止めない |
| `limited` | idleWait | 同上 | 止めない |
| `spend_limit` / `auth` / `blocked` | humanWait | **`interaction_idle_timeout` で halt**（新規） | 止めない |
| `question` / `plan` / `permission` | humanWait | **`interaction_idle_timeout` で halt**（新規） | **止めない**（＝busy から外す・新規） |
| kind = shell / ssm | unknown | halt しない（現状どおり） | 止めない（現状どおり） |

要点:

- **人待ちは tier1 が畳む**。halt はプロセス内で行われるので、そこで保留ペイロードを
  「持ち越し」へ昇格でき、通知も出せる。畳まれた後はもう question ではないので、
  **tier2 には特別扱いが 1 つも要らない**（busy = machineBusy だけになる）。
- それでも tier2 の busy から question を外すのは、`session_idle_timeout=0`（tier1 無効）で
  `ws_idle_timeout` だけ有効なテナントがありうるから。持ち越しの昇格は
  「Agent 起動時の reconcile」でも走る（§75.6.3）ので、この経路でも失われない。
- `interaction_idle_timeout` は新しいテナント別ノブ。既定は `session_idle_timeout` と同値。
  「質問は早めに畳んで安くしたい」「うちは 4 時間は待ってほしい」の両方を、
  通常のアイドルとは独立に決められるようにする。
- 在席（`isAttached` / `wsConns`）には **attention TTL** を入れる: 端末 WS が開いていても、
  その接続に**入力**（PTY への text フレーム）が `presence_idle_timeout`（既定 =
  `ws_idle_timeout`）以上無ければ、tier2 の在席としては数えない。tier1 の
  「見られているセッションは畳まない」は従来どおり接続の有無で判定してよい
  （畳んでも安いものしか失わないため）。
- `unknown`（shell / ssm）は現状維持。**走っているジョブが見えない**という既知の割り切りを
  ここで解決しようとしない（tmux のフォアグラウンドプロセス検査は別議論）。

## 75.6 保留中の対話の持ち越し（本体）

### 75.6.1 実測: claude 側では「復元」できない

未応答の AUQ を抱えたまま止まった転写を実データから探し（`/var/lib/af/claude/projects/*/*.jsonl`
の `tool_use` と `tool_result` の突合）、5 件見つかったうち会話が続いている 2 件を読んだ。

- `…wip-simdlbp/004cae61…jsonl`: 行 30 に AUQ の `tool_use`（uuid `57da0559`、親 `42a5a958`）。
  直後にセッション開始マーカ群（`last-prompt` / `custom-title` / `agent-name` /
  `file-history-snapshot`）。次の user 発言（行 35、`8513ba54`）の **`parentUuid` は
  `42a5a958`** — つまり AUQ ではなく**その 1 つ手前の assistant メッセージ**。
- `…wip-szjm2zk/18c57ab2…jsonl`: 行 156 に AUQ（`30d44937`、親 `a50636aa`）、39 分後の
  user 発言（行 165）の親は同じく **`a50636aa`**。

**結論: 未応答の `tool_use` は親ポインタで迂回され、会話木から外れる。** claude を
`--resume` してもモーダルは戻らないし、戻す手段も無い（API 契約上、`tool_result` の無い
`tool_use` を含む枝はそのまま送れない）。したがって復元できるのは**モーダルではなく意図**だけである。

この結論は §75.10 で **実 TUI を立てた対照実験**（claude 2.1.241）でも再現した。あわせて、
そこでしか分からなかった 4 点（再開時の自動ターン、C-c がモーダルを閉じないこと、
ExitPlanMode は保留中の転写に**現れない**こと、プラン承認は文章で通って**二度目の関門が無い**
こと）が出たので、以下の設計はそれを織り込んである。

### 75.6.2 保留(pending)と持ち越し(carried)は別物として持つ

現状の `pending-question` / `pending-plan` / `pending-perm` は「**今まさにモーダルが出ている**」
という意味で、Console はそれを**キー列**で答える（`questionKeys.ts` → `POST /sessions/{name}/input`）。
持ち越しは意味が正反対で、答えは**文章として注入**するしかない。この 2 つを同じファイル・
同じ wire キーで表すと、停止中のカードが生きたペインへ `Down/Enter` を撃つ事故に直結する
（既に MirrorView の保留カードは `alive` で出し分けていない — `MirrorView.tsx:2817` 以降）。

よって別ストアにする:

```
~/.config/agent-fleet/carried-interaction/<sid>.json
{ "kind": "question"|"plan"|"permission",
  "capturedAt": "...", "reason": "idle-halt"|"ws-stop"|"unclean",
  "questions": <tool_input.questions の生 JSON>,   // kind=question
  "plan": "...",                                    // kind=plan
  "permission": "Bash · npm ci",                    // kind=permission
  "text": "<質問直前の地の文 pending-text>" }
```

wire も別キー（`carriedQuestion` / `carriedPlan` / `carriedPermission`）で出し、
Console は**別のカード**として描く（見出しは「停止時に未回答だった質問」、
ボタンは「回答して再開」— キーは送らない）。

### 75.6.3 昇格の契機は 3 つ（3 つ目が本命）

1. `handleHaltSession`（`session_handlers.go:944` / managed は `:914`）の `status.Remove` の**前**。
2. `gracefulShutdown`（`shutdown.go`）— C-c を撒く前。Workspace 停止の正常系。
   **C-c はモーダルを閉じない**（§75.10 実測 B）ので、撒いた後でも保留ファイルはそのまま残る。
   つまりこの契機は「必須」ではなく「早い」だけで、正しさは 3 が担保する。
3. **Agent 起動時の reconcile** — meta はあるがペインが無いセッションについて、
   残っている `pending-*` を carried へ昇格させる。SIGKILL（ECS の stop timeout 超過、
   ホスト OOM、EC2 の強制停止）では 1 も 2 も走らないので、**これが受け皿**になる。
   `pending-*` はホームに残るため、この経路だけで実質すべてを拾える。
   逆に SessionStart フックの `applyPendingPayloads(sid,"idle",h)`（`session_status.go:82`）は
   **昇格の後に**走らせる必要がある（今は無条件に消している＝D2）。

### 75.6.4 種類ごとの扱い

| 保留 | 持ち越すか | 再開時にすること |
|---|---|---|
| `question` | **回答そのもの** | Console で選ばせ、再開後に文章として配達。**文面は「質問し直すな」を含む 1 行**（§75.10 実測 C：これが無いと質問し直す）。`recordInjection` で由来を記録し、ミラーが注入バッジを出す |
| `plan` | **承認/却下そのもの** | 「承認」→ 再開して `（停止前に承認待ちだった計画を承認します）さきほどの計画のとおり進めてください` を配達。★**これは実行そのもの**（§75.10 実測 E：文章の承認で claude はそのまま実行し、ExitPlanMode の関門は二度と出ない）。よって Console 側は取り消せない決定として扱う（確認を挟む・注入として記録する）。「却下」→ 修正指示を配達 |
| `permission` | **事実のみ** | 許可の答えは死んだツール呼び出しには届かない。カードは「停止時に `Bash · …` の許可を求めていました」と表示し、操作は「続けて」だけ |
| `blocked` / `auth` / `limited` / `spend_limit` | 持ち越さない | 再開後に再導出される（`auth` は Workspace 単位の事実、`limited` は時計、`blocked` のメニューは死んでいる） |

★ **plan は転写に痕跡が残らない**（§75.10 実測 D：保留中の ExitPlanMode は `tool_use` として
転写に現れない。AUQ は現れる）。つまりプランでは `pending-plan/<sid>.md` が**唯一の記録**で、
これを消したら計画本文は Console からも履歴からも復元できない。question は転写に
孤児の `tool_use` が残るので、カードと履歴の突合は AUQ だけの問題として扱えばよい。

配達には既存の seam をそのまま使う: 再開は `ensureSessionTmux`（`session_tmux.go:107`）、
配達は `deliverInitialPrompt`（CLI の起動を待って打鍵する既存実装）。新規 API は
`POST /sessions/{name}/carried-answer` 1 本で、**CP 側では `ensureWorkspaceStarted` を通す**
（停止中の Workspace を明示操作で起こす。端末アタッチが auto-start しないのと同じ理屈で、
これは利用者の明示操作なので起こしてよい）。

**再開そのものが 1 ターン焼く**（§75.10 実測 A/E）: 中断された会話を `--resume` すると claude は
`Continue from where you left off.` を自分で投げ、モデルが `No response requested.` と答える。
小さいが 0 ではないので、`interaction_idle_timeout` を極端に短くすると「畳んでは起こす」で
かえって高くつく。既定を `session_idle_timeout` と同値（1h）にするのはこの理由でもある。

### 75.6.5 停止中に「未回答がある」ことをどう見せるか（D8）

持ち越しがあっても、Workspace が停止していれば Agent に誰も聞けない。最小の追加は 2 つ:

- **DB ミラーに 1 列**: `SessionRow` に `pending_interaction`（`""|question|plan|permission`）を足し、
  停止中の一覧にもバッジを出す。`sessionsPayload` は running のとき Agent から作るので、
  停止直前の最後のポーリングで自然に入る。
- **停止直前に通知を吸い出す**: tier2 が `rt.Stop` を呼ぶ前に `drainAgent` 相当を 1 回叩く。
  これが無いと「未回答のまま停止しました」通知が、次に Workspace を起こすまで届かない。

## 75.7 段階

- **P0（安全側・非機能）**: 分類を `sessionActivity` 1 関数へ集約し、表駆動テストで固定する。
  `backgroundBusy` を machineBusy に入れる（D3・現状の実害を先に止める）。この段階では
  question はまだ busy のまま＝挙動不変。
- **P1（持ち越し）**: carried ストア（TTL/GC 付き・D9）＋昇格 3 契機＋wire＋Console カード＋
  `POST /sessions/{name}/carried-answer`。SessionStart の消去を昇格の後ろへ。
  配達文面は §75.10 の実測形を固定値として持つ（「質問し直すな」の 1 行が本体）。
- **P2（条件の切り替え）**: question を busy から外し、humanWait を tier1 の対象に。
  `interaction_idle_timeout` を追加（既定は `session_idle_timeout`）。
  halt 時に「未回答のまま停止しました」通知＋停止直前の drain。
- **P3（在席の TTL）**: `presence_idle_timeout` と、端末入力に基づく attention 判定（D5）。
- **P4（観測）**: `GET /api/admin/workspaces/{id}/idle-holders` — この Workspace を起こし続けて
  いるもの（接続 N 本 / セッション X が working / 最終 mutating 3 分前）を返す（D7）。
- **P5（他 kind）**: managed / kiro / agy の人待ちを tier1 の対象に（D4）。持ち越しは
  driver の Interaction を構造化で持てないため、question は文章配達へフォールバックする。

P0〜P2 で「止まらない」と「黙って消える」の両方が閉じる。費用に直接効くのは P2 で、
P1 抜きの P2 は禁止（原則 2）。

## 75.8 テスト計画

- **表駆動の分類テスト**（P0）: 9 state × `backgroundBusy` × kind の組み合わせを固定。
  これが D6 の再発防止そのもの。
- **tier1 の人待ち halt**（P2）: fake Agent が `question` を返す → `interaction_idle_timeout`
  経過で `/halt` が飛ぶ → 以後 busy が消えて tier2 が走ることを 1 本で通す。
- **持ち越しの round-trip**（P1）: pending 書込 → 昇格 → `/messages` が `carriedQuestion` を返す →
  `carried-answer` → `deliverInitialPrompt` に渡る文字列を固定（複数質問・自由入力・
  preview 付きの 3 形。文字列契約は版ごとに壊れるので[92](dev/92-tui-modal-driving.md)の型に従う）。
- **キーを撃たないこと**（P1）: carried カードからは `POST /input` が 1 回も出ないことを
  DOM テストで固定（生きたペインへの誤配達の防止）。
- **実機**: ECS の実配備で「質問を出したまま放置 → 1h で halt → 2h で Workspace 停止 →
  Console から回答 → 起動して配達される」を 1 周。実機まで通していないものは検証済みと言わない。

## 75.9 採らなかった案

- **question を busy から外すだけ**（1 行修正）。費用は解決するが、人の判断が無言で消える
  （D2 の穴に落ちる）。原則 2 に反する。
- **モーダルを復元する**。§75.6.1 の実測どおり不可能。
- **再開時に「さっきの質問をもう一度出して」と撃つ**。非決定的で、質問が変質しうるうえ
  1 ターン分課金する。持ち越した回答を配達する方が短く確実。
- **停止中も質問に答えられるよう、質問だけ CP 側で保持して自動で再開・投函する**。
  P1+P2 の上なら可能だが、「利用者が答えた瞬間に Workspace が起きる」は課金の観点で
  意図と逆に働きうる（夜間に答えたら朝まで起きている）。**答えは持ち越し、起こすのは
  明示操作**を既定にする。
- **質問に自動回答する**。既定選択肢を押すのと同じで、誤回答は取り消せない。
  自動オペレーター（[30](30-session-report.md)）が答える構成は既にあり、そちらは
  「人が設定した自動走行」なので別物。
- **CRIU によるコンテナ hibernate**。p3-9 で不採用済み。

## 75.10 実測（claude 2.1.241・2026-08-24）

設計の前提を、実データの突合ではなく**実 TUI を立てた対照実験**で確かめた。手順の型は
[92](dev/92-tui-modal-driving.md)。他セッションに触れないよう専用の tmux ソケット
（`tmux -L probe75`）を使い、`af` と同じ起動形（`claude --session-id <uuid>
--dangerously-skip-permissions` / プランは `--allow-dangerously-skip-permissions
--permission-mode plan`）を再現。ワークスペース停止は `tmux kill-session`＝SIGKILL 相当で代替した。

> **★ハーネスの罠（先に踏んだ）**: プローブの pane に `AF_SESSION_NAME` が継承されると、
> `workspace-agent session-status` フックは `NormalizeHookSID`（`internal/agents/claude/sid.go`）で
> **その名前のセッションの slot sid に付け替える**。結果、プローブの question 状態と
> `pending-question` が**計測者自身のセッション**に書かれ、`claude-sid` 台帳もプローブの会話を
> 指しかける（実際にそうなった。ホスト側セッションが自分のフックを撃つたびに
> `sids.Remove` で自己修復されるため実害には至らなかった）。プローブは必ず
> `env -u AF_SESSION_NAME`（＋`CLAUDECODE` などの `CLAUDE_CODE_*`）で起動すること。

**A. AUQ の保留 → 強制終了 → 再開**

| 時刻 | 観測 |
|---|---|
| t+4s | `session-status` = `question`、`pending-question/<sid>.json` あり |
| t+12s | `session-status` = **`permission`**（AUQ 自身の permission_prompt）、`pending-question` は残る |
| モーダル表示中 | 転写の**末尾が既に** `tool_use: AskUserQuestion`（＝ask 時点で書かれる） |
| `kill-session` 後 | `session-status`（`permission`）も `pending-question` も `pending-perm` も**残る** |
| `--resume` 後 | モーダルは**戻らない**。`session-status` = `idle`、`pending-question`・`pending-perm` は**消える**（SessionStart の `applyPendingPayloads`） |

転写上の再開境界:

```
10 assistant 22:42:18 3c1ad2fb <- 8130fa36  USE:AskUserQuestion  ← 未応答のまま
13 user      22:43:17 24347467 <- 8130fa36  "Continue from where you left off."   ← 親が AUQ を飛ばしている
14 assistant 22:43:17 58c6d50b <- 24347467  "No response requested."
```

`EffectiveModal` が必要なこと・「ask 時点で転写に載る」こと・**再開が自動で 1 ターン焼く**ことが、
いずれもこの 1 本で確認できる。

**B. graceful shutdown の C-c**: モーダル表示中の pane に `C-c` を送っても**何も起きない**
（モーダルはそのまま、`session-status` も `pending-question` も不変）。`gracefulShutdown` の
「C-c を撒いて working が消えるのを待つ」は、人待ちセッションには素通りする（`anySessionWorking`
は `working` しか見ないので待たない）。よって正常停止でも異常停止でも**ディスク上の状態は同じ**。

**C. 持ち越した回答の配達（P1 の検証）**: 「AUQ で語を選ばせ、選ばれた語を `out.txt` に書く」
タスクで、モーダル表示中に kill → resume → 回答を文章で配達した。

- 文面が「（停止前の質問への回答）好きな色は？ = 青」だけだと、**質問し直した**
  （モデルから見ると質問は会話木から消えているので、当然の反応）。
- 文面に**「質問し直さず、この回答を使って作業を続けてください」を入れると**、質問し直さずに
  分岐を実行して `out.txt` に「みかん」を書いた。**＝持ち越し方式は成立する。ただし文面が本体。**

**D. プランは転写に載らない**: ExitPlanMode の承認待ち中、`pending-plan/<sid>.md` は書かれるが
転写に `tool_use: ExitPlanMode` は**現れない**（保留中の転写の tool_use は `ToolSearch` と
`Write`＝プラン本文の保存だけ）。AUQ と非対称。したがって
`hidePendingInteraction`（`session_transcript.go`）が想定する「保留プランは転写にも居る」は
この版では成立せず、**プラン本文はペイロードファイルだけが記録**である。
kill → resume の挙動は AUQ と同じ（モーダルは戻らない・`pending-plan` は消える）で、
**再開だけでは計画が勝手に実行されることはなかった**（ファイルは 0 件のまま）。

**E. プラン承認は文章で通り、二度目の関門は出ない**: 上記の続きで
「（停止前に承認待ちだった計画を承認します）さきほど提示した計画のとおり進めてください」を
配達したところ、claude は ExitPlanMode を出し直さず**そのまま実行**して 2 ファイルを作成した
（pane のフッタは `⏸ plan mode on` のまま）。当初案の「plan モードのまま再開すれば TUI が
再提示するので、生きたモーダルで承認させればよい」は**この実測で否定**され、§75.6.4 を
「carried plan の承認ボタン＝取り消せない実行の承認」に改めた。

**F. 未解決の観測（本筋ではないが記録）**: 別の 1 本で、plan モードのセッションが第 1 ターンに
「計画は不要なので直接作成します」と述べて `Write` を実行し、**承認前に実ファイルが作られた**。
同じフラグ（`--allow-dangerously-skip-permissions --permission-mode plan`）で
「いますぐ Write しろ」と直に指示する A/B を 2 本回した限りでは再現せず（どちらも
ExitPlanMode の承認ダイアログで止まった。`--allow-dangerously-skip-permissions` の有無でも
差は出なかった）。**再現していないので事実として扱わない**が、plan モードの実効性の話なので
別途 1 本追うだけの価値はある。
