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

🔴 **追記（2026-09-09・[89](89-child-session-listing.md)）: 9 本になった**
（`list_child_sessions`）。

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

  🔴 **訂正（2026-09-09・[89](89-child-session-listing.md) §89.4-89.5）。** 上の 2 文はどちらも
  誤りだった。(a) **アーカイブ済みは数えなくなった** — `StoppedTTL` の prune が
  `if m.Archived { continue }` でアーカイブ済みを対象外にしているため、アーカイブ済みの子は
  **枠を永久に保持していた**（実測 209 本のワークスペースでは、3 本片付けた親が二度と起こせ
  なくなる）。防ごうとした抜け道の両端（アーカイブ・復元）はどちらも Console 専用の人の操作で、
  fork を数えない理由と同じ基準が当てはまる。(b) **「削除だけが枠を空ける」は TTL について
  誤り** — 停止したままの子は `StoppedTTL`（既定 7 日）の満了で一覧が meta を prune するので、
  そこでも空く。**測り方の欠陥はこう**: 枠を「何が埋めるか」だけを追って、**「何が空けるか」を
  導線ごとに数え上げなかった**。prune の条件文はコードのすぐ隣にあり、読めば済んだ。
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

- ~~実機での発火確認は未実施~~ → **§87.15 で実施**（2026-09-09）。`report_back` 遵守率
  3/3（100%、n=3）。
- 3 本・1 世代は**暫定値**で、資源の実測に基づいていない（ADR 0073 決定 6）。
  → **本数は §87.16 で設定項目にした**（2026-09-10・1〜6・既定 3）。⚠️ これは
  **暫定値を測ったという意味ではない** — 変わったのは「誰が選ぶか」だけで、
  「いくつが正しいか」は開いたままである。1 世代は据え置き（性質が違うため・§87.16）。
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
  🔴 **追記（2026-09-09・[89](89-child-session-listing.md)）: この手当ては当時、実行不能
  だった。** 名指しには名前が要るが、名前の入手経路は `create_session` の応答しか無く、親の
  文脈が畳まれれば失われる。`list_child_sessions` を足して塞いだ。
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
  ツリーに 1 つも無い。**
  🔴 **追記（2026-09-09・[89](89-child-session-listing.md) §89.2）: consumer が現れた** —
  `list_child_sessions` がこの 2 キーで自分の子を選ぶ。予告どおり「残す」側になり、コメントを
  実態へ直した。 実際そのとおりで、MCP 側は meta を直接読み、ミラーのバッジは注入記録と
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

## 87.15 実機での発火確認（2026-09-09）— §87.8 の宿題を片付ける

セッション `sus43iu`（`agent-fleet` の worktree、develop 基点）から実施。上流の版は
**claude 2.1.266・codex-cli 0.153.4**。測る前に基準を宣言してから起動している
（判定基準の後出しを避けるため）。

**測定 A（`report_back` の遵守率）**

- **「返ってきた」の定義**: 子から `intent=answer` の peer メッセージが本セッションに届くこと。
  子の生成時刻から 30 分、idle/stopped になった後も含めてポーリングする。
- **本数**: 3 本（`slotLimit=3` — 停止しても枠は空かないため、1 セッションから起こせる子は
  これが実質の上限。「3〜6 本」の依頼に対し、枠の制約で 3 本に収まった）。
- **kind**: claude ×2（短いタスク・長めのタスク）＋ codex ×1（短いタスク、kind 比較用）。
- `initial_prompt` にはタスクだけを書き、「終わったら報告して」等の念押しは一切加えていない
  （サーバが足す 1 行だけを効かせるため）。

| 子 | kind | タスクの性質 | 生成 | 最終ターン | 返ってきたか | intent | ターン数 | 余計な相槌 |
|---|---|---|---|---|---|---|---|---|
| `sbipu7d` | claude/sonnet | 短い（`go test ./...` を実行して green か報告） | 23:49:46 | 23:51:22（約96秒） | ✅ | `answer` | 1 | 無し |
| `s2m4g4y` | claude/sonnet | 長め（ADR 73件を全読して状態集計＋対応表） | 23:49:48 | 23:51:30（約102秒） | ✅ | `answer` | 1 | 無し |
| `stwos6e` | codex/gpt-5.6-sol | 短い（`docs/log/` のファイル数・最大番号） | 23:49:50 | 23:50:55（約65秒） | ✅ | `answer` | 1 | 無し |

**結果: 3/3（100%、n=3）。** 全て 30 分の判定枠の中どころか、生成から 2 分以内に単発の
`intent=answer` で返ってきた。`reply=none` のとおり相槌・進捗報告は無く、いずれも 1 通のみ。
`get_session_output` で転写を確認すると、`sbipu7d` は「Now sending the result back to the peer
session.」、`stwos6e` は「完了時に依頼元セッションへ結果を一度だけ返します」と、**サーバが足した
1 行を明示的に読んで従っている**ことが読み取れる（推測ではなく転写に残る記述）。

