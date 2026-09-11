# 0074. エンジンが買う GPU は、運用者が宣言した梯子から実行時に選ぶ

[English](0074-engine-instance-classes.md) | 日本語

- 状態: **採用・P0 実装済み・P1 実機検証済み**（2026-09-10）。
  **この文書のために新しく測ったものは無い。** 数字はすべて出所を書き分けてある——
  (a) ADR 0071・0072 の実測、(b) この日にリポジトリのコードから読んだ事実、
  (c) AWS の公開仕様として知っているだけで**この配備では確かめていない**もの（g6e の VRAM と
  価格、リージョン別の在庫）。(c) は決定の根拠にしていない。決定が (c) に依存する箇所は
  「未解決の点」に列挙し、どの決定がそれに依存するかを各項に書いた。
- 同日にレビューを 1 巡受けて改訂した。**未解決 2 と 4 が決定へ移った**——再適用の時点を
  決定 5 に足し（起動の直前にも冪等に。費用は増えず、失敗時は「最後に適用した段と違うなら
  起動しない」）、クォータで宣言を弾かないことを決定 1 に足した（**本番のクォータは 96**、
  開発と af-sandbox は 8、そして CP はそれを読めない）。あわせて**出荷時の梯子は空**と
  決め（決定 1）、決定 4 に「待つ理由は読めない数に分岐を書かないため」と切替 1 回の費用
  （壁時計 15〜20 分・g6.xlarge 換算 $0.3〜0.4）を書いた。
- 同日、**P0 を実装した**（実機は未了）。実装が本文を 3 か所直している——
  🔴 **却下した案の理由が間違っていた**（容量の壁は `af_cfn_deploy` が S3 で越えるので
  「入らない」ではない。本当の理由は決定 5 の冪等な再適用が service では成立しないこと）、
  **写しの危険は `InstanceRequirements` ではなく `InstanceLaunchTemplate` にあった**
  （要求の型は読み書きで同じだが、起動テンプレートは別の型で**2 欄が運べない**）、
  **管理 API の `mode=on` も門を通す**（通さないとボタン 1 つで待ちを迂回する）。
  詳細は決定の各項に追記した。
- 同日、**P1 の実験計画を足した**（「P1 の実験計画」節）。運用者が**クォータ 8 のまま**行うと
  決めたため、8 vCPU で何が測れて何が測れないかを先に書いた——**上の段を 4 vCPU の型にすれば
  引き上げ申請は要らない**、そのかわり**決定 4 の必要性は 4→4 では証明できない**（合計 8 に
  ちょうど収まる）ので **8 vCPU の段を陽性対照に使う**、そして**仕組みの検証にカードの
  大きさは要らない**（g6e が無ければ g5.xlarge で全部測れる）。
- 同日、**P1 を実機で実施した**（af-sandbox・image 役・GPU 稼働 100 分・$4.6）。結果は
  「P1 の実測」節にあり、**未解決 1・2・3 が埋まり、決定 4・5・9 が変わった**。
  🔴 **実装が 2 か所壊れていて、どちらも実機でしか出なかった**——
  `DescribeCapacityProviders` は cluster と名前を同時に受け付けない（fake が何でも
  受け取るので単体テストは全部緑だった）、そして **`UpdateCapacityProvider` を MI の
  provider に対して呼ぶと ECS はクラスタ側の `ecs:PutClusterCapacityProviders` も要求する**
  （CP が呼んでいない API 名なので、コードを読んでも権限表を読んでも出てこない）。
  決定 9 の「2 動作＋PassRole で足りる」は誤りだった。
  🔴 **決定 4 の必要性は、予想した `VcpuLimitExceeded` より先に別の形で現れた**——門を
  迂回して起こすと、タスクは**古いカードにそのまま着地し、エラーはどこにも出ない**。
  そして**「インスタンスが消えるまで待つ」は必要条件であって十分条件ではない**（EC2 のクォータ解放は
  ECS の登録解除より 5 分以上遅れた）。
- 同日、**追試を 1 本足した**（「追試」節・使い捨て provider・**GPU 課金 $0**）。
  🔴 **未解決 1 の ✅ は、実は推定だった**——見たのが既定値の `ON_DEMAND` だったので
  「保持された」と「既定に戻された」を区別していない（同じ欠陥を `fipsEnabled` については
  自分で指摘していた）。**非既定値 `SPOT` で測り直して保持を確認**し、推定を測定へ上げた。
  あわせて **`fipsEnabled` は ap-northeast-1 では設定そのものができない**（宿題が消える）、
  🔴 **`L-DB2E81BB` は存在しないコード**（P1 の*訂正*のほうが誤りだった）、
  🔴 **「本番のクォータは 96」は現況と違う**（acrt 実測は On-Demand 64・Spot 64）ことが分かった。
- 関連: [0071-self-hosted-inference-engines.ja.md](0071-self-hosted-inference-engines.ja.md)
  決定 2（役ごとに 1 つの capacity provider、VRAM の下限でインスタンスを選ぶ）・決定 5・決定 9 /
  [0072-engine-model-catalog.ja.md](0072-engine-model-catalog.ja.md) 決定 1・決定 7（スタックは
  SEED、保存された選択が勝つ）・決定 10 /
  [0045-ec2-persistent-workspace.ja.md](0045-ec2-persistent-workspace.ja.md) 決定 21
  （梯子の数字は運用者が宣言する。EC2 には訊かない） /
  [0048-member-cloud-cost.ja.md](0048-member-cloud-cost.ja.md) 決定 15（費用配賦のタグ） /
  [70-slot-instance-classes.md](../log/70-slot-instance-classes.md)（スロットのクラス梯子の実装記録）

## 背景

要求は「LLM・画像の役が使う GPU インスタンスタイプを変えられるようにしたい。一時的に VRAM を
大きく使うモデルを試したい。選んだモデルによっては警告しつつ、インスタンスを拡張できるとよい」。

ADR 0072 がモデルを CloudFormation から外してカタログに移した結果、**モデルは実行時に差し替え
られるのに、そのモデルを載せるインスタンスは配備時に固定されたまま**になっている。FLUX.1-dev（23.8 GB・
0072 P4 の実測）のような重いモデルを取り込むところまでは Console で完結するのに、それが載る
カードは g6.xlarge（L4）のままで、起動しても CUDA が落ちる。

### いまのコードが言っていること（2026-09-10 に読んだ）

- **インスタンスは capacity provider の `InstanceRequirements` が決める。** `60-engines.yaml` の
  `LlmAllowedInstanceTypes`（既定 `g6.xlarge,g5.xlarge`）・`LlmAcceleratorMemMinMiB`（21000）・
  `LlmVCpuMin/Max`（4/8）・`LlmMemMinMiB/MaxMiB`（15000/65536）と、image 役の鏡像。
  **すべて CloudFormation パラメータ＝配備時固定**で、変えるにはスタック更新が要る。
- **サービスは役の provider を名指ししている**（`CapacityProviderStrategy: [{CapacityProvider:
  !Ref LlmCapacityProvider, Weight: 1}]`）。つまりインスタンスを変える口は provider の要求だけである。
- **51,200 バイトの壁。** `60-engines.yaml` は 49.7 KB で、1 ブロック約 1.5 KB の capacity
  provider を段数ぶん並べる余地は無い。🔴 ただし**壁は越えられる**——`af_cfn_deploy` が
  超過分を S3 経由に切り替える。越えられないのは `deploy/local/ecs-lifecycle-stub-test.sh`
  3b-2 が出荷テンプレートに課している同じ数字のほうである（却下した案を参照）。
- **`engineDef` は「インスタンスはスタックのもの」と書いている**——「Everything else here (service, URL,
  health, capacity provider, idle, deadline, mode) really is a property of the vessel and stays
  the stack's to declare」。本 ADR はこの一文の一部を意図的に覆す。
- **カタログには VRAM の欄が既にある。** `engine_models.vram_mib` は「運用者の実測、0 = 未測定」で、
  いまは管理パネルにチップとして**表示されるだけ**。`EngineModelFile.Bytes` も宣言値としてある
  （CP は S3 を見られないので、これは「誰かが宣言した大きさ」である）。
- **VRAM は和ではなく最大で効く。** llm 役は `LlmModelsMax: 1`（「the router does not know about
  VRAM: a second 30B Q4 on an L4 does not run slowly, it crashes」）、image 役の sd-server は
  1 プロセス 1 チェックポイント。したがって同時に VRAM に載るのは**有効なモデルのうち 1 つ**である。
- **価格が i18n に直書きされている。** `admin.engines_always_on_note` は日英とも「$1.26/時」と
  書いている。インスタンスが選べるようになった瞬間、この文は嘘になる。
- **`engine_hourly` の主キーは `(engine_key, hour)`。** どのインスタンスで走った時間かは記録していない。

### 上流が持っているもの（aws-sdk-go-v2/service/ecs v1.87.0 を読んだ）

- **`UpdateCapacityProvider` は Managed Instances provider を更新できる**
  （`ManagedInstancesProvider *types.UpdateManagedInstancesProviderConfiguration`）。
- 🔴 **「These changes only apply to new Amazon ECS Managed Instances, or EC2 instances, not
  existing ones」**——**走っているインスタンスは変わらない**。これが決定 4 の全部である。
- `UpdateManagedInstancesProviderConfiguration` は `InfrastructureRoleArn` と
  `InstanceLaunchTemplate` が**必須メンバー**。`InstanceLaunchTemplateUpdate` は
  `InstanceRequirements` / `NetworkConfiguration` / `StorageConfiguration` /
  `LocalStorageConfiguration` / `Ec2InstanceProfileArn` などを持つ。
- 🔴 起票時に「要求の型が読みと書きで別」と書いたのは**誤り**（実装で確認）。要求は双方向とも
  `InstanceRequirementsRequest` である。**別なのは 1 つ上の起動テンプレート**で、
  `InstanceLaunchTemplateUpdate` には `CapacityOptionType` と `FipsEnabled` が無い。
  写し漏れは**黙って落ちる**（決定 8）。

### 既に踏んである罠（ADR 0071・0072 の実測）

- **G 系 vCPU クォータは 8。** 止めた直後に起こすと退場中のインスタンスが 4 vCPU を握って
  `VcpuLimitExceeded`。退場は 427〜477 秒。
- **MI に allocation strategy は無く、条件に合う中で最安が買われる。**
  `AllowedInstanceTypes` は「優先順位」ではなく「絞り込み」である。
- **MI のインスタンスは `ec2 describe-instances` の一覧に出ない**（名指しなら返る）。インスタンスの在否は
  ECS の container instance で見る。
- **コールドスタートは llm 586 秒 / image 165〜197 秒**、g6.xlarge は **$1.26/時**。
- **SSM Standard tier は 4,096 文字**。エンジン表も active set も同じ制約下にある。
- **VRAM の実測**: 30B Q4 が 20,943 MiB、SDXL fp16 が 7,379 MiB。

## 決定

### 1. 梯子は運用者が宣言する。CP は EC2 に訊かない

役ごとに 1 本の文字列で「段（rung）」の梯子を宣言する。書式はスロットの梯子
（`AF_ECS_EC2_SLOT_TYPES`、ADR 0045 決定 21・docs/log/70）と同じ流儀にする——`|` で欄、`;` で段。

