# 0099. 1 台の VM から配備自身の GPU を——ec2-single が ECS のエンジン役を動かし、置き換えたスタックはリリース確認のときだけ立てる

[English](0099-engines-from-a-single-vm.md) | 日本語

- Status: **proposed**（2026-09-22）・**同日レビュー済み**（末尾の「レビュー」）。実装は無い。
  この形でスタックを立てたことも、請求を読んだこともまだ無い。🔄 **決定 3・4・5・6・7・10 は
  そのレビューで訂正した**——各決定に「何が動いたか」と「どの指摘が動かしたか」を書いてある。
  **P0 はレビューの門が閉じるまで始めない。**
- 🔄 **より安い第一歩の後ろで保留（2026-09-23）。** これを作る前に、ECS 構成をそのまま使い、使わない
  間は手で止める——「運用で覆う案を先にやる」を参照。その請求を 2 週間読んで、差額が作業とレビューの
  リスクに見合うと分かったときにだけ、この ADR を再開する。
- **この文書のために測ったものは何も無い。** 数字にはすべて出所を書いた——
  (a) このリポジトリに既にある実測（`docs/log/67` §67.3・`deploy/aws/ecs/pause.sh`・ADR 0074/0075/0077）、
  (b) 2026-09-22 に読んだテンプレートとコード（「確認した出典」に file:line）、
  (c) **ap-northeast-1 の AWS 定価**——請求書ではなく算術。(c) に乗っている決定はすべて
  「未解決の点」に名前を挙げてある。
- 依頼は 1 文である。**毎日立っている配備が、image と llm の GPU エンジンを自前で持ったまま、
  VM 1 台ぶんの値段で動くこと。ECS 構成は「リリース前に立てて、確認したら畳むもの」にすること。**

## 背景

### ECS 構成の床は、止められない部分である

`pause.sh` が請求の形をそのまま書いている——**稼働 $5.5/日・休止 $2.6/日、その半分は固定**、
そして残るものの名前も: NAT・ALB・RDS・EFS。配備を縮めるとスロットの計算と Control Plane の
Fargate は止まるが、床を作っている 4 つは止まらない。

このリポジトリが読んだ唯一の実請求（`docs/log/67` §67.3・`af-sandbox`・2026-08-01〜16・
**合計 $9.0370**）は、**ほとんど存在していなかった配備**の請求である:
`APN1-NatGateway-Hours` $1.24 を定価 $0.062/h で割ると **16 日で NAT が約 20 時間**、ALB と RDS
の行も同じ桁だ。それは ECS 構成が得意な使い方——立てて、証明して、畳む。

「別のフリートに GPU を貸すために毎日立っている配備」の使い方ではない。そちらで全部を決めるのは
**床**であり、ECS 構成の床は月 $78 ほどで、`teardown.sh` 以外に消す手段が無い。

### エンジン側は、ランタイムが何かを知らない

2026-09-22 に読んだ。この ADR が小さく済む理由はここにある。

- `newEngineRegistry` が要求するのは 2 つだけ——エンジン表（`AF_ENGINES_SSM_PARAM` か
  インラインの `AF_ENGINES_JSON`）と、ここが面倒を見る行が 1 つでもあれば AWS 設定
  （`engines.go:721-775`）。ワークスペースが何の上で動くかは一度も訊かない。
- **`control-plane/engine_*.go` のどのファイルも `AF_RUNTIME` で分岐していない。** 配備が
  エンジンを持つのは宣言したからであって、ECS だからではない。
- クラスタ名は既に `firstEnv("AF_ENGINE_ECS_CLUSTER", "AF_ECS_CLUSTER")` で読まれている
  （`engines.go:812`）——「ワークスペースのクラスタ」と「エンジンのクラスタ」の分離は想定済みで、
  この ADR が使う継ぎ目そのものである。
- エンジンが開けている相手は Control Plane の**セキュリティグループ**であって、タスクの身元では
  ない（`60-engines.yaml:431`）。`CpSg` を着ているものはエンジンに届く。

### 既に自己完結している 3 スタック、しかも時間課金がゼロ

`20-platform` のスタック跨ぎ import は VPC id ただ 1 つ（`20-platform.yaml:139`）。
`60-engines` が取るのはネットワーク（VPC・private サブネット・CP SG）とプラットフォーム
（クラスタ・exec ロール・エンジンの ECR・Cloud Map 名前空間）だけで、**`10-data` にも
`30-ingress` にも `40-ec2-pool` にも触っていない**。

この 3 つを立てたままにする費用は、定価・月あたり: ECS クラスタ $0、IAM $0、起動テンプレート $0、
desired count 0 の ECS サービス $0、Cloud Map の私設 DNS 名前空間 ~$0.50、Secrets Manager 2 本
~$0.80、ECR とモデルバケツは容量ぶん。**ECS 構成の高い半分は `10-data`（RDS・EFS）・
`30-ingress`（ALB・CP の Fargate サービス）・`40-ec2-pool`（スロットとそのボリューム）であり、
エンジンはそのどれも必要としていない。**

### 今日の ec2-single

Docker 入りの Ubuntu VM 1 台に Elastic IP と Route53 の A レコード、その上で compose 構成
（`deploy/aws/ec2-single/cfn.yaml`・`deploy/compose/`）。RDS ではなく SQLite
（`.env.example:374`）、ALB ではなく Caddy、ワークスペースは同じホストのコンテナ。
エンジンを**指す**ことはできる——`AF_COMFY_URL`・`AF_LLM_URL`・`AF_REMOTE_ENGINE_URL`——し、
`guide/ref/deploy-targets.md` の能力表がその帰結を記録している: 「配備が提供する画像エンジン」の
docker の ✓ は「誰かが常時動かしている ComfyUI」の意味であって、**必要なときに買う自前の GPU は
`ecs-ec2` の行にしか無い**。

テンプレートは VM をアカウントの既定 VPC に置き、インスタンスプロファイルを与えない。つまり今日の
VM は、エンジン VPC の中の住所も、箱を買ってよい身元も持っていない。

### 本当に邪魔なものは 1 つ——エンジンのタスクは公開 IP を持てない

エンジンのサービスは EC2 起動タイプの `awsvpc` で `AssignPublicIp: DISABLED`
（`60-engines.yaml:770,880`）——これは選択ではなく、この起動タイプが受け付ける唯一の値である。
だからタスク自身の ENI は、VPC が用意したものを通してしか外に出られない。実際に要るのは:

| タスクがすること | 出口 |
|---|---|
| モデル実体の取得、数十 GB（`fetch-models.sh:99`） | **S3 ゲートウェイエンドポイント＝無料**（`00-network.yaml:167`）。ECR のレイヤ実体も S3 |
| active set の読みと pending の書き——SSM。「1 周 2 回」ではなく監視の反復ごと（`fetch-models.sh:74-80,105,141-149`） | 今日は NAT ゲートウェイ |
| llm の `--api-key`（SSM）と ingest のトークン（Secrets Manager）をタスク開始時に実行ロールで解決（`60-engines.yaml:592-655,727-739`） | 今日は NAT ゲートウェイ |
| ingest タスクの Hugging Face / Civitai からのダウンロード | 今日は NAT ゲートウェイ（`engine_ingest.go:1060-1069,1652-1661` が `AssignPublicIp: DISABLED` を直書き） |

🔄 **そして以上はタスクの ENI だけの話である。** レビュー R3 が、タスクが走る前に**箱**が必要と
するものを足した: ECS エージェントの制御・テレメトリ、ECR の認証とマニフェスト（レイヤの実体は
S3 で無料だが、それ以外は違う）、全コンテナの `awslogs` 配送
（`60-engines.yaml:501-510,681-710,804-834`）。足りていないインターフェースエンドポイントの名前は
このリポジトリ自身が書いている——`ecr.api`・`ecr.dkr`・`logs`・`ssm`（`00-network.yaml:160-166`）。
つまり「S3 エンドポイントがバイトを NAT から外す」は真で、**「越えるのは SSM 2 回だけ」は偽**だった。

