# 89. 親が自分の子を列挙できるようにする（段階 2 の後始末）

- 状態: **実装済み**（2026-09-09）。`list_child_sessions` を足し、完了検出を列挙の行に載せ、
  予算からアーカイブ済みを外し、「削除だけが枠を空ける」という誤った記述を直した。
  §89.3 が残した「TUI 4 種は誰かがポーリングするまで記録されない」は **§89.8 で塞いだ**
  （2026-09-10）。
- 出どころ: [88-agent-team-comparison.md](88-agent-team-comparison.md) §88.6 の示唆 2 件と、
  §88.6-2 が名指ししていた [87-session-spawn.md](87-session-spawn.md) §87.13 の **B5**
  （「新ワイヤ 2 キーを読む consumer がツリーに 1 つも無い」）。
- 設計: [ADR 0073](../decisions/0073-session-spawned-sessions.ja.md)（決定 4・6・9・§3-b）。
  前段: [87-session-spawn.md](87-session-spawn.md)。判断軸:
  [86-session-fleet-observe.md](86-session-fleet-observe.md) §86.2。

---

## 89.1 4 件、それぞれ何を直したのか

| # | 直したもの | なぜ今か |
|---|-----------|---------|
| 1 | `list_child_sessions`（新規・9 本目） | 子の名前を知る経路が `create_session` の応答しか無く、**親の文脈が畳まれると名前を失う**。§87.12 の A4 が入れた「終わる前に、残した子を名指しで伝える」が実行不能だった |
| 2 | 行に `lastTurnEndAt` を載せる | 完了検出。**封筒も起床もゼロの pull 型**で、決定 9 の「ポーリングが正」と整合する |
| 3 | 予算からアーカイブ済みを外す | アーカイブ済みは `StoppedTTL` の prune 対象外なので、**枠を永久に保持していた**。片付ける利用者ほど早く詰む |
| 4 | 「削除だけが枠を空ける」の訂正 | 停止中の枠は `StoppedTTL`（既定 7 日）で自動的に空く。短期的には正しいが「削除だけ」は誤り |

## 89.2 `list_child_sessions` — 経路は Session DTO にした

meta 直読みではなく **`GET /sessions` を読む**。理由は 3 つとも「一覧側が既にやっている」
ことに由来する。

- **live state（working / idle / stopped）は一覧が kind ごとに計算済み**で、meta からは
  そもそも導けない。
- 一覧は**アーカイブ済みを落とし、`StoppedTTL` を過ぎた停止中の子を prune する**。単に
  除外するのではなく **`RemoveMeta` まで走る**ので、返す残枠が次の `create_session` が
  予約する相手と一致する。meta を直に数えると 1 ポーリング分ずれる。
- ワイヤの `origin` / `originSession` がまさにこの問いのために載っている。

**これが §87.13 の B5 への答えである。** `session/session.go` のコメントは「consumer が
現れないままなら消す」と予告していた。現れたので残す側になり、コメントを実態へ直した。

述語は `countChildren` / `sessionDriveAllowed` と同じ **`origin=session` かつ
`origin_session` が呼び手**。決定 5 の広い述語（系譜が非空）ではない——子を fork した先は
系譜を継ぐが人が Console で作ったもので、親が操縦する対象でも予算を食う対象でもない。

返す行は name / kind / title / state / dir / createdAt / lastTurnEndAt と、`slotsLeft` /
`slotLimit`。**それ以外の DTO のキー（コンテキスト量・色・ブランチ差分・終了コード）は
載せない** — ポーリングで読まれる行は、以降の会話ぜんぶに乗る固定費になる。

説明文は英語で新規に書いた（段階 1 の実測で日本語比 40% 減）。実測 **700 B**。既存 8 本の
説明文だけの平均は 467 B（`create_session` の 1,226 B が引き上げている）なので、**これは
平均より厚い側である** — 完了検出の契約（`lastTurnEndAt` が空になる 3 条件）を説明文に
置いたぶんで、その 3 条件をモデルが自分で発見する経路は無い。

## 89.3 完了検出は「サーバ発火」ではなく行の 1 列にした

