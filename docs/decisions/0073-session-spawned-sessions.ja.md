# 0073. セッションからのセッション操縦は「自分が起こした子」に限り、新しい出自 `session` で記録する

[English](0073-session-spawned-sessions.md) | 日本語

- 状態: **採用・実装済み**（2026-09-09。実装の記録は
  [87-session-spawn.md](../log/87-session-spawn.md)）。設計は別セッションのレビューを 2 巡、
  実装はさらに 1 巡受けている（1 巡目: 決定 1・4・5・6・7・10・11 の訂正、決定 14 と §3-b の追加。
  2 巡目: 決定 6 のアーカイブと予約、決定 7 の比較単位、決定 14 の 2 面の分離、決定 5 の却下理由、
  §3-b の試験方針。実装レビュー: 決定 6 の予算の述語、決定 7 の比較する値、拒否の実行順）
- 関連: [86-session-fleet-observe.md](../log/86-session-fleet-observe.md)（段階 1・§86.2 の判断軸と
  §86.9 の宿題） / [0041-cross-session-messaging.md](0041-cross-session-messaging.ja.md)（加算
  フラグの型・決定 4 の arm 不可侵・決定 5 の shell 除外・決定 6 の封筒・決定 13 の intent） /
  [0029-usage-accounting.md](0029-usage-accounting.ja.md) §6（出自の軸） /
  [46-usage-accounting.md](../log/46-usage-accounting.md) §2-c /
  [51-session-report-v2-ledger.md](../log/51-session-report-v2-ledger.md)（指示台帳と arm の所有者） /
  [0056-tool-permission-choice.md](0056-tool-permission-choice.ja.md) 決定 1（セッションは既定で
  権限確認をスキップする） / [0069-image-generation-providers.md](0069-image-generation-providers.ja.md)
  （progress ハートビートと kind 別の上限実測） /
  [44-operator-interaction-graph.md](../log/44-operator-interaction-graph.md)

## 背景

段階 1（docs/log/86）で、オペレーター専用だった MCP ツールのうち観測系 4 本をセッション面へ
開けた。残る要求は**セッション操縦**、すなわち `create_session` を中心とした一群である。

そのまま配れない理由は、ツール個別の危険性ではなく、**セッションが構造的に持たない 3 性質**
（§86.2）が `create_session` の配管そのものに埋まっていることにある。

- 現在の `create_session` は `origin=operator` と `origin_conv=convID()` を刻む。セッション発だと
  **「オペレーターが起動した」という嘘**になり、ADR 0029 §6 が「無人の消費」と「人が開いた消費」を
  分けるために置いた軸が狂う。
- 冪等キー `CreateSessionKey(convID(), …)` は conv 込みである。セッションは conv を持たないので
  第 1 引数が空になり、**別セッション同士の同一内容の起動が 1 本に畳まれる**。
- `report_to` も conv であるため空になり、**起こした子の完了はどこにも届かない**。さらに
  `report_to` が空だと初期指示は注入記録に残らず（`session_handlers.go:805,833`）、`Source` 空は
  利用者自身の入力として扱われる（`session_injections.go:24`）ので、**子から見て親発の指示が
  利用者の指示と区別できない**。
- `BridgeApprovalGate`（shell 宛の承認）は conv が無いと即 no-op で、承認ゲートは存在しないのと
  同じになる。セッションは加えて既定で権限確認をスキップして走る（ADR 0056 決定 1）。

さらに `create_session` には、他のどのツールにも無い性質が 2 つある。**再帰する**（子がさらに子を
起こせる）ことと、**共有・メモリ制約下のホストの資源を実際に消費する**ことである。上限を設計に
組み込まないなら、これは開けてはいけない。

### 用語: 「引き継ぎ」は 2 つの別導線を指す

本 ADR では毎回どちらかを名指しする。混同すると決定 1・4・5 の根拠がそのまま崩れるためである。

| 呼び名 | 実体 | 出自 |
|---|---|---|
| **fork（旧称 handoff）** | `HandleForkSession`（`session_handlers.go:964`）。既存セッションから直接分岐する | `origin=handoff`・`origin_conv` を継承 |
| **引き継ぎ提案** | `propose_session_handoff`。**何も起こさない**。利用者が Console で確認し、通常の起動導線から立てる（`HandoffProposal.tsx:212` → `StartHost.tsx:94` → `useStartWork.ts:40,70` → 通常の `POST /sessions`） | 元セッションの出自を渡さないので `origin=user`（`session_handlers.go:467`） |

