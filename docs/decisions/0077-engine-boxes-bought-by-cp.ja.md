# 0077. エンジンの箱は CP が EC2 Fleet で買う——ECS は EC2 launch type で走らせ、Managed Instances をやめる

[English](0077-engine-boxes-bought-by-cp.md) | 日本語

- 状態: **提案**（2026-09-12）。実装はしていない。
- **この文書のために GPU は 1 台も買っていない。** 数字はすべて出所を書き分けてある——
  (a) ADR 0045・0070・0071・0074・0075 の実測、(b) 2026-09-12 にこのリポジトリのコードとテンプレート、
  および af-sandbox の読み取り専用 API から読んだ事実、(c) AWS の公開仕様として知っているだけで
  **この配備では確かめていない**もの（EC2 Fleet `instant` の応答の語彙、ECS agent の Spot 排水、
  EC2 launch type の `awsvpc` の ENI 上限、CloudFormation が `CapacityProviderStrategy` → `LaunchType`
  を置き換えとして扱うか）。(c) は決定の根拠にしていない。依存する箇所は「未解決の点」に列挙し、
  どの決定がそれに依存するかを各項に書いた。
- **利用者の要求は ADR 0075 と 1 文字も変わらない**——(1) 必要な VRAM を満たすものを、オンデマンドと
  Spot を区別せず安いものから買う。(2) Spot が取れなければオンデマンドで取る。(3) Spot の突然死は許容し、
  死んだら規則 2 で立て直す。例外: llm 役はオンデマンド専用。**変わるのは「誰が箱を買うか」だけ**である。
- 🔴 **この ADR は ADR 0075 の見直し条件が成立したことから書かれた。** 0075 は却下案「EC2 Fleet /
  Auto Scaling group へ乗り換える」に「規則 2 の実装が『AWS の再試行と CP の再試行が二重になる』形に
  育ったら、この却下は見直す」と書いた。実機 3 巡（実機 0・実機 1〜7・再走）でその形になった（背景）。
- 関連: [0075-engine-purchase-offers.ja.md](0075-engine-purchase-offers.ja.md)（提案一覧・VRAM の絞り込み・
  固定と自動・パネルの契約は**そのまま継承する**。覆すのは決定 3・4・5・12）/
  [0071-self-hosted-inference-engines.ja.md](0071-self-hosted-inference-engines.ja.md) 決定 1（GPU は MI で買う
  ——**覆す**）・決定 2（役ごとに provider——**消える**）・決定 5（起こして待つ）・決定 7（`draining`）/
  [0074-engine-instance-classes.ja.md](0074-engine-instance-classes.ja.md) 決定 5（段の適用——**消える**）・
  決定 9（IAM——**置き換わる**）/
  [0045-ec2-persistent-workspace.ja.md](0045-ec2-persistent-workspace.ja.md) 決定 6（Workspace に MI は
  使わない——不変）・決定 19（自前 AMI は遅い——撤回済み・継承）・決定 22・23・29（プールの不変条件）/
  [0070-tts-ondemand-engine.ja.md](0070-tts-ondemand-engine.ja.md) 決定 1（置き場所を必ず明示する——不変）

## 背景

### ADR 0075 の実機 3 巡が示したこと

0075 は「役ごとに provider を 2 本置き、service の strategy を running 0 のときだけ差し替える」設計で、
コード（#548・#549・#552・#561・#564）は全部 develop に入っている。実機で出た問題は、どれも
**購入の判断を ECS service のデプロイ機構に載せていること**から来ている（出所はすべて 0075 の追記）。

| 実機 | 出たこと | 直し | 直しの対価 |
|---|---|---|---|
| 0（$0） | strategy を変える `UpdateService` は desired 0 でも `forceNewDeployment` 必須（HTTP 400） | force を渡す | — |
| 1〜7（$1.45） | 予算 180 秒は「箱が来たか」を見ず、一覧を歩き切って 2 台買い 0 台起動 | 予算は箱の到着で止める | provider 名指しの照合が要る |
| 1〜7 | Spot のクォータ超過は `MaxSpotInstanceCountExceeded` が別のエラーに包まれて来る。3 コード表に無く 15 分待った | 4 つ目のコードを部分一致で | 文字列照合が 1 つ増える |
| 1〜7 | desired 0 → 1 と strategy を 1 回で渡すと ECS が古い strategy で先に置き、**箱を 2 台買う** | 起動を 2 回に割る | — |
| 再走（$0.38） | 2 回に割っても、古い deployment が ACTIVE のうちは ECS がそちらにも置き、**いまも 2 台** | 門を「古い deployment が消えた」に上げる | **起動が 2 分 35 秒以上遅くなる** |
| 再走 | 前の提案の（別 provider の）イベントを次の提案の答えとして読み、起動ごと諦めた | イベントを provider 名と時刻で絞る | 誤判定 1 つで一覧全部が無効になるリスクは残る |
| 1〜7 | 「買えない提案」は宣言で作れない。`UpdateCapacityProvider` が 400 で拒み CP は起動を続けた | `unusable` を足す | — |
| 3 | 3 型の Spot 行で来た箱は **g6e.xlarge**（最安の g6 ではない）。MI に allocation strategy は無い | 直せない | — |

**共通の形**: ECS は「タスクが置けない」を見て箱を買いに行き、CP はその**結果**をサービスイベントの
文字列と deployment の状態から**推測**する。買う主体と判断する主体が違うので、(i) 2 台買う、(ii) 前の
判断の残響を次の判断が読む、(iii) 買えない要求が黙って前の要求のままになる、が構造として起きる。
直すたびに実機が 1 巡要り（3 巡で約 $2.2）、直した先で同じ種類の穴が出た。

### いまのコードとテンプレートが言っていること（2026-09-12 に読んだ）

- **エンジンは MI の資源に縛られている**（`60-engines.yaml`）: capacity provider 3 本（llm・image・
  image-spot）と `Associations`、`InfraRole`（`AmazonECSInfrastructureRolePolicyForManagedInstances`）、
  `InstanceRole` / `InstanceProfile`（`AmazonECSInstanceRolePolicyForManagedInstances`）、タスク定義の
  `RequiresCompatibilities: [ MANAGED_INSTANCES ]`、匿名の `Host: {}` ボリューム。service は `awsvpc`・
  Cloud Map の A レコード・`MinimumHealthyPercent: 0`・placement constraint 無し。GPU は
  `ResourceRequirements: [{Type: GPU}]`。
- **MI の AMI は Bottlerocket で、モデルの置き場はタスク ID 入りの匿名ディレクトリ**（PARAMETERS
  「The model volume」・0071 P0 実測 4）。`SourcePath` はルートに落ちて `No space left on device`、匿名は
  片づけられずに 4 起動で 18.5 GB × 4。結論は「MI では温かいモデルボリュームは作れない」。
- **スロットプールは CP が EC2 を買っている**（`runtime_ecs_ec2.go`）: `RunInstances` 1 台ずつ、
  launch template（`40-ec2-pool.yaml`、AMI は SSM 公開パラメータ `…/amazon-linux-2023/recommended/image_id`
  を CFN が解決。`MetadataOptions HttpTokens: required`。テンプレートが付けるタグは `af-managed-by`
  だけ）、user data で `ECS_CLUSTER` を書いてクラスタに入る、タグ `af-pool` / `af-role=slot` /
  `af-slot-size` は **CP が `RunInstances` 時に付ける**（テンプレートではない）、登録待ちは
  `ListContainerInstances` を 3 秒ごと、退場は `DeregisterContainerInstance (Force)` →
  `TerminateInstances`。容量エラーは `isEC2CapacityError`（文字列照合）で次の AZ へ。
  **Spot は使っていない**（`InstanceMarketOptions` / `CreateFleet` はリポジトリに 1 か所も無い）。
  エンジンの service と MI の provider 3 本は既にタグ `af-pool` / `af-role=engine-<役>` を持つ
  （`PropagateTags: SERVICE`）ので、決定 3 が使う語彙は新しくない——新しいのは、それが CP が
  列挙できるインスタンスに付くことである。
- 🔴 **スロットプールが「自分の箱でない」と判定する唯一の根拠は `capacityProviderName` が空でないこと**
  （`isPoolContainerInstance`。0071 レビュー R7(a) の取り込み）。CP 自身が EC2 で買った箱は
  `capacityProviderName` が**空**なので、この判定はそのままでは崩れる。EC2 側の走査 5 つ
  （`slotsOfMyType` とそれを通る `freeSlots`・`poolSize`・`sweepFreeSlots`・`makeRoom`・`PoolStatus`）は
  `af-pool` **と** `af-role=slot` の両方で引くので混ざらない。**1 つだけ違う: `sweepSlotOwnerTags` は
  `af-pool` だけで引き**、見つけた全インスタンスに `af-membership` / `af-tenant` を書いたり剥がしたりする
  ——`af-pool` を付けたエンジンの箱はこれに歩かれる（決定 3）。ECS 側で混ざるのは `registeredSlots`・
  `sweepGhostInstances`・`deregisterSlot`・`slotTaskCounts` の 4 か所、関数は 1 つ。
