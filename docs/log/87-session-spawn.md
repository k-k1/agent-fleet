# 87. セッションからのセッション操縦（段階 2）

- 状態: **実装済み**（2026-09-09）。オペレーター専用だった操縦系 8 本をセッション面へ開放した。
  既定 OFF、opt-in は「セッションからのフリート観測」が ON のときだけ設定できる。
  実装後に別セッションのレビューを 2 巡受け、7 件を反映した（§87.9）。
- 設計: [ADR 0073](../decisions/0073-session-spawned-sessions.ja.md)（本書は実装の記録で、
  なぜそう決めたかは全部そちら）。前段: [86-session-fleet-observe.md](86-session-fleet-observe.md)
  （段階 1・§86.2 の判断軸と §86.9 の宿題）。
- 関連: ADR 0041（加算フラグ・arm 不可侵・shell 除外・封筒）/ ADR 0029 §6（出自の軸）/
  [51-session-report-v2-ledger.md](51-session-report-v2-ledger.md)（指示台帳）/ ADR 0069
  （progress ハートビート）

---

## 87.1 開けた 8 本

`--self-report --fleet-spawn` で加算する。ui-prefs は `sessionFleetSpawn`。

| ツール | セッションでの用途 | 追加のゲート |
|--------|------------------|-------------|
| `create_session` | 文脈が割れる作業を並行セッションへ渡す | 深さ・枠・worktree・kind（§87.3） |
| `list_repos` / `list_models` / `get_agent_usage` | 起動先・モデル・残枠を選ぶ | 無し（読むだけ） |
| `get_session_output` | 子が何をしたか読む | 子限定 |
| `stop_session` / `stop_session_after_turn` / `resume_session` | 子を畳む・予約する・戻す | 子限定 |

段階 1 の 4 本は `--fleet-observe` のまま。**`--fleet-spawn` は独立フラグ**で、観測に相乗り
させていない（段階 1 の説明文が利用者に「動かす操作は一切増えません」と約束しているため）。
ただし**依存はある**: 子を見る唯一の経路が `get_session_status` なので、観測 OFF では起動も
開かない。依存は Console（設定行そのものを出さず理由を出す）と Agent（`uiprefs.FleetSpawn()` が
`FleetObserve()` との論理積）の両方で効かせた — 手書きの prefs でも成立しないようにするため。

## 87.2 帰属: `origin=session` と `origin_session`

`session.Meta` に `OriginSession` を足し、出自の enum に `session` を加えた（ADR 0029 §6 の
凍結表への追記）。**値はサーバが `AF_SESSION_NAME` から埋める** — ツールに引数は無く、モデルは
自分でない親を名乗れない。

継承するのは **recreate と fork の 2 導線だけ**。`propose_session_handoff` は何も起こさず、
利用者が Console の通常導線から起こすので `origin=user` になる（これは正しい。人が開いた消費で
ある）。ADR 0073 決定 5 が「孫を絶対に作れないとは言えない」と書いているのはこの経路のこと。

**足す先を 4 か所で見落とすと黙って壊れる**: ADR 0029 の表 / `session.ValidOrigin`
（ここを漏らすと `session` が `user` に落ちる）/ Console の凍結順テーブル（`usage/colors.ts`）/
使用量ラベルの ja/en。使用量の行に焼くのは `origin` だけで、系譜は Meta に留めた。

## 87.3 拒否条件は Agent 側に置いた

MCP ツールではなく `sessionx`（`session_spawn.go`）に置いた。ツールは薄い層で、そこだけの
不変条件は層を差し替えれば回避できる — peer の封筒とレート制限を Agent に置いたのと同じ理由。
すべて `origin=session` の create だけに掛かるので、Console・オペレーター・スケジュール・
fork・recreate の経路は形が変わらない。

**実行順が効く。** 拒否と枠の予約は**冪等照合の後**に走らせる。前に置くと、**再送を拒否する**:
create がタイムアウトしたクライアントが同じ要求を送り直すころには自分の子が既に居るので、先に
予算を見ると「もう 3 本ある」と返り、自分が作ったセッションを受け取れない（進行中の再送も同じで、
本来は `create_in_progress` として台帳が答え、呼び手側がそれを最終的なセッションへ解決する）。
作業コピーの検査だけは**解決後の `dir`** が要るのでさらに後ろにある。

