# 01. 要件定義

## 1.1 ゴール

社内メンバーが Web ブラウザから Claude Code を効率良く共同利用できるサービスを構築し、
AWS 上でホストする。各ユーザーは隔離された環境で Bitbucket リポジトリを扱い、
Claude セッションを起動・操作・管理できる。

## 1.2 用語

| 用語 | 定義 |
|------|------|
| Workspace | ユーザー 1 人に対応する永続コンテナ環境。`~/.claude`・暗号ストア・working copy を保持 |
| Working copy | Workspace 内に clone した Bitbucket リポジトリの作業ディレクトリ |
| Session | Working copy に紐づく Claude CLI プロセス（tmux セッション 1 本）|
| Control Plane | Workspace の生成・破棄・仲介を行うバックエンド（コンテナ群の外側）|
| Workspace Agent | 各 Workspace コンテナ内に常駐し、ターミナル・git・セッション操作を仲介する小さなプロセス |
| Console | ブラウザで動く Web フロントエンド |

## 1.3 アクター

- **メンバー** — 社内開発者。自分の Workspace を操作する。
- **管理者** — ユーザー追加・削除、リソース上限、監査ログ閲覧。
- **システム** — Control Plane / Workspace Agent。

## 1.4 機能要件

### A. 認証・アクセス制御
- A1. Web コンソールの認証は Google OAuth。許可ドメインで制限（`hd` クレーム検証）。
- A2. 許可リスト（メール / ドメイン）に基づくアクセス制御。
- A3. Claude 本体の認証は各ユーザーが自分のアカウントで `/login`。
- A4. コンソールから各ユーザーの Claude `/login` 認証状況を可視化（ログイン済み / 期限切れ / 未ログイン）。
- A5. 認証切れ時に、コンソール経由で再ログインを誘導できる。

### B. Bitbucket 連携
- B1. リポジトリの clone。
- B2. checkout / ブランチ変更 / ブランチ作成。
- B3. working copy の状況確認（`git status` 相当、現在ブランチ、差分概要、未コミット有無）。
- B4. git 認証情報の登録（Console から HTTPS トークン/OAuth で接続）。当初の SSH 公開鍵生成・手動登録から
  Connections へ格下げ（[decisions/0003](../decisions/0003-ssh-to-connections.md)）。

### C. Workspace / サンドボックス
- C1. ユーザー毎に隔離されたコンテナ環境を生成。
- C2. clone したディレクトリ単位、またはユーザー単位でサンドボックスを構成できる。
- C3. ユーザー毎の `~/.claude`・資格情報（暗号ストア）・working copy を永続管理。
- C4. Workspace の起動・停止・再作成。

### D. Claude セッション・ターミナル
- D1. clone したディレクトリに対して新しい Claude セッションを作成。
- D2. 稼働中の Claude CLI の一覧・状態確認・停止。
- D3. Web 上でのターミナル操作（PTY 接続、tmux アタッチ）。
- D4. Claude セッションの管理（再開 / 新規 / 履歴参照）。

### E. 設定管理
- E1. ユーザー毎の `settings.json` を Web から閲覧・編集。
- E2. remote-control（`remoteControlAtStartup`）を有効化・管理。
- E3. 設定テンプレート（管理者既定）+ ユーザー上書き。

## 1.5 非機能要件

| 区分 | 要件 |
|------|------|
| 規模 | 同時 〜20 人。1 ユーザー複数セッション。 |
| 隔離 | ユーザー間でファイル・プロセス・ネットワーク・認証情報を相互不可視に。 |
| 永続性 | Workspace 再起動・コンテナ再作成後もホームと clone と認証情報を保持。 |
| 可用性 | 個々の Workspace 障害が他ユーザーへ波及しない。Control Plane は冗長化検討。 |
| 監査 | ユーザー操作（clone / セッション起動 / 設定変更）を記録。 |
| コスト | アイドル時はスケールダウン（scale-to-zero）でコスト最適化。 |
| 運用 | イメージ更新でフリート全体の Claude/ツールを一括更新。 |

## 1.6 確定事項（再掲）

| 論点 | 決定 |
|------|------|
| Claude 認証 | 各ユーザーが自分のアカウントで `/login` |
| 分離方式 | ユーザー毎コンテナ |
| 規模 | 〜20 人 |
| 永続化 | EBS/EFS で永続化 |
| git 認証 | Console から HTTPS トークン/OAuth（Connections）。秘密を CP は保持しない |
| 技術スタック | Console=React+Vite+xterm.js / Backend=Go |

## 1.7 未決事項（今後詰める）

> Phase 0〜2 + P3-1〜5 完了で多くが確定済み。下記は**現に残る**もの。決定の経緯は [decisions/](../decisions/)、
> 現状は [HANDOFF](../HANDOFF.md)、前向きの計画は [ロードマップ](../roadmap.md)。

1. **scale-to-zero / idle-stop** — アイドル検出とコールドスタート許容時間。オンプレ単一ホストの RAM 逼迫で
   優先度が上がっている（ロードマップ P3-9）。
2. **課金・上限** — ユーザー個人の Claude サブスクを使う前提で会社が負担する範囲（BYO・社内 showback は P3-9）。
3. **ディスククォータの強制** — メータリング止まり（FS ハードクォータは後付け、P3-4 スコープ外）。

### 解決済み（確定）
- **コンテナ実行基盤 / 永続ストレージ** → `aws`=ECS(Fargate)+EFS、`local`=Docker+bind mount（[aws](aws.md) / [ポータビリティ](portability.md)）。
- **`/login` の対話フロー** → 方式 A（コード貼り戻し）。auth と onboarding は別物が最終結論（[decisions/0002](../decisions/0002-claude-auth-onboarding.md) / [architecture §2.6](architecture.md#26-claude-login-フロー)）。
- **Control Plane ↔ Agent の認証** → per-container `AGENT_TOKEN`（Bearer）+ ネットワーク分離（[api-agent §7.5](api-agent.md#75-control-plane-との認証)）。
- **git 認証** → SSH 鍵廃止・Connections へ（[decisions/0003](../decisions/0003-ssh-to-connections.md)）。
- **技術スタック** → **React+Vite + Go**（[decisions/0004](../decisions/0004-vanilla-to-react.md)）。
