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
MarkdownView、MarpView、Mermaid の遅延ロード経路を再利用する。通常のMarkdownでは
`preview` がMarkdownViewのプレビュー、`split` がCodeMirror編集とプレビューの左右分割になる。
Marpではプレビュー側を既存MarpView（スライド表示）で描画する。現行のMarkdownの
`source` は新しい `edit` に、`slides` は `preview` のMarp描画に対応付け、4つ目のモードは増やさない。

編集開始時に GET した本文をタブ内のバッファへコピーし、同時にその本文の
`baseDiskRevision` と保存対象パスを保持する。入力でバッファが変われば dirty とする。
dirty 本文、undo 履歴、提案、バッファ世代はメモリだけに置く。`PaneContent` や layout descriptor
には本文・revision・undo・提案を格納しない。layout自体は現行どおり per-user/tenant の
`sessionStorage` と `localStorage` へ永続化されるため、ソース本文をその経路へ混入させない。

dirty判定は「ペインを閉じる」だけでなく、layout mutation の手前にある dirty registry/navigation
guard で一括して扱う。対象は `openActive` によるactive paneの差し替え、8ペイン上限時の
`openInNew` による再利用、`setPaneTarget`、Back/Forwardの `wireLayoutHistory`、tenant切替・
全pane reset、readerへの切替、popout（`openPanePopout`）を含む。保存・破棄・キャンセルを
ユーザーに選ばせ、guardを通過するまでlayoutをcommitしない。beforeunloadもdirty時に有効にする。
dirtyなfile paneのpopoutは、バッファ転送を別途設計しないv1では拒否または保存/破棄確認を必須とする。

### 1.2 保存と競合

revision は、正規化前のファイル生バイト列に対する SHA-256 である。ディスク上の前提は
`baseDiskRevision`、編集中バッファの前提は `bufferRevision` と呼び分ける。

```text
revision = "sha256:" + lowercase(hex(SHA-256(raw file bytes)))
```

v1の編集対象は **LFのみ** とする。raw bytesにCR（`0x0d`）を含む本文、CRLF、CR単独、混在改行は
読み取り可能でも編集不可とする。改行、末尾改行、Unicodeの表現を正規化してはならない。
LFのみなら CodeMirror 6 のdocumentは `\n` を1 code unitの行区切りとして保持し、JSONの `content`
をUTF-8バイト列へ戻したものとdocumentの文字列が一致する。したがってraw byte revisionと
CodeMirrorのoffsetに変換表を設けずに済む。将来CRLFを扱う場合は、raw/document間の改行マッピングを
別バージョンで定義してから対象を広げる。

保存成功時のrevisionは保存したraw bytesから再計算する。同じ本文は同じrevisionになる。

同一APIの保存は、検証後に得た正規化相対パスをmutex keyとして対象パスごとに直列化する。
入力文字列をkeyにしないため、`a/../b` 等の別名で直列化を回避できない。APIは比較時点で観測した
現在本文のrevisionと `baseDiskRevision` が一致する場合だけ書込みを開始する。

このCASは、**比較時点までにAgentが観測できた変更を検出し、同一APIの保存を直列化する契約**である。
shell、Claude/Codex、git checkout等の外部writerをこのmutexへ参加させないv1では、比較後rename前の
外部変更を検出・防止する保証はない。その場合は外部変更を新本文で上書きし得る。全writerを協調ロック
下に置く設計は採用せず、Phase 1のテストと運用上の限界として明記する。

### 1.3 AI提案の境界

既存のClaude/Codex等の一般セッションは、環境によってWrite/Edit/Bash等の書込み能力を持つ。
したがって「AI全体が書込みtoolを持たない」とは定義しない。Phase 4の変更提案機能だけは、
read-only allowlistで動く提案生成チャネル（書込みtool・PUT・shell変更操作なし）を使う。
一般セッションのtool権限をこの設計で縮小したり、既存の監査方針を置き換えたりはしない。

提案をacceptするとバッファの指定範囲をreplacementで置き換え、dirtyになる。rejectはバッファを
変更しない。accept/rejectのレビューUIはPhase 4の実装範囲である。

提案の `baseRevision` は、提案が計算された本文そのものの `bufferRevision` である。cleanな場合は
直前のGETのrevisionと一致する。dirtyな本文から提案を作る場合は、未保存バッファのraw UTF-8本文
から同じ規則で計算したrevisionを使う。現在のbuffer revisionと一致しない提案は古い提案として
適用せず、範囲の推測や自動rebaseを行わない。