- **Workspace のタスクは task definition の placement constraint `memberOf(ec2InstanceId == …)` で
  特定の箱に置かれる**。service は `LaunchType: EC2`・`awsvpc`・desired 1。
- **CP の IAM**（`20-platform.yaml`）: Sid `Ec2SlotPool` に `ec2:RunInstances` / `TerminateInstances` /
  `DescribeInstances` / `CreateTags` など `Resource: *`（Describe は資源に限定できず、柵はタグ）、
  Sid `PassSlotRole` に `iam:PassRole` の `role/af-*-slot`（`iam:PassedToService: ec2.amazonaws.com`）、
  Sid `EcsContainerInstances` に container instance の 3 操作。**どれも条件付きではない——
  `20-platform.yaml` に `Conditions:` 節は無く、全フレーバーが持つ**（「ecs-ec2 のみ。Fargate では無害」の
  コメントは散文であって条件ではない）。`ssm:GetParameter` は**ある**（Sid `SsmWorkspaceParams`、
  `parameter/af-ws/*` 限定）ので、`/aws/service/` 配下の AMI パラメータは読めない。
  **`ec2:CreateFleet` はどこにも無い。** MI の権限（`ecs:DescribeCapacityProviders` /
  `UpdateCapacityProvider` / `PutClusterCapacityProviders`、`iam:PassRole` の `InfraRole` /
  `InstanceRole`）は **`60-engines.yaml` の `CpIngestPolicy`** に、Sid 無しの文として CP のタスクロールへ
  import で貼られている——20-platform ではない。
- **CP の `main` パッケージに EC2 クライアントは無い。** エンジンのコードのポートは `engineECSAPI`
  （`engine_ecs.go`: DescribeServices / UpdateService / ListContainerInstances /
  DescribeContainerInstances）と `engineCapacityAPI`（`engine_class.go`）。唯一の EC2 ポート `ec2API` は
  `internal/runtime` の非公開で、ecs-ec2 のランタイムしか組み立てない。`CreateFleet` はいまのどちらの
  ポートにも足せない。
- **クラスタは 1 つで共有**。af-sandbox の実物 (b): container instance 4 台（m7i / m8g のスロット・
  `capacityProviderName` 無し）と provider 5 本（FARGATE・FARGATE_SPOT・llm・image・image-spot）が並ぶ。
- **ECS 最適化 GPU AMI の SSM 公開パラメータは ap-northeast-1 で引ける** (b):
  `/aws/service/ecs/optimized-ami/amazon-linux-2023/gpu/recommended/image_id`（2026-09-12 の値は
  `al2023-ami-ecs-gpu-hvm-2023.0.20260901-kernel-6.1-x86_64-ebs`、ECS agent 1.106.2、Docker 25.0.16）。
  AL2 GPU も引ける。
- **`AWSServiceRoleForEC2Fleet` は af-sandbox に無い** (b)。`AWSServiceRoleForEC2Spot` は 0074 で作った。
- 0075 の実装のうち、**買う主体に依存しないもの**: 提案の解析（`buy` 欄）、VRAM の絞り込み、固定と自動
  （`engine_<役>_class`）、`offer_trail` と契約 B、Console のカード（#549）。依存するもの:
  `engine_offer.go` の「買う」「箱を待つ」「失敗コードを読む」（`startOnOffer` / `applyFirstUsableOffer` /
  `stepOffers` / `offerBoxIsUp` / `moveToNextOffer` / `engineOfferVerdict` / `engineEventIsAbout` と
  実行状態の時計）、`engine_ecs.go` の strategy 書き込み（`setStrategy` / `writeStrategyOnly`）と
  provider 名指しの `boxOn()` / `describeBoxes()`、`engine_class.go` の `applyEngineClass`
  （`UpdateCapacityProvider`）と `startGate`。`startGate` の門は順に 4 つ——梯子無し（素通し）・VRAM で
  絞って候補無し（拒む）・入れ替え待ち（いまの箱の型が先頭候補に無い）・段の適用——で、ここに残るのは
  中の 2 つだけ。「一周したら cooldown」は門ではなく controller（`engine_control.go`）にあり、残る。
- **0075 決定 6（中断）はコードには `noteReplacement` しか無い**——desired 1 のまま running → starting を
  見たときのログ 1 行と監査 1 行。「一覧の先頭から立て直す」「同じ提案で 2 回続けば飛ばす」は 0075 の
  P1 で、着手されていない（`engine_offer.go` の冒頭がそう言っている）。決定 4 が継承するのは設計であって
  実装ではない。
- **契約 A（エンジン表）は SSM パラメータ**（`EnginesParam`、`/af-ws/engines`）で、その名前が Output/Export
  と env `AF_ENGINES_SSM_PARAM` で CP に届く。欄の集合を固定するスキーマも golden も無く、改名で落ちるのは
  `engine_gateway_test.go`・`engine_offer_test.go`・`engine_table_reload_test.go` の手書き JSON と
  `60-engines.yaml` の生成部。Output `Llm` / `Image` / `ImageSpot` の `CapacityProviderName` 3 本は
  harness スクリプト 6 本（`bench-image-engine`・`probe-fetch-client`・`probe-llm-mount-load`・
  `probe-s3-fetch-tuning`・`probe-s3-mount`・`probe-warm-volume`）が読む。
- **`teardown.sh` はエンジンの箱を terminate しない**: desired 0 にして、スタック削除のあいだに MI の
  ドレインが箱を消すのに任せている。スロットは自分でタグから terminate する。

### 公開仕様として知っていること (c)——決定の根拠にはしない

- **EC2 Fleet の `instant` 型**は「**同期の 1 回要求**で、起動した箱と、起動できなかった箱の**エラーを
  応答で返す**」。Spot とオンデマンドを 1 要求に載せられ、複数の型と AZ を overrides に並べ、Spot の
  allocation strategy に `price-capacity-optimized`（最も空いているプールから、その中の最安）が使える。
  launch template の `ImageId` に `resolve:ssm:<パラメータ>` を書けるのは **instant 型だけ**。箱が全部
  terminate されるか 1 台も起動しなかったら、fleet は自動で削除される。
- 同じページが `RunInstances` の Spot について言うこと: 「**1 型・1 AZ に限られ**、Spot とオンデマンドを
  同じ要求に載せられず、その Spot プールに在庫が無ければ **`RunInstances` の呼び出しが失敗する**」。
- ECS agent の `ECS_ENABLE_SPOT_INSTANCE_DRAINING=true` は、Spot の 2 分前予告を受けると container
  instance を DRAINING にする（AWS のページは 2026-09-12 に取れず、**未確認**）。
- `awsvpc` の EC2 launch type は、タスクごとに ENI を 1 つ使う。g6.xlarge の ENI 上限は 4（**未確認**）。

### ADR 0071 決定 1 が MI を選んだ 3 つの理由を、いま読み直す

| 0071 の理由 | 2026-09-12 の状態 |
|---|---|
| **AMI と NVIDIA ドライバの所有** | AWS が管理する ECS 最適化 GPU AMI が SSM 公開パラメータで引ける (b)。所有しない。0045 決定 19 が「自前で焼いた AMI は遅い」と撤回済みなので、焼く動機も無い |
| **停止中の EBS 課金** | エンジンは stop しない。**terminate する**（0045 決定 23 がスロットで採ったのと同じ）。停止中の課金は存在しない |
| **ADR 0045 の走査と `Ec2MaxSlots` の外に置く配慮が全経路に要る** | 本当に要る。ただし EC2 側はタグで既に外れ、ECS 側は 4 か所が 1 つの関数（`isPoolContainerInstance`）を通る（背景）。決定 3 で判定を属性に変える |

0071 が「MI が東京に無い配備のための代替案として残す」と書いた自前 EC2 は、**その日から代替案として
存在していた**。0045 決定 6 が Workspace について MI を退けた理由（stop が無い・既存ボリュームを指せない）は
エンジンでは利点だったが、その利点は「タスク 0 でインスタンスが消える」ことであって、**買う判断を ECS に
預けること**ではない。決定 5 で terminate を CP が持てば、利点は残り、預けた判断だけが戻る。

## 決定

### 1. 箱は CP が `CreateFleet(type=instant, TotalTargetCapacity=1)` で買う。提案 1 行 = 1 回の呼び出し