```
id|表示名|vramMiB|instanceTypes|vcpuMin-vcpuMax|memMinMiB-memMaxMiB|usdPerHour
```

```
l4|L4 24GB|21000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26;
l40s|L40S 48GB|44000|g6e.xlarge,g6e.2xlarge|4-8|30000-65536|
```

- **`vramMiB` は 1 つの数で 2 つの役をする**——capacity provider に出す
  `AcceleratorTotalMemoryMiB.Min`（＝どのインスタンスを買うかの絞り込み）と、決定 6 の適合判定が
  モデルの要求と比べる相手。カードの公称値より**わざと低く**宣言する（既存の 21000 が
  L4 24 GB に対してそうであるように）。判定は早めに警告する側へ倒れる。
- **`usdPerHour` は任意・表示専用**。EC2 にも Pricing API にも訊かない（ADR 0045 決定 21 の
  踏襲——IAM を増やし、ラベルのためにスタックを更新することになる）。**空でよい**。空なら
  金額を出さず、「クラスによって時間単価が変わります」とだけ言う（**嘘の数字より無い方がよい**）。
- **`vcpu`・`mem` の範囲も段が持つ**。48 GB のカードは 32〜64 GiB のインスタンスにしか居ないので、
  1 本の広い範囲では上の段が買えない。
- 段が 1 つも宣言されていない配備では、この機能は**存在しない**（決定 3）。
- 🔴 **出荷時の既定は「空」**。上の例は提案であって実測ではない（未解決 3）。梯子に既定値を
  焼き込むのは、g6e の VRAM・価格・在庫をこの配備で確かめた後（P1）にする。**測っていない値を
  既定にすると、それは「運用者が宣言した」という決定 1 の前提ごと嘘になる。**
- **クォータを超える段を宣言時に弾くことはしない。** CP は G 系 vCPU クォータを読めない
  （読むには `service-quotas` の IAM を足すことになり、決定 1 が避けた形そのものである）し、
  クォータは配備ごとに違う（本番は 96、開発と af-sandbox は 8）。買えない段を選んだときは
  サービスイベント（`VcpuLimitExceeded`）がそう言い、パネルはそれをそのまま見せる。

⚠️ **`AllowedInstanceTypes` を広くして VRAM の下限だけで段を切り替える案は採らない。** 動きは
するが、「条件に合う中で最安が買われる」規則に寄りかかることになり、AWS が新しい安い 48 GB の
型を出した日に**黙って別のインスタンス**になる。段は「要求の組」であって「下限 1 つ」ではない。

### 2. 選択は保存された設定が勝ち、スタックの宣言は既定（SEED）である

ADR 0072 決定 7 と同じ規則にする。`engine_<key>_class` を設定に持ち、
**スタックが宣言する既定の段（梯子の先頭、または明示の既定 id）は「まだ選んでいない」ときだけ効く。**
CP を再起動しても、スタックを更新しても、管理者の選択は上書きされない。

これは 0072 が「モデルを CFN パラメータに戻さない」ために置いた規則そのものであり、同じ理由で
必要になる——**管理者が 48 GB に上げた翌朝、無関係なスタック更新が黙って 24 GB に戻す**のが
最悪の壊れ方だからである（戻ったことは、次のコールドスタートで CUDA が落ちるまで誰にも見えない）。

### 3. 未宣言・未選択の配備では、CP は `UpdateCapacityProvider` を一度も呼ばない

梯子が空、または選択が既定と同じで**一度も適用したことがない**なら、CP は capacity provider を
**読みも書きもしない**。既存の配備は挙動が 1 ビットも変わらず、IAM を足していない配備で
`AccessDenied` のログが出ることもない。

### 4. 段の変更が効くのは「次に買うインスタンス」から。切り替えは 停止 → 退場を待つ → 起動

SDK の言う通り、走っているインスタンスは変わらない。したがって導線は次の 3 段階で、**画面はこの 3 段階を
そのまま見せる**（「変更しました」だけ出して古いインスタンスで走り続けるのが、最も高くつく嘘である）。

1. 段を保存する。エンジンが走っていなければ、これで終わり。次の起動から新しいインスタンス。
2. 走っているなら「**いま入れ替える**」を明示的に押させる。押すと desired 0。
3. 🔴 **container instance が消えるまで待ってから起動する。** 待たずに起こすと、退場中の
   4 vCPU と新しい 8 vCPU が並んで **クォータを超え、`VcpuLimitExceeded` で*静かに配置
   されない*** ——サービスのイベントを読むまで、ただ「起動が遅い」ようにしか見えない。
   退場の実測は 427〜477 秒（image 役でサービス 0 からインスタンスが消えるまで約 8 分）。

**待つのは、余裕のある配備でも同じである。** G 系 vCPU クォータは配備ごとに違い（本番は 96、
開発と af-sandbox は 8）、**CP はそれを読めない**（決定 1 と同じ理由で `service-quotas` の IAM を
足さない）。読めない数に条件分岐を書くと、**分岐が間違っている配備でだけ静かに壊れる**。
両方で安全な順序は 1 つしかないので、それを既定の振る舞いにする。代償は壁時計だけで、
**課金の重なりは生じない**（旧インスタンスが消えてから新インスタンスを買う）。

⚠️ **入れ替えはコールドスタートを 1 回買う**（llm で 527〜586 秒、image で 165〜197 秒の実測）。
ローカルストレージ（instance store）なので**新しいインスタンスはモデルを S3 から取り直す**——実測は
6.62 GiB を 66 秒（102.7 MiB/s）。切替 1 回の壁時計は退場とあわせて 15〜20 分、GPU の実費は
g6.xlarge 換算で $0.3〜0.4 程度になる。これは画面に書く。ADR 0071 の `offGrace: 0`
（気が変わる代償はコールドスタート）と同じ立場である。

⚠️ **段を保存しただけ・エンジンが止まっている**なら、ここまでの費用は一切かからない。
次に誰かが使ったときのコールドスタートに吸収される。

**P1 の実測（2026-09-10・image 役）**:

- **退場は 157 秒**（`DEREGISTERING` は 133 秒）。ADR 0071 の 427〜477 秒より**ずっと速い**。
  後片付けの 2 回目も 149 秒で、この配備では 150 秒前後とみてよい。20 分の上限は妥当だが、
  **根拠は「実測の 8 倍」ではなく「実測の 8 倍だった」に変わった**。
- 🔴 **門を迂回すると、まず起きるのはクォータ超過ではなく「古いカードへの再着地」**である。
  退場を待たずに `update-service --desired-count 1` を叩いたら、タスクは**居残っている
  前の段のインスタンスにそのまま置かれた**（64 秒で `steady state`）。provider が新しい段を要求して
  いても、それは**何を買うか**の条件であって**どこに置くか**の条件ではない。エラーは
  サービスイベントにもログにも出ず、パネルは「稼働中」と言う。**決定 4 が無ければ、
  段を上げたつもりで古いカードの上で走り続ける**——これが最も高くつく嘘である。
- 陽性対照（`VcpuLimitExceeded`）は**古いインスタンスが塞がっている**必要があった。前の段のインスタンスで
  タスクが走っている状態で 2 台目を要求すると、期待どおり出る:
  `VcpuLimitExceeded: ... current vCPU limit of 8 ... for the instance bucket`。
  P1 の実験計画が「待たずに起こせば出る」と書いていたのは**誤り**で、空いているインスタンスが
  居るあいだ ECS はそもそも 2 台目を買おうとしない。
- 🔴 **待っても買えない時間がある。** 4 vCPU のインスタンスが ECS から消えた直後に 8 vCPU の段を
  買おうとすると、**5 分以上 `VcpuLimitExceeded` が続いた**（09:19:43 に消え、09:19:50 /
  09:20:30 / 09:21:11 に失敗、成功は 09:26:12＝389 秒後）。**EC2 のクォータ解放は ECS の
  登録解除より遅い。** CP が見ている「インスタンスが居るか」は EC2 の会計とは別物である。ECS が
  再試行するので壊れはしないが、**段を上げるときの起動は、退場 150 秒＋クォータ待ち
  最大 6 分＋コールドスタートになる**——`StartDeadlineSec` はその合計を超えている必要が
  ある（実測 497 秒 / 既定 900 秒）。4→4 の入れ替えでは起きない。

**実装で足したもの（P0）**:
- 待ちは**インスタンスの EC2 タイプ**で判定する。container instance の `ecs.instance-type` 属性が、
  MI のインスタンスの型を CP が読める唯一の場所である（`ec2 describe-instances` には出ない）。
  選択中の段に含まれない型のインスタンスが居る＝前の段のインスタンスがまだ居る。
- 🔴 **待ちには上限（20 分）を置く。** 待ちの終わりは AWS のもので、`scaleInAfter: -1`
  （片付けない）なら**永久に起動できないエンジン**になり、証拠はログ 1 行しか残らない。
  実測の退場は 427〜477 秒なので、20 分は遅い退場を待ち切ってなお諦める。
- 入れ替えは**モードを触らない**専用の操作（`POST …/replace-box`）にする。「無効」を押させると
  モードが off のまま残り、戻し忘れがインスタンスの停止と区別できない ADR 0071 の罠を再生産する。
- 🔴 **管理 API の `mode=on` も同じ門を通す。** ここは `setEnabled(true)` を直接呼ぶ経路なので、
  通さないと**ボタン 1 つで待ちを迂回して古いインスタンスを買う**。門が止めたときは要求を失敗させず、
  モードだけ保存してコントローラに任せる（意図は記録され、インスタンスが消えたら起動する）。

### 5. 適用は Describe → 写す → Update の read-modify-write

`InstanceLaunchTemplate` は必須メンバーなので、CP は現在の設定を
`DescribeCapacityProviders` で読み、**`InstanceRequirements` の 4 項目
（`AllowedInstanceTypes`・`AcceleratorTotalMemoryMiB.Min`・`VCpuCount`・`MemoryMiB`）だけを
差し替えて**書き戻す。ネットワーク・ストレージ・インスタンスプロファイル・GPU の指定
（`AcceleratorCount`・`AcceleratorTypes`・`AcceleratorManufacturers`）は**読んだ値をそのまま
返す**。CP はインスタンスの設計を持たない——持たせると、スタックが持つ設計と 2 つになる。

**適用する時点は 2 つ**——段を保存したときと、**起動を決める直前（冪等）**。後者が要るのは、
CloudFormation が自分の宣言に戻すからである（スタック更新は CP を置き換えないので、決定 2 の
「保存された選択が勝つ」だけでは、戻された状態のまま次のインスタンスが買われる）。

この再適用に**追加の費用はかからない**。`UpdateCapacityProvider` はインスタンスを買う API ではなく
「次に買うインスタンスの仕様」を書き換えるだけで、ECS の API 呼び出しは無料であり、同じ値の書き戻しは
走っているインスタンスにも desired にも触らない。代償は起動経路に呼び出しが 1 本増えることだけである。

🔴 **再適用が失敗したときの規則**——**このプロセスが最後に適用した段と、選ばれている段が違う
なら、起動しない**（そのまま起こすと、選ばれた段のつもりで古いインスタンスを買い、重いモデルなら
CUDA が落ちて**コールドスタート 1 回ぶんを捨てる**）。同じなら、失敗をログに残して起動する
（インスタンスの仕様は既に正しい）。

