# 80. 外部の作業項目（GitHub Issue / Jira チケット）を左ペインに出し、そこから始める

- 状態: ✅ **P0〜P2 実装済み**（2026-08-26）。⏸ 残り = 実機目視・作業グループ自動作成の判断（§80.16-5）・P3 以降。
  採否と判断は [decisions/0061](decisions/0061-work-item-inbox.md)。
- ゴール: 人の仕事の起点である**チケット**を左ペインに置き、そこから 1 クリックで
  文脈込みのセッションを立てられるようにする。Workspace が停止していても一覧は見え、
  「始める」を押したときに初めて起きる。
- 非ゴール（v1）: チケットの閲覧・編集 UI、全件同期、自動での状態遷移やコメント投稿、
  無人での自動着手。

関連: [21](history/21-memo-queue.md)（左ペインの CP 永続セクションの雛形）/
[25](25-ops-monitoring.md) §4.3・§4.6・§5（接続 kind とイベント駆動、
プロンプトインジェクションの先行検討）/ [38](38-scheduled-execution.md)（停止中 WS を
起こして注入する経路）/ [48](48-mcp-registry.md)（MCP レジストリ。§0 が OAuth MCP を
非目的にしている）/ [52](52-working-sets.md)（作業グループ＝“案件”）/
[51](51-session-report-v2-ledger.md)・[68](68-session-changed-files.md)（書き戻しの材料）/
[75](75-idle-stop-and-pending-interactions.md)（止まらない Workspace）/
[20](20-container-audit-egress.md)（egress allowlist）

---

## 80.1 これは何ではないか

**チケット一覧ビューアではない。** 絞り込み・ソート・詳細画面を作り込む道は沼で、しかも
本家の Web UI に永遠に勝てない。af が持つべきなのは「保存済みクエリの結果を N 行出す」までで、
価値は一覧そのものではなく次の 3 つにある。

| 価値 | 中身 | 無いとどうなるか |
|---|---|---|
| **文脈の自動注入** | キー・タイトル・URL を最初の指示へ、`feature/{key}` をブランチ名へ、1 チケット 1 worktree | 結局コピペになり、ブラウザのタブと変わらない |
| **対応状況の可視化** | 「この課題は誰の・どのセッションが触っていて、いま質問待ち」が項目行に出る | Jira と af の二重管理。同じ課題に 2 人が着手する |
| **戻り先** | 変更ファイル・報告をコメント下書き / PR へ | 「やった結果がチケットに戻らない」で運用が死ぬ |

いまの起動導線は **repo 起点**（`RepoRow` → `LaunchModal` → `useStartWork`）で、
「どのリポジトリで作業するか」から始まる。本機能はその起点を**チケット**に付け替える。

## 80.2 既にある部品と、空いている穴

| 要るもの | 状態 | 実体 |
|---|---|---|
| 左ペインの CP 永続セクション（membership 単位・端末間同期・**WS 停止中も見える**） | ✅ 雛形そのもの | `control-plane/memo.go` + `console/src/features/memo/MemoQueueSection.tsx`（`App.tsx:500`） |
| 押した瞬間にセッションを立てる | ✅ | `console/src/features/repos/useStartWork.ts`（`POST /api/sessions` ＋ `initial_prompt` は **Agent が配達**＝Console を見ていなくても走る）／`LaunchModal.tsx` |
| 停止中 WS を起こして注入する | ✅ | CP スケジューラ → `ensureWorkspaceStarted` → Agent REST（[38](38-scheduled-execution.md)） |
| 案件別に左ペインを分ける | ✅ | 作業グループ（[52](52-working-sets.md)）。**“案件”の正体がチケット**で、本機能と合流して初めて自動所属が自然になる |
| GitHub API をユーザーのトークンで叩く | ✅ 前例あり | `workspace/agent/git_remote.go`（GraphQL でブランチ一覧・`/user`・repos 一覧）。トークンは `internal/secrets`（コンテナ内 AES-256-GCM） |
| 外部サービスのトークン保管と接続カード | ✅ | `workspace/agent/connections.go`（github / bitbucket / pagerduty / grafana / slack / …）。**Jira だけ無い** |
| Console への push | ✅ | `control-plane/events.go` の SSE。**「この接続は idle クロックに触れない」**と明記されており、stream を足しても WS を温めない |
| af 自身の MCP クライアント | ✅ ただし接続テスト用 | `workspace/agent/internal/mcpreg/probe.go`（`initialize` → `tools/list`・2025-\* / 2026-07-28 両 era・stdio / http）。`tools/call` は無い |
| 長い処理を「ジョブ」として観測する型 | ✅ | `workspace/agent/repo_jobs.go`（[78](78-repo-import-jobs.md)） |
| 報告・変更ファイル（書き戻しの材料） | ✅ | 指示台帳（[51](51-session-report-v2-ledger.md)）・変更ファイル一覧（[68](68-session-changed-files.md)） |