## 決定

### 1. 新しい出自 `session` と `origin_session` を足す

`session.Meta` に `OriginSession`（ワイヤ `origin_session`）を足し、出自の enum に `session` を
加える（ADR 0029 §6 の凍結表への追記。追記は実装と同じコミットで入れる）。

- `origin_session` を埋めるのは**サーバであって呼び手ではない**。値は MCP サーバ自身の
  `AF_SESSION_NAME`（コンテナ env であって、モデルが書ける入力ではない）から解決する。
  peer の `peer_from` と同じ扱いで、ワイヤから来た値は信用しない。
- `origin_conv` は**空のまま**にする。親セッション名を `origin_conv` に流用すれば新しい
  フィールドは要らないが、凍結された集計軸に嘘を入れることになる。
- **使用量の行に焼き込むのは `origin` だけ**とし、`origin_session` は Meta に留める。ADR 0029 §6 が
  必要としている軸は「無人か、人が開いたか」であって、`session` はその問いに答えている。**系譜は
  集計の次元ではなく台帳・俯瞰図の問い**（ADR 0041 決定 9 / docs/44）である。
- 継承するのは **recreate（`session_handlers.go:1251`）と fork（`:964`）の 2 導線だけ**。どちらも
  既に `origin_conv` を継承しており、「同じ枠をもう一度」「そこから分岐」で出自は変わらない。
- **引き継ぎ提案は継承しない**（上の用語表）。利用者が Console から起こした時点で `origin=user`
  になるのは**正しい**——それは人が開いた消費だからである。系譜がそこで切れることの帰結は決定 5 に書く。

### 2. 冪等キーの名前空間を conv から「呼び手」へ一般化する

`CreateSessionKey` の第 1 引数を conv からスコープ文字列へ一般化し、オペレーターは conv を、
セッションは自分のセッション名を渡す。**空スコープでキーを作ることを許さない**（空のまま通せば、
別々のセッションが同じ内容で起こした 2 本が 1 本に畳まれる）。

### 3. 加算フラグは独立の `--fleet-spawn`（既定 OFF）

`--self-report` との論理積で、`--chromium-attach` / `--peer-messaging` / `--image-gen` /
`--fleet-observe` と同じ加算パターン（ADR 0041 決定 3）。ui-prefs のキーは `sessionFleetSpawn`。

`--fleet-observe` に相乗りさせない。段階 1 の説明文は利用者に**「動かす操作は一切増えません」**と
約束しており、相乗りはその約束を無言で撤回する。

ただし **Console 上では観測 ON を前提条件にする**。子の様子を見る唯一の経路が
`get_session_status`（段階 1）であり、観測 OFF のまま起動だけ開けると「起こせるが二度と見られない」
状態を作るためである。

### 3-b. 段階 2 で広告する 8 本（フラグ別の全量）

| ツール | 広告するフラグ | 追加のゲート |
|---|---|---|
| `create_session` | `--fleet-spawn` | 決定 5・6・7・8（起動前の拒否条件） |
| `list_repos` / `list_models` / `get_agent_usage` | `--fleet-spawn` | 無し（起動先とエージェントを選ぶための読み取り） |
| `get_session_output` | `--fleet-spawn` | 決定 4（子限定） |
| `stop_session` / `stop_session_after_turn` / `resume_session` | `--fleet-spawn` | 決定 4（子限定）・決定 10 |

- 段階 1 の 4 本（`get_session_status` / `get_session_usage` / `list_memos` / `add_memo`）は
  `--fleet-observe` のまま動かさない。
- **`TestFleetObserveDoesNotOpenOperatorTools`（`mcp_stdio_test.go:792`）の一覧は 1 つも外さない。**
  あの試験は観測**単独**の広告集合を見ており、`create_session` / `stop_session` /
  `resume_session` / `get_session_output` がそこに並んでいることこそが、段階 2 で固定したい性質
  である（`--fleet-spawn` が無ければ出ない）。8 本を一覧から抜くのは、その性質を試験から
  取り除くことに等しい。
- 代わりに**`--fleet-spawn` を立てたときちょうど 8 本が増えることを固定する試験を足す**。
  2 本セットで「観測だけでは出ない」と「起動を入れると出るのはこの 8 本だけ」の両方が留まる。

### 4. 操縦できるのは自分が起こした子だけ