0075 の提案一覧（`<役>Offers`、8 欄・`buy` 欄）は**そのまま**である。変わるのは 1 行を「試す」とは何かで、
**「その行の型の組を overrides に並べ、`buy` を `DefaultTargetCapacityType` にして instant fleet を 1 回
呼ぶ」**になる。

- `buy=spot`: `DefaultTargetCapacityType: spot`、`SpotOptions.AllocationStrategy: price-capacity-optimized`。
  0075 実機 3 で 3 型の Spot 行に **g6e.xlarge**（最安ではない）が来た問題は、MI に allocation
  strategy が無いことが原因で、ここで初めて制御できる。
- `buy=od`: `DefaultTargetCapacityType: on-demand`、`OnDemandOptions.AllocationStrategy: prioritized`。
  ⚠️ `prioritized` が読むのは**各 override の `Priority` 欄**で、並び順ではない (c)——CP は宣言順に
  `Priority` 1, 2, … を書く。優先度が宣言順に並ぶことをテストで固定する。
- overrides は「型 × 私有サブネット」の直積。AZ を CP が選ぶ必要は無い（スロットの `spreadAZs` は
  home ボリュームの AZ 拘束のためにあり、エンジンには home が無い）。
- 🔴 **応答は同期である。** 起動した箱があればその `InstanceId` が返り、無ければ `Errors[]` に
  `ErrorCode` が並ぶ。**0075 の予算の時計・サービスイベントの文字列照合・deployment の門は、この 1 点で
  全部消える。** 次の提案へ移るのは「応答に箱が無かった」ときで、待つものは何も無い。
- 予算（`<役>OfferBudgetSec`）は意味が縮む——**箱が ECS に登録されるまでの上限**（スロットの
  `waitSlotRegistered`・3 秒ポーリングと同じ）だけになる。既定は 300 秒（0045 決定 22 の実測: 起動→ECS
  登録 21 秒、自前 AMI で 77 秒。10 倍強）。超えたら箱を terminate して次の提案へ。
- instant fleet は箱が消えると自動で削除される (c)。CP は fleet の id を**覚えない**。覚えるのは
  `InstanceId` と、箱に付けたタグである（決定 3）。⚠️ instant fleet が本当に何も残さないかは未解決 3 で
  測る（$0 の呼び出しの後に `describe-fleets`）。残るなら、応答を読んだ直後に CP が
  `DeleteFleets(TerminateInstances=false)` を打つ——`ec2:DeleteFleets` はどちらにせよ足す（決定 10）。

🔁 **反証されたら変える条件**: `instant` の応答が「箱もエラーも無い」形を返す例が実機で出たら
（(c) は「必ずどちらか」と言う）、`DescribeFleets` で 1 回だけ追う経路を足す。

### 2. ECS は走らせるだけ。service は `LaunchType: EC2`、strategy を持たない。desired は箱が来てから上げる

- service は `LaunchType: EC2` を**明示**する（0070 決定 1 の不変条件——置き場所を書かないことが
  禁止であり、`LaunchType` はそれを満たす）。`CapacityProviderStrategy` は持たない。CloudFormation が
  リリースのたびに strategy を宣言へ戻す問題（0075 決定 12 の CFN の行）は、**戻すものが無いので消える**。
- タスク定義は `RequiresCompatibilities: [ EC2 ]`。`awsvpc`・Cloud Map・GPU の
  `ResourceRequirements`・fetch サイドカー・idle wrapper は不変。
- **placement constraint は service に置き、役の属性で書く**: `memberOf(attribute:af-role == engine-<役>)`。
  スロットが task definition に `ec2InstanceId == …` を書くのは「その利用者をその箱に」だからで、エンジンは
  箱が入れ替わるたびにタスク定義を切りたくない。役の属性なら箱が変わっても service は不変である。
- 🔴 **順序が 0075 と逆になる。** 0075（MI）は desired を 1 にすると ECS が箱を買いに行った。ここでは
  **CP が箱を買い、登録を待ち、それから desired を 1 にする**。箱が無いのに desired が 1 になる時間は
  存在しないので、「PENDING のタスクを誰が置くか」という 0075 の再走の問題（古い deployment が置く）は
  形として起きない。
- desired 0 → 1 の `UpdateService` は desired だけを運ぶ（strategy 無し・`forceNewDeployment` 無し）。
  0075 実機 0 の 400 は strategy を変えるときの話で、ここでは変えるものが無い。

🔁 **反証されたら変える条件**: 属性の placement constraint が EC2 launch type の service で効かない
（未解決 2）なら、スロットと同じ `ec2InstanceId` をタスク定義に書き、起動のたびに revision を切る。

### 3. 箱の同定は ECS の属性 `af-role=engine-<役>` と EC2 のタグで行う。スロットプールの判定を変える

- launch template の user data が `ECS_INSTANCE_ATTRIBUTES={"af-role":"engine-<役>"}` を
  `/etc/ecs/ecs.config` に書く（スロットは `ECS_CLUSTER` を書いている同じ場所）。役ごとに launch
  template を 1 本（`af-<stack>-engine-llm` / `af-<stack>-engine-image`）。
- EC2 のタグ: `af-pool=<クラスタ名>`・`af-role=engine-<役>`・`af-engine-offer=<提案 id>`・
  `af-engine-buy=spot|od`・`af-managed-by=agent-fleet`。0045 決定 29 の一般則（「実体が AWS にあり
  名前が DB にあるとき、掃除を DB からしか辿れないと『行だけ消えた』が恒久的な漏れになる」）に
  従い、**CP の記憶が無くても `describe-instances` のタグだけで自分の箱を全部見つけられる**ようにする。
  MI と違って CP が買った EC2 は `describe-instances` で列挙できる（0071 P1 実測 2 の裏返し）。
- 🔴 **`isPoolContainerInstance` を「`capacityProviderName` が空」から「`capacityProviderName` が空、
  **かつ**属性 `af-role` が無いか `slot`」へ変える。** provider の判定は残す（MI の箱は属性を持たないので、
  外すと MI の箱がプールに戻る——MI の箱は決定 11 の移行が全配備で済むまで存在する）。これが 0071
  レビュー R7(a) の判定の後継で、MI の箱（provider 名あり）も EC2 のエンジン箱（属性あり）も、同じ 1 つの
  関数でスロットから外れる。テストは 3 種の container instance（スロット・MI エンジン・EC2 エンジン）で
  固定し、陽性対照は属性を外すとエンジン箱がスロットに数えられること。
- **エンジン自身の deregister は `deregisterSlot` を通さない**（プールの判定を当てるので、エンジンの箱を
  拒む）。エンジンのランタイムは container instance の ARN で直接 deregister する。
- EC2 側の走査は `af-pool` ＋ `af-role=slot` で既に外れている（`slotsOfMyType`・`poolSize`・
  `sweepFreeSlots`・`makeRoom`・`PoolStatus`）。**`sweepSlotOwnerTags` には欠けている `af-role=slot` の
  フィルタを足す**（背景）。テストの陽性対照は、フィルタを外すと走査がエンジンのタグの箱に触ること。
  これで `Ec2MaxSlots` の外に置く配慮が満たされる。
- 0070 決定 1 の不変条件「Workspace でないタスクが 1 つ混ざると前提が崩れる」は、**箱の側で**守る:
  エンジンの箱には Workspace のタスクが置かれない（Workspace の placement constraint が
  `ec2InstanceId` を名指しする）し、Workspace の箱にはエンジンが置かれない（決定 2 の属性）。

🔁 **反証されたら変える条件**: `ECS_INSTANCE_ATTRIBUTES` が AL2023 GPU AMI の agent で効かない例が
出たら（未解決 2）、`PutAttributes` を登録直後に CP が呼ぶ。

### 4. 中断は EC2 の事実として読む。立て直しは一覧の先頭から、同期に

- 0075 決定 6 の検出（desired 1 のまま running → starting、かつ箱が消えた）は**設計として**継承する:
  コードには `noteReplacement` のログと監査行しか無く、立て直しと飛ばす規則は 0075 の P1 で書かれて
  いない（背景）。P2 でここに 1 回だけ作る。箱の消失は **`describe-instances` の state（`shutting-down` /
  `terminated`）と、タグ `af-role=engine-<役>` の箱が 1 台も `running` でないこと**で言える。MI の
  「列挙できない」制約が無い。
- terminate されたインスタンスの container instance は ECS が自分で deregister する (c)。しなければ、
  エンジンの属性を持ち EC2 の実体が無い container instance がゴーストとして残る——プールの
  `sweepGhostInstances` はもう見ない（決定 3）ので、決定 5 の走査が拾う。