§88.6-1 のサーバ発火通知は**採らない**。af がセッションへ伝える経路は TUI 打鍵＝ターン開始
しかないので、子の 1 ターンを節約して親の 1 ターンを無条件に発生させるだけ（差引ゼロ）で、
配達は停止中の親を起こすため「子が終わるたびに親が蘇る」が既定になる。加えて**機械 idle ≠
意味的完了**である（子は毎ターン終わりに idle になる。docs/log/51 が 3 度の事故を経てこの
乖離に耐える設計になっている）。

代わりに、**検出だけを列挙の行で救った**。新しい購読状態も封筒も起床も要らない。

### 時刻の出どころ — 3 候補を実物で比べた

| 候補 | 採否 | 理由 |
|---|---|---|
| **status ストアの `TurnEnd` ビット** | **採用** | 「ターンが終わった」と「説明の付かない idle」を分けるために存在するビットそのもの。全 kind が到達する |
| 転写の最終アシスタントターン | 却下 | **agy / cursor / kiro はアシスタントターンに時刻を書かない**（`transcript.Turn{Role:"assistant", Idx:…}` に `TS` が無い）。加えて一覧のたびに転写を全解析することになる |
| meta | 却下 | 作成と停止は記録するが、ターンは記録しない |

🔴 採用の**中身は §89.8 で 1 段変わった**。同じ status ストアだが、行が読むのは `TurnEnd` ビットの
`TS` ではなく、そこから切り出した **`TurnEndAt`** フィールドである。証拠としての意味（本物の終端
でしか書かれない）は変えていない。

`status.SessionStatus.TurnEnd` は **`PersistTurnEnd` だけが立てる**（claude の Stop フック、
codex / opencode のフック、managed ドライバと**ポーリングで完了を観測する TUI**
（agy / copilot / cursor / kiro）の `MarkTurnEnd`）。SessionStart のリセットや、ランタイム
ハンドルを失った managed の idle は**わざと立てない** — docs/log/51 の照合器が「不明」を
「完了」と読まないための、証拠を陽性側に置く 1 ビットである。親が「子は終わったか」を判断
するのに必要な区別と、完全に同じものだった。

**帰結を 2 つ、説明文にも書いた。**

1. **ターンの最中は空になる。** 次のターンが status を素の `working` で上書きするため。
   `state=idle` ＋ `lastTurnEndAt` あり＝「T に終わってから何も渡されていない」で、
   `state=idle` だけ＝「起動しただけ / heal で消えた」。**idle の 2 つの意味が分かれる**ので、
   これは欠落ではなく利得である。
2. **再起動でも空になる**（boot の idle は `TurnEnd` を立てない）。空は「最後の起動以降に
   終わったターンが無い」であって「一度も終わっていない」ではない。

### ⚠️ 実装して分かった穴: TUI 4 種は「誰かがポーリングするまで」記録されない

`MarkTurnEnd` を撃つのは **`DriveState`** であって `WireLive` ではない
（`sessionx/agent.go:100,113,126,142`）。つまり **agy / copilot / cursor / kiro を TUI で
動かした子**は、`get_session_status` などが一度呼ばれるまで `lastTurnEndAt` が空のままに
なる。一覧（`GET /sessions` → `WireLive`）だけでは記録されない。

> 🔴 **この節の「塞がなかった」は §89.8 で塞いだ**（2026-09-10）。判断そのもの——`WireLive` から
> `MarkTurnEnd` を呼ぶ解は採らない——は今も正しい。下の「将来の選択肢」を実装したので、以下は
> **当時の状態の記録**として読むこと。

**塞がなかった。** `WireLive` 側でも撃つと `RecordSessionNotification` が走り、通知と
オペレーター報告の発火経路がセッション一覧のポーリングに乗ってしまう——**Console が常時叩く
読み取り経路が副作用を持つ**という別種の欠陥であり、docs/log/51 の一帯なので尚更である
（§87.10 の「別の直しのついでの変更から欠陥が出る」形でもある）。説明文に
「completion has been observed (get_session_status on that child records one)」と書いて
範囲を明示する側を選んだ。managed の同 4 種はドライバが直接撃つので影響を受けない。

**将来の選択肢（未着手）**: `MarkTurnEnd` を「**終端を記録する**」と「**通知・報告を撃つ**」に
分け、前者だけを `WireLive` から呼べば読み取りは副作用なしのまま遅れが消える。ただしそれは
通知・報告機構そのものを触る別件で、本件に同梱すべきではない。
→ **別件として実施した: §89.8。**