`get_session_output` / `stop_session` / `stop_session_after_turn` / `resume_session` は、対象の
Meta が **`origin == "session"` かつ `origin_session == 呼び手`** のときだけ通す。判定は段階 1 の
`memoWriteAllowed()` と同じ形のハンドラ側ゲート（`sessionDriveAllowed(target)`）に置く。
広告されたツール集合が第一の境界、これが第二の境界である。

2 条件にするのは、**fork の後継（`origin=handoff`）を子に含めない**ためである。fork は人が Console
で起こす操作であり、親が操縦してよい理由が無い。

### 5. 深さは「人の起動を挟むまで 1 世代」

**`origin_session` が空でないセッションは `create_session` を呼べない。** 呼び手自身の Meta を
1 回引くだけで判定でき、系譜を辿らないので途中のセッションが消えていても壊れない。

判定条件を「`origin == session`」ではなく「`origin_session` が空でない」にするのは、子を fork した
先が `origin=handoff` になって条件をすり抜けるからである（決定 4 とは**わざと違う述語**を使って
いる。操縦は狭く、再帰の抑止は広く）。

**「孫は絶対に作れない」とは言えない。** 子が `propose_session_handoff` を呼び、利用者が Console
から起こせば、その後継は `origin=user` / `origin_session` 空になり（用語表）、再び子を起こせる。

これは塞げないのではなく、**塞がないと決めた**。2 つのフィールドは独立しているので、
`origin=user`（人が開いた消費——集計上これが正しい）を保ったまま `origin_session` にだけ提案元を
継がせることは技術的に可能であり、集計は壊れない。採らない理由は**利用者が明示的に起こした
セッションから、提案元がたまたま子だったという理由で起動の権能を削らない**ことにある（加えて、
提案の保管と Console の起動導線へ系譜を通す配管が要る）。

したがって本決定が保証するのは木の深さではなく、**人の起動と人の起動のあいだにセッションだけで
伸ばせる段数が 1 である**ことである。無人で無限に伸びる形は作らない。

### 6. 同時に持てる子は 3 本（停止中もアーカイブ済みも数える予算）

呼び手が**起こした**（`origin=session` かつ `origin_session` が呼び手）**Meta が存在する子**を
数え、3 本で拒否する。`origin_session` だけを見ないのは、子を fork した先（系譜は継ぐが
`origin=handoff`）まで数に入ってしまうためである — fork は人が Console で行う操作で、それを親の
予算から引くと**利用者自身の fork が原因で親が起こせなくなる**。ここに抜け道は無い: fork した先も
起こせない（決定 5 の述語は系譜を見る、わざと広いほう）。

- **枠が空くのは削除（`RemoveMeta`）だけ。** アーカイブは Meta を残して一覧から隠すだけで
  （`Archived` フラグ・`session_handlers.go:134`）、**復元導線がある**。アーカイブで枠を空けると、
  畳んで枠を空けて起こして復元する、で上限をいくらでも越えられる。
- 「生きた子だけ数える」でも破れる。`resume_session`（`mcp_stdio.go:2300`）も peer 送信の
  自動再開（`:1789` → `agentResumeAndSend:3246`）も `/start` を直接叩き、**`create_session` を
  通らずに走っている子を増やす**からである。消すまで数えるなら、増やす唯一の入口が create に戻る。

**recreate は枠を 1 本に保つ。** recreate は旧 meta をアーカイブ保持したまま新 meta を作り、
両方が親を継ぐので、放っておくと**子 1 本が枠を 2 本食う** — 利用者自身の recreate が原因で親が
起こせなくなる。fork を数えない理由と同じなので、成功時に系譜を後継へ移す
（`handOverSpawnLineage`）。**その帰結**: 置き換えられた側は「`origin=session` かつ系譜が空」で
アーカイブに残るので、**利用者が復元するとそれは子を起こせる**。ここへ到達するには recreate と
復元という Console 専用の人の操作が 2 回要るので、決定 5 の「人の起動と人の起動のあいだ 1 世代」
は破れていない。塞がないのは、塞ぐと**利用者が意図して置き換えたセッションに枠を課し続ける**
ことになるからである。

**枠は数えるのではなく予約する。** 数え上げを create の冪等台帳（`session_idempotency.go:48`）と
同じ排他の下に置くだけでは足りない。あの台帳が直列化するのは同一冪等キーだけで、内容の違う
2 本の並行 create は互いの Meta が書かれる前に走るため、どちらも「既存 2 本」を見て 4 本になる。
親ごとのカウンタを台帳の `begin` と同じロックの下で **+1 してから launch し、失敗時に戻す**。
数える対象は「存在する子の Meta ＋ この呼び手の進行中の create」である。

