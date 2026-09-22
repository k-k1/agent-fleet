# 0099. 1 台の VM から配備自身の GPU を——ec2-single が ECS のエンジン役を動かし、置き換えたスタックはリリース確認のときだけ立てる

[English](0099-engines-from-a-single-vm.md) | 日本語

- Status: **proposed**（2026-09-22）。実装は無い。この形でスタックを立てたことも、請求を読んだことも
  まだ無い。
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
| active set の読みと pending の書き、1 周につき小さな SSM 2 回（`fetch-models.sh:79,105,142`） | 今日は NAT ゲートウェイ |
| ingest タスクの Hugging Face / Civitai からのダウンロード | 今日は NAT ゲートウェイ（`engine_ingest.go:1068,1660` が `AssignPublicIp: DISABLED` を直書き） |

ADR 0079 はこの作業を後ろに回したとき、まさにこの問いを予告していた——*「採算は NAT ゲートウェイを
避けられるかで決まる」*。その通りである。NAT ゲートウェイは**月 $36 ＋ $0.062/GB**、狙う床は
小さいインスタンス 1 台ぶんだ。

## 決定

### 1. VM がエンジンの VPC に入り、`CpSg` を 2 枚目として着る

`deploy/aws/ec2-single/cfn.yaml` に `VpcId` / `SubnetId` を足す（空＝今日の既定 VPC の挙動なので
既存のスタンドアップは変わらない）。そして**2 つ**のセキュリティグループを付ける: `EngineSg` が
既に許している import した `CpSg` と、22/80/443 のための自前の公開グループ。

大事なのは**変えないもの**である: このために `60-engines.yaml` は編集しない。エンジンは
「Control Plane に」開いていて、その Control Plane が VM になっただけ——文はそのまま真である。

これが無いと、どの代案も「エンジンスタックの規則にアドレスを名指しで書く」で終わる。再構築を
跨いで保持できない値を、配備が抱えることになる。

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

`standup.sh` は今 `00-network 10-data 20-platform 30-ingress` を必須にしている
（`standup.sh:130`）ので、エンジンの 3 点だけを立てる経路を足す。

### 4. private の外向きは VM を通る。NAT ゲートウェイは条件になる

VM は「誰かがエンジンを要求しうる間」は必ず立っていて、既に公開アドレスを持ち、この設計で唯一の
常時起動の箱である。だから private サブネットの既定経路はそれにする: `SourceDestCheck: false`、
`net.ipv4.ip_forward=1`、cloud-init に `MASQUERADE` 1 行、そして `00-network.yaml` に
`PrivateEgress: nat-gateway | instance | none` を足し、NAT ゲートウェイのリソースを条件に入れる。

- **$0 で、しかも量のあるバイトはそこを通らない**: モデル実体と ECR レイヤは S3 ゲートウェイ
  エンドポイント（無料）を通るので、VM を越えるのは 1 周 2 回の SSM と、誰かがモデルを取り込む
  ときのダウンロードだけ。
- 壊れ方が正直である: VM が落ちていればエンジンは取得できない——そして VM が落ちているなら、
  エンジンを求めている者もいない。
- 🔴 これは経路なので、壊れても誰も報告しない。この決定の完了確認は health ではなく、
  **実モデルを 1 本、この経路で取り込むこと**である。

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

### 6. 🔥 1 つのエンジン役に、Control Plane は 1 つだけ

同じエンジン表を読む Control Plane が 2 つあると、両方が同じサービスの desired count を動かし、
両方が同じ起動テンプレートで箱を買い、両方が active set を書く。コードにこれを検出する仕組みは
無い。運用者に見えるのは「起動した数秒後に止まるエンジン」である。

したがって、エンジンの 3 点が生きているアカウントでリリース確認のために ECS 構成を立てるときは、
次のどれか 1 つが必ず真であること:

- その間 VM の Control Plane を止める、
- VM 側のエンジンを `off` にして ECS 配備が持つ、
- ECS 配備が `lifecycle: "remote"` で VM から借りる（ADR 0079）——両方の配備が同時に絵を出せる
  唯一の形。

これはコードではなく runbook に書く: 安い検出（「別の CP がこのサービスを見ているか」）は
ほとんどの時間 0 のものを見に行くだけだし、高い検出（エンジン表のリース）は他に使い道が無い。

