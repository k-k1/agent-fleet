# 75. アイドル自動停止の条件整理と、保留中の対話の持ち越し

> 状態: **P0〜P5 実装済み**（2026-08-24）。設計判断は
> [decisions/0055](../decisions/0055-idle-stop-and-carried-interactions.md)。
> 残り = ECS 実配備での 1 周確認（§75.8 の「実機」）と、許可の写し取りのうち実機で
> 再現できなかった経路（§75.10.1 J）。
> 前提となる既存実装は [history/p3-9-idle-stop](p3-9-idle-stop.md)（二段構えの導入）と
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

live 状態は 9 つ（`internal/agents/notify.go` と `internal/status/status.go`）——に、
kind 固有の `compacting` を足して 10。
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
| `compacting` | codex が文脈圧縮中（ワイヤに出る 10 個目。この表から漏れていた — P5 で追加） | 動く |

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
- 在席（`wsConns`）には **attention TTL** を入れる（実装済み）: 端末 WS が開いていても、
  その接続に**打鍵**（`{"type":"input"}` フレーム）が `AF_PRESENCE_IDLE_TIMEOUT` 以上
  無ければ、tier2 の在席としては数えない。tier1 の「見られているセッションは畳まない」
  （`isAttached`）は従来どおり接続の有無で判定してよい（畳んでも安いものしか失わない）。
  端末以外の長命接続（定時実行の起床・ブラウザペイン）は無条件のまま — 前者に打鍵という
  概念は無く、後者は**見えている間だけ**接続を張るので可視性そのものが在席の合図になっている。
- `unknown`（shell / ssm）は自動判定しない。代わりに**利用者の宣言**＝「自動停止しない」
  ピン（`keepAwakeUntil`・下記）を逃げ道にする。

### 75.5.1 shell / ssm は「走っているか」を判定できない（実測）

`pane_current_command` で前景コマンドは取れるが、**放置された `less` と実行中のビルドが
区別できない**（実測: idle=`bash` / `sleep 60` 実行中=`sleep` / **`less` を開いて放置=`less`**）。
名前で busy と読むと、閉じ忘れた `less` / `vim` / `top` が Workspace を永久に温める — D5 と
同じ穴を自分で作ることになる。**ssm はさらに致命的**で、pane は
`exec aws ssm start-session …` を張り続けるので前景コマンドは常に非シェル＝**常時 busy** に
なり、ssm ワークスペースが一生停止しなくなる。

よって推測しない。代わりに **「自動停止しない」ピン**（`Meta.KeepAwakeUntil`、
`POST /sessions/{name}/keep-awake {"hours":4}`、Agent 側上限 24h）を置き、
`sessionActivity` の**一番外**で machineBusy に倒す（shell/ssm は state が空なので、
分類の内側では一切引っかからない）。

- **真偽値ではなく時刻**にしたのは、消し忘れたピンが閉じ忘れた端末タブと同じ
  「黙って課金し続けるもの」になるから。止めない理由が本物なら数時間で済み、
  そうでなければ勝手に切れる。延長は押し直す。
- 効くのは**生きているセッションだけ**（死んだセッションのピンがコンテナを抱えない）。
- 読めない値は「ピンされていない」に倒す。壊れた文字列が永久にコンテナを上げ続ける方が、
  ジョブ 1 本を落とすより高くつく。
- 一覧のバッジは accent 色（鍵バッジと同じ muted にしない — これは費用が出ている状態）で、
  **期限が切れたら消える**（切れたピンを残すと「守られているつもり」で放置される）。

shell / ssm で残る割り切りは変わらない: ピンを押していなければ、端末を閉じた状態で
`ws_idle_timeout` を過ぎたコンテナは走行中のジョブごと止まる。再開は新しい `bash -l` で、
**スクロールバックも既定では消える**（`/tmp` 直下。`terminal_history_retention_days` を
入れていれば home に残る）。ssm はさらに SSO セッションが切れるので再ログインが要る。

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

