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
  ⚠️ `prioritized` が読むのは**各 override の `Priority` 欄**で、並び順ではない——CP は宣言順に
  `Priority` 1, 2, … を書く。優先度が宣言順に並ぶことをテストで固定する。**実測（P0 未解決 3）**: 優先度は
  尊重され、fleet の設定に記録される。
- overrides は「型 × 私有サブネット」の直積。AZ を CP が選ぶ必要は無い（スロットの `spreadAZs` は
  home ボリュームの AZ 拘束のためにあり、エンジンには home が無い）。
- 🔴 **応答は同期である。** 起動した箱があればその `InstanceId` が返り、無ければ `Errors[]` に
  `ErrorCode` が並ぶ。**0075 の予算の時計・サービスイベントの文字列照合・deployment の門は、この 1 点で
  全部消える。** 次の提案へ移るのは「応答に箱が無かった」ときで、待つものは何も無い。
- 予算（`<役>OfferBudgetSec`）は意味が縮む——**箱が ECS に登録されるまでの上限**（スロットの
  `waitSlotRegistered`・3 秒ポーリングと同じ）だけになる。既定は 300 秒（0045 決定 22 の実測: 起動→ECS
  登録 21 秒、自前 AMI で 77 秒。10 倍強）。超えたら箱を terminate して次の提案へ。
- instant fleet は箱が消えると自動で削除される (c)。CP は fleet の id を**覚えない**。覚えるのは
  `InstanceId` と、箱に付けたタグである（決定 3）。**実測（P0 未解決 3）**: instant fleet は残るが、id で名指し
  しない限り `describe-fleets` には出ず、`DeleteFleets(TerminateInstances=false)` は instant fleet には拒まれる
  （`NoTerminateInstancesNotSupported`）。よって**後追いの呼び出しは無い**: ポートは `CreateFleet` /
  `DescribeInstances` / `TerminateInstances` の 3 つで、CP が読むどの一覧にも溜まらない。

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
  `sweepFreeSlots`・`makeRoom`・`PoolStatus`）。**`sweepSlotOwnerTags` には欠けている役のフィルタを足す**
  （背景）——`af-role` が `{slot, quarantined}` で、`slot` だけではない: 隔離された箱は停止しているだけで
  解放されておらず、人の `af-membership` を持ったままのことがあり、それを直すのがこの走査の存在理由
  （CP レーンの発見・#577）。テストの陽性対照は、フィルタを外すと走査がエンジンのタグの箱に触ること。
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
  `sweepFreeSlots` / `makeRoom` が `slotTaskCounts` を読むのと同じ）、猶予より古く、その役に進行中の
  起動が無いもの——terminate する。CP は自前の `ghostAfter` を持たない（あれはスロットプールの設定で別
  パッケージ）ので、猶予は **desired 0 で 2 分**（通常の退場。タスクが消えた次の tick）と **service が上がって
  いるあいだは 15 分**（0075 の「箱 2 台」の漏れを、置かれる途中のタスクに触らずに閉じる）——CP レーンの形・#577。(b) エンジンの属性を持つ container instance で EC2 の
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
  `Errors[].ErrorCode`** へ移る——HTTP 200 の中に override ごとに 1 つ（実測・P0 未解決 3）:
  `MaxSpotInstanceCountExceeded` / `VcpuLimitExceeded` → その購入形態を飛ばす。`InvalidFleetConfiguration`
  → **全 override がそう言ったときだけ** `unusable`（綴り違いと「この AZ では提供されていない」は同じ文なので、
  1 つの override が言うだけなら綴りではなく在庫の事実）。`SpotMaxPriceTooLow` / `InsufficientInstanceCapacity`
  → 次へ。未知 → 次へ＋ログ。🔴 **そしてサービスイベントの表に無かった分類: 認可の失敗は「在庫無し」と
  同じ形で来る**——`UnauthorizedOperation` は同じ配列に override ごとに来る（`iam:PassRole` を外して実測）し、
  `SsmAccessDenied` は top-level に来る。どちらも**一周を止め**、失敗に数え、文をそのままログに出す。「次へ」
  扱いにすると、設定を誤った配備は一覧を全部歩いて「どこにも在庫が無い」と報告する。`--dry-run` は override
  ごとの認可を検査しないので事前確認にならない。**「イベントを提案で絞る」（#564）は要らない**——応答は
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
| `ec2:CreateFleet`（`Resource: *`。Fleet は資源に限定できない）。`ec2:DescribeFleets` / `ec2:DeleteFleets` は**呼ばない**（決定 1・実測）。#575 は付けているが無害 | `ecs:DescribeCapacityProviders` / `ecs:UpdateCapacityProvider` |
| `ec2:CreateLaunchTemplateVersion` は**足さない**（CFN が所有） | `ecs:PutClusterCapacityProviders` |
| `iam:PassRole` を `role/af-*-engine`（`iam:PassedToService: ec2.amazonaws.com`）。`PassSlotRole` と同じ形の独立した文で | `iam:PassRole` の `InfraRole` / `InstanceRole` |
| `iam:CreateServiceLinkedRole` を `iam:AWSServiceName` が `[spot.amazonaws.com, ec2fleet.amazonaws.com]` に限定（後者が `AWSServiceRoleForEC2Fleet` を作る） | `InfraRole` そのもの |
| `ssm:GetParameters` の `arn:aws:ssm:<region>::parameter/aws/service/ecs/optimized-ami/*` を**無条件で**——実測（P0 未解決 3）: 無いと `CreateFleet` が top-level の `SsmAccessDenied` で落ち、操作名もパラメータ名も言わない。CP が持つ `ssm:GetParameter` は `/af-ws/*` 限定で、これは読めない | |
| `ec2:CreateTags` は箱のタグには**要らない**: `CreateFleet` の `TagSpecifications` が launch template のタグと併合する（実測）。`ec2:DescribeLaunchTemplates` / `Versions` も要らない | |

`ec2:RunInstances` / `TerminateInstances` / `DescribeInstances` / `CreateTags` は `Ec2SlotPool` に既にある。
`ecs:ListContainerInstances` / `DescribeContainerInstances` / `DeregisterContainerInstance` も
`EcsContainerInstances` に既にある。**どちらの Sid も `20-platform.yaml` で無条件で、全フレーバーが
持つ**（背景）ので、60-engines は繰り返さない。60-engines が触るのは自分の `CpIngestPolicy` で、MI の
文が出て Fleet の文が入る。権限の全集合は**未解決 3 の成果物**である: $0 の呼び出しをこの表で始め、
`AccessDenied` が名指したものを足して記録する（EC2 Fleet の公開例のポリシーは `ec2:*` で、最小を
何も言わない）。

**`AWSServiceRoleForEC2Fleet` は安い保険であって、既知の失敗ではない。** 公開仕様の「無いと最初の
`CreateFleet` が失敗する」は**確かめられなかった**（P0 未解決 4）: ロールが無いうちに起動しない呼び出しが
3 回通り、`AWSServiceRoleForEC2Spot` は既にあり、起動した 1 回はロールを作った後だった——「どちらの
ロールも無い口座で起動する呼び出し」は未測。配備手順（`standup.sh`）ではそれでも
`create-service-linked-role --aws-service-name ec2fleet.amazonaws.com` を `|| true` で 1 回打つ
（`AWSServiceRoleForEC2Spot` を 0074 で手で作ったのと同じ列。この ADR の前の `standup.sh` は
`ecs.amazonaws.com` にしか打っていなかった）。CloudFormation の `AWS::IAM::ServiceLinkedRole` にはしない——
既にあるロールはスタックを落とす。af-sandbox にはいまはある（P0 が作って残した）。

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
  実測: `delete-capacity-provider` は箱 0 で即 `INACTIVE`）。**実測（P0 未解決 1）: service の
  `CapacityProviderStrategy` → `LaunchType` は CFN で置き換えになる。** 両 service は `ServiceName` を
  明示している（`af-<stack>-llm` / `-image`）ので、CloudFormation は古い service を消す前に新しい service を
  作ろうとして名前が衝突し（`AlreadyExists`）、更新はロールバックして稼働中の service は 1 バイトも
  変わらない。**よって移行は往復であり、往復だけである**: `<役>Enabled=false`（条件が service を消す。
  `update.sh` がまさにこれを警告している）→ 新テンプレートを当てる → `<役>Enabled=true`——同じ形の使い捨てで
  25 秒 + 48 秒。あいだ Cloud Map の名前は**消えない**（`AWS::ServiceDiscovery::Service` は条件の無い別資源）。
  消えるのはそこへ登録する ECS service だけで、desired 0 なら登録するものも無い。service の
  `DependsOn: Associations` はそのまま（`Associations` は残る）。
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

🔁 **反証されたら変える条件**: 成立した——未解決 1 は「再作成」と言い、上の往復が移行になった。ECS の
service は 1 分ほど消える。TTS と違って待つ利用者はいない（image は再送、llm は会話が切れる）ので、
両役 `mode: off` の窓で行う。

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

### P0 の実測をこのレーンに反映した（2026-09-12・PR #576 の後）

P0 の判定がこのレーンの PR が開いているあいだに出たので、4 点を CFN 側に取り込んだ。
ブランチに積んだもの:

