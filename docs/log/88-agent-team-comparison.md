# 88. Claude Code「Agent Team」との比較（af のセッション操縦は何を解いているのか）

- 状態: **調査のみ**（2026-09-09）。コードは変更していない。利用者の「今回の機能、Claude の
  Agent Team という機能と似てるよね。後で分析して」という指示に対する回答である。
  結論は **「似ているのは形だけで、解いている問題が違う」**。af の設計を変えるべき示唆は
  **2 件**（どちらも欠陥ではない・未実装。§88.6）。
- 対象: [ADR 0073](../decisions/0073-session-spawned-sessions.ja.md)（セッションからのセッション
  操縦）と [87-session-spawn.md](87-session-spawn.md)（その実装）。判断軸は
  [86-session-fleet-observe.md](86-session-fleet-observe.md) §86.2。
- 関連: ADR 0041（peer の封筒・arm 不可侵）/ [ADR 0029](../decisions/0029-usage-accounting.ja.md)
  §6（出自の軸）/ [ADR 0056](../decisions/0056-tool-permission-choice.ja.md) 決定 1（権限確認の
  既定スキップ）
- **追記（2026-09-09）**: §88.6-1 を採らないと決めた直後に出た別案（**親が子をバックグラウンド
  タスクで待つ**）を検討し、**採らなかった**。前提は実測で成立するので「動かないから採らない」
  ではない。§88.9。

---

## 88.1 調べ方と、この文書が対象にしている「実物」

**上流は動く。この文書がいつの何についてのものかを先に書く。**

- **調査日**: 2026-09-09。
- **対象の版**: 公式ドキュメントが `This page describes agent teams as of v2.1.178.` と名乗って
  いる版。ただし同じページが v2.1.199 / v2.1.234 / v2.1.251 / v2.1.260 の挙動差にも言及して
  いるので、**ページ自体は v2.1.260 以降の時点のもの**である。cross-session messaging のほうは
  「v2.1.224 以降（native Windows は v2.1.234 以降）」が要件と書かれている。
- **Agent Team は実験機能**である。`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1` を設定しない限り
  有効にならず、公式が `Agent teams are experimental` と明記している。**この比較は仕様の
  スナップショットであって、恒久的な事実ではない。**

**確認手段**: まず `claude-code-guide` サブエージェント（Claude Code / Agent SDK / API 専門）に
調べさせ、**そのうえで要点は一次資料に当たり直した**。サブエージェントの回答には一次資料で
裏の取れない記述が混じっていたためで（§88.8 に隔離した）、本書の断定はすべて下記 3 本からの
直接確認に基づく。

| 略号 | URL |
|---|---|
| **[AT]** | `https://code.claude.com/docs/en/agent-teams` |
| **[CSM]** | `https://code.claude.com/docs/en/cross-session-messaging` |
| **[COST]** | `https://code.claude.com/docs/en/costs` |

§88.2 から §88.7 は**確認できた事実**だけで書いている。**推測・未確認は §88.8 に隔離した。**

## 88.2 結論: 同じ問題ではない。核心は「人が見ているか」

Agent Team は **対話セッション専用**である。

> `Spawning teammates also requires an interactive session. In non-interactive mode with the -p
> flag, including Agent SDK sessions, Claude doesn't spawn teammates` — **[AT]**

そして teammate の権限確認は、**lead の端末に上がって人が答える**。

> `Teammate permission prompts appear in the lead session, so approve them there yourself.`
> — **[AT] Permissions**

af のセッション操縦は、**この前提が成り立たないところから始まっている**。§86.2 が開放の判断軸
として挙げた 3 性質の 2 番目がそれで、「見ている人間が居ない」は af では前提であって欠落では
ない。`BridgeApprovalGate` は conv が無いと即 no-op になり、セッションは既定で権限確認を
スキップして走る（ADR 0056 決定 1）。**つまりセッションから呼ぶと、承認ゲートは存在しないのと
同じになる。**

**af の機構の大半は、この 1 点から出ている。** Agent Team が人に委譲しているものを、af は
構造的な拒否で置き換えるしかない——同時 3 本の予算、孫の禁止、shell/ssm の拒否、worktree の
強制、封筒の 4 禁止は、どれも「人が答えてくれるなら要らなかったかもしれないもの」である。

したがって両者はこう並ぶ。

- **Agent Team** ≒ 一人の対話セッションを並列化する。
- **af のセッション操縦** ≒ 無人のセッションが、会計され永続する異種フリートへ仕事を足す。

**機構は重なるが、問題は違う。**「向こうにあるから af にも要る／向こうに無いから af にも
要らない」という形の議論は、この差を挟まずには成立しない。

## 88.3 寿命の差 — teammate は作業単位、af の子はセッション