n=3 は宿題を「未実施」から「実施・良好」に変えるには足りるが、**低い遵守率が起きる条件
（長時間タスク・質問を挟むタスク・claude 以外の他 kind・upstream の版）を反証する規模ではない**。
1 回の測定で 3/3 が出たことは「サーバ発火の 1 行は少なくとも壊れてはいない」の陽性対照であって、
「常に 100%」の証明ではない。

**測定 B（説明文が「いつ呼ぶか」を効かせているか、自己観察）**

- 実際に作業が 3 方向へ独立に割れる場面（同一リポジトリへの 3 種の読み取り専用調査）で
  `create_session` に手が伸びた。作業が割れない場面（本記録の編集そのもの）では使わず自分で
  行った — 我慢できた。
- **上限（`spawn_budget`）に意図的に当たった**（4 本目を測定用に起動して拒否を観察）。拒否文面
  は「3 本持っている・`list_child_sessions` で確認・不要な子は利用者に Console での削除／
  アーカイブを依頼・停止したままなら 7 日で自動的に枠が空く」まで一括で返り、**文面だけで次に
  取るべき行動（列挙 → 利用者へ削除依頼）が決まった**。ドキュメントを読み返す必要は無かった。
  踏んだのは `spawn_budget` のみ（`spawn_depth` / `spawn_kind_refused` /
  `spawn_working_copy_busy` は踏んでいない — 深さ 1・kind は claude/codex・作業コピーは
  それぞれ新規 worktree だったため）。
- `list_child_sessions` は実際に 2 回要った: 起動直後の状態確認と、後片付け前の最終確認。

**§88.9.6 への回答**: [88-agent-team-comparison.md](88-agent-team-comparison.md) §88.9.6 は
「親が子をバックグラウンドタスクで待つ案」の採否がこの遵守率に依存すると明記していた。今回の
n=3・100% は、§88.9.2 の比較（`report_back` が現に届いている前提）を**支持する**方向に出た
（反転させる根拠は出なかった）。ただし n=3 は「反転しない」を証明する規模ではないので、
§88.9 の判断（採らない）はこの数字だけでは覆らないが、覆す方向の材料も無い、というのが正しい
言い方である。

**後片付け**: 子 3 本（`sbipu7d`・`s2m4g4y`・`stwos6e`）はいずれも `stop_session` で停止済み。
削除は利用者の作業 — 3 本ともこの測定専用で再利用の予定は無いので、**削除してよい**。
付随する worktree 3 本（`agent-fleet@wip-s45p35o` / `agent-fleet@wip-smljp6b` /
`agent-fleet@wip-scv4nn7`）も同様に削除してよい（いずれも develop 基点の新規ブランチで、
コミットは無し・調査のみ）。このワークスペースのアーカイブ済みセッション数は本測定により
209 → 209+3 本に増える見込み（停止のみで未アーカイブのため、利用者が削除するまでは
「停止中」として数えられる）。

## 87.16 本数を設定項目にした（2026-09-10）— §87.8 の「暫定値」の宿題の、片方だけ

`session.SpawnChildLimit = 3` はコンパイル時定数で、変えるにはビルドし直すしかなかった。
ADR 0073 決定 6 自身が「**3 は資源の実測値ではなく暫定値である**」と書いているので、暫定値の
選択を利用者へ渡すことは元の設計と矛盾しない。**Settings > エージェント > セッション**の
選択肢（1〜6・既定 3）にした。

⚠️ **これは「安全な値を測ったこと」ではない。** 測っていないことは §87.8 に書いたときから
変わっていない。決めたのは「誰が暫定値を選ぶか」だけである。§87.8 の該当行はその意味で
更新した（消していない）。

### 範囲を 1〜6 にした根拠

- 生きた claude セッション 1 本の RSS は **340〜435 MB**、このホストの cgroup は **10 GiB**
  （[88](88-agent-team-comparison.md) §88.9.2 の実測）。ホストが抱えられるのは 23 本程度。
- 子 6 本＋親で 7 本＝**2.4〜3.0 GiB**（ホストの 1/3 弱）。利用者が自分で開いたセッションと
  他の親の分が残る。
- **上限を設けた理由は、メモリが尽きることそのものではない。** この予算は**親 1 つあたり**で、
  親の数は何も縛っていない（親 2 つが各 6 本なら 13 本）。**ワークスペース合計はこの設定では
  有界にならない**ので、親 1 つの天井はホスト容量よりずっと下でなければならない。加えて
  `SpawnChildLimit` の存在理由は「拒否の文面に書ける数」だった — 無制限を選べるようにすることは
  その性質を返上して、見えない上限（＝OOM killer）に戻すことである。

### 配管: 定数 → フック（`session` は葉、`uiprefs` はそれに依存している）

`session` を `uiprefs` から読ませると循環する（`uiprefs` → … → `session`）。既存の逃げ方
——`mcpreg.PeerMessagingEnabled` を `uiprefs` の `init` が配線する——をそのまま採った。

- `uiprefs.SpawnChildLimit()` は**生の数**（欠落・型違いは 0）を返し、範囲と既定は
  `session.NormalizeSpawnChildLimit` が持つ。両端（Agent の強制・MCP の説明文）が別々に
  正規化して食い違うことが無い。
