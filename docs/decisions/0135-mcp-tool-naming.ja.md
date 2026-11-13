# 0135. 2 つのサーバーの MCP ツール名の命名規則と、`af_report` / `af_stop_after_turn` の扱い

[English](0135-mcp-tool-naming.md) | 日本語

- Status: **accepted**（2026-11-13）：1 節の規則と、3 節の**選択肢 A**（`af_report` と `af_stop_after_turn` は文書化した例外として名前を保つ）。
  選択肢 B は退けた案です。ツールの改名・別名・説明の書き換えはありません。4 節のチェック（規則 1〜3）はこの受け入れとともに足し、
  規則 4 は見ません。
- Tracking: #3990。
- See also: [0124](0124-second-refactor.ja.md)（この記録が読むツール表の等価性ゴールデン）／
  [0041](0041-cross-session-messaging.ja.md)（`send_to_peer_session`）／
  [0105](0105-stop-continues-into-the-queue.ja.md)（`stop_session_after_turn`）
- コードの参照は `14f9d74f4` 時点の `main` の `path`。パスはリポジトリのルートからの相対で、
  `workspace/agent/` は `agent/` と略します。件数はその時点の `git grep` です。

## Context

MCP のツールを出すサーバーは 2 つあります。Control Plane の `/mcp`（`control-plane/internal/mcpsrv`）は利用者自身のクライアント向け、
Agent のワークスペース内サーバー（`agent/internal/mcpx`）はセッション自身とアシスタント向けです。両者の `tools/list` は
ゴールデン `*/testdata/mcp_tools/list.json` に固定され、生成カタログ `guide/ref/mcp/` はそこから作られます。

ツール名の付け方を述べた ADR・コメント・テストはありません。名前は隣のツールを真似て増え、結果は偶然おおむね揃っています。
重複を除いた 87 個のうち 75 個はすでに `<動詞>_<対象>` です。合わないものは 2 節にまとめます。目に付く唯一の例外が接頭辞で、
`af_report` と `af_stop_after_turn` だけが `af_` で始まります。クライアントはどのツールも `mcp__<server>__<tool>` で見せるので、
この接頭辞は読む人に何も足しません。

### 測ったこと

- **件数。** Control Plane のゴールデンは 43 ツール。Agent のゴールデンは `name#n` 形式の 90 エントリで、定義ごとに 1 つです。
  セッション向けとアシスタント向けで説明やスキーマが違うツールは 2 回出ます（`create_session#1`、`create_session#2`）。
  合計 133 エントリ、**重複を除くと 87 個**（CP 43、WS 76、両方 32）。Issue #3990 は 119 エントリと書いていますが、
  ゴールデンからはその数を再現できなかったので、上の数字を使います。
- **両サーバーにある同名。** 32 個。2 つの定義の引数名は 45 組のうち 35 組で一致します。違う 10 組は、
  `create_session#1` / `#2`（Agent 側に `effort`、セッション向けにはさらに `allow_shared_working_copy`・`report_back`・
  `spend_cap_usd`・`work_item`）、7 つの `memory_*`（Control Plane は `project` と `repository` を取り、Agent のセッション側は両方を固定）、
  `update_memo#2`（Control Plane にだけ `position`）です。
- **同名で範囲が狭いもの。** Agent サーバーのセッション向けは、対象が狭い操作に同じ名前を使います。`get_session_output#2`・
  `stop_session#2`・`stop_session_after_turn#2` は自分の子セッションだけが対象で、説明にもそう書いてあります。狭い形では届かない対象には
  別の名前が付いています：`list_peer_sessions`、`peek_session_output`、`send_to_peer_session`。
