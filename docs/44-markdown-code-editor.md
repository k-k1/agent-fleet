# docs/44 — Console Markdown / コードエディタ設計と Phase 0 契約

> 正: 本書は Phase 0 で固定した設計契約。実装の詳細は各フェーズのコードとテストが正。/
> ADR: [0027](decisions/0027-markdown-code-editor.md) /
> 対象: Console の既存 `file` ペイン + Workspace Agent の fs API

## 1. 固定する全体設計

### 1.1 ペインとバッファ

File ペインは次の2層を持つ。

| 層 | 値 | 意味 |
|---|---|---|
| File ペイン | `mode: "view" | "edit"` | 読み取り専用表示か、編集バッファか。ペイン種別は `file` のまま。 |
| Markdown 表示 | `edit` / `preview` / `split` | edit 時の本文編集、既存 MarkdownView/MarpView によるプレビュー、左右分割。 |

非Markdownのテキストは CodeMirror 6 の編集表示を使う。Markdown の preview は既存の
MarkdownView、Marp のスライド表示、Mermaid の遅延ロード経路を再利用する。Markdown の
ソース表示は編集表示へ統合し、専用ペインや別の保存経路は作らない。

編集開始時に GET した本文をタブ内のバッファへコピーし、同時にその本文の revision と
保存対象パスを保持する。入力でバッファが変われば dirty とする。dirty 本文と undo 履歴は
メモリだけに置く。File ペインを閉じる際は未保存警告を出すが、再読み込み後の復元はしない。

### 1.2 保存と競合

revision は、正規化前のファイル生バイト列に対する SHA-256 である。

```text
revision = "sha256:" + lowercase(hex(SHA-256(raw file bytes)))
```

改行、末尾改行、Unicode の表現、ファイルエンコーディングを正規化してはならない。JSON の
`content` を UTF-8 バイト列へ戻したものをそのまま保存し、保存成功時の revision はそのバイト列
から再計算する。したがって同じ本文は同じ revision になる。

保存は対象パスごとに revision 検査と置換を一つの排他的な処理として扱う。検査後に別の保存が
成功した場合、後続の保存は必ず `409 revision_conflict` になる。保存成功後にレスポンスを返す。

### 1.3 AI提案の境界

AIはファイルパスを直接書き込むツールを持たず、対象 File ペインに対する構造化提案を返す。
提案を accept するとバッファの指定範囲を replacement で置き換え、dirty になる。reject は
バッファを変更しない。accept/reject のレビューUIは Phase 4 の実装範囲である。

提案の `baseRevision` は、提案が計算された本文そのものの revision である。clean な場合は
直前の GET の revision と一致する。dirty な本文から提案を作る場合は、未保存バッファの raw
UTF-8 本文から同じ規則で計算した revision を使う。現在のバッファ revision と一致しない提案は
古い提案として適用せず、範囲の推測や自動rebaseを行わない。

## 2. ファイル対象の固定条件

### 2.1 対応するファイル

- browse root（通常はユーザーの home）配下の通常ファイル。
- 本文全体が UTF-8 として妥当で、NUL byte を含まないファイル。
- 拡張子による許可リストは設けない。`.md`、`.mdx`、コード、設定、プレーンテキストなど、
  UTF-8 テキストなら同じ API 対象とする。Markdown の判定と描画モードは既存の `filemeta`/viewer
  の規則に従う。
- 空ファイルは対象に含む。

### 2.2 対応しないファイルと上限

- 編集対象のファイル本文、および保存後本文は **2 MiB（2 * 1024 * 1024 bytes）以下**。
  上限は文字数ではなく UTF-8 バイト数で判定する。JSON envelope のオーバーヘッドは本文上限に
  含めないが、サーバーは本文を上限+1までしか受け取らない。
- NUL byte を含むバイナリ、UTF-8 でない本文、画像その他のバイナリは編集不可。
- 2 MiB を超えるファイルは viewer の既存 truncated 表示に留め、編集モードへ遷移しない。
- ファイルの新規作成、ディレクトリ作成、rename、delete は本APIの責務ではない。既存の fs 操作を
  使用する別フローとし、PUT は存在する通常ファイルの本文置換だけを行う。

### 2.3 パスとセキュリティ

- `path` は browse root 相対の非空相対パスのみ。`/` で始まる絶対パス、ドライブ形式、NUL byte、
  `.`/`..` による root 外への移動を拒否する。
- 既存 fs の denylist（`.claude`、`.claude.json`、`.config/agent-fleet`、`.ssh`、
  `.git-credentials`、`.codex`、`.gemini`、`.copilot`、`.cursor`、`.config/cursor`、
  `.kiro`、`.local/share/opencode`、`.local/share/kiro-cli`、`.aws` など）を一覧・GET・PUT
  すべてで共通適用する。denylist の正本は `workspace/agent/fs.go` とし、追加時は書込み面にも
  同時反映する。