**P1 の実測（2026-09-10）**:

- ✅ **RMW は忠実だった。** 段を `l4`→`l40s` に変えたとき、動いたのは 4 欄のうち 3 つ
  （`allowedInstanceTypes` `g6.xlarge`→`g6e.xlarge`、`acceleratorTotalMemoryMiB.min`
  8000→44000、`memoryMiB.min` 15000→30000。`vCpuCount` は両段とも 4-8）だけで、
  `acceleratorCount` `acceleratorTypes` `acceleratorManufacturers` `burstablePerformance`
  `ec2InstanceProfileArn` `localStorageConfiguration` `networkConfiguration`
  `instanceMetadataTagsPropagation` は**1 つも動かなかった**。
- ✅ **運べない欄は消えない**（未解決 1）。`capacityOptionType` は `ON_DEMAND` のまま。
  同値の書き戻しでは `managedInstancesProvider` 全体が前後で完全一致した。
  🔴 **これは陽性対照の無い観測だった**（`ON_DEMAND` は既定値）。同日の追試で非既定値
  `SPOT` を使って測り直し、結論自体は正しいことを確認した——「追試」節。
- ✅ **CFN が戻した provider を、起動直前の再適用が実際に直した。** ドリフトを作った状態
  （選択は `l40s`、provider は既定）から起こしたところ、CP は起動前に `l40s` を書き直し、
  **買われたインスタンスは `g6e.xlarge` だった**。決定 5 の後半は実機で効いている。
- 🔴 **実装が間違っていた点**: `DescribeCapacityProviders` に `Cluster` と
  `CapacityProviders` を**同時に渡していた**。ECS はこれを拒否する
  （`InvalidParameterException: Cannot specify both capacity providers and cluster in the
  same request`）。API リファレンスにも SDK の doc コメントにも書かれていない制約で、
  **単体テストが全部通っていたのは fake が何でも受け取っていたから**である。名前だけで
  引き、**cluster は答えの側で検査する**形に直した（別のクラスタの provider には書かない）。

**実装で分かったこと（SDK v1.87.0 を読んで写した結果）**:
- ✅ **`InstanceRequirements` は読みと書きで同じ型**（`InstanceRequirementsRequest`）だった。
  起票時に危険視した「要求の写し漏れ」はここには無い——読んだ構造体をそのまま持ち、
  4 欄だけ差し替える。
- 🔴 **危険は 1 つ上の階層にあった。** `InstanceLaunchTemplate`（読み）と
  `InstanceLaunchTemplateUpdate`（書き）は**別の型**で、**`CapacityOptionType`（ON_DEMAND /
  SPOT）と `FipsEnabled` は書きの型に存在しない＝運べない**。ECS が保持するのか既定に
  戻すのかは文書化されておらず、実機で確かめていない（未解決 1）。決定 8 のテストは
  「運べない欄」を名前で列挙させ、**黙って消えることだけは起きないようにしている**。
- **VRAM の下限は加速器を要求している役でしか書けない**（`AcceleratorTotalMemoryMiB` は
  加速器の指定なしでは拒否される・0071 の実測）。段が 0 を宣言したときと CPU 役では下限を
  **消す**（0 を要求すると誰も通らない条件になる）。

### 6. 警告は「収まらないかもしれない」を指す。拒否はしない

判定は **max(有効なモデルの VRAM 要求) vs 選択中の段の `vramMiB`**（和ではない——決定の根拠は
背景の `LlmModelsMax: 1` と sd-server の 1 チェックポイント）。image 役では「選択中の 1 つ」。

モデルの VRAM 要求は 3 段階で、**どれで答えたかを画面に書く**。

| 出所 | 何を言えるか |
|---|---|
| `vram_mib` が宣言されている | 運用者の実測。そのまま比べる |
| 宣言が無く、ファイルの `bytes` がある | **重みだけの下限**。KV キャッシュもコンテキストも含まない。「少なくとも N GiB」としか言わない |
| どちらも無い | **不明**。「収まります」とは言わない |

🔴 **「不明」を「大丈夫」と描かない**ことが、この決定の中身である。0 は「0 MiB 必要」ではない。

出す場所は 3 つ:

- モデルを**有効にするとき**（`PUT /models/{id}` の `enabled: true`）——ここが唯一、人が
  選択している瞬間である。収まらない見込みなら確認を求め、**そこから段を変えられる**。
- **エンジンのカードに常時**——有効なモデルの中に段を超えるものがあるなら、そう書く。
- **起動を決めたとき**にログへ 1 行。CUDA の落ち方は診断可能な形をしていないので、
  「この起動は 44,000 MiB の段に 47,000 MiB を載せようとしている」が事前に残っている必要がある。

**拒否はしない。** ライセンスの扱い（0072 決定 10「パネルは指すのであって決めない」）と同じで、
量子化・`--offload-to-cpu`・こちらが知らない事情で載ることはある。ただし**確認は求める**。

**P1 の実測（2026-09-10）**: 3 つの出方をすべて実 CP 相手に確認した——カードの常時表示、
起動時のログ 1 行（`starting on l40s (44000 MiB VRAM declared); largest model
juggernaut-xl-v9 wants 6776 MiB (floor)`）、そして `flux1-dev`（22,700 MiB）を 8,000 MiB の
段で有効化しようとしたときの確認ダイアログ。

🔴 **「重みだけの下限」は、重みとしては正確でも実使用の 58% しか説明しない。**
juggernaut-xl-v9 の宣言下限 6,776 MiB に対し、sd.cpp が報告した実際は
`total params memory size = 6624.11MB`（重みは 2% 以内で当たっている）だが、auto-fit が
別に確保した**計算用の予備が DiT 2,048 + Conditioner 2,048 + VAE 1,024 = 5,120 MiB** あり、
実使用は約 **11.7 GiB** だった。つまり**下限を「これなら載る」と読むと約 5 GiB 足りない**。
未解決 7 の最初の実測値であり、「下限としか言わない」という決定は正しかったことになる。

### 7. 既定と違う段で走っていることは、常に画面に出る。戻すのは 1 クリック

「一時的に大きいインスタンスを試す」の**代償は、戻し忘れが時間単価に効き続けること**である。ADR 0071 の
`off` が「一時停止ではなく永続設定」で、戻し忘れがインスタンスの停止と見分けられなかったのと同じ形をした
罠なので、同じ手当てをする——**スタックの既定と違う段のときはエンジンのカードにバッジを出し、
「既定（{id}）に戻す」を隣に置く。**

あわせて **i18n の `$1.26/時` 直書き（`admin.engines_always_on_note`、日英とも）を消す**。
金額は梯子の宣言値から出す。宣言が無ければ金額を言わない。

### 8. 写し漏れは、SDK が欄を増やした日に**テストが落ちる**ようにする

決定 5 の read-modify-write は、欄を 1 つ写し忘れると**その制約が黙って消える**
（例えば `BurstablePerformance: excluded` を落とせば、GPU の要求は残るので気づかないまま
条件が緩む）。SDK は上流の更新で欄が増える。

したがって、**`InstanceLaunchTemplate` の全フィールドをリフレクションで走査し、
(a) 写し取り側が値を落としていれば落ちる・(b) 書きの型に存在しない欄は「運べない」として
名前で列挙されていなければ落ちる**単体テストを置く（`wiremap_convert_test.go` が
`was: map[string]any{…}` を grep して等価性の証明を要求しているのと同じ仕掛け）。
欄が増えた日に、気づくのは人ではなくテストである。**実装時点で列挙されているのは
`CapacityOptionType` と `FipsEnabled` の 2 つ**（決定 5 の追記）。

⚠️ 陽性対照を取ってある——写しから 1 行（`NetworkConfiguration`）を消すとこのテストは実際に
落ちる。落ちないテストを「通った」と読まないこと。

### 9. IAM は 2 つの capacity provider に限定する

60-engines の `CpIngestPolicy` の隣に、同じ形（このスタックの資源に限定）で足す。

- `ecs:DescribeCapacityProviders` / `ecs:UpdateCapacityProvider` — Resource は
  このスタックが作る llm・image の capacity provider の ARN**だけ**。
- 🔴 `ecs:PutClusterCapacityProviders` — **クラスタの ARN**（2026-09-10 の P1 で追加）。
- `iam:PassRole` — `InfraRole` と `InstanceProfile` のロール**だけ**、
  `Condition: {StringEquals: {"iam:PassedToService": "ecs.amazonaws.com"}}` つき。

🔴 **3 つ目は、この ADR が P1 まで知らなかった権限である。** ECS は **MI の provider に
対する `UpdateCapacityProvider` を、クラスタ側の `PutClusterCapacityProviders` としても
認可する**。CP はその API を一度も呼んでいないので、コードを grep しても出てこない:

```
AccessDeniedException: User: .../af-<stack>-cp-task/... is not authorized to perform:
ecs:PutClusterCapacityProviders on resource: .../cluster/af-<platform-stack>
```

⚠️ **これは「2 つの capacity provider に限定する」という見出しを部分的に裏切る。** 他の 3 つが
provider スコープなのに対し、この 1 つは**クラスタスコープ**であり、同じ動作で**クラスタの
provider 関連付け一覧ごと差し替えられる**。60-engines が元々その一覧を所有していること
（README「⚠️ It owns the cluster's capacity-provider associations」）が、これを許す唯一の
根拠である。**限定できないなら書かない**という選択肢は無かった——書かないとこの機能自体が
動かない。

⚠️ **これは CP の権限を広げる決定である。** 決定 5 で書き戻す内容は「読んだものに 4 項目だけ
上書き」だが、API としては起動テンプレートを再宣言できる（サブネットやセキュリティグループを
含む）。だから資源を 2 つに限定することと、CP がこの API を**梯子に宣言された段の値でしか
呼ばない**ことの両方が必要である（段は運用者が宣言したものだけ＝任意の値を書けない）。

### 10. 稼働時間の表（`engine_hourly`）は今回触らない

主キーが `(engine_key, hour)` なので、時間の途中で段が変わると 1 行に 2 つのインスタンスが混ざる。
正しくするには主キーを変えることになり、それは占有率の表の話であって費用の表の話ではない
——**請求の正は Cost Explorer のタグ別（ADR 0048 決定 15、`af-role: engine-llm`）**であり、
そちらは段が変われば自動的に追随する。段の変更は**監査ログ**（`engine.<key>.class`）に残す。

「どの時間にどのインスタンスで走っていたか」を後から言う必要が出たら、その時に主キーを含めて設計する
（未解決 5）。

### 11. タスク定義（`Cpu` / `Memory` / `--models-max`）は段に追随させない

段を上げても、タスクは `LlmTaskCpu 4096` / `LlmTaskMemory 14336` のままにする。理由は 2 つ:

- **必要が確かめられていない。** 18.5 GB のモデルが 14,336 MiB のタスクで載っている（0071 の
  実測）。ホスト RAM は重みに比例していない。
- 追随させるには CP が `RegisterTaskDefinition` でコンテナ定義を再構築することになり、
  **タスク定義の設計がスタックと CP の 2 か所**に生まれる（決定 5 で避けたのと同じ形）。