1. **移行手順は `<役>Enabled` の往復だけになった**（未解決 1 が 🔴）。PARAMETERS には
   「答えが出たら片方を消す」と書いて 2 案を並べていたが、その場更新の案を削除し、往復の
   実測（25 秒 + 48 秒）を残し、逆順で試したときに気づけるよう `AlreadyExists` の文面を引用した。
   ✅ **往復中も Cloud Map の名前は消えない**——`AWS::ServiceDiscovery::Service` は条件の付かない
   別資源で、実測でも同じ registry ARN のままだった。決定 11 の「その間 Cloud Map の名前は消える」が
   P0 の訂正した唯一の文で、運用者が読む場所（PARAMETERS）にそう書いた。
2. **`ssm:GetParameters`（`arn:aws:ssm:<region>::parameter/aws/service/ecs/optimized-ami/*`）を
   `CpIngestPolicy` に無条件で入れた。** 決定 10 の条件付きの行に答えが出た——`resolve:ssm:` を
   解決するのは `CreateFleet` の**呼び出し側**で、無いと top-level の `SsmAccessDenied` で落ち、
   アクション名もパラメータ名も出ない。P0 が「要らない」と示した 2 つ——
   `ec2:DescribeLaunchTemplates(Versions)` と `ec2:CreateTags`（要求側の `TagSpecifications` は
   テンプレートのタグと併合される）——はもともと足しておらず、「足さないこと」として記録した。
3. **SLR の主張は消さずに弱めた。** 決定 10 の ⚠️ は「`AWSServiceRoleForEC2Fleet` が無いと最初の
   `CreateFleet` が失敗する」と書いていたが、P0 は無い状態で 3 回通した。`standup.sh` では作り
   続ける——安い保険であり、「SLR が両方とも無いアカウントで**実際に起動する**呼び出し」は
   測れていない——が、テンプレートのコメント・`standup.sh`・README の前提・PARAMETERS から
   「失敗する」という断定を外した。2 回目の作成が返すのは `EntityAlreadyExists` ではなく
   `InvalidInput` である（＝コード一致では扱えず `|| true` が要る）ことも `standup.sh` に書いた。
4. **未解決 8 を実装した（未検証）。** 上の追記 1 で「閉じるべき退行」と書いたものを、P1 の
   報告に回さず launch template で閉じた: user data が最初のインスタンスストア NVMe を mount し、
   **Docker の data-root** をそこへ置く。匿名の `host` ボリュームは Docker のボリュームそのものなので
   モデルもそこへ落ちる——タスク定義は 1 行も変えない。EBS のみの型は何にも当たらず root のまま、
   AMI が持つ agent と pause の像は先に複写し、各段は `&&` で繋いであるので失敗しても Docker は
   元の場所に残る。🔴 **未計測**: P1 の初回は箱の上で `df /var/lib/docker` と
   `docker info | grep "Docker Root Dir"` を見ること。ここが黙って失敗したときの症状は
   「起動が遅い」だけである。

反映後のテンプレートのサイズ: **40,182 バイト**（最初の push では 36,816・壁は 51,200）。

### CP レーン（PR #577）からの 1 点——予算の意味と既定値

契約 A でこの ADR が動かす欄は `launchTemplate` のほかに `<役>OfferBudgetSec` だけで、CP レーンが
意味を確定させた: これは**買えた箱が ECS に登録されるまでの上限**であって、提案ごとの購入の時計では
ない（購入は呼び出しの中で答える）。CP の既定は決定 1 の数字である **300 秒**。よってテンプレートの
`Default` を両役とも 300 にし、`Description` の 2 行を新しい意味に書き換えた。

🔴 **0.19.0 の捕捉は `180` を持っている。** これは旧い意味の既定値で、放っておくと新しい上限が
ADR の選んだ数字より短くなる——しかも黙って。`standup.sh` は **180 ちょうど**だけを落として
テンプレートの 300 を効かせ、落としたことを stdout に出す。それ以外の値は運用者の選択として
そのまま渡す。古い既定値と意図した 180 は区別できない——だから規則が「旧い既定値だけ、他は触らない」
なのである。スタブ試験は両方向を、それぞれの陽性対照つきで固定した。

## 追記 — P0 未解決 1〜4 の実測（2026-09-12・af-sandbox・$0.01 未満）

$0 の前提 4 件を、コードを 1 行も書く前に、**使い捨ての ECS クラスタと Cloud Map namespace を自前で
持つ**使い捨て CloudFormation スタックの上で測った。この実測が打ったコマンドのうち、本物のスタック・
本物のクラスタ・本物の service・本物の capacity provider を名指したものは 1 つも無い。書き込みは
すべて `af-adr0077-p0-` で始まる資源が対象。**GPU は 1 台も買っていない。** 所要
**16 分 45 秒**（11:50:02〜12:06:47 JST）。

非 GPU の箱は 1 台ではなく 2 台上げた。未解決 2 の `t3.small`（3 分 6 秒）と、意図して足した
**成功する `CreateFleet`** が候補 IAM 集合の下で買った `t3.small` 1 台（1 分 6 秒）である。2 台目を
上げた理由は、そうしないと IAM 集合が「何も起動しない呼び出し」に対してしか検証されないからで、
下の実測が示すとおり `iam:PassRole` が**検査されないのはまさにその経路**だった。合計で約 4.2
インスタンス分、未解決 2 に認められた $0.05 の枠の内側。

**判定: 1 は 🔴 赤・2 は 🟢 緑・3 は緑だが決定 1・8・10 に 🔴 の訂正が 3 件・4 は 🟢 緑。**
未解決 1 が赤でも骨格は書き直さない——決定 11 の移行が、本文がすでに ⚠️ の分岐として持っている
`<役>Enabled` の往復に確定するだけである。他のレーンが待っていたのは未解決 2 で、それが緑なので
**CP と CFN のレーンは始めてよい。**

| # | 判定 | 一行で |
|---|---|---|
| 1 | 🔴 | CloudFormation は service を**置き換え**、明示した `ServiceName` が衝突する——`AlreadyExists`・ロールバック。`<役>Enabled=false` → 当てる → `true` の往復は通る（25 秒 + 48 秒） |
| 2 | 🟢 | `ECS_INSTANCE_ATTRIBUTES` で付けた属性に EC2 launch type の service の placement constraint が効く。desired 1 の 28 秒後に RUNNING。陽性対照: 合わない属性を要求した側は 0 のまま「MemberOf placement constraint unsatisfied」 |
| 3 | 🟢/🔴 | 語彙は **200** の応答の中に override ごとに 5 種——`UnauthorizedOperation` を含む。`resolve:ssm:` は呼び手の `ssm:GetParameters` を**要る**。fleet は**残る**うえ、instant fleet に `DeleteFleets(TerminateInstances=false)` は拒まれる。`Priority` は効き、記録される |
| 4 | 🟢 | 条件付きの `iam:CreateServiceLinkedRole` で `AWSServiceRoleForEC2Fleet` は作れる。ただし `CreateFleet` は**それが無くても通った**——役が存在する前に 3 回通っている |

### 未解決 1 — service は置き換えられ、名前が衝突する

使い捨ての service は本物と同じ形にした。`ServiceName` を明示（`af-adr0077-p0-svc`）、使い捨ての
Cloud Map の A レコード service を指す `ServiceRegistries`、`awsvpc`、`MinimumHealthyPercent: 0`、
`DesiredCount: 0`。2 つの形はテンプレートのパラメータと `!If` で切り替えており、資源の差分としては
テンプレートを書き換えたのと同じものになる。

| 時刻（JST） | やったこと | 返ってきたもの |
|---|---|---|
| 11:50:02 | `create-stack`・`Mode=capacity-provider` | 11:51:29 に `CREATE_COMPLETE`。`capacityProviderStrategy: [{FARGATE, weight 1, base 0}]`・`launchType: null`・`placementConstraints: []`・PRIMARY `ecs-svc/3919890012809245997` |
| 11:51:51 | `LaunchType: EC2` ＋ `PlacementConstraints` への `create-change-set` | `Replacement: Conditional`。プロパティ別には `CapacityProviderStrategy` が `RequiresRecreation: Never`・**`LaunchType` が `Conditionally`**・`PlacementConstraints` が `Never` |
| 11:52:20 | **`execute-change-set`**（0074 の教訓: change set は答えではない） | 11:52:26 `Service UPDATE_IN_PROGRESS`——**「Requested update requires the creation of a new physical resource; hence creating one.」** 11:52:27 `UPDATE_FAILED`（全文は下）。11:52:32 `UPDATE_ROLLBACK_COMPLETE` |
| 11:52:41 | ロールバック後の `describe-services` | `status: ACTIVE`・strategy は FARGATE のまま・`desiredCount` 0・**PRIMARY も同じ `ecs-svc/3919890012809245997`**——元の service は一切動いていない |
| 11:52:55 | `ServiceEnabled=false` で `update-stack`（条件が service を消す） | **25 秒**で `UPDATE_COMPLETE`。`describe-services` は `INACTIVE`・`list-services` は `[]` |
| 11:53:30 | `Mode=launch-type`・`ServiceEnabled=true` で `update-stack` | **48 秒**で `UPDATE_COMPLETE`。`launchType: EC2`・`capacityProviderStrategy: null`・`placementConstraints: [{memberOf, "attribute:af-role == engine-image"}]`・**`serviceRegistries` の ARN は同じ `srv-5grs42msyxunj3f5`** |

