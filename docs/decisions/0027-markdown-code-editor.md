# 0027. File ペインに CodeMirror 6 の編集モードを追加し、保存を明示操作に限定する

- 状態: **採用・Phase 0 設計固定**（2026-07-25）
- 詳細契約: [docs/44-markdown-code-editor.md](../44-markdown-code-editor.md)
- 関連: [docs/dev/02-console.md](../dev/02-console.md)（Console のペイン構成）/
  [docs/dev/04-workspace-agent.md](../dev/04-workspace-agent.md)（fs の境界と denylist）/
  [docs/dev/05-api-contracts.md](../dev/05-api-contracts.md)（API 中継の地図）/
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
   durabilityリスク承認だけを例外とする。保持する属性は `mode.Perm()` のみで、owner/ACL/xattr/
   special bitsはv1で保証しない。
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
   415 `binary_not_supported`、CRは415 `unsupported_newline` とする。将来の改行マッピングや
   CRLF対応は別設計で固定してから追加する。
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

## 対象範囲とフェーズ境界

- Phase 0（本ADRと [docs/44](../44-markdown-code-editor.md)）で、設計・API・revision/競合・提案形式・
  入力制約を固定する。
- Phase 1 は Agent/CP route、中継、監査（`write_state_unknown`含む）、strict decoder、fd-relative操作、
  GET/download race、symlink/CAS/failure injection、current file/path boundを含む保存API基盤とGo単体
  テストを行う。
- Phase 2 は CodeMirror 6、Fileペインのview/edit、single-flight保存、snapshot/generation、
  `SaveStateUnknown`、409競合とremote取得不能のstate machine、全buffer validator、dirty registry/
  navigation guard、beforeunload、ARIA、Saveボタンを実装する。
- Phase 3 は Markdown/Marpのedit/preview/split、通常preview/slide renderer切替と既存描画資産の
  回帰テストを実装する。
- Phase 4 は read-only提案生成チャネル、identity付き構造化提案、差分レビュー、accept/rejectを実装する。
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

## 結果と受け入れる制約

- 既存の File ペインと描画資産を維持したまま、ユーザーが編集・確認・明示保存できる。
- 競合時は安全側に倒して保存を止める。自動マージや強制上書きは将来の別設計とする。
- v1 は UTF-8 のテキストファイル、かつ編集本文 2 MiB 以下に限定する。バイナリ、非UTF-8、
  大容量ファイル、denylist 配下、symlink 経由のファイルは編集対象外とする。GET/downloadは
  scratch/docsのallowed read root絶対pathをread-onlyで許可する。
- dirty バッファはタブのメモリにしか存在しないため、ブラウザ再起動後の復元機能は提供しない。
- 同一uidのnamespace mutatorやhardlinkによるinode別名は非協調writer脅威モデル外であり、fd境界は
  request-controlled pathとsymlink解決の保護範囲として説明する。