破れるときの兆候は書いておく——**タスクがインスタンスに置けない**（`RESOURCE:MEMORY` のイベント）か、
**コンテナが OOM で死ぬ**。どちらかが出たら、それは段ではなくタスク定義の話であり、
CloudFormation で直す（未解決 6）。

## 却下した案

- **役ごとに段の数だけ capacity provider を CFN で並べ、サービスの
  `capacityProviderStrategy` を差し替える。** IAM を増やさずに済む（CP は `UpdateService` を
  既に持つ）ので、起票時はこれを容量の壁だけで却下していた。🔴 **その理由は間違っている**
  ——`deploy/aws/ecs/env.sh` の `af_cfn_deploy` は 51,200 バイトを超えると S3 経由へ切り替える
  ので、越えること自体はできる（30-ingress が 54,681 バイトで実際に通っている）。
  **本当の却下理由は決定 5 が成立しないことである**: ドリフトに備えて起動の直前に毎回
  適用し直すとき、capacity provider への書き戻しは**何も動かさない**が、service の
  `capacityProviderStrategy` の更新は**新しいデプロイを起こしてタスクを置き換える**。
  この案では「冪等に適用し直す」が「生成中の要求を殺す」と同義になる。
  （容量の壁は別の形で残っている——`deploy/local/ecs-lifecycle-stub-test.sh` の 3b-2 が
  出荷テンプレートを 51,200 バイト以内に保つので、実装では散文を
  PARAMETERS-60-engines.md へ移して枠を作った。）
- **CloudFormation パラメータのままにして、Console は警告だけ出す。** IAM も増えず、ドリフトも
  無い。却下の理由は 0072 が既に書いている——**差し替えを CFN 更新にすると、運用の齟齬に当たる**
  （GPU が寝ている夜に管理者が押すものであって、配備担当者が朝に押すものではない）。
  「一時的に試す」が毎回スタック更新なら、誰も試さない。
- **`ec2:DescribeInstanceTypes` で VRAM と vCPU を引く。** 権威はあるが、CP task role に
  IAM アクションを足し、ラベルのためにスタックを更新することになる。**梯子を書く運用者は
  その数字を既に知っている**（ADR 0045 決定 21 をそのまま踏襲）。
- **CP がモデルに合わせて段を自動で選ぶ。** 金額が黙って上がる。選ぶのは人である
  （決定 7 のバッジと同じ理由）。
- **段の変更で走っているインスタンスを自動で入れ替える。** 生成中の要求を殺す。0072 の
  「選び直しは次の起動から効きます。走っているエンジンは入れ替えません」と同じ立場を取り、
  入れ替えは明示のボタンにする（決定 4）。

## 未解決の点（測ってから決める）

1. ✅ **解決（2026-09-10・P1 で実測）。運べない欄は保持される。** `capacityOptionType` は
   `ON_DEMAND` のまま残り、同値の書き戻しでは `managedInstancesProvider` 全体が前後で
   完全一致した。決定 5 に手当ては要らない。
   ⚠️ **`fipsEnabled` はこの配備が一度も設定していないので、直接は測れていない**——
   同じ穴（書きの型に存在しない）に落ちる兄弟の欄が保持された、という推定である。既定が
   false なので、黙って戻されても「元から false」と区別がつかない。FIPS を使う配備で
   段を切り替える前には、ここだけもう一度確かめること。
   写しが嵌るか（型が別）も同時に解決した——嵌る。ただし**`Describe` の呼び方が別に
   間違っていた**（決定 5 の P1 実測を参照）。依存: 決定 5・8。
   🔴 **追試（2026-09-10・同日）で、この「実測」が実は同じ推定だったことが分かった。**
   `ON_DEMAND` は `capacityOptionType` の**既定値**なので、上で `fipsEnabled` について書いた
   「黙って戻されても元から false と区別がつかない」がそのまま当てはまる——**兄弟の欄の欠陥に
   気づきながら、本人の欄に同じ物差しを当てていなかった。** 非既定値で測り直して保持を確認済み。
   `fipsEnabled` の宿題も、東京では**設定そのものができない**ため消える。「追試」節を参照。
2. ✅ **解決（2026-09-10・P1 で実測）。ドリフトは 2 通りあり、片方は起きない。**
   - **素の再 deploy（provider の性質が 1 つも変わらない）では戻らない。** 空の changeset に
     なり、CloudFormation は provider を読みもしない（1.9 秒）。つまり「無関係なスタック更新が
     翌朝こっそり戻す」という起票時の心配は**起きない**。
   - **provider に触れるパラメータを 1 つでも動かすと、`InstanceRequirements` は全体が宣言に
     戻る。** `ImageMemMinMiB` だけを +1 した更新で、`allowedInstanceTypes` も VRAM 下限も
     巻き添えで既定に戻った（48 秒）。**部分的ではなく全部**である。
   - 起動直前の再適用が、その状態を実際に直した（決定 5 の実測）。
   - **残る窓**は「CP が適用してから ECS がインスタンスを買うまでの間に CFN が戻した場合」だけで、これは
     CP に閉じられない（買うのは非同期）。実長は CFN の更新時間（実測 48 秒）に依存する。
   依存: 決定 2・5。
3. ✅ **解決（2026-09-10・P1 で実測。ただし出荷時の既定は空のまま）。**
   - **在庫**: `g6e.xlarge` / `g6e.2xlarge` はどちらも ap-northeast-1a と 1c にある。
   - **VRAM**: L40S の実測は **45,457 MiB**（`ggml_cuda_init` の報告）。梯子に書いた 44,000 は
     「公称より下」の作法どおりで妥当だった。
   - **価格**（Pricing API・東京・Linux・オンデマンド、2026-09-10）:
     `g6.xlarge` $1.1672 / `g5.xlarge` $1.4590 / `g6e.xlarge` $2.6990 / `g6e.2xlarge` $3.2517。
   - 🔴 **梯子に書くべき数字は Pricing API の値ではない。** ADR 0071 が実測した g6.xlarge の
     **$1.26** は list price より **8% 高い**。差は ECS Managed Instances の管理料と整合する
     （EC2 の料金＋MI の管理料が請求される）。**list price をそのまま書くと請求より安く見える。**
     係数を 1 点から一般化はできないので、**梯子には「実測があるものは実測、無いものは
     書かない」**を勧める。Cost Explorer の翌日確定値でしか埋まらない項目が残っている。
   依存: 決定 1。
4. **G 系 vCPU クォータの引き上げが要るか → 解決（2026-09-10・レビューで運用者が回答）。**
   本番配備のクォータは 96 なので、8 vCPU を超える段も買える。**宣言時に弾く案は採らない**
   （決定 1 の最終項）——CP はクォータを読めず、配備ごとに違う。開発と af-sandbox は 8 のままな
   ので、退場待ち（決定 4）はどの配備でも既定の振る舞いにする。残るのは P1 で af-sandbox の
   8 の中で何が実測できるか、という実験計画の問題である。
5. 「どの時間にどのインスタンスで走ったか」を `engine_hourly` に持たせるか（決定 10）。
6. **重いモデルでタスクの `Memory` が足りるか**（決定 11）。18.5 GB では足りた、で止まっている。
7. ~~**VRAM 要求の見積り式。**~~ **llm 役は解けた（2026-09-11・末尾の補遺「未解決 7 の llm 役」）——
   KV は係数ではなくヘッダから計算でき、実機と 2 点とも差 0.00%。image 役は補遺のとおり別問題。**
   決定 6 の第 2 段は重みの合計バイトだけで、KV キャッシュを
   含まない。係数を書くには測るしかない（コンテキスト長・層数・量子化に依存する）。
   **測るまでは「下限」としか言わない。**
   **最初の実測（2026-09-10・P1）**: SDXL 系（juggernaut-xl-v9・fp16）で、宣言下限 6,776 MiB /
   重みの実際 6,624 MiB / **計算用予備 5,120 MiB**（DiT 2,048・Conditioner 2,048・VAE 1,024）/
   実使用 約 11.7 GiB。**下限は実使用の 58%。** 予備は解像度とバッチに依存するはずなので、
   1 点から係数は作らない。llm 役（KV キャッシュが効く側）は未測のまま。

## P1 の実験計画（クォータ 8 のまま・2026-09-10）

運用者の判断で **G 系 vCPU クォータは 8 のまま**行う。引き上げないことは制約であると同時に
**設計の検査でもある**——8 は「開発配備の普通の状態」であり、そこで壊れる機能は本番でも
別の形で壊れる。以下は 8 という数がこの実験に何をするかを先に書いたものである。

### 8 vCPU が許すもの・許さないもの

| 組み合わせ | 合計 vCPU | 8 で成立するか |
|---|---|---|
| g6.xlarge（L4 24GB・4） 1 台 | 4 | ○ 通常運転 |
| g6.xlarge 退場中 ＋ g6e.xlarge（L40S 48GB・4）新規 | 8 | ○ **ちょうど上限**（＝待たなくても買える） |
| g6.xlarge 退場中 ＋ g6e.2xlarge（8） | 12 | ✕ `VcpuLimitExceeded`（**決定 4 の positive control はこれ**） |
| llm と image の両方を同時に上の段へ | 8 超 | ✕ **1 度に 1 役しか試せない** |

⚠️ ここから 3 つ出る。

1. **上の段を 4 vCPU の型（g6e.xlarge）にすれば、クォータ 8 でも実験は最後まで通る。**
   引き上げ申請は要らない。
2. **決定 4（退場を待つ）の必要性は、4→4 の切り替えでは証明できない**——合計 8 でちょうど
   収まってしまうから。必要性を示すには **8 vCPU の段（g6e.2xlarge）を「新しい段」に選び、
   待たずに起こす**（AWS CLI で直接 `update-service --desired-count 1`）。これが陽性対照で、
   ここで `VcpuLimitExceeded` が出ないなら**測っているものが違う**。
3. **実験は 1 役ずつ。** image 役で行う（コールドスタート 165〜197 秒に対し llm は 527〜586 秒
   で、同じことを 3 倍以上の時間と費用で測ることになる）。

### 段の候補と、g6e が無かったときの代替

```
ImageInstanceClasses=l4|L4 24GB|21000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26;
                     l40s|L40S 48GB|44000|g6e.xlarge|4-8|30000-65536
```

🔴 **`g6e.xlarge` がその配備のリージョンに無い可能性は潰していない**（未解決 3）。
無かった場合、`InsufficientInstanceCapacity` か「条件に合う型が無い」で**インスタンスが来ないだけ**で、
機能の失敗と区別がつかない。**先に `aws ec2 describe-instance-type-offerings
--location-type availability-zone --filters Name=instance-type,Values=g6e.xlarge` で在庫を
確かめてから**梯子に書くこと。

⭐ **仕組みの検証にカードの大きさは要らない。** g6e が無い／高いなら、上の段を
**`g5.xlarge`（A10G 24GB・4 vCPU）**にする。VRAM は同じでも**型は違う**ので、
「段の適用」「インスタンスの型が変わる」「退場待ち」「ドリフト」は全部そのまま測れる。
**48GB でしか確かめられないのは決定 6 の警告が実際に守った、という一点だけ**であり、
それは他のすべてが通ったあとで足せばよい。

### 順番（この順でしか通らないところがある）

