---
audience: "配備を建て、上げ、守る人（ホストの端末と配備管理者の権限を持つ情シス / SRE）"
source_of_truth: "コマンドは `deploy/` 配下の runbook、各手順が何を決めているかはこの棚"
---

# 配備を運用する

[English](README.md) | 日本語

Agent Fleet を自分の基盤で動かす人の「やりたいこと」を、1 ページに 1 つずつまとめています。

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
| [ECS 構成の全体像](aws-ecs/overview.ja.md) | どのスタックが何を作り、どの順番に建てるかを知る |
| [ECS に構築する](aws-ecs/install.ja.md) | アカウントを準備し、スタックをデプロイしてイメージを push し、サインインできるところまで進める |
| [パラメータで任意の機能をオンにする](aws-ecs/options.ja.md) | プレビュー用のサブドメイン・KMS・リモートの MCP・提供ルート・タイマーと外部のエンジン・WAF をオンにする |
| [データベースに届かないときのアラームを設定する](aws-ecs/alarms.ja.md) | Control Plane がデータベースに届かなくなったら、メールで知らせを受け取る |
| [アップグレードする](aws-ecs/upgrade.ja.md) | `update.sh` で新しいリリースに上げ、戻し、パラメータを空に戻す |
| [テンプレートの大きさの上限に気をつける](aws-ecs/template-size.ja.md) | 51,200 バイトを超える `30-ingress` を S3 経由でデプロイする |
| [デプロイに必要な IAM 権限](aws-ecs/iam.ja.md) | デプロイする人や CI に、サービスごとの最小の権限を与える |
| [ワークスペースの大きさを決める](aws-ecs/workspace-size.ja.md) | 全ワークスペースの CPU・メモリ・作業ディスクの既定と、キャッシュを EFS から移す条件を決める |
| [決まった時刻の起動を有効にする](aws-ecs/schedules.ja.md) | スケジューラーを止める、または起動の遅い配備で待ち時間を延ばす |
| [外向きの IP アドレスを固定する](aws-ecs/egress-ip.ja.md) | 許可リストに載せるアドレスを調べ、作り直しても変わらないようにする |
| [外向きの通信をゲートウェイで制御する](aws-ecs/egress-gateway.ja.md) | ワークスペースの通信を egress ゲートウェイに通し、アドレスを登録し、一時停止し、元に戻す |
| [日本語の読み上げを足す](aws-ecs/tts.ja.md) | VOICEVOX のエンジンのスタックを建て、自動の起動と停止を調整する |
| [推論エンジンを足す](aws-ecs/engines.ja.md) | GPU のエンジンのスタックを建て、モデルを手で S3 に置き、制御を調整する |
| [費用を抑える](aws-ecs/cost.ja.md) | 常にかかる費用を知り、つまみを選び、動いているスタックを S3 ゲートウェイエンドポイントに移す |
| [EFS をバックアップ・復元する](aws-ecs/backup.ja.md) | EFS の毎日のバックアップを有効にし、1 人分かファイルシステム全体を戻す |
| [スクリプトで構築・一時停止・撤去する](aws-ecs/lifecycle.ja.md) | 配備を記録し、一時停止と再開をし、各スクリプトが何をどの順でするかを知る |
| [撤去する](aws-ecs/teardown.ja.md) | 止まらない順番ですべてを消し、残るものを知る |
| [スロットを入れ替える・止める](aws-ecs/slot-maintenance.ja.md) | `ecs-ec2` で古いスロットを入れ替え、応答しないスロットに気づいて止める |
| [ECS で困ったとき](aws-ecs/troubleshooting.ja.md) | 構築・アップグレード・起動のつまずきを、症状から探す |

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

## Docker Compose

| ページ | できること |
|---|---|
| [Docker Compose で構築する](compose/install.ja.md) | `.env` を書き、ソースからか公開リリースのイメージで起動し、最初の管理者としてサインインする |
| [macOS（Apple Silicon）で動かす](compose/macos.ja.md) | Mac の Docker のエンジンか Linux の VM で、1 人用に動かす |
| [ワークスペースから CP に届かないとき](compose/workspace-listener.ja.md) | ワークスペース専用のリスナーで、ホストの外に出ない経路を渡す |

## ネイティブパッケージ

| ページ | できること |
|---|---|
| [ネイティブパッケージを入れる](native/install.ja.md) | Docker のない 1 人用の Linux や WSL2 に入れて、`af start` で使い始める |
| [ネイティブパッケージを更新する](native/update.ja.md) | 新しい版を用意して再起動で切り替え、版を固定し、閉じたネットワークに持ち込む |
| [systemd でサービスとして動かす](native/service.ja.md) | `af start` をユーザーのサービスにして、端末を閉じても動かし続ける |
| [リセット・アンインストールする](native/uninstall.ja.md) | データを消してやり直すか、ホストから取り除く |

## そのほかの構成

| ページ | できること |
|---|---|
| [WSL で個人用に動かす](wsl.ja.md) | WSL2 の中の Docker で、サインインなしに 1 人で試す |
| [EC2 1 台に構築する](aws-ec2-single.ja.md) | CloudFormation で EC2 を 1 台建て、リリースのバンドルで Docker Compose の配備を動かす |

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

各形態が何に対応しているかは [配備形態](../ref/deploy-targets.ja.md)、誰が何をできるかは [ロールとできること](../ref/roles.ja.md) にあります。