- **✅ DB ミラーに 1 列**: `SessionRow.Carried`（`""|question|plan|permission`）を足し、
  停止中の一覧にもバッジ（停止中・質問あり）を出す。`sessionsPayload` は running のとき
  Agent から作るので、停止直前の最後のポーリングで自然に入る。**中継とミラーの両方**に
  要る点に注意 — `sessionWire` に足し忘れれば silent drop、ミラーに列が無ければ
  「Workspace を止めた瞬間にバッジが消える」。
- **✅ 停止直前に通知を吸い出す**: tier2 が `rt.Stop` を呼ぶ前に `drainAgentOutbox` を 1 回叩く
  （`res` を取らない形へ切り出した）。これが無いと「未回答のまま停止しました」通知が、
  次に Workspace を起こすまで届かない。失敗しても停止は続ける（通知は次回拾える）。

## 75.7 段階

- **✅ P0（安全側・非機能）**: 分類を `sessionActivity` 1 関数へ集約し、表駆動テストで固定する。
  `backgroundBusy` を machineBusy に入れる（D3・現状の実害を先に止める）。この段階では
  question はまだ busy のまま＝挙動不変。（`control-plane/session_activity.go`）
- **✅ P1（持ち越し）**: carried ストア（TTL/GC 付き・D9）＋昇格 3 契機＋wire＋Console カード＋
  `POST /sessions/{name}/carried-answer`。SessionStart の消去を昇格の後ろへ。
  配達文面は §75.10 の実測形を固定値として持つ（「質問し直すな」の 1 行が本体）。
- **◐ P2（条件の切り替え）**: question を busy から外し、humanWait を tier1 の対象に。
  `interaction_idle_timeout` を追加（既定は `session_idle_timeout`。テナント設定＋管理 UI 込み）。
  停止中の一覧バッジ（§75.6.5 の DB ミラー 1 列）も入れた。
  ✅ halt 時の「未回答のまま停止しました」通知（`carried-interaction`）と、
  **停止直前の通知 drain** も入れた — Agent のアウトボックスは Console が見に来たときにしか
  drain されないので、畳んだ直後に止めると通知が次に起こすまで届かない。費用のために
  止めた結果、止めたことを知らせる通知だけが止めたせいで消える、という形になっていた。
- **✅ P3（在席の TTL）**: 端末の presence を「ソケットがある」から「人が触っている」へ。
  `AF_PRESENCE_IDLE_TIMEOUT`（既定 30m・0 で無効）。テナント別にしなかったのは、これが
  課金方針ではなく**人の注意の定数**だから — 実際に止まるまでの時間は従来どおり
  `ws_idle_timeout` が決め、この値はその時計を「開きっぱなしのソケット」が止めてしまうのを
  防ぐだけ。**★ping と resize を打鍵と数えない**のが要（Console は開いたソケットへ定期的に
  ping を送るので、「フレームが来た＝在席」にすると元の挙動がそのまま戻る）。
  DB の presence lease（`connected_until`）も打鍵が途絶えたら更新を止める — ここを直さないと
  in-memory 側だけ直しても `WorkspaceHasRecentActivity` が常に true を返し、何も変わらない。
  **操作ビーコン**（`POST /api/workspace/attention`）を対で入れた: 打鍵に絞った結果、
  **打鍵も送信もせずミラーで過去ログを読み続けている人**が不在に見えるため。Console は
  document が可視のあいだの実操作（pointerdown / keydown / wheel / touchstart・`isTrusted`）を
  **60 秒に 1 回だけ**投げてアイドル時計を進める。「タブが開いている」ではなく「人が操作した」
  を送るので、開きっぱなしのタブは 1 回も送らない。**auto-start は通さない**（停止した
  Workspace のタブを開いてクリックしただけで起き上がってはいけない）。

### 75.7.1 何が「在席」で何が「操作」か（実装の対応表）