- **深さ**: `origin_session` が空でないセッションは起こせない。**述語が決定 4 とわざと違う**
  （操縦は `origin==session` かつ親一致、再帰抑止は `origin_session` が非空）。子を fork すると
  `origin=handoff` になるので、origin だけを見る規則ではすり抜ける。
- **枠**: 同時 3 本（`session.SpawnChildLimit`）。数えるのは**呼び手が起こした子**
  （`origin=session` かつ親一致）で、子を fork した先は数えない（fork は人の操作なので、
  利用者の fork が原因で親が起こせなくなる形にしない。fork した先も起こせないので抜け道は無い）。
  **停止中もアーカイブ済みも数え、削除だけが
  枠を空ける** — アーカイブは Meta を残す復元可能な操作なので、枠を空けると「畳んで起こして
  復元」で無限に越えられる。数えるだけでなく**予約する**（冪等台帳は同一キーしか直列化せず、
  内容の違う並行 create は互いの Meta が書かれる前に走る）。
- **worktree**: セッション面の既定は `true`。明示 `false` は、その作業コピーで別の生きた
  セッションが動いているときに拒否する。**比較は `dir` のみで `subdir` は含めない**（同じ作業
  コピーなら `console/` でも直下でも事故は同じ）。比べるのは**解決後の `dir`** ＋ symlink 解決の
  結果である（`workingCopyKey`）: create は空の dir をホームに、相対パスをホーム基準に直すので、
  要求のまま比べると `dir: ""` がホームで走っているセッションを素通りする。
- **kind**: `shell` / `ssm` は拒否。`BridgeApprovalGate` は conv が無いと no-op なので、
  セッション面に承認ゲートは存在しない。

## 87.4 子の初期指示に出自を付ける（面が 2 つある）

`report_to` が空だと初期指示は注入記録に残らず、`Source` 空は利用者入力として扱われる。
つまり素のままだと**子から見て親発の指示が利用者の指示と区別できない**。手当ては 2 面で違う。

- **CLI の転写には封筒しか効かない。** `SpawnEnvelope` が本文の先頭へ
  `[agent-fleet:spawn from=<親>]` を置く（Agent 側で組む — 呼び手が省略・詐称できないように）。
  投入は TUI への打鍵なので、子の転写では通常入力にしか見えない（ADR 0041 決定 11 の性質は
  ここでは回避できない）。
- **ミラーは注入記録で出せる。** `TurnSourceSpawn`（`"spawn"`）を足し、`badgeOriginOf` に分岐を
  足した。**分岐を足さないと記録しても無バッジ**（`reportTo` が空だと schedule しか通らない）。
  親の名前は記録に持たせず Meta の `origin_session` から引く。Console 側は `spawnParentOf` が
  封筒からも読む（記録が間に合わない・上限で押し出された場合の保険。peer と同じ二重化）。

**記録を配達より前へ動かしたのは今回の変更**である（既存の create は
`go deliverInitialPrompt` を先に起こしてから記録していた）。`noteCreateOrigin` に 1 本化して
両方の launch path から配達前に呼ぶ。オペレーター・スケジュール経路の潜在的な取りこぼしも
同時に閉じた。

## 87.5 完了の返り方

`report_to` は空のまま（セッション宛の報告チャネルは作らない）。正は親の
`get_session_status` ポーリングで、補助として **peer messaging が ON のときだけ**、
`create_session` が初期指示の末尾に「終わったら親へ `intent=answer` で 1 通返せ」を足す
（`report_back`、既定 ON、初期指示が空なら足さない）。**arm には触っていない**。

セッション発の `stop_session` は `disarm_report` を送らない。あれは「オペレーターが自分の指示を
取り下げた」の意味で、親が子を畳むことはそれを取り下げないため。ただし停止そのものは既存の
報告を保留する — 「arm を変えない」と「無影響」は違う。

## 87.6 opencode の 60 秒