```
Service UPDATE_FAILED
Resource handler returned message: "Resource of type 'AWS::ECS::Service' with identifier
'af-adr0077-p0-svc' already exists." (HandlerErrorCode: AlreadyExists)
```

**判定 🔴。** 置き換えであり、CloudFormation は消す前に作り、明示した名前がレビュー R6 の予想どおり
衝突する。したがって決定 11 の ⚠️ の分岐は**代替路ではなく唯一の移行経路**である:
`<役>Enabled=false` → 新テンプレートを当てる → `true`。

本文への訂正が 1 つ。この往復で **Cloud Map の名前は消えない**。`AWS::ServiceDiscovery::Service` は
条件の付いていない別資源なので、DNS 名も ARN も残る。その数十秒のあいだ消えるのは「ECS service が
そこにインスタンスを登録している」ことだけで、desired 0 なら登録は元より無い。決定 11 の
「あいだ Cloud Map の名前は消える。desired 0 なので要求は失われない」は安全側だが言い過ぎである。

**影響——依存: 決定 11。** 決定 11 の 🔁 が発火した。結果は決定 11 自身の本文がすでに書いている。
それ以外は動かない。失敗した更新は生きている service を 1 欄も変えなかったので、順序を間違えても
戻れる。

### 未解決 2 — 属性は効く

使い捨ての launch template 1 本（`ImageId` は
`resolve:ssm:/aws/service/ecs/optimized-ami/amazon-linux-2023/recommended/image_id`——
`ami-0f57b5b58b68248a3` に解決された・`HttpTokens: required`・user data が `/etc/ecs/ecs.config` に
`ECS_CLUSTER` と `ECS_INSTANCE_ATTRIBUTES={"af-role":"engine-image"}` を書く）、`t3.small` 1 台、
そしてスタックがすでに持っているタスク定義（`awsvpc`・256/512・GPU 要求無し）。

| 時刻（JST） | やったこと | 返ってきたもの |
|---|---|---|
| 11:54:56 | `run-instances`・`t3.small` 1 台・1a の private subnet | `i-05375bcf612c7ec74`・`pending` |
| 11:55:20 | `describe-container-instances` | 起動の **24 秒後**に登録。`agentConnected: true`・属性に **`{"name": "af-role", "value": "engine-image"}`**・そして **`capacityProviderName` は無い** |
| 11:55:45 | **陽性対照**: 同じタスク定義で `attribute:af-role == engine-llm` を要求する EC2 launch type の service をもう 1 本・desired 1 | 11:56:00 に `runningCount` 0・`pendingCount` 0。イベントは *"was unable to place a task because no container instance met all of its requirements. The closest matching (container-instance fb2ddff7…) encountered error `"MemberOf placement constraint unsatisfied."`"* |
| 11:55:47 | 測る側の service（`== engine-image`）を desired 1 に | 11:55:53「has started 1 tasks」・**11:56:15 に `RUNNING`（呼び出しの 28 秒後）**。`launchType: EC2`・`attachments: [(ElasticNetworkInterface, ATTACHED)]` |
| 11:56:57 | desired 0 のあと `terminate-instances` | 11:58:02 に EC2 が `terminated`。container instance は**約 30 秒で自分から `INACTIVE`** になった——手で deregister はしていない |

**判定 🟢。** AL2023 の ECS AMI 上でエージェントは `ECS_INSTANCE_ATTRIBUTES` を尊重し、その属性を読む
*service* の placement constraint が EC2 launch type で効く。陽性対照は、制約が無視されているのでは
なく本当に評価されていることを示す——同じ箱・同じタスク定義で、式を 1 語変えるとタスクは置かれない。

**影響——依存: 決定 2 と 3。** どちらも本文どおりで立つ。決定 2 の 🔁（タスク定義に `ec2InstanceId` を
書き、起動ごとに revision を切る）は**不要**。決定 3 の 🔁（登録直後の `PutAttributes`）も**不要**。
決定 3 の前提 2 つが副産物として確認された。CP が買った EC2 の箱は **`capacityProviderName` を
持たない**（＝背景の言うとおり `isPoolContainerInstance` は崩れ、属性の節が要る）ことと、EC2 launch
type の `awsvpc` はエージェントの設定を何も足さずにタスク ENI を付けること。決定 4 の 1 項目も裏付けを
得た。ECS は**自分で**終了したインスタンスの container instance を deregister する——ここでは
`t3.small` で約 30 秒。本文ではこれは (c) だった。

### 未解決 3 — `CreateFleet` の語彙・IAM の最小集合・残る fleet・`Priority`

以下の呼び出しはすべて、使い捨ての IAM ロール（`af-adr0077-p0-fleet`。信頼はこのセッション自身の
プリンシパル）として実行した。決定 10 の表をそのまま権限に持たせ、`AccessDenied` が「表に足りない
もの」を名指すようにしてある。探りの 4 呼び出しはどれも 1 台も起動していない。

| 時刻（JST） | 呼び出し | `Errors[].ErrorCode` / 結果 |
|---|---|---|
| 11:58:49 | Spot `g6.4xlarge` × subnet 2 つ・`price-capacity-optimized`・決定 10 の表そのまま | **`Errors[]` すら無い最上位の失敗**: `SsmAccessDenied`・*「Access denied to SSM」* |
| 11:59:16 | `parameter/aws/service/ecs/optimized-ami/*` への `ssm:GetParameters` を足して同じ呼び出し | **HTTP 200**・`Instances: []`・override ごとに 1 件ずつ **`MaxSpotInstanceCountExceeded`** / *「Max spot instance count exceeded」*・`Lifecycle: spot`（この口座の G/VT Spot クォータは 8 vCPU・`g6.4xlarge` は 16） |
| 12:00:54 | Spot `g6.xxlarge`（綴り違い） | **`InvalidFleetConfiguration`** / *「Your requested instance type (g6.xxlarge) is not supported in your requested Availability Zone (ap-northeast-1a).」* |
| 12:00:57 | Spot `g6.xlarge` × subnet 2 つ・`MaxPrice: "0.001"` | **`SpotMaxPriceTooLow`** / *「Your Spot request price of 0.001 is lower than the minimum required Spot request fulfillment price of 0.5762.」*（もう一方の AZ は 0.5633） |
| 12:01:03 | On-demand `g6.48xlarge`（Priority 1）と `g6.24xlarge`（Priority 2）・`OnDemandOptions.AllocationStrategy: prioritized` | **`VcpuLimitExceeded`** / *「You have requested more vCPU capacity than your current vCPU limit of 8 allows for the instance bucket…」*・`Lifecycle: on-demand`。各エラーは override を **`Priority: 1.0` / `2.0` 込みで**反響する |
| 12:03:34 | On-demand `t3.small`（Priority 1）/ 別 subnet の `t3.small`（Priority 2）・要求側の `TagSpecifications` 付き | **`Errors: []`**・箱はちょうど 1 台・取られたのは **Priority 1** の override: `i-05bfc8ee328b5a232`・`Lifecycle: on-demand` |

🔴 **このうち 3 つが本文に効く。**

1. **`UnauthorizedOperation` は 200 の `Errors[]` の中に override ごとに来る。** ロールから
   `iam:PassRole` を外して同じ `SpotMaxPriceTooLow` の呼び出しをすると、200 で
   `ErrorCode: UnauthorizedOperation`・*「is not authorized to perform: iam:PassRole on resource:
   …/af-adr0077-p0-instance」* が返る。**IAM の穴は「在庫が無い」と寸分違わぬ形をしている。**
   決定 8 の失敗コードの表は `UnauthorizedOperation`（および最上位で来る `SsmAccessDenied`）を
   **周回を止める硬い失敗**に分類しなければならない。「次の提案へ」にしてはいけない——さもないと
   設定を誤った配備が黙って提案リストを最後まで歩き、「どこにも在庫が無い」と報告する。
2. **`--dry-run` はこれを捕まえられない。** `iam:PassRole` を外したまま同じ呼び出しに `--dry-run` を
   付けると `DryRunOperation`——*「Request would have succeeded, but DryRun flag is set.」*が返る。
   dry run は override ごとの認可を通らない。事前検査には使えない。
3. **綴り違いと「その AZ ではその型が提供されていない」は同じコード・同じ文である。** 決定 8 の
   「綴りを間違えた型は `CreateFleet` が**その場で**拒む」は真だが、応答は「綴り違い」とは言わない。
   `<役>Offers` の打ち間違いは在庫の事実として読まれ、次の需要でも永遠に再試行される。コードを
   読むより、テンプレート側で型の綴りを検査するほうが価値がある。

⚠️ **測れなかったもの**: 本物の在庫切れの拒否。`SpotMaxPriceTooLow` はその代役の価格拒否であって、
本物のコード（`InsufficientInstanceCapacity` か EC2 Fleet が何と呼ぶか）はいまだ不明である。それを
引き出すには「届くかもしれない容量」を要求するしかないため。

#### (a) IAM の最小集合

決定 10 の表から始め、拒否が名指したものだけを足した。足す必要があったのは **1 つだけ**で、表に
すでにあった 1 つは苦い形で必要性が証明された。

