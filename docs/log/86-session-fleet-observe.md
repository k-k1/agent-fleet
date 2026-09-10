# 86. セッションへのフリート観測ツール開放（段階 1）

- 状態: **段階 1 実装済み**（2026-09-09）。これまでフリート・オペレーターにしか配られて
  いなかった MCP ツールのうち、観測系 4 本をセッション側サーバへ開放した。~~既定 OFF~~
  **→ 同日中に既定 ON・設定項目ごと廃止・`update_memo` 追加（§86.10）**。本文の
  「opt-in」「`--fleet-observe`」「既定 OFF」の記述は当時のもので、いまは §86.10 が正。
- 関連: [19-assistant-chat.md](19-assistant-chat.md)（`af_write` = オペレーター面の由来）/
  [51-session-report-v2-ledger.md](51-session-report-v2-ledger.md)（`--self-report` の
  1 本限定契約と指示台帳の arm）/ [58-cross-session-messaging.md](58-cross-session-messaging.md)
  ・ADR 0041（加算フラグの型と、セッション面に配らないと決めたものの理由）/
  [53-chromium-attach-view.md](53-chromium-attach-view.md) §53.8（最初の加算フラグ）/
  [21-memo-queue.md](21-memo-queue.md)（メモキュー）/ ADR 0056（承認スキップの既定）

---

## 86.1 背景

af の MCP サーバは 1 本のバイナリ（`workspace-agent mcp-stdio`）で 2 つの面を持つ。

| 面 | 起動フラグ | 配るもの |
|----|-----------|---------|
| オペレーター面 | `--write` | 読み取り 17 本 ＋ 書き込み 36 本 |
| セッション面 | `--self-report` | 自己申告・引き継ぎ（＋加算フラグで chromium 7 本 / peer 2 本 / 画像生成 1 本） |

**認可はこの「配る表」そのものである。** Agent REST のトークンは両面で同じで、
`mcpStdioCall` が「宣言していない名前を拒否する」ことだけが境界になっている
（docs/19 Q2 以来の約束）。したがって「開放」とは表の編集そのもので、追加の認証層は無い。

利用者の要求は「オペレーターにしか許可されていないツールをセッションにも開きたい」。
全 53 本を一度に検討して 1 回で開けるものではないので、リスクで段を切った。本書は
**段階 1（低リスクの観測系）** の記録である。

## 86.2 セッションに無くてオペレーターにあるもの（開放の判断軸）

段階を切る根拠は、セッションが構造的に持たない 3 つの性質である。ツール個別の危険性より、
こちらが先に効く。

1. **会話 ID（conv）が無い。** オペレーターのツールは `report_to` / `owner_conv` に conv を
   載せる。セッションから呼ぶと空になり、起こした子セッションの完了報告はどこにも届かない。
   `get_chat_plan` / `set_chat_plan` や `session_mode=assistant` の定時実行は、conv を前提に
   した機能なので意味自体が失われる。
2. **見ている人間が居ない。** オペレーターのツール説明にある「実行前に利用者へ確認すること」は
   チャットに人が居る前提の緩和であって、強制ではない。`BridgeApprovalGate`（shell 宛の
   `create_session` / `send_to_session` に掛かる Discord 承認）も **conv が無いと即 no-op** に
   なる。加えてセッションは既定で権限確認をスキップして走る（ADR 0056 決定 1）。
   つまりセッションから呼ぶと、承認ゲートは存在しないのと同じになる。
3. **返信の上限が無い。** オペレーターの自動応答には連続上限（既定 10・最大 50）がある。
   セッション側に相当物は無い。peer messaging のレート制限（6 通/分・重複 drop・未読上限）は
   peer 専用で、他のツールには掛からない。

これに、セッションは**攻撃者影響下になり得るデータを読む**（リポジトリの中身）という
docs/30 以来の前提が重なる。開放したツールは「汚染リポジトリ → 他セッション・永続設定」への
横展開経路になり得る。

## 86.3 段階 1 で開けた 4 本

`--self-report --fleet-observe` で加算する。既定 OFF、ui-prefs の `sessionFleetObserve`。
（**§86.10 で撤回**: フラグも設定も無くなり、`--self-report` の面そのものになった。）

| ツール | セッションでの用途 | 開けた理由 |
|--------|------------------|-----------|
| `get_session_status` | 指示を渡した相手がまだ動いているか | 状態は `list_peer_sessions` が既に返している値と同種。ただし §86.4 の削りを掛ける |
| `get_session_usage` | 自分の文脈の詰まり具合を見て引き継ぎを判断 | 自分の消費を自分で見るだけ。副作用なし |
| `list_memos` | 追加前に重複と既存ラベルを見る | 読むだけ |
| `add_memo` | 頼まれた範囲の外で見つけたことを利用者へ残す | 宛先は**人**。キューは Console で人が見てから送る |

