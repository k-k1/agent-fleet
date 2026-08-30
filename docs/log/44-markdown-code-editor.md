# docs/44 — Console Markdown / コードエディタ設計と Phase 0 契約

> 正: 本書は Phase 0 で固定した設計契約。実装の詳細は各フェーズのコードとテストが正。/
> ADR: [0027](../decisions/0027-markdown-code-editor.md) /
> 対象: Console の既存 `file` ペイン + Workspace Agent の fs API

## 1. 固定する全体設計

### 1.1 ペインとバッファ

File ペインは次の2層を持つ。

| 層 | 値 | 意味 |
|---|---|---|
| File ペイン | `mode: "view" \| "edit"` | 読み取り専用表示か、編集バッファか。ペイン種別は `file` のまま。 |
| Markdown 表示 | `edit` / `preview` / `split` | edit 時の本文編集、既存 MarkdownView/MarpView によるプレビュー、左右分割。 |

非Markdownのテキストは CodeMirror 6 の編集表示を使う。Markdown の preview は既存の
MarkdownView、MarpView、Mermaid の遅延ロード経路を再利用する。通常のMarkdownでは
`preview` がMarkdownViewのプレビュー、`split` がCodeMirror編集とプレビューの左右分割になる。
Marpではトップレベルの `edit` / `preview` / `split` を維持し、preview側に既存の2つのrendererを
持つ。通常のMarkdownViewによるMarp `preview` と、MarpViewによる `slides` をpreview rendererの
選択として切り替える。`split`では右側preview rendererを切り替えられる。現行のMarkdownの
`source` は新しい `edit` に対応し、`preview` と `slides` はそれぞれこの2つのpreview rendererへ
対応付ける。トップレベルに4つ目のモードは増やさないため、Marpの通常previewを失わない。

Markdownファイルでは、この3モードをペイン上部の唯一のトップレベル操作とし、冒頭の表の
`mode: "view" | "edit"` はそこから導出する。`edit` と `split` は編集バッファを持つ
`mode: "edit"`、`preview` は `mode: "view"` に対応する。非Markdownのテキストは従来どおり
view/editの2択を直接操作する。編集可能なMarkdownに読み取り専用の `source` モードは設けない。
現行の `source` が持っていた選択→送ると行引用のジャンプは、§1.8のとおり編集面が引き継ぐ。

編集できないMarkdown（`editable:false`、2 MiB超、viewerのplainモード）は3モード群を出さず、
現行の preview / source / slides をそのまま維持する。この場合ペインは `mode: "view"` に固定される。

`preview` と `split` のプレビューは、ディスクの本文ではなく現在の編集バッファから描画する。
未保存の変更がそのまま見えることが編集モードの目的であり、保存の有無でプレビューの入力を
切り替えない。プレビューの再描画は入力ごとではなくデバウンスし、書きかけのMermaid図やMarp
スライドを毎打鍵描画しない。

Marpかどうか（front matterの `marp: true`）は編集で変わり得る。判定を毎打鍵で追従させると
モード群のボタンが増減して不安定になるため、判定にも同じデバウンスを用いる。判定が変わって
選択中のrendererが使えなくなった場合は、通常previewへ落とす。

3モードとpreview rendererの選択はペイン内のUI状態であり、本文・revision・undo・提案と同じく
layout descriptorへ永続化しない。ブラウザ再読込後は初期状態から始める。

編集開始時に GET した本文をタブ内のバッファへコピーし、同時にその本文の
`baseDiskRevision` と保存対象パスを保持する。入力でバッファが変われば dirty とする。
dirty 本文、undo 履歴、提案、バッファ世代はメモリだけに置く。`PaneContent` や layout descriptor
には本文・revision・undo・提案を格納しない。layout自体は現行どおり per-user/tenant の
`sessionStorage` と `localStorage` へ永続化されるため、ソース本文をその経路へ混入させない。

dirty判定は「ペインを閉じる」だけでなく、layout mutation とアプリケーション lifecycle の手前に
ある dirty registry/navigation guard で一括して扱う。対象は `openActive` によるactive paneの差し替え、
8ペイン上限時の `openInNew` による再利用、`setPaneTarget`、Back/Forwardの `wireLayoutHistory`、
tenant切替・全pane reset、readerへの切替、popout（`openPanePopout`）を含む。さらにPWAの直接reload、
logout前のlocal state削除、version update reload、workspaceのrecreate/clean-home/stopなど、
bufferを破棄し得る操作も対象にする。

保存・破棄・キャンセルをユーザーに選ばせ、guardを通過するまでlayout commit、reload、logout、
workspace lifecycle操作を実行しない。beforeunloadもdirty時に有効にする。version updateにある
`__afUpdating` のbeforeunload例外はterminalの停止を避けるためのもので、editor dirty guardには
適用しない。更新は「保存して更新」「更新を中止」のeditor専用確認を経る。
dirtyなfile paneのpopoutは、バッファ転送を別途設計しないv1では拒否または保存/破棄確認を必須とする。

guardの破棄はin-flight PUTと復旧GETの完了を待つ非同期処理であり、その間のキャンセル
（Back/popstate・キャンセル・close）はguard requestの`AbortSignal`として破棄処理へ伝播する。
中断された破棄はバッファのclean化を行わずに失敗し、ナビゲーションは中止済みのままdirty bufferを
保持する。保存・破棄ボタンは二重送信を防ぐため処理中は無効化するが、キャンセルとcloseは
処理中も常に有効な安全な中断経路とする。guard requestの解決は冪等で、停滞していた保存/破棄の
遅延完了が後続の新しいguard requestを上書きしてはならない。

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

同一APIの保存は、request pathの字句検証で確定したcanonical相対pathをmutex keyとして対象パスごとに
直列化する。入力文字列をkeyにせず、canonicalでない別名は拒否するため、`a/../b` 等で直列化を
回避できない。key確定後は、対象や親directoryのfd安全検証、open、current本文のread/hashより前に
mutexを取得し、親directoryのfsync成否を含む保存結果が確定するまで保持する。APIはmutex保持中に
観測した現在本文のrevisionと `baseDiskRevision` が一致する場合だけ書込みを開始する。

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
helperはrequest-controlled pathのTOCTOUを抑止できないため不十分であり、既存の
`safeBrowsePath` / `safeWritableBrowsePath` はこのPUTのsafe helperとして再利用しない。

pathの許可形は操作面で分ける。**PUTはPOSIX slash区切りのbrowse-root相対canonical pathだけ**を
受け付ける。GET/downloadはそれに加えて、既存の`allowedReadRoots`（browse root、scratch root、
role-scoped docs root）のいずれかの配下を表すcanonical絶対pathも受け付ける。絶対pathは許可rootを
先に選び、そのroot fdからroot相対へ変換してopenat2等で解決する。許可root外の絶対pathは拒否する。
scratch/docsの絶対pathはGET/downloadでは `editable:false`、`editabilityReason:"read_only_root"`
とし、PUTへ渡しても拒否する。これによりユーザーガイドやMarkdown内の既存絶対リンクを壊さない。
絶対pathがbrowse root配下だった場合は、GETの応答 `path` をbrowse-root相対pathへ変換し、
その相対pathをPUTで使える `editable:true` の編集対象として扱う。

どちらの面でも `\\`、空component、`.`/`..` component、NUL、先頭 `/`（PUT）、
`^[A-Za-z]:` のdrive形式、UNC形式（GET/downloadも許可root配下でなければ拒否）を検証する。
入力をcleanして別名に変換するのではなく、PUTのcanonical relative pathをmutex keyとする。
Windows runtimeはv1対象外だが、Windows形式の表記はLinuxでも先に拒否する。