### 7. 🔥 VM を止めるのは、エンジンを止めたあと

`pause.sh` がスロットについて同じ教訓を既に抱えている——*CP を先に止めると走っているスロットが
起きたまま取り残される。止めたつもりのあとで一番高いものが課金され続けるという、一番痛い間違え方
である。* GPU の箱は $1.17/時で、それを止めるはずだったのは VM の中のコントローラだけだ。

だから停止手順は: エンジンを `off`（または idle 停止を待つ）→ 箱が走っていないことを確認 →
VM を停止。夜間停止を自動化する（決定 10 の P2）なら、自動化がこの順序を実行するのであって、
`stop-instances` 単体ではない。

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

## 定価でいくらか（算術であって、請求書ではない）

月あたり・ap-northeast-1・730 時間。GPU は両側から除いてある: 買うコードは両方同じで、ADR 0074 の
実測（`g6.xlarge` $1.1672/h、ADR 0077 以降はその上に載る managed-instances 手数料が無い）は
ここでは変わらない。

| | 毎日立っている ECS 構成 | この ADR |
|---|---:|---:|
| NAT ゲートウェイ | ~$36 | **$0**（決定 4） |
| ALB | ~$18 | $0 |
| RDS（db.t4g.micro） | ~$18 | $0（SQLite） |
| EFS | ~$6 | $0 |
| CP の Fargate ＋ スロットとそのボリューム | 使用量 | $0（VM の上のコンテナ） |
| VM | — | $63（`t4g.large`）/ **$31（`t4g.medium`）** |
| その EBS ＋ 公開 IPv4 | — | ~$14 |
| `20-platform` ＋ `60-engines` の常設 | 両方同じ | ~$5〜15（ECR・モデルバケツ・名前空間・secrets） |
| **止められない床** | **~$78** | **~$10**（停止中の VM はその EBS だけ） |

最後の行がこの決定である。24/7 の `t4g.large` は ECS の床とほぼ同額——**この ADR は、存在するだけでは
元が取れない**。元が取れるのは床を**選べるようになる**ことによってだ: インスタンスを適正化し、
Graviton にし（両イメージとも既にマルチアーキ——`release.sh:67`）、GPU を貸していない夜は API 1 回で
配備ごと止める。ECS 構成には最後のそれができない。

## 却下した案

| 案 | 却下の理由 |
|---|---|
| **VM 自体を GPU インスタンスにして**、エンジンを compose のサイドカーで動かす | `g6.xlarge` 24/7 は月 $850 ほど。ADR 0071 のコントローラの存在意義が「GPU は 1 日のほとんど寝ている」ことである |
| **ECS を使わず、素の EC2 の上でコンテナを動かすエンジン lifecycle を新設する** | 得るものが無い。ECS クラスタと desired count 0 のサービスの請求は $0 である。ラダー・offers・配置・fetch サイドカー・active set——ADR 0071/0072/0074/0075/0077——を、既にゼロの行を消すために作り直すことになる |
| **自宅からエンジン VPC へトンネルを掘り**、AWS 側に配備を置かない | ADR 0079 が理由つきで却下済み（インスタンスは短命・Cloud Map 経由・SG は CP しか通さない）。しかも安くならない: トンネルには VPC 内に常時起動の箱が要り、それはこの ADR の VM そのもので、配備が無いぶん機能だけ減る |
| **ECS の af-sandbox から借り続け、もっと強く pause する** | `pause.sh` のヘッダ自身が残るものを書いている: NAT・ALB・RDS・EFS。しかも休止中の配備は GPU を貸せない——貸すために立てているのに |
| **Caddy の代わりに ACM** | 決定 9 |
| **NAT ゲートウェイの代わりに SSM インターフェースエンドポイント** | 1 AZ で月 ~$9、しかも決着しない: ingest タスクは依然としてインターネットが要るので、何らかの NAT が残る。決定 4 の経路が不安定だった場合の退避先 |
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
6. **費用の按分は生き残るか。** `60-engines` がエンジン資源に `af-role` を打つのでエンジンの行は
   解決できるはず。VM は「全ワークスペースを載せた、タグの無いインスタンス 1 台」で、これは
   `docs/log/67` が既に警告している「77.7% は共有」の形そのものである。
