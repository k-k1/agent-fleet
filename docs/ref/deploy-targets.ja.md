# デプロイ形態 — どこに何が在るか

[English](deploy-targets.md) | 日本語

Audience: 全員（[operate/](../operate/README.ja.md) には決定的）
Source of truth: この表（行は Control Plane が受け付ける runtime プロファイルと突き合わせ）
Updated: 2026-08

コアはどの形態でも同じで、変わるのは周縁のアダプタだけです。だから差は小さく具体的で、
**だからこそ正確に書く価値があります**——ここで「うちの配備では動く」は最も高くつく
ドキュメントの誤りです。

| 形態 | ワークスペースの実体 | ホームの置き場 | 選ぶとき |
|---|---|---|---|
| docker | ホストの Docker デーモン上のコンテナ | ホスト上の bind mount したディレクトリ | オンプレの既定。1 台をチームで共有する |
| native | Docker を使わない、サンドボックスされたホストプロセス | ホスト上のディレクトリ | Docker を入れられない（素の WSL2 など）。**単独利用のみ**——コンテナ境界が無いので、共有モードでは起動を拒否する |
| ecs | AWS ECS / Fargate のタスク | EFS | AWS で、インスタンスを管理したくない |
| ecs-ec2 | プールから取った EC2 スロット上のタスク | ユーザー毎の EBS ボリューム | AWS で、起動レイテンシとディスク性能がインスタンス管理に見合う |

`docker` は `local`、`ecs` は `aws`、`native` は `wsl` という綴りでも通ります。
それ以外は**起動時に失敗**します（黙って docker に落ちたりしません）。
`ecs` と `ecs-ec2` をフラグでなく**別プロファイル**にしてあるのは意図的です。EC2 プールは
「実績のある 2 リソースのワークスペース」を「6 リソースのもの」に取り替えるので、
配備は明示的に選び、**コードを戻さずこの 1 つの値だけで引き返せる**ようにしてあります。

## 能力の差

| 機能 | docker | native | ecs | ecs-ec2 |
|---|:--:|:--:|:--:|:--:|
| 複数ユーザーの相互不可視な利用 | ✓ | — | ✓ | ✓ |
| ユーザー毎の CPU / メモリ上限 | ✓ | — | ✓ | ✓ |
| ユーザー毎のディスクサイズ指定 | — | — | ✓ | ✓ |
| アイドル自動停止 | ✓ | ✓ | ✓ | ✓ |
| ホームを保ったまま停止 / 再開 | ✓ | ✓ | ✓ | ✓ |
| コンテナ内のロール別ドキュメント | ✓¹ | ✓¹ | ✓² | ✓² |
| ブラウザペイン | ✓ | ✓³ | ✓ | ✓ |
| メンバー毎の費用の按分 | — | — | ✓ | ✓ |

¹ ホスト側でステージして、起動時に bind mount する。

² タスクへ mount できるホスト経路が無いので、コンテナが Control Plane の内部
エンドポイントから**同じ部分集合**を取りに行く。決定は 1 つ、配り方が 2 つで、
「このロールに何を見せてよいか」の実装は 1 本。

³ `native` が使う lean イメージは Chromium を焼いていないので、初回にオンデマンドで
取得する。

## 手順の在り処

[operate/](../operate/README.ja.md) を書き上げるまで、runbook は「操作する対象の隣」に
あります。

| 形態 | runbook |
|---|---|
| docker（compose）| [deploy/compose/README.md](../../deploy/compose/README.md) |
| native | [deploy/native/README.md](../../deploy/native/README.md)。個人の WSL2 は [deploy/local/README-wsl.md](../../deploy/local/README-wsl.md) |
| ecs / ecs-ec2 | [deploy/aws/ecs/README.md](../../deploy/aws/ecs/README.md) |
| compose を EC2 1 台に載せる | [deploy/aws/ec2-single/README.md](../../deploy/aws/ec2-single/README.md) |

`ec2-single` は別の runtime プロファイルではありません。**VM 上の `docker`** です。
「AWS を使う」と「インスタンスを自分で管理する」が独立した選択だから存在します。

## どの形態でも同じもの

ワークスペースのイメージと、その中のエージェントは、**どの形態でも同一の成果物**です
（分離の目的がまさにこれ）。本当に差が出るのは隔離の強度・ストレージ性能・egress の
統制手段で、これらは Agent Fleet の性質ではなく**土台の性質**です。