Agent Team の teammate は**その対話セッションに閉じた作業単位**である。

> `**One team per session**: a session has exactly one team, scoped to that session.` /
> `**Lead is fixed**: the main session is the lead for its lifetime.` /
> `**No session resumption with in-process teammates**: /resume and /rewind do not restore
> in-process teammates.` — **[AT] Limitations**
>
> `The team config directory is removed when the session ends.` — **[AT] Architecture**

af の子は**第一級のセッション**である。親より長生きし、Console の一覧に出て、使用量の行を持ち、
recreate / fork / アーカイブ / 復元の対象になり、**削除できるのは利用者だけ**（ADR 0073 決定 13
——子であっても `delete_session` / `archive_session` は開けない）。§87.8 が「子が親より長生き
したときの掃除は利用者の手に残る」と書いているのは、これが仕様だからである。

**この差は帰結を持つ。** 向こうの上限・帰属・系譜が薄くてよいのは、teammate がセッションの
終了とともに消えるからで、af の子は消えない。§88.4 の表の右側が軒並み「無い／短命」なのは
機能の欠落ではなく、**寿命が違えば要るものが違う**ということである。

## 88.4 対応表

af 側は `workspace/agent/` からの相対パス＋行番号（2026-09-09 時点の `temp/se5kh6j`）。
Agent Team 側は出典。**すべて確認できた事実**である。

| 軸 | af（本ツリー） | Agent Team | 差の読み方 |
|---|---|---|---|
| **権限確認** | 人が居ない前提。`BridgeApprovalGate` は conv が無いと no-op、セッションは既定で権限確認をスキップ（ADR 0056 決定 1、docs/log/86 §86.2） | teammate の権限確認は **lead の端末に上がり人が答える**。`Teammate permission prompts appear in the lead session` — **[AT] Permissions**。teammate は lead の permission mode を継承し、spawn 時の個別指定は不可 | **これが核心の差**（§88.2）。向こうは人に委譲、af は構造的拒否で代替 |
| **寿命** | 子は第一級セッション。親より長生きし、削除できるのは利用者だけ（ADR 0073 決定 13） | セッションに閉じる。1 セッション 1 チーム / lead 固定 / resume で復元されない / team config はセッション終了で削除 — **[AT] Limitations, Architecture** | §88.3。**右側が薄いのは寿命が短いから** |
| **帰属・系譜** | `Meta.OriginSession`（親のセッション名）＋出自 enum の `session`。**値はサーバが `AF_SESSION_NAME` から埋める**ので詐称できない（ADR 0073 決定 1、docs/log/87 §87.2）。使用量の行に焼くのは `origin` だけで、「無人の消費か人が開いた消費か」を切る（ADR 0029 §6） | team config の `members` 配列に name / agent ID / agent type（lead は `team-lead`）。チーム名は `session-` ＋ セッション ID 先頭 8 文字。**config ディレクトリはセッション終了で削除**され、残るのは task list だけ — **[AT] Architecture**。使用量側は `/usage` の attribution 軸が `skills, subagents, plugins, and individual MCP servers` — **[COST]**。**teammate 別も、無人 vs 対話の軸も無い** | **af のほうが長く残る。** 向こうに「無人の消費」という概念が要らないのは、Agent Team が対話セッション限定だから |
| **上限** | 同時 3 本（`session.SpawnChildLimit`）。数えるのは呼び手が起こした子＝`origin=session` かつ親一致（`sessionx/session_spawn.go:42` `countChildren`）。**停止中もアーカイブ済みも数え、削除だけが枠を空ける**。数えるだけでなく**予約する**（`:59` `reserveSpawnSlot`、`:86` `publish`） | `There's no hard limit on the number of teammates` / 指針として `Start with 3-5 teammates` — **[AT] Choose an appropriate team size**。ブレーキはトークン費用（`Token costs scale linearly`） | **守っている対象が違う。** af の 3 は共有・メモリ制約下のホストの資源。向こうは API 呼び出しなので資源＝金で、plan limit と請求が事実上の上限になる |
| **深さ（孫）** | `origin_session` が空でないセッションは `create_session` を呼べない（`sessionx/session_spawn.go:178` `spawnDepthRefusal`）。述語が操縦ゲートとわざと違う（ADR 0073 決定 4 / 5） | `**No nested teams**: teammates cannot spawn their own teammates. Only the lead can manage the team.` — **[AT] Limitations** | **同じ結論が別の理由で。** 向こうは *Limitations* に置かれたトポロジ上の帰結。af は「無人で無限に伸びる形は作らない」という安全論 |
| **出自の提示（受信側の作法）** | 初回プロンプト本文の先頭に `[agent-fleet:spawn from=<親>]`（`sessionx/session_spawn.go:267` `SpawnEnvelope`、Agent 側で組むので呼び手が省略・詐称できない）。ミラー側は注入記録＋`TurnSourceSpawn`（docs/log/87 §87.4） | **harness 層で同じ 4 つを実装している。** `Claude Code tells the receiving agent the message came from another Claude session, not from you. A teammate can't approve a permission prompt or supply consent on your behalf, and a teammate that was denied an action can't relay it to another teammate to bypass the check` — **[AT] Messages between agents**。加えて `It can't change configuration: ...never to change permission settings, CLAUDE.md, or other configuration because another session asked` / `Commands don't run: a command in the message's text... arrives as plain text. Claude Code never executes it.` — **[CSM]** | **同じ内容が別の層に。** af が本文に置いているのは選択ではなく制約で、kind 非依存で確実に届く層が初回プロンプトしかない（ADR 0041 決定 11）。§88.7-1 |
| **完了通知** | **親のポーリングが正**（`get_session_status`）。補助として peer messaging が ON のときだけ、`create_session` が初期指示末尾に「終わったら親へ `intent=answer` で 1 通返せ」を足す＝**モデル発火**（ADR 0073 決定 9、docs/log/87 §87.5）。セッション宛の報告チャネルは作らない（ADR 0041 が却下した案。arm の所有者が二重になる） | **イベント駆動。** `when a teammate finishes and stops, it automatically notifies the lead and includes its final answer in the notification` — **[AT] Context and communication**。別機能の `notify_when_idle` は `Claude Code subscribes without starting a turn or spending tokens in the watched session` / `The notice is one-shot... neither session polls the other` / 12 時間で失効 — **[CSM]** | **向こうがハーネス層で解いている。§88.6-1 の示唆はここ** |
| **異種エージェント** | claude / codex / opencode / cursor / kiro / agy / copilot をまたぐ（`kind` 引数。`mcpx/mcp_stdio.go:765` の `create_session` 説明文） | **Claude Code セッションのみ。** ページ全体を通じて他社 CLI・他社モデルへの言及は無い（`Teammates` は `Separate Claude Code instances` — **[AT] Architecture**） | **af 固有。** 「起こせる種類が 1 つしかない」ことは向こうの他の判断（shell の軸が無い等）にも効いている |