## 89.4 予算からアーカイブ済みを外した（ADR 0073 決定 6 の変更）

**変更前の根拠**は「アーカイブは復元可能なので、枠を空けると畳んで起こして復元で無限に
越えられる」。**見落としていたのは、枠が二度と戻らないこと**である。

- `StoppedTTL`（既定 7 日）の prune は `if m.Archived { continue }` で**アーカイブ済みを
  対象外にしている**（`HandleListSessions`）。したがって**アーカイブ済みの子は永久に枠を
  保持する**。
- 実測でこのワークスペースにアーカイブは **209 本**（claude 197 / codex 11 / opencode 1）。
  この運用では、親が子を 3 本アーカイブした時点で恒久的に詰む。**片付ける利用者ほど早く
  詰む**という逆立ちした挙動である。
- 復元は 209 本から特定の 1 本を Console で探して戻す操作で、抜け道として現実的な形をして
  いない。そして**アーカイブはセッションには開いていない**（決定 13）ので、これは人の操作
  である——**fork を数えない理由・recreate で枠を後継へ引き継ぐ理由・決定 5 と同じ基準**が
  そのまま当てはまる。

**枠が空くのは**: 削除・アーカイブ・`StoppedTTL` の満了（停止中の子）。

### 副次的な帰結: `handOverSpawnLineage` の効き所が変わった

§87.12 の A5 で入れた recreate の系譜引き継ぎは、**「アーカイブされた前任と後継の 2 本が
両方数えられる」を潰すためのもの**だった。アーカイブ済みを数えなくなったので、
**recreate 直後の二重計上はそもそも起きない**。

それでも残す。**利用者が前任を復元したとき**に二重計上が復活するからで、引き継ぎはそこを
潰している。試験（`TestRecreateHandsTheChildSlotToTheSuccessor`）も**復元後に数える**形へ
書き換えた——アーカイブ状態のまま数えると、引き継ぎの有無が結果に出ない（＝陽性対照が
効かない試験になる）。

## 89.5 「削除だけが枠を空ける」の訂正

停止中の子の枠は `StoppedTTL`（既定 7 日、`session_handlers.go` の一覧）で自動的に空く。
「停止では枠は空かず、削除で空く」は短期的には正しいが、**「削除だけが空ける」は誤り**
だった。直した先は 5 か所:

- `create_session` の説明文（`mcpStdioFleetSpawnTools`）と `stop_session` の説明文
- 予算の拒否文（`session_spawn.go`。TTL は `AF_SESSION_STOPPED_TTL` で動くので、
  文面には**設定から読んだ値**を入れる。ADR 0073 の「見えない上限を作らない」に従う）
- ADR 0073 決定 6（ja / en の補遺）
- 本書 §89.4 と [87-session-spawn.md](87-session-spawn.md) §87.3（🔴 訂正を添えた）
- Console の `agents.note_fleet_spawn`（ja / en）。develop がこの一帯の説明文を短縮した
  直後（`fc4ae75b`）なので、**その文体に合わせて短くしたうえで**直した

## 89.6 検証

`workspace/agent` は全緑（`go test ./...`）。exit code はパイプを通さずに取っている。
Console は変更が i18n の 2 行だけなので `npx vitest run`（既知の viewer dom 6 ファイルのみ
失敗 — `node_modules` を親と共有しているときの `Denied ID`。AGENTS.md の既知事項）。

新規・変更した試験は全部、**対応するコードを壊して落ちることを確認した**（§87.7 の表の続き）。

