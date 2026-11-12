---
audience: "AWS の ECS 配備に、VOICEVOX の読み上げエンジン（`50-tts`）を足す配備管理者"
source_of_truth: "`deploy/aws/ecs/cfn/50-tts.yaml` と `cfn/PARAMETERS.md`、`deploy/aws/ecs/standup.sh`、Control Plane の読み上げの制御（`control-plane/tts_engine_control.go`）"
---

# 日本語の読み上げを足す

[English](tts.md) | 日本語

ECS の配備に、VOICEVOX（ずんだもん）の読み上げエンジンを足します。
エンジンはふだん 0 台で、読み上げの需要がたまると Control Plane が起動し、だれも聞いていなければ止めます。

## 全体像

| あなたがやること | 起きること |
|---|---|
| 取得した状態に `params/50-tts` を置いて `standup.sh` を実行する | イメージを ECR にコピーし、`50-tts` を建て、0 台にして、`30-ingress` にエンジンの場所を渡す |
| 管理の「**読み上げ**」でモードを選ぶ | Control Plane が、ECS の desired count を動かしてエンジンを起動・停止する |
| 何もしない | エンジンに届かないあいだは、Amazon Polly が読む |

このスタックがなくても、読み上げはどの配備でも動きます。Control Plane の `auto` の振り分けが、エンジンに届かないときはすべて Polly に送ります。`50-tts` が足すのは、ずんだもんの声です。
エンジンのモードと読み仮名の辞書は、管理の画面で決めます（[読み上げエンジンを動かす](../engines/read-aloud.ja.md)）。

## 費用

エンジンは ECS の **Fargate** のサービスで、**ふだんは 0 台**です。

- 2 vCPU / 4 GiB で動かしっぱなしにすると、月に約 $90 です。配備全体の月額が $165 前後なので、大きな割合になります
- 1 日の大半は、だれも聞いていません。そのため、需要があるときだけ動かします

## 有効にする

`standup.sh` は、取得した状態に `params/50-tts` のファイルがあるときだけ `50-tts` を建てます。

```bash
# ~/.config/agent-fleet/deploy/<profile>.<region>/params/50-tts
# Every parameter has a default, so this file may hold nothing but the namespace name;
# standup.sh overrides NetworkStackName / PlatformStackName / ServiceConnectNamespace
# from what it already knows about the deployment.
ServiceConnectNamespace=af.internal
CpuArchitecture=X86_64
TaskCpu=2048
TaskMemory=4096
# UseSpot=on   # ~70% cheaper, an interruption just falls back to Polly (see below)
```

| パラメータ | 既定 | 中身 |
|---|---|---|
| `ServiceConnectNamespace` | `af.internal` | Service Connect の名前空間 |
| `CpuArchitecture` | `X86_64` | `X86_64` か `ARM64` |
| `TaskCpu` | `2048` | `1024`・`2048`・`4096` のどれか |
| `TaskMemory` | `4096` | MiB |
| `UseSpot` | `off` | `on` で Fargate Spot に置く |

`standup.sh` は次の順に進めます。

1. イメージの段階で、固定したイメージを `crane` で ECR に運びます
2. スロットのプールのあと、`30-ingress` の前に `50-tts` をデプロイします
3. 新しいサービスを 0 台にします
4. スタックの出力 `TtsEcsService` / `VoicevoxUrl` を `30-ingress` に渡します

### 動いている配備に手で足す

1. `20-platform` をデプロイします（リポジトリを作ります）
2. `crane copy` でイメージを ECR にコピーします
3. `50-tts` をデプロイします
4. `ecs update-service --desired-count 0` で 0 台にします
5. `30-ingress` を 2 つのパラメータ付きでデプロイし直します。この手順で Control Plane のタスクが置き換わります

> [!WARNING]
> **`50-tts` を作る前に、イメージを ECR に入れておきます。** CloudFormation は ECS のサービスが安定するのを待つので、空のリポジトリに向けて作ったサービスは `CannotPullContainerError` を繰り返し、スタックは `CREATE_IN_PROGRESS` のまま戻りません。あとのどの手順でも救えません。リポジトリが `20-platform` にあるのはこのためです。
> スタックを作るときは、いつも 1 回コールドスタートがかかります。CloudFormation は最初のタスクが動くのを待ち、そのあとでサービスを 0 台に戻します。

Control Plane 側に要るのは、`30-ingress` の 2 つのパラメータだけです。`AF_TTS_ECS_CLUSTER` と `AF_TTS_ECS_REGION` は既存の `AF_ECS_*` から決まり、`CpTaskRole` は必要な `ecs:DescribeServices` / `ecs:UpdateService` をすでに持っています。パラメータの詳細は `deploy/aws/ecs/cfn/PARAMETERS.md` の「Speech (the VOICEVOX engine)」にあります。

## 有効にする前に知っておくこと