GETとdownloadもsymlinkを追跡しない。対象または親componentがsymlinkなら
`400 symlink_not_allowed` とする。これはrequest pathからdenylist名へsymlinkで到達する経路を
抑止するが、同一uidの非協調namespace mutatorやhardlinkによるinode別名までは保証しない（§2.3）。

### 1.5 保存クライアントの世代管理

同一file paneからのPUTは一度に1件だけ送る。送信時に `{paneId, path, bufferGeneration,
bufferRevision, content, baseDiskRevision}` のsnapshotを保持する。通常保存の200応答時、現在のbufferGeneration
またはbufferRevisionがsnapshotと同じ場合だけdirtyを解除し、成功レスポンスのrevisionを新しい
`baseDiskRevision`として保持する。入力が続いていた場合はdirtyを維持し、現在bufferを次の保存対象にする。

Agentから明示的な `500 write_state_unknown` を受けた場合、またはPUTがAgentへ到達した可能性を
否定できないまま応答を失った場合は、`SaveSnapshot`と現在のmineを保持して
`SaveStateUnknown`へ遷移する。この状態ではmineをdirtyのままとし、GETの一致だけで自動的にcleanへ
遷移しない。GETで分かるのは現在のlive namespaceであり、renameのクラッシュ耐久性ではないためである。

復旧GETの結果ごとの最小状態遷移は次のとおり。

| GET結果 | 遷移と保持内容 | 許可する次の操作 |
|---|---|---|
| `editable:true` かつrevisionが送信本文のhashと一致 | `SaveStateUnknown`を維持。live反映済みだがdurability未確定としてmineと`SaveSnapshot`を保持する。 | ユーザーが明示的に再保存するか、durabilityリスクを承認する。自動clean・自動再送はしない。 |
| `editable:true` かつrevisionがPUT送信時の旧`baseDiskRevision`と一致 | `SaveStateUnknown`を維持。mineをdirtyのまま保持する。 | ユーザー確認後に旧baseのまま再保存する。自動再送はしない。 |
| `editable:true` かつ上記以外の第三のrevision | `Conflict`へ遷移し、取得本文から`ConflictSnapshot`を作る。 | §1.6の通常競合解決を行う。 |
| GET失敗、404、または安全境界エラー | `SaveStateUnknown`を維持。mineと`SaveSnapshot`を保持する。 | 復旧を待って再取得するか、mineをコピーするか、明示確認して閉じる。 |
| `editable:false` | `SaveStateUnknown`を維持。revisionを推測せず、mineと`SaveSnapshot`を保持する。 | 再取得・mineのコピー・明示確認して閉じる。 |

liveが送信本文と一致した場合の明示的な再保存では、復旧GETで観測した送信本文のrevisionをbaseとして
ユーザー操作によりPUTを開始し、親directory fsyncまで成功した200を受けて初めて通常の世代規則で
cleanにする。この場合のdurabilityリスク承認も自動判定ではなくユーザー操作とし、現在bufferが
送信snapshotと同じかにかかわらず、まず復旧GETで確認した送信本文のrevisionを
`baseDiskRevision`へ設定する。その上で、現在bufferが送信snapshotと同じなら
`CleanRiskAccepted`、保存中に追加入力があればその観測revisionをbaseにした `Dirty` とする。
`CleanRiskAccepted` はdirtyを解除したclean系状態だが、送信本文のlive反映を確認したユーザーが
durabilityリスクを明示承認した場合に限る。これは「通常保存は200でのみclean」の唯一の例外であり、
GET一致やtimeoutだけで自動遷移しない。どちらもリスク承認済みであることをセッション中に表示し、
保存中の入力を復旧結果で誤ってcleanにしない。

PUTと復旧GETはクライアント側で`AbortController`による15秒のタイムアウトを持ち、guardの
破棄待ちやモーダルが無期限に停止しないことを保証する。PUTの打ち切りは応答喪失と同じ扱いで、
Agentがrenameを確定済みの可能性を否定できないため通常の保存失敗にせず`SaveStateUnknown`へ
遷移する（200のbody読取中の打ち切りも同様）。2xx応答のbodyがタイムアウト以外の理由
（不正JSON・途中切断・空本文）で読めない場合も同じく応答喪失であり、通常の保存失敗へ
分類してはならない。確定失敗として扱えるのは非2xxステータスを受信できた場合だけである。
復旧GETの打ち切りは取得不能（unavailable）として扱い、破棄は失敗してdirtyとguardを維持した
まま操作可能な状態へ戻る。タイムアウトによる自動clean・自動再送は行わない。

### 1.6 409競合の解決状態

409を受けたとき、クライアントはGETが `editable:true` で返したremote本文・remote revision・取得時刻を
`ConflictSnapshot`として別状態に保持し、現在のdirty bufferを上書きしない。解決が完了するまで
`baseDiskRevision`はPUT送信時の旧値のままとし、remote revisionだけをbaseへ差し替えてmineを
そのまま保存する操作は提供しない（実質的なforce overwriteになるため）。

競合UIの最小状態遷移は次のとおり。

| 操作 | 遷移 | 本文とbase |
|---|---|---|
| remoteを採用 | `Conflict` → `Clean` | remote本文をbufferへ置き、remote revisionを`baseDiskRevision`にしてclean。 |
| mineを破棄 | `Conflict` → `Closed`または`Clean` | dirty bufferを破棄し、保存せずremoteを表示/閉じる。remoteがdiskの正本。 |
| remoteをbaseに手動マージ | `Conflict` → `Dirty` | remoteを別スナップショットとして表示し、ユーザーが作ったmerged bufferを保持。baseはremote revision、保存は次の明示操作でのみ行う。 |
| キャンセル | `Conflict` → `Conflict` | mineとremoteを変更せず、解決操作を延期。 |

手動マージでremoteをbaseにするのは、remote本文を表示してユーザーが明示的に採用した後だけとする。
自動rebase、remote revisionだけの更新、mineの暗黙上書きは行わない。Phase 2でこのstate machine、
mine/remoteの差分表示、3つの解決操作、キャンセル、競合後の保存を実装・テストする。

409後のGETまでに対象が削除・巨大化・バイナリ化・CRLF化・symlink化するなどして、404、安全境界
エラー、`editable:false`、または取得失敗になった場合は `ConflictSnapshot`を捏造せず、
`ConflictRemoteUnavailable`へ遷移する。この状態でもmineとPUT送信時の旧`baseDiskRevision`を保持し、
保存は許可しない。

| 操作 | 遷移 | 本文とbase |
|---|---|---|
| remoteを再取得 | 編集可能なら`Conflict`、取得不能なら`ConflictRemoteUnavailable` | 成功時だけ取得本文・revision・取得時刻から`ConflictSnapshot`を作る。mineは変更しない。 |
| mineをコピー | `ConflictRemoteUnavailable`のまま | mineをクリップボードへコピーし、bufferとbaseは変更しない。 |
| 閉じる | `ConflictRemoteUnavailable` → `Closed` | 明示確認後にmineを破棄する。通常のdirty guardを迂回しない。 |
| キャンセル | `ConflictRemoteUnavailable`のまま | mineと旧baseを変更せず、解決を延期する。 |

### 1.7 バッファ不変条件

初期GETだけでなく、ユーザー入力、IME、paste、undo/redo、remote採用、手動merge、AI replacementの
全transactionへ同じvalidatorを適用する。transaction適用前の候補本文が次を満たさない場合は適用を
拒否し、現在bufferを保持してUIへ理由を通知する。