→ **本当に新規なのは 3 つだけ**: ①プロバイダ別の取得アダプタ ②CP 側の非機密メタキャッシュ
③項目 ↔ セッションの紐付け台帳。見積もりが崩れるのはここではなく §80.3 の判断である。

## 80.3 ★ 取得はどこで走るか

### 80.3.1 停止中に見えなければ、この機能は無い

「チケットを見て、どれをやるか決める」のは**セッションを立てる前**の行為である。
つまりこの一覧が最も必要な瞬間、Workspace は止まっていることが多い。ところが秘密
（GitHub / Jira のトークン）は**コンテナ内の `secrets.enc` にしかない**。素直に Agent で
取得すると、停止中の左ペインは空になる。

かといって**表示のために WS を起こしてはいけない**。課金に効くのは tier2（Workspace 停止）
だけで、一覧のポーリングで温め続けるのは [75](75-idle-stop-and-pending-interactions.md) が
塞いだ穴を別の場所で開け直すことに等しい。

### 80.3.2 決定：取得は Agent、キャッシュは CP

```
[WS 稼働中]  CP がクエリを渡す ──▶ Agent がトークンで解決 ──▶ 非機密メタだけ返す（5 分間隔）
                                                        │
                                                        ▼
[CP]  work_item_cache（membership scoped）── SSE /api/events の workitems stream ──▶ [Console 左ペイン]
                                                        ▲
[WS 停止中]  取得は走らない。行は残り、「最終取得 14:20」を必ず添える
                                                        │
             「始める」を押したときだけ ensureWorkspaceStarted で起きる
```

（実装では取得の**向き**を CP → Agent にした。理由は §80.6。取得が Agent 内で走ることと、
トークンがコンテナから出ないことは変わらない。）

これで **「止まっている Workspace を、チケットから起こす」**という、この機能で一番価値のある
導線が成立する。追加で必要なのは「最終取得時刻の明示」だけ — 古いかもしれない一覧を、
古いと言わずに出すことだけはしない。

### 80.3.3 CP に置いてよいもの / 置かないもの

| 置く（非機密メタ） | 置かない |
|---|---|
| `key`（`PROJ-123` / `owner/repo#45`）・`title`・`state`・`url`・`assignee`・`labels`・`updated_at`・`repo_hint` | **本文（description）・コメント・添付**、そして**トークン** |

**本文を CP に持たせない**のが線引きの芯である。課題本文は顧客・取引先の情報が入る最も機微な
部分で、ここを CP に置いた瞬間に「CP は秘密を素通しさせるだけで保持しない」という説明が崩れる
（[71](71-tenant-git-oauth.md) §71.8 が refresh token に対して守ったのと同じ線）。本文が要る場面は
**セッションの中**だけで、そのときは WS が起きているので Agent が取りに行ける。

タイトルも厳密には業務情報だが、これを置かないと一覧が成立しない。**タイトルまでを CP の
保持範囲とする**ことを ADR で明示する（メモキューは既に本文そのものを CP に置いているので、
本機能はむしろ保守的な側にある）。

### 80.3.4 ポーリングを活動に数えない

