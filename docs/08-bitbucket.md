# 08. Bitbucket 連携

確定方針: **ユーザー単位の SSH 鍵 + 手動登録**。Control Plane は Bitbucket の認証情報
（App Password / OAuth トークン）を預からない。鍵は各 Workspace の `~/.ssh` に閉じる。

## 8.1 鍵モデル

- 粒度: **1 ユーザー（= 1 Workspace）に 1 鍵**。Working copy 間で共有。
- 種別: `ed25519`（`ssh-keygen -t ed25519 -N "" -f ~/.ssh/id_ed25519`）。
- 保管: Workspace の `~/.ssh`（EFS, パーミッション 700/600）。他ユーザー・Control Plane から不可視。
- 生成タイミング: Workspace 初回起動時に Agent が「無ければ生成」（[07 §7.7](07-workspace-agent.md#77-ライフサイクルと永続)）。

## 8.2 公開鍵の手動登録フロー

```
1. Console「SSH 鍵」画面 → GET /api/v1/sshkey
2. 公開鍵(ssh-ed25519 …)と fingerprint を表示、コピー導線
3. ユーザーが Bitbucket の Personal settings → SSH keys に貼り付けて登録
4. Console「接続テスト」→ Agent が `ssh -T git@bitbucket.org` で疎通確認
5. 成功表示 → clone 可能に
```

- B5（API 自動登録）は当面見送り。将来必要なら、ユーザー本人の App Password を
  Secrets Manager にユーザー毎保管して REST 登録する拡張を別途設計（責任範囲が増える点に留意）。

## 8.3 鍵ローテーション

- `POST /api/v1/sshkey/rotate` で新鍵を生成し旧鍵を退避。
- 新公開鍵の再登録が必要なため、Console は「未登録（要再登録）」状態を明示し接続テストを促す。

## 8.4 known_hosts / ホスト鍵検証

- MITM 防止のため `StrictHostKeyChecking` を有効に保つ。
- Bitbucket の正規ホスト鍵を Workspace イメージ（または Agent 起動時）に `~/.ssh/known_hosts` へ事前投入。
- ホスト鍵は Bitbucket 公式公表値を Secrets/Param で配布し、手動 yes 承認に依存しない。

## 8.5 git 操作（status / checkout / branch）

Console の操作は Control Plane → Agent の `git.*` に委譲（[07 §7.3](07-workspace-agent.md#73-制御-apiagent-内部)）。

| 操作 | Agent 実体 | Console 表示 |
|------|-----------|-------------|
| clone | `git clone <git@…> <path>` | 進捗ジョブ → 完了で一覧追加 |
| status | `git status --porcelain=v2 --branch` 解析 | ブランチ / dirty / ahead-behind / 変更数 |
| branches | `git for-each-ref` | ローカル/リモート一覧 |
| checkout | `git checkout [-b] <ref>` | 現在ブランチ更新 |
| fetch | `git fetch [--prune]` | リモート追従 |

- remote URL は SSH 形式（`git@bitbucket.org:org/repo.git`）を基本とする。
- HTTPS clone は本方針（鍵運用）では非対応を既定とし、必要時に別途検討。

## 8.6 マルチアカウント / 複数 Workspace

- ユーザーが複数 Workspace を持つ構成は v1 では想定しない（1 ユーザー 1 Workspace）。
- 将来、案件別に鍵を分けたい場合は Working copy 単位の `core.sshCommand` 切替で拡張可能。

## 8.7 セキュリティ要点（[04](04-security.md) と整合）

- 秘密鍵は EFS のユーザー領域に限定。ログ・API レスポンスに秘密鍵を出さない（公開鍵のみ）。
- Control Plane は Bitbucket 資格情報を保持しないため、漏洩面が小さい。
- 接続先は Bitbucket に限定（Egress 許可リスト）。