- UTF-8へエンコードした結果が2 MiB以下。
- CR（`0x0d`）、NUL、unpaired surrogateを含まない。CRLFをLFへ黙って変換する仕様は採用しない。
- `baseDiskRevision`、`bufferRevision`、AIの`baseRevision`/`sourceRevision`の全てが、実行時に
  `^sha256:[0-9a-f]{64}$` と照合される。hashは対応するraw UTF-8 bytesから再計算する。

2 MiBを超える入力は「保存不能なdirty」を作らず、入力transaction自体を拒否する。AI replacementも
replacement単体と適用後全文の両方を同じvalidatorで検査する。これはPUT側のvalidationとは別に、
CodeMirror documentとAI適用境界で先に実行するPhase 2/4の共通契約である。

### 1.8 編集面の選択操作と行ジャンプ

読み取り専用のCodeViewが持つ2つの操作は、編集面（CodeMirror 6）でも同じ機能として提供する。
読み取り専用のsource面をモードとして併存させる方式は採らない（§1.1）。編集面がsource面の
機能を包含することが、モードを3つに保てる前提である。

- **選択→送る:** 選択範囲の行番号はDOMの `data-ln` 属性ではなくCodeMirrorのdocumentから求める。
  引用文字列も**documentから切り出す**。CodeMirrorは行を仮想化するため、画面外へ伸びた選択を
  DOMのSelectionから読むと引用が途中で欠落する。読み取り専用のCodeViewは全行をDOMに持つため
  この問題が無かったので、移植時に同じ実装を流用してはならない。実測（5000行の文書、800×400の
  ペインで1〜4000行を選択）では、描画されている行は37行だけで、`window.getSelection()` は
  1,287文字/37行しか返さない。documentから切り出せば146,892文字/4,000行が揃う。
- **行ジャンプ:** `targetLine` はdocument上の位置へ変換し、スクロールと一時的な行ハイライトで
  示す。現行のCodeViewが `targetColumn` を使っていないため、v1も行単位で揃える。エディタの
  生成完了前に `targetLine` が与えられた場合と、生成後に変化した場合の双方を扱う。
- `split` では選択の捕捉面が編集側とプレビュー側の2つになる。ピルは一度に1つだけ表示し、
  片方で選択が起きたらもう片方の捕捉状態を解除する。
- モード切替でCodeMirrorをアンマウントしない。undo履歴と選択を保持したまま表示だけを切り替える。
- ペイン内検索（Console側のDOM走査）は仮想化された編集面には及ばず、編集面ではCodeMirror内蔵の
  検索を使う。これはPhase 2からの既知の差であり、Phase 3で変わるものではない。

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
- current fileの判定はstatだけで決めず、GET/PUTとも最大2 MiB+1 bytesを実際に読み、超過を
  `too_large`（GETは`editable:false`）として扱う。外部writerで対象が読み取り中に成長しても、
  上限を超えて読み込まない。
- GETのsnapshot取得は最大2試行（初回＋再取得1回）とする。各試行で安全にopenした同一対象fdに
  `fstat-before → offset 0から2 MiB+1 bytesのbounded read → fstat-after` を行う。両fstat値が
  一致して2 MiB以下なら、read bytes数も同値の試行だけをfull responseに採用する。両fstat値が
  一致して2 MiB超なら、bounded readが2 MiB+1 bytesを観測した試行を`truncated:true`に採用する。
  その他の組み合わせは不安定とし、初回なら2回目を行い、2回目も不安定なら`read_failed`とする。
- fullまたはtruncatedとして採用した最終試行の `fstat-after` が返したファイルサイズを `size` に使う。
  bounded readの観測bytes数は `size` に使わないため、既存viewerの実ファイルサイズ表示と互換である。
- ここで「安定したsnapshot」とはsize/content-lengthが整合したfull responseだけを意味する。
  同じsizeのまま外部writerがin-place writeした場合の混在読み取りは検出できず、ファイルのある瞬間に
  対するsnapshot保証は持たない。これは同一API PUTのmutexに参加しない外部writerの制約である。
- ファイルの新規作成、ディレクトリ作成、rename、delete は本APIの責務ではない。既存の fs 操作を
  使用する別フローとし、PUT は存在する通常ファイルの本文置換だけを行う。

### 2.3 パスとセキュリティ

- PUTの `path` は browse root 相対の非空canonical相対パスのみ。`/` で始まる絶対パス、`\\`、
  空component、`.`/`..` component、ドライブ形式（`^[A-Za-z]:`）、UNC形式、NUL byteを拒否する。
  GET/downloadはこれに加えて `allowedReadRoots` 配下のcanonical絶対pathを許可するが、許可root外は
  拒否する。pathを `Clean`して許可することで別名を作る方式は採らない。
- 既存 fs の denylist（`.claude`、`.claude.json`、`.config/agent-fleet`、`.ssh`、
  `.git-credentials`、`.codex`、`.gemini`、`.copilot`、`.cursor`、`.config/cursor`、
  `.kiro`、`.local/share/opencode`、`.local/share/kiro-cli`、`.aws` など）を一覧・GET・PUT
  すべてで共通適用する。denylist の正本は `workspace/agent/fs.go` とし、追加時は書込み面にも
  同時反映する。
- 対象ファイル自身、親ディレクトリ、パス中のいずれかのcomponentがsymlinkの場合は拒否する。
  symlinkを解決して許可root内かを判定する方式は採らない。判定は §1.4 のroot/parent fd固定と
  `openat2`/`fstatat(AT_SYMLINK_NOFOLLOW)`でrequest-controlled pathのTOCTOUを抑止する。scratch
  rootとrole-scoped docs mountは読み取り専用で、GET/downloadの絶対pathとしてのみ許可する。
- PUT は既存のファイルを置換するだけで、親ディレクトリを作成しない。対象が無い、ディレクトリ、
  特殊ファイルの場合は保存しない。
- path全体は4096 bytes以下、各componentは255 bytes以下（LinuxのPATH_MAX/NAME_MAX基準）とする。
  GET/downloadの絶対pathも同じ入力上限で検証する。current fileはstat値を信頼せず、2 MiB+1 bytes
  までしか読む。symlink差替え以外に、同一uidの外部processがroot/parent directoryをnamespace外へ
  renameする操作、hardlinkでdenylist inodeを許可名から参照する操作はv1の非協調writer脅威モデル外である。
  denylistはrequest pathの名前とsymlink解決を制御するもので、inodeの出自まで保証しない。

## 3. 保存API契約

### 3.1 ルートと共通事項

| 面 | ルート | 備考 |
|---|---|---|
| Console公開面 | `GET /api/fs/file` / `PUT /api/fs/file` | CP の認証・membership・workspace running gate を通る。 |
| Agent内部面 | `GET /fs/file` / `PUT /fs/file` | CP が `/api` を剥がして中継する。Agent 外部公開はしない。 |

既存 GET は query parameter `path` を使う。GET/downloadの `path` はbrowse-root相対、または
`allowedReadRoots`配下のcanonical絶対pathを許可する。PUTはJSON body内のbrowse-root相対pathだけを
許可する。Console/Agentが送るcanonical
Content-Typeは `application/json` とする。サーバーは互換性のため `application/json; charset=utf-8`
も受け付けるが、他のmedia typeは415とする。認証、テナント、Workspace 状態の共通
エラーは [docs/dev/05-api-contracts.md](../dev/05-api-contracts.md) の横断規約に従う。

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