ADR 0079 はこの作業を後ろに回したとき、まさにこの問いを予告していた——*「採算は NAT ゲートウェイを
避けられるかで決まる」*。その通りである。NAT ゲートウェイは **$0.062/h＝この文書の 730 時間月で
$45.26、＋ $0.062/GB**（本文の他所にある $36 は、配備が立ちっぱなしではなかった月の実請求から来た
数字）。狙う床は小さいインスタンス 1 台ぶんだ。

## 決定

### 1. VM がエンジンの VPC に入り、`CpSg` を 2 枚目として着る

`deploy/aws/ec2-single/cfn.yaml` に `VpcId` / `SubnetId` を足す（空＝今日の既定 VPC の挙動なので
既存のスタンドアップは変わらない）。そして**2 つ**のセキュリティグループを付ける: `EngineSg` が
既に許している import した `CpSg` と、22/80/443 のための自前の公開グループ。

大事なのは**変えないもの**である: このために `60-engines.yaml` は編集しない。エンジンは
「Control Plane に」開いていて、その Control Plane が VM になっただけ——文はそのまま真である。

これが無いと、どの代案も「エンジンスタックの規則にアドレスを名指しで書く」で終わる。再構築を
跨いで保持できない値を、配備が抱えることになる。

🔄 **初稿が書いていなかったことが 2 つある**（レビュー R2）。`SubnetId` は自由ではない——決定 4 で
VM が出口になる以上、**IGW 経路を持つ public サブネット**に置かねばならない
（`00-network.yaml:69-84,100-117`）。そして **2 枚の SG はどちらも転送パケットを通さない**:
`CpSg` は ALB から CP ポートだけ（`00-network.yaml:186-197`）、公開グループは 22/80/443 だけで、
private サブネットから転送されたパケットは private の送信元のまま届くので両方に弾かれる。
VM には 3 つ目の規則——**2 つの private CIDR からの流入だけ**（`00-network.yaml:39-44`）——が要る。

### 2. エンジンを持つのは配備の性質であって、ランタイムの性質ではない

ec2-single の配備は `.env` の 4 つの値とインスタンスプロファイルでエンジンを宣言する:

```
AF_ENGINES_SSM_PARAM=/af-ws/engines      # 60-engines が書いたエンジン表
AF_ENGINE_ECS_CLUSTER=af-af-ecs-platform # エンジンのクラスタ
AF_ENGINE_SUBNETS=subnet-…,subnet-…      # 買った箱を置く場所（決定 8）
AWS_REGION=ap-northeast-1
```

通常経路のために Control Plane に足すものは他に無い。ECS アダプタ・コントローラ・ラダー・
offers・active set・pending リーダ・ingester・稼働サンプラは、`ecs-ec2` とまったく同じように
表から組み立てられる。

🔴 これは**既にそう振る舞っているコード**についての言明であって、リファクタではない。将来
どこかがエンジンをランタイムプロファイルで門番するなら、壊れるのはこの ADR である。

### 3. 立てたままにするのは `00-network` ＋ `20-platform` ＋ `60-engines`

`10-data`・`30-ingress`・`40-ec2-pool` は**リリース確認用の足場**になる: 同じ 3 スタックの上に
確認のときだけ立て、終わったら消す。エンジン・モデルバケツ（ADR 0085 の台帳）・ECR・エンジン表は
その往復で無傷なので、確認用の配備にかかるのはスタンドアップだけで、再取り込みは要らない。

🔄 **バケツとエンジン表は「走れるエンジン」ではない。そう読める書き方をしたのが誤りだった**
（レビュー R1・最も大きく効いた指摘）。**エンジンが何を載せるかは Control Plane 自身の DB にある**
——有効フラグ・選択モデル・S3 キー・引数・ライセンス同意（`store/migrations/0057_engine_models.sql`）。
起動時、CP は**自分の**カタログから active set を組み直し、**SSM を無条件に上書きする**
（`engine_catalog.go:615-639`・呼び出しは `engines.go:964-968`）。まっさらな SQLite を持つ VM は
**生き残っていた active set を空で上書きする**——エンジンは何も載せずに起き上がる。

よってこの決定に欠けていた半分を足す: **カタログの正本は VM 側であり、確認用の配備はこの役を
決して所有しない——ADR 0079 の `remote` で借りる。** これは決定 6 が必要としている答えと同じ
なので、2 つは「runbook で 3 択」ではなく 1 本の規則になった。正本を逆向きに動かすことは可能だが、
それは衝突規則つきのカタログ移送であり、リリース確認はそれを必要としない。

`standup.sh` は今 `00-network 10-data 20-platform 30-ingress` を必須にしている
（`standup.sh:130`）ので、エンジンの 3 点だけを立てる経路を足す。

### 4. private の外向きは VM を通る。NAT ゲートウェイは条件になる

VM は「誰かがエンジンを要求しうる間」は必ず立っていて、既に公開アドレスを持ち、この設計で唯一の
常時起動の箱である。だから private サブネットの既定経路はそれにする: `SourceDestCheck: false`、
`net.ipv4.ip_forward=1`、cloud-init に `MASQUERADE` 1 行、そして `00-network.yaml` に
`PrivateEgress: nat-gateway | instance | none` を足し、NAT ゲートウェイのリソースを条件に入れる。

- **$0 で、しかも量のあるバイトはそこを通らない**: モデル実体と ECR レイヤは S3 ゲートウェイ
  エンドポイント（無料）を通る。🔄 VM を越えるのは背景の訂正済みの一覧すべて（レビュー R3）——
  監視ごとの SSM、ECR の認証とマニフェスト、ECS エージェントの通信、`awslogs` の配送、
  タスク開始時の Secrets Manager、そして ingest のダウンロード。**バイトは小さいが、1 つでも
  漏らすと箱は idle のまま起き上がり、理由はどこにも出ない。**
- 壊れ方が正直である: VM が落ちていればエンジンは取得できない——そして VM が落ちているなら、
  エンジンを求めている者もいない。
- 🔴 これは経路なので、壊れても誰も報告しない。この決定の完了確認は health ではなく、
  **冷えた状態の一式**である——2 つの private AZ の両方で箱を買い、ECR を冷えた状態で引き、
  ログが届き、llm の鍵が SSM から解決され、実モデルを 1 本取り込む。モデルのダウンロード
  「だけ」では足りない。

管理されたものが欲しい配備のために、NAT ゲートウェイはパラメータ 1 つ隣に残る。

### 5. CP のエンジン IAM は、2 つの役に貼れる形で書く

`60-engines` は既に「自分が持っていない役」に Control Plane 権限を貼っている——import した ARN から
名前を切り出す形で（`60-engines.yaml:284,356,378`）。この仕組みは残す。変えるのは、その役が
`${PlatformStackName}-CpTaskRoleArn` 決め打ちではなく**パラメータ**になることと、`20-platform` が
VM 用に EC2 信頼の役＋インスタンスプロファイルを持ち、CP タスクロールがエンジンのために持っている
のと同じ文（ECS 操作・`ec2:CreateFleet` 一式・`/af-ws/*` の SSM・exec/ingest 役への
`iam:PassRole`・モデルバケツ・エンジンのロググループ・Cost Explorer 読み）を載せることである。

🔴 VM の役は「CP タスクロールに信頼ポリシーを足したもの」**ではない**。タスクロールは EFS・
RDS シークレット・スロットプールの文を抱えていて、ec2-single の配備には使い道が無い。そして
シェルを取れば誰でも読める VM の上の資格情報は、小さい方の集合であるべきだ。

🔄 **「CpTaskRole より小さい」は境界ではない**（レビュー R4——設計の穴ではなく**セキュリティ欠陥**
だった唯一の指摘）。このホストでは Control Plane は**ホストネットワークのコンテナ**
（`docker-compose.yml`）で、各ワークスペースは同じデーモン上の別コンテナであり自前の外向きを持つ
（`runtime_docker.go:280-303`）。インスタンスプロファイルの資格情報は `169.254.169.254` から
読むので、**ワークスペースのコンテナは Control Plane と同じ資格情報に手が届く**——しかもその
資格情報は fleet を買え、エンジンと ingest の役を PassRole できる
（`60-engines.yaml:288-302,318-341`）。ADR 0071 決定 4(a) はワークスペースをエンジンの
ネットワークから遠ざけるために在る。購買権を渡すのは、その決定が拒んでいるものより悪い。