**3 は資源の実測値ではなく暫定値である。**

実測に置き換えるまでは、拒否の文面に数値を書いて「見えない上限」にしないことで担保する。

### 7. worktree は既定 true、`worktree=false` は他の生きたセッションの作業ディレクトリへは拒否

オペレーター面の既定は `false`（人が「ここで動かして」と言える）だが、セッション面の既定は
`true` にする。既定のまま呼ぶと親と子が**同じ作業コピーを 2 つのエージェントで共有**し、それは
運用指示が全セッションに対して名指しで禁じている事故そのものだからである。

明示的な `worktree=false` も、対象で別の生きたセッションが動いているときは拒否する。

- **比較するのは `dir`**——つまり作業コピーそのもの——で、`subdir` は含めない。同じ作業コピーである
  以上、片方が `console/` で片方が直下でも事故は同じだからである。
- **比較する値は「解決後の」`dir`**、すなわちそのセッションが実際に走るディレクトリである。create は
  空の dir をホームに、相対パスをホーム基準に直すので、**要求のまま比べると `dir: ""` が
  ホームで走っているセッションを素通りする**。symlink も解決して比べる（同じ実体を指す別の綴りが
  別物として通るのは、この検査を飾りにする）。
- 停止中のセッションは数えない——ここで守っているのは同時に走る 2 プロセスであって、枠ではない
  （決定 6 とは目的が違う）。

### 8. `kind=shell` / `ssm` は拒否する

ADR 0041 決定 5 と同じ理由。shell への起動は任意コマンド実行であり、汚染されたリポジトリを
読んだセッションが任意のコマンドを他所で走らせられる形を作らない。`BridgeApprovalGate` は
conv が無いと no-op なので、**オペレーター面にある承認ゲートはセッション面には存在しない**。

### 9. 完了は親のポーリングを正とし、子からの `intent=answer` 封筒を任意の補助にする

- `report_to` は**空のまま置く**。セッション宛の報告チャネルは新設しない — ADR 0041 が
  「報告の宛先は会話であってセッションではない」として却下した案であり、arm の所有者
  （docs/log/51）を壊す。
- 正の経路は**親が `get_session_status` をポーリングする**こと。決定 3 の前提条件はこのためにある。
- 補助として、**peer messaging が ON のときに限り**、`create_session` は `initial_prompt` の末尾に
  「終わったら親（名前）へ `send_to_peer_session` の `intent=answer` で 1 通返せ」の 1 行を足す
  （引数 `report_back`、既定 ON。`initial_prompt` が空のときは足さない）。
- **arm は一切触らない**（ADR 0041 決定 4）。`answer` はプロトコル上の終端（決定 13）なので、
  この 1 行が往復ループを生むことはない。
- 文言を**モデルに書かせずサーバが足す**のは、届く/届かないが呼び手の作文に依存しないためである。
- この 1 行を含む初期指示そのものの出自は決定 14 で扱う。

### 10. セッション発の `stop_session` は `disarm_report` を送らない

オペレーターの `stop_session` は `disarm_report:true` を送る。あれは「オペレーターが自分の指示を
取り下げた」の意味であって、**親セッションが子を畳むことはオペレーターの指示を取り下げない**。
省略すれば既定 false（`session_handlers.go:1064`）なので、セッション面は単に送らない。

**ただし「arm を変えない」と「無影響」は別である。** 停止そのものが、その子に対する既存の報告を
保留状態にする（`chat_report_reconcile.go:186,236`）。オペレーターが子へ指示を出していた場合、
親が畳むとその報告は消えないが**遅れる**。主張はあくまで「arm を書き換えない」であって、
「オペレーター側から見て何も起きない」ではない。

### 11. `create_session` は progress ハートビートを出す

`create_session` は最悪 40 秒（POST）＋ 45 秒（冪等ルックアップの待ち）で
（`agentCreateSession:3119`）、**opencode の 60 秒ツール上限**を越える。ADR 0069 が入れた
`startProgressHeartbeat`（10 秒間隔の `notifications/progress`）をそのまま使う。