| 壊した箇所 | 落ちたテスト |
|-----------|------------|
| `wireSession` が `LastTurnEndAt` を載せない | `TestLastTurnEndRidesTheSessionsListing` |
| `lastTurnEndAt` が `TurnEnd` ビットを見ない（idle なら何でも返す） | 同上（ターン中の子に時刻が付く） |
| `countChildren` がアーカイブ済みを数え直す | `TestSpawnBudgetCountsStoppedButNotArchivedChildren` |
| `handOverSpawnLineage` が系譜を消さない | `TestRecreateHandsTheChildSlotToTheSuccessor`（復元で 3 本に見える） |
| `list_child_sessions` を広告しない | `TestFleetSpawnAddsExactlyItsNineTools` |
| 行の系譜フィルタを外す | `TestListChildSessionsReturnsOnlyOwnChildrenAndTheSlotCount` |
| フィルタから `origin` の側だけ落とす（fork した先が子に見える） | 同上 |
| 停止中の子の state を空のまま返す | 同上 |
| ハンドラから `--fleet-spawn` の判定を外す | `TestListChildSessionsRefusedWithoutTheOptIn` |
| **広告はするが `case` を書かない** | `TestFleetSpawnToolsAreCallableNotJustAdvertised` |

### 🔴 陽性対照が 1 件目で捕まらず、試験のほうを直した

**「広告はするが `case` を書かない」を最初は捕まえられなかった。**
`TestFleetSpawnToolsAreCallableNotJustAdvertised`（§87.12 の A1 で足したもの）は応答に
「許可されていません」が含まれるかだけを見ており、`case` が無いときの応答は
`unknown tool: …`（-32602）なので**素通りしていた**。あの試験が防ぐはずだった
「広告されているのに呼べない」の**もう一つの形**が、試験の外にあったことになる。
判定に `unknown tool` を足した。

同じく **`list_child_sessions` の拒否試験も 1 件目は捕まらなかった**。ゲートを外しても
`mcpOwningSession()` が「呼び手が分からない」で落ちるため、**別の理由で isError になって
いた**（陰性結果の陽性対照が、対照になっていない典型）。スタブの Agent と解決可能な呼び手を
用意して、ゲートが無ければ**成功してしまう**状況を作ってから測り直した。

### 固定できていないもの

§89.3 の「TUI 4 種はポーリングされるまで記録されない」は、tmux で実 CLI を動かさないと
再現しない（`LiveState` が実 TUI / 実転写を読む）ので**試験では固定していない**。
説明文と本書で範囲を明示する側を選んでいる。
🔴 **これは誤りだった**（§89.8）。**copilot は状態源が素の `events.jsonl` なので、tmux も実 CLI も
無しで経路ごと試験できる**。「実 CLI が要る」は 4 種のうち kiro（TUI 文字列）にしか当てはまらず、
4 種をひとまとめに諦めた結果、塞げるところまで塞がずに終わっていた。

## 89.7 残り

- **実機での発火確認は未実施**（段階 1・2 から持ち越し）。`list_child_sessions` は
  「名前を失ったとき」「最後のターンの前」に呼ばれることを意図しているので、実セッションで
  そのタイミングに呼ばれるかを確かめたい。
- §88.6-1（サーバ発火の完了通知）は**採らないと決めた**（§89.3）。再燃したときの回答は
  そこにある。
- ~~`isFleetSpawnTool` は 9 本の名簿として残っているが、**呼び出し元が無い**（各ハンドラが
  自前のゲートを持っている）。消すか使うかは決めず、コメントに「これは生きたゲートではない」
  と明記するに留めた。~~ **削除した**（レビューの判断）。双子の `isFleetObserveTool` を
  「呼び手が消えたから」で既に消しており、片方だけ残すと基準がぶれる。名簿としての役割は
  `mcpStdioFleetSpawnTools()` 本体と、ちょうど 9 本を強制している試験側の
  `fleetSpawnToolNames` が果たしている。production 側に重複を残すと**将来これをゲートとして
  配線してしまう**危険だけが残る——コメントで警告するより消すほうが確実である。

## 89.8 §89.3 の遅れを塞いだ — 「終端の記録」と「通知の発火」を分けた

- 状態: **実装済み**（2026-09-10）。§89.3 末尾の「将来の選択肢（未着手）」が**成立したので実装した**。
- 判定: `WireLive` から `MarkTurnEnd` を呼ぶ解は**依然として採らない**。素直な分割も採らない
  （下の罠）。採ったのは**時刻だけを別フィールドに切り出す**形である。

### 塞ぐ相手

TUI で動かした agy / copilot / cursor / kiro の子は、`get_session_status` が一度呼ばれるまで
行の `lastTurnEndAt` が空だった。撃つのが `DriveState` だけだったからで、**一覧しかポーリング
しない親——決定 9 が想定している唯一の使い方——には、子の完了が永久に見えない**。

