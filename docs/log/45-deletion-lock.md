# docs/45 — 削除ロック（セッション / 作業コピー / アシスタント会話）

決定は [ADR 0028](../decisions/0028-deletion-lock.md)。「消したくないもの」を利用者が明示的に固定でき、
**手動削除も自動削除も**その意思を越えられないようにする。

## 1. 何を守るか

| 対象 | ロックの置き場 | 守られる削除経路 |
|------|----------------|------------------|
| セッション | `session.Meta.Locked`（`~/.config/agent-fleet/sessions/<name>.json`） | `POST /sessions/{name}/stop`（Console の「削除」＝メタ忘却）／`DELETE /sessions/{name}[?reclaim=1]`／停止中 7日 TTL の自動 prune／作業コピー削除の巻き添え（`prune_sessions`） |
| 作業コピー（clone / worktree） | `~/.config/agent-fleet/locks.json` の `worktrees`（絶対パスの集合） | `DELETE /repos/{name}`（**`force=true` でも越えられない**）／セッションが尽きた worktree の自動 prune（`maybePruneWorktree`） |
| アシスタント会話 | 会話 JSON の `locked`（`~/.config/agent-fleet/chats/<id>.json`） | `DELETE /chat/conversations/{id}` |

**通るもの（可逆なので止めない）**: セッションの `archive`（一覧から隠すだけ・復元可）と `halt`
（停止して行は残す）、会話の rename、作業コピーの checkout/FF。ロックは「実体が消える」操作だけを止める。

## 2. なぜこの置き場か

- セッションと会話は**自分の JSON を持っている**ので、そこに 1 フィールド増やすのが素直。既存メタとの
  互換は `omitempty` で保たれる（旧メタ＝未ロック）。
- 作業コピーは **AF が所有するメタファイルを持たない**（ディレクトリそのものが実体）。中に印を書けば
  `git status` を汚し、`.git` に置けば worktree 削除で一緒に消える。そこで**外側の小さな台帳**
  （`locks.json`）に持つ。キーは名前ではなく**絶対パス** — 自動 prune（`maybePruneWorktree`）は
  repo 名ではなく dir しか知らないため。読み出し時に実体の消えたエントリを掃除する。

## 3. どこで効くか（enforcement は Agent の REST 層に一本化）

保護は**ハンドラ側**で効かせる。Console のボタンを無効化するだけでは、オペレーター（MCP の
`delete_session` / `delete_worktree`）・チャットブリッジ・素の REST から同じ削除が通ってしまう。
REST で止めれば、**どの入口から来た削除も同じ 403 で止まる**。

- 拒否は `403` ＋ 安定コード `locked`（対象自身がロック）/ `locked_sessions`（ロック済みセッションを
  巻き添えにする作業コピー削除）。Console 側の和文/英文は i18n の `err.locked` / `err.locked_sessions`。
- `session.RemoveMeta` の呼び出し 5 箇所（stop / delete×2 / TTL prune / `forgetNonLiveMetasUnder`）が
  全部ガード済み。worktree 削除は `handleDeleteRepo` と `maybePruneWorktree` の 2 箇所。
- 掃除の点検（`GET /sessions/cleanup`）はロック済みを **`safety=keep`・`action` 空**で返す。黙って
  隠さないのは「なぜ片付かないのか」を利用者にもオペレーターにも見せるため。Console の掃除モーダルは
  keep 行をチェック不可で表示する既存の挙動をそのまま使う。

## 4. API

```
POST /sessions/{name}/lock            {"locked": true|false}  → {name, locked}
POST /repos/{name}/lock               {"locked": true|false}  → {name, locked}
POST /chat/conversations/{id}/lock    {"locked": true|false}  → {id, locked}
```

一覧にもフラグが載る: `GET /sessions`（`Session.locked`）/ `GET /sessions/archived` / `GET /repos`
（`Repo.locked`、台帳 1 回読みで全行に付与）/ `GET /chat/conversations`（`locked`）。
CP は 3 本とも allowlist に登録済み（`control-plane/routes.go`。CP は明示許可方式なので、
Agent 側だけ足しても Console からは 404 になる）。

## 5. Console

- **行の鍵バッジ**: セッション行 `.sess-lock` / 作業コピー行 `.repo-lock` / 会話行（`.sess-lock` を共用）。
  muted 色 — 異常ではなく利用者の意図なので警告色は使わない。
- **メニュー**: 各行の ⋯/右クリックに「削除ロックをかける / 解除する」。ロック中は削除項目を
  `disabled` ＋ tooltip で理由表示（押せない理由が分かる形にする）。
- **一括操作からの除外**: 「停止中をまとめて整理」「その他をまとめて整理」「アーカイブの N日以上を一括削除」は
  ロック済みを**確認ダイアログの件数から先に外す** — Agent が 403 で弾く分だけ「N 件消します」が嘘になるため。

## 6. テスト

`workspace/agent/locks_test.go`（実 HTTP でハンドラを駆動）:

- `TestSessionLockRefusesDeletion` — stop も delete も 403、archive は通る、解除後は消える。
- `TestSessionLockSurvivesTTLPrune` — TTL 超過の 2 行で、ロック済みだけが一覧に残る（自動削除の証明）。
- `TestRepoLockRefusesDelete` — `force=true` でも 403、一覧に `locked` が載る、解除後は消える。
- `TestRepoDeleteRefusedByLockedSession` — ロック済みセッションが住む作業コピーの削除は `locked_sessions`。
- `TestWorktreeLockBlocksAutoPrune` — `maybePruneWorktree` がロック中は消さず、解除後は消す（対照付き）。
- `TestChatLockRefusesDelete` — 会話の 403 と一覧フラグ、解除後の削除。

`session_cleanup_test.go` にロック行が `keep`／action 空になる分岐を追加。

## 7. 残作業

- 実フリート再ビルド後の実機目視（行バッジ・メニュー・一括操作の件数）。
- アシスタント定義（`DELETE /assistants/{id}`）は対象外（builtin は元々削除不可）。必要になったら
  同じ形（`locked` フィールド＋ハンドラ拒否）で足せる。