| 文（Sid） | アクション | 資源 / 条件 | 根拠 |
|---|---|---|---|
| Fleet | `ec2:CreateFleet`・`ec2:DescribeFleets`・`ec2:DeleteFleets` | `*` | `CreateFleet` と `DescribeFleets` は実行済み。`DeleteFleets` は片付けで実行済み |
| Ec2SlotPool（部分集合。全フレーバーで既に付与済み） | `ec2:RunInstances`・`ec2:TerminateInstances`・`ec2:DescribeInstances`・`ec2:DescribeSubnets`・`ec2:CreateTags` | `*` | `TerminateInstances` と `DescribeInstances` はこのロールで実行済み。`RunInstances` / `DescribeSubnets` / `CreateTags` は**必要だと示せていない**——名指した拒否が無い |
| PassEngineRole | `iam:PassRole` | `role/af-*`・`iam:PassedToService: ec2.amazonaws.com` | **証明済み。** 外すと全 override が `UnauthorizedOperation` になる——ただし**起動するはずだった呼び出しでのみ** |
| SsmPublicAmi | **`ssm:GetParameters`** | `arn:aws:ssm:<region>::parameter/aws/service/ecs/optimized-ami/*` | **証明済み。** 無いと `CreateFleet` は最上位で `SsmAccessDenied` になり、アクションもパラメータも名指さない |
| Slr | `iam:CreateServiceLinkedRole` | `iam:AWSServiceName` が `[spot.amazonaws.com, ec2fleet.amazonaws.com]` | **証明済み**（未解決 4）・陰性対照つき |

**つまり決定 10 の条件付きの行は無条件になる**。公開 AMI パラメータへの `ssm:GetParameters` は
`CreateFleet` の**呼び手**に必要で、(c) の疑いどおりだった。逆に**要らないので足すべきでない**もの
が 2 つ。`ec2:DescribeLaunchTemplates` / `ec2:DescribeLaunchTemplateVersions`（ロールはどちらも
持たず、`CreateFleet` は名前でテンプレートを解決した）と、箱のタグのための `ec2:CreateTags`——(d) 参照。

#### (b) instant fleet は残り、`DeleteFleets(TerminateInstances=false)` は拒まれる

- **1 台も起動しなかった**呼び出しのあとでも、`describe-fleets --fleet-ids <id>` は
  `FleetState: active`・`ActivityStatus: fulfilled`・`FulfilledCapacity: 0.0` で、記録された設定
  一式を付けて返す。
- 成功した呼び出しの箱を terminate したあとも、fleet は **`active` / `fulfilled` /
  `FulfilledCapacity: 1.0`**——もはや嘘の数——のまま、さらに 30 秒見ても変わらなかった。**自分では
  消えない。** 公開仕様 (c) は消えると言うが、この配備では消えない。
- 🔴 `delete-fleets --no-terminate-instances`——決定 1 が名指しているまさにその呼び出し——は拒まれる:
  `NoTerminateInstancesNotSupported`・*「NoTerminateInstances option is not supported for instant
  fleet」*。通るのは `--terminate-instances` だけ（`deleted_terminating` → `deleted`）で、CP に
  とっては「もう失うと決めた箱の fleet にしか安全に打てない」ということになる。
- 🟢 **ただし CP が歩く経路には何も溜まらない。** 絞り込まない `describe-fleets` は終始
  `{"Fleets": []}` を返し、同じ秒に打った id 指定の呼び出しは fleet を返した——その id 指定こそが
  空リストの陽性対照である。instant fleet はそもそも列挙されない。

**よって決定 1 の一文は、書かれている理由とは別の理由で生き残る。**「CP は fleet の id を覚えない」は
正しく、`DeleteFleets` の後追いも要らない——fleet が消えるからではなく、どの列挙からも見えず容量も
持たないからである。`ec2:DeleteFleets` は付けておく価値があるが、本文が約束する
`TerminateInstances=false` の形は存在しない。

#### (c) `prioritized` と `Priority`

🟢 記録され、効く。on-demand の fleet の `describe-fleets` は
`OnDemandOptions.AllocationStrategy: prioritized` と `Overrides[].Priority` の `1.0` / `2.0` を持ち、
エラーも override ごとに `Priority` を反響し、成功した唯一の on-demand 呼び出しは **Priority 1** の
override（1a の `t3.small`）を取り、Priority 2 は手つかずだった。完了 6 はこの形に対して書ける。

#### (d) 呼び出しが返してきた、本文に無い 2 つ（＋1）

- **タグ一式は `CreateFleet` の 1 回で載る。** 要求側の `TagSpecifications`（`ResourceType: instance`）
  は launch template 自身のタグと**併合**される。箱は要求側から `af-pool`・`af-role=engine-image`・
  `af-engine-offer`・`af-engine-buy` を、テンプレートから `af-managed-by=agent-fleet` を持って
  上がってきた。決定 3 のタグ集合に別途 `CreateTags` は要らない——`RunInstances` の時点でタグを付ける
  スロットプールとは違う。
- **EC2 は `aws:ec2:fleet-id` を自分で箱に書く。** つまり CP は記憶ゼロで箱から fleet に到達できる。
  決定 3 が求める 0045 決定 29 の形が、ただで手に入る。
- `InstanceLifecycle` は on-demand の箱では **無い**（Spot では `spot`——0075 の実測）。完了 7 の
  「`InstanceLifecycle`」の確認は、「無い」を「データが取れなかった」ではなく「on-demand」と
  読まなければならない。

**影響——依存: 決定 1・8・10。** 決定 10 は `ssm:GetParameters` を無条件で得て、要らなかった Describe
2 つを落とせる。決定 8 の失敗コードの表は、いま持っていない分類——***`Errors[]` の中の認可失敗***——を
足す必要があり、それを容量として扱ってはならない。決定 1 の
`DeleteFleets(TerminateInstances=false)` の一文は「後追いは要らない。instant fleet は id で名指さない
限り `describe-fleets` から見えない」に書き直す必要がある。**3 つとも決定の前提を覆すものではない。**
本文は親が判断するために書かれたままにしてある。

### 未解決 4 — CP は service-linked role を自分で作れる

| 時刻（JST） | やったこと | 返ってきたもの |
|---|---|---|
| 11:58:49〜12:01:03 | 上の `CreateFleet` 4 回を、**口座に `AWSServiceRoleForEC2Fleet` が無い状態で** | どれも普通に振る舞った（SSM の拒否、次いで override ごとのエラーを持つ 200 が 3 回）。**SLR 由来のエラーは一切無く、役が自動で作られることも無かった** |
| 12:02:46 | 条件付きの権限の下で `create-service-linked-role --aws-service-name ec2fleet.amazonaws.com` | **200。** `AWSServiceRoleForEC2Fleet`・`arn:aws:iam::<account>:role/aws-service-role/ec2fleet.amazonaws.com/AWSServiceRoleForEC2Fleet` |
| 12:02:50 | **陰性対照**: 同じ呼び出しを `--aws-service-name autoscaling.amazonaws.com` で | `AccessDenied`——*「not authorized to perform: iam:CreateServiceLinkedRole on resource: …/AWSServiceRoleForAutoScaling」*。`iam:AWSServiceName` の条件は本当に効いている |
| 12:02:52 | 同じ呼び出しをもう 1 度 | `InvalidInput`——*「Service role name AWSServiceRoleForEC2Fleet has been taken in this account, please try a different suffix.」* |

**問い自体への判定は 🟢。** 限定した `iam:CreateServiceLinkedRole` は通り、条件はそれを限定し続ける。
ただし 2 点。

- ⚠️ **`standup.sh` の `|| true` は必須で、コードは `InvalidInput`** である——`EntityAlreadyExists`
  ではない。コードで照合するスクリプトはこれを認識できない。
- 🔴 **決定 10 の ⚠️「`AWSServiceRoleForEC2Fleet` の無い口座では最初の `CreateFleet` が失敗する」は
  確認されなかった。** 役が存在する前に `CreateFleet` は 3 回通っている。**正直な限界**:
  `AWSServiceRoleForEC2Spot` は既にあり（0074 が作った）、その 3 回はどれも 1 台も起動していない。
  実際に起動した 1 回は SLR ができたあとである。つまり「どちらの SLR も無い口座で*起動する*
  `CreateFleet`」は**測れていない**し、`standup.sh` で役を作るのは引き続き正しい。言えるのは、
  この口座では失敗が API の入口では起きない、ということだけである。

**影響——依存: 決定 10。** `standup.sh` の行はそのまま。その ⚠️ の根拠は本文が主張するより弱く、
「最初の呼び出しが失敗する」ではなく「安い保険」と書き直すのがよい。

### 片付け

この実測で作ったものは全部消し、消えたことを「道具なら見つけられたはずのものが同じ出力に写っている」
形（＝陽性対照が同じ出力の中にある）で確かめた。

- `describe-stacks` → **本物の 7 本だけ**（`af-ecs-network` 〜 `af-ecs-engines`。すべて
  `CREATE_COMPLETE` / `UPDATE_COMPLETE`）。`af-adr0077-p0-oq1` は消えている（12:06:47）。
- `describe-instances --filters Name=tag-key,Values=af-adr0077-p0` を**状態で絞らずに** → 探りの箱
  2 台がどちらも `terminated`。（ここが空で返っていたら、それこそ曖昧な答えだった。）