`create_session` にだけ progress ハートビートを掛けた（`agentCreateSession` は最悪
40 秒＋45 秒）。`resume_session` は 15 秒呼び出しなので要らない — 30 秒＋45 秒を要するのは
peer 送信が使う `agentResumeAndSend` で、段階 2 の 8 本には含まれない。opencode 1.18.29 が
`progressToken` を送って通知ごとに時計をリセットすることは ADR 0069 で実測済み。

## 87.7 検証

`workspace/agent` は全緑、Console は 2,195 passed（既知の viewer dom 6 ファイルのみ失敗 —
`console/node_modules` を親と共有しているときの `Denied ID` で、viewer に触っていないので無関係。
AGENTS.md の既知事項）。exit code はパイプを通さずに取っている。

新規テストは全部、**対応するコードを壊して落ちること**を確認した（陰性結果には陽性対照を）。

| 壊した箇所 | 落ちたテスト |
|-----------|------------|
| `--self-report` との論理積を外す | `TestFleetSpawnRequiresSelfReport` |
| 8 本を広告しない（関数はあるが呼ばない） | `TestFleetSpawnAddsExactlyItsEightTools` |
| 子判定から `origin==session` を落とす | `TestSessionDriveAllowedOnlyForOwnChildren` |
| worktree 既定を false に戻す | `TestCreateSessionFromSessionStampsLineageAndDefaults` |
| 出自を operator のままにする | 同上 |
| 冪等キーを conv スコープに戻す | 同上 |
| 報告依頼の 1 行を足さない | 同上 |
| セッションの停止で disarm する | `TestStopSessionDisarmsOnlyForTheOperator` |
| **ゲートを書いたが stop から呼ばない** | 同上（Agent へ届いてしまう） |
| アーカイブ済みの子を数えない | `TestSpawnBudgetCountsArchivedAndStoppedChildren` |
| 深さ判定を origin で書く | `TestSpawnDepthRefusesChildrenAndTheirForks` |
| 枠を予約せず数えるだけにする | `TestSpawnBudgetReservesBeforeTheMetaExists` |
| 封筒を付けない | `TestSpawnEnvelopeNamesTheParent` |
| 作業コピー比較に subdir を混ぜる | `TestSpawnRefusesSharedWorkingCopy` |
| `badgeOriginOf` の spawn 分岐を外す | `TestBadgeOriginOfSpawn` |
| `noteCreateOrigin` の spawn 分岐を外す | `TestNoteCreateOriginRecordsSpawnOnly` |
| run-arg を argv に足さない | `TestFleetSpawnRunArg` |
| 観測との依存を外す | `TestFleetSpawnRequiresFleetObserve` |
| spawn の封筒パーサを緩くする | Console `spawnParentOf` |
| 拒否・予約を冪等照合より前へ戻す | `TestCreateSessionSpawnBudgetAndRetry`（再送が拒否される） |
| 作業コピー検査を解決前の dir で行う | `TestCreateSessionSpawnWorkingCopyGuard`（`dir: ""` が通る） |
| symlink を解決しない | `TestSpawnRefusesSharedWorkingCopy` |
| **ハンドラが封筒を適用しない** | `TestCreateSessionSpawnWiring` |
| **ハンドラが枠を予約しない** | `TestCreateSessionSpawnBudgetAndRetry` |
| **記録を配達の後に書く** | `TestCreateSessionSpawnWiring`（配達時点で無バッジ） |
| 枠を meta 書き込み時に手放さない | `TestSpawnSlotIsHandedOverToTheMeta` |
| `releaseOnce` が毎回解放する（二重解放） | 同上 |
| ハンドラが枠を一度も解放しない | `TestCreateSessionSpawnBudgetAndRetry`（枠の漏れ） |

「関数は書いたが呼ばれていない」は**単体テストでは原理的に見逃す**ので、経路を通すものを 2 層に
置いた。`tools/call` を実際に通す 2 本（`TestCreateSessionFromSessionStampsLineageAndDefaults`・
`TestStopSessionDisarmsOnlyForTheOperator` の拒否経路）と、**実物の create ハンドラを HTTP で
叩く 4 本**（`session_spawn_wiring_test.go`）である。後者は PATH に stub の `claude` を置いて
起動まで通し、封筒・注入記録・予約・拒否の**配線**を見る。配達前記録の順序だけは外から観測
できないので、`deliverInitialPromptFn` をテスト seam にして「配達時点で記録が既にあるか」を
そのものとして確かめている。