- `size` は、§2.2の最終試行で安全にopenした対象fdに対する `fstat-after` が返した
  raw byteサイズである。bounded readの観測bytes数ではなく、truncated時も実ファイルサイズを返す。
  full responseでは両fstat値および `content` のraw byte数と一致する。
- `path` はcanonical display pathであり、browse root配下の絶対入力は相対pathへ変換される。scratch/docs
  の絶対入力はread-only rootの絶対pathとして返る。
- `content` はファイル全体をJSON文字列として返す。改行等はJSONの通常のescapeだけを行う。
- `editable: true` は、書込み可能root内、通常ファイル、symlinkなし、UTF-8、NULなし、CRなし、
  2 MiB以下というPUT条件を全て満たすことを表す。`editabilityReason` はそのとき `null`。
- `revision` は `editable: true` のときだけ返す。`content` をUTF-8バイトへ戻したraw bytesの
  SHA-256で、編集開始時の `baseDiskRevision` として保持する。
- `editable: false` の理由は安定値 `binary`、`invalid_utf8`、`too_large`、`read_only_root`、
  `unsupported_newline` のいずれか。binary/truncated/読み取り専用rootではrevisionを返さない。
- scratch/docsの絶対pathは `read_only_root` であり、GET/downloadのread-only用途に限る。PUTの
  `path`へ同じ絶対文字列を渡しても `bad_path` とする。symlink/denylist/許可root外は
  `editable:false` ではなく安全境界エラーとする。
- browse root配下の絶対pathはGET応答の `path` を相対pathへ変換するため、利用者がその応答pathを
  PUTへ渡せる。絶対入力文字列をそのままPUTへ再利用することはできない。
- symlink、denylist、traversalなど安全境界違反は `editable:false` へ丸めず、GET/downloadとも
  対応するHTTPエラーを返す。GETでバイナリを403に変更することはしない。
- browse root配下のGETは、PUTと同じpath単位mutex（キーはcanonical相対path文字列。§2.3で
  別名表記は拒否済み）を取得してから読み取る。クライアントがPUTをタイムアウトで打ち切っても
  Agent側のatomic writeは継続するため、mutexを共有しない読み取りは「rename確定直前の旧base」を
  復旧GETへ返し、クライアントがその旧baseへdiscardした直後にrenameが完了してmodel/diskが
  不一致になる。GETは進行中PUTのrename確定（またはエラー確定）を待ってから読む。この待機中に
  クライアント側のGETタイムアウトが先に切れた場合は通常の取得不能と同じ扱いで、
  `SaveStateUnknown`/`ConflictRemoteUnavailable` を維持しdiscardは失敗する。read-only rootの
  GETはPUT対象外でありmutexを取得しない（相対表記が偶然一致する別ファイルとの誤競合を避ける）。
- mutexだけでは「PUT goroutineがmutex取得前に停止している間にクライアントがタイムアウトし、
  復旧GETが先にmutexを取って旧baseを読み、その後に再開したPUTが旧baseとのCASに成功する」
  順序を防げない。PUTはmutex取得直後にrequest contextのキャンセルを検査し、既に打ち切られて
  いればdiskを変更せず499 `write_cancelled` で終了する。これで打ち切りの境界は二値になる:
  mutex取得前のキャンセル→書き込まない（この検査で中止）、取得後のキャンセル→rename/エラー
  確定まで完走し後続GETがその結果を待つ。書込み途中でのキャンセル検査は行わない。499応答は
  離脱済みクライアントには届かないのが通常で、目的はdisk無変更の保証である。
- **既知の限界（v1で対応を見送る）:** 上記のmutex＋context検査でAgentプロセス内の競合は閉じて
  いるが、CPはブラウザのrequest contextをそのままAgentへ中継し、PUTと復旧GETは別々の
  CP→Agent HTTPリクエストであるため、キャンセルの**伝播順序**は保証されない。次の極めて狭い
  窓が残る: (1) PUT goroutineがAgent側mutex取得前で停止している間にクライアントの15秒
  タイムアウトがabortし復旧GETを開始する。(2) 復旧GETが旧PUTへのcancel伝播より先にAgentへ
  到着し、mutexを取って旧baseを読む。(3) その後PUT goroutineが再開し、Agent側でまだcancelが
  観測されていないcontextのまま検査を通過してrenameし、GETが返した旧baseを無効化する。発生には
  goroutineスケジューリング遅延とネットワークタイミングの両方が重なる必要があり、実運用上の
  発生確率は極めて低いと判断してv1では対応しない。解消にはCP側アーキテクチャの変更が必要で、
  path単位の進行中PUTをCPが追跡して対象PUTの確定までGETを遅延させるか、保存operation IDを
  導入してGETがPUT確定を待ち合わせる仕組みが将来の改善候補である。

#### メタデータのみの応答（`meta=1`）

外部変更の検知（§7）のために、GETは本文を返さないメタデータ応答を持つ。新しいルートは作らず、
既存の `GET /fs/file` に `meta=1` を加える。CPの中継はクエリをそのまま透過するため、CP側の
ルート追加は不要である。

```json
{
  "path": "repos/example/README.md",
  "size": 1842,
  "binary": false,
  "truncated": false,
  "editable": true,
  "editabilityReason": null,
  "revision": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
```

- 応答は通常のGETから `content` を除いたものとする。各fieldの意味、`editable` の判定順序、
  `revision` を返す条件、エラー契約はすべて通常のGETと同一で、`meta=1` 専用の分岐は設けない。
- path解決、denylist、symlink拒否、許可rootの選択、browse-root配下でのpath mutex取得も通常の
  GETと同じ経路を通る。進行中のPUTがあれば同じくその確定を待つ。
- `meta=1` は監視専用であり、`SaveStateUnknown` / `Conflict` の復旧GETの代替にはしない。復旧は
  §1.5・§1.6のとおり本文を伴うGETで行う。
- `editable:false` のファイルは `revision` を持たないため、`meta=1` でも本文が変化したかを
  確定できない。§7の自動追従の対象は `editable:true` に限る。

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
| `content` | string | yes | 保存後のファイル全体。Unicode scalar valueからなるUTF-8、NUL/CRなし、LF-only、2 MiB以下。空文字列を許可。 |
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
revision がリクエスト本文のハッシュと一致することをクライアントが確認できる。通常保存のdirtyはこの
200応答を受けて初めて解除する。§1.5のユーザーによるdurabilityリスク承認だけが例外である。

#### Atomic write と revision 検査

成功処理は次の順序を守る。

1. HTTP envelope、request本文、PUT相対pathを検証し、pathの字句検証からcanonical相対pathのmutex keyを
   確定する。この段階では対象や親directoryをopenせず、current本文も読まない。
2. keyのmutexを取得する。以降の全手順と早期エラーの結果が確定するまで保持する。
3. mutex保持中にroot/parent fdから対象を解決し、denylist・symlink・通常ファイル条件をfd-relativeに
   検証してopenする。current本文を最大2 MiB+1 bytesだけ読み、size、UTF-8、NUL、CRを検証する。
4. 検証済みcurrent本文のrevisionと `baseDiskRevision` を比較する。不一致なら旧ファイルを変更せず409。
5. 同じdirectoryの一時ファイルへ新本文を書き、既存 `mode.Perm()` を `fchmod(temp, oldMode.Perm())`
   で設定してから `fsync(temp)` する。fchmodはtemp fsyncより前に行う。
6. 一時ファイルを対象名へrenameする。renameが成功した時点で、現在のnamespace上の対象は新本文。
7. 親directoryをfsyncする。成否から200または`write_state_unknown`を確定した後にmutexを解放する。
   fsyncが成功したときだけ耐久性まで含む成功として新revisionを返す。