区別すべき 2 つの時計がある。**Agent 側の HTTP タイムアウト**（`agentDo` の既定 15 秒・
`mcp_stdio.go:3062`、create だけ 40+45 秒）と、**クライアントが 1 回の `tools/call` に許す上限**である。
後者は kind ごとに違い、実測済み（ADR 0069 ja:231）: claude は progress を timeout に使わず、
codex は af builtin へ焼かれた `tool_timeout_sec=600` に収まり、**opencode 1.18.29 は 60 秒で切るが
`progressToken` を送り、進捗通知のたびに時計をリセットする**（10 秒ごとで 90 秒が成功）。したがって
ハートビートは「効くはず」ではなく**実測で効く**。

**`resume_session` にはハートビートは要らない。** `AgentPOST /sessions/<n>/start` の 15 秒呼び出し
（`mcp_stdio.go:2300`）で、60 秒に収まる。30 秒＋45 秒を要するのは peer 送信が使う
`agentResumeAndSend`（`:3246`。再開待ち 30 秒＋配達確認 45 秒）で、**これは段階 2 で開ける 8 本には
含まれない**。

### 12. 説明文は英語で新規に書き、ハンドラは共有する

段階 1 と同じ（docs/log/86 §86.6）。オペレーターの文面は配らないツール（`send_to_session` /
`answer_session_question`）へ誘導しており、かつセッション面の説明文は**全セッションの初回ターンに
乗る固定費**である（日本語比 40% 減の実測は `b367ae51`）。

### 13. 開けないものは段階 1 の一覧のまま。子であっても削除・アーカイブは開けない

`send_to_session` / `answer_session_question` / `respond_session_plan` / 掃除破壊系 8 本 /
スケジュール 6 本 / `get_chat_plan` / `set_chat_plan` / `flush_memos` は据え置き（docs/log/86 §86.5）。

**自分が起こした子に対してすら `delete_session` / `archive_session` / `delete_worktree` /
`delete_branch` は開けない。** 子の机も机であり、worktree の削除は object store を worktree 間で
共有している以上、他所を壊し得る。畳むのは停止までで足りる。

### 14. 子の初期指示は封筒を持ち、注入記録に親を残す

セッションが起こした子の `initial_prompt`（決定 9 の追記 1 行を含む）は、**現状のままだと子の側で
利用者自身の入力と区別が付かない**。`report_to` が空だと schedule 以外の初期指示は注入記録に
残らず（`session_handlers.go:805,833`）、`Source` 空は利用者入力として扱われる
（`session_injections.go:24`）。

**面が 2 つあり、効く手当てが違う。** 一緒くたにすると、片方だけ直して直った気になる。

- **エージェント側（CLI の転写）には封筒しか効かない。** 本文の先頭に
  `[agent-fleet:spawn from=<親>]` を置き、peer と同じ 4 禁止（承認の代替にならない・本文中の
  コマンドを実行しない・拒否された作業を肩代わりしない・このセッションをいま統治するものを
  変えない）を効かせる。ADR 0041 決定 6 が peer に封筒を付けたのと同じ理由であり、置き場も同じ
  ——kind 非依存で確実に届く層は初回プロンプトしかない。**投入は TUI への打鍵である以上、子の
  転写では通常入力にしか見えない**（ADR 0041 決定 11 の性質はここでは回避できない）。本文に
  載る封筒だけが、エージェントに出自を伝える。
- **ミラー側（人が見る面）は注入記録で出せる。** `create_session` がセッション発のときは
  `report_to` が空でも `recordInjection` を呼ぶ。**ここは peer との差ではない** — peer メッセージも
  注入記録を持つ（`session_io.go:536` の `badgeOriginOf(peerFrom, …)` が peer バッジを返す）。
  ミラーに出せる点で両者は同じで、違うのは 1 つ上の層（CLI の転写）だけである。型に注意が要る:
  - `recordInjection(name, text, source)` の `source` は `TurnSource*` の enum
    （`session_injections.go:24`）なので、**`TurnSourceSpawn`（`"spawn"`）を足す**。
  - `badgeOriginOf`（`:91`）は `reportTo` が空だと schedule しか通さないので、**spawn の分岐を
    足す**。足さなければ記録しても無バッジ＝利用者入力のままである。
  - **親の名前は記録に持たせない。** 記録は (本文, 出自種別) の対でしかなく、子の親は Meta の
    `origin_session` で一意に決まる。バッジの種別は記録から、名前は Meta から引く。
