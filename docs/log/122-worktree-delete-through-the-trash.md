# 122. worktree の削除もごみ箱を通す（削除時の記録・ref による保護・未コミットの変更のスナップショット）

- 依頼: #1042（#1040 の P1）。前段は [docs/log/121](121-recreate-deleted-worktree.md)、ADR 0101 を改訂。
- 関連: squash マージのブランチは #1043。

## 1. 何が失われていたか

worktree の削除（`HandleDeleteRepo`）はブランチを残すが、次の 3 つを失っていた。

- **どのコミットにいたか。** ブランチは後で動くか消える。未 push のコミットはそのとき到達不能になり、gc が回収する。
- **未コミットの変更と未追跡のファイル。** force の削除でだけ起きる。非 force の削除は dirty を断る。
- **そこに worktree があったこと自体。** `cleanupManifest.Worktrees` はあったが、何も書いていなかった。

## 2. 決めたこと

- **記録してから消す。** worktree を消す箇所は `git.go` の `worktree remove --force` の 1 か所だけ
  （Console・掃除・MCP の `delete_worktree` はどれもここを通る）。その直前に `gitx.PrepareTombstone` →
  `RecordDeletedWorktree`（`gitx.Deps` 経由で main のごみ箱）を呼ぶ。失敗したら 500
  `worktree_archive_failed` で何も消さない。shell セッションをごみ箱へ移せないときと同じ規則。後続の段
  （shell のごみ箱・`worktree remove`）が失敗したら `undo` で記録と ref を取り消す。
- **スナップショットは一時 index で作る。** `GIT_INDEX_FILE=<tmp> read-tree HEAD` → `add -A` → `write-tree` →
  `commit-tree -p HEAD`。worktree 自身の index と共有の stash は触らない（テストで status と stash list を確かめた）。
  `.gitignore` の対象は含めない（`node_modules` を丸ごと commit しないため）。commit-tree の作者は固定値。
  利用者の `user.email` が無いことで削除が止まらないように。
- **ref で保護する。** `refs/af/deleted-worktrees/<名前>-<ナノ秒時刻>` を、スナップショット（無ければ HEAD）に向ける。
  refs/heads と refs/tags の外なので、ブランチ一覧・push・fetch には出ない。ブランチを消して
  `reflog expire` + `gc --prune=now` をかけても戻せることをテストで確かめた。ref を作らない版ではこのテストが落ちる。
- **ごみ箱の 1 件。** reason `delete_worktree`、`cleanupManifest.Worktree` に
  `{path, name, parent, branch, head, snapshot, ref, shelved}`。`shelved` は、その削除でアーカイブへ移した AI セッション。
  完全に削除すると ref を外す（1 件ずつでも「古いものをまとめて」でも同じ `purgeCleanupArchive` を通る）。
- **作り直しの候補の先頭に「削除したときの状態」（`deleted`）。** 記録した HEAD にブランチを置き、スナップショットを
  `git restore --source=<snap> --worktree -- .` で重ねる。index は HEAD のままなので、変更はすべて未ステージで戻る
  （staged の区別だけは保存しない）。スナップショットに無いファイルは消え、未追跡のファイルは未追跡のまま戻る。
  - ブランチがその後動いた（`moved`）、別の作業コピーが使っている（`in_use`）、削除時に detached だった場合は、
    新しいブランチ名が要る。動いた後のブランチをそのまま使う道は、別の候補 `local` として残る。
- **ごみ箱の「復元」は worktree を作り直す**（利用者の選択）。作り直せたら `shelved` のセッションをアーカイブから戻す。
  新しいブランチ名が要る場合は、復元は何も作らずに 409（`recreate_needs_new_branch` / `branch_in_use` など）で断る。
  Console はその文で、アーカイブの「作業コピーを作り直す」へ案内する。フォルダが既に戻っていれば作らず、セッションだけ戻す。

## 3. 子セッションのレビューで直したこと

- **remove が途中で失敗しても記録を消さない。** 消せないディレクトリ（chmod 555 など）があると、
  `worktree remove --force` はファイルと登録を消した後で失敗する（実測）。最初の版はそこで undo を呼び、
  スナップショットの唯一の記録と ref を捨てていた。remove を試した後は undo しない。worktree が実は無傷なら、
  残った記録の復元は「フォルダがある」でセッションを戻すだけになる。
- **git 管理外の入れ子リポジトリがあれば削除を断る**（409 `worktree_nested_repo`）。`add -A` はそれを gitlink
  （コミット id だけ）として入れる。中身はその入れ子の .git にしか無いので、戻すと空のフォルダになる（実測）。
  スナップショットと HEAD の差分で、新しく増えた 160000 のエントリを検出する。HEAD が既に追跡している
  submodule は対象外。
- **ごみ箱の復元が「新しいブランチ名が要る」で断られたら、ごみ箱タブから作り直しダイアログを開く。**
  AI セッションが 1 件も棚に移らなかった worktree には、アーカイブ一覧に見出しが無い。そのため、エラー文の
  「アーカイブ一覧へ」では行き止まりだった。
- スナップショットを重ねられなかったら、作ったチェックアウトを片付ける。残すと、再試行が「フォルダがある」で
  成功扱いになり、変更が戻らない。
- 棚から戻すセッションは、フォルダの配下（サブフォルダで起動したもの）も対象にする。削除が棚に移す範囲と揃えた。

## 4. テストへの影響

- `TestWorktreeStaysWhenItsShellCannotBeTrashed` は、ごみ箱に書けない状態で削除すると、記録の段で先に止まるようになった。
  shell のごみ箱の段を試すという元の意味を保つため、コミットの無い worktree（記録しない）に変えた。記録の段は
  `TestWorktreeStaysWhenItsTombstoneCannotBeWritten` で試す。
- P0 の `TestRecreateDeletedWorktreeFlow` は、候補の先頭に `deleted` が加わった形に直した。

## 5. 限界

- `.gitignore` の対象のファイルは戻らない。submodule の中の未コミットの変更も戻らない（gitlink の変化は記録する）。
- staged の区別は戻らない。変更は未ステージで戻り、staged だった新規ファイルは未追跡として戻る。
- git 管理外の入れ子リポジトリを含む worktree は、それを移すか消すまで削除できない（§3）。
- 巨大な未追跡のファイルもそのままスナップショットに入る（ignore されていない限り）。
- この変更より前に消した worktree には記録が無い（P0 の候補だけで作り直す）。
- worktree でない作業コピー（通常のクローン）と SVN の削除は対象外。