Agent の取得ジョブは**「止めてよいか」の判定材料にしない**（[78](78-repo-import-jobs.md) §7 が
取り込みジョブを busy に足したのとは逆向きの判断）。理由は明快で、5 分ごとに自分で走る処理を
busy に数えたら Workspace は永久に止まらない。⚠️ 実装上は「取得中フラグを `GET /sessions` に
出さない」ことがそのまま要件になる。

## 80.4 プロバイダとの接し方 — `gh` と MCP をどこで使うか

**層を 2 つに切る。**

```
┌ 一覧（左ペイン・定期・WS 停止中は CP キャッシュ）
│   GitHub … workspace-agent から REST/GraphQL を直叩き（git_remote.go の隣・同一トークン）
│   Jira   … workspace-agent から /rest/api/3/search/jql（JQL）＋ API トークン。★2 経路
│   → 決定的・LLM ゼロ・CLI の出力契約に依存しない
└ セッションの中（本文・コメント・添付・書き戻し）
    GitHub … gh（既に透過認証済み）
    Jira   … Jira MCP（[48](48-mcp-registry.md) のレジストリで user / tenant に配布）
```

### 80.4.1 一覧に `gh` / MCP を使わない理由

1. **backend は既にトークンの持ち主である。** `gh` にトークンを渡している credential helper の
   実装者が **workspace-agent 自身**（`workspace-agent cred`）。自分が持っている値を、自分が
   被せたラッパー越しに取り直すのは一周回っている。直叩きの前例は `git_remote.go` にあり、
   追加は数十行で済む。
2. **CLI の出力契約は版で壊れる。** `gh --json` のフィールドも `gh` 本体の版もイメージ由来で動く。
   この repo は同型の事故を繰り返している（TUI 文字列契約・シムの出力）。**5 分おきに自走する
   処理を CLI 出力のパースに乗せるのが一番まずい置き方**である。
3. **MCP は一覧に構造的に向かない。** (a) `tools/call` の戻りは自由形式でサーバー毎の写像が要る
   (b) Atlassian 公式のリモート MCP は OAuth で、⚠️ [48](48-mcp-registry.md) §0 が
   「OAuth を要する MCP は af 非関与」と明示的に非目的にしている＝**無人・停止中の更新に使えない**
   (c) そもそも MCP は「各 CLI が直接喋り、af は定義を配るだけ」という設計思想で、af backend が
   常時クライアントになるのは逆走。

### 80.4.2 逆に、セッションの中は `gh` / MCP が正しい

`workspace/gh-auth-wrapper.sh` が `/usr/local/bin/gh` を薄く包み、呼び出し毎に
`git credential fill`（＝Connections の暗号化ストア）からトークンを取り出して `GH_TOKEN` に
注入する。**`gh auth login` は要らず、GitHub 接続と同一の資格**で動く。エージェントは
`gh issue view` / `gh pr create` を自然に使えるので、**af が本文取得や書き戻しの API を
実装する必要がない**。Jira 側は同じ役割を MCP が果たす。

⚠️ ただし実測の制約が 3 つある（`docs/dev/08` §8.3）:

- **scope は `repo` のみ（`read:org` 無し）** — 自分にアサインされた Issue/PR は取れるが、
  org 横断の Projects ボードは取れない。
- **GHE 非対応** — ラッパーは `host=github.com` 固定。直叩き側も `api.github.com` 固定なので、
  GitHub Enterprise Server は v1 の対象外（対応するなら host を接続に持たせる別作業）。
- **イメージ再ビルドで初めて有効** — 未ビルドのコンテナでは gh は未認証のまま。

### 80.4.3 Jira でだけ効く 4 つの判断（P1 実装）

1. **メールも資格情報である。** Jira REST v3 は `"<アカウントのメール>:<API トークン>"` の
   HTTP Basic なので、site / email / token の 3 つをまとめてコンテナの暗号化ストアに置く。
   接続カードの入力欄が 3 つあるのはそのため。