同一APIの後続PUTは先行PUTの親directory fsync結果が確定してmutexが解放された後にcurrent本文を
open/readするため、先行PUTが保存したrevisionを観測し、旧baseなら409になる。ただし、この排他は
同一APIの保存同士に限られ、外部writerは保護しない。renameは同一ファイルシステム内で行う。

rename前の一時書込み/fchmod/fsync失敗では、旧ファイルを変更せず一時ファイルを片付け、500
`write_failed`を返す。rename後の親directory fsync失敗では、**現在のnamespace上は新本文**だが、
クラッシュ後もrenameが永続するかが不明である。500 `write_state_unknown`を返し、クライアントは
`SaveStateUnknown`へ遷移してGETでlive revisionを照合する。GET一致だけではdurabilityを確定できず、
通常の競合としても自動再送してはならない。

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
| 400 | `bad_path` | 空、絶対、traversal、NUL、Windows形式、長さ超過、またはcanonical相対パス規則違反 | 変更なし |
| 400 | `symlink_not_allowed` | 対象または親componentにsymlinkがある。文字列/LstatだけではTOCTOUを防げないため、fd-relative操作で拒否する | 変更なし |
| 400 | `bad_request` | wire bodyのUTF-8不正、JSON不正、lone high/low surrogate escape、必須フィールド欠落、型違い、未知フィールド、revision形式違い | 変更なし |
| 415 | `unsupported_media_type` | Content-Typeが `application/json`（charset=utf-8含む）ではない | 変更なし |
| 403 | `denied` | denylist、書込み不可root、権限・認可境界に該当 | 変更なし |
| 404 | `not_file` | 対象が存在しない、ディレクトリ、通常ファイルでない | 変更なし |
| 409 | `revision_conflict` | 比較時点で現在のrevisionが `baseDiskRevision` と一致しない | 変更なし |
| 413 | `too_large` | current fileまたはdecoded contentが2 MiB超、またはHTTP body全体が16 MiB超 | 変更なし |
| 415 | `binary_not_supported` | current fileにUTF-8不正かNUL byteがある、またはdecoded request `content`にNULがある | 変更なし |
| 415 | `unsupported_newline` | current fileまたはdecoded request `content`にCRLF、CR単独、混在改行がある | 変更なし |
| 499 | `write_cancelled` | mutex取得時点でrequest contextが既にキャンセル済み（クライアントのタイムアウト/切断） | 変更なし。応答は離脱済みクライアントに届かないのが通常で、Console i18n登録は不要 |
| 500 | `read_failed` | 現在本文の読み込みに失敗 | 変更なし（保証できない場合は保存処理を開始しない） |
| 500 | `write_failed` | rename前の一時書込み、fsync等に失敗 | 旧本文を保持 |
| 500 | `write_state_unknown` | rename後の親directory fsync等に失敗し、live namespaceは新だがdurabilityが不明 | `SaveStateUnknown`でmineを保持 |

クライアントは競合時に自動再送せず、GETで現在本文を取得してユーザーに差分確認を求める。
`symlink_not_allowed`を400にするのは、認証済み利用者の権限不足ではなく、要求されたpath形式と
安全な解決条件が不正だからである。denylist/readonly rootを403にするのは、path形式は妥当でも
その対象への操作権限を明示的に拒否するためである。Workspace stopped/starting、未認証、membership外
などの共通エラーは、この表のPUT固有コードより優先される既存の横断契約（通常は401/403/409）を使う。

PUT固有エラーは、requestのContent-Type/body上限を検査し、wire bodyがUTF-8不正、JSONとして不正、
またはJSON文字列にlone high/low surrogate escapeを含む場合はdecoded `content`の検査前に
400 `bad_request` とする。decode後はfield、decoded `content`、path字句検証を確定し、mutex取得後に
fd-relativeな安全境界・対象種別・read結果を判定する。
decoded request `content`は `too_large`、`binary_not_supported`（NUL）、`unsupported_newline`（CRを含む）の順に
判定する。current本文は `too_large`、`binary_not_supported`（NULまたはUTF-8不正）、
`unsupported_newline`（CRを含む）の順に
編集可否を判定し、いずれかに該当すれば `baseDiskRevision`が不一致でもそのエラーを返す。
`revision_conflict`はcurrent本文が編集条件を全て満たしrevisionを計算できた後にだけ判定する。

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
- hard limit内のwire body全体が厳密なUTF-8であることをJSON decode前に検査し、不正UTF-8は
  400 `bad_request` とする。wire bodyを不正UTF-8のままdecoded `content` として扱わない。
- Go `encoding/json`等が不正なsurrogateをU+FFFDへ置換する前に、全JSON string tokenのescapeを
  JSON構文として検証する。high surrogate（`\uD800`〜`\uDBFF`）は直後のlow surrogate
  （`\uDC00`〜`\uDFFF`）と正しいpairを作る場合だけ許可し、lone high surrogateとlone low surrogateは
  400 `bad_request` とする。正しいpairは対応するUnicode scalar valueへdecodeする。raw JSON上の
  escaped backslashに続く文字列（例: `"\\ud800"`）はsurrogate escapeではないため拒否しない。
- 実際のU+FFFDは有効なUnicode scalar valueである。UTF-8で直接表現されたU+FFFDとJSON escape
  `\uFFFD`はどちらも許可し、lone surrogateを置換した結果のU+FFFDとは区別する。
- JSON objectを1個だけ受け付け、decode後に末尾のwhitespace以外がないことを検査する。第2 JSON値、
  trailing garbage、空bodyを拒否する。
- `DisallowUnknownFields`相当で未知フィールドを拒否し、required fieldと型を検証する。
- decoded `content` のUTF-8 byte数を2 MiB以下で検査し、NULは415 `binary_not_supported`、
  CRは415 `unsupported_newline` として保存前に拒否する。
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

wire envelope が必要な場合は version を持たせる。Phase 4 の実装は同期POST
（`POST /fs/suggest-edit`）を採り、wireへenvelopeを載せない。Consoleがリクエスト時に控えた
identityから応答受信時にenvelopeを合成し、本節の検証をそのまま適用する（§6 Phase 4）。

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
- `replacement` は §1.7 の共通buffer validator（CR/NUL/unpaired surrogate禁止、LF-only、サイズ上限、
  適用後hash）をtransaction適用前に通す。失敗時は提案を適用せず、理由をUIへ通知する。
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
  Markdownではこのbutton groupがトップレベル操作であり、非Markdownの `role="tablist"` による
  view/edit切替と同じペインに同時に出さない（§1.1）。preview rendererの切替（通常preview /
  slides）も同じ階層のbutton groupとし、`split` では右側のプレビューに作用する。
- Saveボタンはタッチ端末を含む全環境で提供し、Ctrl+S / Cmd+Sと同じsnapshot保存処理を呼ぶ。
  saving中は同一paneのSaveを無効化し、追加入力は可能だが別PUTを並行送信しない。
- dirty、saving、saved、risk accepted、revision conflict、conflict remote unavailable、write state unknownは
  `role="status"` と
  `aria-live="polite"` で状態変化を告知する。競合・状態不明は `role="alert"` 相当で明示し、
  再取得・差分確認・再保存・リスク承認・mineのコピー・破棄の該当する選択肢をfocus可能にする。
- Phase 2の受け入れでは、マウスなしのview/edit切替・保存・競合表示、タッチ端末のSave操作、
  focus移動、`aria-selected`/`aria-pressed`/announceを確認する。