**観測と書き残しを 1 つのスイッチに載せた理由**: これは 1 つの行為だからである。見ている人が
居ない状況で気づいたことを人へ返す唯一の経路がメモで、観測だけ開いて出口を閉じると、
気づきはそのセッションの転写に埋もれて消える。

`add_memo` だけは書き込みツールなので、ハンドラ側の `writeEnabled()` を
`memoWriteAllowed()`（= `writeEnabled() || mcpFleetObserveEnabled`）へ広げた。
`update_memo` / `delete_memo` / `flush_memos` は素の `writeEnabled()` のまま
— **セッションは利用者のキューに足せるが、書き換えも送信もできない**。
（**§86.10 で `update_memo` は開けた**。境界は「消せない・送れない」へ動いた。）

## 86.4 `get_session_status` から `questions` と `plan` を落とす

セッション面に返す前に、`questions`（保留中の質問）と `plan`（承認待ちのプラン本文）を
削る（`withoutPendingInteraction`）。オペレーターには従来どおり両方返す。

理由は 3 つある。

- 段階 1 は `answer_session_question` / `respond_session_plan` を**意図的に配っていない**。
  他セッションの質問に答える・プランを承認するのは、**利用者の承認の肩代わり**だからである
  （ADR 0041 決定 7 が peer の受信側に課したのと同じ禁止）。本文だけ渡すと、正面のツールは
  無いのに肩代わりに必要な入力だけがモデルの手元に残る。peer メッセージへプランを転記して
  他所で承認させる、といった迂回路の材料になる。
- この 2 つが payload の中で**最も大きく、最も攻撃者影響下**にある（他セッションの出力そのもの）。
  プラン本文には上限が無い。
- セッションが**自分自身**の status を見るときも損は無い。自分に向いた質問を自分で読み直す
  意味は無いためである。

パースできない入力は**素通し**にする。これは表示上の削りであって、読めない status を返すのは
「触ってはいけない欄が付いた status」より悪い。

## 86.5 開けなかったもの（段階 1 の線引き）

`TestFleetObserveDoesNotOpenOperatorTools` が名前で固定している。後の段階はこの一覧と
議論することになる。

- **`send_to_session`** — peer 封筒が付かないため、受信側の転写では利用者の入力と区別が
  付かない（ADR 0041 決定 11 が「再現できない」と確定させた性質）。shell 宛はノーゲート。
  代替は `send_to_peer_session` で、差分は「報告付き送信」だけだが、それは conv が無い以上
  どのみち成立しない。
- **`answer_session_question` / `respond_session_plan`** — §86.4 のとおり承認の肩代わり。
- **掃除・破壊系 8 本**（`archive_session` / `delete_worktree` / `delete_session` /
  `delete_branch` / `restore_cleanup_archive` / `purge_cleanup_archive` /
  `restore_memory_snapshot` ほか）— 他セッションの机を消す操作で、運用指示が全セッションに
  対して明示的に禁じている領域。`delete_branch` は object store が worktree 間で共有である
  ことも効く。
- **スケジュール 6 本** — 停止中ワークスペースを無人で起こし、**セッションの寿命を超えて
  永続する**。`session_mode=assistant` でオペレーター会話へ投入もできる。汚染リポジトリから
  仕込める永続バックドアになるため、段階 2 でも開けない見込み。
- **`create_session` ほかセッション操縦系** — 段階 2 で別途検討する（帰属 `origin=operator` の
  嘘、冪等キーの conv 依存、再帰起動の上限、opencode の 60 秒ツール上限が未解決）。
- **`get_chat_plan` / `set_chat_plan` / `flush_memos`** — conv 前提、または封筒なしの送信。

## 86.6 実装

| 置き場 | 変更 |
|--------|------|
| `mcpx/mcp_stdio.go` | `--fleet-observe`（`--self-report` との論理積）、`mcpStdioFleetObserveTools()`、`memoWriteAllowed()`、`withoutPendingInteraction()`。フラグ解釈を `parseStdioFlags` へ切り出し（`RunStdio` は stdin で無限に止まるのでテストから触れない） |
| `mcpreg/builtin.go` | `FleetObserveEnabled` フック → `runArgs` に `--fleet-observe` |
| `mcpreg/attach.go` | codex の default-deny env へ `AF_CP_BASE_URL` / `AF_MEMO_TOKEN` を追加（§86.7） |
| `uiprefs/prefs.go` | `FleetObserve()`（欠落・不正は false） |
| `ui_prefs.go` | 切り替え時に `MaterializeAll()`（peer / 画像生成と同じ理由） |
| Console | `sessionFleetObserve`、AgentsTab「セッション」節、ja/en の説明文 |