2. **保存する前に検証する。** `GET /rest/api/3/myself` が通ったときだけ保存し、表示名を控える。
   3 項目は打ち間違いの機会が 3 回あるということで、検証が無いと**最初の異常はレール行の
   エラー**として数分後に出る＝「機能が壊れている」と読めてしまう。
3. **★ 状態は「ステータス名」ではなく `statusCategory` で判定する。** Jira はプロジェクトごとに
   ステータスを自由に改名できる（レビュー中 / 検証待ち / …）が、その裏のカテゴリは
   `new` / `indeterminate` / `done` の 3 値に固定されている。名前で判定すると**最初の
   カスタムワークフローで壊れる**。
4. **⚠️ 検索は 2 経路を順に試す。** Atlassian は `/rest/api/3/search` を
   `/rest/api/3/search/jql`（token ページング）へ置き換え中で、どちらを答えるかはサイトが
   いつ移行したかによる。新しい方を先に呼び、**404 / 410 のときだけ**旧経路へ落ちる
   （401 では落ちない —— 認証エラーで別経路を叩いても意味が無く、原因を隠すだけ）。
   課題の JSON 形は同じなので写像は 1 つで足りる。

**Jira の課題はリポジトリを持たない。** だから起動先は**クエリの「既定の作業コピー」だけが
手がかり**になる（プロジェクト → 作業コピーの対応表がこれ）。GitHub 行は `owner/name` から
推測できるが、Jira 行で `repoHint` が空だと必ず はじめる ハブでの選択になる。

## 80.5 データモデル

CP 側（`migrations/0051_work_item.sql` ＋ ⚠️ `migrations-pg/0035_work_item.sql` の**両方**。
方言 2 系列の片方忘れは既知の事故経路）。

```sql
CREATE TABLE IF NOT EXISTS work_item_query(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  provider      TEXT NOT NULL,
  label         TEXT NOT NULL DEFAULT '',
  query         TEXT NOT NULL,
  repo_hint     TEXT NOT NULL DEFAULT '',
  enabled       INTEGER NOT NULL DEFAULT 1,
  position      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  fetched_at    TEXT NOT NULL DEFAULT '',
  last_error    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_work_item_query_membership ON work_item_query(membership_id);
CREATE TABLE IF NOT EXISTS work_item_cache(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  query_id      TEXT NOT NULL,
  provider      TEXT NOT NULL,
  item_kind     TEXT NOT NULL DEFAULT 'issue',
  item_key      TEXT NOT NULL,
  title         TEXT NOT NULL DEFAULT '',
  state         TEXT NOT NULL DEFAULT 'open',
  url           TEXT NOT NULL DEFAULT '',
  assignee      TEXT NOT NULL DEFAULT '',
  labels        TEXT NOT NULL DEFAULT '',
  repo          TEXT NOT NULL DEFAULT '',
  updated_at    TEXT NOT NULL DEFAULT '',
  fetched_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_work_item_cache_membership ON work_item_cache(membership_id);
CREATE TABLE IF NOT EXISTS work_item_session(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  provider      TEXT NOT NULL,
  item_key      TEXT NOT NULL,
  session_name  TEXT NOT NULL,
  repo          TEXT NOT NULL DEFAULT '',
  branch        TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_work_item_session_membership ON work_item_session(membership_id)
```

⚠️ `work_item_session` は `work_item_cache` に **FK を張らない**。キャッシュはクエリの結果で
出入りする揮発物で、着手の事実はそれより長生きする（クエリを「未完了だけ」にした瞬間、
完了した項目の履歴が消えるのは間違い）。

正規化した 1 行（`key / title / state / url / assignee / labels / updated_at / repo_hint`）は
プロバイダ非依存にしておく。GitHub Issue・PR・Jira・Backlog・Redmine・GitLab が同型に載る。

## 80.6 API — ★ 向きは「CP が渡して、CP が引き取る」

起票時は「Agent がクエリを持ち、CP へ push する」と書いていたが、実装で**逆向き**にした。
CP がクエリを渡し、Agent がトークンで解決して返す。

