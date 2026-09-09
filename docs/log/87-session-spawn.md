# 87. セッションからのセッション操縦（段階 2）

- 状態: **実装済み**（2026-09-09）。オペレーター専用だった操縦系 8 本をセッション面へ開放した。
  既定 OFF、opt-in は「セッションからのフリート観測」が ON のときだけ設定できる。
  実装後にレビューを 5 巡受け（前任 3 巡 ＋ 別レビュアー 2 巡）、判定は「マージしてよい」。
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
| `publish` が枠を解放しない | `TestSpawnSlotIsHandedOverToTheMeta` |
| `release` が冪等でない（二重解放） | 同上 |

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
  `countChildren` はその子を数えるので、予約と二重に計上され、実在しない台数で上限に当たる。
  正当な並行 create が拒否される。予約は **meta が書かれた時点で手放す**ようにし、契約を
  `TestSpawnSlotIsHandedOverToTheMeta` で固定した。
  なお**ハンドラの呼び出し位置そのものはテストで固定できていない** — 差が出るのは窓の内側
  だけで、ハンドラはテストが掴める同期点で待たない。代わりに「create 後に枠が漏れていない」
  ことだけ経路テストで見ている。

  **窓の長さについて、当初ここに書いた説明は誤りだった**（4 巡目の指摘）。meta の書き込みは
  `startSessionTmux` の**後**なので、旧実装の二重計上区間に起動そのものは入らない。実際に
  効くのは **managed 経路**で、`slot.publish` の後に `h.Send`（ランタイムへの往復）が続く。
  直しは正しかったが、根拠は「起動中だから数秒」ではなく「managed の送信を挟むから」である。

## 87.11 実装レビュー 3 巡目（2026-09-09）

- **[P2] meta の公開と枠の解放が同じロック下になかった。** 2 巡目の直しは「書いてから解放」で、
  窓を狭めただけで閉じていない。`reserveSpawnSlot` は同じロックの下で meta と進行中を数えるので、
  途中の状態を見られると**書いた直後・未解放**で二重計上（上限に届いていない親が拒否される）、
  逆順なら**どちらにも数えられない**（上限を超える）。`spawnSlot.publish` にまとめ、
  meta の書き込みと解放を 1 つのクリティカルセクションにした。
- **[P2] テストの後片付けが「成功応答が名乗った名前」だけを追跡していた。** 起動は成功したのに
  応答が壊れた・テストが登録前に Fatal した、で取りこぼす。**追跡の向きを反転**し、
  「テスト専用ストアの全 meta から、手で植えた fixture を除いたもの」を殺す形にした。meta は
  応答より前に書かれるので、**ストアのほうが「何を起こしたか」の正直な記録**である。

### テストで固定できていないこと（3 件・意図的に列挙する）

いずれも「起動中の窓の内側でしか差が出ない」ため、外から観測できない。退行を実際に入れて
**捕まらないことまで確認した**うえで、seam を足すより範囲を明示するほうを選んでいる。

1. `publish` が 1 つのクリティカルセクションであること（2 つに割っても、割り込んだ側は
   スプリアスな拒否になるだけで、別の呼び手が枠を取るため総数は変わらない）。
2. ハンドラが `session.WriteMeta` ではなく `slot.publish` を通ること（同上）。
3. 解放が「復帰時」ではなく「meta 書き込み時」であること（§87.10）。

**3 を固定できれば 2 も落ちるので、実質 1 件である**（4 巡目の指摘）。それよりも優先すべき
未試験がある: **配線試験は tui 経路しか通っていない**。`kind` の多数派である managed
（codex / opencode / copilot / cursor / kiro）は分岐が別で、`slot.publish` も
`noteCreateOrigin` も `handOverSpawnLineage` も別の行にある。ここは「テストで固定できていない」
ではなく **単に未着手**として残す。**マージ後に独立した変更として入れる** — 5 巡目の助言に
従った。**→ 追記（2026-09-09）: §87.14 で実施した。上の 3 件のうち 2 と 3 も、managed 経路に
限っては同時に固定されている。**2 巡目・3 巡目の追加欠陥はどちらも「別の直しのついでの変更」から出ているので、
別件の commit に同梱するのは同じ形を踏みに行くことになる。