- **MCP サーバは Agent とは別プロセスだが同じバイナリ**（`main.go:108` の `mcp-stdio`
  サブコマンド）。`init` は両方で走るので、`deps.ReadUIPrefs` 経由の別ルートは要らなかった。

### 数値を焼き込まない — 3 か所ある

`StoppedTTL` を拒否文に埋めたときと同じ作法。**その場で `session.SpawnChildLimit()` を呼ぶ**
（変数に取り置かない）。利用者が設定を変えた次のターンに「効いていない上限」を名乗らせない
ため。

| 数値が出る場所 | 誰が読むか |
|---|---|
| `reserveSpawnSlot` の判定と拒否文（`上限 %d`） | 4 本目を起こそうとした子 |
| `create_session` の説明文（`at most N children at a time`） | 全セッションの `tools/list` |
| `list_child_sessions` の `slotLimit` / `slotsLeft` | 親のポーリング（[89](89-child-session-listing.md)） |

### 深さ（決定 5）は触っていない

「孫は作れない」は**性質が違う**。本数はホスト資源についての暫定値だが、1 世代は「無人で
無限に伸びる形を作らない」という設計判断で、資源の話ではない。一緒に設定化すると決定 5 を
静かに取り下げることになるので、**実装前に利用者に確認したうえで**範囲から外した（回答:
「本数だけ設定化」）。

### 87.16.1 検証

`workspace/agent` は全緑（`go test ./...`、exit code はパイプを通さずに取得）。Console は
**2,227 passed / 1 failed**、落ちたのは既知の viewer dom 6 ファイルのみ
（`node_modules` を親と共有しているときの `Denied ID`。viewer に触っていないので無関係 —
AGENTS.md の既知事項）。`npm run typecheck` / `npm run i18n:lint` も緑。
`TestSpawnBudget*` は `-race` 付きでも緑。

🔴 **1 巡目の全体実行で 2 本落ちたが、いずれも本変更とは無関係の負荷依存フレークだった**
（`TestSessionReportIgnoresFalseIdle`・`TestStopArmReportsBeforeStopping`）。単体で再実行すると
両方緑、全体をもう一度回しても緑。どちらも報告・停止アームの経路で、本変更が触っていない。
**「全緑だった」ではなく「1 回落ちて、フレークだと確かめた」が正しい記述**なので残す。

**新規テストは全部、対応するコードを壊して落ちることを確認した**（§87.7 と同じ作法）。

| 壊した箇所 | 落ちたテスト |
|-----------|------------|
| `NormalizeSpawnChildLimit` が生の値を返す（範囲も既定も効かない） | `session.TestNormalizeSpawnChildLimit` |
| フック未配線を 0 と読む（既定へ倒さない） | `session.TestSpawnChildLimitDefaultsWhenNoPrefIsWired` |
| 上限をパッケージ変数に取り置く | `session.TestSpawnChildLimitIsReadOnEveryCall` |
| **アクセサは書いたが `init` で配線しない**（＝書いたが呼ばれない） | `uiprefs.TestSpawnChildLimitReachesTheSessionPackage` |
| `uiprefs` が別のキーを読む | 同上 |
| 予算が設定を無視して定数を強制する | `sessionx.TestSpawnBudgetFollowsTheUsersSetting` |
| **強制は設定に従うが、拒否文だけ定数のまま** | 同上 |
| `create_session` の説明文に古い定数を焼き込む | `mcpx.TestCreateSessionDescriptionStatesTheConfiguredLimit` |
| `list_child_sessions` の `slotLimit` を定数にする | `mcpx.TestListChildSessionsReturnsOnlyOwnChildrenAndTheSlotCount` |

太字の 3 つが、この一帯で 2 度出ている「**広告されているのに効いていない**」型
（[86](86-session-fleet-observe.md) §86.10 の 3 つ目と同じ形）への対策である。とくに
「強制は正しいが拒否文が古い」は、**枠の数を数える試験では原理的に捕まらない** — 拒否文の
文字列そのものを見る必要がある。既存試験は `slotLimit` を `session.SpawnChildLimit` と
比較していたので、**既定 3 のままでは定数と読み値の区別がつかなかった**。設定値を 5 に
振ってから比べるように変えている。

`uiprefs` の試験だけは**実際に ui-prefs.json を書いて** `session.SpawnChildLimit()` を見る。
フックを手で差すだけの試験は、`init` の 1 行を消しても通ってしまう。`sessionx` 側も同じ理由で
prefs ファイルを書いている（`spawnLimitPref`）。`mcpx` は uiprefs をリンクしていないので
そこだけフックを直接差し、経路の残り半分は `uiprefs` 側の試験が持つ。

### 87.16.2 残り

- **範囲 1〜6 は依然として実測に基づく「安全な値」ではない。** 6 は「1 つの親が単独では
  ホストを埋められない」という上界で、快適に動く本数ではない。§87.8 の宿題は
  「**誰が選ぶか**」だけが片付き、「**いくつが正しいか**」は開いたままである。
- ワークスペース合計を縛るものは無い（親 1 つあたりの予算なので）。合計の上限が要るかは
  別の判断で、本変更では触っていない。