- 外部変更のadvisory通知（§7）は `role="status"` / `aria-live="polite"` とする。`role="alert"`
  相当は従来どおり競合・状態不明にだけ用い、入力中の告知でfocusや読み上げを奪わない。
- 編集面へのfocus移動は「モード選択ごとの世代番号」を単位とする。編集面を表示する選択のたびに
  番号を進め、CodeMirrorのready後に一度だけ消費する。ready前の選択も取りこぼさない。ファイルを
  開いただけ・行引用で開いただけではfocusを移さない。
- **未消費のfocus要求は自分の選択より長生きさせない。** 次のいずれでも破棄する。(a) 編集面が
  表示されなくなった時点。(b) 要求を出したコミットで編集面が表示されていない場合（同一バッチ内で
  後続の選択に追い越された）。(c) ファイルが変わった時点。(d) **表示される面の組を変えない選択を
  行った時点**——preview rendererの切替や、すでに選ばれているモードの再選択がこれにあたる。
  (d)が必要なのは、rendererはfocusを移す対象ではない別のbutton groupであり、ready前に `split`
  を選んでからrendererを切り替えると、ready完了時に古い要求がrendererボタンからfocusを奪う
  ためである。

## 6. フェーズ受け入れ条件

### Phase 0（本変更）

- [ADR 0027](../decisions/0027-markdown-code-editor.md) と本書で、CodeMirror 6、File ペイン拡張、
  Markdown/Marpの3モード対応、未保存本文のメモリ限定、一般AIセッションと提案生成チャネルの
  権限差が明記されている。
- CAS保証を「比較時点までに観測した変更の検出 + 同一API保存の直列化」に限定し、外部writerとの
  raceを保証しないこと、字句検証後のcanonical relative pathをmutex keyにして対象open/read/hash前から
  parent fsync結果確定まで保持することが固定されている。
- PUTのbrowse-root相対pathと、GET/downloadのallowedReadRoots配下canonical絶対pathを分離し、
  root選択後fdから相対化する契約、Linux fd-relative操作、`openat2`/`fstatat`/`renameat`相当、
  GET/downloadのsymlink拒否、safeBrowsePathの不適格性、POSIX/Windows形式pathの判定が固定されている。
- LF-onlyの編集バッファ、raw byte revision、CodeMirror UTF-16 offsetの整合とCRLFの将来拡張条件が
  固定されている。
- rename後directory fsync失敗の `write_state_unknown`、live反映とdurabilityの区別、
  `SaveStateUnknown`からの再保存/リスク承認、通常保存の200-only cleanと明示リスク承認の
  例外、リスク承認時の`baseDiskRevision`更新、permission bitsのみ保持、owner/ACL/xattr非保証が
  固定されている。
- `baseDiskRevision`（保存API）と `baseRevision`/`bufferRevision`（AI/バッファ）を区別し、
  save snapshot、generation、応答喪失時の`SaveStateUnknown`とGET結果別遷移、dirty guard全経路が
  固定されている。
- 409後のConflictSnapshot、remote/mine/手動merge/cancelのstate machine、remote取得不能時にmineを
  保持する`ConflictRemoteUnavailable`、remote base更新とforce overwrite禁止が固定されている。
- typing/IME/paste/undo/redo/AI replacementの共通buffer validator、LF/NUL/unpaired surrogate/size
  invariant、revision regex検証が固定されている。
- GETの `editable`/`editabilityReason`、opened fdのfstatを使う`size`、最大2回のsnapshot取得と
  size/content-length整合の保証範囲、decoded content 2 MiB、HTTP body 16 MiB、strict JSON decoder、
  wireの不正UTF-8、lone surrogate escape、decoded contentのNUL/CRのエラー分類、正しいsurrogate pairと
  U+FFFDの許可、JSONエラーとplain-text gateway errorの境界、`suggestion_stale`のUIコードが
  固定されている。
- current file/path resource bound、`fs.file.put`のwrite_state_unknown監査、live namespaceとdurabilityを
  分けた説明、同一uidのnamespace mutator/hardlinkを脅威モデル外とする範囲が固定されている。
- Phase 1以降の実装コード・依存追加・保存API実装・UI実装は本Phase 0の作業に含めない。

### Phase 1（保存API基盤）

- AgentのPUT route/handler、CPのPUT route/proxy、CP proxyのPUTテストを追加する。
- `fs.file.put` を監査分類へ追加する。監査targetはJSON bodyの `path` だけを16 MiB上限内で安全に
  抽出し、`content`、replacement、token、差分本文を監査ログへ記録しない。body parse失敗時は監査
  targetを作らず、pathはcanonical lexical validation後だけtargetにし、不正値は固定の
  `<invalid-path>` にする。
- 監査対象は2xxだけに限定しない。Agentが `500 write_state_unknown` を返した場合も、CPは小さい
  JSON error bodyを監査判定前にbounded readできる構造にし、`action: fs.file.put`、canonical path、
  `detail/outcome: write_state_unknown`、`http_status: 500` を記録する。通常の他の失敗は既存方針に
  従うが、live namespaceは新本文でdurabilityが不明となるこの結果だけは欠落させない。
- `httpx.DecodeJSON`相当のstrict decoder、Content-Type、body/decoded size、trailing value、wireの厳密UTF-8、
  surrogate pair、unknown field検査を実装する。server error codeはAgent/CP定数とConsoleの英日i18nへ
  同時登録する。lone high/low surrogateの拒否、正しいpair、literal/escaped U+FFFD、escaped
  backslash文字列をstrict decoderのGo単体テストへ追加する。
- fd-relative path操作とGET/file/downloadのsymlink拒否を実装し、safeBrowsePath系helperをPUTへ流用しない。
- GETのeditable判定、LF-only、revision、opened fdのfstatによるsize、CASの同一API直列化を実装する。
- path全体4096 bytes、component255 bytes、current fileの2 MiB+1 read boundを実装する。GETは各回に
  fstat-before/read/fstat-afterを行う最大2試行とし、size/content-lengthの不一致が続けば
  `read_failed`にする。同サイズの外部in-place writeは瞬間snapshot保証の対象外とする。
- symlink、path alias、外部writerのrace、GET読み取りrace、revision conflict、rename前失敗、
  同一APIの並行PUT、rename後directory fsync失敗をfailure injectionを含むGo単体テストで検証する。
  同一APIの並行PUTは一方のopen/readが先行PUTの結果確定後になることを確認する。外部writer raceは
  「保証しない範囲」を再現し、誤って保証を主張しないこともテスト文書に残す。

### Phase 2（Text/コード編集MVP）

- CodeMirror導入、view/edit、dirty、single-flight PUT、送信snapshot/generation、応答時dirty判定、
  `baseDiskRevision`更新、`SaveStateUnknown`とGET結果別の復旧、通常保存の200-only clean、
  明示再保存/リスク承認に限定した `CleanRiskAccepted`、リスク承認時の観測revisionによるbase更新を
  実装する。
- 全buffer input transaction（typing/IME/paste/undo/redo）にLF/NUL/unpaired surrogate/2 MiB validatorを
  適用し、失敗時はtransactionを適用せず通知する。
- 409競合のConflictSnapshotとstate machine（remote採用、mine破棄、remoteをbaseにした手動merge、
  cancel）、`ConflictRemoteUnavailable`（再取得、mineコピー、明示close）、mine/remote差分、競合後
  保存を実装・テストする。remote revisionだけを更新してmineをforce overwriteする経路は作らない。
- dirty registry/navigation guardをlayout commit前に接続し、openActive/openInNew再利用、history、
  tenant/reset、reader、popout、reload、logout、version update、workspace lifecycleを網羅する。
  layout storageへ本文を入れない。version updateのterminal例外をeditorへ流用しない。