- **配置先はテンプレートに明記してあり、明記したままにします。** ふだんは `LaunchType: FARGATE`、`UseSpot=on` では代わりに明示の `CapacityProviderStrategy` です（2 つは同時に書けません）。どちらも書かないと API の既定は EC2 になり、2 vCPU のエンジンがワークスペースのスロットのプールに載ります。スイーパーの「ECS のタスクがないインスタンスは空き」という前提が崩れ、だれかのワークスペースが使うはずの席を取ります
- **`UseSpot=on` は既定ではありません。** Fargate Spot は約 70 % 安く、中断されても Polly に切り替わるだけです。エンジンは状態を持たず、消えたタスクは失敗ではなく置き換え（`tts.engine.replaced`）として扱います。既定でオフなのは、アイドル停止ですでに費用が小さいことと、容量が足りないと「エンジンがいつまでも来ない」形で現れるためです
- `UseSpot` を切り替えると配置が変わり、CloudFormation は**サービスを置き換え**ます。新しいサービスは desired 1 で始まるので、コールドスタートがかかります。0 台に戻します
- **テンプレートには `DesiredCount` を書いていません。** CloudFormation は新しいサービスを 1 で始め、以後の更新では数を触りません。これで、管理画面のスイッチが数を持てます。`0` と書くと、スタックを更新するたびに数が戻り、だれかが聞いている最中にエンジンが止まります
- **エンジンは Service Connect ではなく、Cloud Map の DNS 名で呼びます。** desired 0 のあいだは名前に A レコードがなく、準備中の扱いになって Polly が読みます
- **起動のたびに、イメージ（約 2 GB）をすべて取得します。** Fargate はイメージをキャッシュしません。このスタックでは、desired 1 から動くまで 70〜77 秒（うち取得が 34 秒）、さらに Cloud Map の A レコードが出るまで 67〜73 秒かかりました。そのあいだは Polly が読みます
- エンジンはテナントごとの状態を持ちません。読み仮名の辞書はクライアント側で当てるので、エンジンを消しても何も失いません
- **大きすぎる 1 回の合成はエンジンを落とすので、Control Plane が断ります。** 684 文字の 1 回のリクエストで、4 GiB のエンジンがメモリ不足で落ちました（終了コード 137）。`/version` のヘルスチェックは正常のままで、ECS がタスクを置き換えるまで約 2.5 分、全員の読み上げが Polly になりました。Control Plane は 1 回のリクエストを `AF_TTS_MAX_CHARS` 文字（既定は 300、`0` で上限なし）までにし、超えると `413 tts_text_too_long` を返します。Console は 60 文字で区切って送るので、これはスクリプト向けの安全策です
- **同時のリクエストは順番に処理されます。** 2 vCPU / 4 GiB で、328 文字のリクエストを 4 つ同時に送ると、40 秒・80 秒・119 秒・159 秒で返りました。聞く人が 2 人になっても待ち時間は半分にならず、最後の人の待ち時間が倍になります

## 自動の起動と停止を調整する

管理画面のスイッチは、`off`・`on`・`ondemand` の 3 つのモードのどれかを保存します。管理下のエンジンで値が未設定なら `ondemand` です。

- `ondemand` では、読み上げの需要がたまると Control Plane がエンジンを起動し、だれも聞いていなければ止めます
- **自動の起動と停止は、すべて監査ログに残ります**（`tts.engine.auto`、実行者 `tts-controller`）
- 既定のままなら設定は要りません。どれも Control Plane の環境変数で変えられ、このテンプレートはどれも設定しません

| 環境変数 | 既定 | 中身 |
|---|---|---|
| `AF_TTS_ECS_CONTROL_INTERVAL_SEC` | 30 | 見に行く間隔。**`0` で制御を完全に止めます**。モードは振り分けだけに効き、desired count を動かすのはスイッチだけになります |
| `AF_TTS_ECS_START_CHARS` | 2000 | 時間枠の中で、起動に足りる読み上げの需要（文字数）。エージェントの回答 1 つ分ほど |
| `AF_TTS_ECS_WINDOW_SEC` | 300 | その文字数を数える時間枠 |
| `AF_TTS_ECS_IDLE_SEC` | 1800 | だれも聞かないまま、この時間がたつと止める。**`0` で止めません**。起動の期限より短くはなりません |
| `AF_TTS_ECS_START_DEADLINE_SEC` | 300 | この時間までに `running` にならない起動は失敗とみなし、0 に戻して、ECS の示す理由を報告します |
| `AF_TTS_ECS_FAIL_COOLDOWN_SEC` | 900 | 失敗した起動が次の起動を止める時間。続けて失敗するたびに倍になります（最大 16 倍）。これがないと、失敗を繰り返すたびに 2 GB を取得します |
| `AF_TTS_MAX_CHARS` | 300 | 1 回の合成の上限（文字数）。`0` で上限なし。制御のつまみではなく、エンジンを落とす大きすぎるリクエストからエンジンを守ります |
| `AF_TTS_ECS_OFF_GRACE_SEC` | 60 | 明示的に「無効」にしてから desired count を動かすまでの時間。振り分けはすぐ Polly に切り替わります。ECS への書き込みだけを遅らせるので、オフにしてすぐオンに戻してもコールドスタートはかかりません |

## 関連

- [読み上げエンジンを動かす](../engines/read-aloud.ja.md)：モードと読み仮名の辞書
- [読み上げを使う](../../user/settings/read-aloud.ja.md)：メンバーの設定
- [推論エンジンを足す](engines.ja.md)
- [費用を抑える](cost.ja.md)