閉じ方は 2 つ、どちらも「覚えておく」ではなく宣言する:

- VM に `MetadataOptions: { HttpTokens: required, HttpPutResponseHopLimit: 1 }`。ホストネット
  ワークの CP は 1 ホップなので役を読めるが、ブリッジのワークスペースは 2 ホップで読めない。
  🔴 **`40-ec2-pool` と `60-engines` の 2 より意図的に厳しい**（`40-ec2-pool.yaml:123-125`・
  `60-engines.yaml:451,521`）——あちらは余分な 1 ホップを要するタスクを抱えており、このホストは
  要求が逆である。
- 文は `EcsDrive` を丸写しせず**エンジンの操作を列挙する**。あの文は `*` に対してサービス削除と
  タスク定義登録まで許している（`20-platform.yaml:197-227`）。

そして P0 のこの決定の試験は**陰性対照**である: ワークスペースのコンテナの中から metadata を
`curl` して、トークンが取れないこと。

🔄 **Cost Explorer はこの役から外す**（レビュー R6）。`dockerFactory.CostProfile()` は
`Available:false` を返し（`internal/runtime/profiles.go:362`）、`startCloudCostPoller` はそれで
Cost Explorer を作る前に return する（`cloudcost.go:112-115`）。docker の CP ではあの権限は
死んだコードである。何を失うかは決定 10 に書いた。

### 6. 🔥 1 つのエンジン役に、Control Plane は 1 つだけ

同じエンジン表を読む Control Plane が 2 つあると、両方が同じサービスの desired count を動かし、
両方が同じ起動テンプレートで箱を買い、両方が active set を書く。コードにこれを検出する仕組みは
無い。運用者に見えるのは「起動した数秒後に止まるエンジン」である。

🔄 **初稿は逃げ道を 3 つ並べて選択を runbook に預けていた。決定 3 の訂正がそれを決める:
役を持つのは VM で、リリース確認の配備は `lifecycle: "remote"` で借りる**（ADR 0079）——
両方の配備が同時に絵を出せる唯一の形であり、カタログを動かさない唯一の形でもある。

🔄 そして**閉じる方向で失敗させる**必要がある。散文では守れないからだ（レビュー R5）: 各 Control
Plane は自分のプロセスで自分のコントローラ goroutine を起こし（`engines.go:1001-1009`）、2 つは
別のストアを持ち、リースはどこにも無い。安い番人は宣言である——エンジン表が所有者を名乗り、
Control Plane は他人の名前が付いた行を管理せず、`standup.sh` は VM が持つ行を主張する配備の
スタンドアップを拒む。条件付きリースは高い版で、1 人が運用する 2 配備には要らない。

### 7. 🔥 VM を止めるのは、エンジンを止めたあと

`pause.sh` がスロットについて同じ教訓を既に抱えている——*CP を先に止めると走っているスロットが
起きたまま取り残される。止めたつもりのあとで一番高いものが課金され続けるという、一番痛い間違え方
である。* GPU の箱は $1.17/時で、それを止めるはずだったのは VM の中のコントローラだけだ。

だから停止手順は: エンジンを `off`（または idle 停止を待つ）→ 箱が走っていないことを確認 →
VM を停止。夜間停止を自動化する（決定 10 の P2）なら、自動化がこの順序を実行するのであって、
`stop-instances` 単体ではない。

🔄 **そしてそれは「手順書の箇条書き」ではなくコマンド 1 本である**（レビュー R5）。コントローラは
`context.Background()` で走っている（`engines.go:1007-1008`）ので、VM の停止に引っ掛けられる
quiesce フックが無い——ホストが消えることに気づく者がいない。P2 は「全ての管理役を off にし、
ECS と fleet がゼロになるまで待ち、それから `StopInstances` を呼ぶ」停止を 1 本出し、夜間
スケジュールは**それ**を呼ぶ。API を直接呼ぶ経路は作らない。

### 8. `AF_ENGINE_SUBNETS`——`AF_ECS_SUBNETS` は他の全部でワークスペースのプールを意味するから

`engineSubnets()` は `AF_ECS_SUBNETS` だけを読む（`engines.go:609`）。ecs-ec2 の配備では同じ変数が
スロットプールとワークスペースタスクのサブネットでもある。docker の配備ではその変数は他に何の意味も
持たず、ECS のワークスペースを持たない配備の運用者に「ECS のサブネット」を設定させるのは、値が
別の配備の `.env` に迷い込む典型的な道筋である。

`firstEnv("AF_ENGINE_SUBNETS", "AF_ECS_SUBNETS")` にする——`AF_ENGINE_ECS_CLUSTER` が既に持っている
形で、古い名前もそのまま効く。

### 9. TLS は Caddy のまま。ACM は、この ADR が消すロードバランサ無しでは無料ではない

ACM の公開証明書が無料なのは、**それを終端する AWS の口**——ALB・NLB・CloudFront・API Gateway——に
対してである。ここにはその口がもう無い: 消そうとしているのが ALB であり、無料の証明書のために
それを戻すのは、Caddy が既に $0 で出しているものを買い直すために月 $18 払うことになる。
エクスポート可能な公開証明書は証明書ごとの課金で、しかも `caddy:2-alpine` が自分でやっている
更新と reload を運用者に返してくる。証明書は既に `DATA_DIR` の下に落ちていて `backup.sh` が
拾っている。

ACM が再び正解になるのは、別の理由でロードバランサが存在する瞬間である——WAF、複数オリジン、
あるいは「VM は公開アドレスを持ってはならない」という要件。この配備はそれではない。

### 10. 完了確認は請求書であって、スクリーンショットではない

ADR 0079 はこの作業を後ろに回したとき、その試験まで書いていた——**「同じワークスペースが、同じ画像を、
より安く得る」**。具体的には、以下の全部:

1. ec2-single 配備のセッションが、その配備が買ってまた止めた GPU で絵を 1 枚生成する;
2. 自宅のフリートが VM のゲートウェイ越しに同じエンジンを借りる（ADR 0079・無改造）;
3. モデルを 1 本、VM の経路を通る ingest で取り込む;
4. **Cost Explorer の 3 日ぶん**を、同じ 3 日ぶんの ECS の床と比べる。GPU 時間は両方から除く。

🔄 **読む場所は AWS のコンソールであって、我々の画面ではない**（レビュー R6）。製品のコスト
ビューはランタイムの `CostProfile` で門番されていて、docker は「請求書は無い」と申告する。
つまり af-sandbox を ec2-single にすると、**利用者ごとのコストビューは消え**、エンジンの
`af-role` 行は集計されず、この Control Plane はコスト配分タグを有効化しない。
`guide/ref/deploy-targets.md` の能力表が既にそう書いている（「利用者ごとの費用按分」は ECS の行）。
🔄 **とはいえ大半は残せる——決定 11**（請求をワークスペースのランタイムから切り離す）。
本当に失うのは利用者ごとの半分だけであり、上の完了確認は決定 11 が入ったあとの管理者向け
コストビューで読む。

### 11. 🔄 コストビューは残す——ランタイムから導出するのをやめ、配備が請求を宣言する

レビュー R6 は「今は off である」という点で正しい。そして決定 10 は、それを**コードの性質**では
なく**この配備の性質**として記録してしまった点で誤りだった。`cloudCostProfile()` はランタイムの
ファクトリに訊き（`cost_profile.go:31-36`）、`dockerFactory` は「運用者自身のハードウェアだ。
読むべき請求書は無い」と答える（`profiles.go:361-362`）。これは地下室の docker については真で、
**エンジンを持つアカウントの EC2 の上の docker については偽**である。ec2-single は後者であり、
`guide/ref/deploy-targets.md` 自身が「`ec2-single` は独立したランタイムではない——VM の上の
`docker` である」と書いている。