- beforeunload、ARIA、focus、status announce、タッチ向けSaveボタンを実装・テストする。

### Phase 3（Markdown編集）

**実装完了（2026-07-28）。** Console単独で完結し、Agent/CPの変更は無い。既存のGET/PUTは拡張子の
許可リストを持たないため（§2.1）、`.md` は最初から `editable:true` で返っていた。実装は
`fileMode.ts`（モード状態機械）、`selection.ts` / `selectionPill.ts`（編集面の選択と行ジャンプ）、
`useDebounced.ts`（プレビューとMarp判定の debounce）、FileViewの配線、`viewer.css` のsplitレイアウト
からなる。読み取り専用のレビュー担当セッションとのペアで8ラウンド・14件の指摘に対応した（うち1件は
コードではなく実行方法の問題で、AGENTS.mdへ注記した）。

テストは vitest を node/dom の2プロジェクトへ分離して追加した（`console/vite.config.js`）。jsdom
環境の構築はテストファイル1本あたり約1.3秒かかり、全体をjsdomにすると10.8秒→51.3秒になる実測が
あるため、既定はnodeのままとし、コンポーネントを実際にマウントするテストだけ `*.dom.test.tsx` で
opt-inする。Vitest 4はper-fileの `@vitest-environment` docblockを廃止しているため、分離手段は
projectsに限られる。レビュー指摘に対する回帰テストは、修正を戻すと落ちることを個別に確認した。

jsdomにはレイアウトが無いため、送るピルの座標・行ジャンプのスクロール着地は実ブラウザで別途
検証した（5000行の文書、800×400のペイン）。§1.8に実測値を記載している。

- edit/preview/splitの既存MarkdownView/MarpView/Mermaid遅延ロード再利用を実装する。
- Markdownではトップレベルを3モードへ一本化し、ペインの `mode` をそこから導出する（§1.1）。
  編集できないMarkdownは現行のpreview/source/slidesへフォールバックする。
- 編集面に選択→送るピルと行ジャンプを実装する（§1.8）。行番号と引用文字列をCodeMirrorの
  documentから求め、画面外へ伸びた選択でも引用が欠落しないことをテストする。
- 現行source/preview/slidesとの対応、Marpの通常preview/slide renderer、split中のrenderer切替、
  LF-only、dirty guardを回帰テストする。
- プレビューとMarp判定のデバウンス、モード切替でCodeMirrorをアンマウントせずundo履歴と選択を
  保持することを確認する。
- Markdown編集に必要なCodeMirrorの言語拡張は動的importとし、初期バンドルへ含めない。
- 外部変更の追従（Phase 3.5）はこのフェーズに含めない。

### Phase 3.5（外部変更追従）

**実装完了（2026-07-28）。** 設計方針は §7。Agent は既存 `GET /fs/file` に `meta=1` を追加した
（`fs_file_api.go`。応答から `content` を除くだけの後処理であり、path解決・denylist・symlink
拒否・path mutex 待ち・editable判定順序・エラー契約は通常GETと完全共有）。CPのルート追加は無い。
Console は `features/editor/probe.ts`（§7.2のゲート判定と即時トリガ＋12秒間隔のフック）、
`api.ts` の `probeFileMeta`（例外を投げず全失敗を `unavailable` に畳むadvisory GET）、model の
`externalObservation` / `followEpoch` / `observeExternal` / `followExternal`、`useFileEditor` の
`applyProbeResult` / `confirmExternalChange`、`CodeEditor` の `followDocument`（EditorState を
作り直して undo 履歴を残さず、カーソル・スクロールを行番号ベースで復元）、FileView の配線
（編集バッファを持たない view ペインは取得本文で data を差し替え、選択中モードを維持）からなる。
undo 不能と行復元は jsdom に加えて実 Chromium で確認した（500行文書・カーソル行200→200、
スクロール誤差は1行以内）。

実装時の受け入れ条件は次のとおり（すべて検証済み）。

- Agentへ `meta=1` のメタデータ応答を実装し、通常のGETと同じ安全境界・判定順序・エラー契約を
  共有することをGo単体テストで確認する。新規ルートとCPのルート追加は行わない。
- Consoleのプローブを実装する。可視時のみ動き、`saving` 中は停止し、Workspaceが `running` の
  ときだけ実行することをテストする。
- dirtyのときはadvisory通知に留め、プローブが `phase` を遷移させないことをテストで保証する。
- cleanの自動追従で、undo履歴に旧本文が残らないこと（Ctrl+Zで外部変更を巻き戻して保存できない
  こと）と、自分の保存を外部変更として誤検知しないことをテストする。
- 読み取り専用のviewペインが同じプローブで追従することを確認する。

### Phase 4（AI変更提案）

**実装完了（2026-07-28）。** Phase 0 で未確定だった点は次のとおり確定した。

- **リクエストUX = 選択範囲＋指示文。** range はエディタの選択（UTF-16 offset）から確定し、
  LLMにoffsetを計算させない。選択が空のときは全文を range とする。LLMが返すのは
  `summary` と `replacement` だけである。
- **transport = 同期POST。** Agent `POST /fs/suggest-edit`（CPの許可リストへ
  `POST /api/fs/suggest-edit` を追加）。本文はディスクではなく編集バッファのスナップショット
  （選択と前後各16 KiBの文脈、選択は256 KiB上限）をリクエストで運ぶため、dirtyな本文への
  提案が自然に成り立ち、Agentはfsを一切読まない。§4.1のenvelopeはwireに載せず、Consoleが
  リクエスト時に控えた `paneId`/`filePath`/`requestId`/`sourceRevision` から応答受信時に合成し、
  §4.2の検証（identity・revision三重一致・range・validator）を受信時と適用時の両方で通す。
  応答の追い越し・ファイル切替後の遅延応答はrequestIdで棄却する。
- **生成チャネル = `oneShotHeadless` の再利用。** タイトル/返信サジェストと同じ
  backend-agnostic一発ヘッドレスで、claude `--tools ""`・codex tool無付与・cursor `--mode ask`
  は既にread-onlyだった。唯一の穴だったopencode（素のchatWorkdirにはproject configが無く
  ユーザーのglobal設定を継承）は、`OPENCODE_CONFIG` へ `{"edit":"deny","bash":"deny"}` の
  静的ポリシーファイルを渡して閉じた（併合優先度 project > OPENCODE_CONFIG > global の実測に
  基づく）。モデル既定は生成品質を考慮し `AF_EDIT_SUGGEST_MODEL`（既定 sonnet、claude backend
  のみ）。usage台帳へ `suggest.edit` として記録する。
- **staleは保存せず導出する。** 提案は `externalObservation` と同型の advisory
  （`model.suggestion`）として `phase` の横に置き、受信後の入力は
  `suggestion.baseRevision !== bufferRevision` から stale を導出してパネル表示と適用可否に
  反映する。適用は `suggestion_stale` を含む安定UI codeで棄却する（i18n登録済み）。
- **適用はCodeMirrorの範囲transaction。** `CodeEditorHandle.applyEdit` が1回のundoable
  transactionとして流し、共通transactionフィルタ（validator）を通る。CR/CRLFはCodeMirrorが
  dispatch時にLFへ正規化してフィルタに届かないため、改行契約は適用境界の `checkSuggestion` が
  dispatch前に弾く。編集面が無い場合は model 側適用＋content同期で代替する。