止めるかどうかは **在席（`watched`）** と **アイドル時計（`idleBase`）** の 2 つで決まる。
tier2 が止めるのは「在席でない」かつ「アイドル時計が `ws_idle_timeout` より古い」かつ
「machineBusy なセッションが無い」の 3 つが揃ったときだけ。

| 操作 | 打鍵（在席） | 操作/変更（アイドル時計） |
|---|---|---|
| 端末にタイプ・貼り付け・マウス報告中のクリック | ✅ | ✅ |
| 端末のスクロールバックをスクロール | ✕ | ✅（ビーコン） |
| ping（Console が自動送信）/ resize | ✕ | ✕ |
| **タブの表示 / 非表示**（隠れてもソケットは生きたまま） | ✕ | ✕ |
| **分割ペインのフォーカス移動** | ✕ | ✕ |
| ミラーで過去ログを読む・スクロール | ✕ | ✅（ビーコン） |
| ミラーからプロンプト送信・質問カードへの回答 | ✕ | ✅（mutating REST） |
| 一覧 / `/messages` のポーリング（GET）・`/api/events` | ✕ | ✕ |
| 裏のタブでの操作 | ✕ | ✕ |
| ブラウザペイン表示中 / 定時実行の起床 | 無条件で在席 | — |

- 在席は**ワークスペース単位**（`lastInput[wsID]`）なので、分割ペインのどれで打鍵しても
  同じ時計が動く。フォーカスの有無は判定に入らない。
- 隠れたタブもソケットと ping は生き続けるが、**どちらも数えない**。以前はこの
  「隠れたまま開いているソケット」が永久に温める原因だった。
- **✅ P4（観測）**: reaper が毎スイープで「いつ止まるか / 誰が止めているか」を
  `manager.idleForecasts` へ置き、管理画面のメンバー名簿と詳細がそれを読む（D7）。
  **★画面は再計算しない**のが要件そのもの: 自前で導出すると reaper が実際に見ている
  もの（在席・ピン・背景作業・共有ウォーターマーク）とズレて、「なぜ止まらないのか」を
  調べるための画面が別の答えを出す。それなら画面が無い方がまし。よって公開するのは
  **reaper の決定そのもの**で、鮮度はスイープ間隔（既定 60 秒）— 画面は観測時刻を必ず添える。
  「止まらない」と「予定が出ていない」を別物として見せ、無効なテナントは「無効」と言う。
- **✅ P5（他 kind）**: tier1 の門を `kind == "claude"` から「halt が resumable な kind」へ
  広げた（`tier1Foldable`）。**shell / ssm だけが例外**で、こちらの halt は走っている
  ジョブごと殺すことを意味し、しかも何が走っているかは af から見えない（§75.5.1）。
  持ち越しは kind ごとに在処が違うので `agents.ModalReporter` 1 つへ寄せた（§75.7.2）。
  配達は claude/TUI が打鍵、managed が `ThreadHandle.Send` の 1 ターン。

P0〜P2 で「止まらない」と「黙って消える」の両方が閉じる。費用に直接効くのは P2 で、
P1 抜きの P2 は禁止（原則 2）。

### 75.7.2 保留の在処は kind ごとに違う（P5 の本体）

門を広げた瞬間、**claude 以外も畳まれるようになる**。よって原則 2（畳んでよいのは
失われないときだけ）を満たすには、claude 以外の持ち越しが同時に要る。ところが保留の
在処は kind ごとにばらばらで、しかも**寿命が違う**:

