# 21. メモキュー（溜めて一括でセッションへ送る）

ファイルや指示メモを**逐次ではなく溜めてから一括で**コーディングセッションに渡すための、
アカウント単位・別端末同期のメモキュー。docs/19 の「Files 右クリック『セッションに送る…』」
（[19-assistant-chat.md](19-assistant-chat.md) の SendSelectionModal）の発展。

> ステータス: 🗄 **実装済（歴史的記録）**。2026-07-05 に設計確定・未実装として書き起こし、その後全フェーズ実装済み
>（CP `memo.go`＋migration は本文の 0014 案でなく実際は `0017_memo.sql`。MCP 連携は下記追記参照）。

## 背景と狙い

- 現状の `SendSelectionModal` は 1 ファイル/1 引用を**その場で**セッションへ送る。細切れの指示を
  何度もセッションに割り込ませることになる。
- 「あれもこれも」を**溜めておき、まとめて 1 メッセージで**渡せると、割り込みが 1 回で済み、
  受け手のセッションもコンテキストを一度に把握できる。
- メモは**別端末からも確認・追加**したい（スマホで走り書き → デスクトップで整理して送る、等）。
  → ブラウザローカル（localStorage 単独）では不可。**アカウント（membership）単位でサーバ永続**が要件。

## 永続層の選択

`control-plane`（Go + SQLite、`membership_id` scoped）に置く。理由:

- **別端末同期**: 両端末が同じ SQLite 行を参照するので自動的に一致する。
- **コンテナ停止中でも閲覧・追加できる**: CP は `membershipFor` でコンテナを起動せず解決できる。
- 雛形が既にある: **SSM プロファイル**（per-membership リスト）がほぼ 1:1。
  - `control-plane/ssm.go`（`handleSSMProfilesList/Create/Update/Delete`、`membershipFor(w, r)`）
  - `control-plane/store_sqlite.go`（CRUD、所有権ガード `WHERE id=? AND membership_id=?`）
  - `control-plane/migrations/0011_ssm_profile.sql`（`membership_id` + INDEX）

> 補足: この基盤に**データのサーバプッシュは無い**（WebSocket はターミナル専用）。
> よって同期は「開いたときに取り直す＋開いている間だけ軽くポーリング」の粒度になる（後述）。

## 決定事項（サマリ）

| 論点 | 決定 |
|------|------|
| メモの中身 | ファイル参照（`~/repos/...` パス）＋自由テキスト |
| キューの単位 | **レポ毎**。レポ内はさらに **category（サブプロジェクト）** でカテゴライズ |
| レポ非紐付け | 許可（`repo=''` を「共通/未分類」バケツに） |
| カテゴリ入力 | 自由入力（＋そのレポの既出候補をサジェスト、パス由来の初期提案は任意） |
| 一括送信の形式 | 選択メモを **1 メッセージに連結**して 1 回だけ送る |
| 送信の粒度 | レポ全体 / カテゴリ単位 / 個別チェックの 3 粒度（**メモ ID リスト**で統一表現） |
| 送信後の扱い | 削除せず `sent_at` を打ち、**一定期間残して自動削除**（retention、既定 7 日案） |
| 整理 | アシスタントに **整形＋自動カテゴライズ** させる。プレビュー → 承認で反映 |

## データモデル

```sql
-- migrations/0014_memo.sql
CREATE TABLE memo (
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  repo          TEXT NOT NULL DEFAULT '',  -- 'repos/<repo>' 由来。'' = 共通バケツ
  category      TEXT NOT NULL DEFAULT '',  -- サブプロジェクト。自由ラベル。'' = 未分類
  kind          TEXT NOT NULL,             -- 'file' | 'text'
  body          TEXT NOT NULL,             -- 自由テキスト / kind=file のときはコメント
  ref_path      TEXT,                      -- kind=file: '~/repos/<repo>/...'
  position      INTEGER NOT NULL,          -- グループ内の並び
  created_at    INTEGER NOT NULL,
  sent_at       INTEGER                    -- NULL=未送信、値=送信済み（retention 対象）
);
CREATE INDEX idx_memo_membership_repo ON memo (membership_id, repo);
```