非同期応答には `paneId`、`filePath`、`requestId`、`sourceRevision` を付ける。`sourceRevision` は
提案計算時のbufferRevisionであり、提案の `baseRevision` と一致しなければならない。適用時に
pane/file/request identityと現在bufferRevisionを全て確認し、どれかが違えば拒否する。

### 1.4 編集可否と安全なファイル操作

v1の書込みAPIはLinux上のWorkspace Agent（Docker/ECS/native Linux）を対象とする。root directory
fdを固定し、親directory fdをfd-relativeに解決してから操作する。`openat2` の
`RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS`、`fstatat(AT_SYMLINK_NOFOLLOW)`、`renameat`（または同等の
fd-relative・symlink非追従操作）をGET/file、download、PUTで共通利用する。文字列検査とLstatだけの
helperはTOCTOUを防げないため不十分であり、既存の `safeBrowsePath` / `safeWritableBrowsePath` は
このPUTのsafe helperとして再利用しない。

APIのパスはPOSIX slash区切りのbrowse-root相対canonical pathだけを受け付ける。`\\`、空component、
`.`/`..` component、NUL、先頭 `/`、`^[A-Za-z]:` のdrive形式、UNC形式を拒否する。入力をcleanして
別名に変換するのではなく、拒否後にslash結合したcanonical relative pathをmutex keyとする。
Windows runtimeはv1対象外だが、Windows形式の表記はLinuxでも先に拒否する。

GETとdownloadもsymlinkを追跡しない。対象または親componentがsymlinkなら
`400 symlink_not_allowed` とし、denylistをsymlinkで迂回できないことを保証する。

### 1.5 保存クライアントの世代管理

同一file paneからのPUTは一度に1件だけ送る。送信時に `{paneId, path, bufferGeneration,
bufferRevision, content, baseDiskRevision}` のsnapshotを保持する。応答時、現在のbufferGeneration
またはbufferRevisionがsnapshotと同じ場合だけdirtyを解除し、成功レスポンスのrevisionを新しい
`baseDiskRevision`として保持する。入力が続いていた場合はdirtyを維持し、現在bufferを次の保存対象にする。

応答を失った場合はGETでremoteのeditable/revisionを確認する。remote revisionが送信本文のhashと
一致すればsnapshotの保存成功として上記ルールを適用し、一致しなければ競合または
`write_state_unknown`としてユーザー確認を求める。保存中の入力を成功応答で誤ってcleanにしない。

## 2. ファイル対象の固定条件

### 2.1 対応するファイル

- browse root（通常はユーザーの home）配下の通常ファイル。
- 本文全体が UTF-8 として妥当で、NUL byte を含まず、CR byteを含まないファイル。CRLF/CR単独/
  混在改行は `unsupported_newline` として読み取り専用にする。
- 拡張子による許可リストは設けない。`.md`、`.mdx`、コード、設定、プレーンテキストなど、
  UTF-8 テキストなら同じ API 対象とする。Markdown の判定と描画モードは既存の `filemeta`/viewer
  の規則に従う。
- 空ファイルは対象に含む。

### 2.2 対応しないファイルと上限

- 編集対象のファイル本文、および保存後本文は **2 MiB（2 * 1024 * 1024 bytes）以下**。
  上限はdecoded UTF-8バイト数で判定する。
- HTTP PUT body全体（JSON envelope、escape後の文字列、未知フィールドを含む）の上限は
  **16 MiB** とする。decoded `content` の2 MiB上限とは別の制限であり、JSON escapeでbodyが
  膨らむことを考慮する。どちらかを超えたら413とし、サーバーは16 MiBを超えて読み込まない。
- NUL byte を含むバイナリ、UTF-8 でない本文、画像その他のバイナリは編集不可。
- 2 MiB を超えるファイルは viewer の既存 truncated 表示に留め、編集モードへ遷移しない。
- ファイルの新規作成、ディレクトリ作成、rename、delete は本APIの責務ではない。既存の fs 操作を
  使用する別フローとし、PUT は存在する通常ファイルの本文置換だけを行う。

### 2.3 パスとセキュリティ

- `path` は browse root 相対の非空canonical相対パスのみ。`/` で始まる絶対パス、`\\`、空component、
  `.`/`..` component、ドライブ形式（`^[A-Za-z]:`）、UNC形式、NUL byteを拒否する。pathを
  `Clean`して許可することで別名を作る方式は採らない。
