# 0028. 削除ロック — 保護は Agent の REST 層に置き、自動削除にも効かせる

[English](0028-deletion-lock.md) | 日本語

- 状態: **採用・実装済み**。設計は [docs/45](../log/45-deletion-lock.md)。
- 関連: [0012](0012-go-internal-refactor.ja.md)（Agent の内部構造）/ [0021](0021-scheduled-execution.ja.md)（自動実行）/
  `workspace/agent/cleanup_archive.go`（掃除と gz 安全網 — 専用の設計文書は無い）

## 背景

セッション・作業コピー（worktree）・アシスタント会話は、**消し方が複数ある**。人が押す削除
（Console の行メニュー、掃除モーダル）だけでなく、停止中セッションの 7日 TTL 自動 prune、
セッションが尽きた worktree の自動 prune、オペレーター（MCP `delete_session` / `delete_worktree`）、
チャットブリッジ経由の依頼まである。「これは消したくない」を利用者が表明する手段が無く、
残したい会話や長期の作業コピーが自動整理で消える余地があった。

## 決定

**対象ごとに `locked` フラグを持たせ、拒否は Agent の REST ハンドラで行う。**

1. **enforcement は REST 層**。Console のボタン無効化は補助でしかない — オペレーターもブリッジも
   素の REST も同じハンドラを通るので、そこで止めれば入口を問わず一様に止まる。拒否は
   `403` ＋ 安定コード `locked` / `locked_sessions`。
2. **自動削除にも効かせる**。TTL prune と worktree 自動 prune を素通しにすると、ロックは
   「押し間違い防止」でしかなくなる。守りたいのは*時間が経っても消えないこと*なので、
   自動経路こそ対象に含める。
3. **`force` はロックを越えない**。dirty worktree の `force=true` は「未コミットを承知で消す」意思表示だが、
   ロックは「消さない」意思表示で、後から入れた方が強い。越える道はロック解除のみ。
4. **可逆な操作は止めない**。`archive`（復元可）と `halt`（行が残る）は通す。ロックが止めるのは
   実体が消える操作だけ — さもないと「ロックしたら整理もできない」になり、使われなくなる。
5. **置き場は対象の自然な所有物に**。セッション＝`Meta`、会話＝会話 JSON。作業コピーだけは AF 所有の
   メタが無いので、外側の台帳 `~/.config/agent-fleet/locks.json` に**絶対パス**をキーとして持つ
   （自動 prune が名前ではなく dir しか知らないため）。
6. **掃除の点検では隠さず `keep` で見せる**。候補一覧から消すと「なぜ片付かないのか」が分からず、
   オペレーターが 403 になるツール呼び出しを提案してしまう。

## 影響

- 新規 API 3 本（`POST /{sessions|repos|chat/conversations}/…/lock`）と CP allowlist 登録。
- `Session.locked` / `Repo.locked` / `ConversationMeta.locked` が wire に追加（`omitempty`、既存互換）。
- Console は行の鍵バッジ・メニュー切替・削除項目の無効化・一括操作の件数からの除外。

## 却下した案

- **Console だけで無効化**: オペレーターや REST から素通り。守りにならない。
- **ロック対象を「アーカイブ済み」に読み替える**: archive は可逆な非表示で、意味が違う。TTL 除外の
  副作用（アーカイブは prune 対象外）に頼るのは偶然の保護で、意図が読めない。
- **作業コピー内にロックファイルを置く**: `git status` を汚し、worktree 削除で一緒に消える。
- **削除時に毎回確認ダイアログを増やす**: 自動削除には効かず、手動側も確認疲れを増やすだけ。