- `describe-launch-templates` → `af-af-ecs-pool-slot` だけ。
- `af-*` の `list-roles` → 本物の 8 本だけ。使い捨てのロール 2 本と instance profile は消えている。
- `list-clusters` → `af-af-ecs-platform` だけ。`list-namespaces` → `af.internal` だけ。
- instant fleet 4 本は削除済み（`deleted_terminating`）。インスタンスは持っていなかった。

**意図して残したもの: `AWSServiceRoleForEC2Fleet`。** ADR は `standup.sh` にこれを作らせる前提なので、
未解決 4 が許すとおり残した。それ以外にこの実測が残したものは無い。

**費用**: `t3.small` 約 4.2 分 ≒ **$0.002**。Cloud Map の private DNS namespace は同じ時間内に作って
消した（12 時間以内に削除した hosted zone は課金されない）。それ以外——CloudFormation・ECS・IAM・
`CreateFleet` 6 回——はすべて無料。

生の応答は、これを測ったセッションの `~/.cache/adr0077-p0/` にある。

## 追記 — P1 の CP レーンが本文に返したもの（2026-09-12・PR #577）

P1 の CP 側（決定 1・2・3・5・8・9・11、契約 A の CP 端）は #577 として入った。完了 1〜6 は
`engine_offer_test.go` と `internal/runtime/runtime_ecs_ec2_engine_test.go` に陽性対照つきで固定し
てある——手で走らせて戻した変異 2 件を含む: `registered()` の番人を外すと完了 3 が落ち、`af-role` の
絞り込みを外すと完了 5 が落ちる。完了 7 は実機レーンで、これには入っていない。実装が本文に返したもの:

- **EC2 のポートは 5 本ではなく 3 本。** P0 の実測で `CreateTags` は不要（`CreateFleet` の
  `TagSpecifications`（ResourceType `instance`）が launch template 側のタグと併合して起動時に付く）、
  `DeleteFleets` は決定 1 の形では使えない（instant fleet に `TerminateInstances=false` は
  `NoTerminateInstancesNotSupported` で拒まれる）。fleet 自体は残る（P0 (b)）ので、決定 1 の
  「CP は fleet id を覚えない」が生き残る理由は本文の言うものとは別だ——instant fleet は絞り込まない
  `describe-fleets` に列挙されず、容量も持たないので、CP が歩く経路には何も溜まらない。ポートは
  `CreateFleet` / `DescribeInstances` / `TerminateInstances`。
- 🔴 **失敗コードの表に 4 つめの答えが要る。決定 8 が想定していなかった `refused`。** `iam:PassRole`
  の欠落は **200 の `Errors[]` の中に** `UnauthorizedOperation` として来る——「在庫が無かった」と同じ形・
  同じ場所（P0 実測）。これを「次へ」と読むと、1 台も起動できない配備に対して需要のたびに一覧を丸ごと歩き、
  ログのどこにも欠けている権限の名が出ない。だから歩きをその場で止め、失敗 1 回として数え、原文をログに出す。
  top-level の `SsmAccessDenied` も同じ答えに畳む。「次へ」ではないことを試験で固定し、同じ仕掛けで
  `InsufficientInstanceCapacity` を返す陽性対照を隣に置いた。
- **`InvalidFleetConfiguration` は意味ではなく「位置」で読む。** `Errors[]` は override ごとに 1 件で、
  P0 では型の綴り違いと「その AZ で提供されていない型」を区別できなかった——どちらも同じコード・同じ文面。
  よって **全 override がこれのときだけ** その行を `unusable` とし、一部なら他の override はまだ訊く価値が
  あるので次の行へ進む。
- **`<role>OfferBudgetSec` の既定は意味と一緒に 180 秒から 300 秒へ**（決定 1）。⚠️ **旧い意味で 180 を
  書いた配備では、箱の ECS 登録に 180 秒しか与えないことになる**。ADR 0045 決定 22 の実測（21 秒、自前
  AMI で 77 秒）の内側ではあるが、起動が遅い日には余裕がない。この裏返しは CFN レーンが独立に決めていて
  両者は一致する: `standup.sh` は捕捉値の **180 ちょうどだけ**を落とし、テンプレートの 300 を効かせる
  （上のその節）。
- **契約 B から 1 欄だけ抜ける: `class_apply_error`。** 「段は保存されたが capacity provider に拒まれた」
  を伝える欄で、宣言そのものが要求になった以上（決定 8）起こり得ない——保存した瞬間からその選択が効いて
  いる。Console はこの欄があるときだけ読み、無ければ再試行の帯を出さないので決定 8 の「Console は変え
  ない」は成り立つ。ただし契約 B は「不変」ではなく「起こり得なくなった 1 欄を除いて不変」。
- **決定 3 が `sweepSlotOwnerTags` に足す絞り込みは `af-role = slot` ではなく `af-role ∈ {slot,
  quarantined}`。** 隔離された箱は（解放ではなく停止なので）まだ人の `af-membership` を持ち得るし、まさに
  それを直すのがこの掃除の役目である。`slot` だけに狭めると、持っていない箱の代金を誰かに付け続ける古い
  タグが残る——この絞り込みを足した理由と同じ欠陥の裏返し。
- **`draining` は ECS ではなく EC2 に、アダプタへ渡した関数で訊く。** 決定 5 は terminate を CP に渡し、
  順序を deregister → terminate と決めた。つまりこの状態が名指している窓のあいだ、ECS はその箱を知らない。
  そこで `engineECS` は EC2 クライアントもタグも持たず、提案の配線時に `fleet.live` を受け取る。Fargate の
  エンジンには何も渡らず、何も払わない。
- **決定 5 の掃除には猶予が 2 種類要る。CP は自分の `ghostAfter` を持たないから**（それはスロットプールの
  設定で、別パッケージにある）。desired 0 のときはタスクの無い箱を 2 分で終わらせる——これが通常の退場で、
  タスクが消えた次のティックにあたる——。サービスが上がっているときは、タスクの無い 2 台目を 15 分で終わら
  せる。これが ADR 0075 の「箱 2 台」の漏れで、置き付け中のタスクには手を触れない。
- **決定 9 の拒否は解析時に役で分岐する**（`parseEngineOffers(key, spec)`）。呼び出し側ごとの条件では
  なく 1 つの関数にした。再読み込みも同じものを使う。
- **`startGate` には 4 つめの門が要った: 「すでに購入済みの起動」。** 決定 8 は門に「候補無しの拒否」と
  「入れ替え待ち」を残したが、3 つめが必要だった。管理トグルは自分で門を呼ぶので、これが無いと箱が登録待ち
  のあいだにトグルの呼び出しが歩きをやり直し、2 台目を買う——ADR 0075 が 3 回中 3 回出した失敗。
- **CFN レーンに渡す契約 A の最終形**: `launchTemplate`（launch template の id `lt-…` または名前）が
  `capacityProvider` / `spotCapacityProvider` を置き換える。古い 2 欄は、まだ書いている配備でも**無視**する。
  `offers` / `offerBudgetSec` / `classes` ほかは不変。`launchTemplate` の無い行は箱を買わず、箱を見分けも
  しない——そのまま給仕を続け、素の経路でエンジンを起動する。スタックより先に CP が上がった配備がこれ。

## P0 と P1 の 2 レーンの後の改訂（2026-09-12）

上の 3 本の追記（#575・#576・#577）は決定の本文を書き換えず、発見を改訂の判断に差し戻した。この節が
その改訂で、ADR 0075 のレビューと同じ作法で**上の決定の本文に反映済み**——決定の前提は 1 つも動かして
いない。動いたのは、P0 が誤りか (c) だと測った文言である。

| 決定 | 変えたこと | 出所 |
|---|---|---|
| 1 | 「fleet が残るなら `DeleteFleets`」→ 実測: 残るが id で名指ししない限り `describe-fleets` に出ず、`DeleteFleets(TerminateInstances=false)` は拒まれる。**後追いの呼び出しは無し**、ポートは 3 つ／`Priority` は尊重・記録される | P0 未解決 3 |
| 3 | `sweepSlotOwnerTags` のフィルタは `af-role` が `{slot, quarantined}` で、`slot` だけではない（隔離された箱は走査が直す所有者タグを持ったまま） | #577 |
| 5 | 走査の猶予はスロットプールの `ghostAfter`（別パッケージ）ではなく、desired 0 で 2 分・service が上がっているあいだ 15 分 | #577 |
| 8 | 失敗コードの表を実測の語彙で書き出した。`InvalidFleetConfiguration` は全 override が言ったときだけ `unusable`。**`Errors[]` の中の認可失敗は一周を止める硬い失敗**、`--dry-run` は事前確認にならない | P0 未解決 3 |
| 10 | 公開 AMI パラメータの `ssm:GetParameters` は**無条件**。`DescribeFleets` / `DeleteFleets` / `CreateTags` / `DescribeLaunchTemplates` は不要。SLR は「安い保険」で、公開仕様の「最初の呼び出しが失敗」は未確認 | P0 未解決 3・4 |
| 11 | 移行は `<役>Enabled` の往復**だけ**（CFN は service を置き換え、明示した名前が衝突する。25 秒 + 48 秒）。あいだ Cloud Map の名前は**消えない**。🔁 は成立した | P0 未解決 1 |