```
[CP]  work_item_query（自分が持っている） ──POST /work-items/fetch {queries}──▶ [Agent]
      work_item_cache  ◀────────────── 非機密の行 ─────────────────────────────
```

**この向きだと新しい資格情報が 1 つも要らない。** Agent → CP の方向を作ると、memo /
schedule と同じく**専用トークンを 1 種類増やし、4 つのランタイム全部の env 注入に手を入れる**
ことになる（`AF_MEMO_TOKEN` / `AF_SCHEDULE_TOKEN` の前例）。CP → Agent なら既存の
`rt.Endpoint()` + `rt.Token()` でそのまま呼べる（`drainAgentOutbox` と同じ形）。
副産物として **Agent はこの機能のために何も永続しない**（クエリも状態も持たない）。

| 面 | パス | 役割 |
|---|---|---|
| Agent | `POST /work-items/fetch` | CP から渡されたクエリを provider で解決し、非機密の行を返す。**呼ぶのは CP だけ** |
| Agent | `POST /connections/jira` ほか | Jira 接続カード（P1） |
| CP | `GET /api/work-items` | 一覧（**停止中でも 200**。稼働中なら期限切れのクエリを非同期で更新） |
| CP | `POST /api/work-items/refresh` | 更新ボタン。間隔を無視し、**同期**で取ってから返す |
| CP | `GET/POST/PATCH/DELETE /api/work-item-queries[/{id}]` | 保存クエリ CRUD（CP 完結＝停止中でも編集できる） |
| CP | `POST/DELETE /api/work-item-sessions[/{id}]` | 着手台帳 |
| CP | `/api/events` の `workitems` stream | 変化したときだけフレームを送る |

⚠️ **Console は Agent の `/work-items/fetch` を叩かない**ので、**CP のエージェント・プロキシ
許可リストに足すものが無い**（新しい agent REST を足すと `control-plane/routes.go` にも登録が
要る、という毎回踏む穴を、そもそも通らない設計になっている）。

⚠️ **一覧経路の取得は goroutine（membership ごとに 1 本）**。SSE の tick は 4 秒で、そこで
provider の往復を待つと**その購読者の他の stream（workspace / sessions / 通知）ごと止まる**。
更新ボタンだけは同期でよい（tick の中ではないし、押した人は結果を待っている）。

## 80.7 左ペイン（IA）

既存は アシスタント / メモ / スケジュール / プロジェクト / その他 / 共有 / ファイル の 7 枠
（`console/src/app/App.tsx:499-505`）。**8 枠目として独立セクション**を メモ の上に置く。

- **メモキューには統合しない。** 「溜めて渡す」語彙は近いが、メモは自分で書く編集可能な物、
  項目は外部が持つ読み取り専用の物で、混ぜると両方の意味が濁る。
- **プロジェクト（repo）ノードの子にもしない。** GitHub Issue には自然でも、Jira は
  リポジトリに紐づかないので破綻する。
- 既定は「**自分にアサイン済み・未完了**」だけ。行数を抑えるのは UI の都合ではなく、
  全件同期をしないという設計そのもの（§80.12）。
- **作業グループで絞る**（[52](52-working-sets.md)）。項目の所属は起動時に作られる
  グループへ従属させる。
- ⚠️ **停止中レール（`App.tsx:509-515`）にも出す。** CP キャッシュだからこれができる。
  §80.3.1 で述べたとおり、ここが本機能の主戦場である。
- 行は 1 行（左ペインはフラット化の合意がある）: `[状態色] KEY  タイトル …  [セッション状態チップ]`。
  セクションヘッダに「最終取得 hh:mm」と手動更新ボタン。

## 80.8 項目から始める（本体）

**新しいモーダルは作らない。** 既存の `LaunchModal` を項目で前埋めして開く。

