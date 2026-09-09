# 89. 親が自分の子を列挙できるようにする（段階 2 の後始末）

- 状態: **実装済み**（2026-09-09）。`list_child_sessions` を足し、完了検出を列挙の行に載せ、
  予算からアーカイブ済みを外し、「削除だけが枠を空ける」という誤った記述を直した。
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

**塞がなかった。** `WireLive` 側でも撃つと `RecordSessionNotification` が走り、通知と
オペレーター報告の発火経路がセッション一覧のポーリングに乗ってしまう——**Console が常時叩く
読み取り経路が副作用を持つ**という別種の欠陥であり、docs/log/51 の一帯なので尚更である
（§87.10 の「別の直しのついでの変更から欠陥が出る」形でもある）。説明文に
「completion has been observed (get_session_status on that child records one)」と書いて
範囲を明示する側を選んだ。managed の同 4 種はドライバが直接撃つので影響を受けない。

**将来の選択肢（未着手）**: `MarkTurnEnd` を「**終端を記録する**」と「**通知・報告を撃つ**」に
分け、前者だけを `WireLive` から呼べば読み取りは副作用なしのまま遅れが消える。ただしそれは
通知・報告機構そのものを触る別件で、本件に同梱すべきではない。

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
