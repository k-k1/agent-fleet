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