| kind / 経路 | 保留の在処 | プロセスが死んだ後も読めるか | 持ち越しの Kind |
|---|---|---|---|
| claude | `pending-question` / `pending-plan` / `pending-perm`（hooks が ask 時点で書く） | **読める** | question / plan / permission |
| agy | 会話 DB の最終 step（status=9） | **読める** | ASK_QUESTION → question / ツール許可 → permission |
| codex | rollout 末尾の未応答 `request_user_input`（TUI）/ handle の Interaction（managed） | TUI は読める / managed は不可 | question |
| opencode | ストアの running な question ツール | **読める** | question |
| copilot | `events.jsonl` の未完了 `permission.requested`（TUI・managed 共通） | **読める** | permission |
| cursor | ACP `session/request_permission`（managed のみ。TUI は観測不能） | 不可 | permission |
| kiro | ペインのフッタ `requires approval`（TUI）/ ACP の許可（managed） | 不可 | permission |

読み方を畳む側に散らさないため、入口は **`agents.ModalReporter`（`PendingModal`）1 つ**に
寄せてある（`internal/agents/modal.go`）。claude だけは実装しない — そちらは hooks が書く
`pending-*` が正で、同じことを 2 か所から主張させない。

要点が 3 つある。

1. **許可は question として運ばない。** ACP の Interaction も agy の合成メニューも、Console に
   選択カードを描かせるために `question` の形をしているが、可否の宛先（JSON-RPC の id・
   TUI のモーダル）は**プロセスと一緒に消えている**。畳んだ後に選ばせると、届かない答えを
   利用者に選ばせることになる（許可したのに実行されない／その逆）。よって持ち越しは
   `permission`＝**事実だけ**へ落とす（§75.6.4 と同じ判断）。
2. **写し取りは畳む前。** ペインと ACP handle はプロセスと寿命を共にするので、
   `halt` は **`DropHandle` / `kill-session` より前**に、`gracefulShutdown` は
   **`AbortManaged` より前**に昇格する。順序を逆にすると呼ばれても必ず空になる
   （実際 PR #165 の managed 経路は `dropManagedRuntime` の後に昇格しており、
   `managedAlive` が false になるため一度も発火していなかった）。
3. **codex / opencode に許可の持ち越しは無い** — 承認導線そのものが無いからである。
   codex managed は `item/permissions/requestApproval` を `appclient.go` が自動応答し、
   TUI ルートは bypass 起動。opencode managed は `permission.asked` を無条件 auto-allow。
   **人が答える許可プロンプトが存在しない**ので、持ち越すものも無い。両者の人待ちは
   質問ツール（`request_user_input` / `question`）だけで、そちらは持ち越す。

**取れない経路（既知の割り切り）**: コンテナごと SIGKILL（ECS の stop timeout 超過・
ホスト OOM・EC2 の強制停止）されると、cursor / kiro の許可要求と kiro TUI の承認パネルは
失われる。ディスクに痕跡を残す設計は可能だが、**可否の宛先はどちらにせよ死んでいる**ので
戻せるのは事実だけであり、そのために ask 時点の書き込みを増やす価値は薄いと判断した。
`gracefulShutdown` を通る正常停止（tier2 の停止はこちら）では取れる。

**表示側**: 停止中バッジ（`state.stopped_question` ほか）と `CarriedBlock` は元から kind 非依存
だったが、**非 claude の `/messages`（generic 経路）が `surfaceCarried` を呼んでいなかった**。
そのままだと持ち越しは書かれるだけで、一覧のバッジは「質問あり」と言うのに開いても
答えるカードが無い＝ `POST /carried-answer` への入口がどこにも無い状態だった。

**分類の穴を 1 つ塞いだ**: codex の `compacting` が §75.2.1 の 9 状態の表から漏れており、
`sessionActivity` で unknown に落ちていた。**文脈圧縮の最中に Workspace ごと止まりうる**
（機械が動いているのに起こし続ける理由に数えられない）ので machineBusy に入れた。

## 75.8 テスト計画

- **表駆動の分類テスト**（P0）: 9 state × `backgroundBusy` × kind の組み合わせを固定。
  これが D6 の再発防止そのもの。
- **tier1 の人待ち halt**（P2）: fake Agent が `question` を返す → `interaction_idle_timeout`
  経過で `/halt` が飛ぶ → 以後 busy が消えて tier2 が走ることを 1 本で通す。