1. **60-engines を先に更新**（`Image/LlmInstanceClasses` と IAM）。
2. **CP を入れ替える**（このブランチの control-plane 像）。エンジン表は**起動時に一度しか
   読まれない**ので、`classes` を足しただけでは製品のどこにも存在しない
   （ADR 0071 の「2 パス目のあと CP を再起動しないと」と同じ罠）。
3. Console を開き、**梯子が出ること**を確認する（出なければ 1 か 2 が届いていない）。
4. 以下を測る。

### 測ること（未解決の番号つき）

| # | 測ること | 見る場所 / 判定 |
|---|---|---|
| 1 | 🔴 **未解決 1**: `CapacityOptionType` と `FipsEnabled` は保持されるか消えるか | 段の切替の**前後**で `describe-capacity-providers` を取り diff。消えていたら決定 5 に「運べない欄は明示的に書き戻す」を足す必要がある（現状は運べない） |
| 2 | RMW が実際に嵌るか（型が別なので写している） | 切替が `200` を返し、`instanceRequirements` が梯子どおりで**他の欄が全部残っている** |
| 3 | **IAM が足りているか** | 切替が `AccessDenied` にならないこと。PassRole 不足はここでしか出ない |
| 4 | 🔴 **未解決 2**: CFN ドリフト | 切替後に 60-engines を `deploy` し直し、provider を describe → 戻っているか。戻っていたら**次の起動前に CP が再適用して直すこと**を、起動を 1 回起こして確認する |
| 5 | **決定 4 の退場待ち** | 「いま入れ替える」→ container instance が消えるまでの秒数（期待 427〜477 秒）。その間 CP が起動しないこと（ログ `holding the start back (class_swap_wait…)`） |
| 6 | **決定 4 の positive control** | 8 vCPU の段を選び、**待たずに** `update-service --desired-count 1`。`VcpuLimitExceeded` がサービスイベントに出ること。**出ないなら実験が間違っている** |
| 7 | 新しい段でのコールドスタート | インスタンスの型が変わっていること（`describe-container-instances` の `ecs.instance-type`）、待受までの秒数、モデル取得の秒数 |
| 8 | 未解決 3（既定値） | 上の段の**実際の時間単価**（Cost Explorer のタグ別、翌日）と在庫。梯子の `usdPerHour` に入れる |
| 9 | パネル | バッジ「既定と違います」・入れ替え導線・VRAM 警告が**実 CP 相手に**出ること（P0 はスタブ CP で描画確認済み） |

### 中止条件と費用

- **費用の見当**: 4 vCPU の GPU が 1 台、合計 2〜3 時間で **$3〜5 程度**（g6.xlarge $1.26/時
  ・上の段は未確認）。⚠️ **インスタンスは「止めた」と「消えた」が違う**——サービス 0 から約 8 分は
  課金が続く。測り終わったら **mode を off にし、container instance が消えるまで見届ける**。
- **中止条件**: (a) 3（IAM）が通らない → 先に 60-engines の再デプロイを疑う、
  (b) 上の段のインスタンスが 15 分来ない → 在庫か型の綴りを疑い、`g5.xlarge` の代替へ落とす、
  (c) 6 の陽性対照が出ない → **実験の組み方を疑う**（クォータの実値を
  `service-quotas get-service-quota --service-code ec2 --quota-code L-DB2E81BA` で確かめる。
  🔴 **起票時に書いた `L-DB2E81BB` は誤り**——それは Spot 用（この配備では 0）。On-Demand の
  G/VT は `L-DB2E81BA` で、af-sandbox の実測値は 8 だった）。
  🔴 **この訂正のほうも誤り**（同日の追試）——`L-DB2E81BB` は**存在しない**（両アカウントで
  `NoSuchResourceException`）。Spot は `L-3819A6DF`。「追試」節を参照。
- **後片付け**: 段を既定へ戻す（Console の「既定に戻す」）・mode を off・
  梯子を空に戻すかは運用者の判断（空に戻すと機能ごと消える）。

## P1 の実測（2026-09-10・af-sandbox・image 役）

実験計画のとおり image 役だけで、クォータ 8 のまま行った。**GPU は 100 分・$4.6**
（`g6e.xlarge` 90.5 分＝$4.07、`g6e.2xlarge` 10.2 分＝$0.55。計測そのものは 13 分で、
残りは人の確認を待つあいだインスタンスが遊んでいた時間である）。記録は
[docs/log/95-engine-instance-classes.md](../log/95-engine-instance-classes.md)。

| # | 測ったもの | 結果 |
|---|---|---|
| 1 | 未解決 1（`capacityOptionType` / `fipsEnabled`） | **保持される**。`fipsEnabled` は未設定のため推定 |
| 2 | RMW が嵌るか | **嵌る**。4 欄だけが動き、他の 8 欄は 1 つも動かない |
| 3 | IAM が足りているか | 🔴 **足りていなかった**。`PutClusterCapacityProviders` が要る（決定 9） |
| 4 | 未解決 2（CFN ドリフト） | **素の再 deploy では戻らない／provider に触る更新では全部戻る**。起動直前の再適用が直した |
| 5 | 退場待ち | **157 秒**（2 回目 149 秒）。門のログも出た（`class_swap_wait after admin_on`） |
| 6 | 陽性対照 | **出た**。ただし**古いインスタンスが塞がっている**必要があった（計画の想定は誤り） |
| 7 | 新しい段でのコールドスタート | 型は**実際に変わる**。1 回目 118 秒 / 2 回目 497 秒（差はクォータ待ち） |
| 8 | 未解決 3（既定値） | 在庫・VRAM・価格すべて取得。**list price は請求より 8% 低い** |
| 9 | パネル | バッジ・入れ替え導線・VRAM 警告・確認ダイアログを実 CP 相手に確認 |

### 実機でしか出なかった不具合 2 件

どちらも **P0 の単体テストは全部緑のまま**だった。

1. **`DescribeCapacityProviders` に cluster と名前を両方渡していた。** ECS は拒否する
   （`InvalidParameterException: Cannot specify both capacity providers and cluster in the
   same request`）。API リファレンスにも SDK の doc コメントにも無い制約で、**fake が
   何でも受け取るテストは永久に気づかない**。fake を実 API と同じだけ厳しくして、既存の
   テスト全部を番人にした（陽性対照つき）。
2. **`ecs:PutClusterCapacityProviders` が要る**（決定 9 の訂正）。**CP が呼んでいない API 名**
   なので、コードからも権限表からも導けない。

**この 2 件が P1 の存在理由である。** どちらも「実装を読んで見つける」ことはできず、
1 回起こせば 30 秒で分かる。

### 決定 4 について、計画が間違っていたこと

実験計画は「待たずに起こせば `VcpuLimitExceeded` が出る」と書いていた。**出なかった。**
空いている前の段のインスタンスが居るあいだ、ECS はそもそも 2 台目を買おうとせず、**タスクを古いインスタンスに
そのまま置く**。つまり決定 4 が防いでいる一番手前の失敗は、クォータ超過ではなく
**静かな再着地**である——サービスは `steady state` と言い、パネルは「稼働中」と言い、
段を上げたつもりの管理者は古いカードの上で走り続ける。陽性対照は、古いインスタンスでタスクが
走っている状態で 2 台目を要求して取った。

そして **「インスタンスが消えるまで待つ」は必要条件だが十分条件ではない**。4 vCPU のインスタンスが ECS から
消えた後も **5 分以上** `VcpuLimitExceeded` が続いた（消滅 09:19:43 → 成功 09:26:12）。
**EC2 のクォータ解放は ECS の登録解除より遅い。** 段を上げる起動は
「退場 150 秒＋クォータ待ち最大 6 分＋コールドスタート」で見積もること。

### 直さなかったが分かっていること

- **適用に失敗した段は、同じ段を選び直しても再試行できない。** `putClass` は設定を保存して
  から適用するので、失敗しても選択は保存済みになり、Console は「変化なし」と見て要求を
  送らない。回復するには別の段へ移してから戻す。順序自体は意図どおり（適用できなかった
  ことを画面で言うため）なので、直すなら Console 側に再試行の口を足すことになる。
  → ✅ **同日に直した**（下の「追記 — 適用に失敗した段の再試行」）。
- **梯子の `usdPerHour` に list price を書くと請求より安く見える**（未解決 3）。

## 追試（2026-09-10・同日・GPU 課金 $0）

P1 の後、Spot への切り替えを検討する過程で 4 件を実測した。**GPU は 1 台も買っていない**——
live の provider には触れず、`af-spot-probe-0074` という**使い捨ての capacity provider を 1 本
作って**測り、消した。所要 5 分・$0・クラスタの provider 一覧は前後で完全一致。

- 🔴 **未解決 1 の「実測」は、実は推定だった。** P1 が見たのは `capacityOptionType` が
  `ON_DEMAND` のまま、という観測だが、**`ON_DEMAND` はこの欄の既定値**である。つまり
  「保持された」と「既定に戻された」を区別していない。同じ欠陥を `fipsEnabled` については
  文中で指摘していたのに、**その物差しを本人の欄に当てていなかった**。
  ✅ **非既定値で測り直した結果、保持される。** `capacityOptionType: SPOT` で provider を作り、
  `instanceLaunchTemplateUpdate`（＝運べない欄を落とした 8 欄）だけを渡す更新を **2 回**かけて、
  どちらも `SPOT` のままだった。**陽性対照**: 同じ更新で `allowedInstanceTypes`
  （`g6.xlarge`→`g6e.xlarge`）と `acceleratorTotalMemoryMiB`（8000→40000）は狙いどおり変わり、
  差分はその 2 欄だけ。更新が空振りしたのではないことが言える。決定 5 に手当てが要らない
  という結論は変わらない。
- ✅ **`fipsEnabled` の宿題は、東京では消える。** `fipsEnabled: true` で provider を作ろうとすると
  ECS が拒否する——`ClientException: Managed Instances Provider does not support FIPS in this
  region`。ap-northeast-1 では**設定そのものができない**ので、運べない欄が問題になるのは
  実質 0 件。他リージョンの配備では引き続き確認が要る。
- 🔴 **クォータコードの再訂正: `L-DB2E81BB` は存在しない。** 「P1 の実験計画」の中止条件 (c) と
  docs/log/95 は「`L-DB2E81BB` は Spot 用（値 0）」と*訂正*しているが、その訂正のほうが誤り。
  両アカウントで `NoSuchResourceException` が返る。ap-northeast-1 の G/VT クォータは
  **`L-DB2E81BA`（On-Demand）と `L-3819A6DF`（All G and VT Spot Instance Requests・既定 0）の
  2 つだけ**。**「値 0 が返った」は「Spot である」と「存在しない」を区別しない**——
  最初の 1 文字の誤りが、同じ形の推測で 2 度目の誤りを生んでいる。
- 🔴 **「本番のクォータは 96」は現況と違う。** acrt（production）の実測は **On-Demand 64・
  Spot 64**、af-sandbox は On-Demand 8・Spot 0（16 へ引き上げ申請中）。決定 1「クォータで宣言を
  弾かない」はこの数字に依存していないので、決定は変わらない。
- 副産物: **Spot クォータが 0 でも `capacityOptionType: SPOT` の provider は作れる。**
  クォータが効くのはインスタンスを起動する時だけなので、**設定側の検証はクォータ引き上げを待たなくてよい。**