未解決 8（インスタンスストアの mount）は、CFN レーンの発見（`<役>UseLocalStorage` の退役で cold start の
実測 2 倍を失う）により P1 の「測る」から P1 の範囲へ繰り上げた。#575 が user data で NVMe を Docker の
data-root にし、P1 実機が `df /var/lib/docker` を読む。

## 追記 — P1 実機: image 役を EC2 Fleet で（2026-09-12・開発配備・約 $1.02）

完了の定義 7 を af-sandbox で。CP と CFN の 2 レーン（#575・#577）は develop 入り済み、Control
Plane は `0.19.1-dev-07f95ee0`。**移行は通り、購入側は緑。箱の側が 2 段で赤、退場はいまも赤。**
GPU の箱 6 台・**30 分 24 秒**・約 **$1.02**——予算を 2% 超えた理由は費用の表にある。6 台のうち
4 台は成功しようのない起動が買ったもので、**launch template が買った箱は 1 台もクラスタに
参加できなかった**。原因は実機で突き止め、テンプレートを直し、直った箱が 48 秒で登録するところ
まで測った。そのあと予算の門が切ったのが `warm: true` と画像生成である。

以下はすべて生の戻り値か Control Plane のログ行。届かなかったものは「届かなかった」と書く。

### 移行（決定 11・`<役>Enabled` の往復）

| 手順 | 所要 | 戻り |
|---|---|---|
| 両役 `mode: off`・container instance にエンジンの箱なし | — | `describe-container-instances` はスロットの箱 4 台、`capacityProviderName: null`、`af-role` 属性なし。`image` は既に `off`、`llm` は `ondemand` だったので落とした |
| `update-stack --use-previous-template`・`LlmEnabled=false ImageEnabled=false`（**旧**テンプレート） | **24.8 秒**（04:53:40.692 → 04:54:05.454 UTC） | 両 service が 04:54:03 に `DELETE_COMPLETE`。P0 の 25 秒をそのまま再現 |
| `dev-deploy.sh --profile af-sandbox --region ap-northeast-1` | **約 31 分**（13:56 → 14:26:49 JST）、うち約 20 分は 2 アーキテクチャの QEMU ビルド | 60-engines 自体は **189 秒**（05:17:56.559 → 05:21:05.407）。`LlmCapacityProvider` / `ImageCapacityProvider` / `ImageSpotCapacityProvider` / `InfraRole` / `InstanceRole` / `InstanceProfile` が `DELETE_COMPLETE`、`LlmLaunchTemplate` / `ImageLaunchTemplate` / `EngineInstanceRole` / `EngineInstanceProfile` が `CREATE_COMPLETE`、`CpIngestPolicy` と `Associations` は更新。20-platform は「No changes to deploy」＝Fleet の権限は本当に 60-engines だけのもの（R1） |
| `cloudformation deploy … --parameter-overrides LlmEnabled=true ImageEnabled=true` | 🔴 **691 秒**（05:28:39.986 → 05:40:10.996）、しかも手を入れたから終わった（下記） | `launchType: EC2`・`capacityProviderStrategy: null`・`placementConstraints: [{memberOf, "attribute:af-role == engine-image"}]` |

そのあと確認したこと: `describe-capacity-providers` は **`FARGATE` と `FARGATE_SPOT` だけ**
（Managed Instances の 3 本は `INACTIVE` ではなく消えた）、`/af-ws/engines` は両行に
`"launchTemplate":"lt-…"` を持ち `capacityProvider` フィールドは無い。

#### 🔴 `<役>Enabled=true` の側は止まる。`DesiredCount` を書いていないから

`AWS::ECS::Service` にこのテンプレートが `DesiredCount` を書かないのは意図的である（「新規
service で未指定なら 1」。PARAMETERS「エンジンの service」）。Managed Instances では無害だった
——desired 1 が ECS を買い物に行かせ、service は落ち着いた。EC2 起動タイプでは**箱が無い**ので、
両 service は desired 1 で作られ、載せる先が無いまま CloudFormation が安定化待ちに座り込んだ。

```
(service af-af-ecs-engines-image) was unable to place a task because no container instance met all
of its requirements. The closest matching (container-instance 28509d40…) doesn't have the agent
connected.
```

10 分後、別のシェルから両 service に `aws ecs update-service --desired-count 0`（05:38:56）を
当てると steady state に達し、スタックは 05:40:10 に完了した。**放っておけばリソースの
タイムアウトまで走ってロールバックしていた。** `standup.sh` は新しい service を作った直後に 0 へ
落としている。`<役>Enabled` の往復はこの service を作るもう 1 つの経路であり、決定 11 も
PARAMETERS も同じことが要ると書いていない。直す人は「更新中に別シェルから 0 へ落とす」（今回
やったこと）か、リソースに `DesiredCount: 0` を明記するかを選ぶことになる。後者はテンプレートの
コメントが反対している選択で、その理由は消えていない。

**影響 — 決定 11。** 順序が 1 手足りず、その 1 手は省けない。

#### 🔴 Control Plane はエンジン表が空のまま起動し、そこから戻れない

`EnginesParam` は同じ `<役>Enabled` の条件下にあるので、往復のあいだエンジン表は**行が無い**。
そして `dev-deploy.sh` は新しい Control Plane イメージをまさにその窓の中で載せる（CP の起動は
05:22:37）。`newEngineRegistry` は行 0 で `nil` を返すため、表の再読み込み（reloader）も起動
されず、行が戻っても CP は気づかない。

```
GET /api/admin/engines  →  {"engines":[],"super_admin":true}      （05:41・Enabled=true の 1 分後）
```

ログには何も出ない。`engine_table_reload.go` のヘッダは「この process が持たない役が表に現れたら
再起動を要求する」と約束していて、その分岐は実在する——が、行 0 の起動が構築しない reloader の
中にある。CP service の `force-new-deployment` で **86 秒**で直った（05:42:47 →
`engines: image (images) -> http://image.af.internal:8080 …` が 05:43:35）。

**影響 — 決定 11。** 移行は 2 手ではなく 3 手である: `false` → 当てる → `true` → **CP を強制
再配備**。CP イメージを往復の*後*に回しても単独では効かない。すでに走っている CP もその前に
起動しているからである。

#### 🔴 Cloud Map の名前は消える。P0 の訂正は実テンプレートでは成り立たない

P0 は `AWS::ServiceDiscovery::Service` に条件が無い使い捨てスタックで往復を測り、決定 11 の
「あいだ Cloud Map の名前は消える」は言い過ぎだと結論した。`60-engines.yaml` の discovery
リソースは service と同じ `Condition: HasLlmModel` / `HasImageModel` を持っており、一緒に消えた。

```
LlmDiscovery    DELETE_COMPLETE  2026-09-12T04:54:04.621Z
ImageDiscovery  DELETE_COMPLETE  2026-09-12T04:54:04.879Z
```

名前が無い時間は窓の全体、今回は数秒ではなく **34 分**だった（`dev-deploy.sh` のビルドが往復の
あいだに挟まるため）。✅ desired 0 なので失うものは無く、決定 11 自身の理屈は保つ。保たないのは
「言い過ぎだ」と言った P0 の理由のほうである。

もう 1 つ、後の読み手が導き直さずに済むように書いておく: レジストリの **ARN は同一で戻ってきた**
（llm が `srv-myk…`、image が `srv-lge…`。CloudTrail の `DeleteService` リクエストと
`CreateService` レスポンスから読んだ）。Cloud Map が同じ名前空間＋名前に同じ id を振り直した
のであって、リソースが生き残った証拠ではない。PARAMETERS の ✅ は後者を言っているので、前者に
直す必要がある。

### $0 の陽性対照（05:47:38-42・4 秒・箱は上がらない）

宣言: `bad`（spot・`g6.xxlarge`）/ `q4x`（spot・`g6.4xlarge`）/ `q4xb`（spot・`g6.4xlarge`）/
`od48`（od・`g6.48xlarge`）。

| 検査 | 結果 | 証拠 |
|---|---|---|
| (a) 綴り違いの型は `unusable` になり次へ進む | 🟢 | `offer bad (spot) bought nothing: InvalidFleetConfiguration: Your requested instance type (g6.xxlarge) is not supported in your requested Availability Zone (ap-northeast-1a). \| … (ap-northeast-1c).` → `trying the offer q4x (spot) after unusable`。両 override が言ったので位置による読み（#577）が行を unusable と宣言した |
| (b) クォータ超の Spot 行は `quota` になり、同じ購入形態を table から外す | 🟢 | `offer q4x (spot) bought nothing: MaxSpotInstanceCountExceeded: Max spot instance count exceeded \| …` のあと **`q4xb` は一度も現れない**——次に試されたのは `od48`。`offer_trail` は 4 行の一覧に対して `[bad unusable, q4x quota, od48 quota]` の 3 行 |
| (c) オンデマンドのクォータは別の壁 | 🟢 | `offer od48 (od) bought nothing: VcpuLimitExceeded: You have requested more vCPU capacity than your current vCPU limit of 8 allows …` |
| (d) 何も買っていない | 🟢 | `af-role=engine-image` を状態フィルタ無しで `describe-instances`——新しいものは無し。一周は **4 秒** |

⚠️ **一覧を使い切ると管理 API は 502 を返す**——`PUT /api/admin/engines/image 502 4.607s`、
mode は既に `on` として保存済み。これは `errEngineOffersSpent` がハンドラまで届いたもので proxy の
タイムアウトではなく、今日たまたま在庫が無い配備で運用者が見るのと同じ 502 である。もう少し
親切なコードにする価値はあるが、間違ってはいない。