だから**配備が宣言する**。決定 2 がエンジンを宣言するのと同じ形で、ADR 0053 が言う理由で:
`.env` に `AF_CLOUD_COST=aws`、起動時に 1 回読み、ランタイムには知りようがなかった profile を作る。

| | ec2-single では |
|---|---|
| 配備全体の合計・サービス別・日別 | **残る**——Cost Explorer はアカウントに答えるのであって、ランタイムに答えるのではない |
| 役別（`af-role`）の切り口 → `engine-llm` / `engine-image` | **残る。そしてこれがこの ADR の目的の行である**——「昨夜の GPU はいくらだったか」（`cloudcost.go:172,304-349`） |
| 利用者ごとの按分——`/api/cost/me` と会員カード | **消える。しかもゼロを出すのではなく隠さねばならない** |

🔴 危ないのは最後の行だけである。このホストでは `af-membership` を担ぐものが何も無い——
1 台のインスタンスが全ワークスペースをコンテナとして走らせている。`Available: true` だけを立てると
会員カードが描かれ（`SettingsDialog.tsx:232`・`TenantDialog.tsx:98`）、ゼロで埋まる——
`cost_profile.go` のヘッダが拒んでいるまさにその失敗（「ゼロだらけの画面はバグに見えるか、
もっと悪いことに『あなたは無料だ』に見える」）。答えは profile が既に持っている: `Attributable` は
「本当にタグを担いでいるもの」の一覧で、ここでは**空**であり、Console は会員側の節を `available`
ではなくその**長さ**で門番する。この欄は今どこからも読まれていない（`CloudCostView.tsx:26` が
型に書いているだけ）ので、機能ではなく 3 行と dom テスト 1 本である。

見たいものが空にならないように、あと 2 つ:

- **VM が自分のテンプレートで `af-role` を担ぐ**こと。さもないとホスト自身のインスタンス時間は
  税と並んでタグ無しの共有バケツに落ちる。
- アカウント側の前提は変わらない（「IAM ユーザー／ロールの請求情報アクセス」）。そして Cost
  Explorer の要求自体が**月 $1.2 ほど**かかる（`cloudcost.go:109`）——月 $80 を削る文書は、
  それを隠さず書くべきである。

却下: **導出すること**（「AWS 資格情報＋ここが管理する行があれば請求がある」）。多くの場合正しく、
資格情報が他人の払うアカウントのものだったときに静かに誤る。この形に対するこのリポジトリの規則は
「lifecycle は宣言であって推論ではない」である（ADR 0053・ADR 0076 決定 1）。

## 定価でいくらか（算術であって、請求書ではない）

月あたり・ap-northeast-1・730 時間。GPU は両側から除いてある: 買うコードは両方同じで、ADR 0074 の
実測（`g6.xlarge` $1.1672/h、ADR 0077 以降はその上に載る managed-instances 手数料が無い）は
ここでは変わらない。

| | 使った単価 | 毎日立っている ECS 構成 | この ADR |
|---|---|---:|---:|
| NAT ゲートウェイ | $0.062/h ＋ $0.062/GB | **$45** ＋ 通信 | **$0**（決定 4） |
| ALB | $0.0243/h ＋ LCU | ~$18 ＋ LCU | $0 |
| RDS（db.t4g.micro） | $0.024/h ＋ ストレージ | ~$20 | $0（SQLite） |
| EFS | 容量ぶん | ~$6 | $0 |
| CP の Fargate | 0.25〜0.5 vCPU | ~$7〜22 | $0（VM の上のコンテナ） |
| スロットとそのボリューム | 使用量 | 使用量 | $0（VM の上のコンテナ） |
| VM | `t4g.large` $0.0864/h・`t4g.medium` $0.0432/h | — | $63 / **$31** |
| その EBS | gp3 $0.096/GiB・月——**今のテンプレートの既定は 30 GiB**（`ec2-single/cfn.yaml:27-29`）。home を持つ compose ホストはもっと要る | — | 30 GiB で $3・150 GiB で $14 |
| 公開 IPv4 | $0.005/h | 両方 | $3.6 |
| `20-platform` ＋ `60-engines` の常設 | 保管＋名前空間＋secrets | 両方同じ | ~$5〜15（ECR 6 本・staging とモデルの 2 バケツ・Cloud Map・secrets 2 本・ロググループ） |
| Route53 ホストゾーン | $0.50/ゾーン・月 | 両方 | 両方 |
| **停止中に残る床** | | **~$78**——`teardown.sh` 以外に消す手段が無い | **EBS・EIP・ゾーン・バケツ** |

🔄 この表は **730 時間月の定価の算術であり、定価そのものはこのリポジトリからは検証できない**
（レビュー R7）。レビューが強いた訂正が 2 つ: NAT の行は書いてある単価どおりなら $45.26 である
（本文の他所の $36 は、配備が立ちっぱなしではなかった月の実請求から来ている）。そして
「停止中の床 $10」は誤りで、EIP・ホストゾーン・バケツ・ECR は止まらない。

🔄 **耐久性は値付けされていない。これは丸め誤差ではなく、決めていない決定である**（レビュー R8）。
VM のボリュームは `DeleteOnTermination: true`（`ec2-single/cfn.yaml:66-68`）で、同梱の
`backup.sh` はローカルのディレクトリに tar を書くだけ——**そこに今やエンジンのカタログが乗る**
（決定 3）。床にホスト外の保管先と EBS スナップショットの予定を足すか、「ボリュームを 1 本失えば
カタログと全ワークスペースの home が消える」と正面から書くか、どちらかである。

最後の行がこの決定である。24/7 の `t4g.large` は ECS の床とほぼ同額——**この ADR は、存在するだけでは
元が取れない**。元が取れるのは床を**選べるようになる**ことによってだ: インスタンスを適正化し、
GPU を貸していない夜は API 1 回で配備ごと止める。ECS 構成には最後のそれができない。
🔄 **Graviton は能力であって、出荷済みの成果物ではない**（レビュー R9）: `WS_PLATFORMS` /
`CP_PLATFORMS` は既定が空で、これまでの全リリースはビルドホストのアーキだけを publish している
（`release.sh:65-75`）。ec2-single のテンプレートも `t3` と amd64 の AMI しか受け付けない
（`ec2-single/cfn.yaml:22-33`）。P2 の節約は、マルチアーキのタグを publish し、CLI を含む
ワークスペースイメージ全体を arm64 で動かすことを条件とする。

## 運用で覆う案を先にやる

2026-09-23 追記。この ADR に向けられた問いは「全部を運用で覆えないか」だった——ECS 構成を残し、
使っていない間は金のかかるものを止め、使うときに上げる。RDS も含めて。

覆える。しかも先にやる。理由は**それが必要としないもの**にある。ECS 構成を毎日使えば、リリースを
確かめる構成がそのまま日常の構成になる。コストビューも利用者ごとの按分もそのまま残る（決定 11 が
要らない）。そしてレビューの重い指摘はどれも発生しない——カタログの引き継ぎ（R1）も、NAT インスタンス
（R2・R3）も、コンテナホスト上の購買権（R4）も、2 つ目の Control Plane（R5）も無い。必要なコードは
`pause.sh` だけで、それはもう書いた:

- **データベースを止める。**下りでは最後に止め、上りでは最初に起こす（`--keep-db` で残す）。CP が
  先に起きると 500 を返すからである。
- 🔴 **エンジンの GPU の箱を取り残さなくなった。** `pause.sh` はこれを一度も見ていなかった: 箱が
  起きている間に休止すると、それを終わらせる唯一のコントローラが消え、g6.xlarge で 1 日 ~$28 が
  残った。今は CP の idle 停止を待ち（`--fast` なら直接終わらせ）、CP が止まった後に生きている箱を
  掃引で terminate する。