UI は `repo → category` の 2 段でグルーピングして表示する。`repo=''` は「共通/未分類」グループ。

## API（control-plane、`/api` 配下）

すべて `membershipFor(w, r)` でスコープ解決（コンテナ起動不要）。ミューテーションは所有権ガード必須。

| ルート | 役割 |
|--------|------|
| `GET /api/memos` | 一覧（未送信 ＋ retention 内の送信済み） |
| `POST /api/memos` | 追加 `{repo, category, kind, body, refPath}` |
| `PATCH /api/memos/{id}` | 編集（body / category / repo / position） |
| `DELETE /api/memos/{id}` | 削除 |
| `POST /api/memos/flush` | `{sessionName, ids}` → 選択メモを 1 メッセージに連結し、既存の `POST /api/sessions/{name}/input` へ 1 回送信 → 対象 `ids` に `sent_at` を打つ |
| `POST /api/chat/ask`（**新規に露出**） | 整理で使用。下記参照 |

`ids` リスト方式により「レポ全体 / カテゴリ / 個別チェック」の 3 粒度が同一経路に乗る
（フロントが選択に応じて `ids` を組み立てるだけ）。

### 連結メッセージの形（flush）

カテゴリを見出しにして連結する。例:

```
以下のメモをまとめて処理して。

## frontend
1. 対象ファイル: ~/repos/repo-a/src/Button.tsx
   ボタンの余白を詰めて
2. …

## api
1. …
```

送信先セッションは既存の rank ロジック（同レポ・入力待ち優先。`SendSelectionModal` 参照）を流用。
送信後トーストはペインバッジ付きの既存トーストを再利用。

## 整理機能（アシスタント）

雑な走り書きを、**整形（明確化・重複排除・順序化）＋自動カテゴライズ**する。

- バックエンドは既存の `POST /chat/ask`（`workspace/agent/chat.go` `handleChatAsk`）を使う。
  - ステートレス・ヘッドレス・ツール強制 off（書き込み/シェル/サブエージェント禁止）・既定 Sonnet。
  - **現状はローカル Agent 専用で CP 非露出**。CP にプロキシルート 1 本 ＋ `console/src/api.ts` に
    ラッパー（`askAssistant` 等）を足して露出する。コアロジックの新規実装は不要。
- 選択メモ集合（flush と同じ選択モデル）を渡し、**JSON で返す**ようプロンプト指定:
  `[{ id, cleaned, suggestedCategory }]`。構造化出力の前例は無いので、プロンプトで JSON を指示し
  フロントで自前パースする。
- 結果は **プレビュー（元／整形後＋提案カテゴリ）→ 承認で `PATCH`** 反映。自動書き換えはしない
  （誤整形の巻き戻しコストを避ける）。

## 同期

サーバプッシュが無いため:

- キューパネルを**開いたときに `GET /api/memos` で取り直す**。
- **開いている間だけ軽くポーリング**（`console/src/settings/usePolling.ts` 流用）。別端末での追加は
  次のポーリングで反映される。背景 GET はワークスペース活性として扱われない（`proxy.go` の
  `conns.touch` 注記）ので、ポーリングでコンテナを温め続けることはない。
- 即時ロード用に localStorage キャッシュ ＋ 起動時 server-wins hydrate（`console/src/lib/settings.ts`
  方式）を被せるのは任意。

## 保持（retention）

送信は削除ではなく `sent_at` を打ち、「送信済み」として一定期間残す（履歴・再送に使える）。
掃除は `GET /api/memos` の時に「`sent_at` が N 日以上前」を除外＋遅延削除、または起動時/定期スイープ。
既定 7 日案（ws-settings で可変にする余地あり）。

## UI（console）

- **キューパネル**（ドロワー/セクション、件数バッジ）。`repo → category` の 2 段グルーピング。
- **追加**: `SendSelectionModal` に「キューに追加」を追加（repo はパスから自動、category は自由入力
  ＋既出サジェスト）。自由テキストの「＋新規メモ」導線も。