### 🔥 素直な分割は 4 種の完了報告を黙って壊す

`DriveState` の 4 か所のゲートは `status.LiveState(sid) == "working"`、つまり**永続状態が
まだ working であること**を条件にしている。一方 `MarkTurnEnd` の書き込み（`PersistTurnEnd`）は
`{State:"idle", TurnEnd:true}` を書く。

したがって「記録だけする」を `PersistTurnEnd` で実装すると、**一覧のポーリングが先に idle を
書き、後から走る `DriveState` のゲートが二度と真にならない**。`MarkTurnEnd` は呼ばれず、通知も
オペレーター報告も出なくなる。agy のコメントが言うとおり、hooks が無い 4 種にとって
`DriveState` は**唯一の観測点**であり、読み取り経路がそれを先に消費してしまう形である。

### 採った設計: 状態機械に触れない 3 つ目のフィールド

`status.SessionStatus` に **`TurnEndAt`（RFC3339）** を足し、行の出どころをそこへ移した。
`State` と `TurnEnd` の状態機械には**いっさい触れない**。

> 🔴 **ポーリング側の記録の置き場所は §89.9 で変わった**（レビュー指摘）。観測した時刻は
> `SessionStatus` の中ではなく**専用のストア**（`session-turn-end/<sid>.txt`）に置く。
> 「状態機械に触れない」という意図は同じだが、**同じファイルへの read-modify-write** が
> 通知経路の書き込みを実測 143/300 で破壊した。下の表の `RecordTurnEnd` の行と
> 「最初の観測が勝つ」の実現方法は §89.9 の形で読むこと。

| 関数 | 何をするか | ゲート |
|---|---|---|
| `status.PersistTurnEnd` | 従来どおり `{idle, TurnEnd}` を settle し、あわせて `TurnEndAt` を打つ | 変更なし（hooks・managed・`DriveState`） |
| `status.RecordTurnEnd` | 観測した時刻を**専用ストアへ**打つ。status レコードには触れない（§89.9） | 「working で、まだ打たれていない」 |
| `sessionx.notifyPolledTurnEnd` | 従来の `MarkTurnEnd`。4 か所の重複ゲートを 1 本にまとめた | `LiveState == "working"`（**不変**） |
| `sessionx.recordPolledTurnEnd` | 一覧（`wireSession`）から `RecordTurnEnd` を呼ぶ | 上記＋「終端を観測した kind か」 |

**2 つのゲートが違うのが要点である。** 記録は冪等で**何も消費しない**ので、同じターンの終端は
そのあと `DriveState` が**ちょうど 1 回**通知する。逆に、記録経路からは構造上 `MarkTurnEnd` に
到達できないので、**読み取りが通知を撃つことは起こり得ない**。

**なぜ `TurnEnd` ビットを流用せず新フィールドか。** `{State:"working", TurnEnd:true}` という
矛盾した記録になり、docs/log/51 の照合器（`collectReportSignals` は `State` / `TurnEnd` / `TS`
の 3 つを読む）に「working なのに終端」を渡すことになる。§89.3 が `TurnEnd` を選んだ理由——
「終わった」と「説明の付かない idle」を分ける——は新フィールドでもそのまま保っている:
`TurnEndAt` を書くのは**本物の終端だけ**（`PersistTurnEnd` と、4 種の観測 `RecordTurnEnd`）で、
`SessionStart` のリセットも `TurnUnknown` の idle も素の `Persist` なので**打たれず、しかも
消える**（`Persist` は新しいレコードを書くため）。「ターン中は空」「再起動で空」という §89.3 の
2 つの帰結もそのまま成り立つ。

**最初の観測が勝つ。** `PersistTurnEnd` は既に打たれている `TurnEndAt` を上書きしない。両者は
**同じ 1 回の終端**を書いているので、後から来た側で時刻が進むと、`lastTurnEndAt` を見ている親には
**2 本目のターンが終わったように見える**。

### 書き込み増幅

一覧は全セッションを常時ポーリングする経路なので、**遷移したときだけ書く**。ゲートは
「永続状態が working、かつこのターンの終端がまだ打たれていない」で、**1 ターンにつき 1 回**しか
書かない。加えて `status.TurnEndUnrecorded` という**安い先行判定**を置き、状態源そのものを読む前に
落とす——agy は SQLite クエリ、copilot / cursor は 128KB の tail 走査で、行ごとに払う値段ではない。