- ECS agent の Spot 排水 (c) を launch template で有効にする。2 分前予告で DRAINING になれば、
  service のタスクは先に止まり、`noteReplacement`（0075 背景 4）が「頼んでいない置き換え」を記録する。
  効かなくても検出は上の 2 条件で成立する。
- 立て直しは決定 1 の呼び出しを一覧の先頭からやり直す。同期なので、「ECS 自身の置き直しと競合する」
  0075 決定 6 の限定（先頭の候補がいまの provider と違うときだけ書く）は要らない——ECS は箱を買わない。
- 中断は失敗に数えない（0075 決定 6 継承）。立て直しが登録期限まで箱を得られなければ失敗として数える。
- **同じ提案で 2 回続けて中断されたら、その需要のあいだだけ飛ばす**（0075 決定 6 継承）。

🔁 **反証されたら変える条件**: 0075 未解決 9 と同じ（中断が稀なら飛ばす規則は要らない）。

### 5. 退場は CP が terminate する。`draining` は「CP が terminate を発行してから EC2 が消えるまで」

- アイドル窓（0071 決定 5・0070 決定 5）が閉じたら、CP は desired 0 → タスクが消えるのを待つ →
  `DeregisterContainerInstance(Force)` → `TerminateInstances`（スロットの `terminateSlot` と同じ順。
  0045 決定 23 が「自分で消すときに ACTIVE のゴーストを残さない」と決めた理由もそのまま当たる）。
- `draining`（0071 決定 7）の意味は「MI が管理外で 427〜463 秒かけて消す」から「**CP が terminate を
  発行し、EC2 が `terminated` になるまで**」へ変わる。スロットの実測（0045 決定 22: 停止→terminated
  93 秒は CPU の値）から、GPU でも分単位で収まるはずだが**未測**（未解決 6）。0071 決定 7 の
  「退場待ち」（0074 決定 4）は継承する——古い箱が `running` のうちに新しい箱を買わない。
- 🔴 **MI の `scaleInAfter` の罠は消える。** 0071 P0 実測 3 の「`-1` で残した箱は後から値を変えても
  回収されず、`terminate-instances` は MI のポリシーが拒む」は、CP が terminate の主体になった時点で
  存在しない。代わりに**CP が terminate を忘れると箱が永久に残る**——0045 決定 29 の形の走査を掃除
  ループに 1 段足し、監査に出す。向きは 2 つ: (a) タグ `af-role=engine-*` で `running` の EC2 インスタンスで、
  その container instance の**実行中・保留中のタスクが 0**（`DescribeContainerInstances` から読む。
  `sweepFreeSlots` / `makeRoom` が `slotTaskCounts` を読むのと同じ）、登録が `ghostAfter` より古く、
  その役に進行中の起動が無いもの——terminate する。(b) エンジンの属性を持つ container instance で EC2 の
  実体が消えているもの——deregister する（0045 `sweepGhostInstances` の形。あちらが見るのは登録の古さと
  「インスタンスが消えた」であって、タスク数ではない。タスク 0 の慎重さは free slot の走査のもの）。
  どちらの向きも CP の記憶は見ない——0045 決定 29 が求めるとおり、タグと属性で足りる。

🔁 **反証されたら変える条件**: terminate から `terminated` までが GPU で 5 分を超える実測が出たら、
0071 決定 7 の窓の計算を書き直す。

### 6. モデルの置き場は host volume にできる。P0 では匿名のまま、P1 で測る

- ECS 最適化 AL2023 GPU AMI は Bottlerocket ではない。`Host: {SourcePath: /var/lib/af-engine-models}` が
  普通に効き、g6 のローカル NVMe を user data で mount すれば、**同じ箱の 2 起動目はモデルを
  取り直さない**（MI では構造的に不可能だった——背景）。
- ただし価値は限定的である: 箱は退場（決定 5）で消え、中断（決定 4）で消える。温かいのは「アイドル窓の
  内側で desired を 0 → 1 した」ときだけで、それは 0071 決定 5 が既に「窓の内側では止めない」と
  決めている場面である。**したがって P0 は匿名の `Host: {}` のまま**（何も変えない）。
- P1 で測ること: 取り直しの実測（0071: S3 → ローカル 104〜147 MB/s、6.94 GB を 65 秒）に対し、
  host volume で何秒縮むか。縮むのが 1 分なら要らない。

🔁 **反証されたら変える条件**: 30 GB を抱える comfy の配備で取り直しが 5 分を超えることが実測で出たら
（0075「中断の実費」の計算値）、P1 を前倒す。

### 7. AMI は所有しない。launch template の `ImageId` は `resolve:ssm:` で SSM 公開パラメータを指す

- `resolve:ssm:/aws/service/ecs/optimized-ami/amazon-linux-2023/gpu/recommended/image_id`。instant 型は
  これに対応する (c)。CloudFormation の型 `AWS::SSM::Parameter::Value<AWS::EC2::Image::Id>`
  （スロットの `SlotAmiId`）は「スタック更新時に解決」であり、こちらは「起動時に解決」——**スタックを
  更新しなくても最新の AMI で起動する**。どちらにするかは未解決 7（起動のたびに AMI が変わることの
  是非）。
- 0045 決定 19 の教訓を継承する: 自前で焼いた AMI は遅い（新規 144 秒 → 192 秒）。焼かない。
- 0045 決定 19 が残した罠も継承する: パラメータの型が「AMI ID ではなく SSM パラメータ名」であることを
  README とスクリプトで言う。

🔁 **反証されたら変える条件**: 「最新の GPU AMI」が NVIDIA ドライバの版でエンジンのイメージと
噛み合わない日が来たら、パラメータを固定版（`…/gpu/<version>`）へ切り替えるつまみを足す。

### 8. 提案一覧・VRAM の絞り込み・固定と自動・パネルの契約は不変。段の適用は消える

- 0075 決定 1（宣言順が試す順）・決定 2（VRAM で絞る）・決定 8（固定と自動）・決定 11（パネルは
  service ではなく**箱のタグ**から言う——`af-engine-offer` / `af-engine-buy`）と、契約 B（`offers` /
  `offer` / `offer_trail`）は**そのまま**。Console（#549）は変更なし。
- **0074 決定 5 の「段の適用」（`UpdateCapacityProvider` の 4 欄 read-modify-write）は消える。**
  段は overrides の型の組そのものであり、宣言がそのまま要求になる。「宣言と実際がずれる」
  （0075 実機 1〜7 の `unusable`）は構造として起きない——型の綴りが違えば `CreateFleet` が
  **その場で**拒む。`startGate` の「退場待ち」だけが残る（決定 5）。
- 失敗コードの表（0075「失敗コードと対応」）は、サービスイベントの文字列から **`CreateFleet` の
  `Errors[].ErrorCode`** へ移る。語彙は未解決 3 で先に測る。対応の形は同じ（在庫無し → 次へ／
  クォータ → 同じ購入形態を飛ばす）。**「イベントを提案で絞る」（#564）は要らない**——応答は
  その呼び出しのものしか含まない。
- `usdPerHour` の規約（0074 決定 1・0075 決定 1: 買いうるいちばん高い型の**込み**価格）は、MI の
  管理料 7.80% が消えるので「EC2 の価格そのもの」になる。Cost Explorer の確定値で書く規約は不変。
  「管理料込み」と言っている 2 か所（`engine_class.go` の欄のコメント、PARAMETERS「The offers」）も
  一緒に変える。

🔁 **反証されたら変える条件**: 契約 B に「箱の id」を足す必要が出たら（0075 が `box.provider` を
CP 内部に留めた件）、Console レーンと合意して足す。

### 9. llm 役はオンデマンド専用のまま。TTS は Fargate のまま

- 0075 決定 9（llm に Spot の行を書かない）は継承する。ここでは「provider を作らない」に相当する
  二重の担保が無いので、**`LlmOffers` の `spot` 行を CP が拒む**（解析時に落としてログ）。
- 0075 決定 10（TTS）はこの ADR の外である。TTS は Fargate で、`FARGATE_SPOT` ↔ `FARGATE` の話は
  service の strategy に留まる。

### 10. IAM: CP に EC2 Fleet の権限を足し、MI の権限を全部外す

