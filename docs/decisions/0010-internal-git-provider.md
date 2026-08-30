# 0010. テナント内部 git プロバイダ — bare + git-http-backend を CP に自ホスト

- 状態: **採用・P1 実装済み**。設計は [reference/internal-git-provider](../dev/91-internal-git.md)。
- 関連: [0001](0001-self-host-vs-saas.md)（SaaS 断念・自ホスト）/ [0003](0003-ssh-to-connections.md)（git 認証＝Connections）/
  [0005](0005-envelope-custodian.md)（封筒暗号）/ [architecture](../dev/01-architecture.md)

## 背景

テナント内でリポジトリを**フリート内に閉じて**持ちたい要求（A. チーム内共有 / B. エージェント用の
private scratch・seed / C. コードを外に出さない）。現状は外部プロバイダ（GitHub/Bitbucket）への
Connections のみで、外部アカウント前提・コードが外に出る。

## 決定

**テナント毎の bare リポジトリを Control Plane が smart-HTTP（`git http-backend`）で自ホストし、
既存のプロバイダ抽象に載せる。** PR/レビュー/CI は作らない（当面 read/write の 2 段）。

- **設置は CP**: テナントを知る唯一の共有面。per-user コンテナは横断共有できない。
- **bare + git-http-backend**: 最小コードで clone/fetch/**push** が成立。閲覧は既存 SCM（コミット
  グラフ）を clone 後にそのまま流用。
- **認証はトークン注入型 cred helper を流用**: membership 毎のテナントスコープ token を暗号ストア
  `s.Git[internal-host]` に注入 → 統一 cred helper が任意ホストを既に配信するので透過。CP 側の
  smart-HTTP は Basic の password を token として検証し、`<slug>`==tenant を全リクエスト強制。
- **token は membership 毎の決定的 HMAC（token 用 DB なし）**: 署名鍵はデプロイ master key 派生、
  トークンは `membershipID` を内包し tag を HMAC。CP は同じ関数で再生成できるので注入は冪等・平文保存
  不要。検証時は埋め込み membership を**ライブ参照**して (tenant, role) を解決し、無効化された
  membership は即座に失効する（下記「捨てた選択肢: PAT 表流用」参照）。
- ストレージは `${DATA_DIR}/git/<slug>/<repo>.git`（既存の永続ボリューム＋bind）。

### 捨てた選択肢

- **AWS CodeCommit**: 2024 年に新規顧客受付終了で新規土台に不可。加えて IAM 認証がトークン注入型の
  統一 cred helper と噛み合わず、テナント毎 IAM は重い。プロバイダ抽象にも乗せづらい。
- **Gitea/Forgejo を最初から内包（②）**: org/権限/Web 操作まで揃うが、別アプリの運用が増える。
  A–C には PR/権限マトリクスが不要なので過剰。将来 PR/CI が要る段で載せ替える段階戦略を採る。
- **外部 SaaS（GitHub/GitLab）に寄せる**: C（外に出さない）に反する。
- **PAT 表流用でトークンを保管**（当初の第一候補）: 却下。PAT は hash のみ保存で**平文を復元できない**ため、
  CP がワークスペースへ注入する平文を保持できず注入が非冪等になる（毎回再発行→再注入）。加えて machine
  token がユーザーの Console PAT 一覧を汚す。トークンは自動管理で membership 状態が実ゲートなので、
  個別失効・有効期限といった PAT の利点は不要 → **決定的 HMAC（DB なし）**が素直。
- **Agent 経由で内部 repo を列挙**（`git_remote.go` の switch に internal case）: Agent→CP 認証が
  必要で複雑。CP がリポの所有者なので **repo 一覧/作成は CP ネイティブ**に分岐する方が素直。

## 帰結

- 追加は 3 ブロック（CP 側 git サーバ／管理 API／token 注入）＋小さなプロバイダ登録
  （`gitHosts`・`RepoPicker`・`GitTab`・`handleConnectionsGet`）。clone/閲覧/コミットは**無改造**で動く。
- CP に **git 実行面が増える**（新たな攻撃面）。refspec/パス検証を厳格化し、slug 封じ込めと
  tenant 一致を必須にする。
- CP イメージに `git`（http-backend）依存が入る（`control-plane/Dockerfile`）。token 用のテーブルは
  作らず（決定的 HMAC）、新規 migration は `git_repo` のみ。clone URL は `PUBLIC_BASE_URL` 由来。
- 将来 PR/CI が必要になれば ② へ載せ替え可能（本決定は最小の土台に留める）。