代わりに固定してあるもの: `spawnSlot` の契約（`TestSpawnSlotIsHandedOverToTheMeta`）、
create 後に枠が漏れていないこと、競合下で上限ちょうどしか通らないこと
（`TestSpawnBudgetHoldsUnderConcurrentCreates`・`-race`）。

## 87.12 実装レビュー 4 巡目（2026-09-09・別レビュアー `swte2z4`）

前任者が使えなくなったため、別セッションに新しい目で見てもらった。**5 件が実装の欠陥**で、
うち 1 件は「広告したのに呼べない」という、こちらの試験の型そのものの穴だった。

- **[A1] `list_models` のゲートを直し忘れていた。** 8 本を広告しておきながら 1 本だけ
  `writeEnabled()` のままで、セッションから呼ぶと必ず日本語の拒否が返る。**しかも
  `create_session` の説明文が「先に `list_models` を呼べ、model を推測するな」と名指しして
  いる**ので、開放した経路がその最初の一歩で壊れていた。広告集合を見る試験はあったが、
  **8 本を実際に呼ぶ試験が無かった** — `TestFleetSpawnToolsAreCallableNotJustAdvertised` を
  足した。
- **[A2] アーカイブ済みの子を `resume_session` で蘇生できた。** `Archived` を見ていたのは
  一覧だけで、`sessionDriveAllowed` も `HandleStartSession` も見ていない。結果は
  **アクティブ一覧に行の無い、生きたエージェント**。決定 13（子であっても archive / delete は
  開けない）を実質迂回する経路でもある。子ゲートに `Archived` の拒否を足した。
- **[A3] セッション面には出力カーソルが無かった。** 記憶先が `convID()` で切られており、
  セッション側 af に `--conv` は渡らない。つまり「`since` を省けば続きから」という説明文が
  **唯一の対象面で嘘**で、毎回テール全部（既定 32 KiB・最大 1 MiB）を親の文脈へ再投入して
  いた。§86.2 の 3 性質のうち「返信の上限が無い」だけ手当てが漏れていた形。スコープを
  `outputCursorScope()` に切り出し、セッション面では自分の名前で覚える。
- **[A4] 親が終わるときに子をどうするかが、どこにも書いていなかった。** 停止では枠が空かず、
  子を列挙する手段も無い。ツール説明と運用指示に「**終わる前に、残した子とその状態を名指しで
  伝える**」を足した。
- **[A5] 利用者の recreate が親の枠を 2 本食っていた。** recreate は旧 meta をアーカイブ保持
  したまま新 meta を作り、両方が親を継ぐ。**fork について明示的に避けた形（人の操作で親の予算を
  減らさない）と同じ**なのに、recreate では踏んでいた。`handOverSpawnLineage` で成功時に
  系譜を後継へ移す（失敗経路は旧セッションを戻すので、そこで消すと親が操縦できない子が残る）。

**(B) 15 件のうち、記録として効いたもの**: §87.10 の根拠が誤りだった件（上に訂正済み）、
managed 経路が配線試験を通っていない件（§87.11 に移した）、`workspace-notes.md` の追記が
直上の peer 記述と重複していた件（3 行に畳んだ）。説明文の量（745B/本、段階 1 の 773B/本より
薄い）と、`origin=session` の追記漏れが無いこと、`TestFleetObserveDoesNotOpenOperatorTools` の
一覧が 1 つも外れていないこと、開けないものが `mcpStdioCall` で確実に閉じていることは、
独立に確認された。

## 87.13 実装レビュー 5 巡目（2026-09-09）— 判定: マージしてよい

(A) はゼロ。4 巡目の 5 件は「正しい場所で正しい理由で閉じており、新しい欠陥は入っていない」。
(B) 6 件のうち、この巡で入れたのは 4 件。

- **[B1] `handOverSpawnLineage` の帰結が文書化されていなかった。** 置き換えられた側は
  「`origin=session` かつ系譜が空」でアーカイブに残るので、**利用者が復元するとそれは子を
  起こせる**（再帰抑止は同じフィールドを読む）。到達には recreate と復元という Console 専用の
  人の操作が 2 回要るので決定 5 は破れていないが、**書かれていない帰結**だった。ADR 決定 6 に
  ja/en 両方で足し、テストでも `spawnDepthRefusal` が通ることを固定した（塞ぐのではなく、
  そうなると明記する側を選んだ理由も含めて）。