- 既存 fs の denylist（`.claude`、`.claude.json`、`.config/agent-fleet`、`.ssh`、
  `.git-credentials`、`.codex`、`.gemini`、`.copilot`、`.cursor`、`.config/cursor`、
  `.kiro`、`.local/share/opencode`、`.local/share/kiro-cli`、`.aws` など）を一覧・GET・PUT
  すべてで共通適用する。denylist の正本は `workspace/agent/fs.go` とし、追加時は書込み面にも
  同時反映する。
- 対象ファイル自身、親ディレクトリ、パス中のいずれかのcomponentがsymlinkの場合は拒否する。
  symlinkを解決して許可root内かを判定する方式は採らない。判定は §1.4 のroot/parent fd固定と
  `openat2`/`fstatat(AT_SYMLINK_NOFOLLOW)`でTOCTOUを抑止する。scratch rootとrole-scoped docs
  mountは読み取り専用で、相対パスの解決先になってもPUTでは許可しない。
- PUT は既存のファイルを置換するだけで、親ディレクトリを作成しない。対象が無い、ディレクトリ、
  特殊ファイルの場合は保存しない。

## 3. 保存API契約

### 3.1 ルートと共通事項

| 面 | ルート | 備考 |
|---|---|---|
| Console公開面 | `GET /api/fs/file` / `PUT /api/fs/file` | CP の認証・membership・workspace running gate を通る。 |
| Agent内部面 | `GET /fs/file` / `PUT /fs/file` | CP が `/api` を剥がして中継する。Agent 外部公開はしない。 |

既存 GET は query parameter `path` を使う。PUT は JSON body を使う。Console/Agentが送るcanonical
Content-Typeは `application/json` とする。サーバーは互換性のため `application/json; charset=utf-8`
も受け付けるが、他のmedia typeは415とする。認証、テナント、Workspace 状態の共通
エラーは [docs/dev/05-api-contracts.md](dev/05-api-contracts.md) の横断規約に従う。

### 3.2 GET `/fs/file?path=...`

ファイルを返す成功レスポンスは `editable` と `editabilityReason` を必ず含む。編集可能な本文は
次の形にrevisionを加えたものとする。

```json
{
  "path": "repos/example/README.md",
  "size": 1842,
  "binary": false,
  "truncated": false,
  "editable": true,
  "editabilityReason": null,
  "content": "# Example\n",
  "revision": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
```

- `size` は raw bytes のサイズ。
- `content` はファイル全体をJSON文字列として返す。改行等はJSONの通常のescapeだけを行う。
- `editable: true` は、書込み可能root内、通常ファイル、symlinkなし、UTF-8、NULなし、CRなし、
  2 MiB以下というPUT条件を全て満たすことを表す。`editabilityReason` はそのとき `null`。
- `revision` は `editable: true` のときだけ返す。`content` をUTF-8バイトへ戻したraw bytesの
  SHA-256で、編集開始時の `baseDiskRevision` として保持する。
- `editable: false` の理由は安定値 `binary`、`invalid_utf8`、`too_large`、`read_only_root`、
  `unsupported_newline` のいずれか。binary/truncated/読み取り専用rootではrevisionを返さない。
- symlink、denylist、traversalなど安全境界違反は `editable:false` へ丸めず、GET/downloadとも
  対応するHTTPエラーを返す。GETでバイナリを403に変更することはしない。

### 3.3 PUT `/fs/file`

#### Request

```json
{
  "path": "repos/example/README.md",
  "content": "# Example\nUpdated.\n",
  "baseDiskRevision": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
```

| field | 型 | 必須 | 契約 |
|---|---|---:|---|
| `path` | string | yes | browse root 相対の既存通常ファイル。GETと同一の文字列を使う。 |
| `content` | string | yes | 保存後のファイル全体。UTF-8、NUL/CRなし、LF-only、2 MiB以下。空文字列を許可。 |
| `baseDiskRevision` | string | yes | `sha256:` + lowercase hex 64桁。クライアントが最後に取得/確認したディスク本文のrevision。 |

未知のフィールドは拒否する。必須フィールドの欠落、型違い、revisionの形式違いは保存前に拒否する。
`PUT` に強制上書きフラグ、`force`、空の `baseDiskRevision` の特例は設けない。

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
2. 現在の本文のrevisionと `baseDiskRevision` を比較する。不一致なら旧ファイルを変更せず409。
3. 同じディレクトリの一時ファイルへ新本文を書き、`fsync` する。既存の permission bits は可能な
   限り保持する。