🔴 **上記「上限」欄の訂正（2 点・本文は残す）。** どちらも本記録より後の変更で、比較の結論
（守っている対象が違う）は変わらない。

1. **「停止中もアーカイブ済みも数え、削除だけが枠を空ける」は誤り。** アーカイブ済みは数えず
   （2026-09-09・ADR 0073 決定 6 補遺・[89](89-child-session-listing.md) §89.4）、停止したままの
   子は `StoppedTTL` の満了でも枠が空く。起票時に「削除だけ」と書いたのは、TTL の prune が
   アーカイブ済みを対象外にしていることを**規則の側からしか読んでいなかった**ためである
   （一覧の実装を読めば分かった）。
2. **「同時 3 本」は既定 3 本。** 2026-09-10 に利用者設定になった（1〜6・[87](87-session-spawn.md)
   §87.16）。左欄の読み方は変わらない — 3 は当時も「資源の実測値ではない暫定値」で、
   設定化はその暫定値を**誰が選ぶか**を移しただけである。

## 88.5 向こうが解いていて af が解いていない問題

**2 件。1 件は af への示唆になり（§88.6-1）、1 件は非スコープでよい。**

- **(a) 完了通知がサーバ発火である。** → §88.6-1。
- **(b) 共有タスクリスト（claim・依存・ファイルロック）。**
  > `Task claiming uses file locking to prevent race conditions when multiple teammates try to
  > claim the same task simultaneously.` / `a pending task with unresolved dependencies cannot be
  > claimed until those dependencies are completed` — **[AT] Assign and claim tasks**

  af に対応物は無く、ADR 0073 は却下案としてすら挙げていない（＝検討の外）。**非スコープでよい**
  と判断する: af の子は**必ず別 worktree**で走るので、claim すべき共有状態がそもそも無い。
  分業は親が初期指示に書き分ける形になるが、それが af の子の粒度（「文脈が割れる作業」）に
  合っている。

このほか **hooks による品質ゲート**（`TeammateIdle` を exit 2 で押し戻す等 — **[AT]**）と
**auto mode のメッセージ分類器**（`It reviews each message before Claude Code delivers it`
— **[AT]**）も向こうにしか無いが、どちらも af が採るべきものとは考えない（前者は af の親子関係に
相当するフック点が無い、後者は af が分類器を走らせる話にはならない）。

## 88.6 未実装の示唆 2 件 — **どちらも欠陥ではない**