| 前埋めするもの | 決め方 |
|---|---|
| 作業コピー | `repo_hint`（クエリ既定）→ GitHub は項目のリポジトリ → 無ければ選ばせる |
| ブランチ | 既定 `feature/{key}`（テンプレは設定で変更可・`{slug}` も使える）。⚠️ 既存ブランチとの衝突は `useStartWork` の `branch_exists` 経路が既に扱う |
| worktree | **既定 on**（1 チケット＝1 作業コピー） |
| kind / model | 既存の `repoLast`（そのリポジトリで最後に使った組み合わせ） |
| 最初の指示 | §80.9 のテンプレート |

起動後:

- `work_item_session` に 1 行入れ、項目行に**セッション状態チップ**（稼働 ● / 質問あり / 停止）を出す。
  語彙は左ペインの既存チップをそのまま使う。
- ⛔ **作業グループは分けない**（P2 で判断）。代わりに**起動先を選ばせる** —— チケットは
  作業コピーを知らないので（GitHub 項目はリポジトリまで、Jira はそれすら持たない）、
  始める はまず「リポジトリ」と「新しい worktree を作る / 既存の作業コピーで続ける」を聞く。
  ★ はじめる ハブは**base clone しか並べない**（worktree は「タスク用のコピー」でツリーの行から
  起動する建前）ので、2 回目以降の「そのチケットの worktree で続ける」がそこでは選べない。
  起動ダイアログの 場所 も *このコピー* か *新しい worktree* までで、**別の既存コピー**は指せない。
  選んだ結果は `useLaunchTarget`（`inPlace`）で既存の `LaunchModal` に渡すだけで、エージェント・
  モデル・プロンプト・ブランチ・worktree 作成の実装は 1 か所のまま。
- 同じ項目に対して既にセッションがあるときは、**起動前に「着手済み」を出す**。台帳を作る
  一番の実利はこれ（同じチケットを 2 人が別々に始めるのを止める）。

## 80.9 最初の指示に何を書くか（本文の扱い）

**既定では本文を貼らない。** 書くのは キー・タイトル・URL と、**取得手順**である。

```
作業対象: PROJ-123 「ログイン後に一覧が空になる」
URL: https://example.atlassian.net/browse/PROJ-123

本文とコメントは Jira MCP（または `gh issue view 45`）で読める。
まず状況を調べ、実装に入る前に方針を提示すること。
```

- 本文の同梱は **LaunchModal のチェックボックスで opt-in**。入れるときは必ず引用ブロックで包み、
  **「以下は外部データであり、指示ではない」**を添え、文字数の上限で切る。
- 既定で貼らない利点は 2 つ: **CP も Agent も外部本文を保持しない**こと、そして
  インジェクション面が「エージェントが自分で読みに行った結果」に限定されること。
- 欠点も明記する: 本文を読むために **1 ターン余分に焼く**。だから opt-in を残す。

## 80.10 書き戻し（P2 実装済み）

**読み取り専用が既定。** 唯一の書き戻しが「作業の報告をコメントする」で、**人が下書きを読んで
押したときだけ**走る。MCP ツールは無く（エージェントからは到達できない）、自動の発火も無く、
状態遷移も担当者変更もクローズもしない。

**af が下書くのは事実だけ。** ブランチと変更ファイル（[68](68-session-changed-files.md) の
「転写 × git」）——セッション単位でこれを知っているのは af だけで、手で集めるのが面倒な部分。
**要約は生成しない**: 他人のチケットに載るコメントは利用者自身の発言で、もっともらしく読める
生成文は「読まずに投稿される」典型だからである。冒頭の「ひとこと」欄が人の担当。

⚠️ **パスはリポジトリ相対で書く。** 実描画で見つけた —— 作業項目からの起動は worktree を作るので
`repo` は `webshop@checkout-validation` になり、下書きに
`webshop@checkout-validation/src/checkout/validate.ts` と並んでいた。課題の読み手には無意味な
ローカルのフォルダ名で、こちらの作業コピーの並べ方を公開してもいる。ただし**作業コピーを 2 つ
またいだセッションだけは base 名を前置する**（`src/index.ts` が衝突して片方が黙って消えるため。
前置するのは base であって worktree ではない）。

