# 0027. File ペインに CodeMirror 6 の編集モードを追加し、保存を明示操作に限定する

- 状態: **採用・Phase 4 まで実装済み**（2026-07-28）
- 詳細契約: [docs/44-markdown-code-editor.md](../log/44-markdown-code-editor.md)
- 関連: [docs/build/02-console.md](../build/02-console.md)（Console のペイン構成）/
  [docs/build/04-agent.md](../build/04-agent.md)（fs の境界と denylist）/
  [docs/build/05-api.md](../build/05-api.md)（API 中継の地図）/
  [decisions/0011](0011-console-rebuild.md)（Console リビルド）

## 背景

Console の File ペインは現在、Workspace のファイルを読み取り専用で表示する。
コードや Markdown を軽く修正する需要に対して、専用エディタペインを新設すると、既存の
ペイン復元・レイアウト・Markdown/Marp/Mermaid の描画資産が二重化する。また、AI が提案を
そのままディスクへ書き込む設計は、ユーザーの意図しない変更と競合時の復旧を難しくする。

## 決定

1. **エディタは CodeMirror 6 を採用する。** 現行の React + Vite + TypeScript へ組み込みやすく、
   Markdown/コードの編集に必要な拡張を段階的に追加できる。LSP が必須になる場合に限り、
   Monaco を将来再評価する。
2. **既存の `file` ペインを拡張する。** 新しいペイン種別は作らず、ファイルペインに
   `mode: "view" | "edit"` を持たせる。`view` は従来どおり読み取り専用、`edit` はタブ内の
   編集バッファを表示する。
3. **保存APIは `PUT /fs/file` とする。** Console の公開入口は `PUT /api/fs/file`、Workspace
   Agent 内部の実体は `PUT /fs/file` とし、CP は既存の fs 中継規約で転送する。
4. **保存は観測時点CAS＋同一API直列化とする。** ファイルの生バイトSHA-256をrevisionとし、
   保存APIの `baseDiskRevision` と比較時点のrevisionが一致する場合だけ保存する。一致しなければ
   `409 revision_conflict` とする。同一APIのPUTは、字句検証で確定したcanonical相対pathをmutex key
   とし、対象のfd安全検証・open・read・hashより前にmutexを取得する。親directoryのfsync結果を
   確定するまで保持して直列化する。shell、Claude/Codex、git checkout等の外部writerはこのmutexに
   参加しないため、比較後rename前の外部変更を防止・検出する保証は持たない。全writer協調ロックは
   採用しない。
5. **保存はatomic writeとし、rename後失敗を別状態にする。** 対象と同じディレクトリに一時ファイルを
   書き、fsyncしてからrenameし、親directoryもfsyncする。rename前の失敗は旧本文を保持して
   `write_failed`、rename後のdirectory fsync等の失敗は現在のlive namespaceでは新本文だが
   durabilityが不明な `write_state_unknown` とする。GET照合だけではdurabilityを確定できないため、
   クライアントはdirtyなmineを保持する `SaveStateUnknown` へ遷移し、明示的な再保存またはリスク承認を
   要求する。通常保存は200応答でのみcleanとし、送信本文のlive反映を確認した後の明示的な
   durabilityリスク承認だけを例外とする。リスク承認時は復旧GETで確認した送信本文のrevisionを
   `baseDiskRevision`へ設定してからclean/dirtyを分岐する。保持する属性は `mode.Perm()` のみで、
   owner/ACL/xattr/special bitsはv1で保証しない。
6. **操作面ごとにfd-relativeな境界を分離する。** v1のLinux Agentはroot/parent directory fdを固定し、
   `openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS)`、`fstatat(AT_SYMLINK_NOFOLLOW)`、`renameat`相当を
   GET/file、download、PUTで使う。PUTはbrowse-root相対canonical pathのみ、GET/downloadは既存
   `allowedReadRoots`（browse/scratch/docs）配下のcanonical絶対pathも許可し、許可rootを選んでroot fd
   から相対化する。既存の字句検査＋Lstat helperはrequest-controlled pathのTOCTOU対策として再利用しない。
   symlinkによるpath迂回は抑止するが、同一uidのnamespace mutatorやhardlinkによるinode別名はv1の
   非協調writer脅威モデル外で、denylistはinode出自まで保証しない。GETは最大2回の
   `fstat-before → 2 MiB+1 bounded read → fstat-after`でsize/content-length整合を確認するが、
   同サイズの外部in-place writeに対する瞬間snapshotは保証しない。
7. **編集対象はLF-onlyのUTF-8テキストとする。** CRLF/CR単独/混在改行は読み取り専用にし、raw byte
   revisionとCodeMirror 6のdocument offsetを変換なしで一致させる。typing/IME/paste/undo/redo/AI
   replacementの全transactionへCR/NUL/unpaired surrogate/2 MiB validatorを適用する。PUTのwire bodyが
   UTF-8不正またはJSON不正なら400 `bad_request`、current fileのUTF-8不正/NULとdecoded contentのNULは
   415 `binary_not_supported`、CRは415 `unsupported_newline` とする。JSON文字列のlone high/low
   surrogate escapeも400 `bad_request`とし、正しいsurrogate pairと実際のU+FFFDは許可する。
   将来の改行マッピングやCRLF対応は別設計で固定してから追加する。