- **持ち越しの round-trip**（P1）: pending 書込 → 昇格 → `/messages` が `carriedQuestion` を返す →
  `carried-answer` → `deliverInitialPrompt` に渡る文字列を固定（複数質問・自由入力・
  preview 付きの 3 形。文字列契約は版ごとに壊れるので[92](../dev/92-tui-modal-driving.md)の型に従う）。
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
[92](../dev/92-tui-modal-driving.md)。他セッションに触れないよう専用の tmux ソケット
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

## 75.10.1 実測（P5・非 claude の 1 周・codex 0.149.0 / cursor・2026-08-24）

§75.10 が claude を対象にしたのと同じことを、**claude 以外**で 1 周させた。専用の
tmux ソケット（`AF_TMUX_SOCKET=p5probe`）と使い捨て HOME で本物の `workspace-agent` を
立て、資格情報は `~/.codex` / `~/.config/cursor` を**symlink して CLI 自身に読ませた**
（コピーも読み出しもしない）。§75.10 の罠どおり `env -u AF_SESSION_NAME` で起動している。

**G. managed codex の質問 → halt → 持ち越し → 回答 → 文章配達（本命）**

| 段 | 観測 |
|---|---|
| 質問が出た | wire `state=question`、`/messages` に `pendingQuestions`（`call_zViSr5…`・選択肢 2 つ） |
| `POST /halt`（tier1 が撃つのと同じ） | 応答が `alive=false, carried=question`。`carried-interaction/<sid>.json` に**選択肢ごと**保存された |
| 停止中の一覧 | `alive=false, carried=question`（停止中バッジの材料） |
| 停止中の `/messages`（**generic 経路**） | `carried` が載る ← **この行が無いと持ち越しは書かれるだけで見えない** |
| `POST /carried-answer {"labels":["りんご"]}` | 配達文 `（停止前に未回答だった質問への回答です。質問し直さず…）「out.txt に書き込む語を選んでください。」= りんご` |
| 再開後 | codex は**質問し直さず** `out.txt` に `りんご` を書いた。`carried` は消え `pendingQuestions` も出ない |

**「質問し直すな」の一文は claude 以外でも効く**（§75.10 C と同じ結論）。

**H. 昇格は `DropHandle` より前でなければ一度も発火しない**: managed の保留は handle の
メモリにしかなく、`DropHandle` は `handles` から消して `alive=false` を立てるので、
その後に呼ぶ `promoteCarriedManaged` は先頭の `!managedAlive(m)` で必ず抜ける。
PR #165 の `handleHaltSession` はこの順序だったため、**managed の持ち越しは書かれたことが
一度も無かった**。順序を入れ替えた後の実測が G。同じ理由で TUI 側も `kill-session` の前へ
移した（kiro の承認パネルはペインの文字列にしかない）。

**I. managed cursor の live 状態はここで初めて付いた**: 同じ probe で managed cursor
（plan 起動）にターンを流すと wire は `working` → `idle` と動いた。この working ディレクトリに
cursor の TUI 転写 JSONL は**存在しない**ことを確認済み（`find … -name '*.jsonl'` が 0 件）
——つまり従来の JSONL 末尾分類は空を返す経路で、値は新設の `managedLiveState`（turn 状態機械）
から来ている。従来はここが空＝ `activityUnknown` で、**tier1 が畳むことも tier2 が起こし続ける
こともない**行だった。

**J. 取れなかったもの（正直な記録）**: cursor は `--trust` 付きの plan 起動でも、`ls` にも
ファイル作成にも `session/request_permission` を出さなかった（実測 2 ターン）。codex の
**TUI ルート**は `-c features.default_mode_request_user_input=true` を渡しているにも
かかわらず 0.149.0 が「Default モードでは利用できない」と答え、`request_user_input` を
呼べなかった（**フラグのドリフト。本件とは別の課題**）。よって cursor / kiro / copilot の
**許可**の写し取りと codex TUI の質問は、実機では未確認で単体テスト止まりである。
「動くはず」とは書かない。