**いずれも「いま壊れている」ものではない。** 実装していないのは、今回の成果物が分析だから
である。将来やるときのために、根拠と既存決定との整合まで書いておく。

🔴 **追記（2026-09-09・[89](89-child-session-listing.md)）: 2 件とも決着した。**
**88.6-2 は実装した**（`list_child_sessions`。B5 も「残す」側で決着）。
**88.6-1 は採らないと決めた** — af がセッションへ入る経路は TUI 打鍵＝ターン開始しかないので、
子の 1 ターンを節約して親の 1 ターンを無条件に発生させるだけ（差引ゼロ）で、配達は停止中の
親を起こす。**検出には価値があるが配達には無い**ので、検出だけを 88.6-2 の列挙の行
（`lastTurnEndAt`）で救った。ADR 0073 決定 9 の補遺に記録してある。

### 88.6-1 決定 9 の補助経路を「モデル発火」から「サーバ発火」へ（P2）

- **現状**: `create_session` が初期指示末尾に足す 1 行が補助経路（ADR 0073 決定 9）。
  **届くかどうかが子のモデルの遵守に依存し、届いても子のターンを 1 回消費する。**
- **向こう**: `notify_when_idle` を**ハーネスが購読**する。watched 側でターンもトークンも
  消費せず、one-shot、12 時間で失効（**[CSM]**）。
- **af でやるなら**: `create_session(report_back=true)` が Agent 側に購読を登録し、子の状態が
  idle / exit に落ちたとき **Agent が親へ peer 封筒 1 通を配達する**。必要な状態は既に全部ある
  （status ストア、`mcpx/mcp_stdio.go:3610` `agentPeerSessionState`、peer の配達経路）。
- **既存の決定と矛盾しないこと**:
  - ADR 0041 決定 4（arm 不可侵）— 状態由来の通知は報告ではないので arm を書き換えない。
  - ADR 0073 の却下案「子の完了を親の転写へ直接注入する」— 却下理由は「投入経路が TUI への
    打鍵しかなく、受信側で通常入力と区別が付かない」だが、**採用された peer 封筒経由にも同じ
    性質がある**。ADR 自身が「peer 封筒を通すほうが、少なくとも出自が本文に載る」と書いている
    とおり、封筒に包む形なら採用済みの結論の中に収まる。**変わるのは誰が発火するかだけ。**
  - 往復ループ — `answer` はプロトコル上の終端（ADR 0041 決定 13）、かつ one-shot。
- **効き目**: 正の経路であるポーリングの回数が減る。ポーリングは §87.12 の A3 が直したばかりの
  箇所（セッション面に出力カーソルが無く、毎回テール全部を親の文脈へ再投入していた）で、
  **回数そのものを減らせるのはここだけ**である。

### 88.6-2 親が自分の子を列挙できない（P2）

- **現状**: 開けた 8 本のうち子を名指しするツール（`get_session_output` / `stop_session` /
  `stop_session_after_turn` / `resume_session`）は**すべて名前を引数に取る**が、名前の入手経路が
  `create_session` の応答しか無い。**親の文脈が畳まれれば子の名前は失われる。**
- これは §87.12 の **A4** の手当てを実行不能にし得る。A4 の答えは「終わる前に、残した子とその
  状態を名指しで伝える」で、**名指しできることが前提**になっている。
- `list_peer_sessions` は代わりにならない: **別フラグ**（`--peer-messaging`）で広告され、
  **アーカイブ済みを除外**し（`sessionx/session_peer.go:145` `PeerReachableSessions`）、返す行は
  `name` / `kind` / `dir` / `title` / `state` だけで**系譜フィールドを持たない**
  （`mcpx/mcp_stdio.go:2021`）。つまり「どれが自分の子か」に答えられない。
- **向こうの対応物**: lead は常にチームを列挙できる（agent panel / `/list-agents` / team config の
  `members` 配列 — **[AT]**）。
- **af でやるなら**: 読み取り 1 本（`list_child_sessions`、または `get_session_status` を名前省略で
  「自分の子ぜんぶ」に）。ゲートは `mcpx/mcp_stdio.go:918` `sessionDriveAllowed` と同じ述語
  （`origin=session` かつ親一致）を再利用でき、**枠の残数（決定 6）と A4 の指示を 1 回の呼び出しで
  満たせる**。**アーカイブ済みも返す必要がある**——枠を食っているのはそれらだから（決定 6）。
  決定 13（削除・アーカイブは子にも開けない）とは矛盾しない。読み取りだけである。