| 足す | 外す |
|---|---|
| `ec2:CreateFleet`・`ec2:DescribeFleets`・`ec2:DeleteFleets`（`Resource: *`。Fleet は資源に限定できない） | `ecs:DescribeCapacityProviders` / `ecs:UpdateCapacityProvider` |
| `ec2:CreateLaunchTemplateVersion` は**足さない**（CFN が所有） | `ecs:PutClusterCapacityProviders` |
| `iam:PassRole` を `role/af-*-engine`（`iam:PassedToService: ec2.amazonaws.com`）。`PassSlotRole` と同じ形の独立した文で | `iam:PassRole` の `InfraRole` / `InstanceRole` |
| `iam:CreateServiceLinkedRole` を `iam:AWSServiceName` が `[spot.amazonaws.com, ec2fleet.amazonaws.com]` に限定（後者が `AWSServiceRoleForEC2Fleet` を作る） | `InfraRole` そのもの |
| `ssm:GetParameters` の `parameter/aws/service/ecs/optimized-ami/*` は、**未解決 3 が「`resolve:ssm:` には `CreateFleet` の呼び手がこれを要る」と言ったときだけ** (c)。CP が持つ `ssm:GetParameter` は `/af-ws/*` 限定で、これは読めない | |

`ec2:RunInstances` / `TerminateInstances` / `DescribeInstances` / `CreateTags` は `Ec2SlotPool` に既にある。
`ecs:ListContainerInstances` / `DescribeContainerInstances` / `DeregisterContainerInstance` も
`EcsContainerInstances` に既にある。**どちらの Sid も `20-platform.yaml` で無条件で、全フレーバーが
持つ**（背景）ので、60-engines は繰り返さない。60-engines が触るのは自分の `CpIngestPolicy` で、MI の
文が出て Fleet の文が入る。権限の全集合は**未解決 3 の成果物**である: $0 の呼び出しをこの表で始め、
`AccessDenied` が名指したものを足して記録する（EC2 Fleet の公開例のポリシーは `ec2:*` で、最小を
何も言わない）。

⚠️ **`AWSServiceRoleForEC2Fleet` が無いアカウントでは最初の `CreateFleet` が失敗する** (c)。
af-sandbox に無い (b)。配備手順（`standup.sh`）で `create-service-linked-role --aws-service-name
ec2fleet.amazonaws.com` を `|| true` で 1 回打つ（`AWSServiceRoleForEC2Spot` を 0074 で手で作ったのと
同じ列。いまの `standup.sh` は `ecs.amazonaws.com` にしか打っていない）。CloudFormation の
`AWS::IAM::ServiceLinkedRole` にはしない——既にあるロールはスタックを落とす。

🔁 **反証されたら変える条件**: `CreateFleet` を launch template の ARN に限定できる条件キー
（`ec2:LaunchTemplate`）が実機で通るなら、`Resource: *` を狭める。

### 11. 移行: MI の資源を外し、launch template と instance role を足す。順序は 2 段

- `60-engines.yaml` から外す: capacity provider 3 本・`Associations` の provider 項目・`InfraRole`・
  `InstanceRole` / `InstanceProfile`（MI 用）・Output 3 本 `LlmCapacityProviderName` /
  `ImageCapacityProviderName` / `ImageSpotCapacityProviderName`（`LlmLaunchTemplateId` /
  `ImageLaunchTemplateId` に置き換える。これを読む harness スクリプト 6 本——背景——は新しい名前に
  移すか MI 時代のものと明記する）・パラメータ `*AllowedInstanceTypes` / `*AcceleratorMemMinMiB` /
  `*VCpuMin` / `*VCpuMax` / `*MemMinMiB` / `*MemMaxMiB` / `*UseLocalStorage` / `*ScaleInAfter`
  （要求は提案一覧に**既に**書いてある。0074 決定 1 の梯子が「段＝要求の組」で、0075 が `buy` を足した。
  MI の 4 欄は梯子の写しだった）。⚠️ **glob ではなく名指しで**: `*TaskMemory` と `*GpuCount` も
  `*Mem*` や MI のブロックに引っかかるが、タスク定義に流れるので**残す**。
- **`*StorageGiB` は残して意味を変える**: MI provider のストレージ要件から launch template の
  **root gp3 のサイズ**へ。匿名の `Host: {}` ボリュームはルートファイルシステムに落ち、ECS 最適化 AMI の
  既定 root は 30 GiB (c)——30 GB のモデルを抱える comfy の配備は溢れる。既定値は今のまま、名前を
  残すので `params/60-engines` の捕捉がそのまま運べる。
- 足す: 役ごとの launch template（AMI・instance profile・`EngineSg`・user data・`MetadataOptions
  HttpTokens: required`・`*StorageGiB` の root gp3・タグは `af-managed-by` だけで、残りのタグは
  スロットプールが `RunInstances` で付けるのと同じく CP が `CreateFleet` で付ける）、
  `EngineInstanceRole`（`service-role/AmazonEC2ContainerServiceforEC2Role` ＋
  `AmazonSSMManagedInstanceCore`——スロットのロールと同じ組）、`EngineInstanceProfile`。エンジン表の
  JSON は `capacityProvider` / `spotCapacityProvider` を `launchTemplate`（id）に置き換え、`offers` /
  `offerBudgetSec` / `classes` は不変。`CpIngestPolicy` は MI の文を Fleet の文に入れ替える（決定 10）。
- 🔴 **順序**: (1) **箱が 1 台も無い状態**（両役 `mode: off`・container instance にエンジンの箱が
  無い）で、(2) 本テンプレートを当てる。provider の削除は箱が無ければ通る（0074 未解決 1 の使い捨てで
  実測: `delete-capacity-provider` は箱 0 で即 `INACTIVE`）。service の `CapacityProviderStrategy`
  → `LaunchType` が CFN で置き換えになるかは未解決 1。⚠️ **両 service は `ServiceName` を明示している**
  （`af-<stack>-llm` / `-image`）し、`ServiceRegistries` を持つ。置き換えなら CloudFormation は古い
  service を消す**前に**新しい service を作るので名前が衝突し、更新は失敗してロールバックする。その
  ときの移行は既にある経路——`<役>Enabled=false`（条件が service を消す。`update.sh` がまさにこれを
  警告している）→ 当てる → `<役>Enabled=true`。あいだ Cloud Map の名前は消えるが、desired 0 なので
  失う要求は無い。service の `DependsOn: Associations` はそのまま（`Associations` は残る）。
- `Associations` を外すと、クラスタの provider 一覧は **60-engines が所有していたもの**なので、
  FARGATE / FARGATE_SPOT を誰かが持たなければならない。`50-tts` が strategy で `FARGATE_SPOT` を使い、
  自前の `Associations` は持たない（0070）。**`Associations` は残し、中身を `[FARGATE, FARGATE_SPOT]`
  にする**（provider を外すだけ）。engines スタックの無い配備には `Associations` が無く、50-tts は
  クラスタの既定一覧に寄りかかっている——この ADR で変えないが、誰かが資源を「片づけ」ないように書く。
- 0075 の実機 1（SPOT スタックの 2 段移行）は不要になる——provider ごと消えるので同名衝突は無い。
- 捕捉 `params/60-engines` に残る `*AllowedInstanceTypes` 等は `af_param_drop`（`env.sh` で定義、
  `standup.sh` が呼ぶ。0075 の `ImageCapacityOptionType` と同じ列）で落とす。`update.sh` は 60-engines に
  **1 組だけ**パラメータを渡す——稼働中のスタックから読み戻した `<役>Enabled=true`、これを直せる唯一の
  経路——ので、`cloudformation deploy` が名指ししないパラメータを前の値に保つ性質により、消えた
  パラメータはあのブロックを触らない限り黙って落ちる。
- **`teardown.sh` はエンジンの箱をタグ（`af-pool` ＋ `af-role=engine-*`）で terminate する**——スロットと
  同じ列で、スタックを消す前に。それをするはずの CP は手順 1 で先に止まっており、MI のドレインも
  もう肩代わりしない。
- テンプレートのサイズ: provider 3 本（約 3 KB）と MI のパラメータ群を外して launch template 2 本を
  足すので**減る見込み**だが、`wc -c` で前後を示す（壁 51,200・現在 42,500。`ecs-lifecycle-stub-test.sh`
  の CI ケース 3b-2 が壁を越えるとビルドを落とす）。

🔁 **反証されたら変える条件**: 未解決 1 が「service の再作成が要る」なら、移行は上の `<役>Enabled` の
往復になり、Cloud Map の名前が数分消える。TTS と違って待つ利用者はいない（image は再送、
llm は会話が切れる）ので、両役 `mode: off` の窓で行う。

## 却下した案

- **MI を直し続ける（0075 の 4 巡目）。** #564 は入れる価値がある（イベントの絞り込みは正しい）が、
  残る代償は構造で固定されている——起動は deployment が落ち着くまで 2 分 35 秒以上遅く、判断は
  文字列の推測で、3 型の Spot 行がどの型を買うかは制御できない。3 巡で約 $2.2 と 1 日を使って
  同じ種類の穴が続けて出た。**4 巡目が最後である保証が無い。**