- **2 つの `af_` 名を実行時に名前で照合する所は、ツール定義の外にありません。** `mcpreg.IsAFToolName`
  （`agent/internal/mcpreg/af_server_name.go`）が使われるのは `generate_image` だけです。名前があるのは、ツール定義
  （`agent/internal/mcpx/mcp_self_report.go`）、報告を負う指示に付く `[agent-fleet]` の注記
  （`agent/internal/sessionx/session_selfreport.go`）、Console のプランレビュー用プロンプト
  （`console/src/lib/i18n/locales/{en,ja}/mirror.ts`）です。

## 提案する決定

### 1. 規則

どちらのサーバーでも、ツール名は次を満たします。

1. **接頭辞を付けない。** `af_` もサーバー名も付けません。クライアントが `mcp__<server>__` を足すので、2 つ目の接頭辞は雑音です。
   `af_` は名前がサーバー自身のもののようにも見せます。
2. **小文字 ASCII の `snake_case`**：`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`。現在の名前はすべて満たしています。
3. **動詞が先**：`<動詞>_<対象>`（`list_repos`、`stop_session`）。動詞単独が許されるのは、動詞だけで操作が決まり、対象が固定のときだけです
   （今は `watch` と `unwatch`。どちらもセッションの監視が対象）。機械で判定できるのは最初の語だけです。集まりには `list_`、
   1 つのもの・1 つの状態には `get_` を使うのは、チェックではなくレビュー担当者向けの慣習です。現に `get_schedule_runs`
   （実行履歴）、`get_session_usage`、`get_agent_usage`（セッションや CLI ごとの行）は `get_` で複数行を返しますが、そのまま認めます。
4. **どちらのサーバーでも同じ操作は同じ意味。** 同じ操作は、どこで出しても名前も引数名も同じにします。ある対象者向けに範囲を狭める
   （子だけ）ときは名前を保ち、説明に書きます。狭い形では届かない対象には、動詞の後に限定語を付けます
   （`send_to_peer_session`、`list_peer_sessions`）。1 つの名前が別々の操作を指すことはありません。
5. **改名は別名付きでだけ。** 旧名は少なくとも 1 リリースのあいだ呼べるまま（一覧には出さない）残します。アップグレード前に起動して
   旧名をプロンプトに持つセッションが、その期間は動き続けるためです。別名は失敗を狭めるだけで、無くしはしません
   （3 節「すでに動いているセッション」）。

2 つの系統は、改名せずに据え置きます。名前が共有メモリの API（ADR 0022 / 0130）で、改名がすべてのエージェント種別のメモリ用プロンプトに及ぶためです。
名詞が先の `memory_*`（8 ツール）と、単独の例外 `branch_name`・`skill_restore` です。新しいツールにはこの許しを与えません。

### 2. 現在の全ツール名を規則に照らす

重複を除いて 87 個を、最初の語でまとめました。`(CP)` は Control Plane だけ、`(WS)` は Agent だけ、印なしは両方です。
規則 1・2 は 2 つの `af_` 名を除くすべてで成り立ちます。