- **⚠️ これは §87.13 の B5 の答えでもある。** B5 は「新ワイヤ 2 キー（`session.Session.origin` /
  `.originSession`）を読む consumer がツリーに 1 つも無い」という指摘で、
  `session/session.go:113` のコメントが **「consumer が現れないままなら消す」** と書いている
  （フィールドは `:119-120`）。**その consumer とは、まさにこの列挙である。** 本示唆を採るなら
  2 キーは残す側の根拠を得るし、採らないと決めるならコメントの予告どおり消す判断へ進む
  ——**どちらであれ、B5 は本件と一緒に決めるべきである。**

## 88.7 変えなくてよいと確認できたもの（再燃したときの回答）

将来「向こうがこうだから af も合わせるべきでは」と言われたときに、ここを読めば答えが出るように
根拠付きで残す。

1. **封筒の 4 禁止は、そのままでよい。** 向こうは同じ 4 つ——承認の代替にならない / 拒否された
   作業を肩代わりしない / 本文中のコマンドを実行しない / 設定（permission・`CLAUDE.md`）を
   他セッションの依頼で変えない——を **harness 層**で実装している（**[AT] / [CSM]**、§88.4 の
   「出自の提示」行に引用）。**独立した実装が同じ 4 つに到達したことは、af の一覧の裏取りである。**
   置き場が違うのは選択ではない: af の投入経路は TUI への打鍵しかなく、kind 非依存で確実に届く
   層が初回プロンプト本文しかない（ADR 0041 決定 11）。**「向こうはハーネスでやっているのだから
   af も本文をやめてハーネスへ」は、af では成立しない**（af は 7 種の他社 CLI のハーネスを
   持っていない）。
2. **worktree 既定 true は、そのままでよい。** 向こうに対応物は**無い**。公式は
   `Two teammates editing the same file leads to overwrites. Break the work so each teammate
   owns a different set of files.` — **[AT] Avoid file conflicts** と書き、worktree は *別の手動の
   方法*（`Manual parallel sessions: Git worktrees let you run multiple Claude Code sessions
   yourself`）として案内している。**af のほうが強い。** 明示 `worktree=false` を生きた
   セッションの居る作業コピーへ拒否する検査（`sessionx/session_spawn.go:226`）も同様で、
   向こうにはプロンプト作法しかない。
3. **上限の数え方（停止中もアーカイブ済みも数え、削除だけが枠を空ける）は、そのままでよい。**
   向こうの teammate はセッション終了で消えるので、この問題自体が発生しない＝**参考にならない**。
   af の却下理由は今も有効である: 生きた子だけ数えると `resume_session` と peer の自動再開が
   `create_session` を通らずに走っている子を増やし、アーカイブで枠を空けると「畳んで起こして
   復元」で越えられる（ADR 0073 決定 6）。
4. **`shell` / `ssm` の拒否は、そのままでよい。** 向こうにはこの軸が**存在しない**（teammate は
   必ず Claude Code セッションで、任意コマンドは teammate の Bash 権限として lead の permission
   mode を継承して掛かる）。af は `kind` を選べるので要る（`sessionx/session_spawn.go:246`）。
5. **`--fleet-spawn` を独立フラグにしたこと（ADR 0073 決定 3）は、向こうの実例が支持している。**
   Agent Team を有効にすると `while agent teams are enabled, a subagent that Claude names launches
   as a teammate, **so teams can form even when you didn't ask for one**` — **[AT] Enable agent
   teams**。af が「告知なしにセッション起動権が生える」ことを避けたのは、まさにこの形である。

## 88.8 確認できていないこと（推測・未確認として隔離する）

**以下は事実として扱わないこと。**

- **⚠️ teammate の作業ディレクトリが厳密に lead と同一かは、明記が無い。** 「同一作業ツリーで
  動く」は、`Two teammates editing the same file leads to overwrites` と、worktree を「自分でやる
  別の方法」として案内している事実からの**推論**である。
- **⚠️ teammate 単位の使用量帰属**: `/usage` の attribution 軸に teammate は**挙がっていない**
  （**[COST]**）が、「不可能」とまでは書かれていない。**確認できた事実は「文書に無い」まで。**
- **⚠️ OpenTelemetry の `parent_agent_id` / `agent_id` 属性で親子を追跡できる**という説明を
  `claude-code-guide` から受け取ったが、**[COST]** にその記述は無く、裏を取れていない。
  **未確認として扱うこと。**
- **参考値**: `approximately 7x more tokens than standard sessions when teammates run in plan
  mode` — **[COST]** は確認できた事実だが、**af の 3 本という上限の根拠にはならない**。af が
  守っているのはトークンではなく共有ホストのメモリだからである（3 本が暫定値であることは
  ADR 0073 決定 6 / §87.8 のとおり変わらない）。
- **⚠️ 本書は実験機能のスナップショットである**（§88.1）。上流が GA 化・仕様変更した時点で、
  §88.4 の右列は再確認が要る。