- **Auto Scaling group（`maintain`）。** 混在ポリシーと Spot の置き換えは AWS 側にあるが、
  「1 役 1 台で desired を CP が握る」設計と ASG の自律的な置き換えが競合する——0075 決定 6 が
  「ECS 自身の置き直しと競合する」で困ったのと同じ形が、今度は ASG との間で起きる。instant で足りる。
- **`RunInstances`（スロットプールと同じ）。** Spot が 1 型・1 AZ に限られ、Spot とオンデマンドを
  1 要求に載せられない (c)。0074 の実測「型を 3 つに広げたら買えた（1 型は 1/10・3 型は 9/10）」が
  そのまま instant fleet の overrides になるので、こちらを採る。
- **`request` / `maintain` 型の EC2 Fleet。** 非同期で、0075 と同じ「結果を推測する」形に戻る。
- **自前 AMI の焼き込み（イメージ層やモデルを入れる）。** 0045 決定 19 が「遅い」で撤回済み。
- **Karpenter / EKS。** 規模が違う。1 役 1 台にコントローラをもう 1 つ置く理由が無い。
- **`ecs:UpdateCapacityProvider` を残して MI と EC2 を併存させる。** 2 つの買い方を CP が持つと、
  0075 の判定の穴が片側に残る。**一度に切り替える**（移行の節）。

## 上書きする既存の決定と、補遺の付け方

ADR は追記型・不変である（`docs/CONVENTIONS.ja.md` §5）。既存の決定の本文は書き換えず、
それぞれの ADR の末尾に「**追記 — ADR 0077 がこの決定を覆した（日付）**」を足す。

| ADR・決定 | 何が変わるか | 変わらないもの |
|---|---|---|
| 0071 決定 1「GPU は ECS Managed Instances で買う」 | **CP が EC2 Fleet で買う**。却下していた自前 EC2 の 3 点は背景の表のとおり解けた | 「タスクが無ければインスタンスが存在しない」（決定 5 で CP が保証する）／スロットプールには置かない |
| 0071 決定 2「役ごとに 1 つの capacity provider」 | **provider は無くなる**。役ごとに launch template 1 本 | 2 つの役を同じインスタンスに載せない |
| 0071 決定 7「`draining`」 | 意味が「MI の管理外のドレイン」から「CP の terminate 発行〜`terminated`」へ | 状態集合・退場待ち |
| 0074 決定 5「Describe → 写す → Update」 | **消える**。段は overrides の型の組 | 段の宣言・保存された選択が勝つ規則 |
| 0074 決定 9「IAM は 2 つの capacity provider に限定」 | **置き換わる**（決定 10 の表） | 資源に限定できないものはタグを柵にする |
| 0075 決定 3「役ごとに provider を 2 本」 | **消える** | 提案一覧の書式・`buy` 欄 |
| 0075 決定 4「strategy は running 0 のときだけ」 | **消える**（strategy が無い） | running 1 以上で箱を替えない（決定 5 の退場待ち） |
| 0075 決定 5「予算と失敗コード」 | 予算は**登録待ちの上限**へ縮み、失敗コードは `CreateFleet` の応答へ移る | 一周したら cooldown／監査 1 行 |
| 0075 決定 6「中断」 | 検出に `describe-instances` を使える（列挙できる） | 中断は失敗に数えない／2 回続けば飛ばす |
| 0075 決定 11「パネルは service の strategy から言う」 | **箱のタグから言う** | EC2 の価格には訊かない（`describe-instances` は読む） |
| 0075 決定 12「再適用は provider に限る／CFN が strategy を戻す」 | **消える** | — |

**覆らない決定**: 0070 決定 1（置き場所を必ず明示——`LaunchType: EC2` で満たす）、0045 決定 6
（Workspace に MI は使わない）、0045 決定 19（自前 AMI は遅い）、0045 決定 22・23・29（プールの
不変条件——決定 3 で箱の側から守る）、0074 決定 1・2・3・6・7・10・11、0075 決定 1・2・7・8・9・10。

## 未解決の点（$0 で測れる順）

1. 🔴 **CloudFormation で service の `CapacityProviderStrategy` → `LaunchType: EC2` がその場更新か
   置き換えか。** 公開仕様 (c) は API として有効な遷移に「capacity provider → launch type」を挙げるが、
   CFN の `AWS::ECS::Service` がどう扱うかは別である。使い捨てスタック（0074 未解決 1 と同じ形・
   service 1 本・$0）で change set を**実行して**確かめる（change set は `Conditional` としか
   言わない——0074 の教訓）。⚠️ 使い捨ての service は本物と同じく **`ServiceName` を明示**し、
   `ServiceRegistries` を持たせる——匿名の service で置き換えが通っても、うちの service については
   何も言えない（決定 11）。依存: 決定 11。
2. 🔴 **`ECS_INSTANCE_ATTRIBUTES` で付けた属性に、EC2 launch type の service の placement constraint
   `memberOf(attribute:af-role == …)` が効くか。** GPU 無しの m 系 1 台と GPU 要求の無いタスク定義で
   $0.05 以下。依存: 決定 2・3。
3. 🔴 **`CreateFleet(instant)` の `Errors[].ErrorCode` の語彙。** クォータを超える型（0075 実機 4 の
   `g6.4xlarge`）と、綴り違いの型と、在庫の無い型で、応答に何が入るか。$0（1 台も起動しない）。
   同じ呼び出しに 3 つ相乗りする: (a) **IAM の最小集合**——決定 10 の表で呼び、`AccessDenied` が
   名指したものを足す（特に launch template の `resolve:ssm:` に呼び手の `ssm:GetParameters` が
   要るか）。(b) **1 台も起動しなかった instant fleet が `describe-fleets` に残るか**（決定 1 は (c) を
   頼りに fleet の id を覚えない）。(c) `prioritized` が override の `Priority` を尊重するか（fleet に
   記録された設定で見える）。依存: 決定 1・8（失敗コードの表）・10。
4. **`AWSServiceRoleForEC2Fleet` を CP が自動作成できるか**（`iam:CreateServiceLinkedRole` の限定
   で通るか）。$0。依存: 決定 10。
5. **`awsvpc` の EC2 launch type で、g6.xlarge の ENI 上限とタスク ENI**。1 台 1 タスクなら上限 4 で
   十分なはずだが、`ECS_AWSVPC_BLOCK_IMDS` などの agent 設定を含めて (c)。依存: 決定 2。P1 の実機。
6. **terminate から `terminated` までの GPU の実測**（決定 5 の `draining` の長さ）。P1 の実機。
7. **AMI を起動時に解決するか（`resolve:ssm:`）、スタック更新時に解決するか（CFN の型）。** 前者は
   最新に追随し、後者は配備が世代を固定する。0045 のスロットは後者。依存: 決定 7。
8. **ローカル NVMe の mount。** AL2023 の ECS AMI は instance store を自動では mount しない (c)。
   user data で mount するなら、Docker の data-root かモデルの host volume か。依存: 決定 6。P1。
9. **ECS agent の Spot 排水が AL2023 GPU AMI で効くか** (c)。P2（中断の実機）。
10. **`price-capacity-optimized` が実際に g6 を選ぶか**（0075 実機 3 で MI は g6e を買った）。P1 の実機。

## フェーズと完了の定義

### P0 — $0 の前提測定（コードを書かない）

範囲: 未解決 1・2・3・4。**どれか 1 つでも赤なら、この ADR は決定ごとに書き直す**——特に 1
（移行の形）と 2（箱の同定）。

**完了の定義**: 4 件の判定が ADR 0075 の実機 0 と同じ形（手順・生の戻り値・判定）で追記されている。

### P1 — image 役の実装と実機

範囲: 決定 1・2・3・5・7・8・10・11（decision 4 の中断と 6 の host volume は入れない）。

- CP: **`main` パッケージに EC2 のポートを新設する**（`engineFleetAPI`: `CreateFleet`・
  `DescribeInstances`・`TerminateInstances`・`CreateTags`、未解決 3 (b) 次第で `DeleteFleets`）。fake
  つきで、**全フレーバー**で組み立てる（いまは ecs-ec2 のランタイムしか EC2 クライアントを作らず、
  エンジンのコードは持たない——背景）。`engine_offer.go` の「買う」「箱を待つ」「失敗コードを読む」を
  提案 1 行 = `CreateFleet` 1 回に差し替える。`engine_ecs.go` の `setStrategy` / `writeStrategyOnly` と
  provider 名指しの `boxOn()` / `describeBoxes()` を外し、箱の同定を属性とタグに。`engine_class.go` の
  `applyEngineClass` / `engineCapacityAPI` を外し、`startGate` は候補無しの拒否と入れ替え待ちを残す。
  `runtime_ecs_ec2.go` は `isPoolContainerInstance` に属性の条件を足し、`sweepSlotOwnerTags` に役の
  フィルタを足す。契約 A（表の JSON）は `capacityProvider` / `spotCapacityProvider` → `launchTemplate`
  （`engine_gateway_test.go`・`engine_offer_test.go`・`engine_table_reload_test.go` の JSON リテラルが
  追随する）。決定 5 の走査を両方向で。