**セッション向け説明文は英語で新規に書いた**。オペレーターの文面を共有しなかったのは、
配らないツール（`answer_session_question` 等）へ誘導する文になっており、かつセッション面の
説明文は全セッションの初回ターンに乗る固定費だからである（日本語比 40% 減の実測は `b367ae51`）。
**ハンドラは共有**していて、`mcpStdioCall` が同じ case へ落ちる。実装は 1 つ、直す場所も 1 つ。

## 86.7 メモツールだけが CP へ出ていく

af のセッション側サーバは通常ローカルの Agent REST を叩くが、メモキューだけは実体が CP の
ストアにあり、`AF_CP_BASE_URL` へ `AF_MEMO_TOKEN` で hairpin する（`cpMemoDo`）。
両方ともワークスペース起動時に注入されるコンテナ env なので他の kind は継承で持っているが、
**codex は stdio MCP 子プロセスを default-deny env で起動する**ため、明示的に転送しないと
codex セッションでだけメモツールが in-band エラーになる。`extraEnvVars` に 2 つ足した。

opt-in の有無で出し分けていない。この一覧は起動時に 1 度 codex の設定へ焼かれるのに対し
opt-in は後から切り替わるためで、転送しただけの env は何も許可しない
（境界はあくまで広告されたツール集合）。

## 86.8 検証

`workspace/agent` と `console` の全テストが緑。新規テストは、退行を実際に捕まえることを
コードを壊して確認した（陰性結果には陽性対照を、の作法）。

| 壊した箇所 | 落ちたテスト |
|-----------|------------|
| `--self-report` との論理積を外す | `TestFleetObserveRequiresSelfReport`（§86.10 で廃止） |
| メモのゲートを `writeEnabled()` だけに戻す | `TestAddMemoGateAcceptsSessionAndRefusesReadOnlyAssistant` |
| 4 本を無条件広告にする | `TestFleetObserveToolsAreOffByDefault`（§86.10 で意味が反転し `TestFleetObserveToolsAreAlwaysOnForSessions` に） |
| `plan` の削りを外す | `TestSessionStatusDropsPendingQuestionAndPlan` |
| 削りを**書いたが配線しない** | `TestGetSessionStatusTrimsOnlyForSessions` |
| en カタログのキーを消す | Console の i18n パリティ検査 |

最後の 2 つが要る理由: 関数を単体で試すテストは「書いたが呼ばれていない」を見逃す。
`TestGetSessionStatusTrimsOnlyForSessions` は Agent をスタブして実際の `tools/call` を通す。

## 86.9 残り

> **追記（2026-09-09）— 段階 2 は着手・実装済み。** 下の 1 点目は
> [ADR 0073](../decisions/0073-session-spawned-sessions.ja.md) が設計として答え、
> [87-session-spawn.md](87-session-spawn.md) が実装を記録している。宿題として挙げた 5 つの
> うち 4 つはそのまま採られ（新しい origin 値・親子関係の検査・再帰の上限・ポーリング）、
> 1 つは**前提が違っていた**: opencode の 60 秒上限に掛かるのは `create_session` だけで、
> 30 秒＋45 秒を要する `agentResumeAndSend` は段階 2 で開けた 8 本に含まれない。
> §86.5 の線引きのうち動いたのは `create_session` ほかセッション操縦系だけで、残りは据え置き。
> 併せて `TestFleetObserveDoesNotOpenOperatorTools` の一覧は**そのまま維持**した — あの一覧に
> `create_session` が並んでいることが「観測単独では出ない」を固定している当のものなので、
> 段階 2 で 8 本を抜くのは性質を試験から取り除くことになる。

- **段階 2（セッション操縦）は未着手。** `create_session` を中心に、帰属の新しい origin 値、
  親子関係の検査、再帰の上限、子の完了をどう親へ返すか（`get_session_status` の
  ポーリングか peer の `answer` 封筒か）、opencode の 60 秒ツール上限が設計課題として残る。
  ADR を 1 本起こす規模。
- **実機での発火確認は未実施。** 説明文は「いつ呼ぶか」を規定して発火率を上げる意図で
  書かれているので、`b367ae51` がやったように実セッションで呼ばれることを確かめたい。
- control-plane 側の `/mcp`（PAT 認証・外部クライアント向け）は別系統で、本書の範囲外。

## 86.10 追記（2026-09-09）— 観測は設定をやめ、既定になった。`update_memo` も開けた