## 87.8 残り

- **実機での発火確認は未実施**（段階 1 から持ち越し）。説明文は「いつ呼ぶか」を規定して
  発火率を上げる意図で書いているので、実セッションで呼ばれること・上限の文面が効くことを
  確かめたい。
- 3 本・1 世代は**暫定値**で、資源の実測に基づいていない（ADR 0073 決定 6）。
- 子が親より長生きしたときの掃除は利用者の手に残る（削除ツールは子にも開けていない）。
- ADR 0073 が開けないと決めたもの（`send_to_session`・承認の肩代わり・掃除破壊系・
  スケジュール・chat_plan・`flush_memos`）は据え置き。

## 87.9 実装レビューで直したもの（2026-09-09）

別セッションのレビュー。3 件は実装の欠陥、2 件は文書と実装の食い違いだった。

- **[P1] 拒否・枠予約が冪等照合より前だった。** 成功済み／進行中の create の**再送を拒否**して
  いた（§87.3）。順序を入れ替え、再送が replay されることをテストで固定した。
- **[P1] 作業コピー検査の dir が解決前だった。** create は空 dir をホームに、相対パスをホーム
  基準に直すので、`dir: ""` や相対パスで共有拒否を迂回できた。symlink も解決していなかった。
  検査を解決後の位置へ移し、`workingCopyKey` で symlink まで畳んで比べるようにした。
- **[P1] spawn 封筒に対応する受信側の作法が運用指示に無かった。** 封筒だけ作って、それを受け
  取ったセッションが従うべき 4 禁止をどこにも書いていなかった。`workspace/workspace-notes.md`
  と `notes/agent-fleet.md`（＝ af-agent-fleet スキル）に、起動する側の作法と
  「起動された側」の 4 禁止を足した。
- **[P2] 予算の述語が ADR と食い違っていた。** 実装は「呼び手が起こした子」（`origin=session`
  かつ親一致）を数えるが、ADR は `origin_session` だけで書いていた。**実装が正**（fork は人の
  操作で、利用者の fork が原因で親が起こせなくなるのはおかしい）なので ADR と本書を直した。
- **[P2] 検証が実ハンドラの配線欠落を検出しなかった。** §87.7 のとおり、HTTP で create を叩く
  4 本と配達 seam を足した。

## 87.10 実装レビュー 2 巡目（2026-09-09）

- **[P1] テストの後片付けが、自分で起こしていない tmux セッションを殺し得た。** 後片付けが
  「テスト用ストアの全 meta」を対象にしていたが、そこには手で書いた fixture（`busy` / `kid1` /
  `parent1` …）が含まれる。**tmux サーバはワークスペース内で共有**なので、同名の実セッションが
  あれば他人の作業を落とす。加えて `kill-session -t <名前>` は完全一致ではなく前方一致・
  fnmatch で解決されるため、`claude_kid1` が他人の `claude_kid10` に当たり得た。ハーネスを
  「**このテストが HTTP 経由で起こしたセッション名だけ**を記録し、`-t =<名前>` で殺す」形に
  作り直した。運用指示の「自分が起こした PID / セッションだけを止める」がテストコードにも
  掛かる、という当たり前を踏み外していた。
- **[P2] meta 保存後もハンドラ復帰まで枠を予約したままだった。** 書き込んだ瞬間から
  `countChildren` はその子を数えるので、予約と二重に計上され、**起動中（tmux や worktree の
  作成で数秒）は実在しない台数で上限に当たる**。正当な並行 create が拒否される。予約は
  **meta が書かれた時点で手放す**ようにし（`releaseOnce` で失敗経路の defer と二重解放を
  両立）、契約を `TestSpawnSlotIsHandedOverToTheMeta` で固定した。
  なお**ハンドラの呼び出し位置（復帰時ではなく meta 書き込み時）そのものはテストで固定できて
  いない** — 差が出るのは起動中の窓の内側だけで、ハンドラはテストが掴める同期点で待たない。
  代わりに「create 後に枠が漏れていない」ことだけ経路テストで見ている。
