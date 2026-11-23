---
audience: "メンバーのスマートフォンにプッシュ通知を届けたい配備管理者"
source_of_truth: "Control Plane の Web Push サービス（`control-plane/internal/pushsrv`、`control-plane/internal/webpush`）とその環境変数"
---

# Web Push をオンにする

[English](web-push.md) | 日本語

Web Push は、セッションが質問したとき・承認を求めたとき・ターンを終えたときに、Console を閉じていてもメンバーのスマートフォンやパソコンに通知を届けます。
既定ではオフです。Control Plane の環境変数でオンにし、ブラウザのプッシュサービスへの外向きの HTTPS を開けます。

## 全体像

| あなたがやること | 起きること |
|---|---|
| `AF_WEB_PUSH=true`、VAPID の秘密鍵、連絡先を設定して再起動する | Control Plane が送信側と購読 API を起動する。メンバーの設定 →「通知」に「この端末へのプッシュ通知」が出る |
| `AF_WEB_PUSH` を設定しない | 何も変わらない。送信側もルートもなく、Console の項目も出ない |
| Control Plane の外向きの経路で、プッシュサービスのホストを許可する | プッシュが端末に届く |

鍵ペアはこの配備のものです。中継はなく、Control Plane が自分でプッシュに署名し、ブラウザが指定したプッシュサービス（Google・Mozilla・Apple・Microsoft）へ送ります。

## 設定する

1. 秘密鍵を作ります。32 バイトの乱数を、パディングなしの base64url にしたものです。例：

   ```
   openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n'
   ```

2. Control Plane の環境に次の変数を入れ、再起動します。compose なら `.env`、Kubernetes ならオーバーレイの `cp.env`（秘密鍵は Secret `af-cp-secrets`）、ネイティブ実行なら `af start` の環境か systemd ユニットの `Environment=` 行です。AWS ECS では下のスタックパラメータを使います。compose では `docker compose restart` は `.env` を読み直しません。`docker compose up -d` で Control Plane のコンテナを作り直してください

   | 変数 | 意味 |
   |---|---|
   | `AF_WEB_PUSH=true` | Web Push をオンにする。これ以外の値ではオフのまま |
   | `AF_WEB_PUSH_VAPID_PRIVATE_KEY` | 手順 1 の鍵。必須。ほかの配備の秘密と同じく秘密にする。公開鍵はここから導かれる |
   | `AF_WEB_PUSH_SUBJECT` | プッシュサービスへの連絡先。`mailto:you@example.com` か `https://` の URL。必須。ほかの形だと、ログに警告を出して Web Push はオフのまま |
   | `AF_WEB_PUSH_ENDPOINT_HOSTS` | 任意。受け付けるプッシュサービスのホストを足す。カンマ区切りで、先頭のドットはサブドメイン全部（`.push.example.com`） |
   | `AF_NOTIFICATION_DRAIN_INTERVAL` | 任意。既定は `15s`。動いているワークスペースの通知を Control Plane が集める間隔。Console を開いていなくても、質問がスマートフォンに届くようにする。Web Push がオンのときだけ動く |