| 最初の語 | 名前 | 個数 |
|---|---|---|
| `list` | 16 | `list_allowlist` (CP), `list_assistants` (WS), `list_child_sessions` (WS), `list_chromium_targets` (WS), `list_cleanup_archives`, `list_cleanup_candidates`, `list_memory_snapshots` (WS), `list_memos`, `list_models`, `list_my_sessions`, `list_peer_sessions` (WS), `list_repos`, `list_schedules` (WS), `list_sessions` (CP), `list_watches` (WS), `list_workspaces` (CP) |
| `get` | 12 | `get_agent_usage`, `get_browser_action_result` (WS), `get_chat_plan` (WS), `get_chromium_attachment` (WS), `get_egress_stats` (CP), `get_image_studio` (WS), `get_memory_snapshot` (WS), `get_schedule_runs` (WS), `get_session_output`, `get_session_status`, `get_session_usage`, `get_usage` (CP) |
| `memory` | 8 | `memory_feedback`, `memory_forget`, `memory_index`, `memory_patch`, `memory_projects` (CP), `memory_read`, `memory_save`, `memory_search` |
| `delete` | 5 | `delete_branch`, `delete_memo`, `delete_schedule` (WS), `delete_session`, `delete_worktree` |
| `set` | 4 | `set_chat_plan` (WS), `set_chromium_control_mode` (WS), `set_image_draft` (WS), `set_user_quota` (CP) |
| `stop` | 4 | `stop_session`, `stop_session_after_turn`, `stop_user_session` (CP), `stop_workspace` (CP) |
| `add` | 2 | `add_image_knowledge` (WS), `add_memo` |
| `af` | 2 | `af_report` (WS), `af_stop_after_turn` (WS) |
| `create` | 2 | `create_schedule` (WS), `create_session` |
| `propose` | 2 | `propose_allowlist_change` (CP), `propose_session_handoff` (WS) |
| `restore` | 2 | `restore_cleanup_archive`, `restore_memory_snapshot` (WS) |
| `resume` | 2 | `resume_schedule` (WS), `resume_session` |
| `run` | 2 | `run_image_trial` (WS), `run_schedule_now` (WS) |
| `send` | 2 | `send_to_peer_session` (WS), `send_to_session` |
| `update` | 2 | `update_memo`, `update_schedule` (WS) |
| `answer` | 1 | `answer_session_question` (WS) |
| `archive` | 1 | `archive_session` |
| `ask` | 1 | `ask_assistant` (WS) |
| `attach` | 1 | `attach_chromium` (WS) |
| `branch` | 1 | `branch_name` (WS) |
| `detach` | 1 | `detach_chromium` (WS) |
| `flush` | 1 | `flush_memos` |
| `generate` | 1 | `generate_image` (WS) |
| `link` | 1 | `link_work_item` (WS) |
| `pause` | 1 | `pause_schedule` (WS) |
| `peek` | 1 | `peek_session_output` (WS) |
| `purge` | 1 | `purge_cleanup_archive` |
| `rename` | 1 | `rename_child_session` (WS) |
| `request` | 1 | `request_browser_action` (WS) |
| `respond` | 1 | `respond_session_plan` (WS) |
| `search` | 1 | `search_sessions` (WS) |
| `skill` | 1 | `skill_restore` (WS) |
| `tail` | 1 | `tail_audit` (CP) |
| `unwatch` | 1 | `unwatch` (WS) |
| `watch` | 1 | `watch` (WS) |

規則に照らすと：

| 規則 | 合う | 合わない |
|---|---|---|
| 1 接頭辞 | 85 | `af_report`、`af_stop_after_turn` |
| 2 形 | 87 | なし |
| 3 動詞が先 | 75 | 8 つの `memory_*`、`branch_name`、`skill_restore`、2 つの `af_` 名（最初の語が接頭辞） |
| 4 同じ操作 | 共有 32 個。うち 10 組で引数名が違う（上記） | `peek_session_output` は「動詞の後に限定語」の形から外れる（子以外に対する `get_..._output`）。改名は提案しない |
| 5 改名は別名付き | この規則のもとでの改名はまだない | |

チェックで持つ例外は、規則 3 について 12 個（`memory_*` 8 個、`branch_name`、`skill_restore`、`af_report`、`af_stop_after_turn`）、
規則 1 について 2 個（同じ 2 つ）です。
これは、チェックで試せるもの（形、接頭辞、最初の語）に対する例外です。意味の水準の点はレビュー担当者の仕事で、許可リストには入れません：
`list_` / `get_` の慣習（今わかっている外れは `get_schedule_runs`、`get_session_usage`、`get_agent_usage`）、動詞単独の
`watch` / `unwatch`、規則 4 の限定語の形（`peek_session_output`）、2 つのツールがそもそも同じ操作かどうか。

### 3. `af_report` と `af_stop_after_turn`：選択肢は 2 つ

どちらも Agent サーバー（`session` の対象者）にだけあり、Control Plane にはありません。

#### 選択肢 A. 文書化した例外として両方を残す