4. 一時ファイルを対象名へ rename し、親ディレクトリを `fsync` する。
5. rename 後の成功結果として新本文の revision を返す。

revisionの比較からrenameまでを同一正規化相対パスの排他的処理にする。ただし、この排他は同一API
の保存同士に限られ、外部writerは保護しない。renameは同一ファイルシステム内で行う。

rename前の一時書込み/fsync失敗では、旧ファイルを変更せず一時ファイルを片付け、500
`write_failed`を返す。rename後の親directory fsync失敗では、旧ファイルを残す保証はない。実体が
新本文か旧本文かをAPIだけで断定できないため、500 `write_state_unknown` を返し、クライアントは
GETでrevisionを照合する。`write_state_unknown`を通常の競合として自動再送してはならない。

置換時に保持するのは既存regular fileのUnix permission bits（`mode.Perm()`）だけとする。owner、
ACL、xattr、setuid/setgid/sticky等のspecial bitsの保持はv1で保証しない。temp fileとrename後の
所有者はAgent実行ユーザーの通常のファイルシステム規則に従う。

### 3.4 エラー契約

Agent/CPがアプリケーションとして生成するエラーは、既存の `WriteErr`/`writeAPIErr` と同じく
次のJSON形で返す。`message` は開発者向けの安定しない説明であり、Consoleの表示文言は安定した
`code`をローカライズする。v1の共通error objectに `details` フィールドは設けない。

```json
{
  "error": {
    "code": "revision_conflict",
    "message": "file changed since it was read"
  }
}
```

PUT が追加する確定コードは次のとおり。

| HTTP | code | 発生条件 | 変更結果 |
|---:|---|---|---|
| 400 | `bad_path` | 空、絶対、traversal、NUL、Windows形式、またはcanonical相対パス規則違反 | 変更なし |
| 400 | `symlink_not_allowed` | 対象または親componentにsymlinkがある。文字列/LstatだけではTOCTOUを防げないため、fd-relative操作で拒否する | 変更なし |
| 400 | `bad_request` | JSON不正、必須フィールド欠落、型違い、未知フィールド、revision形式違い | 変更なし |
| 415 | `unsupported_media_type` | Content-Typeが `application/json`（charset=utf-8含む）ではない | 変更なし |
| 403 | `denied` | denylist、書込み不可root、権限・認可境界に該当 | 変更なし |
| 404 | `not_file` | 対象が存在しない、ディレクトリ、通常ファイルでない | 変更なし |
| 409 | `revision_conflict` | 比較時点で現在のrevisionが `baseDiskRevision` と一致しない | 変更なし |
| 413 | `too_large` | decoded contentが2 MiB、またはHTTP body全体が16 MiBを超える | 変更なし |
| 415 | `binary_not_supported` | NUL byteまたはUTF-8不正の本文を保存しようとした | 変更なし |
| 415 | `unsupported_newline` | CRLF、CR単独、混在改行を含む本文を保存しようとした | 変更なし |
| 500 | `read_failed` | 現在本文の読み込みに失敗 | 変更なし（保証できない場合は保存処理を開始しない） |
| 500 | `write_failed` | rename前の一時書込み、fsync等に失敗 | 旧本文を保持 |
| 500 | `write_state_unknown` | rename後の親directory fsync等に失敗し、実体を断定できない | GET照合が必要 |

クライアントは競合時に自動再送せず、GETで現在本文を取得してユーザーに差分確認を求める。
`symlink_not_allowed`を400にするのは、認証済み利用者の権限不足ではなく、要求されたpath形式と
安全な解決条件が不正だからである。denylist/readonly rootを403にするのは、path形式は妥当でも
その対象への操作権限を明示的に拒否するためである。Workspace stopped/starting、未認証、membership外
などの共通エラーは、この表のPUT固有コードより優先される既存の横断契約（通常は401/403/409）を使う。

CPとAgentが到達できずCPのproxyが生成するplain-text 502/504は、アプリケーションエラー契約の外側
である。Consoleの `api()` は既存どおり `http_<status>` として扱い、JSONエラーと混同しない。

新規のserver error code（`bad_path`、`symlink_not_allowed`、`unsupported_media_type`、
`revision_conflict`、`too_large`、`binary_not_supported`、`unsupported_newline`、
`write_state_unknown`等）は、実装時にAgent/CPのerror code定数とConsoleの日本語/英語i18nを
同時登録する。Phase 0ではコード登録を行わない。`suggestion_stale` はHTTP errorではなく、
非同期提案を適用できなかったときのConsole側の安定したUI codeとして固定し、Phase 4で
Consoleの英日カタログへ登録する。