⚠️ **Jira の投稿は ADF**（Atlassian Document Format）。REST v3 はコメント本文にプレーン文字列を
受け付けず 400 になる —— この経路が壊れる一番ありがちな形。空行で段落、段落内の改行は
`hardBreak` に写す。

**PR 起票はここでは作らない**（当初 P2 に挙げていたが取り下げ）。理由は
[ADR 0061](decisions/0061-work-item-inbox.md) 決定 4 そのもので、**セッションの中の `gh` が既に
できる**（透過認証済み）。af 側に 2 つ目の実装を置くと、push 済みかの判定・既定ブランチの解決・
既存 PR の検出を af が抱えることになり、`gh pr create` の劣化コピーが増える。

## 80.11 セキュリティ

1. **プロンプトインジェクション**が構造的に発生する領域である。§80.9 の 3 点（既定で貼らない /
   引用と宣言 / 上限）と §80.10（write を既定にしない）で閉じる。⚠️ **無人自動着手を既定にしない** —
   webhook 起動を将来足すときも opt-in ＋ read-only。
2. **トークンは最小権限**。GitHub は既存接続（scope=`repo`）、Jira は read-only の API トークン。
   接続カードのヘルプに最小権限の作り方を書く。
3. **egress**: Jira のホスト（テナント固有）と `api.github.com` が allowlist に要る
   （[20](20-container-audit-egress.md)）。テナント固有ホストが入るのは本機能が初めてに近いので、
   接続保存時に allowlist への追加を促す。
4. **CP の保持範囲**を ADR で明示（§80.3.3）。「本文は置かない」は実装の都合ではなく約束。
5. **項目の URL をそのままリンクにする**ので、`https?:` 以外のスキームは弾く。

## 80.12 やらないこと

- 一覧の作り込み（絞り込み UI・ソート・詳細画面）。**クエリを保存する**以上のことはしない。
- 全件同期。数千件のキャッシュは CP を太らせるだけで、誰も読まない。
- 一覧の生成に LLM を挟む（非決定的・遅い・課金される）。
- 表示のために停止中の Workspace を起こす。
- 自動でチケットを閉じる / コメントする / 状態遷移させる。
- GHE 対応（§80.4.2）。要望が出たら host を接続に持たせる別作業。

## 80.13 段階

| Phase | 中身 | 触る場所 |
|---|---|---|
| **P0** ✅ | モデル・**GitHub アダプタ**（既存トークン流用＝追加認証ゼロ）・CP キャッシュ・SSE stream・左ペイン独立セクション・`LaunchModal` 前埋め・`work_item_session` 台帳 | `workspace/agent/workitems.go`（新設）/ `control-plane/workitems.go`（新設）+ `{store,store_sqlite,routes,events}.go` ＋ migrations 0051 / pg 0035 / `console/src/features/workitems/`（新設）/ `App.tsx`・`core/push/*`・`StartHost`・`LaunchModal` |
| **P1** | **Jira 接続 kind**（email + API トークン）・JQL 保存クエリ・repo マッピング | `connections.go`・`ConnectionsTab`・アダプタ追加 |
| **P2** ◐ | **報告コメントの下書き → 人が承認して投稿**（GitHub / Jira）＋**ブランチ名テンプレート**（`{key}`/`{slug}`・プレビュー付き）。⏸ 作業グループ自動作成は保留（§80.16-5）、**PR 起票は取り下げ**（§80.10） | `workspace/agent/workitems_comment.go`（新設）・`control-plane/workitems.go`・`features/workitems/{report.ts,WorkItemReportModal.tsx}`・`lib/settings.ts` |
| **P3** | webhook 受信・通知・（opt-in の）自動初動 | [25](25-ops-monitoring.md) §4.6 と合流 |
| **P4** | 汎用アダプタ（API トークンで動く stdio MCP を `mcpreg` の `tools/call` で叩く / URL テンプレ＋JSON 写像） | `internal/mcpreg/` |