規則に 1 文を足します：「`af_report` と `af_stop_after_turn` は名前を保つ。呼び出したセッション自身についての 2 つのツールで、規則より前からある」。
チェックの許可リストに入れ、そのまま残します。ほかは何も変わりません。

#### 選択肢 B. 改名し、旧名を少なくとも 1 リリース別名として残す

動詞が先の候補：`af_report` → `report_done`、`af_stop_after_turn` → `stop_self_after_turn`。
（`stop_after_turn` だけでは `stop_session_after_turn` の隣に並び、同じツールに見えます。）旧名は呼べるが一覧には出さないので、
`tools/list` とカタログには新しい名前だけが出ます。

改名が触る範囲は、`14f9d74f4` 時点の `git grep -E 'af_report|af_stop_after_turn'` で、合計 **71 ファイル・213 行**です。

| 場所 | ファイル | 行 | B での作業 |
|---|---|---|---|
| 挙動：2 つのツール定義（`agent/internal/mcpx/mcp_self_report.go`）、`[agent-fleet]` の注記（`agent/internal/sessionx/session_selfreport.go`）、Console のプランレビュー用プロンプト（`console/src/lib/i18n/locales/{en,ja}/mirror.ts`） | 4 | 17 | 変更し、別名の振り分けを足す |
| Agent のその他のコード（コメントのみ）（`muse`、`chatx`、`mcpreg`、`mcpx`、`sessionx`、`control-plane/scheduler_wake.go`） | 11 | 17 | コメントを直す |
| 方針・トピックノート・エージェント知識（`workspace/workspace-notes.md`、`workspace/notes/agent-fleet.md`、`agent/knowledge/af-usage.md`） | 3 | 6 | 変更。前の 2 つは次のイメージビルドで全エージェントに届く |
| テストとゴールデン（`mcpx` の `list.json` と `calls.txt`、`sessionx` の `create_success.golden`、`_test.go` / `.test.ts` 15 ファイル） | 18 | 87 | ゴールデンを作り直す。`calls.txt` には別名の行が増える |
| カタログの元 `scripts/mcp-catalogue/tools.tsv` と生成物 `guide/ref/mcp/README{,.ja}.md` | 3 | 6 | tsv を直して再生成 |
| Console のコメント（`stopTree.ts`、`SessionMenu.tsx`、`session.ts`） | 3 | 3 | 直す |
| `docs/build/04-agent`・`08-integrations`（両言語） | 4 | 4 | 変更 |
| ADR 0035・0038・0041・0069・0093・0095・0105（両言語、13 ファイル） | 13 | 34 | **直さない**：ADR は不変 |
| `docs/log/` の記録 | 12 | 39 | **直さない**：履歴 |

大きさ以上に効く行が 2 つあります。

- **すでに動いているセッション。** アップグレード前に起動したセッションは旧名を最初のプロンプトに持ち、クライアントはツール一覧を
  キャッシュしていることもあります。別名がなければ、そのセッションは無くなったツールを呼び、報告を返しません
  （`session_selfreport.go` にも「`af_report` を呼べと言われたのにそのツールがないセッションは、報告しないだけ」とあります）。
  別名が守るのは、別名がある間だけです。別名を外したあとに再開したセッションや、古いツール一覧を持ち続けるクライアントは旧名を呼び、
  未知のツールのエラーを受けて、その指示は報告されません。そのため次の 2 つを B の一部とします。(1) 撤去は暦の日付ではなくリリースで決めます。
  Agent は自動更新するためです（ADR 0025）。別名が丸 1 リリース呼ばれなかったことを確かめてから外します（別名の振り分けに呼び出しの計数が要り、
  今はそれがありません）。(2) 撤去後、旧名への未知ツールのエラーは、どのツールに替わったかを伝えます。再開したセッションが自分で直せるようにするためです。
  どちらも B を危険なしにはしません。