- **レビューUI** は競合パネルと同じオーバーレイに、`DiffView` の LCS `lineDiff`（export化）で
  選択範囲→置換文の差分を描く。エラーではないため `role="alert"` にしない。

実装: Agent `fs_suggest_edit.go`（strict decoder・フェンス/散文混じり応答からのJSON抽出・
summary 240 bytes整形・CR/NUL拒否）、Console `editor/suggest.ts`（型と`checkSuggestion`・
文脈切り出し）、`model.ts` の `setSuggestion`/`applySuggestion`、`useFileEditor` の
request/cancel/accept/reject、FileViewの`EditorSuggestPanel`。テストはGo単体（検証・抽出・
handler・ルート両側）、Console node（suggest/model/useFileEditor — accept が `putFile` を
呼ばないことを含む）、dom（範囲適用がundo 1ステップ・フィルタ棄却）、実Chromiumでの
一連のフロー目視（compose→生成→diffレビュー→適用→dirty→undo復元→stale導出）。

元の受け入れ条件（すべて満たす）:

- 書込みtoolを持たないread-only allowlist提案チャネルを実装し、一般のWrite/Edit/Bash可能な
  セッションをこの経路に接続しない。
- `paneId`/`filePath`/`requestId`/`sourceRevision` identityとbuffer revision検査を実装し、
  stale proposalを拒否する。
- accept handlerがPUTを呼ばず、replacement適用前に共通buffer validatorを通し、メモリbufferだけを
  dirty化することをテストで保証する。

### Phase 5（高度な支援）

- 複数候補、hunk単位accept、セッション連携、補完、CRLF/改行マッピング対応は別ADRまたは本ADRの
  改訂で設計してから着手する。

## 7. 外部変更の追従（Phase 3.5 設計方針）

> 本節はPhase 3.5の方針を固定するものである。実装は2026-07-28に完了した（§6 Phase 3.5）。

### 7.1 前提

Workspaceのファイルは、Console以外の書き手が日常的に変更する。コーディングエージェント
（Claude/Codex等）のWrite/Edit、shell、git checkout、その他のプロセスがそれにあたる。

現在のConsole/Agentにはこれを検知する仕組みがない。ファイルペインは開いた時に一度GETするだけで
以後は再取得せず、ファイルツリーとgit変更一覧も手動リフレッシュである。Agentはfsnotify等の
ファイル監視を持たず、CPのpushストリームにもfs系のstreamはない。したがって現状の外部変更検知は
**保存時のCAS（`409 revision_conflict`）による事後検知だけ**である。

Phase 3.5の目的は、この事後検知に能動的な検知を足すことである。CASを置き換えるものではなく、
保存の正しさの根拠は引き続き§1.2のCASにある。プローブはあくまで早期警告であり、保証を増やさない。

### 7.2 検知方式

`meta=1` のメタデータGET（§3.2）をポーリングする。全文GETのポーリングは、開いているペインごとに
最大2 MiBを繰り返し転送するため採らない。

プローブは次を全て満たすときだけ実行する。

- Workspaceが `running` である。
- タブが前面（`document.hidden` でない）かつ対象ペインが可視である。
- 対象ファイルを `editable:true` として取得済みである。
- `phase` が `saving` でない。進行中のPUTの結果は200応答で確定させ、プローブで先回りしない。

トリガは、`visibilitychange`・ウィンドウfocus・ペインのactivateによる即時プローブと、低頻度の
定期プローブ（10〜15秒程度。既存のセッションポーリング4秒より明確に緩く）を併用する。ブラウザへ
戻った瞬間に気付けることが実用上もっとも効くため、即時トリガを主、定期実行を補助とする。
プローブの失敗・タイムアウトは無音で扱い、次のトリガで再試行する。トーストは出さない。

比較対象は `baseDiskRevision` とする。保存が成功すると `baseDiskRevision` は保存した本文の
revisionへ更新されるため、自分の保存を外部変更として誤検知しない。

### 7.3 dirty（未保存バッファがある）ときの扱い

自動リロードは行わない。dirtyなmineを暗黙に上書きしない原則（§1.6）をそのまま適用する。

外部変更は `phase` とは独立したadvisoryな観測として保持する。**プローブは `phase` を遷移させて
はならない。** 「`Conflict` は409応答を受けたときにだけ発生する」という§1.6の不変条件を維持し、
Phase 2で固定した状態遷移とそのテストを変更せずに載せる。

- ステータス行へ外部変更を告知する。`role="status"` / `aria-live="polite"` とし、入力中に
  `role="alert"` へ化けさせない（§5）。
- 「差分を確認する」操作を提供する。実行時に本文を伴うGETを行い、成功すれば§1.6の
  `ConflictSnapshot` を作って通常の競合UI（remote採用・mine破棄・手動merge・cancel）を開く。
  取得できなければ `ConflictRemoteUnavailable` と同じ扱いにする。
- ユーザーが何もしなくても、保存すれば409となり§1.6の経路で解決できる。
- 観測したrevisionが現在の `bufferRevision` と一致する場合は競合ではない（外部の書き手が自分の
  未保存本文と同じ内容を書いた）。§1.5の `classifyUnknownRemote` と同型に分類し、競合とは別の
  文言で示す。

### 7.4 clean（未編集）のときの扱い

自動で最新化してよい。ただし次を守る。

- **undo履歴に旧本文を残さない。** 現在の編集バッファへ全文置換のtransactionを流す実装をそのまま
  使うと、Ctrl+Zで外部変更を巻き戻した本文を保存できてしまう。自動追従はエディタの状態を作り直す
  等、履歴に残さない方法で行う。
- カーソル位置とスクロール位置は行番号ベースで可能な範囲まで復元する。
- `clean_risk_accepted` も自動追従の対象とするが、追従後はリスク承認の表示を解除する。
- 読み取り専用のviewペインも同じプローブで追従する。編集バッファを持たないぶん扱いは単純で、
  取得した本文で表示を差し替える。

### 7.5 境界条件

| 観測 | 扱い |
|---|---|
| 404（削除・rename） | ペインを自動で閉じない。dirtyならmineを保持して告知し、cleanでも告知に留める。 |
| `editable:false` へ転落（CRLF化・巨大化・バイナリ化・read-only化） | 保存を試みる前に「保存できなくなった」ことを告知する。自動追従はしない。 |
| symlink化・denylist該当などの安全境界エラー | 取得不能として扱い告知のみ。mineと `baseDiskRevision` は変更しない。 |
| プローブ自体の失敗・タイムアウト・Workspace停止 | 無音で失敗させ、次のトリガで再試行する。 |

`editable:false` のファイルは `revision` を持たないため、本文が変化したかを確定できない。v1の
自動追従の対象は `editable:true` のファイルに限る。読み取り可能だが編集不可のファイル
（`unsupported_newline`、`invalid_utf8`、`read_only_root`）へ追従を広げるには、これらにも
raw byte revisionを返す契約拡張が必要であり、将来課題とする。

### 7.6 v1で採らない選択肢

- **Agentのfsnotify + pushストリーム:** リアルタイム性は高いが、監視レジストリ、CP経由の新しい
  streamの配線、コンテナFSでのinotify挙動とwatch上限の検討が必要で、v1の変更範囲を大きく超える。
  プローブでは不足だと確認された時点で別設計とする。
- **全文GETのポーリング:** 通信量が開いているペイン数に比例して増える。
- **dirtyなバッファへの自動マージ・自動リベース:** §1.6で採らないと決めた方針をそのまま適用する。
- **ファイルツリー・git変更一覧の自動追従:** 本節の対象は開いているファイルペインに限る。左ペインの
  自動更新は別の設計とする。
