---
audience: "配備を建て、上げ、守る人（ホストの端末と配備管理者の権限を持つ情シス / SRE）"
source_of_truth: "コマンドは `deploy/` 配下の runbook、各手順が何を決めているかはこの棚"
---

# 配備を運用する

[English](README.md) | 日本語

Agent Fleet を自分の基盤で動かす人の「やりたいこと」を、1 ページに 1 つずつまとめています。
コマンドは各形態の runbook にあり、ここの各ページはそれが何を決めるのかを書きます。

## はじめに

| ページ | できること |
|---|---|
| [セルフホストの全体像](overview.ja.md) | 何ができ、何を用意し、運用する人が何に責任を持つかを知る。採用を判断する前に読む |
| [デプロイ先を選ぶ](choose.ja.md) | compose・native・AWS・Kubernetes から形態を選び、AWS の費用と出口の経路を比べる |

## インストール

| ページ | できること |
|---|---|
| [インストールする](install.ja.md) | 秘密を作り、サインインを設定して起動し、最初の管理者としてサインインしてメンバーを迎える |
| [テナントとプロビジョニングの方式を決める](install-decisions.ja.md) | 証明書の取り方、招待制か自動か、テナントの分け方と、テナントごとのサインインを決める |

## 日々の運用

| ページ | できること |
|---|---|
| [バックアップして復元する](run/backup-restore.ja.md) | `backup.sh` を定期実行し、別のホストに復元する |
| [アップグレードする](run/upgrade.ja.md) | 新しい版に上げ、ワークスペースを新しいイメージに移す |
| [閉じたネットワークに入れる](run/air-gapped.ja.md) | 外に出られないホストに、イメージ・TLS・CLI・図のアイコンを用意する |
| [全エージェントに指示を配る](run/fleet-policy.ja.md) | フリート方針で、すべてのエージェントに運用のルールを読ませる |
| [opencode の共有サーバーを運用する](run/opencode-server.ja.md) | ターミナルのセッションのサーバーと opencode の版を管理する |
| [インターネットに公開する](run/public-internet.ja.md) | 探索が来たときに何が届くかを知り、受け口を絞る |
| [複数のデプロイを見分ける](run/deployment-label.ja.md) | 配備ごとに色とラベルを付ける |
| [アイドル停止と強制停止を使う](run/idle-stop.ja.md) | 自動停止の時間を決め、止まらないワークスペースを止め、配備を一時停止する |

## サインイン

| ページ | できること |
|---|---|
| [サインインの方法を設定する](signin/overview.ja.md) | どの IdP でも共通の準備、リダイレクト URI、設定の置き場所を知り、動いたことを確かめる |
| [Google でサインインする](signin/google.ja.md) | Google Cloud Console でクライアントを作り、`.env` に書く |
| [Microsoft Entra ID でサインインする](signin/entra.ja.md) | Entra でアプリを登録し、issuer を自社のディレクトリに固定する |
| [GitHub でサインインする](signin/github.ja.md) | GitHub の組織のメンバーだけが入れるようにする |
| [そのほかの OIDC でサインインする](signin/oidc.ja.md) | Okta・Keycloak・Auth0・Cognito・GitLab などをつなぐ |
| [テナント独自のサインインを承認する](signin/tenant-idp.ja.md) | テナントが登録した IdP を確かめて承認し、止める |

## 困ったとき

| ページ | できること |
|---|---|
| [症状から原因を探す](diagnose/checklist.ja.md) | ログとヘルスを見て、症状の表から原因を絞る。コンテナから Docker を動かす 3 つの制約、TLS、よくある質問 |
| [ログインできない](diagnose/cannot-login.ja.md) | サインインが拒否される・ボタンが出ない・外した人がまだ使えるときに、原因を探す |

## セキュリティ

| ページ | できること |
|---|---|
| [脅威モデルと残るリスクを知る](secure/threat-model.ja.md) | 隔離の境界がどこにあるかを知り、残るリスクに備え、脆弱性を報告する |
| [配備の鍵を扱う](secure/deployment-keys.ja.md) | `AF_MASTER_KEY` と `AF_COOKIE_SECRET` の要件を守り、鍵のない配備に鍵を入れ、古い値を封じる |
| [外向きの通信を制御する](secure/egress.ja.md) | ワークスペースの外向きの通信を観測し、許可リストで絞る |
| [MCP と外部接続のサインインを運用する](secure/mcp.ja.md) | `/mcp` を OAuth で開き、信頼するクライアントのホストを決め、届いたものをログで読む |
| [開発用サインインを使う](secure/dev-signin.ja.md) | `AUTH=dev` を使ってよい場所を知り、開発用 OIDC IdP をオン・オフする |
| [そのほかの運用上の制御](secure/controls.ja.md) | プレビュー用ドメイン、サインインの許可リスト、監査ログ、ホストのクラウドの身元を正しく保つ |
| [アクセスを取り消す・super_admin を引き継ぐ](secure/offboarding.ja.md) | 人を外し、全員のセッションを切り、配備管理者を交代させる |