## 88.9 検討して採らなかった案 — 親が子をバックグラウンドタスクで待つ（2026-09-09）

§88.6-1（完了通知をサーバ発火にする）を採らないと決めた直後に、利用者から別案が出た。

> 親が「子が終わるまでブロックする」処理を**バックグラウンドタスクとして起動**し、CLI 側の
> 仕組み（claude の `run_in_background`）で完了時に親が起こされる。af が用意するのは
> 「指定セッションが状態 X になるまでブロックするコマンド」1 本で足りるはず。

§88.6-1 の却下理由 3 つのうち 2 つ——「子の 1 ターンを節約して親の 1 ターンを無条件に発生させる
だけ（差引ゼロ）」と「配達が停止中の親を蘇らせる」——を、**この案は確かに回避する**。それでも
**採らない**（利用者判断）。以下は根拠と、将来やるときのための形である。調べ方は §88.1 と同じ
規律で、**確認できた事実と未確認を分けてある**。上流の版は **claude 2.1.266**。

| 略号 | URL |
|---|---|
| **[IM]** | `https://code.claude.com/docs/en/interactive-mode` |
| **[SA]** | `https://code.claude.com/docs/en/sub-agents` |
| **[SESS]** | `https://code.claude.com/docs/en/sessions` |

### 88.9.1 前提は実測で成立する（「動かないから採らない」ではない）

**実測（claude 2.1.266・2026-09-09）**: `run_in_background` のシェルタスク（`sleep 5` を 24 回
回すポーリング型のループ＝提案されている待ちと同じ形）を起こしてからターンを終え、**約 2 分
アイドルで居た親が、タスク終了の 4 秒後に打鍵なしで再開した**（ループ終了 22:49:33 →
再開 22:49:37）。陰性側の見分け方を先に宣言したうえで測っている。

**⚠️ この挙動は公式ドキュメントに書かれていない。** [IM] は
`Claude Code can respond to new prompts while the command continues executing in the background`
までで、**終了時に自動でターンが始まるとは書いていない**。[SA] がサブエージェントについてだけ
`A background subagent's results reach Claude as a completion notification in a later turn` と
書いているが、"in a later turn" は**誰がそのターンを始めるか**を言っていない。断定できる根拠は
⑴ harness が配る Bash ツールの説明文 `run_in_background runs the command detached: it keeps
running across turns and re-invokes you when it exits`、⑵ 上の実測、の 2 つだけである。

**⚠️ 測ったのは 2 分の待ちだけ。** 長い待ちについては、上流の文書に**この案を直撃する記述が
2 つある**。

- **[IM]**: `Claude Code terminates running background tasks when the operating system signals
  memory pressure, provided the session has been idle for at least 30 minutes and no turn or
  subagent is running`（v2.1.193 以降。`CLAUDE_CODE_DISABLE_BG_SHELL_PRESSURE_REAP=1` で無効化可。
  サブエージェント所有のものは代わりに 60 分で終了）。**共有・メモリ制約下のホストで 30 分以上
  待っている親のタスクは、claude 自身に刈られうる。** この案がいちばん効いてほしい場面
  （長い子を長く待つ）と、刈られる条件が一致している。無効化はできるが、**メモリが本当の制約で
  ある af のホストでその環境変数を立てるのは方向が逆**である。
- **[SESS]**: 再開時に復元されるものの一覧で `Background Bash and monitor tasks aren't`。
  **親を畳んで再開しても待ちは戻らない。** [IM] の
  `Background tasks are automatically cleaned up when Claude Code exits` も同じ向きで、
  halt（`tmux kill-session`）で待ちは死ぬ。

### 88.9.2 採らない理由 1 — 比較対象は §88.6-1 ではなく、現に動いている `report_back`

**ここを取り違えると結論が変わる。** この案が置き換えるのはポーリングだけではない。**ADR 0073
決定 9 の `report_back` が既に「子が終わったら親が起きる」を実現している**（子が peer 封筒を
1 通返し、配達は `agentResumeAndSend` が停止中の親を起こす。§87.5）。並べるとこうなる。

| 経路 | 子のターン | 親のターン | 待機中の親 |
|---|---|---|---|
| **`report_back`（現状）** | +1（報告を書く） | +1（配達で起きる） | **畳んでよい**（idle＝`activityIdleWait`） |
| **本案** | 0 | +1（待ちの終了で起きる） | **畳めない**（`BackgroundBusy`→`activityMachineBusy`） |

**親のターンはどちらの経路でも 1 回**である。差し引きの利得は**子の報告 1 ターンだけ**で、
対価は親が resident のまま固定されること（`control-plane/session_activity.go:87` が
`BackgroundBusy` を `activityMachineBusy` に落とし、`:125` の `tier1Reapable` から外す）。