- **`--status` が RDS 自身の再起動を知らせる。** AWS は止めた RDS を 7 日で勝手に起動する。CP が
  休止中なのに DB が動いているなら、ほぼそれである。

できないのは床である。AWS には NAT ゲートウェイとロードバランサの「停止」が無く、削除しかないので、
休止中も課金される。`pause.sh` の実測 $2.6/日から RDS の計算分（db.t4g.micro で ~$0.6/日）を
引いた、定価の算術で:

| 月あたり・1 日 U 時間使う | 休止中の床 | U = 4 | 終日稼働 |
|---|---:|---:|---:|
| A. `pause.sh`＋DB 停止 | ~$60 | ~$78 | ~$165 |
| B. A ＋ NAT と ALB を CloudFormation の条件で消して作り直す | ~$20〜30 | ~$48 | ~$165 |
| C. この ADR | ~$10〜25 | ~$30 | ~$80 |

そして時間がかかる: 休止中は何も借りられず、`--up` の後の最初の 1 枚は DB（5〜10 分）・CP・
エンジンのコールドスタート（9〜10 分）を待つ。まとまった作業には向き、思いつきの 1 枚には向かない。

だから順序は: A を 2 週間回して Cost Explorer で請求を読む。C との差が表の予想どおり月 ~$50 なら、
そのときに、この ADR の作業とレビューのリスクに見合うか——あるいは構成を 1 つに保てる B の方が
よい中間か——を決める。

## 却下した案

| 案 | 却下の理由 |
|---|---|
| **VM 自体を GPU インスタンスにして**、エンジンを compose のサイドカーで動かす | `g6.xlarge` 24/7 は月 $850 ほど。ADR 0071 のコントローラの存在意義が「GPU は 1 日のほとんど寝ている」ことである |
| **ECS を使わず、素の EC2 の上でコンテナを動かすエンジン lifecycle を新設する** | 得るものが無い。ECS クラスタと desired count 0 のサービスの請求は $0 である。ラダー・offers・配置・fetch サイドカー・active set——ADR 0071/0072/0074/0075/0077——を、既にゼロの行を消すために作り直すことになる |
| **自宅からエンジン VPC へトンネルを掘り**、AWS 側に配備を置かない | ADR 0079 が理由つきで却下済み（インスタンスは短命・Cloud Map 経由・SG は CP しか通さない）。しかも安くならない: トンネルには VPC 内に常時起動の箱が要り、それはこの ADR の VM そのもので、配備が無いぶん機能だけ減る |
| **ECS の af-sandbox から借り続け、もっと強く pause する** | 🔄 **もう却下ではない——先にやる**（上の節）。初稿の言い分は今も真である: NAT と ALB は残り、休止中の配備は GPU を貸せない。見落としていたのは、どちらも計画的な使い方ではこの案を誤りにしないこと、そしてこの案がこの ADR のリスクを 1 つも持たないことだった |
| **Caddy の代わりに ACM** | 決定 9 |
| **NAT ゲートウェイの代わりに SSM インターフェースエンドポイント** | 1 AZ で月 ~$9、しかも決着しない: レビュー R3 の完全な一覧を満たすには `ecr.api`・`ecr.dkr`・`logs` にも要り、置き換えるはずの NAT ゲートウェイより高くつく（`00-network.yaml:160-166` が既にそう書いている） |
| 🔄 **稼働する窓のあいだだけ NAT ゲートウェイを作る**（レビュー R10） | 管理されたものと保持した EIP を残したまま、エンジンや ingest が動いている間だけ課金される。P1 では採らず退避先に置く: 既に 527〜586 秒あるコールドスタートの前に CloudFormation の往復が増え、そして「消し忘れられるもの」が 1 つ増える——この ADR が相手にしている失敗そのものである |
| 🔄 **小さな NAT インスタンスを別に立てる**（レビュー R10） | private サブネットの外向きをアプリの VM に結びつけるのが許容できないと分かった場合の、信頼性側の答え。インスタンスと当てるべきパッチが 1 つ増えるので、採用ではなく記録にとどめる |
| **リリースの合間は af-sandbox を丸ごと畳む** | 可能な限り安い答えで、今日実際にやっていること——実測請求が 16 日で $9 なのはそのためである。代償は毎日の GPU で、それこそが払っている対象 |
| **自宅のホストに GPU を買う** | このリポジトリの範囲外。そして毎日重く使うなら正直これが最安である: ADR 0076 は LAN の ComfyUI を既に支えており、ADR 0093 の llm 役にも同じ扱いが要る。P2 がインスタンスの大きさに金を使う前に、もう一度決め直す価値がある |

## 上書きする既存の決定と、維持する決定

- **ADR 0079 の却下案「ec2-single のエンジンスタックを先にやる」——これがその作業である。**
  却下ではなく後回しであり、その順序の理屈は当たった: 0079 が先に入り、借りる側が存在し、この ADR は
  0079 が書いた完了試験を持った「貸す側の費用最適化」になった。
- **ADR 0071 決定 4（ワークスペースがエンジンに届く唯一の道はゲートウェイ）: 維持。** この文書の
  どこにも Agent 側の変更は提案していない。
- **ADR 0072 / 0074 / 0075 / 0077: 無改変で維持。** この ADR はエンジンの振る舞いを足さない。
  変えるのは「誰が資格情報を持つか」と「Control Plane がどこで走るか」だけ。
- **ADR 0076 の `external` 行と ADR 0079 の `remote` 行: 維持。** 配備は両方を同時に持ってよい——
  買ったエンジンと借りたエンジン——それが表であってモードではないからだ。
- `guide/ref/deploy-targets.md` の能力表が変わる: 「配備が提供する画像エンジン」は `docker` でも
  「配備自身の GPU」の意味で真になる。脚注に「AWS 側のエンジン 3 点とインスタンスプロファイルが要る」
  と添える。

## 未解決の点（測ってから決める）

1. **今この配備はいくらかかっているのか。** 実測の内訳は 2026-08 のもので、16 日に 20 時間しか
   立っていなかった配備を描いている。P0 は Console のコストビューで直近 30 日を読むところから
   始める。上の表はそれまで定価の算術である。
2. **数十 GB の ingest が VM 経由で現実的な時間で終わるか。** `t4g.medium` の持続帯域は NAT
   ゲートウェイの何分の一かである。実モデルを 1 本測る。
3. **VM 障害はエンジン側からどう見えるか。** fetch サイドカーは再試行する。経路が死んでいる間に
   パネルが有用なことを言うかは分かっていない。
4. **ワークスペースイメージの CLI は全部 arm64 で動くか。** イメージ自体はビルドできる
   （`workspace/Dockerfile:27`）が、中に入れている各 CLI は別の問いで、P2 の節約はその答えに乗る。
5. **2 つの構成が名前をどう分け合うか。** 1 つの FQDN を EIP と ALB の間で振り替えるか、確認用の
   配備に別ホスト名を与えて OAuth のリダイレクトを別に登録するか。
6. 🔄 **答えは半分ずつ出た。** 役別・サービス別のビューは請求を宣言すれば残る（決定 11）。
   **利用者ごとの按分は残らない**——このホストでは `af-membership` を担ぐものが無いからである。
   測って確かめるべきことは「タグの付いた資源がエンジンの箱と VM しかない配備で、役別の切り口が
   実際に埋まるか」に変わった。
7. **4 GB で足りるか**——CP・Caddy・ワークスペース 1 つ・NAT 経路。ec2-single の README は
   `WS_MEMORY` を下げれば `t3.medium` で動くと既に書いている。

## フェーズ

- 🔄 **P0 には門がある。** 始める前に、レビューの門が挙げる 4 点をこの文書の中で閉じること:
  カタログの正本（決定 3）・資格情報の配り方（決定 5）・閉じる方向で失敗する単一所有者（決定 6）・
  コストビューが消えることを認めた完了条件（決定 10）。4 つとも本文に書いた。残っているのは
  **誰もまだ走らせていない**ということである。