3. ログに `web push enabled` が出ているか確かめ、メンバーとして設定 →「通知」を開き、端末のプッシュをオンにします（[通知を受け取る](../../user/settings/notifications.ja.md#スマートフォンにプッシュ通知を受け取る)）

`AF_WEB_PUSH=true` なのに鍵や連絡先がない・形が違うときは、Control Plane はログに警告を出し、Web Push をオフにして動き続けます。止まりはしません。

## ECS で使う

AWS の ECS 配備（`ecs`・`ecs-ec2`）では、Control Plane の環境は `30-ingress` スタックなので、Web Push はそのパラメータで設定します。タスク定義に手で足した変数は、新しいタスク定義を登録する更新（新しいイメージタグなど）で元に戻ります。

1. 手順 1 と同じ方法で鍵を作り、Web Push をオンにする**前に** SSM に SecureString として入れます。存在しないパラメータを指すスイッチはタスクを起動できなくし、`standup.sh` も鍵がなければ止まります

   ```bash
   aws ssm put-parameter --profile <profile> --region <region> --type SecureString \
     --name <SsmPrefix>/web-push-vapid-key \
     --value "$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n')"
   ```

   `<SsmPrefix>` は `30-ingress` のパラメータで、既定は `/af-cp` です。実行ロールはすでに読めます。既定のままにしてください。`/af-cp/` と `/af-ws/` の外にすると、実行ロールの `ssm-read` ポリシーを広げる必要があります。値は自分の端末で生成し、控えをシークレットの保管場所に残してください。鍵をなくす・替えると、メンバーの購読はすべて無効になります

2. スイッチと連絡先を付けて `30-ingress` をデプロイします

   ```bash
   cd deploy/aws/ecs
   aws cloudformation deploy --profile <profile> --region <region> \
     --stack-name <ingress stack> --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
     --capabilities CAPABILITY_NAMED_IAM \
     --parameter-overrides WebPush=on WebPushSubject=mailto:you@example.com
   ```

   | パラメータ | 意味 |
   |---|---|
   | `WebPush` | `on` で `AF_WEB_PUSH=true` にし、鍵を `<SsmPrefix>/web-push-vapid-key` から読む。`off`（既定）は Web Push をオフのままにする |
   | `WebPushSubject` | `AF_WEB_PUSH_SUBJECT`。`mailto:` か `https://`。`WebPush=on` では必須で、空のままだとデプロイが拒否される |
   | `WebPushEndpointHosts` | 任意。`AF_WEB_PUSH_ENDPOINT_HOSTS` |
   | `NotificationDrainInterval` | 任意。`AF_NOTIFICATION_DRAIN_INTERVAL` |

   手順 1 と同じ `--profile` と `--region` を使います。`CFN_BUCKET` は `20-platform` の出力 `CfnTemplatesBucket` です（[ECS に構築する](../aws-ecs/install.ja.md#3-30-ingress-をデプロイする)）。`WebPush` 以外の 3 つのパラメータは、スイッチがオンのときだけ読まれます。`update.sh` はこれらを設定も消去もしないので、次の更新でも値は残ります
3. 手順 3 と同じように、ログの `web push enabled` と、設定 →「通知」を確かめます

鍵は変えないでください。オフに戻すときは `WebPush=off` をデプロイします。SSM のパラメータは残しておくと、再びオンにしても端末の購読は無効になりません。`teardown.sh --purge-secrets` は、既定の `/af-cp/` の下にあるほかのシークレットと一緒にこれも消します。フラグがなければ残ります。

ECS では外向きの許可は要りません。既定の `SsmPrefix` と AWS 管理の `aws/ssm` キーなら、IAM の変更も要りません。Control Plane のタスクは、ワークスペースの egress ゲートウェイではなく VPC の NAT ゲートウェイから出るので、下のホストにはそのまま届きます。パラメータの一覧は [`cfn/PARAMETERS.md`](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS.md#web-push)、ほかのオプションは[タイマー・GitHub サインイン・外部のエンジンを設定する](../aws-ecs/options.ja.md#タイマーgithub-サインイン外部のエンジンを設定する)にあります。

## 外向きの通信を開ける

Control Plane は、メンバーのブラウザが登録した宛先へ、プッシュごとに POST します。Control Plane から次のホストへの外向きの HTTPS（443 番）を許可してください。

| プッシュサービス | ホスト |
|---|---|
| Chrome・Edge などの Chromium 系、Android | `fcm.googleapis.com` |
| Firefox | `updates.push.services.mozilla.com` と `*.push.services.mozilla.com` |
| Safari、iPhone・iPad のホーム画面アプリ | `*.push.apple.com` |
| Windows の通知サービス | `*.notify.windows.com` |

- これ以外のホストを宛先にした購読は、登録の時点で断ります。メンバーが Control Plane を内部アドレスへ向けられないようにするためです。ホストを足すときは `AF_WEB_PUSH_ENDPOINT_HOSTS` に入れ、ファイアウォールやプロキシでも同じホストを開けます
- 管理 → Egress が制御するのはワークスペースの通信で、Control Plane 自身の外向き通信ではありません。Control Plane が動くネットワークで、これらのホストを開けてください
- メンバーのブラウザがプッシュサービスに届くのは、ブラウザ自身の通信です。内向きに開けるものはありません

## 何がプッシュされるか

- プッシュするのは 4 種類だけです：質問、プランの承認待ち、権限の許可待ち、ターンの完了。ほかは通知センターに残ります
- 載せるのはセッション名と種類です。質問の文面は、テナント管理者がテナントの「上限・自動停止」で許可したときだけ、短く切って載せます。この設定は Web Push がオフでも保存されますが、スイッチが Console に出るのはオンにしてからなので、オンにした後で設定します
- 同じメンバーの別の Console で画面に出ているセッションにはプッシュしません。種類ごとのオフはメンバーが選べます。無効になったメンバーにはプッシュしません
- 1 時間たっても届けられないプッシュは捨てます。1 人が登録できる端末は 20 台までです
- 一度に来た通知は上限のある待ち行列を 2 つ通ります。前段で通知ごとの読み取り（メンバー・その設定・端末）を待ち、後段で端末ごとに 32 個ある送信役の空きを待ちます。どちらかの待ち行列が満杯で入れなかったプッシュと、送信役を 30 秒を超えて待ったプッシュは捨てます（30 秒は後段に入ってから数えます）。捨てたものは `web push: ... dropped` としてログに残します

## 困ったとき

| 見えること | 原因と直しかた |
|---|---|
| 設定に項目が出ない | Web Push がオフか、設定が不正です。`AF_WEB_PUSH`・鍵・連絡先と、起動時のログを確かめます |
| 端末は登録されるが何も届かない | プッシュのホストへの外向き HTTPS が塞がれています。Control Plane のネットワークから試します。iPhone では、ホーム画面のアイコンから Console を開いている必要があります |
| どの端末にも「プッシュ用の鍵が変わる前に設定されています」と出る | 秘密鍵を入れ替えました。購読はすべて無効になる仕様で、メンバーが端末ごとにオフ・オンし直します。鍵は変えないでください |
| どのブラウザでも「このブラウザはプッシュ通知を受け取れません」と出る | Console が HTTPS で配信されていません。ブラウザは安全なオリジンでしかプッシュを使わせないので、`PUBLIC_BASE_URL` を `https://` にします |
| 見慣れないホストで登録した端末に届かない | そのホストを `AF_WEB_PUSH_ENDPOINT_HOSTS` に足します。購読は消さずに残してあるので、許可すれば届きます |

## 関連

- [通知を受け取る](../../user/settings/notifications.ja.md)：メンバーの設定
- [スマートフォンで使う](../../user/start/phone.ja.md)：Console をホーム画面に追加する
- [外向きの通信を制御する](../secure/egress.ja.md)：ワークスペースの通信。こことは別