### agy だけ非対称

copilot / cursor / kiro の `WireLive` は `DriveState` と同じ `LiveState(m)` を読むので、その結果を
そのまま使い回せる。**agy の `WireLive` は `Probe(m)` しか呼ばず、working / idle をいっさい返さない**（保留中の対話プロンプトだけを見る）ので、終端の読みはこちら側で `agy.LiveState(m)` を
呼ぶ必要がある。上の先行判定があるので、この追加読みは**ターンが in-flight のときだけ**走る。

### 併せて直した文言

- `list_child_sessions` の説明文から「until a completion has been observed
  (get_session_status on that child records one)」を削除（制約が消えたため。説明文は全セッションの
  固定費なので、短くなる方向である）。
- ADR 0073 決定 9 の補遺（ja / en）に、出どころが `TurnEnd` ビットから `TurnEndAt` へ移ったこと、
  および一覧のポーリング自身が終端を記録するようになったことを追記。

### 89.8.1 検証

`(cd workspace/agent && go test ./...)` 全緑（37 パッケージ、exit code はパイプを通さずに取得）。
テストが起こした tmux サーバは残っていないことを確認済み（`af-test-*` ソケットに生存プロセスなし）。

新規試験は 3 本 + status パッケージ 6 本。**経路を通す試験**と**回帰ガード**を分けている:

| 試験 | 何を固定するか |
|---|---|
| `TestSessionsListRecordsAPolledTurnEndWithoutGetSessionStatus` | **一覧経路だけ**で `lastTurnEndAt` が入る。ターン中は空。記録後も**永続状態は working のまま**（＝通知ゲートが生きている）。2 回目のポーリングでファイルを書き直さない |
| `TestPolledTurnEndStillReportsExactlyOnceAfterTheListingRecordedIt` | 🔥 **回帰ガード**。一覧が先に記録したあとでも `DriveState` の通知が **working→idle でちょうど 1 回**出る。以降どちらの経路を何回叩いても 2 回目は出ない。記録した時刻が動かないことも見る |
| `TestPolledTurnEndDeliversTheOperatorReportAfterTheListingRecordedIt` | オペレーター報告カードが**ちょうど 1 枚**届き、指示台帳が reported へ動く（TUI kind でこれを見る試験は他に無い） |
| `TestPolledTurnEndIgnoresKindsThatReportTheirOwnEnd` | 自分で終端を報告する kind（claude）の「説明の付かない idle」は記録しない |
| `status` の 6 本 | `RecordTurnEnd` が `State`/`TurnEnd`/`TS` を動かさない・1 ターン 1 回しか書かない・in-flight でなければ書かない／`PersistTurnEnd` の「最初の観測が勝つ」・観測が無ければ自分で打つ／`Persist` が消す |

**copilot を代表に選んだ。** 状態源が素の `events.jsonl` なので、**tmux も実 CLI も無しに経路
ごと**動かせる（`BuildLaunch` に session id を採番させ、`copilot.EventsPath` にイベントを書く）。
agy / cursor / kiro は同じ 2 つの呼び口を通る。

#### 陽性対照（§87.16.1 の続き）

| 壊した箇所 | 落ちたテスト |
|-----------|------------|
| `wireSession` が `recordPolledTurnEnd` を呼ばない | 新規 3 本すべて |
| 🔥 `RecordTurnEnd` が `PersistTurnEnd(sid,"idle")` で記録する（**素直な分割**） | `…StillReportsExactlyOnce…`（3.01s タイムアウト＝通知が出ない）・`…WithoutGetSessionStatus` |
| `lastTurnEndAt` が `TurnEndAt` ではなく `TurnEnd`+`TS` を読む | 新規 3 本すべて |
| `recordPolledTurnEnd` の kind 制限を外す | `…IgnoresKindsThatReportTheirOwnEnd` |
| `notifyPolledTurnEnd` から `LiveState=="working"` ゲートを外す | `…StillReportsExactlyOnce…`（2 回発火） |
| `PersistTurnEnd` が先の観測を上書きする | `TestPersistTurnEndKeepsTheFirstObservation` |
| `RecordTurnEnd` の冪等ゲートを外す | `TestRecordTurnEndWritesOncePerTurn` |