- CFN: 移行の節のとおり——Output・`CpIngestPolicy`・`standup.sh` の service-linked role と
  `af_param_drop` の列・`teardown.sh` のエンジン箱 terminate・harness スクリプト 6 本を含む。
  PARAMETERS の「The capacity providers」「The offers」を書き直す。
- Console: **変更なし**（契約 B は不変）。

**完了の定義**:

1. 提案ゼロの配備で EC2 の呼び出しが 1 本も増えないことがテストで言える（0074 決定 3 継承・陽性対照つき）。
2. `CreateFleet` の応答の `Errors` で「次へ／同じ購入形態を飛ばす」が分かれることがテストで言える。
3. desired を 1 にする経路が**箱の登録の後にしか無い**ことがテストで言える（陰性の主張——番人を外すと
   落ちる陽性対照）。
4. `isPoolContainerInstance` が 3 種の箱（スロット・MI・EC2 エンジン）を正しく分けることがテストで言える。
5. `sweepSlotOwnerTags` がタグ `af-role=engine-*` のインスタンスに触らないことがテストで言える
   （陽性対照: フィルタを外すと書く）。
6. `od` の提案の overrides が宣言順の `Priority` を持つことがテストで言える。
7. **実機（af-sandbox・image 役・GPU 30 分・$1 まで）**: 0075 の実機 3〜7 と同じ判定に、
   **「箱は 1 台だけ」**（0075 で 3 回とも 2 台だった点）と「起動が deployment の落ち着きを
   待たない」（+2 分 35 秒が消える）を足す。Spot の行で来た型と `af-engine-buy` タグ、`InstanceLifecycle`。

### P2 — 中断と host volume

範囲: 決定 4・6。0075 P1 の手順（FIS か `terminate-instances`）をそのまま使う。**ここでは
`terminate-instances` が MI のポリシーに拒まれない**ので、陽性対照が素直に作れる。

### P3 — llm 役

image 役で P1・P2 が通ってから。差分は launch template 1 本と `LlmOffers` の `spot` 拒否だけ。

### 実機の駆動について

0075 と同じ——**`claude` のセッションで駆動する。1 レーン 1 配備。** 開発配備は他のセッションと
共有されているので、`60-engines` を触る前に誰も配備していないことを確かめる。P0 の 4 件は
使い捨てスタックと読み取り API だけで済み、配備には触らない。

## レビュー（2026-09-12・P0 の前）

ADR 0075 のレビューと同じ作法で、「決定 → 根拠 → 現状」の連鎖をコード・テンプレート・引用先の ADR に
照らした。判定: **承認（P0 に入ってよい）。ただし現状の記述 2 件が誤りで、決定 1 つがその上に
立っていた（R1・R2）。「影響なし」と書いた EC2 側の走査 1 つが影響を受ける（R3）。決定 2 つがコードに
無いものを「継承」していた（R4・R5）。移行に穴が 4 つ（R6〜R9）。決定 1 の細部 3 件が不正確（R10〜
R12）。本文は上で直してある**——この ADR はまだ「提案」で 1 行も実装していないので、直しは「実装が
本文を訂正する」段の前倒しであり、何をなぜ変えたかをここに残す。**この節のために測ったものは無い**。
コードとテンプレートを読み直せば足りた。参照はすべて（0045 決定 6・19・22・23・29、0070 決定 1、
0071 決定 1・2・5・7、0074 決定 5・9、0075 決定 1〜12 と実機の追記 3 本）本文の言う場所にあった。

### レビューが確かめたこと

- **R1. 「スロットプールを持たない配備には `Ec2SlotPool` / `EcsContainerInstances` が無い」は誤り。**
  `20-platform.yaml` に `Conditions:` 節は無く、両 Sid は全フレーバーの CP タスクロールにある。決定 10 の
  結び（「60-engines が同じ形で持つ」）は偽の前提から引いた結論だった。権限は 60-engines 自身の
  `CpIngestPolicy` へ入れる——置き換える MI の文が既にそこにある（Sid 無し。20-platform にも無い）。
  スロットロールの `iam:PassRole` は独立した Sid（`PassSlotRole`）で、エンジンのもその形に従う。
- **R2. 「`ssm:GetParameter` は無い」は誤り。** Sid `SsmWorkspaceParams` が `parameter/af-ws/*` 限定で
  持つ。正しいのは「`/aws/service/` 配下の AMI パラメータはその範囲外」。`resolve:ssm:` の launch
  template で `CreateFleet` の呼び手に `ssm:GetParameters` が要るかは (c) で、決定 10 は答えを断定して
  いた。いまは未解決 3 の一部で、IAM の最小集合ごと測る（`AccessDenied` は $0）。
- **R3. `sweepSlotOwnerTags` は `af-pool` だけで引く。** EC2 側の走査 5 つは `af-pool` と `af-role=slot`
  を組にするが、これだけは組にせず、見つけたものに `af-membership` / `af-tenant` を書く。`af-pool` を
  付けたエンジンの箱（決定 3）はこれに貼り替えられる。決定 3 で役のフィルタを足し、テストで固定する
  （完了 5）。あわせて訂正: スロットのタグは CP が `RunInstances` で付けるのであって
  `40-ec2-pool.yaml` ではない（テンプレートのタグは `af-managed-by` だけ）。エンジンの service と
  provider は既に `af-pool` / `af-role=engine-<役>` を持つ。
- **R4. 決定 4 は書かれていない中断処理を「継承」していた。** 0075 決定 6 はコードには
  `noteReplacement`（ログ 1 行と監査 1 行）しか無く、先頭からの立て直しと 2 回連続の飛ばしは 0075 の
  P1 で着手されていない——`engine_offer.go` の冒頭がそう言う。決定は「設計を継承し、P2 で 1 回だけ
  作る」に改めた。
- **R5. `isPoolContainerInstance` の新しい規則が provider の判定を落としていた。** 「A から B へ変える」
  では MI の箱（属性無し）がプールに戻り、同じ段落の「同じ 1 つの関数で外れる」と 3 種のテストに
  矛盾する。規則は「A かつ B」にした。同じ段落に足したこと: エンジンの deregister は
  `deregisterSlot`（プールの判定を当てる）を通さない。決定 5 の走査は両方向——タスクの無い EC2 の箱と、
  EC2 の無い container instance——で走る（プールの `sweepGhostInstances` はエンジンのゴーストをもう
  見ない）。「タスク 0 という `sweepGhostInstances` の慎重さ」も誤りで、あの走査が見るのは登録の古さと
  「インスタンスが消えた」であり、タスク数の慎重さは `sweepFreeSlots` / `makeRoom` のもの。
- **R6. 両エンジン service は `ServiceName` を明示している。** 未解決 1 が「置き換え」なら CloudFormation
  は消す前に作るので名前が衝突し、更新はロールバックする。そのときの移行は既にある `<役>Enabled=false`
  → 当てる → `true` の往復（条件が service を消す——`update.sh` がまさにこれを警告している）。未解決 1 の
  使い捨ては名前と `ServiceRegistries` を持たせないと、別の service を測ることになる。
- **R7. 「`update.sh` はパラメータを渡さない」は誤り。** 60-engines には稼働中のスタックから読み戻した
  `<役>Enabled=true` を渡す——あの値を直せる唯一の経路。ADR が頼った性質（`cloudformation deploy` は
  名指ししないパラメータを保つ）は正しく、文が違った。`af_param_drop` の定義は `env.sh`。
- **R8. 外す一覧が glob で、残すべきものを捕まえていた。** `*Mem*` は `*TaskMemory` に当たり、MI の
  ブロックの隣の `*GpuCount` はタスク定義に流れる。一覧はパラメータを名指しにした。`*StorageGiB` は
  残して root ボリュームのサイズになる——匿名のモデルボリュームはルートに落ち、既定の root 30 GiB (c)
  では comfy の配備のモデルが入らない。`*CapacityProviderName` の Output 3 本は provider と一緒に消え、
  harness スクリプト 6 本がそれを読む。`teardown.sh` は MI のドレインが箱を消すのに任せていたので、
  スロットと同じくタグで terminate する。