- **選択と送信**: チェックボックス。カテゴリ/レポ見出しの「まとめて送信」＋「選択を送信」。
- **整理**: 選択 →「アシスタントで整理」→ プレビュー → 承認で反映。
- 送信済みメモは薄表示で retention 期限まで残す。

## 実装順（フェーズ）

1. **CP 永続＋ CRUD**: `migrations/0014_memo.sql`、`store.go`/`store_sqlite.go` に List/Create/Update/Delete、
   `control-plane/memo.go` ハンドラ、`main.go` にルート登録。SSM を雛形に。
2. **flush**: `POST /api/memos/flush`（連結 → session input → `sent_at`）。retention 掃除。
3. **整理の露出**: `/chat/ask` を CP プロキシ＋`api.ts` ラッパー。
4. **フロント**: キューパネル、`SendSelectionModal` 改修（キューに追加）、整理プレビュー、ポーリング/hydrate。

## 制約・未決

- 保持日数の既定値、パス由来カテゴリ提案の踏み込み度、整理 JSON スキーマの詳細は実装フェーズで確定。
- メモは membership 単位で CP に載るが、参照先ファイルはコンテナ内（`~/repos/...`）。コンテナ再作成で
  レポ構成が変われば `ref_path` が解決不能になり得る（パス参照方式は docs/19 と同じ前提）。
- リアルタイム同期は基盤上不可（プッシュ無し）。別端末反映はポーリング粒度にとどまる。

## 追記（2026-07-10）: MCP からのメモ読み書き（オペレーター＋PAT）

フリート・オペレーター（在コンソールのローカル stdio MCP）と CP 側 MCP（PAT）の両面から、
メモキューを **list / add / update / delete / flush** できるようにした。

- **共有コア化**: `memo.go` の CRUD/flush 本体を `memoListFor / memoCreateFor / memoUpdateFor /
  memoFlushFor`（＋`DeleteMemo` 直呼び）に抽出。既存のセッション認証ハンドラ・内部トークン面・
  CP MCP ツールの 3 面がすべてこの membership スコープのコアを通る（flush の「1回送信＋sent_at」原子性を単一化）。
- **CP MCP（PAT）**: `memberTools` に `list_memos`(read) / `add_memo` / `update_memo` / `delete_memo` /
  `flush_memos`(write) を追加。メモは CP store に住むので Agent 往復せず**直接** store を叩く（membership は
  PAT 由来の `res.mv`）。flush だけは `res.rt` でワークスペースへ連結メッセージを届ける。
- **オペレーター（コンテナ内）向けブリッジ**: コンテナは各自専用ネットワークで CP と直結できず、内部 git と
  同じ**公開ヘアピン**でしか CP に届かない。そこで内部 git トークンに倣い、**membership 毎のメモトークン**
  `AF_MEMO_TOKEN`（HMAC・`afm_` 接頭辞、`memo_bridge.go`）と公開ベース URL `AF_CP_BASE_URL` を
  `workspaceExtraEnv` で注入（`PUBLIC_BASE_URL` 設定時のみ）。CP は `/internal/memos*`（session-exempt）で
  Bearer メモトークン→membership を**サーバ側で解決**（クライアント供給の id は信用しない）。ローカル stdio MCP は
  `cpMemoDo` で `AF_CP_BASE_URL` + `AF_MEMO_TOKEN` を使い CP を叩く。`list_memos` は read（読み取り専用の
  Agent Fleet アシスタントも保持）、変更系は `--write`（オペレーター）限定。
- **セキュリティ**: メモトークンは git トークンと**別鍵・別クレデンシャル**なので、漏えい時の影響はメモ面に限定。
  トークンは membership を内包し CP がトークン→membership を引くため、クロス membership アクセスは起きない。
- **store**: `IdentityIDForMembership`（membership→identity）を追加（internal flush が
  `resolveByMembership` でランタイムを解決するため）。sqlStore 実装で sqlite/postgres 両対応。
- **ペルソナ**: `operatorPersona` にメモ運用（list/add/整理→flush）と、一括送信も作成前に利用者へ確認する
  ガードを追記。案内役の Agent Fleet アシスタントは `list_memos` で溜まり具合を答えるのみ（追加・送信はオペレーターへ誘導）。