条件表（§75.5）の裏返し。**止めた後に何が戻り、何が戻らないか**を状態ごとに並べる。
実装前（現状）と実装後（P0〜P2 適用後）を分けて書く — 現状の欄がそのまま「今そこにある損失」である。

### 75.11.1 共通の道筋

|  | tier1（セッション halt） | tier2（Workspace 停止） |
|---|---|---|
| 何が起きるか | `POST /sessions/{name}/halt` → `tmux kill-session` ＋ **`status.Remove`（status と pending-question/plan/perm を即削除）** ＋ `StoppedAt` 押印。コンテナは動いたまま | コンテナへ SIGTERM → `gracefulShutdown`（各 pane に C-c → `working` が消えるのを待つ → kill-session）→ コンテナ消滅。**status も pending も home に残る** |
| 報告 arm（docs/30） | **温存**（reaper は body 無しで叩く。`disarm_report` は MCP の停止だけ） | 温存 |
| 終了記録（docs/26） | 走らない（意図的な kill はクラッシュに数えない） | 走らない |
| 7 日 prune の時計 | **ここから動き出す**（`StoppedAt` 押印）。7 日放置で meta ごと消え、`maybePruneWorktree` で**綺麗な worktree も消える**（削除ロックがあれば免除） | 動かない。Agent が死んでいるので押印されず、**WS が次に起きて `GET /sessions` が来た時点**から 7 日 |
| 再開の操作 | セッション行をクリック（端末アタッチ）か `/start` → `ensureSessionTmux` → `claude --resume <sid>` | **まず Workspace を Start**（端末アタッチは停止中の WS を起こさない）→ そのあと同上 |
| 再開時に必ず起きること | `SessionStart(boot)` が status=idle にし、**pending-question/plan/perm を消す**。中断ターンを抱えた会話では claude 自身が `Continue from where you left off.` を投げて **1 ターン焼く**（§75.10） | 同左 |

### 75.11.2 現状（実装前）

| state | tier1 | tier2 | 再開後どうなるか / 失うもの |
|---|---|---|---|
| `working` | 対象外 | 対象外（busy） | — （ただし利用者/管理者の停止やデプロイでは止まる。C-c でターンが中断され、再開後は `Continue…` の 1 ターン） |
| `idle` | **畳む**（1h） | 止める（2h） | **完全に戻る**。会話は jsonl から復元、モデル/モードは meta から再指定。失うものは無い。設計どおりの happy path |
| `idle` ＋ `backgroundBusy` | **畳む**（見ていない） | **止める** | **走っていた背景ジョブ（`run_in_background` / 背景サブエージェント / Workflow）が死ぬ**。claude は殺されたことを知らないので、会話上は「起動したまま報告が来ないタスク」になる＝**無言の作業喪失** |
| `question` | 畳まない | **止めない** | 止まらないので再開もしない＝**本件（課金）**。手動 halt した場合は `status.Remove` で**その場でペイロードが消え**、再開後は質問が影も形も無い（転写に孤児の `tool_use` が残るだけ） |
| `plan` | 畳まない | **止める** | **計画本文が完全に消える**。保留中のプランは転写に載らない（§75.10 D）ので、`pending-plan` を boot フックが消した時点で Console からも履歴からも復元不可。★救済: claude 自身が `$CLAUDE_CONFIG_DIR/plans/<slug>.md` に本文を残しており、**これは停止でも再開でも消えない**（実測）。今のところ唯一の復元経路 |
| `permission` | 畳まない | **止める** | 許可を求めていたツール呼び出しごと消える。再開後、claude が同じツールを撃ち直すかは**未実測**。撃ち直さなければ、利用者は「何を許可しようとしていたか」を知る手段が無い |
| `blocked`（上限メニュー） | 畳まない | **止める** | メニューはプロセスごと消えるので**それ自体は解消**。ただし上限が続いていれば、再開時の `Continue…` が同じ 429 を踏んで中断ターンを 1 本作る |
| `auth`（期限切れ） | 畳まない | **止める** | 再開しても**何も動かない**（TUI は入力を受け取るがターンが始まらない）。af 側は `auth_expired` で送信を拒否するので、実質「再認証するまで再開する意味が無い」 |
| `limited` | **畳む** | 止める | 設計どおり: CP の定時実行（`wake_policy=wake`）がリセット時刻に WS ごと起こして再開プロンプトを届ける |
| `spend_limit` | **畳む** | 止める | 起こす予約は無い（待っても解けないため）。増枠前に再開すると同じ 429 を踏む |
| shell / ssm | 畳まない（意図的） | **止める** | プロセス・環境・実行中ジョブは戻らない。再開＝**まっさらな `bash -l`**（端末の見た目の履歴は `record-terminal` の記録として残る）。ssm は SSO セッションごと切れるので再ログインが要る |
| managed（codex / opencode / …） | 畳まない（`kind=="claude"` 限定） | **止める** | 共有 daemon ごと落ちるので runtime handle は失われ、再開は driver の `Resume`（`codex resume <id>` など）。**保留中の Interaction（質問）はハンドルの中にしか無いので消える** |