段階 1 の中心的な判断（**既定 OFF の opt-in にする**）を、実装から 1 日で取り下げた。
利用者の指示は「規定 ON にして、既存ユーザーも ON にして、設定項目から消したい」。

**何が変わったか**

- `--fleet-observe` フラグ・ui-prefs の `sessionFleetObserve`・Console の設定行・`mcpreg` の
  フックを**すべて削除**した。観測系は `--self-report` の面そのものになった。
- **既存ワークスペースも ON になる。** 保存済みの `sessionFleetObserve: false` は読まれない。
  「設定から消す」を「既定値を true にする」で実装すると、**明示的に false を書いた利用者だけが
  取り残される**——それが最も多いはずの、この機能を見送った利用者である。値ごと無視するのが
  「消す」の正しい実装。
- **`update_memo` を追加**した（利用者の指示）。セッションはキューに足すだけでなく、
  自分が足した内容を直せる。`delete_memo` / `flush_memos` は据え置き。
- 退役したフラグは `parseStdioFlags` で**受け取って無視する**。MCP 設定は再 materialize まで
  古い argv を渡し続けるため。

**§86.3 の「観測と書き残しを 1 つのスイッチに載せた理由」は、スイッチが無くなっても生きている**
——観測だけ開いて出口を閉じない、という形は保たれている。`update_memo` はその出口を
「足す」から「足して直す」に広げたもので、境界（**消せない・送れない**）は動いていない。

**取り下げた理由の記録。** §86.2 の 3 性質は観測を危険にしない。危険なのは**動かす操作**で、
それは ADR 0073 の `--fleet-spawn` に分かれた。観測を opt-in のままにしていたのは、
段階 1 の時点で「セッション面を広げること」自体が新しかったからで、性質の分析からの帰結では
なかった。分けた今、観測側にスイッチを残す理由は残っていない。

**代償として認識しておくこと**: 説明文 5 本分が全セッションの初回ターンに乗る固定費になった
（利用者が外す手段は無い）。`add_memo` / `update_memo` は**全セッションが利用者のメモキューへ
書ける**ことを意味する。どちらも取り消せる（キューは人が読んでから送る）が、既定になった以上
「気づいたら誰かが書いていた」は起こる。

**検証**（§86.8 と同じ作法）。退行を入れて落ちることを確認した。

| 壊した箇所 | 落ちたテスト |
|-----------|------------|
| 観測系をフラグの後ろへ戻す（opt-in していない面から消える） | `TestFleetObserveToolsAreAlwaysOnForSessions` |
| `update_memo` を広告しない | 同上 |
| **`update_memo` を広告したままゲートを `writeEnabled()` に戻す** | `TestFleetObserveToolsAreCallableNotJustAdvertised` |

3 つ目は ADR 0073 の実装レビューで見つかった型（`list_models` が広告されているのに呼べなかった）
そのもので、**広告集合を見る試験では絶対に捕まらない**。5 本を実際に呼ぶ試験を足した。

## 86.11 追記（2026-09-10）— 「メモに追加して」がエージェントメモリーへ行った

利用者が実セッションで「メモに追加して」と言ったところ、`add_memo` ではなく **Claude 自身の
エージェントメモリー**（`CLAUDE_CONFIG_DIR` 配下の memory）に追記された。説明文は
「何のためのツールか」（人へ宛てた、頼まれた範囲の外の気づき）は書いていたが、
**利用者がその語で呼んだときに何を指すか**を書いていなかった。CLI 側は「メモを取れ」という
指示に自前の記憶機構を持っており、名前が衝突している以上、当たり負けする。

`add_memo` と `list_memos` に「利用者が『memo』と言ったらこのキューであって、自分の
agent memory / notes ファイルではない（そちらは memory と言われたとき、またはファイル名を
指されたときだけ）」を足した。§86.10 の「説明文は全セッションの固定費」がここでも効くので、
**日本語のトリガ語（「メモに追加して」等）は入れず英語のみ**にした（`b367ae51` の規約と
`TestFleetObserveDescriptionsAreEnglishAndSelfConsistent`）。メモ＝memo なので橋渡しは
モデルに任せる、という賭けである。CP 側 `/mcp`（PAT 面）は固定費の算術が当てはまらないので、
そちらの `add_memo` には `(メモ)` を直書きした。

**これで足りるとは限らない、の理由**: Claude Code はツールを遅延読み込みする構成があり、
その場合 **説明文は判断の時点で文脈に載っていない**（名前だけが見えている）。今回の取りこぼしも
それが真因の可能性がある。再発したら、常時文脈に載る `workspace/workspace-notes.md` に 1 行
足すのが次の手（今回は入れていない）。