### 🔴 箱の側: 1 台もクラスタに入れず、原因は 2 段

| 走行 | 箱 | 起きたこと |
|---|---|---|
| A（05:50-06:05） | `i-0b8c…` g6.xlarge spot・`i-0d42…` g6.xlarge od・`i-0a96…` g6e.xlarge od | いずれも `the box … for offer <id> did not register within 3m0s; ending it` → 次の提案へ。一覧を使い切った |
| B（06:07-06:13）・修正 1 の後 | `i-0779…` g5.xlarge spot・`i-0225…` g6.xlarge od | 同じ |
| C（06:14-06:23）・修正 2 の後 | `i-0353…` g6e.xlarge od | **48 秒で登録** |

**診断は生きている箱に SSM で入って行った**（`EngineInstanceRole` に
`AmazonSSMManagedInstanceCore` があり、エージェントは `Online` だった——つまり箱には最初から
ネットワークも資格情報も AWS への経路もあった）。

```
systemctl is-active ecs docker   →  inactive / active
systemctl status ecs             →  ○ ecs.service … enabled; Active: inactive (dead)
journalctl -u ecs                →  -- No entries --
df -h /var/lib/docker            →  /dev/nvme1n1  233G  1.7G  232G   1% /var/lib/docker
```

user data は仕事を全部終えていた——NVMe は Docker の data-root として mount され、
`/etc/ecs/ecs.config` には `ECS_CLUSTER` と `ECS_INSTANCE_ATTRIBUTES` が入っていた——そして
**エージェントがただ起動されていなかった**。手で `systemctl start ecs` を叩くと箱は **3 秒**で
登録された（`Websocket connection established … containerInstanceArn=…/a0694593…`）。これが
「鎖の残りは全部正しい」ことの陽性対照である。

1. 🔴 **`ecs.service` は `PartOf=docker.service`。** user data は data-root を移すために docker を
   止める。すると systemd は `multi-user.target` のために積んでいた `ecs.service` の起動ジョブを
   取り消し、`systemctl start docker` はそれを積み直さない。エージェントは journal が空のまま
   `inactive (dead)` で残る——**どこにもエラーは出ず**、症状は「箱が登録されない」だけである。
   決定 6 も PARAMETERS の「ecs.service は After=cloud-final なのでエージェントはまだ起動して
   いない、だから順序は成り立つ」も、これを見ていない。エージェントは起動していないが、
   *ジョブが待っていた*。docker を止めたのがそのジョブを奪ったのである。
2. 🔴 **user data の末尾に素の `systemctl start ecs` を足すと固まる。** `ecs.service` は
   `After=cloud-final.service` で、user data 自身が cloud-final なので、ジョブの完了を待つ
   start は自分を待つ呼び出しを待つ。次の箱で実測:

   ```
   JOB UNIT                  TYPE  STATE
   871 ecs.service           start waiting
   263 cloud-final.service   start running
   cloud-init status: running
   ```

   箱は登録の上限に終了されるまでそこに座っていた。

**修正は `systemctl start --no-block ecs` を、`ecs.config` を書いたあと最後に置くこと**
（エージェントは起動時に設定を読む）。この PR に両役の launch template ぶん入れ、2 つの実測を
コメントに書いた。⚠️ これが箱に届くのは、CP が launch template の **`$Latest`** を指定して
いるからである。CloudFormation は version 2・3 を作りながら `DefaultVersionNumber` を **1** の
まま残した。動いた箱は `aws:ec2launchtemplate:version = 3` を持っている。

**影響 — 決定 6・7 と CFN レーンの追記 4。** どの決定の前提も動かない。user data が 1 行足りず、
順序が安全だという ADR の理由が話の半分だった。

### 走行 C・完了の定義 7 が求める検査

箱（`i-0353a9e56cba02b2f`・`g6e.xlarge`・オンデマンド・提案 `l40s`）は 06:14:15 に買われ、走行は
06:19:13 に予算の門で止めた。

| 検査 | 結果 | 証拠 |
|---|---|---|
| **箱は 1 台だけ**（ADR 0075 は 3 回中 3 回とも 2 台） | 🟢 | どの標本でも `af-role=engine-image` の `running` は 1 台以下。クラスタの container instance は 4 → 5 → 4。`offer_trail` の `active` は 1 行。0075 の形は起こりようがない——desired は登録の後に書かれるので、ECS が自分で置きにいく窓が無い |
| **起動が deployment の落ち着きを待たない** | 🟢 | `06:15:03 engines: image: the box i-0353… registered; asking for the task`——desired が上がるのは**箱が登録したのと同じ秒**。0075 の再走はここで古い deployment の退場に 2 分 35 秒を払った。その待ちは形を取りようがなく、ログにも現れない |
| 購入 → 箱の登録 | 🟢 **48 秒** | 06:14:15 購入・06:15:03 登録。効いていた上限は **180 秒**（後述） |
| Spot の行で来た型（0075 実機 3 は g6e） | 🟢 | Spot の購入は 2 回、**`g6.xlarge`**（走行 A）と **`g5.xlarge`**（走行 B）。g6e は一度も無い。`price-capacity-optimized` は Managed Instances に頼めなかったことをしている——当時の Spot 価格は g6.xlarge $0.563-0.576・g5.xlarge $0.739-0.790・g6e.xlarge $1.353 |
| `af-engine-buy` / `af-engine-offer` タグ | 🟢 | 1 回の `CreateFleet` から箱に: `af-pool=af-af-ecs-platform`・`af-role=engine-image`・`af-engine-offer=l40s`・`af-engine-buy=od`・`af-managed-by=agent-fleet`・`Name=af-engine-image`、加えて EC2 自身の `aws:ec2:fleet-id` と `aws:ec2launchtemplate:{id,version}`。`CreateTags` の呼び出しは無い（P0 (d) のとおり） |
| `InstanceLifecycle` | 🟢 | Spot の 2 台は `spot`、オンデマンドの 4 台は**欠落**。P0 (d) の読み（「無い＝オンデマンド」）が読み手に要るもの |
| パネルが箱から答える | 🟢 | `box: {id: i-0353…, instance_type: g6e.xlarge, since: 06:14:48Z, status: ACTIVE}`・`offer: {id: l40s, buy: od}`・`offer_trail` は `[spot3 budget, l4 budget, l40s active]`。箱があるあいだ `null` にはならない |
| **`df /var/lib/docker` はインスタンスストアか**（未解決 8・ここまで未検証） | 🟢 | `/dev/nvme1n1 233G … /var/lib/docker`・`Docker Root Dir: /var/lib/docker`、fetch 中の `du -sh /var/lib/docker/volumes` は **24G**。匿名の `host` ボリュームは設計どおり NVMe に落ち、タスク定義は何も変わっていない |
| fetch が 125 MB/s ではなく NVMe の速度で書くか | 🟢 | fetch サイドカー自身のログから: `sd3.5_medium 5,107,104,286 bytes in 27s`（189 MB/s）・`t5xxl 4,893,934,904 in 47s`（104 MB/s）・`neoAnime 7,105,352,134 in 34s`（209 MB/s）・`sd_xl_base 6,938,078,334 in 34s`（204 MB/s）・`z_image_turbo 12,309,866,400 in 60s`（205 MB/s）。ADR 0071 の Managed Instances の数字は 104-147 MB/s |
| `state: running` / `warm: true` | 🔴 **届かず** | desired 1 が 06:15:03 → fetch 開始 06:15:31 → 既定のモデル一式がディスクに 06:17:04 → ComfyUI の process が GPU に乗ったのが **06:18:52**（`Total VRAM 45458 MB … Device: cuda:0 NVIDIA L40S`）＝ desired から **3 分 49 秒**。予算の門が 06:19:13 に閉じ、warm の判定まで 30〜90 秒ほど足りなかった。比較: ADR 0075 の再走は Managed Instances の g6.xlarge Spot で `warm: true` まで 5 分 11 秒 |
| 画像を 1 枚生成して 200 | 🔴 **届かず** | 同じ門 |
| **退場**: desired 0 → deregister → terminate | 🔴 | 06:19:13 に `mode: off` → `desiredCount` は即 0、タスクは 06:20:29 までに消えた（**76 秒**）——そこから**何も起きない**。4 分後も箱は `running`、パネルは `box: {…, status: ACTIVE}` / `state: draining` のまま、CP のログは 1 行も出ていない。06:23:39 に手で terminate |
| terminate → `terminated`（未解決 6） | 実測 **5 分 28 秒〜5 分 45 秒** | 06:23:39 に `terminate-instances`、最後に `shutting-down` を見たのが 06:29:07、`terminated` が 06:29:24（15 秒間隔の poll）。🔴 決定 5 の 🔁 が言う**5 分を超えている**ので、0071 決定 7 の窓の算術は書き直しが要る |

#### 🔴 退場が起きない理由: `startInFlight()` が二度と下りない

