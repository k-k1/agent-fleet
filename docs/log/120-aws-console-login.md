# 120. af-aws-exec の SSO ログインを Console で頼む（ADR 0102 の実装と実測）

- 依頼: #1026。設計は [ADR 0102](../decisions/0102-aws-login-through-the-console.ja.md)（レビュー 7 巡で指摘 0）。
- 版: aws-cli 2.36.46。
- この記録は、ADR 0102 決定 3 と決定 5 が「実装を受け入れる前に測る」とした項目の置き場。

## 1. 測れたもの

### 1.1 claude の Bash ツールのタイムアウト（決定 5）

`echo first-line-before-wait; sleep 20; echo after` を、タイムアウト 3 秒で実行した（2026-09-27）。

- 3 秒で「時間内に終わらなかったので background に移した」と返り、コマンドは殺されなかった。
- 出力はファイルに残り、終了時に通知が来た。中身は `first-line-before-wait` と `after` の 2 行で、
  どちらも欠けていない。
- 既定のタイムアウトは 120 秒。90 秒の待ちはその内側に収まる。越えても、出力は失われない。

→ `consoleLoginWaits` に claude = 90 秒を入れた（`workspace/agent/aws_exec.go`）。

### 1.2 ダミーの開始 URL ではデバイス認可まで進めない（決定 3）

実在しない開始 URL（`https://d-9067000000.awsapps.com/start`）を持つ sso-session で
`aws sso login --use-device-code --no-browser` をパイプにつないで実行した。

- `StartDeviceAuthorization` が `InvalidRequestException`（`invalid_request`）で断り、exit 254 で終わった。
- URL もコードも出ないので、パイプへの書き出しと表示ホストは、この方法では測れない。

## 2. 測れていないもの

- **パイプへの書き出しと、表示される URL のホスト**（決定 3）: 本物の IAM Identity Center の開始 URL が要る。
  利用者のポータルでデバイスコードを発行する操作なので、受け入れ実行（#1026 の Acceptance）で測る。
  現在の許可ホストは、`device.sso.<sso_region>.amazonaws.com`（中国は `.amazonaws.com.cn`）と、開始 URL のホスト。
- **claude 以外の kind のシェルコマンドのタイムアウト**（決定 5）: 未測定。未測定の kind は「分からない」扱いで、
  測った中で最も短い待ち（今は claude の 90 秒）を使う。→ #1036

## 3. 偽の aws で確かめたこと

`workspace/agent/internal/awsx/login_test.go`（11 本）。

- 承認されるまで待ってから実行する。
- 時間切れのときは、依頼を残したまま exit 3 で終わる。2 本目の実行は同じ依頼に加わり、通知は 1 件のまま。
- Settings のプロファイルでない場合、`--no-login` の場合、ワークスペースの外の場合は、依頼を出さない。
- キャンセルすると、待っている実行はすぐ終わる。10 分の抑止中は依頼を出さない。キャッシュが変われば抑止は無効になる。
- 期限内でも拒否されるトークンは、ログインとみなさない。
- 確認の間にログインが済んだら、記録せずに取り直す。
- 一覧の文言は Settings から取り、依頼元の文字は切り詰める。
- ホストは名前全体の一致で比べる。
- 試行が置き換えられたら、前の試行のコードは消す。
- 試行が走っている間は、依頼は失効しない。

変異テストの結果は次のとおり。

- ホストの比較を前方一致にすると、2 本が落ちる。
- 「片付いた」の判定を「期限内」だけにすると、1 本が落ちる。
- キャッシュの状態を確認の後に読むと、1 本が落ちる。