### 3.5 JSON decoder とサイズ制限

Phase 1のAgent/CP実装は、現在の `httpx.DecodeJSON` の単純な一回decodeを拡張する。

- HTTP bodyを16 MiBでhard limitし、超過を読み込まない。
- JSON objectを1個だけ受け付け、decode後に末尾のwhitespace以外がないことを検査する。第2 JSON値、
  trailing garbage、空bodyを拒否する。
- `DisallowUnknownFields`相当で未知フィールドを拒否し、required fieldと型を検証する。
- decoded `content` のUTF-8 byte数を2 MiB以下で検査し、NUL/CR/UTF-8不正を保存前に拒否する。
- CPの監査用body抽出も同じ16 MiB上限を使い、JSON本文を監査ログへ記録しない。

## 4. AI提案の構造化フォーマット

### 4.1 型

提案本体は対象ペインのコンテキストに紐づけて渡し、パスは `EditSuggestion` 型へ含めない。
非同期transport envelopeにはidentityを含め、対象パスの取り違えを避ける。

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

type EditSuggestionEnvelope = {
  kind: "edit_suggestion";
  version: 1;
  paneId: string;
  filePath: string;
  requestId: string;
  /** 提案計算時のbufferRevision。suggestion.baseRevisionと同値でなければ不正。 */
  sourceRevision: Revision;
  suggestion: EditSuggestion;
};
```

wire envelope が必要な場合は version を持たせる。

```json
{
  "kind": "edit_suggestion",
  "version": 1,
  "paneId": "pane-3",
  "filePath": "repos/example/README.md",
  "requestId": "req-42",
  "sourceRevision": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
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
- UTF-16 code-unitを採用するのは、LF-onlyで固定したCodeMirror 6 document offsetと一致させるため。
  サロゲートペアの途中を指定するrangeは不正として適用しない。CRLF等の改行マッピングはv1に存在しない。
- `replacement` はUTF-8として妥当な文字列で、改行を含めてそのまま挿入する。改行コードの自動変換、
  trim、format、全体再生成は行わない。
- 適用前に現在のpaneId、filePath、requestId、sourceRevision、バッファ本文のrevisionを確認する。
  `sourceRevision`、`suggestion.baseRevision`、現在bufferRevisionの3つが完全一致しない提案は、
  `suggestion_stale` のUI code/stateとして棄却する。これはHTTPレスポンスcodeではない。paneが
  破棄・差し替え済みなら適用しない。
- Phase 4 のMVPは単一rangeの1提案とする。複数hunk、複数候補、fuzzy match、自動rebaseはPhase 5
  以降で別途設計する。
- `summary` は表示用メタデータであり、ファイル内容や権限を変更する命令として実行しない。

### 4.3 保存との関係

提案のacceptはメモリ上の編集バッファだけを変更し、Fileペインをdirtyにする。accept handlerは
`PUT /api/fs/file`を呼ばないことをテストで保証する。保存時はディスクの現在revisionに対して
`baseDiskRevision`を使い、ユーザーのCtrl+S / Cmd+SまたはSaveボタンの明示操作だけが書込みを発生させる。

## 5. UI操作とアクセシビリティ契約

- view/edit切替は `role="tablist"` 内の `role="tab"` とし、選択状態を `aria-selected` で表す。
  編集開始時はCodeMirrorへfocusを移し、viewへ戻ると切替tabへfocusを戻す。キーボードでtab間を
  移動できる。
- Markdownのedit/preview/split切替はbutton groupとし、トグル状態を `aria-pressed` で表す。
  Marpのプレビューはこのpreview側の描画方式であり、別の編集モードを増やさない。
- Saveボタンはタッチ端末を含む全環境で提供し、Ctrl+S / Cmd+Sと同じsnapshot保存処理を呼ぶ。
  saving中は同一paneのSaveを無効化し、追加入力は可能だが別PUTを並行送信しない。
- dirty、saving、saved、revision conflict、write state unknownは `role="status"` と
  `aria-live="polite"` で状態変化を告知する。競合・状態不明は `role="alert"` 相当で明示し、
  再読込・差分確認・破棄の選択肢をfocus可能にする。
- Phase 2の受け入れでは、マウスなしのview/edit切替・保存・競合表示、タッチ端末のSave操作、
  focus移動、`aria-selected`/`aria-pressed`/announceを確認する。

## 6. フェーズ受け入れ条件

### Phase 0（本変更）

- [ADR 0027](decisions/0027-markdown-code-editor.md) と本書で、CodeMirror 6、File ペイン拡張、
  Markdown/Marpの3モード対応、未保存本文のメモリ限定、一般AIセッションと提案生成チャネルの
  権限差が明記されている。
- CAS保証を「比較時点までに観測した変更の検出 + 同一API保存の直列化」に限定し、外部writerとの
  raceを保証しないこと、mutex keyを検証後canonical relative pathにすることが固定されている。
- Linux fd-relative操作、`openat2`/`fstatat`/`renameat`相当、GET/downloadのsymlink拒否、
  safeBrowsePathの不適格性、POSIX pathとWindows形式pathの判定が固定されている。
- LF-onlyの編集バッファ、raw byte revision、CodeMirror UTF-16 offsetの整合とCRLFの将来拡張条件が
  固定されている。
- rename後directory fsync失敗の `write_state_unknown`、permission bitsのみ保持、owner/ACL/xattr
  非保証が固定されている。
- `baseDiskRevision`（保存API）と `baseRevision`/`bufferRevision`（AI/バッファ）を区別し、
  save snapshot、generation、応答喪失時GET復旧、dirty guard全経路が固定されている。
- GETの `editable`/`editabilityReason`、decoded content 2 MiB、HTTP body 16 MiB、strict JSON decoder、
  JSONエラーとplain-text gateway errorの境界、`suggestion_stale`のUIコードが固定されている。
- Phase 1以降の実装コード・依存追加・保存API実装・UI実装は本Phase 0の作業に含めない。

### Phase 1（保存API基盤）

- AgentのPUT route/handler、CPのPUT route/proxy、CP proxyのPUTテストを追加する。
- `fs.file.put` を監査分類へ追加する。監査targetはJSON bodyの `path` だけを16 MiB上限内で安全に
  抽出し、`content`、replacement、token、差分本文を監査ログへ記録しない。body parse失敗時は監査
  targetを作らず、成功レスポンスの操作だけを記録する。pathはcanonical lexical validation後だけ
  targetにし、不正値は固定の `<invalid-path>` にする。
- `httpx.DecodeJSON`相当のstrict decoder、Content-Type、body/decoded size、trailing value、厳密UTF-8、
  unknown field検査を実装する。server error codeはAgent/CP定数とConsoleの英日i18nへ同時登録する。
- fd-relative path操作とGET/file/downloadのsymlink拒否を実装し、safeBrowsePath系helperをPUTへ流用しない。
- GETのeditable判定、LF-only、revision、CASの同一API直列化を実装する。
- symlink、path alias、外部writerのrace、GET読み取りrace、revision conflict、rename前失敗、
  rename後directory fsync失敗をfailure injectionを含むGo単体テストで検証する。外部writer raceは
  「保証しない範囲」を再現し、誤って保証を主張しないこともテスト文書に残す。

### Phase 2（Text/コード編集MVP）

- CodeMirror導入、view/edit、dirty、single-flight PUT、送信snapshot/generation、応答時dirty判定、
  `baseDiskRevision`更新、応答喪失時GET復旧を実装する。
- dirty registry/navigation guardをlayout commit前に接続し、openActive/openInNew再利用、history、
  tenant/reset、reader、popoutを網羅する。layout storageへ本文を入れない。
- beforeunload、ARIA、focus、status announce、タッチ向けSaveボタンを実装・テストする。

### Phase 3（Markdown編集）

- edit/preview/splitの既存MarkdownView/MarpView/Mermaid遅延ロード再利用を実装する。
- 現行source/preview/slidesとの対応、Marp preview、LF-only、dirty guardを回帰テストする。

### Phase 4（AI変更提案）

- 書込みtoolを持たないread-only allowlist提案チャネルを実装し、一般のWrite/Edit/Bash可能な
  セッションをこの経路に接続しない。
- `paneId`/`filePath`/`requestId`/`sourceRevision` identityとbuffer revision検査を実装し、
  stale proposalを拒否する。
- accept handlerがPUTを呼ばず、replacement適用がメモリbufferだけをdirty化することをテストで保証する。

### Phase 5（高度な支援）

- 複数候補、hunk単位accept、セッション連携、補完、CRLF/改行マッピング対応は別ADRまたは本ADRの
  改訂で設計してから着手する。