8. **AIは変更提案チャネルで編集バッファまでしか変更しない。** 一般のClaude/Codex等にはWrite/Edit/
   Bash能力があり得るため、AI全体の権限を「書込みtoolなし」とは表現しない。Phase 4の提案生成だけは
   read-only allowlist経路に限定し、`EditSuggestion`をpaneId/filePath/requestId/sourceRevisionと
   ともに検証する。acceptはPUTを呼ばず、保存はユーザーのCtrl+S/Cmd+SまたはSaveボタンに限定する。
9. **Markdownは既存資産を再利用する。** 編集・プレビュー・左右分割の3モードをFileペイン内で提供し、
   MarkdownView、MarpView、Mermaidの遅延ロードを再利用する。現行sourceはedit、previewとslidesは
   preview側のrenderer選択へ対応させ、Marpの通常previewを失わない。
10. **未保存本文はメモリ限定とし、全navigationをguardする。** dirty本文、undo、提案、世代を
    PaneContent/layoutへ入れず、layoutがstorageに永続化されても本文を保存しない。closeだけでなく
    active差し替え、pane再利用、history、tenant/reset、reader、popout、beforeunloadをdirty registryで
    保護する。reload、logout、version update、workspace recreate/clean-home/stopも対象とし、
    terminal用のversion update例外をeditorへ流用しない。dirty popoutはv1では拒否または明示確認とする。
11. **409競合は別remote snapshotを持つstate machineで解決する。** dirty mineを上書きせず、remote採用、
    mine破棄、remoteをbaseにした手動merge、cancelをPhase 2で提供する。409後のGETで対象消失・安全境界
    エラー・編集不可となった場合はmineを保持する `ConflictRemoteUnavailable` とし、再取得・コピー・
    明示的に閉じる操作を提供する。remote revisionだけを更新してmineをforce overwriteする経路は作らない。
12. **Markdownのトップレベルモードは3つに保ち、編集面がsource面の機能を包含する。** Markdownでは
    edit/preview/splitを唯一のトップレベル操作とし、ペインの `mode` をそこから導出する。編集可能な
    Markdownに読み取り専用の `source` モードは残さない。代わりに、CodeViewが持っていた選択→送ると
    行引用のジャンプをCodeMirrorの編集面へ実装する。行番号と引用文字列はDOMではなくCodeMirrorの
    documentから求める（仮想化により画面外の選択がDOMに存在しないため）。編集できないMarkdownは
    従来のpreview/source/slidesへフォールバックする。
13. **外部変更の追従はadvisoryなrevisionプローブとし、CASを置き換えない。** Console以外の書き手
    （エージェント・shell・git）によるファイル変更を、本文を返さない `GET /fs/file?meta=1` の
    ポーリングで検知する。新規ルートは作らずクエリフラグとし、CPのルート追加を不要にする。dirtyの
    ときは通知だけを行い、**プローブは `phase` を遷移させない**。`Conflict` を作るのは409応答と、
    ユーザーが明示的に「差分を確認」を実行して本文GETに成功した場合（docs/44 §7.3）に限り、
    プローブがConflictを自動発生させない不変条件を維持する。cleanのときは自動追従してよいが、
    undo履歴に旧本文を残さない。追従対象は `revision` を持つ `editable:true` のファイルに限る。

## 対象範囲とフェーズ境界

- Phase 0（本ADRと [docs/44](../log/44-markdown-code-editor.md)）で、設計・API・revision/競合・提案形式・
  入力制約を固定する。
- Phase 1 は Agent/CP route、中継、監査（`write_state_unknown`含む）、strict decoder、fd-relative操作、
  GET/download race、symlink/CAS/failure injection、current file/path boundを含む保存API基盤とGo単体
  テストを行う。
- Phase 2 は CodeMirror 6、Fileペインのview/edit、single-flight保存、snapshot/generation、
  `SaveStateUnknown`、409競合とremote取得不能のstate machine、全buffer validator、dirty registry/
  navigation guard、beforeunload、ARIA、Saveボタンを実装する。
- Phase 3 は Markdown/Marpのedit/preview/split、通常preview/slide renderer切替、編集面の選択→送ると
  行ジャンプ、既存描画資産の回帰テストを実装する。**2026-07-28に実装完了。** Console単独で完結し
  Agent/CPの変更は無い。レビュー8ラウンド・14件の指摘に対応し、既知の限界として残した項目は無い。
- Phase 3.5 は `meta=1` メタデータGET、Consoleのプローブ、dirty時のadvisory通知、clean時の自動追従
  （読み取り専用のviewペインを含む）を実装する。**2026-07-28に実装完了**（docs/44 §6 Phase 3.5）。
  Agent側は応答からの `content` 除去のみで判定・排他・エラー契約を通常GETと共有し、Console側は
  undo履歴を残さないEditorState再構築と行番号ベースのカーソル/スクロール復元で追従する。