**実測（同一ホスト・2026-09-09）**: 生きた claude セッション 1 本の RSS は **340〜435 MB**
（6 本で 2.2 GB / cgroup `memory.max` は 10 GiB）。**待ちプロセス自体は安い** — bash のループが
1.9 MB、Go バイナリでも 16〜18 MB で、3 本並べても 50 MB 未満。**高いのは、畳めなくなる親の
ほう**である。「親 1 本につき最大 3 プロセスが眠る」ことの評価は、そこではなかった。

これは [85-stop-after-turn.md](85-stop-after-turn.md) §85.5 の**訂正**——「self-stop の効能は
費用ではなく、**コンテナの RAM とプロセスが即座に返ること**」——と正面から噛み合う。本案は
その逆をやる。af がこの一帯で守っている制約はトークンではなくメモリである（ADR 0073 決定 6 の
「同時 3 本」も同じ理由）。

### 88.9.3 採らない理由 2 — ADR 0055 決定 1 の側面口が開く

**子が働いているあいだの限界費用はゼロである。** 子は同じワークスペースに立ち、`working` なら
それ自体が `activityMachineBusy` なので、親が待とうが待つまいがコンテナは起きている。つまり
「30 分待てば 30 分課金」は正確ではない。**問題は、その区間が無限に伸びる場合が 1 つあること**
である。

**子が `question` / `plan` / `permission` に落ちたとき**、子は `activityHumanWait` になって
**ワークスペースを起こす理由ではなくなる**（[ADR 0055](../decisions/0055-idle-stop-and-carried-interactions.ja.md)
決定 1。人待ちは何日でも続きうるので理由にしない）。ところが `state=idle` や `state=stopped` を
待つ親の待ちは**そこで終わらない**ので、`BackgroundBusy` が立ったまま**人が答えるまで課金が
続く**。ADR 0055 が塞いだ穴（AUQ を抱えた Workspace が永久に停止しない・docs/64 §64.26 の
一晩 9.4 時間）を、**親の側から開け直す**ことになる。

しかも **`BackgroundBusy` には期限が無い**。keep-awake ピンは 24h 上限
（`workspace/agent/internal/sessionx/locks.go:177`）、stop-after-turn の arm は 6h TTL
（`session.StopArmMaxAge`）。**待ちだけが無期限**で、docs/log/75 §75.5 の「止めない理由が本物
なら数時間で済み、そうでなければ勝手に切れる」という設計思想から外れる。利用者の画面に出るのは
「アイドル＋バックグラウンドのバッジ」だけで、**なぜ課金が続くのかは説明されない**
（ADR 0055 決定 11 が作った運用画面にも待ちの理由は載らない）。

### 88.9.4 個別の論点の結論（A〜D）

- **A. 待ちがワークスペースを起こし続けてよいか。** 採るなら、終了条件は「状態 X」ではなく
  **`sessionActivity(子) != machineBusy`**——「子がワークスペースを起こしておく理由でなくなった
  瞬間」——にすべきである。そうすれば待ちが単独でコンテナを抱える区間はポーリング間隔 1 つに
  縮み、到達した状態（idle / question / plan / permission / stopped / limited / exit）をそのまま
  親に渡せる。**上限時間は衛生ではなく費用の安全装置**として必須。**「畳まれてよい」は採れない**
  ——halt で待ちごと死ぬので「畳まれた、かつ二度と起きない」になる。
- **B. 待つ対象は idle か stopped か。** **`stopped` は選ばせない。** ⑴ MCP の `create_session` は
  `stop_after_turn` を受け取らない（REST の `POST /sessions` は受ける
  ——`workspace/agent/internal/sessionx/session_handlers.go:840`）ので親は「create → arm」の順に
  なるが、それは §85.8 が名指しで禁じた順序である（**arm より後に届いたプロンプトは arm を
  解除する**ので、初回プロンプトの配達が自分の arm を消しうる）。⑵ 人待ちに落ちた子は arm では
  止まらない。⑶ **答えが届いた瞬間に arm 自体が解除される**（§85.4-5）ので、**質問を 1 回でも
  した子は二度と stopped にならない**。idle 側については、af は素の `idle` より強い述語を既に
  持っている（`evalReportEvidence`＝「idle 証拠 ≥ 1 ∧ busy 証拠 = 0」を 2 tick 連続。
  [51-session-report-v2-ledger.md](51-session-report-v2-ledger.md)）ので、待ちが見るべきはそちら。
  それでも「ターンが終わった」以上のことは言えない——**機械 idle ≠ 意味的完了は設計で消せず、
  戻り値に到達状態を載せて親に分岐させるしかない**。