`sweepBoxes`（決定 5・両方向）は `e.offers.startInFlight()` が真のあいだ早期に return する。
それ自体は正しい——買ったばかりの箱にタスクが無いのは構造上あたりまえだからである。しかし
`startInFlight()` は `boxID() != ""` であり、箱の id を消すのは登録上限の経路の `dropBox()` と
*次の*起動の `begin()` の 2 つだけである。**成功した起動は id を立てたまま残す。** つまり箱が
登録した瞬間から、その engine について決定 5 の走査は切られる——それが行うはずの通常の退場、
すなわち desired 0 を書いた次の tick も含めて。

`startInFlight` のコメントは意図を正確に書いている——「箱は買われ、desired はまだ書かれて
いない」——そして後半を書き戻すものが無い。今日 2 回測った: 一度は mode off と登録しなかった箱で
（9 分後も `running`）、一度は成功した起動の後の mode off で（4 分後も `running`、手で terminate）。

**影響 — 決定 5。** 前提は無傷で、実装が自分の走査を切っている。修正は CP レーンのもの:
`registered()` の後に `setEnabled(ctx, true)` を呼ぶ場所で箱の id を消し、「立てたままだと退場の
走査が一度も走らない」を陽性対照にした試験で留める。

#### ⚠️ 登録の上限は 300 ではなく 180 秒で動いていた

この配備の `ImageOfferBudgetSec` は **180**——0.19.0 の既定、古い意味のものである。CFN レーンの
追記は `standup.sh` にちょうど 180 を落とさせてテンプレートの 300 を立てるが、この配備を更新した
のは **`update.sh`** で、こちらは live のスタックのパラメータを `UsePreviousValue` で通し、何も
落とさない。つまり普通に上げた配備は短い上限を黙って抱えたままになる。今回は差が出なかったが
（48 秒）、既定を動かした理由はまさにその「余裕の無さ」である。

### 費用と後始末

| 箱 | 型 | 購入 | 起動（UTC） | 終了 | 生存 | $/h | 概算 |
|---|---|---|---|---|---|---|---|
| `i-0b8c04c29851f88fa` | g6.xlarge | spot | 05:50:11 | 05:53:16 | 3m05s | 0.576 | $0.030 |
| `i-0d428939322e54519` | g6.xlarge | オンデマンド | 05:53:17 | 05:56:22 | 3m05s | 1.167 | $0.060 |
| `i-0a96425b7edf037bb` | g6e.xlarge | オンデマンド | 05:56:23 | 06:05:43 | 9m20s | 2.699 | $0.420 |
| `i-0779f939b8309f373` | g5.xlarge | spot | 06:07:42 | 06:10:47 | 3m05s | 0.739 | $0.038 |
| `i-02258932d93e27f7d` | g6.xlarge | オンデマンド | 06:10:49 | 06:13:14 | 2m25s | 1.167 | $0.047 |
| `i-0353a9e56cba02b2f` | g6e.xlarge | オンデマンド | 06:14:15 | 06:23:39 | 9m24s | 2.699 | $0.423 |

**30 分 24 秒・約 $1.02**（オンデマンドは pricing API の ap-northeast-1/Linux、Spot は同じ時間帯の
`describe-spot-price-history`）。予算は $1 で、最後の箱から `df` と fetch の数字を読んでいる
あいだに 2% 超えた。⚠️ **g6e の 2 台が支出の 82%** ——どちらも、安い 2 行が登録の上限で終了された
あと提案の一覧が `l40s` まで落ちてきたから買われ、どちらも上記の退場の欠陥のせいで手で終わらせる
まで生き延びた。どの数字にも Managed Instances の手数料はもう乗っていない（決定 8）。

- Spot の購入 2 回はどちらも G/VT Spot クォータ（`L-3819A6DF`）8 vCPU のまま通った。
  `AWSServiceRoleForEC2Fleet` も `AWSServiceRoleForEC2Spot` も既にあった。
- `af-role` が `{engine-image, engine-llm}` の `describe-instances` を**状態フィルタ無し**で:
  6 台、すべて `terminated`。（陽性対照は同じ出力の中にある——`tag-key=af-role` だけの問い合わせは
  スロットの 4 台を含めて 10 を返すので、空の答えのほうが曖昧だったはずである。）
- container instance は **4 台**に戻り、すべてスロットの箱。`describe-fleets` を絞らずに引くと
  `[]`——P0 (b) の実測どおり、instant fleet は列挙されない。
- **手順 1 で控えた値に戻した**: `image` は `mode: off`、`llm` は `mode: ondemand`、`ImageOffers`
  は走行前に読んだ値とバイト一致、`ImageOfferBudgetSec` は 180、`ImageInstanceClasses` は無変更、
  両 `<役>Enabled=true`。
- ⚠️ **意図して変えたまま残したもの**: 開発配備は launch template の 2 つの修正を載せて動いて
  いる。これはこの PR にあり、develop にはまだ無い。それ以外でスタックがブランチから遅れている
  ものは無い。
- 生の戻り値は測ったセッションの `~/.cache/adr0077-p1/` にある。

### 本文に返すもの

| # | 判定 | 決定 | 直すこと |
|---|---|---|---|
| 1 | 🔴 | 6・7 | launch template が ECS エージェントを自分で、`--no-block` で、`ecs.config` の後に起動すること。この PR で修正・実測済み。PARAMETERS の「ecs.service は After=cloud-final だから順序は成り立つ」に `PartOf=docker.service` の半分を足す必要がある |
| 2 | 🔴 | 5 | 成功した起動のあと `startInFlight()` が下りないので退場の走査が一度も走らず、誰かが気づくまで箱が課金され続ける。CP レーン |
| 3 | 🔴 | 11 | `<役>Enabled=true` の側が「箱の無い desired 1」の安定化待ちで止まる。移行に「更新中に両 service を 0 へ落とす」か明示の `DesiredCount` が要る |
| 4 | 🔴 | 11 | 往復のあとに CP を強制再配備する必要がある。CP は行 0 のエンジン表で起動し、`newEngineRegistry` はそこで `nil` を返すので reloader すら無い |
| 5 | 🔴 | 11 | 実テンプレートでは Cloud Map の service も役の条件を持ち、service と一緒に消える。P0 の ✅ は条件の無い使い捨てで測ったもので、PARAMETERS は運用者が読む場所で違うことを言っている |
| 6 | ⚠️ | — | `update.sh` は捕まった `<役>OfferBudgetSec=180` を落とさない（落とすのは `standup.sh` だけ）ので、普通に上げた配備は古い上限を抱える |
| 7 | ⚠️ | 6 | PARAMETERS の「EBS だけの型（g6e）は何にも当たらない」は誤り——`g6e.xlarge` は 232.8 GB の インスタンスストアを持ち、実際に使った |
| 8 | 🟢 | 1・2・3・8 | 箱は 1 台・落ち着き待ち無し・安いほうの Spot 型・タグ・箱から答えるパネル・NVMe の data-root。購入側に直すところは無い |

**完了の定義 7 は未達である**: `warm: true`・画像生成・CP が駆動する退場は測れていない。項目 1 と
2 が develop に入ったあと、GPU 約 10 分の走行がもう 1 回要る。

## 追記 — P1 の項目 1・3・4・6 に対する CFN レーンの答え（2026-09-12・PR #585）

項目 2（CP の `startInFlight`）と、P1 の PR がその場で直した PARAMETERS の 2 文（項目 5・7）は
このレーンのものではない。残りの 4 件がこのレーンで、うち 3 件はコードの修正が P1 の PR に
入っているので文書だけである:

- **項目 1 — 順序の段落。** PARAMETERS の「この順序が効くのは ECS エージェントがまだ起動して
  いないから」に、同じ事実のもう半分を足した: `ecs.service` は `PartOf=docker.service` なので、
  docker を止めるとエージェントに積まれていた起動ジョブが消え、docker を起こし直しても積み直され
  ない——だから user data は自分でエージェントを起こし、ブロックする start は自分を走らせている
  cloud-final のジョブを待ってしまうので `--no-block` を付ける。
- **項目 3・4 — 移行は 4 手になった。** 手順は 3 手（`false` → 当てる → `true`）だったが、
  `true` の半分が走っているあいだ両 service を desired 0 に保つ**別シェル**と、そのあとの
  コントロールプレーンの `force-new-deployment` を書き足した。どちらも P1 実機の実測で、
  どちらも飛ばすと黙って壊れる——スタックは勝てない安定化待ちに座り、パネルは
  `{"engines":[]}` と答えてログには何も出ない。
- **項目 6 — `update.sh` でも予算を直す。** `standup.sh` は捕捉された
  `<役>OfferBudgetSec=180`（旧い意味の既定値）を落とすが、既存の配備が上がる道は `update.sh` で、
  そちらはパラメータを渡さない。そこで、そのファイルに既にある ADR 0072 P6 の修復と同じ形で
  ——live のスタックを読み、**値がちょうど 180 のときだけ**テンプレートの既定値を渡す——修復を
  足した。それ以外の値は運用者の選択として触らない。手で `cloudformation deploy` を走らせる道は
  どちらでもないので、PARAMETERS にそう書いた。

🔁 **これが変わるとき**: エンジンの service に `DesiredCount: 0` を明示すれば項目 3 の別シェルは
要らなくなる——が、それはタスク定義を変えるたびに desired を戻すという罠でもあり、
[the engine services](../../deploy/aws/ecs/cfn/PARAMETERS-60-engines.md) がその理由で
このプロパティを書いていない。別シェルのほうが安い。