- Phase 4 は read-only提案生成チャネル、identity付き構造化提案、差分レビュー、accept/rejectを実装する。
  **2026-07-28に実装完了**（docs/44 §6 Phase 4）。UXは「選択範囲＋指示文」（rangeはユーザー選択で
  確定し、LLMにoffsetを計算させない。選択なしは全文）、transportは同期POST
  `POST /fs/suggest-edit`（envelopeはwireに載せずConsoleが合成して§4.2の検証を通す）、生成は
  タイトル/返信サジェストと同じ `oneShotHeadless` を再利用し、唯一read-onlyでなかったopencode
  one-shotへ `OPENCODE_CONFIG` のedit/bash denyポリシーを追加して閉じた。staleは保存せず
  `baseRevision !== bufferRevision` から導出し、適用はCodeMirrorの範囲transaction 1回
  （undo可能・共通validatorフィルタ通過）で行う。
- Phase 5 の複数候補・hunk単位accept・セッション連携・補完・CRLF対応は別設計後に着手する。

## 却下した選択肢

- **Monaco を先に採用する:** LSP のない段階では依存とバンドルが大きく、既存の軽量な viewer との
  統合に対する利点が不足する。LSP が要件になった時点で再評価する。
- **編集専用の新規ペイン種別:** レイアウト、URL/履歴、ペイン復元、既存 viewer の再利用点が増え、
  File ペインの自然な view/edit 遷移を失うため採らない。
- **AIが直接ファイルを書き込む:** 明示的なユーザー承認、競合レビュー、監査の境界が曖昧になるため
  採らない。AIは提案の生成とバッファ適用までに限定する。
- **localStorageへのdraft保存:** 共有端末やアカウント切替時の残留、機微なソースコードの意図しない
  永続化を招くため採らない。
- **revisionなしの上書き保存:** 別タブ、外部エージェント、git操作による変更を黙って消すため採らない。
- **全writerを協調ロック下に置く:** shell/agent/gitを含む全書込み経路の統合はv1の変更範囲と権限境界を
  大きくするため採らない。比較時点CASの限界を契約とテストに明記する。
- **編集可能なMarkdownに読み取り専用のsourceモードを残す:** トップレベルのモードが4つ（Marpでは実質
  5つ）になり、キーボードでの巡回とスマホ幅のレイアウトが破綻する。さらに、見た目がほぼ同じ
  プレーンテキスト面が2つ並び、送るピル・検索・編集可否が面ごとに食い違う。編集面へ機能を移せば
  この重複自体が不要になるため採らない。
- **Agentのfsnotify + pushストリームで外部変更を検知する:** 監視レジストリ、CP経由の新しいstream配線、
  コンテナFSのinotify挙動とwatch上限の検討が必要で、v1の変更範囲を大きく超える。プローブで不足だと
  確認された時点で別設計とする。
- **全文GETをポーリングして外部変更を検知する:** 開いているペインごとに最大2 MiBを繰り返し転送し、
  Console↔CPの通信量削減の方針に反するため採らない。

## 結果と受け入れる制約

- 既存の File ペインと描画資産を維持したまま、ユーザーが編集・確認・明示保存できる。
- 競合時は安全側に倒して保存を止める。自動マージや強制上書きは将来の別設計とする。
- v1 は UTF-8 のテキストファイル、かつ編集本文 2 MiB 以下に限定する。バイナリ、非UTF-8、
  大容量ファイル、denylist 配下、symlink 経由のファイルは編集対象外とする。GET/downloadは
  scratch/docsのallowed read root絶対pathをread-onlyで許可する。
- dirty バッファはタブのメモリにしか存在しないため、ブラウザ再起動後の復元機能は提供しない。
- 外部変更の検知はポーリングであり、リアルタイムではない。タブが背面のあいだ、およびプローブ間隔の
  ぶんだけ気付くのが遅れる。`editable:false` のファイルは `revision` を持たないため追従対象外である。
  検知は早期警告にすぎず、保存の正しさは引き続き比較時点CASが担保する。
- 同一uidのnamespace mutatorやhardlinkによるinode別名は非協調writer脅威モデル外であり、fd境界は
  request-controlled pathとsymlink解決の保護範囲として説明する。
- **既知の制約:** クライアントがPUTをタイムアウトで打ち切った後の復旧GETは、Agent内の
  path mutex共有とmutex取得直後のcontext検査でAgentプロセス内の競合を閉じている。ただし
  PUTと復旧GETは別々のCP→Agent HTTPリクエストであり、CPはキャンセルの伝播順序を保証しない
  ため、「mutex取得前に停止していたPUTへcancelが届くより先に復旧GETが旧baseを読み、その後
  再開したPUTが検査を通過してrenameする」極めて狭い窓が残る（docs/44 §3.2）。発生には
  goroutineスケジューリングとネットワークタイミングの重なりが必要で確率は極めて低く、v1では
  受け入れる。解消はCP側でのpath単位進行中PUT追跡、または保存operation IDによるGETの
  待ち合わせを要する将来課題とする。