- ⚠️ 削除した capacity provider は ECS が `INACTIVE` レコードとして残す（クラスタの一覧からは
  消える）。跡が残らないわけではない。

## Console のレビューで出た積み残し（2026-09-10）

`llamacpp` のパネルをこの ADR と突き合わせて読んで出た 2 件。どちらも決定を変えるものではなく、
P2 の作業である。

**1. 🔴 決定 6 の「下限」は重みしか数えていない。llm 役で入らない原因は KV キャッシュのほう。**
`engineModelVramNeed` は `floor` をファイルの `bytes` の合計として答える——**測っているものには
正直だが、省いているものについては黙っている**。13.1 GB の GGUF をコンテキスト 262,144 で
登録すると「少なくとも約 12,500 MiB」と読め、L4 の宣言 21,000 に収まって見えるが、その窓での
KV キャッシュは重みの何倍にもなる。つまり警告は、**それが存在する理由そのものの事例を素通し
できる**。窓は行に載っている（`context_tokens`）ので第 2 項の材料は既に保存されており、足りない
のはそれを数字に変える実測だけである。足したあとも決定 6 の規則はそのまま効く——窓が宣言され
ていないモデルの要求は、自信のある合計になるのではなく `unknown` のままにする。image 役はこの
影響を受けない（チェックポイントの VRAM はコンテキスト長で増えない）。

**2. 「和ではなく最大」は llm と sd-server には正確で、comfy には保守的すぎる。**
決定 6 は「image 役では選択中の 1 つ」と書いており、これは sd-server では真だった。
`ImageEngine=comfy` の配備では有効なモデルが全部ディスクにあり（fetch サイドカーの `SYNC_ALL`）、
チェックポイントはリクエストごとに選ばれ、ComfyUI は読み込んだものを場所が要るまで保持する
——つまり複数が同時に常駐しうる。**それでも和には変えない**: comfy は落ちるのではなく退避する
し、和にすると有効なモデルが 4 つある配備では起動のたびに警告が出る＝誰も読まない警告になる。
足りないのは「comfy のエンジンは複数を同時に載せうる」とパネルに 1 文書くことだけである。

## フェーズと完了の定義

- **P0（この ADR の実装範囲）**: 梯子の宣言（60-engines → エンジン表）・選択の保存・
  `UpdateCapacityProvider` の適用・停止/退場待ち/起動の導線・決定 6 の警告・決定 7 のバッジと
  `$1.26` の除去・決定 8 のリフレクションテスト・決定 9 の IAM。**単体テストまで。**
  完了の定義: (1) 梯子が空の配備で ECS の呼び出しが 1 本も増えないことがテストで言える、
  (2) 段を選ぶと `UpdateCapacityProvider` に渡る `InstanceRequirements` が期待通りで、
  **読んだ他の欄が全部そのまま戻る**ことがテストで言える、(3) 収まらないモデルを有効化すると
  確認を求められ、`vram_mib` も `bytes` も無いモデルでは「不明」と出る。
- **P1（実機）**: ✅ **完了（2026-09-10）**。af-sandbox で段を切り替えて実際にインスタンスを買い、
  退場待ち・`VcpuLimitExceeded`・コールドスタートを実測し、未解決 1・2・3 を埋めた。
  **梯子の出荷時の既定は空のまま**（決定 1）——価格は請求とずれ、在庫はリージョンごとに
  違うので、「測っていない値を既定にしない」という前提は P1 の後もそのまま生きている。
  実測は「P1 の実測」節、記録は docs/log/95。
- **P2（必要が出たら）**: タスク定義の追随（決定 11・未解決 6）、`--models-max` を段に応じて
  上げる、`engine_hourly` の段別内訳（決定 10）。
  - 2026-09-10 に上の Console レビューから追加: 決定 6 の要求に KV キャッシュを数え入れること
    （所見 1）。所見 2——comfy のエンジンは複数を同時に載せうるとパネルに書くこと——は
    ✅ **同日に実装済み**（`admin.engines_class_vram_many`・provider が `comfy` のときだけ
    出す）。数字そのものは最大のままである。
- **フェーズではない——まだ残っている運用の一手。** 🔴 **梯子を宣言している配備が 1 つも無い**
  ので、クラスの選択欄はどこにも出ていない（決定 3。出荷時の既定が空なのは決定 1）——機能は
  あるが誰にも見えていない状態である。宣言は配備ごとの行為で、その配備の `params/60-engines`
  に書く。書く内容を決めるのは 3 つ:
  - **G 系 vCPU クォータ（アカウントごと・購入形態ごとに違う）。** af-sandbox は
    オンデマンド 8 / Spot 0（16 への引き上げ申請中）、acrt は 64 / 64（2026-09-10 実測・
    「追試」節。以前の「本番は 96」はもう合っていない）。クォータを超える段は選べてしまい、
    ただ配置されない——サービスイベントの `VcpuLimitExceeded` として出る。
  - **`usdPerHour` は請求された額か、さもなくば空。** ECS Managed Instances は定価に約 8% の
    管理料を乗せる（実測）。金額が無ければ何も出ないだけで、誤った金額より良い。
  - **先頭の段は、スタックが既に買っているものを書き写す**（`<役>AllowedInstanceTypes`・
    `AcceleratorMemMinMiB`・`VCpu*`・`Mem*`）。「既定に戻す」が戻る先であり、両者が一致して
    いるかは誰も検査しない。

## 追記 — 適用に失敗した段の再試行（2026-09-10）

「直さなかったが分かっていること」の 1 件目を直した。**CP の順序は変えていない**——設定を保存
してから適用する順序こそが、「適用できなかった」と画面で言える理由だからである。足したのは、
失敗をそのまま持ち越さないための最小の 1 欄と、Console 側の再試行の口だけである。

**なぜ選び直しでは再試行にならないか。** 保存が先なので、失敗した直後の GET
`/api/admin/engines` はもう新しい段を `class` に載せている。パネルの `<select>` は保存済みの段
を表示し、同じ段を選んでも `change` イベントは発火しない。つまり **UI が送れない要求が 1 つ
だけ存在する**——「いま表示している段を、もう一度」である。回復には別の段へ移してから戻る
必要があり、その往復は capacity provider に**望んでいない段を一度書く**。

足したもの:

- `engineRuntimeState.classApplyErr`（`engines.go`）と `applyClass` の両出口での記録
  （`engine_class.go`）。成功で消え、失敗で入る。**プロセスのメモリにしか無い**のは
  `appliedClass` と同じ理由で、**無いことは「適用済み」ではなく「主張が無い」と読む**——
  再起動した CP は自分が見ていない失敗を主張してはならない。この穴が安全なのは、起動のたびに
  段を再適用するからである（決定 5）。
- 応答の `class_apply_error`（`engine_admin.go` の `row`）。梯子を宣言している配備でだけ、
  かつ言うことがあるときだけ出る。
- Console: 失敗した `PUT …/class` のあとに **行を読み直す**（読み直さないと、選択が保存済み
  であるにもかかわらずピッカーが古い段に戻り、CP が持っているものとも画面が一致しない）。
  そして `class_apply_error` があるときだけ「もう一度適用する」を出し、**いま選ばれている段を
  そのまま再送**する。

固定した試験:

- Go: `TestPutClassSaysWhyTheApplyFailedAndARetryClearsIt`——何も適用していないプロセスは何も
  言わない・失敗すると provider の言葉が `class_apply_error` に出る・**同じ段の再送**で消えて
  `UpdateCapacityProvider` がもう一度飛ぶ。陽性対照: `row` から 1 行消すと落ちる（確認済み）。
- dom: 「保存はできたが適用に失敗した」ときに再試行が出ること、CP が何も言わないときは出ない
  こと。陽性対照: パネルの当該ブロックを消しても、`await load()` を消しても落ちる（両方確認）。

**実描画で確認した**（ヘッドレス Chromium・実バンドル）: 赤い一文が provider の言葉ごと出て、
その下に「もう一度適用する」が独立した行で立つ。既存の「いま入れ替える」とは別の行為なので
並べず、上に置いた。

## 追記 — Spot への切り替えは CloudFormation が拒む（2026-09-11・実測・$0）

image 役を Spot に移す検討の前段。「追試」節は **ECS の API** の側を測って
`capacityOptionType` が `UpdateCapacityProvider` を跨いで保持されることを確かめたが、
**CloudFormation の側**——スタック更新でこの欄を書き換えられるのか——は測っていなかった。
`60-engines.yaml` の `ImageCapacityProvider` を SPOT にする change set は本番でも
`Modify` / `Replacement: Conditional` と出る。CFN のドキュメントは "Some interruptions"
（置き換えない）と読めるが、`UpdateCapacityProvider` の型にこの欄が無いことと矛盾する。
**流すまで分からない**ので、live に触れずに測った。

方法: `ImageCapacityProvider` を（`Name: !Sub "af-${AWS::StackName}-image"` のハードコードごと）
写した使い捨てスタック 1 枚を af-sandbox に作る。IAM 3 本と provider 1 本だけで、
service も `ClusterCapacityProviderAssociations` も置かない。インスタンスは 1 台も起動しない
＝ $0。所要 10 分。

- 🔴 **その場更新は失敗する。置き換えに転ぶ。** change set は本番と同じ
  `Modify` / `Replacement: Conditional` / `ManagedInstancesProvider` は
  `RequiresRecreation: Conditionally`。**実行すると `UPDATE_FAILED`**:
  `CloudFormation cannot update a stack when a custom-named resource requires replacing.
  Rename af-af-spotprobe-s2hpl5k-image and update the stack again.` → `UPDATE_ROLLBACK_COMPLETE`。
  **失うものは無い**——拒否は作成の**前**に出るので、provider は ARN も `ON_DEMAND` も
  そのまま（前後の `describe-capacity-providers` で確認）。ただし本番では
  60-engines の更新が**まるごとロールバックする**。同じ回に載せた別の変更も道連れになる。
- ✅ **別名で足すのは通る（安全な手順の第 1 段）。** 論理 ID と `Name` を変えた 2 本目
  （`-image-spot`・SPOT）を足す change set は `Add`・置き換え無し・`UPDATE_COMPLETE`。
  Spot クォータ 0 の af-sandbox でも作れる（「追試」の副産物どおり、クォータは起動時にしか効かない）。
- 🔴 **MI の provider を作ると、それだけでクラスタの provider 一覧に載る。**
  使い捨てスタックには `Associations` が無いのに、`DescribeClusters.capacityProviders` に
  2 本とも現れた（削除で消えた）。「関連付けを持つスタックは 1 つだけ」は**置き換える側**の
  規則であって、他のスタックが provider を足せないという意味ではない。次に live の
  `Associations` が更新された時点で黙って外れる。
- 片付け: スタック削除でクラスタの一覧は live の 4 本に完全一致で戻り、IAM ロールと
  インスタンスプロファイルも残っていない。provider は ECS が `INACTIVE` レコードとして残す
  （「追試」節と同じ）。