- **リポジトリは旧名を捨てられない。** 不変の ADR と記録の 25 ファイルが旧名を持ち続けるので、B のあとも `git grep af_report` は空になりません。
  別名のコードには理由のコメントが要ります。

#### 推奨：A

1. 得られるものは見た目だけで、87 個のうち 2 個です。規則とそのチェックは 2 つを許可リストに置いても成り立つので、A でも強制は失われません。
2. B の負担は 213 行そのもの（大半は機械的）ではなく、動作中のセッションと、背負って後で外す別名です。得られる変化に、エージェントも利用者も気付きません。
3. `af_` は「Agent Fleet」を表し、「このセッション」を表すわけではありません。`af_stop_after_turn` と `stop_session_after_turn` の
   どちらを選ぶかは、モデルが説明とスキーマから判断します。接頭辞にできるのは、2 つの名前が一目で衝突しないようにすることくらいで、
   これは可能性であり、ここで測ったことではありません。`stop_self_after_turn` に改名するなら、その区別を名前の別の形で持たせる必要があります。

B は、この 2 つの定義をどのみち出し直す変更（スキーマ変更など）が来たとき、または 3 つ目の `af_` ツールが提案されたときに見直します。

### 4. 新しいツールに規則を強制する

提案した変更では足しませんでした。利用者がまだ受け入れていない規則を守らせるチェックは、決定より先に CI の法になるためです。小さく、A・B と独立なので、
この記録を accepted にする変更で足します（`scripts/mcp_catalogue.py` の `problems()`。`docs` ワークフローが走らせます）。

- 置き場所：`scripts/mcp_catalogue.py` の `problems()`。`scripts/docs-check.py` の `check_mcp` がすでに 2 つのゴールデンに対して走らせています。
  テストは `scripts/test_docs_check.py` の `McpCatalogueTests`。
- 見るもの（重複を除いた名前ごと。`name#n` の `#` より前）：規則 2 は正規表現、規則 1 は「`af_` で始まらない」、規則 3 は
  「最初の語が閉じた動詞リストにある」。リストは現在使われている 31 個（`add`、`answer`、`archive`、`ask`、`attach`、`create`、`delete`、
  `detach`、`flush`、`generate`、`get`、`link`、`list`、`pause`、`peek`、`propose`、`purge`、`rename`、`request`、`respond`、`restore`、
  `resume`、`run`、`search`、`send`、`set`、`stop`、`tail`、`unwatch`、`update`、`watch`）で、新しい動詞は意図して足します。
- 許可リスト：2 節の 12 個。A なら残り、B なら 2 つの `af_` 名が抜けます。
- 規則 4 も同じやり方（共有名の引数名が一致）で、既知の 10 組を許可すれば見られます。こちらは大きい方で、後回しにできます。

チェックは、信用する前に、仕込んだ違反（ゴールデンに `af_new_tool`）で失敗するところを示します。

## 退けた案

- **接頭辞を規則にする**（Agent サーバーの全ツールに `af_`）。74 個の改名になり、クライアントの `mcp__<server>__` がすでに言っていることを繰り返すだけです。
- **B で `stop_after_turn` に改名する。** 子向けの `stop_session_after_turn` と同じに読めます。
- **旧名も別名として `tools/list` に出す。** 1 リリースのあいだカタログとゴールデンに両方の名前が並び、この記録が無くそうとしている不揃いそのものです。

## 利用者だけが答えられる問い

1. 1 節の規則を、`memory_*`・`branch_name`・`skill_restore` の据え置きも含めて受け入れるか。
2. 2 つの `af_` ツールは A か B か。推奨は A。
3. B なら、候補の名前と、別名を残すリリース数。

## Consequences

- この記録を受け入れても挙動は変わりません。規則が引用できるようになり、レビューがそれを指せます。
- A なら 4 節のチェックを 12 個の許可リストで足せて、今のツールのために変える必要はありません。B なら 10 個で足し、改名は別の変更にします。