- **記録を投入より前へ動かすのは、本 ADR の実装要件である**（既存の挙動ではない）。いまの create は
  `go deliverInitialPrompt` を先に起こしてから `recordInjection` を呼ぶ
  （`session_handlers.go:825,833`）ので、配達が速ければターンが記録より先に現れ、**無バッジのまま
  確定する**。送信経路は既に「打鍵の前に記録する」形になっている（`session_io.go:531`）ので、
  create をそちらへ揃える。記録は本文をキーにして、後から現れたターンへ突き合わせる。
- **根拠は出自の欠落そのものに置く。** ここから権限の洗浄（拒否されたセッションが子に代行させる）
  まで成立するかは、モデルの挙動に依存する推測である。実証されているのは「出自が落ちる」ことまで
  であり、それだけで塞ぐ理由になる。

## 却下した案

- **`report_to` にセッション名を入れられるようにする。** ADR 0041 が既に却下している。報告の
  宛先は会話であり、セッション宛の報告チャネルを新設すると arm の所有者が二重になる。
- **`origin_conv` に親セッション名を流用する。** 新フィールドは不要になるが、凍結された集計軸に
  嘘が入り、`by=origin_conv` の集計が会話名とセッション名の混在になる。
- **引き継ぎ提案の経由でも `origin_session` だけ継がせる（孫を完全に塞ぐ）。** `origin=user` を
  保てるので集計は壊れず、**技術的には成立する**。採らないのは、利用者が明示的に起こした
  セッションから、提案元がたまたま子だったという理由で起動の権能を削ることになるからである。
  提案の保管と Console の起動導線へ系譜を通す配管も要る（決定 5）。
- **上限を「生きた子」で数える／アーカイブで枠を空ける。** 前者は `resume_session` と peer の
  自動再開が create を通らずに走っている子を増やすので上限が上限でなくなり、後者は畳んで
  起こして復元するだけで越えられる（決定 6）。
- **枠を予約せず、create の直前に数えるだけにする。** 冪等台帳が直列化するのは同一キーだけなので、
  内容の違う並行 create が互いを見られず上限を越える（決定 6）。
- **`--fleet-observe` に相乗りさせる。** スイッチ 1 つで済み、決定 3 の前提条件も自動的に満たされる
  が、既に ON にしている利用者に**告知なしにセッション起動権が生える**。
- **深さ・本数を資源の実測から動的に決める。** 見えない上限は、踏んだときに理由が分からない。
  固定値で拒否し、拒否の文面に上限を書く。
- **起動時に承認ゲートを掛ける。** conv が無い以上 `BridgeApprovalGate` は no-op で、掛けたつもりに
  なるだけである（ADR 0041 の補遺が env の遮断について記録したのと同じ失敗の形）。
- **子の完了を親の転写へ直接注入する。** 投入経路は TUI への打鍵しか無く、受信側では通常入力と
  区別が付かない（ADR 0041 決定 11）。peer 封筒を通すほうが、少なくとも出自が本文に載る。

## 影響 / 未解決

- **`origin` に `session` を足す先は 4 か所**（実装と同じコミットで入れる）: ADR 0029 §6 の凍結表、
  `session.ValidOrigin`（`session.go:59` — ここを漏らすと `session` が黙って `user` へ落ちる）、
  Console の凍結順テーブル（`usage/colors.ts:86`）、使用量ラベルの ja/en
  （`locales/ja/usage.ts:127` / `locales/en/usage.ts:128`）。
- **Meta による系譜追跡は Meta が在るあいだだけ有効。** `RemoveMeta`（削除導線
  `session_handlers.go:1035`）で `origin_session` は消え、その子の親は二度と辿れない。使用量の行に
  焼かれた `origin` は残るので**「無人の消費だった」ことは残り、「誰の子だったか」は消える**。
  系譜を永続させたいなら別の置き場（台帳）が要る——本 ADR では取らない。
- **docs/44 の俯瞰図は `origin_session` を読めば系譜を描ける**（上の保持期間の範囲で）。ADR 0041
  決定 9 の `DispatchEntry` に `kind:"spawn"` を足す案は採らない。
- 段階 1 と同じく、**実機での発火確認は未実施**。説明文は「いつ呼ぶか」を規定して発火率を
  上げる意図で書くので、実セッションで呼ばれることを確かめたい。
- 子が親より長生きしたときの掃除は利用者の手に残る（決定 13）。決定 6 の枠は呼び手ごとの
  数え上げであって予約ではないので、**親が消えれば上限も消える**。