- **P0——動く。ネットワークは何も変えない。** `ec2-single` に public サブネットへの VPC 配置・
  決定 1 の SG 群・IMDSv2＋ホップ上限 1・インスタンスプロファイルを足し、`standup.sh` にエンジン
  3 点の経路を足し、`AF_ENGINE_SUBNETS` を入れる。NAT ゲートウェイは今のまま。🔄 決定 11 の
  3 点（`AF_CLOUD_COST`・会員側の節を `Attributable` で門番・VM に `af-role`）もここに入る——
  完了確認をそのビューで読むからである。完了条件は、VM の Control Plane が買って止めた箱の上で
  セッションが絵を 1 枚出すこと、**かつ**ワークスペースのコンテナが IMDS のトークンを取れないこと、
  **かつ**管理者のコストビューにその絵の `engine-image` の行が出ること。
- **P1——NAT ゲートウェイを外す。** `PrivateEgress` と VM の経路。完了条件は、NAT ゲートウェイを
  削除した状態で決定 4 の「冷えた一式」が 2 つの private AZ の両方で通ること——箱・冷えた ECR・
  ログ・SSM の鍵・ingest のトークン・モデル 1 本・自宅からの borrow——そして 3 日ぶんの請求が
  手元にあること。
- **P2——床を選ぶ。** インスタンスの系統と大きさ、決定 7 の順序を守った夜間停止、image 役だけの
  Spot（ADR 0075 の切り分け: 中断された会話と中断された絵は同じではない）。
- **P3——任意。P1 が「経路が問題だ」と言った場合だけ。** active set をモデルバケツに publish して
  無料のゲートウェイエンドポイント越しに読ませ、ingest タスクに公開 IP を持たせる
  （`engine_ingest.go`）。private サブネットの外向きの必要が完全に無くなる——そして
  `engine-tools/CONTRACT` と `TAGS.tsv` のバンプを伴う。リポジトリで最も壊れやすい継ぎ目なので、
  意図的に最後に置く。

## 確認した出典（2026-09-22・このリポジトリ）

🔄 この表の全行をレビューが開き直し、6 行はここに書いてあることを言っていなかった。訂正後の
場所が下で、行ごとの判定はレビュー R11 にある。

| 主張 | 場所 |
|---|---|
| レジストリの起動時の門が要るのは表と AWS 資格情報 | `control-plane/engines.go:721-775` |
| ……ストア・クラスタ・ingest・active set の publish・サブネットは残りの部分 | `control-plane/engines.go:800-869,945-987` |
| エンジンのクラスタは既に別変数 | `control-plane/engines.go:812` |
| 買った箱を置くサブネットは `AF_ECS_SUBNETS` から来る | `control-plane/engines.go:609-616` |
| AWS を任意にしているのは external/remote 行。読み込みの門は後者 | `control-plane/engines.go:705-715`・`:752-775` |
| カタログは CP の DB で、active set は起動時に publish し直される | `control-plane/internal/store/migrations/0057_engine_models.sql`・`control-plane/engine_catalog.go:615-639`・`control-plane/engines.go:964-968` |
| エンジンは CP の SG を import で許している | `deploy/aws/ecs/cfn/60-engines.yaml:422-433` |
| エンジンサービスは `awsvpc`・private サブネット・公開 IP 無し（llm・image） | `deploy/aws/ecs/cfn/60-engines.yaml:768-776`・`:878-885` |
| CP のエンジン IAM は import した役に貼られている | `deploy/aws/ecs/cfn/60-engines.yaml:278-341` |
| 同じ仕組みが import した**実行**ロールに secret の読みを貼る | `deploy/aws/ecs/cfn/60-engines.yaml:350-384` |
| VM が丸写ししてはいけない広い ECS 操作 | `deploy/aws/ecs/cfn/20-platform.yaml:197-227` |
| `ec2:CreateFleet` は CP のもの | `deploy/aws/ecs/cfn/60-engines.yaml:318-322` |
| 箱の側の ECS エージェント・ECR の引き・`awslogs` | `deploy/aws/ecs/cfn/60-engines.yaml:501-510,681-710,804-834` |
| llm の鍵と ingest のトークンはタスク開始時に解決される | `deploy/aws/ecs/cfn/60-engines.yaml:592-655,727-739` |
| `20-platform` の import は VPC id だけ | `deploy/aws/ecs/cfn/20-platform.yaml:139` |
| `20-platform` に時間課金のリソースは無い（保管・リクエスト課金は否定していない） | `deploy/aws/ecs/cfn/20-platform.yaml:34-149` |
| NAT ゲートウェイ・無料の S3 ゲートウェイエンドポイント・なお NAT を通るもの | `deploy/aws/ecs/cfn/00-network.yaml:119-173`（特に `:160-166`） |
| private の CIDR・public サブネット・IGW 経路 | `deploy/aws/ecs/cfn/00-network.yaml:39-44,69-84,100-117` |
| `CpSg` は ALB からしか通さない | `deploy/aws/ecs/cfn/00-network.yaml:186-197` |
| fetch サイドカー: pending の put・S3 の get・active の get・監視ループ | `deploy/aws/ecs/engine-tools/fetch-models.sh:74-80,92-103,105,141-149` |
| ingest タスクの `AssignPublicIp` は定数 | `control-plane/engine_ingest.go:1060-1069,1652-1661` |
| ワークスペースのコンテナは自前の外向きを持つ／CP はホストネットワーク | `control-plane/internal/runtime/runtime_docker.go:280-303`・`deploy/compose/docker-compose.yml` |
| 今日の VM に IMDS の制限は無い。他所は 2 ホップ | `deploy/aws/ec2-single/cfn.yaml:59-73`・`deploy/aws/ecs/cfn/40-ec2-pool.yaml:123-125`・`deploy/aws/ecs/cfn/60-engines.yaml:451,521` |
| docker は請求書が無いと申告し、ポーラはそれで return する | `control-plane/internal/runtime/profiles.go:362`・`control-plane/cloudcost.go:112-115` |
| profile はランタイムのファクトリから来る／`Attributable` は「タグを担ぐもの」 | `control-plane/cost_profile.go:31-36`・`control-plane/internal/runtime/profiles.go:266-283` |
| 役別の 2 本目の問い合わせ＝ここで生き残る切り口 | `control-plane/cloudcost.go:172,304-349` |
| 会員側の節は今 `available` で門番され、`attributable` はどこからも読まれていない | `console/src/features/settings/SettingsDialog.tsx:232`・`console/src/features/settings/TenantDialog.tsx:98`・`console/src/features/cost/CloudCostView.tsx:26` |
| Cost Explorer 自身の月 ~$1.2 | `control-plane/cloudcost.go:109` |
| `standup.sh` が必須にしているもの | `deploy/aws/ecs/standup.sh:126-135` |
| 休止しても残るものと、順序の教訓 | `deploy/aws/ecs/pause.sh:8-26` |
| 唯一の実測請求 | `docs/log/67-member-cloud-cost.md:54-80` |
| ec2-single: 既定 VPC・インスタンスプロファイル無し・`DeleteOnTermination`・t3 と amd64 のみ | `deploy/aws/ec2-single/cfn.yaml:22-33,50-73` |
| RDS ではなく SQLite | `deploy/compose/.env.example:373-374` |
| Caddy は公開ホストを reverse proxy する。Let's Encrypt と明言しているのは runbook | `deploy/compose/Caddyfile:14-16`・`deploy/aws/ec2-single/README.md:77-79` |
| arm64 は**既定 off の opt-in**＝今日マルチアーキで出ているイメージは無い | `deploy/compose/release.sh:65-75,147-187` |
| この ADR が変える能力表 | `guide/ref/deploy-targets.md:28-57` |
| この ADR の正体である「後回しにした案」 | `docs/decisions/0079-remote-engine-from-another-deployment.md:556-564` |

## レビュー（2026-09-22・P0 の前）