## 鍵

| ページ | できること |
|---|---|
| [保存時の鍵を AWS KMS にする](keys/kms.ja.md) | AWS で、Control Plane が封じるものを KMS で守り、切り替える前の値を封じ直す |
| [ホームごとに鍵を分ける](keys/home-keys.ja.md) | AWS で、メンバーの資格情報ストアをホームごとの鍵で封じる |
| [ブリッジトークンを入れ替える](keys/bridge-tokens.ja.md) | ワークスペースのトークンを、1 人分または全員分まとめて無効にする |

## ECS

| ページ | できること |
|---|---|
| [スロットを入れ替える・止める](aws-ecs/slot-maintenance.ja.md) | `ecs-ec2` で古いスロットを入れ替え、応答しないスロットに気づいて止める |

## 推論エンジン

| ページ | できること |
|---|---|
| [自前の GPU で推論エンジンを動かす](engines/gpu.ja.md) | 役ごとにモードを選び、自動停止の時間と GPU のインスタンスクラスを決める。全画像から除外する語を決める |
| [推論エンジンにモデルを取り込む](engines/models.ja.md) | Hugging Face と Civitai でモデルを探して取り込み、有効にする。テナントに取り込みを許可する |
| [推論エンジンの状態と履歴を見る](engines/status.ja.md) | いま動いているインスタンスと要求を読み、14 日の稼働実績を見る |
| [読み上げエンジンを動かす](engines/read-aloud.ja.md) | VOICEVOX のエンジンを用意して動かし、共通の読み仮名辞書を作る |
| [自前の ComfyUI で画像を生成する](engines/comfyui.ja.md) | 画像エンジンを自分のネットワークの ComfyUI に向け、モデルを登録し、届く経路を閉じる |
| [別のデプロイのエンジンを借りる](engines/borrow.ja.md) | 自分のホストの配備から、AWS の Agent Fleet のチャットと画像のエンジンを使う。貸す側としてトークンを発行する |
| [LAN の llama.cpp でチャットする](engines/llama-cpp.ja.md) | チャットのエンジンを自分のネットワークの llama-server に向ける |

## Kubernetes

| ページ | できること |
|---|---|
| [Kubernetes 構成の全体像](kubernetes/overview.ja.md) | 何がどこに作られるか、クラスターが満たすべき前提条件、GKE のディスクの割り当てを知る |
| [GKE に構築する](kubernetes/gke.ja.md) | Terraform とマニフェストで、プロジェクトの準備からロードバランサの確認まで進める |
| [ほかのクラスターに構築する](kubernetes/other-clusters.ja.md) | GKE 以外のクラスターに、汎用のオーバーレイから建てる |
| [CP をアップグレードする](kubernetes/upgrade.ja.md) | バックアップを取って新しい版に上げ、必要なら戻す |
| [バックアップ・復元する](kubernetes/backup.ja.md) | CP のディスクにスナップショットの予定を入れ、データベースとディスクの復元を予行する |
| [アラートを設定する](kubernetes/alerts.ja.md) | CP・証明書・起動できないワークスペース・ノードのディスクを Cloud Monitoring で見張る |
| [ノードを入れ替える・止まったノードに対処する](kubernetes/nodes.ja.md) | セッションを切らずにノードを入れ替え、応答しないノードから回復し、破棄の残りを片付ける |
| [一時停止する・費用を見る](kubernetes/pause.ja.md) | `pause.sh` で配備を眠らせ、費用を知る |
| [撤去する](kubernetes/teardown.ja.md) | 配備と残ったものを消す |

次の章の内容は、上のページに移りました: [01 配備形態を選ぶ](01-choose.ja.md)・[02 初期構築](02-install.ja.md)・[03 動かし続ける](03-run.ja.md)・[04 守る](04-secure.ja.md)・[05 サインイン方式](05-signin.ja.md)・[06 切り分ける](06-diagnose.ja.md)・[11 ecs-ec2 のスロットプール](11-slot-pool.ja.md)・[12 保存時の鍵](12-keys.ja.md)・[07 推論エンジンと読み上げ](07-engines.ja.md)・[08 自前の ComfyUI で画像を生成する](08-image-engine.ja.md)・[09 別の配備のエンジンを借りる](09-borrowed-engine.ja.md)・[10 自前の llama.cpp でチャットの推論を担う](10-llm-lan.ja.md)

各形態が何に対応しているかは [配備形態](../ref/deploy-targets.ja.md)、誰が何をできるかは [ロールとできること](../ref/roles.ja.md) にあります。