#### 🔴 対照になっていなかったものが 3 件

1. **オペレーター報告の end-to-end 試験は、罠を仕掛けても緑のままだった**（実測）。`PersistTurnEnd`
   で記録すると通知は死ぬのに、**docs/log/51 の照合器が settle 済みの idle+TurnEnd マーカーを読んで
   代償経路で報告を届けてしまう**ため。つまり「報告カードが届くこと」を見る試験は、この罠の対照に
   ならない。**回帰ガードは通知シームを見る側である**（試験のコメントにも書いた）。
   ——なお、その代償配達は**一覧のポーリングがオペレーター報告を撃っている**ということであり、
   §89.3 が拒んだ欠陥そのものが別の顔で出てくる形である。
2. **`PersistTurnEnd` の上書き**は、最初は経路試験の中で見ていたが**素通りした**。RFC3339 は秒
   粒度で、試験は数ミリ秒で走り切るため**両方の時刻が同じ文字列**になる。既知の値を直接書ける
   status パッケージ内の単体試験へ移して測り直した。
3. **`TurnEndUnrecorded` の「まだ打たれていない」条件**を単独で外しても緑のまま。`RecordTurnEnd`
   側の同じ条件が受け止めるためで、**二重化された防御の片側だけを壊しても観測できない**のは構造上
   当然である。両方外す二重変異では両側が落ちることを確認した（先行判定は正しさではなく**費用**の
   ためのものだと本文に明記した）。

### 89.8.2 残り

- **デプロイ直後の 1 回だけ、`TurnEndAt` が空になる**。この変更より前に書かれた status レコードは
  `TurnEnd`+`TS` しか持たないため。互換の読み替えは**入れていない**——恒久的に死ぬ分岐になるうえ、
  「最後の起動以降に終わったターンが無い」という §89.3 の意味そのままであり、次のターン終端で自然に
  埋まる。
- **kiro だけ経路試験が無い**。状態源が実 TUI の文字列契約なので、実 CLI 無しでは再現できない。
  呼び口は 4 種で共通（`turn_end_poll.go` の 2 関数）なので、固定されていないのは kiro 固有の
  状態源だけである。

## 89.9 🔥 §89.8 の記録が通知経路の書き込みを潰していた（レビュー指摘・修正済み）

- 状態: **修正済み**（2026-09-10、PR 前）。§89.8 の実装をレビューで止められた。
- 実測: 2 経路を同時に走らせて **300 回中 143 回**（指摘元は 193/300）、settle が消えた。

### 何が起きたか

§89.8 の `RecordTurnEnd` は `Read` → 構造体を書き換え → `write` の **read-modify-write** だった。
`fstore.Store.Write` は素の `os.WriteFile` で、**ロックも atomic rename も CAS も無い**
（`fstore.go:30`）。読んでから書くまでに通知経路の `PersistTurnEnd` が `{idle, TurnEnd:true}` を
書くと、こちらが読んだ古い `{working}` で**丸ごと上書きする**。

**この変更が status ストアに read-modify-write を初めて持ち込んだ**のが効いている。従来の
`Persist` / `PersistTurnEnd` は毎回**完全なレコードをブラインド書き**していたので、競合しても
last-writer-wins で壊れなかった。

窓が狭いから起きない、ではない。**両方の書き込みは同じ working→idle 遷移で誘発される**ので
時間的に相関する。被害はどれも docs/log/51 の一帯である: 永続状態が working に戻って Console の
バッジが止まり、`TurnEnd` が消えて照合器が marker-working で settle を禁じ（＝完了報告が出ない）、
次のポーリングで `LiveState=="working"` が再び真になって**同じターンで `MarkTurnEnd` が 2 度**走る。

### ロックではなく read-modify-write を無くした

**まず「このストアに書くのは Agent プロセスだけか」を確かめた。違う。** claude の hooks は
`workspace-agent session-status <state>` を起動する——`main.go:91` の**別プロセス**である
（`memoryx/memory_trigger.go` のコメントも "a separate hook process" と書いている）。したがって
プロセス内ロックでは守れない。

