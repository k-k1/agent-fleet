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
4. **保存は compare-and-swap とする。** ファイルの生バイトSHA-256を `revision` とし、クライアントが
   直前に取得した `baseRevision` と現在の revision が一致する場合だけ保存する。一致しなければ
   `409 revision_conflict` とし、上書き保存や自動マージは行わない。
5. **保存は atomic write とする。** 対象と同じディレクトリに一時ファイルを書き、内容を fsync
   してから rename する。rename 後は親ディレクトリも fsync する。書込み失敗時に旧ファイルを
   部分的な内容で壊さない。
6. **書込み境界は既存の fs 読取り境界より狭くする。** パスは browse root 相対の相対パスだけを
   受け付け、絶対パス・traversal・denylist・symlink を拒否する。scratch とロール別 docs mount
   は読み取り専用のため保存対象にしない。
7. **AIは編集バッファまでしか変更しない。** AIが返す変更は構造化された `EditSuggestion` とし、
   適用は現在のバッファへ行う。ディスクへの保存は AI の経路から分離し、ユーザーの明示操作
   （Ctrl+S / Cmd+S）で `PUT /api/fs/file` を呼ぶ。
8. **Markdownは既存資産を再利用する。** 編集・プレビュー・左右分割の3モードを File ペイン内で
   提供し、既存の MarkdownView、MarpView、Mermaid の遅延ロードを再利用する。Markdown の
   編集モードに専用ペインを追加しない。
9. **未保存本文はメモリ限定とする。** dirty な本文、undo 履歴、未適用の提案はタブ内メモリだけに
   保持し、localStorage/sessionStorage/IndexedDB などのブラウザストレージには永続化しない。
   タブを閉じる、再読み込みする、Workspace を失う場合の復元は保証しない。

## 対象範囲とフェーズ境界

- Phase 0（本ADRと [docs/44](../44-markdown-code-editor.md)）で、設計・API・revision/競合・提案形式・
  入力制約を固定する。
- Phase 1 は保存APIの実装と Go 単体テストを行う。
- Phase 2 は CodeMirror 6、File ペインの view/edit、dirty、明示保存を実装する。
- Phase 3 は Markdown の編集・プレビュー・分割を実装する。
- Phase 4 で AI 提案の選択範囲アクション、差分レビュー、accept/reject を実装する。
- Phase 5 の複数候補・hunk 単位 accept・セッション連携・補完は本ADRの必須範囲ではない。

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

## 結果と受け入れる制約

- 既存の File ペインと描画資産を維持したまま、ユーザーが編集・確認・明示保存できる。
- 競合時は安全側に倒して保存を止める。自動マージや強制上書きは将来の別設計とする。
- v1 は UTF-8 のテキストファイル、かつ編集本文 2 MiB 以下に限定する。バイナリ、非UTF-8、
  大容量ファイル、denylist 配下、symlink 経由のファイルは対象外とする。
- dirty バッファはタブのメモリにしか存在しないため、ブラウザ再起動後の復元機能は提供しない。
