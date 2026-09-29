---
audience: "コードを変える人（新規参画・将来の保守者・エージェントのセッション）"
source_of_truth: "コード（この棚は地図と設計意図）"
updated: "2026-09"
---

# Agent Fleet をつくる

[English](README.md) | 日本語

この棚は「**どう動いているの？**」に答えます。3 プロセスと各々の持ち物、認証の 2 層、
API の境界、データモデル、脅威モデル、外部連携、ビルドとテストの作法、そして
エージェント種別やデプロイ形態を足すときになぞる型。

この棚に何を書き、何を他の棚に任せるか、どう書くか（ワイヤ契約と grep できるアンカー。
行番号は書かない）は [CONVENTIONS §9](../CONVENTIONS.ja.md#9-棚ごとの担当) と
[§4](../CONVENTIONS.ja.md#4-棚ごとの語彙) にあります。次のものは複製せずリンクします。

- 配備の運用: [guide/operate/](../../guide/operate/README.ja.md)
- メンバーや管理者が見るもの・行うこと: [guide/member/](../../guide/member/README.ja.md)、
  [guide/admin/](../../guide/admin/README.ja.md)
- どの種別・プロバイダ・形態・ロールが何に対応するか: [guide/ref/](../../guide/ref/README.ja.md)
  （[CONVENTIONS §6](../CONVENTIONS.ja.md)）
- なぜそうなっているか（退けた案を含む）: [decisions/](../decisions/)
- まだ誰かがやるべきこと: GitHub Issue（[CONVENTIONS §10](../CONVENTIONS.ja.md#10-未完の作業は文でなく-issue-に)）

## 更新トリガ

| 変えたもの | 更新するもの |
|---|---|
| API グループ・パス | [05](05-api.ja.md) と、それを受けるコンポーネントの章 |
| マイグレーション | [06](06-data.ja.md) のエンティティ。作法そのものを変えたら [§6.5](06-data.ja.md#65-マイグレーション作法) も |
| 認証・暗号・隔離・監査 | [07](07-security.ja.md)。監査の書き込み点は [05 §5.5](05-api.ja.md#55-監査の書き込み点) |
| 外部プロバイダ | [08](08-integrations.ja.md) |
| エージェント種別、またはその CLI のサインイン | [04 §4.3](04-agent.ja.md#43-エージェント種別を統合する型) と [08](08-integrations.ja.md)。新しい種別は [20](20-add-an-agent.ja.md) の末尾に挙がるものも負う |
| デプロイ形態・アダプタ・変数 | [09](09-deploy.ja.md)。新しい形態は [21](21-add-a-deploy-target.ja.md) の末尾に挙がるものも負う |
| 種別・プロバイダ・形態・ロールが何に対応するか | [guide/ref/](../../guide/ref/README.ja.md) の表だけ。他には書かない |
| ビルド/反映/テストの仕組み | [10](10-development.ja.md) |
| ファイルの置き場（リファクタ） | [90](90-code-map.ja.md) と、動かしたファイルをアンカーに名指している全章——棚を旧パスで grep する |
| 利用者から見える機能 | 該当章と、[CONVENTIONS §8](../CONVENTIONS.ja.md#8-機能の完了条件) の完了条件 |

## 章立て

**はじめて？** [00](00-project-context.ja.md) → [01](01-architecture.ja.md) →
[05](05-api.ja.md) → [06](06-data.ja.md) → [10](10-development.ja.md)。
**特定コンポーネント担当？** [01](01-architecture.ja.md) → その章。grep の起点は
[90](90-code-map.ja.md)。**セキュリティレビュー？** [07](07-security.ja.md) →
[08](08-integrations.ja.md) → [01](01-architecture.ja.md)。**種別や形態を足す？**
[20](20-add-an-agent.ja.md) か [21](21-add-a-deploy-target.ja.md)。

| | |
|---|---|
| [00 プロジェクトの前提](00-project-context.ja.md) | 他の章が拠って立つ前提: 現状・決着している仮定（v1）・何から作られたか |
| [01 アーキテクチャ](01-architecture.ja.md) | 提供モデル・用語・3 プロセス・認証 2 層・主要フロー・アダプタの seam・作ったもの/作っていないもの |
| [02 Console](02-console.ja.md) | ブラウザ側の SPA: スタック・状態とサーバ同期・ペイン・情報設計・表示の体系・i18n・ビルドとテスト |
| [03 Control Plane](03-control-plane.ja.md) | 責務・リクエストの一生・Runtime 抽象・MCP サーバ・バックグラウンドジョブ・自前エンジン |
| [04 Agent](04-agent.ja.md) | セッションモデル・種別の統合・状態バッジ・チャット・転写と使用量・秘密情報・Workspace イメージ・ブラウザマネージャ |
| [05 API](05-api.ja.md) | 2 つの境界の地図（全ルートはルートのゴールデン）・中継経路・横断規約・監査の書き込み点 |
| [06 データ](06-data.ja.md) | ストアの構成・エンティティとその関係・DB に無いもの・マイグレーション作法 |
| [07 セキュリティ](07-security.ja.md) | 脅威モデル・隔離・認証 2 層・CP ↔ agent 認証・封筒暗号・監査・egress |
| [08 外部連携](08-integrations.ja.md) | 外部プロバイダと、それが落ちる 2 つの型（CP がコールバックを持つか否か）、各々の契約 |
| [09 デプロイ](09-deploy.ja.md) | 形態・アダプタとそのつまみ・入口・env 索引・AWS 形態・バックアップと更新の前提・費用 |
| [10 開発](10-development.ja.md) | リポジトリ構成・変更を見る手順・テスト・コミットとブランチ・文書 |
| **[20 エージェント種別を足す](20-add-an-agent.ja.md)** | 先に決めること・種別が埋める面・実際に踏んだ罠・検証 |
| **[21 デプロイ形態を足す](21-add-a-deploy-target.ja.md)** | アダプタが負う契約・アダプタの仕事ではないもの・費用とレイテンシ・検証 |
| [90 コードマップ](90-code-map.ja.md) | ディレクトリごとの grep の起点——目録ではなく例 |
| [91 内部 git](91-internal-git.ja.md) | テナント内の git ホスティング: この形の理由・保存先・トークンモデル・接続点 |
| [92 TUI の駆動](92-driving-a-tui.ja.md) | キー列で駆動するモーダル画面の検証と、CLI 更新のたびのチェックリスト |
| [93 worktree の依存](93-worktree-deps.ja.md) | worktree が共有するもの / 重複するもの（言語別・実測）|
