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

2. Control Plane の環境（compose なら `.env`）に次の変数を入れ、再起動します。compose では `docker compose restart` は `.env` を読み直しません。`docker compose up -d` で Control Plane のコンテナを作り直してください

   | 変数 | 意味 |
   |---|---|
   | `AF_WEB_PUSH=true` | Web Push をオンにする。これ以外の値ではオフのまま |
   | `AF_WEB_PUSH_VAPID_PRIVATE_KEY` | 手順 1 の鍵。必須。ほかの配備の秘密と同じく秘密にする。公開鍵はここから導かれる |
   | `AF_WEB_PUSH_SUBJECT` | プッシュサービスへの連絡先。`mailto:you@example.com` か `https://` の URL。必須。ほかの形だと、ログに警告を出して Web Push はオフのまま |
   | `AF_WEB_PUSH_ENDPOINT_HOSTS` | 任意。受け付けるプッシュサービスのホストを足す。カンマ区切りで、先頭のドットはサブドメイン全部（`.push.example.com`） |
   | `AF_NOTIFICATION_DRAIN_INTERVAL` | 任意。既定は `15s`。動いているワークスペースの通知を Control Plane が集める間隔。Console を開いていなくても、質問がスマートフォンに届くようにする。Web Push がオンのときだけ動く |

3. ログに `web push enabled` が出ているか確かめ、メンバーとして設定 →「通知」を開き、端末のプッシュをオンにします（[通知を受け取る](../../user/settings/notifications.ja.md#スマートフォンにプッシュ通知を受け取る)）

`AF_WEB_PUSH=true` なのに鍵や連絡先がない・形が違うときは、Control Plane はログに警告を出し、Web Push をオフにして動き続けます。止まりはしません。

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

## 困ったとき

| 見えること | 原因と直しかた |
|---|---|
| 設定に項目が出ない | Web Push がオフか、設定が不正です。`AF_WEB_PUSH`・鍵・連絡先と、起動時のログを確かめます |
| 端末は登録されるが何も届かない | プッシュのホストへの外向き HTTPS が塞がれています。Control Plane のネットワークから試します。iPhone では、ホーム画面のアイコンから Console を開いている必要があります |
| どの端末にも「プッシュ用の鍵が変わる前に設定されています」と出る | 秘密鍵を入れ替えました。購読はすべて無効になる仕様で、メンバーが端末ごとにオフ・オンし直します。鍵は変えないでください |
| 見慣れないホストで登録した端末に届かない | そのホストを `AF_WEB_PUSH_ENDPOINT_HOSTS` に足します。購読は消さずに残してあるので、許可すれば届きます |

## 関連

- [通知を受け取る](../../user/settings/notifications.ja.md)：メンバーの設定
- [スマートフォンで使う](../../user/start/phone.ja.md)：Console をホーム画面に追加する
- [外向きの通信を制御する](../secure/egress.ja.md)：ワークスペースの通信。こことは別
