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
   `409 revision_conflict` とする。同一APIのPUTは、fd安全検証後のcanonical相対pathをmutex keyに
   して直列化する。shell、Claude/Codex、git checkout等の外部writerはこのmutexに参加しないため、
   比較後rename前の外部変更を防止・検出する保証は持たない。全writer協調ロックは採用しない。
5. **保存はatomic writeとし、rename後失敗を別状態にする。** 対象と同じディレクトリに一時ファイルを
   書き、fsyncしてからrenameし、親directoryもfsyncする。rename前の失敗は旧本文を保持して
   `write_failed`、rename後のdirectory fsync等の失敗は実体不明として `write_state_unknown` とし、
   クライアントにGET照合を要求する。保持する属性は `mode.Perm()` のみで、owner/ACL/xattr/special
   bitsはv1で保証しない。
6. **fd-relativeな書込み・読取り境界を採用する。** v1のLinux Agentはroot/parent directory fdを固定し、
   `openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS)`、`fstatat(AT_SYMLINK_NOFOLLOW)`、`renameat`相当を
   GET/file、download、PUTで使う。既存の字句検査＋Lstat helperはTOCTOU対策として再利用しない。
   POSIX canonical relative pathだけを受け付け、絶対・traversal・空component・backslash・Windows drive/
   UNC形式・denylist・symlinkを拒否する。scratchとrole別docs mountは読み取り専用とする。
7. **編集対象はLF-onlyのUTF-8テキストとする。** CRLF/CR単独/混在改行は読み取り専用にし、raw byte
   revisionとCodeMirror 6のdocument offsetを変換なしで一致させる。将来の改行マッピングやCRLF対応は
   別設計で固定してから追加する。
8. **AIは変更提案チャネルで編集バッファまでしか変更しない。** 一般のClaude/Codex等にはWrite/Edit/
   Bash能力があり得るため、AI全体の権限を「書込みtoolなし」とは表現しない。Phase 4の提案生成だけは
   read-only allowlist経路に限定し、`EditSuggestion`をpaneId/filePath/requestId/sourceRevisionと
   ともに検証する。acceptはPUTを呼ばず、保存はユーザーのCtrl+S/Cmd+SまたはSaveボタンに限定する。
9. **Markdownは既存資産を再利用する。** 編集・プレビュー・左右分割の3モードをFileペイン内で提供し、
   MarkdownView、MarpView、Mermaidの遅延ロードを再利用する。現行sourceはedit、slidesはMarp previewへ
   対応させ、編集専用ペインは追加しない。
10. **未保存本文はメモリ限定とし、全navigationをguardする。** dirty本文、undo、提案、世代を
    PaneContent/layoutへ入れず、layoutがstorageに永続化されても本文を保存しない。closeだけでなく
    active差し替え、pane再利用、history、tenant/reset、reader、popout、beforeunloadをdirty registryで
    保護する。dirty popoutはv1では拒否または明示確認とする。

## 対象範囲とフェーズ境界

- Phase 0（本ADRと [docs/44](../44-markdown-code-editor.md)）で、設計・API・revision/競合・提案形式・
  入力制約を固定する。
- Phase 1 は Agent/CP route、中継、監査、strict decoder、fd-relative操作、GET/download race、
  symlink/CAS/failure injectionを含む保存API基盤とGo単体テストを行う。
- Phase 2 は CodeMirror 6、Fileペインのview/edit、single-flight保存、snapshot/generation、dirty
  registry/navigation guard、beforeunload、ARIA、Saveボタンを実装する。
- Phase 3 は Markdown/Marpのedit/preview/splitと既存描画資産の回帰テストを実装する。
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
  大容量ファイル、denylist 配下、symlink 経由のファイルは対象外とする。
- dirty バッファはタブのメモリにしか存在しないため、ブラウザ再起動後の復元機能は提供しない。