### 75.11.3 実装後（P0〜P2 適用）

| state | tier1 | tier2 | 再開後どうなるか |
|---|---|---|---|
| `working` / `backgroundBusy` | 対象外 | **止めない**（machineBusy） | そもそも畳まれない＝背景作業の無言喪失（現状の 3 行目）が消える |
| `idle` / `limited` | 畳む（`session_idle_timeout`） | 止める | 現状と同じ（完全に戻る） |
| `question` | **畳む**（`interaction_idle_timeout`）。畳む前に payload を **carried へ昇格**＋「未回答のまま停止しました」通知 | busy から外れるので**止まる** | Console に「停止時に未回答だった質問」カードが出る（**キーは撃たない**）→ 利用者が選ぶ → WS/セッションを起こして**回答を文章で配達**（「質問し直すな」の 1 行込み・§75.10 C）→ 会話は分岐どおり続く |
| `plan` | **畳む**（同上・carried へ昇格） | 止まる | カードに計画本文が残る。「承認」は**そのまま実行の承認**（§75.10 E）なので確認を挟む。「却下」は修正指示を配達 |
| `permission` | **畳む**（事実だけ carried） | 止まる | カードは「停止時に `Bash · …` の許可を求めていました」と事実を出すだけ。操作は「続けて」1 つ |
| `blocked` / `auth` / `spend_limit` | **畳む**（安い回収へ寄せる） | 止まる | 持ち越すものは無い。再開の判断材料（メニュー/再認証/増枠）は Console 側の表示で足りる。`auth` は再開そのものを促さない |
| shell / ssm | 畳まない | 止める | 現状と同じ（走行中ジョブの喪失は未解決のまま。§75.5 の `unknown`） |
| managed / 他 kind | **畳む**（`tier1Foldable`。shell / ssm だけ例外） | 止まる | 保留は kind ごとに在処が違う（§75.7.2）。質問は回答フォームごと持ち越して**文章で配達**し、許可は**事実だけ**持ち越す（可否の宛先はプロセスと一緒に死んでいる）。ペイン / ACP handle にしか無い保留は、`halt` と正常停止では取れるが**コンテナごと SIGKILL された場合は失われる** |

**要するに実装後は、どの人待ち状態でも「畳まれてよい」に変わり、失われるのは
`permission` の答えだけになる**（それも事実は残る）。逆に、**現状で最も静かに損をしているのは
`plan` と `backgroundBusy` の 2 つ** — どちらも tier2 が止める側なのに、止めた事実を利用者に伝える
経路が無い。
