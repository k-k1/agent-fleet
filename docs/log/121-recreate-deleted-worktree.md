# 121. 削除した worktree を元のパスに作り直し、アーカイブのセッションを戻す

- 依頼: #1040（P0）。P1（削除を取り消せるようにする）は #1042、squash マージのブランチは #1043。
- 関連: ADR 0101（セッション削除はごみ箱経由）、docs/log/115。

## 1. 要点: 元のパスに戻せば、kind ごとの作業は要らない

セッションの識別子は `session.UUID(dir, name)` で、claude の `--session-id` にも、各 kind の id 台帳にもなる。
claude / opencode / cursor は、自分の保存先も cwd ごとに持つ。だから別のパスから再開すると、どの kind でも
会話が切れる。同じパスに worktree を作り直せば、既存の復帰と再開がそのまま動く。meta を書き換える必要もない
（`worktree_recreate_test.go` の最後の確認）。

`EnsureWorktree` はフォルダ名を `<親>@<seg>` から組み立てるので、入れ子の名前（`repo@wip-A@wip-B`）を再現できない。
そこで、パスを直接受け取る `gitx.RecreateWorktreeAt` を足した。後処理（identity・submodule の種付け・scratch）は
`finishNewWorktree` として共用する。

## 2. 作り直し元の順序

`gitx.ResolveRecreate` は、そのパスで動いたセッションの開始ブランチを新しい順に並べ、ブランチごとに最初に
見つかった取り出し元を 1 つ返す。開始ブランチは Console での改名に追従する（`updateStartBranch`）。

1. ローカルブランチ（別の作業コピーがチェックアウト中なら `in_use`）
2. リモート追跡ブランチ（`--track -b` で作る）
3. ごみ箱の SHA（`handleDeleteBranch` が記録した `cleanupManifest.Branches`。親とその worktree のどれで消しても拾う）
4. マージコミットの第 2 親（GitHub / Bitbucket / git 自身のマージ件名。オフラインで動く）
5. どれも無ければ、親の現在のブランチから同名の新しいブランチ

どのセッションもブランチを記録していなければ、フォルダの seg と `sanitizeSeg` が一致する既存ブランチを探し、
それも無ければ seg を新しいブランチ名にする。

- `in_use` のときは detached HEAD にしない。エージェントのコミットの行き場が無くなるので、同じコミットから
  新しいブランチを切る（`new_branch`）。
- POST は、クライアントが見た候補を `source` と `branch` で指す。SHA は受け取らず、サーバ側で解決し直す。
  解決できなくなっていれば `recreate_stale` で断る。
- Console の外で消された worktree は登録が残り、`worktree add` が "missing but already registered" で断る。
  そのため、add の前に `worktree prune` を流す（この行を消すとテストが落ちることを確かめた）。
- 親を消す処理と並んで走らないよう、削除ゲートの中で実行する。

## 3. Console

- アーカイブモーダル: `<repo>@<seg>` のフォルダで、全行が `resumable === false` の群の見出しに
  「作業コピーを作り直す」を出す（`recreatableGroup`）。Agent が返した `path` が群の `dir` と違えば作らない。
  別の場所で作り直しても会話は戻らないため。
- 作り直したら、そのフォルダのアーカイブ済みセッションを選んで復帰させる。フォルダ名は同名ブランチの別の作業で
  再利用されることがあるので、日付を出し、開始ブランチが作り直したブランチと違う行には印を付ける。
- 作業コピーの行のメニューに「この作業コピーのアーカイブ済みセッション」を足した。件数は出さない。
  アーカイブ一覧は行ごとに取りに行く値ではないため。
- 再開は自動ではしない。復帰した行は停止中の再開可能なセッションとして一覧に戻り、クリックで再開できる。

## 4. 限界

- 消した worktree の未コミットの変更と ignore 対象のファイルは戻らない（今後の削除については #1042 で直す）。
- squash / rebase マージのブランチは、マージコミットが残らないので新しいブランチになる（#1043）。
- `workingCopyId` は新しい値になる。worktree 単位の共有ルールは戻らない。
- 別のパスへ履歴を移す案（#1040 の P2）は、この PR では扱っていない。