- 対象ファイル自身、親ディレクトリ、パス中のいずれかの component が symlink の場合は拒否する。
  symlink を解決して許可 root 内かを判定する方式は採らない。scratch root と role-scoped docs
  mount は読み取り専用で、相対パスの解決先になっても PUT では許可しない。
- PUT は既存のファイルを置換するだけで、親ディレクトリを作成しない。対象が無い、ディレクトリ、
  特殊ファイルの場合は保存しない。

## 3. 保存API契約

### 3.1 ルートと共通事項

| 面 | ルート | 備考 |
|---|---|---|
| Console公開面 | `GET /api/fs/file` / `PUT /api/fs/file` | CP の認証・membership・workspace running gate を通る。 |
| Agent内部面 | `GET /fs/file` / `PUT /fs/file` | CP が `/api` を剥がして中継する。Agent 外部公開はしない。 |

既存 GET は query parameter `path` を使う。PUT は JSON body を使う。すべての JSON は UTF-8、
`Content-Type: application/json; charset=utf-8` とする。認証、テナント、Workspace 状態の共通
エラーは [docs/dev/05-api-contracts.md](dev/05-api-contracts.md) の横断規約に従う。

### 3.2 GET `/fs/file?path=...`

編集可能な本文を返す成功レスポンスは次の形に revision を加えたものとする。

```json
{
  "path": "repos/example/README.md",
  "size": 1842,
  "binary": false,
  "truncated": false,
  "content": "# Example\n",
  "revision": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
```

- `size` は raw bytes のサイズ。
- `content` はファイル全体をJSON文字列として返す。改行等はJSONの通常のescapeだけを行う。
- `revision` は `content` をUTF-8バイトへ戻したraw bytesのSHA-256で、編集開始時の保存前提として
  必ず保持する。
- binary または truncated の既存レスポンスは `content`/`revision` を編集用に扱えない。編集UIは
  それらを検出して edit mode を提供しない。GETでバイナリを403に変更することはしない。

### 3.3 PUT `/fs/file`

#### Request

```json
{
  "path": "repos/example/README.md",
  "content": "# Example\nUpdated.\n",
  "baseRevision": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
```

| field | 型 | 必須 | 契約 |
|---|---|---:|---|
| `path` | string | yes | browse root 相対の既存通常ファイル。GETと同一の文字列を使う。 |
| `content` | string | yes | 保存後のファイル全体。UTF-8、NUL byteなし、2 MiB以下。空文字列を許可。 |
| `baseRevision` | string | yes | `sha256:` + lowercase hex 64桁。クライアントが最後に取得/確認した本文のrevision。 |

未知のフィールドは無視してよいが、必須フィールドの欠落、型違い、revision の形式違いは保存前に
拒否する。`PUT` に強制上書きフラグ、`force`、空の `baseRevision` の特例は設けない。

#### 200 response

```json
{
  "path": "repos/example/README.md",
  "size": 1958,
  "revision": "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
}
```

成功時は `content` を返さず、保存した本文の raw byte サイズと新しい revision を返す。新しい
revision がリクエスト本文のハッシュと一致することをクライアントが確認できる。dirty はこの
応答を受けて初めて解除する。

#### Atomic write と revision 検査

成功処理は次の順序を守る。

1. パスを相対・denylist・symlink・通常ファイルとして検証し、現在の本文を読み込む。
2. 現在の本文の revision と `baseRevision` を比較する。不一致なら旧ファイルを変更せず409。
3. 同じディレクトリの一時ファイルへ新本文を書き、`fsync` する。既存の permission bits は可能な
   限り保持する。
4. 一時ファイルを対象名へ rename し、親ディレクトリを `fsync` する。
5. rename 後の成功結果として新本文の revision を返す。

revision の比較から rename までを同一パスの排他的処理にする。途中で失敗した場合、旧ファイルを
残し、一時ファイルを片付け、500 `write_failed` を返す。rename は同一ファイルシステム内で行う。

### 3.4 エラー契約

エラーはすべて次の JSON 形で返す。`message` は開発者向けの安定しない説明であり、Console の
表示文言は安定した `code` をローカライズする。

```json
{
  "error": {
    "code": "revision_conflict",
    "message": "file changed since it was read",
    "details": {}
  }
}
```

PUT が追加する確定コードは次のとおり。