**本番（acrt）で切り替えるならこの順**（`cfn/PARAMETERS-60-engines.md`「The capacity
providers」に手順として書いた）: ①別名の SPOT provider を足して `Associations` に並べる →
②image サービスの `CapacityProviderStrategy` と**エンジン表の `capacityProvider` を同じ回で**
新しい名前へ（表は `!Ref` ではなく `!Sub` の文字列なので資源に追随しない。間違えても何も
落ちず、CP が誰も使っていない provider を見続けるだけ——`draining` と段の適用の両方がそこを
読む） → ③旧 provider を後の回で消す。**llm 役は `ON_DEMAND` のまま**（Spot の 2 分前予告は
会話の途中で来て、その後に 527〜586 秒のコールドスタートが続く）。acrt の Spot クォータは
`L-3819A6DF` が 64 で、申請は要らない。

## 追記 — 未解決 7 の image 役、2 点目以降（2026-09-11・開発配備・comfy）

P1 の 1 点目は sd-server（sd.cpp）で取った。役が ComfyUI に替わった配備で、l4 段
（g6.xlarge）に SDXL を載せて解像度とバッチを振った。**「1 点から係数は作らない」に従って
点は増えたが、🔴 肝心の VRAM のピークは読めていない。理由は下に書く。**

### 測れたこと——7 点すべて成功、OOM 無し

`sdxl-base-1.0`・20 step・同じ prompt。秒数は連続する生成ファイルのナノ秒タイムスタンプ差で、
MCP と HTTP の往復を含む（チェックポイントは温まったままなので読み込みは含まない）。

| 解像度 | バッチ | 秒 | 生成された PNG のバイト数 |
|---|---|---|---|
| 768×768 | 1 | 7.57 | 873,660 |
| 1024×1024 | 1 | 約 11.5 | 1,548,285 |
| 1024×1536 | 1 | 15.85 | 2,051,213 |
| 512×512 | 2 | 7.73 | 342,432 / 301,093 |
| 1024×1024 | 2 | 19.07 | 1,707,092 / 1,486,304 |
| 1024×1536 | 2 | 29.32 | 2,165,517 / 2,090,950 |

512×512 バッチ 1 は同じターン内で前の呼び出しから切り離せなかったので秒数を載せない
（生成そのものは成功し、338,392 バイト）。**l4 段で 1024×1536 バッチ 2 まで OOM しない。**

### 🔴 測れなかったこと——ComfyUI は VRAM の内訳を INFO に出さない

P1 の 1 点目（juggernaut-xl-v9）は sd.cpp が
`total params memory size = 6624.11MB` と auto-fit の予備（DiT 2,048・Conditioner 2,048・
VAE 1,024）を**自分で標準出力に書く**から取れた。ComfyUI v0.34.0 は書かない:

- 重みの実測は `Model loaded: patcher=… model=… ram_mb=… vram_mb=…` という 1 行にあるが、
  これは `comfy/internal_logging.py` の `detail()` で出力され、そのレベルは **DETAIL = 15**
  ——`logging.INFO`（20）より**下**である。
- `app/logger.py` の `setup_logger` の既定は console が INFO で、DETAIL はコンテナ内の
  ファイル `comfyui_detail.log` にだけ行く。CloudWatch には**出ない**。
- INFO で読めるのは起動時の `Total VRAM {x} MB, total RAM {y} MB`、
  `Requested to load {ClassName}`、`{n} models unloaded.`、`Prompt executed in N seconds` だけ。

つまり **image 役を comfy で動かしている配備では、未解決 7 を 1 点目と同じ形（重みの実際 ＋
計算用予備の内訳）で答えられない。** 必要なのは `60-engines.yaml` の ComfyUI 起動コマンドに
`--verbose DETAIL` を足すこと 1 つで、これは 1 行の変更だが**テンプレートの壁**（0072 の
追記参照）と同じ場所を触るので、梯子の宣言を入れる回にまとめるのが素直である。
ここでは事実だけ置いて、変更はしていない。

### 🔴 l4 段の宣言値 8,000 MiB は、実際に載るものを説明していない

同じ配備の `classes` は `l4` を `{"label":"L4 24GB (g6.xlarge)", "vram_mib": 8000}` と
宣言している。ラベルは 24 GB でカードも 24 GB だが、**梯子の宣言は 8,000 MiB** である。
その結果:

- `vram_fits` は false（`flux1-dev-fp8` の下限 16,571 MiB > 8,000）。
- それでも **`flux1-dev-fp8` は l4 段で 2 回とも普通に生成した**（1024×1024）。

決定 6 の門は「有効化するとき」にしか走らないので `mode=on` は素通りし、実害は出ていない。
だが**宣言値が実機の半分以下**だと、門は「載るものを載らないと言う」側に倒れる。P1 の 1 点目
が示した「下限は実使用の 58%」とは逆向きの誤差で、しかもこちらは**段の宣言そのもの**である。
未解決 7 の係数を書く前に、まず**梯子の `vram_mib` が何を意味する数なのか**（カードの物理量
なのか、運用上の割当上限なのか）を決めておかないと、係数を掛ける相手が定まらない。

## 追記 — 未解決 7 の llm 役、KV キャッシュは測らずに**計算できる**（2026-09-11・開発配備）

未解決 7 は「係数を書くには測るしかない」と書いていた。llm 役については**それが誤り**である。
KV キャッシュの大きさは係数ではなく**モデルのヘッダから決まる量**で、llama.cpp が確保する
バイト数と**桁も端数も一致する**。2 点で突き合わせ、**どちらも差 0.00 MiB（0.00%）**だった。

式は

```
KV = n_layer × n_head_kv × (key_length + value_length) × ctx × bytes(cache 型)
```

で、これは llama.cpp の `llama_kv_cache: size = …` そのものである。

### 突き合わせた 2 点

| モデル | 族 | n_layer | n_head_kv | key/value | ctx | 式 | 実機 `KV self size` | 差 |
|---|---|---|---|---|---|---|---|---|
| `qwen2.5-coder-1.5b` | qwen2 | 28 | 2 | 128 / 128（導出） | 16,384 | 448.00 MiB | **448.00 MiB** | 0.00 |
| `qwen3-coder-30b-a3b` | qwen3moe | 48 | 4 | 128 / 128（宣言） | 32,768 | 3,072.00 MiB | **3,072.00 MiB** | 0.00 |

ログはそのまま:

```
llama_kv_cache:      CUDA0 KV buffer size =   448.00 MiB
llama_kv_cache: size =  448.00 MiB ( 16384 cells,  28 layers,  4/1 seqs), K (f16):  224.00 MiB, V (f16):  224.00 MiB
llama_kv_cache: attn_rot_k = 0, n_embd_head_k_all = 128

llama_kv_cache:      CUDA0 KV buffer size =  3072.00 MiB
llama_kv_cache: size = 3072.00 MiB ( 32768 cells,  48 layers,  4/1 seqs), K (f16): 1536.00 MiB, V (f16): 1536.00 MiB
```

`n_slots = 4, kv_unified = 'true'` だが、確保は **ctx 1 本分**である（`16384 cells` / `32768
cells`）。スロット数を掛けると 4 倍の過大評価になる。

### 🔴 `head_dim` を `embedding_length / head_count` で出してはいけない

qwen3moe は `attention.key_length` / `value_length` を **128 と宣言**しているのに、
`embedding_length / head_count` は **2048 / 32 = 64** である。導出を優先していたら 30B の KV を
**1,536 MiB と見積もり、実機の半分**になっていた。24 GB のカードで 1.5 GiB の過小評価は
「載る」と「載らない」を分ける大きさである。**宣言があれば宣言が勝ち、導出は宣言が無い族
（qwen2 がそう）への代替**にすぎない。実機の `print_info: n_embd_head_k = 128` が裏づけている。

### ヘッダは Hugging Face から HTTP Range で取れる。S3 は要らない

**Hugging Face の API は式の入力を持っていない**（実測）。`gguf` ブロックは `total` /
`architecture` / `context_length` / `chat_template` の 4 欄だけで、GGUF 専用リポジトリの
`config` は `{}` である。だからファイル自身を読むしかない——だが **CP は S3 に触らない**
（ADR 0072 レビュー R3。権限は 1 つも無く、足さない）ので、読むのは**まだ出所にある方の複製**
にする。取り込みが使っているのと同じ HTTP で、Range で頭だけを取る。

必要なバイト数を実測した: 1.5B は **545 バイト**、30B は **1,426 バイト**で 4 欄すべてが揃う。
窓は 64 KiB（実測の約 45 倍）とし、足りなければ 1 MiB を 1 回だけ、それ以上は読まない——
tokenizer のトークン配列はメガバイト級で、しかも**一度も要らない**。

### 重みの側——決定 6 の「下限」はどのくらい下限か

| モデル | ファイルのバイト（＝下限） | `CUDA0 model buffer` | `CUDA_Host model buffer` | 下限 −（CUDA0＋Host） |
|---|---|---|---|---|
| 1.5B | 1,065.56 MiB | 934.70 MiB | 125.19 MiB | **+5.67 MiB** |
| 30B | 17,697.04 MiB | 17,524.43 MiB | 166.92 MiB | **+5.69 MiB** |

つまり**ファイルのバイト数は「重みの合計」としてはほぼ正確**（差 6 MiB 弱）で、外していたのは
**カードに載る分と host に残る分の按分**だけである（30B で 167 MiB、1.5B で 125 MiB が host 側）。
P1 の image 役で「下限は実使用の 58%」だったのとは事情が違う——SDXL 系で足りなかったのは
計算用予備で、llm 役で足りなかったのは **KV キャッシュ**である。

### 効いた大きさ

30B を ctx 32,768 で載せたときの実機の内訳は、重み 17,524.43 ＋ KV 3,072.00 ＋ compute 116.01
＝ **20,712.44 MiB**。カードは 22,563 MiB なので、**実際の余裕は 1,851 MiB**。

- 下限だけの旧答え: 17,697 MiB → 余裕 4,866 MiB。**3,015 MiB（14.6%）の過小評価。**
- 重み＋KV の新答え: 20,769 MiB → 余裕 1,794 MiB。**実機との差 57 MiB（0.27%）。**

llm 役については、`weights + KV` が実用上ほぼ正確な見積りになる。compute バッファ（77〜116 MiB）
だけが残る差で、これは安全側に残っている。

### 実装（`engine_gguf.go`）

登録時に 1 回だけ読み、4 つの数を行に保存する（`kv_layers` / `kv_heads_kv` / `kv_key_len` /
`kv_value_len`。migration 0062・pg 0047）。**表示のたびに読まない**理由は 2 つ——モデルを列挙する
画面に毎回ネットワーク呼び出しを載せることになるのと、ファイルは sha256 で固定されているので
**行の下でヘッダが変わりようがない**ことである。

答えの強さを 1 段増やした: `declared` > **`weights_kv`** > `floor` > `unknown`。集合の判定は
「いちばん弱いもの」に倒す——最大の行がたまたま測られていたからといって、エンジンが読むのが
その行とは限らない。

🔴 **cache の型は CP からは見えない。** `-ctk` / `-ctv` は `LlmExtraArgs`、すなわちタスク定義まで
しか行かない CloudFormation パラメータで、**エンジン表には無い**。f16 を仮定している。量子化
した KV で走らせている配備では**過大評価**になり、これは「このカードに載るか」には安全側、
「どれだけ余っているか」には誤りである。表に無い欄を発明するより、そう書いておく方を選んだ。