**P0 だけで機能として成立する**（GitHub ユーザーは追加設定ゼロで使い始められる）ことを
段階の設計目標に置く。

## 80.14 採らなかった案

- **CP が直接取得する（トークンを CP に封筒暗号で保管）。** 停止中も鮮度が保て、チーム共有の
  サービスアカウントにも向く。しかし「CP は秘密を保持しない」原則の破棄で、
  [25](25-ops-monitoring.md) §4.3 が ADR 送りにした未決点そのもの。**本機能のために原則を
  ひっくり返すには釣り合わない**（一覧の鮮度は数分古くて困らない）。
- **Agent 取得のみ・CP には何も置かない。** 原則には最も忠実で実装も最小。だが停止中に
  左ペインが空になり、§80.1 の 3 つの価値のうち 2 つが消える。
- **`gh` / MCP を一覧の取得に使う。** §80.4.1 の 3 点。
- **メモキューに統合する。** §80.7。
- **セッション側（エージェント）に一覧を作らせる。** LLM を挟むと非決定的・遅い・課金される。
  一覧はプログラムの仕事である。
- **`work_item_cache` に本文を入れて全文検索する。** 機微情報の保持範囲が一段広がるのに対し、
  得られるのは「af の中でチケットを検索できる」という、本家がやることの劣化コピー。

## 80.15 テスト計画

- 正規化（provider 別レスポンス → 共通行）の表駆動テスト。GitHub の Issue と PR、Jira の
  ステータスカテゴリを含む。
- キャッシュ差し替えの冪等性（同じ取得を 2 回流して行が増えない）。
- **停止中の `GET /api/work-items` が 200 を返し、`fetched_at` が古いまま出る**こと。
- 取得ジョブが busy に数えられない（`GET /sessions` に現れない）こと。§80.3.4 の回帰。
- `work_item_session` がキャッシュから消えた項目でも残ること。
- Console: 行のレンダリング（dom テスト）、`LaunchModal` の前埋め、着手済みの警告。
- ⚠️ `console/scripts/shots/` のスタブに新ルートを足す（未対応ルートは `{}` を返すだけなので、
  足し忘れると UI 検証で**空のセクション**が出て気付けない）。

## 80.16 未決

1. **クエリの UI をどこに置くか。** 接続カード内 / セクションの歯車 / 設定タブ。P0 では
   セクションの歯車に最小フォーム（ラベル＋クエリ＋既定リポジトリ）で足りると見ている。
2. **チーム共有のクエリ**（テナント管理者が配る「このチームの未対応」）。P1 以降。
   [48](48-mcp-registry.md) のテナントスコープ配布と同型にできる。
3. **PR を項目として出すか。** GitHub では Issue と PR が同じ検索に乗る。レビュー待ちの PR は
   「始める」対象として自然だが、行数が増える。既定 off で始める。
4. **通知**。アサインされた瞬間に通知するかは、通知センターの語彙（流れ物 / バッジ）を
   [77](77-member-handoff.md) §77.9 に合わせて決める。P3。
5. **✅ 作業グループの自動作成（P2 で不採用）。** 利用者の判断で「グループは分けない」に決着。
   代わりに §80.8 の起動先ピッカー（リポジトリ＋新規/既存 worktree）を入れた。以下は
   なぜ自動作成が成立しなかったかの記録: 「1 チケット = 1 作業グループ」は
   [52](52-working-sets.md) の所属モデルと噛み合わない —— 所属は**リポジトリ粒度**で、
   `ProjectTree` は `repoInSet(base)` で群ごと絞る。だからチケット用のグループに
   セッション（や worktree）だけを入れると、**そのセッションはツリーから消える**（親の
   リポジトリ群が除外されるため）。かといって base を入れると、そのリポジトリの他の
   セッションが全部入ってきて、案件別に分ける意味が消える。
   所属モデルを作業コピー粒度へ広げれば通せるが、docs/52 + ADR0036 の決定を書き換える
   作業になる。**グループ一覧がチケットの数だけ増える**という別の問題もあり、
   「案件」は人が決める単位のままにした。