- **C. 種別非依存。** 🔴 **検討の途中で「claude 限定の上積みになる」と整理したのは誤りだった。**
  **af が足すコマンド 1 本は完全に種別非依存**である（読むのは子の状態で、af は 7 kind すべてに
  ついてそれを計算している）。claude 限定なのは「**待ちながら別の作業ができる**」という点だけで、
  非 claude の親は同じコマンドを**前景で**走らせればよい。そのとき親は待っているあいだ `working`
  を名乗るので、**状態としてはむしろ正直**になり §88.9.3 の問題が消える。制約は kind ごとの
  ツール呼び出し上限で、af は既に実測を持っている（opencode は 60 秒で切るが progress 通知で
  時計がリセットされる、codex は `tool_timeout_sec=600`、claude は progress を無視して困らない
  ——`workspace/agent/internal/mcpx/mcp_imagegen.go:114-118`）。**他 6 kind に「完了で親を起こす
  バックグラウンド実行」があるかは未確認**（⚠️ 「無い」とは書けない）。
- **D. `list_child_sessions` との関係。** **代替ではなく上積み。かつ依存関係がある。** 待ちは
  名前を引数に取るので、**親の文脈が畳まれた後は列挙が無いと呼べない**（§88.6-2 そのもの）。
  説明文を書き分けるなら、列挙は「いま何を持っているか・枠の残数・最後のターンの前に必ず 1 回」、
  待ちは「次の一手が特定の子の完了にしか無いとき、子が 1 本のときだけ」。**逆に、列挙の行に
  『最後にターンが終わった時刻』が載るなら、待ちの必要性はさらに下がる。**

### 88.9.5 将来やるなら、形が違う（「採るならこの形」として残す）

バックグラウンドタスクは claude 固有の人間工学であり、**同じ目的は CLI 側の機能に一切依存せず
達成できる**。`wait_for_child(name, timeout)` を**ブロックする MCP ツール**として出し、
既存の `startProgressHeartbeat`（`workspace/agent/internal/mcpx/mcp_imagegen.go:194`）で進捗通知を
撃ちながら待つ形である。

- **種別非依存**（ADR 0041 決定 6 の線を割らない）。
- **状態が正直**: 親は `working` を名乗る。コンテナが起きている理由が「このセッションが実際に
  ツールの応答を待っている」になるので、**§88.9.3 の側面口が原理的に開かない**
  （`BackgroundBusy` を使わないため `tier1` と stop-after-turn の扱いも一貫する）。
- **トークンを消費しない**（モデルはツール結果を待っているだけ）。§88.9.1 の刈り取り
  （メモリ圧・resume で戻らない）も、待ちがプロセスでなくなるので当たらない。
- **上限は kind ごとの実測値がそのまま入る**（codex の `tool_timeout_sec=600` は progress では
  伸びない硬い上限なので、10 分弱で「まだ働いている、もう一度呼べ」を返す形になる）。
- 親のプロセスが resident になる点（§88.9.2 の 340〜435 MB）だけは、どの形でも消せない。

**バックグラウンド案がこれに勝るのは 1 点だけ**——待ちながら別の作業ができること。だがそれが
要るなら、そもそも親は待つ必要がない（`report_back` で起きればよい）。**「待ちたい」と
「待ちながら働きたい」は両立しない**、というのがこの比較の結論である。

### 88.9.6 この判断は 1 つの数字に依存している

**先にやるべきことがある。** §88.9.2 の比較は「`report_back` が実際に届いている」ことを前提に
しているが、[87-session-spawn.md](87-session-spawn.md) §87.8 の宿題
**「実機での発火確認は未実施」がまだ片付いていない**。`report_back` は子のモデルの遵守に依存
する経路なので、**遵守率が低ければ本案（子のモデルに依存しない完了経路）の価値は跳ね上がり、
この結論は反転しうる**。順序は「§87.8 を測る → そのうえで再検討し、採るなら §88.9.5 の形」で
ある。

🔴 **追記（2026-09-09・[87](87-session-spawn.md) §87.15）: 測った。** n=3（claude ×2・
codex ×1、短いタスクと長めのタスク）で遵守率 **3/3（100%）**。§88.9.2 の前提を支持する方向に
出て、反転させる根拠は無かった——ただし n=3 は「反転しない」を証明する規模ではないので、
「本案を採らない」の判断はこの数字では覆らないが、覆す材料も出なかった、という以上には言えない。

**本案とは独立の欠陥候補を 1 件、別途起票する**: `tier1` は `BackgroundBusy` を machineBusy と
して尊重するのに、stop-after-turn の settle 述語 `busyEvidence()` はそれを数えていない
（`workspace/agent/internal/chatx/chat_report_reconcile.go:142`）。詳細はそちらの調査で拾う。