`07cad1f29` に対して、AWS には触れず、配備も測らずに確認した。判定: **まだ P0 に入っては
いけない。** docker の Control Plane から ECS のエンジンサービスを流用すること自体は成立し、
新しいエンジン実行器も要らない。しかし提案の所有境界はコードの境界と一致していない。モデル台帳は
Control Plane の DB にあり、docker ランタイムは AWS 費用の仕組みを止め、NAT インスタンスの経路は
受信規則と必須の外向き先を欠き、IMDS を明示的に隔離しなければ VM の役は購入資格情報をコンテナへ
露出する。いずれも実装の細部ではなく、設計への入力である。

### 重大

- **R1. エンジン表とモデルバケットだけではエンジンは動かない。モデル台帳は Control Plane の
  DB に属する。** `engine_models` が enabled、selected/default、S3 key、引数、ライセンス受諾、
  サイズ情報を持つ（`control-plane/internal/store/migrations/0057_engine_models.sql:14-26,38-62`、
  `control-plane/internal/store/migrations/0058_engine_ingest.sql:40-53`）。
  起動時、managed 行は `mgr.store` からその行を読み（`control-plane/engines.go:800-807,945-957`）、
  手元のカタログから active set を組み（`control-plane/engine_catalog.go:82-104,425-467`）、SSM を
  無条件に上書きする（`control-plane/engine_catalog.go:615-639`、呼び出しは
  `control-plane/engines.go:964-968`）。したがって新しい SQLite で起動した ec2-single CP は、残って
  いた active set を**空で上書きする**。逆に `10-data` を消すと、既定の `Persistence=delete` では
  RDS も消える。`retain` が残すのは、このテンプレートに復元パラメータが無い最終 snapshot だけで
  ある（`deploy/aws/ecs/cfn/10-data.yaml:64-66,157-174`）。決定 3 の「バケットとエンジン表が残る」
  では足りない。P0 の前に台帳の唯一の権威と、両方向の引き渡し規則を決める必要がある。最小で安全な
  形は VM の SQLite 台帳を権威のままにし、リリース確認側は ADR 0079 で役を借りること。確認側 CP に
  所有させるなら、明示的な台帳移行と競合規則が要る。

- **R2. 決定 4 の NAT インスタンスは、書かれたままでは最初の packet を通さない。** VM に付けるのは
  公開 SG と `CpSg` だけだが、`CpSg` は ALB から CP port だけを許し
  （`deploy/aws/ecs/cfn/00-network.yaml:186-197`）、公開 SG は 22/80/443 だけを許す
  （`deploy/aws/ec2-single/cfn.yaml:50-57`）。転送 packet の送信元は private subnet のままなので、
  どちらの SG にも入れない。private の 2 CIDR（`deploy/aws/ecs/cfn/00-network.yaml:39-44`）だけを
  許す NAT 専用 ingress が要る。また VM を IGW route のある public subnet に固定しなければならない
  （`deploy/aws/ecs/cfn/00-network.yaml:69-84,100-117`）。任意の `SubnetId` では足りない。P1 の
  受け入れ試験は ingest より先に、両 private AZ から route・SG・DNS を証明しなければならない。

- **R3. 決定 4 の外向き一覧は、壊れるほど不完全である。** このリポジトリ自身が、存在しない interface
  endpoint として `ecr.api`・`ecr.dkr`・`logs`・`ssm` を挙げている
  （`deploy/aws/ecs/cfn/00-network.yaml:160-166`）。GPU host は ECS agent を起動し
  （`deploy/aws/ecs/cfn/60-engines.yaml:501-510`）、task image は ECR から来て、全 container が
  `awslogs` を使う（`deploy/aws/ecs/cfn/60-engines.yaml:681-710,697-702,734-739,804-834,
  821-826,847-852`）。ingest は container 起動前に Secrets Manager の secret を解決する
  （`deploy/aws/ecs/cfn/60-engines.yaml:592-655`）。任意の llm key も SSM の task secret である
  （`deploy/aws/ecs/cfn/60-engines.yaml:727-739`）。S3 が NAT から外すのは layer/model の**バイト**で
  あって、ECR 認証・manifest、ECS agent、log 配送、SSM、Secrets Manager ではない。Cloud Map 登録は
  task 側 API 呼び出しではなく ECS service の配線で（`deploy/aws/ecs/cfn/60-engines.yaml:741-750,
  752-775,854-884`）、custom health もそこに宣言される。host の時刻同期経路を裏付ける repo 内の
  根拠は**見つからず、未確認**である。P1 は実モデルの取得だけでなく、冷えた GPU host、冷えた ECR
  pull、log 到着、SSM secret の llm key、ingest secret、service discovery を試す必要がある。

- **R4. 決定 5 は、資格情報の境界無しに高価値の資格情報を multi-tenant container host へ置く。**
  Workspace は意図的に NAT 付きの外向きを持ち（`control-plane/internal/runtime/runtime_docker.go:
  280-303`）、CP 自身は host-network container である（`deploy/compose/docker-compose.yml:17-44`）。
  現在の VM は IMDSv2 も hop limit も宣言していない（`deploy/aws/ec2-single/cfn.yaml:59-73`）。CP
  container に instance profile の資格情報を渡すには通常 metadata を container から届くようにするが、
  workspace network から `169.254.169.254` を明示的に拒まなければ member も同じ資格情報を読める。
  提案する役は fleet を買い engine instance role を pass でき
  （`deploy/aws/ecs/cfn/60-engines.yaml:318-341`）、pass 可能な task role で ingest を起動できる
  （`deploy/aws/ecs/cfn/60-engines.yaml:288-302`）。「CpTaskRole より小さい」は境界にならない。P0
  には、IMDSv2 と試験済みの hop/firewall 境界、または CP process だけが届く credential proxy という
  具体的な配送設計が要る。さらに `CpTaskRole` の広い `EcsDrive`——`*` に対する service 削除と task
  definition 登録まで含む——をコピーしてはならない（`deploy/aws/ecs/cfn/20-platform.yaml:197-227`）。
  engine 操作を列挙し、workspace container から拒まれる負の試験を足すべきである。

- **R5. 決定 6 と 7 は、請求を生む invariant を文章だけに預けている。** 各 CP は自分の controller
  goroutine を起動し（`control-plane/engines.go:1001-1009`）、route 登録は process ごとに registry を
  1 つ作る（`control-plane/engine_gateway.go:191-205`）。配備をまたぐ lease は無い。2 つの CP は store
  も別なので mode と demand の行でも協調できない。P0 前に所有を fail closed にするべきである。
  エンジン表の明示的な controller identity と必須の一致 env は安い設定 guard になり、設定を重複させた
  場合まで防ぐなら conditional lease が要る。購入 policy は選んだ所有者だけへ貼り、`standup.sh` は
  同じ managed 行を宣言する確認 CP を拒むべきである。停止についても、controller は
  `context.Background()` で走る（`control-plane/engines.go:1007-1008`）ため VM shutdown に quiesce hook
  が無い。全 managed role を off にし、ECS/fleet が 0 になるまで poll してからだけ `StopInstances` を
  呼ぶ 1 本の停止 command または systemd shutdown unit を出し、直接停止は失敗または明瞭な警告に
  しなければならない。

### 中

- **R6. docker ランタイムは、この ADR が頼る費用検証と tag 有効化そのものを無効にする。**
  `dockerFactory.CostProfile` は `Available:false` を返し
  （`control-plane/internal/runtime/profiles.go:361-365`）、その場合 `startCloudCostPoller` は Cost
  Explorer を作る前に return する（`control-plane/cloudcost.go:99-128`）。したがって VM role の Cost
  Explorer read/update は死んだ権限である。費用 tab は隠れ、engine の `af-role` 行は poll されず、
  cost allocation tag もこの CP から有効化されない。これは測定待ちではなく実コード変更である。
  workspace runtime から費用能力を分離し（例えば明示的な AWS billing profile）、docker-on-EC2 を
  試験する必要がある。