観測した時刻を **`session-turn-end/<sid>.txt` という専用ストア**へ出した。

- `RecordTurnEnd` は**自分のファイルだけをブラインド書き**する。status レコードには 1 バイトも
  触れない。読み手（通知ゲート・照合器・pending 掃き出し）から不可視という §89.8 の性質は、
  より強い形で保たれる。
- `PersistTurnEnd` は観測ストアを**読んで**、従来どおり**完全なレコードをブラインド書き**する。
  読むのは別ストアなので、status ファイルへの書き込みは §89.8 以前と同じ性質のままである。
- **「最初の観測が勝つ」は維持**（`PersistTurnEnd` が観測値を採用する）。

### 期限切れは mtime で決める（クリアしない）

観測ストアは**誰もクリアしない**。かわりに `ObservedTurnEnd` が
「**観測ファイルが status ファイルより新しいときだけ有効**」と判定する。

- status レコードへの**あらゆる書き込み**——次のターンの `working`、settle、heal——が、それ以前の
  観測を自動的に無効化する。「次のターン開始時に消す」という**2 ファイルにまたがる原子性**を
  必要としない形にするための判定である。
- `Persist` のホットパス（claude の PostToolUse ハートビートは毎ツール `working` を書き直す）に
  unlink を足さずに済むという副次的な利点もある。
- **同時刻は無効側に倒す**。取りこぼしは遅延、誤判定は誤配達である（docs/log/51）。
- `status.Remove`（heal・停止・削除）は観測も一緒に落とす。

### 89.9.1 検証

`(cd workspace/agent && go test ./...)` 全緑（37 パッケージ、exit code はパイプを通さずに取得）。

**回帰ガードは指摘の再現をそのまま試験にした**: `{working}` を書いてから `RecordTurnEnd` と
`PersistTurnEnd` を 2 本の goroutine で 300 回ぶつけ、毎回 `{idle, TurnEnd}` が残ることを見る
（`TestRecordingAnEndNeverDestroysTheSettle`）。**§89.8 の試験がこの形を捕まえられなかったのは、
2 経路を順番に呼んでいたからである。**

| 壊した箇所 | 落ちたテスト |
|-----------|------------|
| 🔥 `RecordTurnEnd` を status レコードの read-modify-write に戻す | `TestRecordingAnEndNeverDestroysTheSettle`（275/300 で消失） |
| `ObservedTurnEnd` が「どちらのファイルが新しいか」を見ない | `TestAnObservationDiesWithTheRecordItWasTakenAgainst`・`TestPersistTurnEndAdoptsTheObservedEnd` |
| `PersistTurnEnd` が観測を採用せず打ち直す | `TestPersistTurnEndAdoptsTheObservedEnd` |
| `status.Remove` が観測を残す | `TestRemoveDropsTheObservation` |
| `RecordTurnEnd` の冪等ゲートを外す | `TestRecordTurnEndWritesOncePerTurn` |
| `wireSession` が `recordPolledTurnEnd` を呼ばない | 経路試験 3 本 |
| `lastTurnEndAt` が観測を見ない | 経路試験 3 本 |
| `notifyPolledTurnEnd` から `LiveState=="working"` を外す | `…StillReportsExactlyOnce…`（2 回発火） |
| `recordPolledTurnEnd` の kind 制限を外す | `…IgnoresKindsThatReportTheirOwnEnd` |

#### 🔴 陽性対照が、自分の試験の陳腐化を 1 件見つけた

「kind 制限を外す」の対照が**緑のままだった**。試験が `SessionStatus.TurnEndAt` を見たままで、
記録先が専用ストアへ移ったことに追随していなかった——**壊したのに落ちない**のではなく、
**そもそも記録を見ていない**試験になっていた。`status.ObservedTurnEnd` を見る形へ直してから
測り直した。設計を変えたら、その設計を固定していた試験の**主張の対象**も点検すること。

### 89.9.2 判断の記録

**ロックは採らなかった。** プロセス内 mutex は上のとおり hooks の別プロセスに効かない。ファイル
ロックなら効くが、`fstore` は 7 系統のストアが共有する基盤で、そこにロックを持ち込むのは本件の
範囲を超える。**書き込みを競合しない形にするほうが、守るものが少ない。**