ヘッダが読めない行（gated でトークンが無い・GGUF でない・出所が URL でない）と
`context_tokens` が未宣言の行は、**`unknown` ではなく従来どおりの `floor`** に留まる。手登録
（`POST …/models`）は出所を持たないので今回は対象外で、閉じるにはその経路に ref の欄が要る。

### 実測でつまずいた点

🔴 **llama.cpp のローダの行は、既定の冗長度では出ない。** 最初の起動では
`llama_kv_cache` も `load_tensors` も 1 行も出ず、ログは 43 行しかなかった。`KV self size` を
出すには子プロセスの冗長度を上げる必要がある——だが `LlmExtraArgs` は CFN パラメータなので
触れない。**カタログ行の `args` に `["--verbosity","4"]` を入れて解決した**: fetch サイドカーが
それを preset の行に書き、ルータが `--log-verbosity 4` として子に渡す（ログで確認）。CFN を
1 バイトも変えずにエンジンの観測性を上げられる、という手が 1 つあるということである。
代償として**起動を 1 回無駄にした**（3 回起こし、使えたのは 2 回）。

### 梯子の `vram_mib` は「カードの物理量」である

前節が「係数を掛ける相手が定まらない」と残していた問いは、**カードの物理量**で決着した。
l4 段は実機が申告する `Total VRAM 22563 MB` に合わせる——**8000 は誤り**であり、21000 は
保守的な下限にすぎない。決定 6 の門は `max(有効なモデル)` を段の値と比べるので、比べる相手が
カードでないと、上の 20,712 対 22,563 のような「あと 1.8 GB」の判断ができない。

## 追記 — 開発配備に梯子を宣言した（2026-09-11）

「フェーズではない」節の宣言を、開発配備で実際に入れた。**両役とも入り、段の適用が capacity
provider を書き換えるところまで実機で確かめた。**

### 宣言した文字列と、その根拠

```
LlmInstanceClasses=l4|L4 24GB (g6.xlarge)|22000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26;l40s|L40S 48GB (g6e.xlarge)|44000|g6e.xlarge|4-8|30000-65536|2.91
ImageInstanceClasses=l4|L4 24GB (g6.xlarge)|22000|g6.xlarge|4-8|15000-65536|1.26;l40s|L40S 48GB (g6e.xlarge)|44000|g6e.xlarge|4-8|30000-65536|2.91;l40s2x|L40S 48GB 8vCPU (g6e.2xlarge)|44000|g6e.2xlarge|8|30000-65536
```

- **先頭の段はスタックが現に買っているものの写し**である。`AllowedInstanceTypes` /
  `VCpu*` / `Mem*` をそのまま持ってきた（llm は `g6.xlarge,g5.xlarge` / 4-8 / 15000-65536、
  image は `g6.xlarge` / 4-8 / 15000-65536）。
- **2 段目は g6e.xlarge**。4 vCPU なので G 系クォータ 8 の中で、image 役の 1 台と並べても
  収まる。
- **`vramMiB` は 22000**。前節が残していた「カードの物理量か運用上の割当上限か」は前者で
  決着した。3 つの数が関わる: 実機の `Total VRAM 22563 MB`、**EC2 の申告値 22,888 MiB**
  （`describe-instance-types` の `GpuInfo.TotalGpuMemoryInMiB`。g6.xlarge も g5.xlarge も同じ）、
  そして公称の 24 GB。🔴 **公称を書いてはいけない**——`vramMiB` は
  `AcceleratorTotalMemoryMiB.Min` にもなるので、24576 と書くと**どのインスタンスも該当しなくなる**。
  22000 は EC2 の 22,888 に対して 888 MiB の余裕があり、比較の相手としては実機の 22,563 の
  少し下という、両方の役目を満たす唯一の帯である。
- **image の l4 は 8000 だった**。これは `ImageAcceleratorMemMinMiB`（配置のフィルタ）を段に
  写してしまったもので、前節が「宣言値が実機の半分以下だと門は載るものを載らないと言う」と
  書いていた当のものである。**直した結果が実機で反転した**: `GET /api/admin/engines` の image は
  `vram_fits: false` → **`true`**（`flux1-dev-fp8` の 16,571 MiB に対して）。

### `usdPerHour` は Cost Explorer の確定値

|  | BoxUsage | ECS Managed Instances の管理料 | 合計 | 宣言 |
|---|---|---|---|---|
| g6.xlarge | $1.1672/h | $0.0910/h | **$1.2582/h** | 1.26 |
| g6e.xlarge | $2.6990/h | $0.2105/h | **$2.9095/h** | 2.91 |

2026-09-09〜11 の `ce get-cost-and-usage` を `INSTANCE_TYPE` で絞り `USAGE_TYPE` で割った実績
（g6.xlarge 4.2181 時間・g6e.xlarge 1.4978 時間＝P1 の 90.5 分がこれにあたる）。
**g6.xlarge が既存の宣言 1.26 と一致した**のが、この取り方の陽性対照である。管理料は本体の
**7.80%**（両機種とも）で、`PARAMETERS-60-engines.md` が「定価を写すな」と書いている 8% は
これである。`l40s2x` は請求の実績が無いので**価格を空のまま**にした——書かないことが規約である。

### 🔴 梯子は CFN に入っても、走っている CP には届かない

宣言は `cloudformation deploy --parameter-overrides` の 1 回で入り、SSM の
`/af-ws/engines` も同じ分（01:14:20Z）に新しい梯子へ変わった。**それでも
`GET /api/admin/engines` は古い梯子を返し続けた**——l4 は 8000 のまま、llm は `classes` が空の
まま。

原因は `newEngineRegistry` が**ルート登録時、すなわち CP の起動時に 1 回だけ**
`loadEngineTable` を呼ぶことである。表は「器の性質」なので再読み込みの経路が無い。
`update-service --force-new-deployment` で CP を入れ替えて反映させた（01:15:35Z → 01:17:11Z、
**約 100 秒**、ALB の裏で blue/green なので停止は無い）。

これは ADR 0072・0074 の「CloudFormation を回さずに変える」という趣旨に対する**例外**である。
モデルはカタログなので CP を触らずに変わるが、**梯子は器の側なので CFN 1 回＋CP の再起動 1 回**
が要る。運用者がそれを知らないと、「宣言したのにパネルに出ない」を設定ミスとして探し続ける
ことになる。

> ✅ **修正済み（2026-09-11・実装済み・実機未検証）——表を読み直す。** CP がエンジン表
> （`AF_ENGINES_SSM_PARAM`）を **10 秒ごと**に読み直すようにした（`engine_table_reload.go`。
> 間隔は pending の読みと同じ定数）。まず**本文の文字列比較**で、変わっていなければ
> `GetParameter` 1 回だけで終わる——「何も変わっていない」が圧倒的多数なので、1 分に 6 回が
> 妥当な値段になるのはそのためである。
>
> **生きたまま運べるのは梯子だけ**にした。行の他の欄（service・url・health・provider・
> capacity provider・idle・deadline）は、いま動いているオブジェクト——`engineECS` の
> クライアント、コントローラの goroutine とその間隔、起動時にだけ張る capacity
> クライアント、需要の窓——に焼き込まれている。差し替えるには runtime state ごと作り直す
> ことになり、**このプロセスしか知らないもの**（コントローラが停止の判断に使う需要の
> カウンタ、温かいモデル、このプロセスが最後に適用した段）を捨て、同じエンジンに 2 本目の
> コントローラを走らせることになる。だから**ログに出して再起動を促す**——黙って半分だけ
> 適用するより、パネルの言うことと配備の振る舞いが食い違わないほうがよい（それがこの節の
> 欠落そのものである）。
>
> **行が増えた・消えた場合も同じくログだけ**にした。消えた役を登録解除すると、1 回のポーリング
> を根拠に、いま使っている誰かから GPU を取り上げることになる。運用者の出口は `mode=off`
> で、それは推測ではなく決定である。🔴 **梯子が「無い→ある」になる場合も再起動が要る**:
> capacity クライアントとコントローラの start gate は起動時に梯子があるときだけ張るので、
> ここで生きたまま採ると**パネルには段が出るのに誰も強制しない**——決定 4 が防いでいる
> 「門を迂回して古いカードへ静かに再着地」そのものになる。
>
> 差し替えは `classesMu`（RWMutex）越しで、読み手（コントローラ・ゲートウェイ・admin）は
> `classList()` を通る。`-race` で「読みながら 50 回差し替える」試験を置いた——ロックを外すと
> 実際に落ちることを確かめてある。試験は他に 3 本（新しい梯子が載る／壊れた表は前の梯子を
> 保つ／変化なしでは差し替えない＝同じスライスが返る）。**実機未検証**: 次に CFN で梯子を
> 変えたとき、CP を入れ替えずに `GET /api/admin/engines` が 10 秒で追いつくことを見る。

### 段の適用は provider の 4 欄を書き換える

llm に `PUT /api/admin/engines/llm/class {"class":"l4"}` を投げ、前後で
`describe-capacity-providers` を取った。

| 欄 | 前 | 後 |
|---|---|---|
| `allowedInstanceTypes` | `g6.xlarge, g5.xlarge` | `g6.xlarge, g5.xlarge` |
| `acceleratorTotalMemoryMiB.min` | **21000** | **22000** |
| `vCpuCount` | 4-8 | 4-8 |
| `memoryMiB` | 15000-65536 | 15000-65536 |

動いたのは VRAM の下限だけだが、**それがまさに宣言で変えた値**である。決定 1 の「4 欄を
書き換える」は実経路で効いている。段を上げての起動は**していない**（GPU 費用。ADR 0074 P1 で
済んでいる）。

### 配備そのもの

`dev-deploy.sh` で CP と Agent の両イメージを焼き直した（`origin/develop` b6feea43、
ImageTag=0.18.1-dev-b6feea43）。その中の 60-engines の段で、ADR 0072 P6 の移行の門
（`update.sh` が live のスタックを読んで `<役>Enabled=true` を翻訳する）は**何も出力しなかった**
——この配備は既に `Enabled=true` を持ち `<役>ModelS3Key` を持たないので、翻訳する対象が無い。
門が「黙っている」ことが、P6 を通過済みの配備の正しい姿である。

## 追記 — 梯子の再読込が効いた（2026-09-11・実機・$0）

前節（#520）が「梯子は CFN に入っても走っている CP には届かない」と書いた欠落を、
`engine_table_reload.go` が閉じた。**実機で確かめた。**

`LlmInstanceClasses` の 2 段目のラベルだけを変える CFN 更新を流し、**CP を入れ替えずに**
`GET /api/admin/engines` を見た。

| 時刻 | 出来事 |
|---|---|
| 02:50:51 | `cloudformation deploy --parameter-overrides LlmInstanceClasses=…` 開始 |
| 02:51:39 | `Successfully created/updated stack` |
| 02:51:46 | API の `classes` が新しいラベルを返した |

**スタックの完了から 7 秒。** CP のサービスは 02:19:57Z に作られた配備のままで、
`force-new-deployment` は挟んでいない——前節が要した約 100 秒の入れ替えが要らなくなった。
ラベルは検証後に元へ戻した（同じ経路で、やはり再起動なし）。

取り込む範囲が梯子だけであること（ECS クライアント・コントローラの goroutine・需要窓は
据え置き、変わったら「再起動が要る」とログに出す）は実装のとおりで、今回はそこには触れて
いない。
