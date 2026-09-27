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

→ `consoleLoginWaits` に claude = 90 秒を入れた（`workspace/agent/aws_exec.go`）。未測定の kind は
`consoleLoginUnmeasuredWait`（5 秒）を使う。実装レビューで、最初の版は未測定の kind にも claude の 90 秒を
使っていたと指摘された。90 秒より短いタイムアウトで出力を捨てる kind があれば、その kind のエージェントには
何も伝わらないので、未測定の kind は 5 秒に改めた。

### 1.2 ダミーの開始 URL ではデバイス認可まで進めない（決定 3）

実在しない開始 URL（`https://d-9067000000.awsapps.com/start`）を持つ sso-session で
`aws sso login --use-device-code --no-browser` をパイプにつないで実行した。

- `StartDeviceAuthorization` が `InvalidRequestException`（`invalid_request`）で断り、exit 254 で終わった。
- URL もコードも出ないので、パイプへの書き出しと表示ホストは、この方法では測れない。

### 1.3 パイプへの書き出し（決定 3・実装レビューで測定）

偽の OIDC エンドポイント（`AWS_ENDPOINT_URL_SSO_OIDC`）と隔離した HOME を使い、`aws sso login --use-device-code
--no-browser` の標準出力を、時刻を付けて読むパイプにつないだ（aws-cli 2.36.46）。

- デバイス認可から 50 ms 以内に、次の 4 行がパイプに届いた。プロセスが終わる約 5 秒前で、CLI が /token を
  ポーリングしている間に書き出している。
  - 「Please visit…」
  - `https://device.sso.ap-northeast-1.amazonaws.com/`
  - `ABCD-EFGH`
  - `…?user_code=ABCD-EFGH`
- 素の URL が先、user_code 付きの URL が最後に出る。`DeviceAuthorization` はどちらの順でも扱える。

## 2. 測れていないもの

- **本物のポータルが表示する URL のホスト**（決定 3）: 偽の OIDC は、`sso_region` から組み立てた
  `device.sso.<region>.amazonaws.com` を返した。本物のポータル、特に新しいドメインの開始 URL で同じになるかは、
  受け入れ実行（#1026 の Acceptance）で確かめる。いまの許可ホストは次の 2 つ。
  - `device.sso.<sso_region>.amazonaws.com`（中国は `.amazonaws.com.cn`）
  - 開始 URL のホスト
- **claude 以外の kind のシェルコマンドのタイムアウト**（決定 5）: 未測定。未測定の kind と、kind の分からない
  呼び出し元は 5 秒待つ。どのツールのタイムアウトにも切られない短さで、依頼を出して表示し、exit 3 で終える。
  測った kind だけが、その値を使う（今は claude の 90 秒）。→ #1036

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