- **R7. 費用表は内部の算術が合わず、like-for-like でもない。** 草案自身が東京の NAT を
  `$0.062/h` とするなら、同じ 730 時間では `$45.26` であって `~$36` ではない
  （`docs/decisions/0099-engines-from-a-single-vm.md:24-29,243`）。EBS + public IP の `~$14` は rate も
  volume size も示さないが、現テンプレートの既定は 30 GiB である
  （`deploy/aws/ec2-single/cfn.yaml:27-29,66-68`）。停止時の床の文は、残る EIP・Route53・Cloud Map・
  Secrets・ECR・S3 を落としている。実測請求には既に hosted zone・regional transfer・public IPv4・
  RDS storage/DNS/Secrets/snapshot 等の残差がある（`docs/log/67-member-cloud-cost.md:57-75`）。
  `20-platform` は staging bucket と ECR repository 6 個を作り
  （`deploy/aws/ecs/cfn/20-platform.yaml:34-56,58-131`）、`60-engines` は model bucket と log group を
  retain する（`deploy/aws/ecs/cfn/60-engines.yaml:386-419`）。さらに常時 lender の ECS 構成には CP
  Fargate task が要るのに表では「usage」のまま、VM 列は CP と Workspace の両方を供給する host を
  含む。同じ workload で表を作り直し、日付付き URL または保存した price sheet で単価を列挙し、
  data 量・request 数を明記して、稼働時と停止時を分けるべきである。AWS 定価そのものは**この
  リポジトリからは確認不能**だった。

- **R8. 耐久性を決めずに費用 0 としている。** VM volume は `DeleteOnTermination:true`
  （`deploy/aws/ec2-single/cfn.yaml:66-68`）。同梱 backup は呼び手が選ぶ local directory の tarball で、
  SQLite 台帳を含む（`deploy/compose/backup.sh:4-18,33-58`）。off-host の宛先も EBS snapshot schedule
  も無い。実用的な床には S3/EBS snapshot の storage・request・transfer を含めるか、1 回の
  instance/volume 喪失で台帳と全 Workspace を失うことを明記して受け入れなければならない。これは
  決定 3 の「周期を越えて残る」も運用上不完全にする。

- **R9. Graviton は能力であって、現在の artifact の保証ではない。** 引用された release の行が
  言うのは `WS_PLATFORMS` / `CP_PLATFORMS` を設定したときだけ multi-arch で publish することと、
  両方の既定が空であること（`deploy/compose/release.sh:65-75,147-187`）。現 ec2-single template は
  t3 と amd64 AMI しか受け付けない（`deploy/aws/ec2-single/cfn.yaml:22-33`）。
  `workspace/Dockerfile` は Agent を cross compile するが（`workspace/Dockerfile:15-29`）、同梱 CLI が
  動くかは ADR 自身が残した別の未確認事項である。P2 の節約は publish 済み manifest の検査と arm64
  での workspace image 全体の実行を条件にするべきで、「両 image が arm64 を build する」という
  出典表の主張は引用行に対して強すぎる。

- **R10. より安い／単純な 2 案を明示的に比較する価値がある。** 第 1 は managed NAT gateway を
  残し、engine/ingest の活動窓だけ作って EIP は retain する案。数分の cold-start orchestration と
  引き換えに利用中の時間課金だけとなり、host の route/patch 運用を避ける。第 2 はリリース確認中も
  VM を唯一の engine owner にし、ECS CP は ADR 0079 の既存 `remote` lifecycle を使う案。台帳移送と
  二重 controller も避けられる。現在の却下表には「もっと pause」と常設 SSM endpoint はあるが
  （`docs/decisions/0099-engines-from-a-single-vm.md:260-271`）、どちらも無い。application VM と外向きを
  結合する信頼性・隔離が受け入れられない場合の退避として、小さな NAT 専用 instance も記録する
  価値がある。

### 軽

- **R11. 「確認した出典」表は、まだ決定の監査可能な根拠になっていない。** 記載位置を全行開いた。
  結果は下表のとおり。「一部」は引用内容は実在するが主張全体を支えない、という意味である。

| 行 | 判定 | 引用位置が実際に示すもの／正しい位置 |
|---|---|---|
| registry に必要なのは表 + AWS だけ | **誤り** | `control-plane/engines.go:721-775` は起動 gate と client だけ。store・cluster・ingest・catalogue publish・subnet は `:800-869,812,945-987` |
| engine cluster は別変数 | 正しい | `control-plane/engines.go:812` はそのまま `firstEnv("AF_ENGINE_ECS_CLUSTER", "AF_ECS_CLUSTER")` |
| 購入箱の subnet | 正しいが行が狭い | 読むのは `control-plane/engines.go:609-616`（特に `:611`）。function header だけではない |
| external/remote なら AWS は任意 | 正しい | `control-plane/engines.go:705-715`、load gate は `:752-775` |
| EngineSg は CpSg を許す | 正しい | `deploy/aws/ecs/cfn/60-engines.yaml:422-433` |
| service は awsvpc/private/public IP 無し | **一部** | 引用 `:768-776` は llm だけ。image は `:878-885`、ingest は `control-plane/engine_ingest.go:1060-1069,1652-1661` |
| CP engine IAM は import role に貼る | **一部／範囲誤り** | CP policy は `60-engines.yaml:278-341`。`:350-384` は secret read を import した **execution** role に貼る。基礎 ECS 制御は `20-platform.yaml:197-227` |
| `ec2:CreateFleet` は CP 権限 | 正しい | `deploy/aws/ecs/cfn/60-engines.yaml:318-322` |
| `20-platform` の import は VPC id だけ | 正しい | 唯一の `Fn::ImportValue` は `deploy/aws/ecs/cfn/20-platform.yaml:139` |
| `20-platform` は時間課金 0 | **一部** | `:34-149` は S3・ECR・Cloud Map・ECS を示す。ECS cluster の時間 resource は無いが storage/namespace/request 課金が無い根拠にはならない |
| NAT + S3 gateway endpoint | 正しい | `deploy/aws/ecs/cfn/00-network.yaml:119-173`。`:165-166` は NAT に残る通信も名指す |
| public subnet は public IP を map | 正しい | `deploy/aws/ecs/cfn/00-network.yaml:69-84` |
| fetch sidecar の S3 + SSM | **主張が不正確** | Put は `fetch-models.sh:74-80`、S3 は `:92-103`、Get は `:105` と各 watch の `:141-149`。「loop ごとに 2 回」ではない |
| ingest public IP は定数 | 正しい | `control-plane/engine_ingest.go:1060-1069,1652-1661` |
| standup の必須 stack | 正しい | `deploy/aws/ecs/standup.sh:126-135` |
| pause の残存物と順序 | 正しい | `deploy/aws/ecs/pause.sh:8-26` |
| 実測請求 | 正しい | `docs/log/67-member-cloud-cost.md:54-80` |
| ec2-single の形 | 正しい | default-VPC/no-profile は `deploy/aws/ec2-single/cfn.yaml:50-73`。`VpcId`・`SubnetId`・`IamInstanceProfile` の宣言は無い |
| SQLite | 正しい | `deploy/compose/.env.example:373-374` |
| Caddy ACME | **一部** | `deploy/compose/Caddyfile:14-16` は public host と reverse proxy。Let's Encrypt の明記は `deploy/aws/ec2-single/README.md:77-79` |
| 両 image は arm64 build | **記述どおりなら誤り** | `deploy/compose/release.sh:65-75` は既定が空の opt-in 変数。build branch は `:147-187` |
| 能力表 | 正しいが範囲無し | 現在の行と脚注は `guide/ref/deploy-targets.md:28-57` |
| ADR 0079 の後回し案 | 正しい | `docs/decisions/0079-remote-engine-from-another-deployment.md:556-564` |

### P0 前の門

ADR が台帳の権威を選び、隔離された資格情報配送を定め、controller 1 つを fail closed にし、P0 の
完了試験に docker の cost profile と Workspace→CP の画像経路を足すまで P0 には入れない。P1 には
さらに NAT SG/public subnet の設計と、上の cold-start 外向き matrix が要る。このレビューでは AWS
定価・時刻同期・実経路のいずれも確認していない。