7. **4 GB で足りるか**——CP・Caddy・ワークスペース 1 つ・NAT 経路。ec2-single の README は
   `WS_MEMORY` を下げれば `t3.medium` で動くと既に書いている。

## フェーズ

- **P0——動く。ネットワークは何も変えない。** `ec2-single` に VPC 配置・2 枚目の SG・
  インスタンスプロファイルを足し、`standup.sh` にエンジン 3 点の経路を足し、`AF_ENGINE_SUBNETS` を
  入れる。NAT ゲートウェイは今のまま。完了条件は、VM の Control Plane が買って止めた箱の上で
  セッションが絵を 1 枚出すこと。
- **P1——NAT ゲートウェイを外す。** `PrivateEgress` と VM の経路。完了条件は、NAT ゲートウェイを
  削除した状態で ingest と borrow が両方通り、3 日ぶんの請求が手元にあること。
- **P2——床を選ぶ。** インスタンスの系統と大きさ、決定 7 の順序を守った夜間停止、image 役だけの
  Spot（ADR 0075 の切り分け: 中断された会話と中断された絵は同じではない）。
- **P3——任意。P1 が「経路が問題だ」と言った場合だけ。** active set をモデルバケツに publish して
  無料のゲートウェイエンドポイント越しに読ませ、ingest タスクに公開 IP を持たせる
  （`engine_ingest.go`）。private サブネットの外向きの必要が完全に無くなる——そして
  `engine-tools/CONTRACT` と `TAGS.tsv` のバンプを伴う。リポジトリで最も壊れやすい継ぎ目なので、
  意図的に最後に置く。

## 確認した出典（2026-09-22・このリポジトリ）

| 主張 | 場所 |
|---|---|
| レジストリが要るのは表と AWS 資格情報だけ | `control-plane/engines.go:721-775` |
| エンジンのクラスタは既に別変数 | `control-plane/engines.go:812` |
| 買った箱を置くサブネットは `AF_ECS_SUBNETS` から来る | `control-plane/engines.go:609` |
| AWS を任意にしているのは external/remote 行 | `control-plane/engines.go:705-715` |
| エンジンは CP の SG を import で許している | `deploy/aws/ecs/cfn/60-engines.yaml:422-433` |
| エンジンサービスは `awsvpc`・private サブネット・公開 IP 無し | `deploy/aws/ecs/cfn/60-engines.yaml:768-776` |
| CP のエンジン IAM は import した役に貼られている | `deploy/aws/ecs/cfn/60-engines.yaml:279-300,350-378` |
| `ec2:CreateFleet` は CP のもの | `deploy/aws/ecs/cfn/60-engines.yaml:321` |
| `20-platform` の import は VPC id だけ | `deploy/aws/ecs/cfn/20-platform.yaml:139` |
| `20-platform` に時間課金は無い | `deploy/aws/ecs/cfn/20-platform.yaml:34-210` |
| NAT ゲートウェイと無料の S3 ゲートウェイエンドポイント | `deploy/aws/ecs/cfn/00-network.yaml:136-172` |
| public サブネットは既に起動時に公開 IP を付ける | `deploy/aws/ecs/cfn/00-network.yaml:75,83` |
| fetch サイドカーの S3 と SSM 呼び出し | `deploy/aws/ecs/engine-tools/fetch-models.sh:79,99,105,142` |
| ingest タスクの `AssignPublicIp` は定数 | `control-plane/engine_ingest.go:1068,1660` |
| `standup.sh` が必須にしているもの | `deploy/aws/ecs/standup.sh:130` |
| 休止しても残るものと、順序の教訓 | `deploy/aws/ecs/pause.sh`（ヘッダ） |
| 唯一の実測請求 | `docs/log/67-member-cloud-cost.md` §67.3 |
| ec2-single は既定 VPC の VM 上の compose・インスタンスプロファイル無し | `deploy/aws/ec2-single/cfn.yaml` |
| RDS ではなく SQLite | `deploy/compose/.env.example:374` |
| Caddy が ACME で TLS を終端する | `deploy/compose/Caddyfile:14-16` |
| 両イメージとも arm64 をビルドする | `deploy/compose/release.sh:67-72` |
| この ADR が変える能力表 | `guide/ref/deploy-targets.md` |
| この ADR の正体である「後回しにした案」 | `docs/decisions/0079-remote-engine-from-another-deployment.md:564` |

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
