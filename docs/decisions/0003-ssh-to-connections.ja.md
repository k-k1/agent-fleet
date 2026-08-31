# 0003. git 認証 — SSH 鍵から Connections（HTTPS トークン/OAuth）へ

[English](0003-ssh-to-connections.md) | 日本語

- 状態: 確定（Phase 2）。旧 `docs/08-bitbucket.md`（SSH 鍵モデル・陳腐化のため削除済み）を置換
- 関連: [HANDOFF §6.6](../HANDOFF.md) / [dev/05 API 契約](../build/05-api.ja.md)（旧 api-agent §7.0 API 表面地図） / [dev/07 §7.6 シークレット管理と封筒暗号](../build/07-security.ja.md#76-シークレット管理と封筒暗号)（旧 security §4.4）

## 背景

当初設計（旧 08）は **1 ユーザー 1 SSH 鍵 + Bitbucket へ手動登録**で、git は `git@bitbucket.org:...` の
SSH URL を使う方針だった。しかし前段の Google oauth2-proxy が全リクエストを認証ゲートするため、
リダイレクト型 OAuth コールバックは壁に当たり、また SSH 鍵の手動登録は UX が重い。WebUI で完結する
統合認証へ寄せたい。

## 決定

**SSH 鍵モデルを廃し、Connections（HTTPS トークン/OAuth）へ格下げ。** CP 利用者がプロバイダごとに
WebUI で認証し、得た資格情報を**暗号化してコンテナ home に保存 → コンテナ内の git/claude が利用**する。
ターミナル CLI 認証は不要。秘密は CP を保持・解釈しない（`proxyAgentREST` で Agent へ委譲）。

- **GitHub** = Device Flow（`github.com/login/device` で承認）または PAT（`x-access-token`）。scope `repo`。
- **Bitbucket** = Auth Code Grant（callback はブラウザの Google cookie で oauth2-proxy を素通り＝**前段改変不要**）
  または email + API token。失効トークンは git credential helper（`workspace-agent bitbucket-cred`）が
  `bitbucket.json` を読み自動 refresh。
- 保存は暗号ストア `secrets.enc`（AES-256-GCM, 0600）。統一 cred helper `workspace-agent cred` が都度復号で
  出力し**平文ファイルを作らない**。鍵の保護は封筒暗号（[0005](0005-envelope-custodian.ja.md)）。
- SSH 鍵は任意の後付けに格下げ（既定では使わない）。

## 帰結

- 旧 `/sshkey`・`/sshkey/rotate` エンドポイント、`SshKey` テーブル、known_hosts 配布は廃止。
- clone/fetch/**push** が透過認証。private repo も統一 cred helper で通る。submodule の SSH URL は
  clone 後に HTTPS へ best-effort 書換（[HANDOFF §6.10.5](../HANDOFF.md)）。
- CP は Bitbucket/GitHub のトークンを預からない（漏洩面が小さい）。責任範囲は各ユーザーに閉じる。