| HTTP | code | 発生条件 | 変更結果 |
|---:|---|---|---|
| 400 | `bad_path` | 空、絶対、traversal、NUL、または相対パス規則違反 | 変更なし |
| 400 | `symlink_not_allowed` | 対象または親 component に symlink がある | 変更なし |
| 400 | `bad_request` | JSON不正、必須フィールド欠落、型違い、revision形式違い | 変更なし |
| 403 | `denied` | denylist、書込み不可root、権限・認可境界に該当 | 変更なし |
| 404 | `not_file` | 対象が存在しない、ディレクトリ、通常ファイルでない | 変更なし |
| 409 | `revision_conflict` | 現在のrevisionが `baseRevision` と一致しない | 変更なし |
| 413 | `too_large` | 現在または保存後本文が2 MiBを超える | 変更なし |
| 415 | `binary_not_supported` | NUL byteまたはUTF-8不正の本文を保存しようとした | 変更なし |
| 500 | `read_failed` | 現在本文の読み込みに失敗 | 変更なし（保証できない場合は保存処理を開始しない） |
| 500 | `write_failed` | 一時書込み、fsync、rename、後処理に失敗 | rename成功前は旧本文を保持 |

`revision_conflict` の `details` は次の情報を返してよいが、現在本文そのものは返さない。

```json
{
  "path": "repos/example/README.md",
  "baseRevision": "sha256:...",
  "currentRevision": "sha256:..."
}
```

クライアントは競合時に自動再送せず、GETで現在本文を取得してユーザーに差分確認を求める。
Workspace stopped/starting、未認証、membership外などの共通エラーは、この表のPUT固有コード
より優先される既存の横断契約（通常は401/403/409）を使う。

## 4. AI提案の構造化フォーマット

### 4.1 型

提案は対象ペインのコンテキストに紐づけて渡し、パスはこの型へ含めない。対象パスの取り違えを
避けるため、呼び出し側が対象 pane/file を固定してから適用する。

```ts
type Revision = `sha256:${string}`;

type EditRange = {
  /** 0-based, inclusive, UTF-16 code-unit offset. */
  from: number;
  /** 0-based, exclusive, UTF-16 code-unit offset. */
  to: number;
};

type EditSuggestion = {
  /** UIに表示する1〜240 UTF-8 bytesの短い説明。変更命令として解釈しない。 */
  summary: string;
  /** range を replacement に置き換える。空文字列は削除を表す。結果本文は2 MiB以下。 */
  replacement: string;
  /** 提案元本文に対する [from, to) の範囲。 */
  range: EditRange;
  /** range を計算した本文のRevision。 */
  baseRevision: Revision;
};
```

wire envelope が必要な場合は version を持たせる。

```json
{
  "kind": "edit_suggestion",
  "version": 1,
  "suggestion": {
    "summary": "見出しを具体化",
    "replacement": "## 保存競合の扱い",
    "range": {"from": 12, "to": 18},
    "baseRevision": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  }
}
```

### 4.2 range と適用規則

- `from` は0以上、`to` は `from` 以上で、本文のUTF-16 code-unit長以下。範囲は `[from, to)`。
- `summary` は空でない UTF-8 文字列で、UTF-8 bytes で240以下。`replacement` はUTF-8文字列で、
  適用後の本文が2 MiBを超える提案は受け付けない。
- UTF-16 code-unit を採用するのは、CodeMirror 6 の document offset と一致させるため。サロゲート
  ペアの途中を指定するrangeは不正として適用しない。
- `replacement` はUTF-8として妥当な文字列で、改行を含めてそのまま挿入する。改行コードの自動変換、
  trim、format、全体再生成は行わない。
- 適用前に現在のバッファ本文から revision を再計算し、`suggestion.baseRevision` と完全一致する
  ことを確認する。不一致なら `suggestion_stale` 相当のUI状態として棄却する。
- Phase 4 のMVPは単一rangeの1提案とする。複数hunk、複数候補、fuzzy match、自動rebaseはPhase 5
  以降で別途設計する。
- `summary` は表示用メタデータであり、ファイル内容や権限を変更する命令として実行しない。

### 4.3 保存との関係

提案の accept はメモリ上の編集バッファだけを変更し、File ペインを dirty にする。accept 後も
`PUT /api/fs/file` は呼ばない。保存時はディスクの現在revisionに対して保存APIの
`baseRevision` を使い、ユーザーの Ctrl+S / Cmd+S の明示操作だけが書込みを発生させる。

## 5. フェーズ受け入れ条件（Phase 0）

- [ADR 0027](decisions/0027-markdown-code-editor.md) と本書で、CodeMirror 6、File ペイン拡張、
  Markdown 3モード、AIの書込み禁止、draftのメモリ限定が明記されている。
- GET の revision、PUT の request/200 response、compare-and-swap、atomic write、競合時の
  `409 revision_conflict` が固定されている。
- UTF-8テキストのみ、2 MiB上限、絶対パス・denylist・symlink拒否が固定されている。
- `EditSuggestion` の `summary` / `replacement` / `range` / `baseRevision` と、rangeの単位・
  stale時の扱いが固定されている。
- Phase 1以降のコード変更、依存追加、保存API実装、UI実装はこのPhase 0の作業に含めない。