- **[B2] 数秒前の meta のコピーを書き戻していた。** ヘルパの引数を名前に変え、中で読み直す。
- **[B3] recreate の配線試験のコメントが「2 経路」を名乗っていたが tui しか通っていない。**
  コメントを実態に合わせた（managed は §87.11 の未着手へ）。
- **[B4] `OutputCursors` のコメントが per conversation のままだった。** セッション側の
  エントリには後始末が無い（会話側にしかない）ことも併記した。

入れなかったもの:

- **[B5] 新ワイヤ 2 キー（`session.Session.origin` / `.originSession`）を読む consumer が
  ツリーに 1 つも無い。** 実際そのとおりで、MCP 側は meta を直接読み、ミラーのバッジは注入記録と
  封筒から出している。削除ではなくコメントの訂正に留めた（CP がこの DTO をそのまま復号するので、
  外から系譜を答えられる形は残す。consumer が現れないままなら消す、と書いた）。
- **[B6] A2 の防御は MCP 層だけで、`HandleStartSession` は今もアーカイブ済みを起こす。**
  決定 4 の「ハンドラ側ゲート」の設計どおりで回帰ではない、という判定に同意。
- **managed 経路の配線試験** — 上のとおりマージ後。

## 87.14 追記（2026-09-09）— managed 経路の配線試験（§87.11 の宿題）

`session_spawn_wiring_managed_test.go` を足した。ADR 0073 の実装課題として唯一「未着手」で
残っていたもので、マージ後の独立した変更として入れる、という 5 巡目の助言どおりの扱いである。

**安く済んだ理由**: 分岐が解決に使うレジストリ `managedDrivers`（`session_turn.go:31`）が
パッケージ変数なので、そこへ偽ドライバを差せば launch が関数呼び出しになる。ランタイムも
デーモンも CLI も要らない。偽ドライバは `agents.Driver` を nil 埋め込みし `Resume` だけを
実装する（他が呼ばれたら値をでっち上げずに panic する。`deps_stub_test.go` と同じ作法）。
mcpx の deps は既に `deps_stub_test.go` が本物で配線している（managed 起動が mcpx を通るため）。

固定したもの（tui の 4 本と同じ形）:

- `TestCreateSessionSpawnWiringManaged` — 系譜（wire と meta の両方）、`h.Send` へ届く本文の
  封筒、**配達時点で注入記録が既にあること**、create 後に枠が漏れていないこと。
- `TestRecreateSpawnedChildKeepsOneSlotManaged` — managed の recreate が
  `handOverSpawnLineage` を呼び、子 1 本が枠 1 本のままであること。

**§87.11 が「固定できていない」と列挙した 3 件のうち 2 と 3 が、managed 経路では固定できた。**
理由は経路の性質そのもので、tui の配達が `go deliverInitialPrompt`（窓の外）なのに対し、
managed の `h.Send` は **`slot.publish` の直後・ハンドラ復帰の前**、つまり窓の内側で同期的に
呼ばれる。偽ハンドルの `Send` の中で「いま親は子を何本抱えているか（`countChildren`）」と
「予約はいくつ残っているか」を同時に読めば、公開と解放が 1 つのクリティカルセクションで
起きたかどうかが外から観測できる。§87.10 の訂正（効くのは managed だから、という根拠）が
そのまま試験になった形である。

陽性対照（コードを壊して落ちることを確認済み）:

| 壊した箇所 | 落ちたテスト |
|-----------|------------|
| managed 分岐から `noteCreateOrigin` を消す | `TestCreateSessionSpawnWiringManaged`（配達時に無バッジ） |
| managed 分岐の `noteCreateOrigin` を `h.Send` の後ろへ移す | 同上 |
| managed 分岐の `slot.publish` を `session.WriteMeta` に戻す（解放は復帰時の defer 任せ） | 同上（窓の内側で 1 本の子 ＋ 1 件の予約＝二重計上） |
| managed の recreate から `handOverSpawnLineage` を消す | `TestRecreateSpawnedChildKeepsOneSlotManaged`（子が 2 本に見える） |

残るのは §87.11 の 1 件目（`publish` が 1 つのクリティカルセクションであること）だけで、
これは割っても別の呼び手が枠を取るため総数が変わらず、依然として外から観測できない。