- **R9. CP の `main` パッケージに EC2 クライアントが無い。** エンジンのポートは ECS 専用
  （`engineECSAPI`・`engineCapacityAPI`）で、唯一の EC2 ポートは `internal/runtime` の非公開、ecs-ec2 の
  ランタイムしか組み立てない。P1 で fake つきの `engineFleetAPI` を全フレーバーに足す。契約 A には
  スキーマも golden も無く、テスト 3 本の JSON リテラルが事実上の固定。
- **R10. `prioritized` が読むのは override の `Priority` であって並び順ではない。** 決定 1 は「並び順＝
  宣言順」と書いていた。CP は `Priority` を明示して書く（完了 6）。
- **R11. 「instant fleet は自分で消える」は (c) で、決定 1 はそれに寄りかかっていた**（「CP は fleet の
  id を覚えない」）。未解決 3 で $0 の呼び出しの後に `describe-fleets` を記録し、残るなら応答ごとに
  `DeleteFleets` を打つ。
- **R12. service-linked role のサービス名。** `AWSServiceRoleForEC2Fleet` を作るのは
  `ec2fleet.amazonaws.com` であって `spot.amazonaws.com` ではなく、条件キーは `iam:AWSServiceName`。
  いまの `standup.sh` は `ecs.amazonaws.com` にしか `create-service-linked-role` を打っていない。
  CloudFormation の資源にはしない——既にあるロールはスタックを落とす。

### 決定ごとの改訂（本文に反映済み）

| 決定 | 変えたこと | 理由 |
|---|---|---|
| 背景（コード） | スロットのタグは CP が付ける／`sweepSlotOwnerTags`／3 つの Sid と `SsmWorkspaceParams` の実際／MI の権限は `CpIngestPolicy`／`main` に EC2 クライアント無し／0075 決定 6 は `noteReplacement` だけ／`startGate` の 4 つの門／契約 A の在処と固定／`teardown.sh` | R1・R2・R3・R4・R7・R8・R9 |
| 1 | `Priority` を明示して書く／fleet が残るかを測り、残るなら `DeleteFleets` | R10・R11 |
| 3 | provider の判定を残す（「A かつ B」）／エンジンの deregister は `deregisterSlot` を通さない／`sweepSlotOwnerTags` に役のフィルタ | R3・R5 |
| 4 | 「設計として継承。P2 で作る」／ECS 自身の deregister を (c) とし、後ろに走査 | R4・R5 |
| 5 | 走査は両方向、タスク数は `DescribeContainerInstances` から、記憶は見ない | R5 |
| 8 | 「管理料込み」の 2 か所を `usdPerHour` と一緒に変える | — |
| 10 | 2 つの Sid の繰り返しではなく `CpIngestPolicy`／`ssm:GetParameters` は未解決 3 次第／`ec2fleet.amazonaws.com`／IAM の最小集合は P0 の成果物／CFN 資源ではなく `standup.sh` | R1・R2・R12 |
| 11 | パラメータを名指し、`*StorageGiB` の意味変更／Output と harness／`ServiceName` の明示と `<役>Enabled` の往復／`update.sh` の実際／`teardown.sh`／50-tts が `Associations` に依る件 | R6・R7・R8 |
| 未解決 | 1: 使い捨てに名前と registry／3: IAM の最小集合・fleet の残留・`Priority` | R2・R6・R10・R11 |
| P1 | `engineFleetAPI` を全フレーバーに／関数名を正確に／完了 5・6 | R3・R9・R10 |

### 実装の分け方

契約 B（CP → Console）は動かないので Console のレーンは無い。契約 A（テンプレート → CP）は 1 欄だけ
変わり（`capacityProvider` / `spotCapacityProvider` → `launchTemplate`）、それが CP と CFN のレーンの
継ぎ目になる。レーンは 4 本:

- **P0 実機（$0。最初に、単独で）。** af-sandbox で使い捨てスタックと読み取り API による未解決 1〜4。
  **1 か 2 が赤なら他のレーンは始めない。** 成果物は 0075 実機 0 の形の判定 4 件と、未解決 3 から
  出る IAM の最小集合と fleet 残留の答え。`claude` のセッションが駆動する。
- **CP（Go）。** P1 の CP 項目の全部を fake の `engineFleetAPI` の裏で。完了 1〜6。P0 の 2 と 3 が緑に
  なった日に fake を頼りに始めてよく、失敗コードの表の語彙は P0 から届いた時点で取り込む。
- **CFN とスクリプト。** P1 の CFN 項目の全部、PARAMETERS、`standup.sh` / `update.sh` / `teardown.sh` /
  harness スクリプト、そして 0071・0074・0075 への補遺（「追記 — ADR 0077 がこの決定を覆した」）。
  P0 の 1 が緑になった日に始めてよい（移行の形がそれに依る）。
- **P1 実機。** 直列・1 レーン 1 配備、CP と CFN が入ってから: 完了 7。開発配備は共有なので、60-engines を
  触る前に `pgrep -af dev-deploy.sh`。

P2（決定 4・6）と P3（llm 役）は P1 の実機の判定を待ち、ここでは分けない。

## 追記 — P1 の CFN レーンが本文に返したもの（2026-09-12・PR #575）

CFN レーンは決定 11 を書かれたとおりに実装した（テンプレート・スクリプト・PARAMETERS・
0071 / 0074 / 0075 への補遺）。配備はしていないし、何も測っていない。AWS への呼び出しは
`validate-template` の 1 回だけで、それは読み取りである。実装が本文に返したものは 4 つ。
**上の決定は 1 つも書き換えていない**——この節が記録で、形は 0075 の追記に倣った。

1. 🔴 **`<Role>UseLocalStorage` を退役させると実測の cold start を失う。決定 11 はそれを言っていない。**
   削除一覧は 8 つを「梯子の写し」と呼び、7 つはそのとおりである。これだけは違う——EBS のデータ
   ボリュームではなく**インスタンスストア**を要求する唯一の口で、入れた効果は実測済みだった
   （2026-09-09・ADR 0071 の数字: S3 → ディスク 1.4 倍、ディスク → VRAM **2.9 倍**、
   RunTask → モデル読込 527-586 秒 → **275 秒**、モデルの入れ替え 276-282 秒 → **98.5 秒**）。
   このレーンが書いた launch template は NVMe を**マウントしない**（未解決 8 は P1）ので、
   起動の両半分が EBS 帯域に戻る。パラメータ自体は外すしかない——訊く相手の MI provider が無い——
   が、対価は実在し、PARAMETERS の `LlmStorageGiB` に記録した。
   **未解決 8 は「やれたらやる」ではなく、閉じるべき退行である。**
2. **決定 10 の `role/af-*-engine` への `iam:PassRole` は、テンプレートには要らない約束事だった。**
   20-platform がスロットのロールを名前で書くのはスタックの循環参照を避けるためで、エンジンの
   インスタンスロールは 60-engines 自身が作るので `!GetAtt EngineInstanceRole.Arn` で書いた——
   同じ形のまま、1 資源ぶん狭い。（ロール名は `af-<stack>-engine` のままなので、約束事でも当たる。
   `EngineTaskRole` は `af-<stack>-engine-task` なので当たらない。）
3. **`teardown.sh` は既にエンジンの箱を terminate していた。足りなかったのは言葉のほう。**
   決定 11 は「スロットと同じ形で `af-pool` ＋ `af-role=engine-*` のタグで terminate する段」を
   求めている。`list_slots` は `af-pool` **単独**で絞る——レビュー R3 が `sweepSlotOwnerTags` の
   バグとして挙げたのと同じ「1 フィルタ」の形——ので、CP が買った箱は段 3 が terminate する一覧に
   もう入っている。見えなかったのは MI の箱だけ（AWS 管理アカウント・列挙不可）で、だから段が
   「スロット」と書かれていた。レーンがやったのは段の名前と数え方を実態に合わせ、エンジンの箱を
   別に数えて理由を書いたことで、2 度目の terminate は足していない。
4. **launch template は無条件に作る**（capacity provider がそうだったように）。決定 11 は何も
   言っておらず、`<Role>Enabled` を条件にするのが素直に見える——が `check-cfn-exports.py` は
   空文字を export しうるテンプレートを落とすし、`*LaunchTemplateId` の 2 本は export である。
   起動しないあいだ launch template は 1 円もかからない。

もう 1 つ、後から探す人のために書いておく。**harness 6 本は両方やった。** 決定 11 は
「新しい名前へ移す**か**、MI 時代のものと明記する」と選ばせているが、機械的に移した
（タスク定義を `EC2`、`run-task` を launch type ＋ `attribute:af-role == engine-<役>`）**うえで**
冒頭に `exit 2` の門を置いた。移設は $0 では検証できず、各スクリプトの説明にある数字はすべて
Managed Instances 時代の実測だからである。門を消すのは「測り直す」の一部であって、「読む」の
一部ではない。
