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

### 1.1.1 claude 以外の kind（決定 5・#1036）

各 CLI を非対話モードで起動し、次のプローブを既定のタイムアウトのまま実行させた（2026-09-27）。

- プローブは乱数の nonce を付けた 1 行を出し、1 秒ごとの時刻と TERM/HUP/INT/PIPE の受信をファイルに書き、
  400 秒で終わる。最後に `after-<nonce>` を出す。
- 打ち切りの時刻は、そのファイルの最後の行で見た。出力が渡ったかは、CLI のイベント（JSON）のツール結果に
  nonce があるかで見た。モデルの説明文は根拠にしていない。
- プローブの子が `AF_SESSION_NAME` を継がないよう、`env -u` で外して起動した。

| kind | 版 | 既定のタイムアウトと、そこで起きること | 出力 | 待ち |
|---|---|---|---|---|
| codex | 0.157.1 | 10 秒で途中までの出力を返し、コマンドは走り続ける（`session_id` でポーリングでき、最後の結果も届いた） | 残る | 90 秒 |
| opencode | 1.18.32 | 120 秒で SIGTERM（`exceeding timeout 120000 ms`） | 残る（途中までの出力に注記が付く） | 90 秒 |
| copilot | 1.0.88 | 30 秒で途中までの出力を返し、コマンドはバックグラウンドで走り続ける（終わると通知） | 残る | 90 秒 |
| cursor | 2026.09.26 | `timeout: 30000`・`TIMEOUT_BEHAVIOR_BACKGROUND`（`hardTimeout` は 24 時間） | 残る | 90 秒 |
| kiro | 2.16.0 | 400 秒以内には切られない | 切られないので問わない | 90 秒 |
| muse | 1.3.0 | Managed（`muse serve`）: `execution_state: background_running` で途中までの出力を返し、コマンドは走り続けて最後の出力も届いた。`muse exec` では 400 秒以内に切られない | 残る | 90 秒 |
| lcpp | （自前） | `harness/tools_bash.go` の既定 300 秒。時間切れでも途中までの出力に注記を付けて返す（`TestBashTimeoutKeepsPartialOutput`） | 残る | 90 秒 |
| agy | 1.2.11 | 400 秒以内には切られない（`run_command` は 405 秒で完了し、両方の行を返した） | 切られないので問わない | 90 秒 |

- どの kind も、タイムアウトが 90 秒より長いか、途中までの出力を残す。よって、すべて 90 秒にした。
- kiro と agy は 400 秒のプローブが最後まで走ったので、タイムアウトで切られたときに出力が残るかは
  測れていない。決定 5 はタイムアウトが 90 秒より長い kind に出力の扱いを問わないので、値は変わらない。
- 測ったのは各 CLI の非対話モード（`codex exec`・`opencode run`・`copilot -p`・`cursor-agent -p`・
  `kiro-cli chat --no-interactive`・`agy -p`）で、製品が起動する経路（Managed の app-server や `serve`、
  Terminal (CLI) の TUI）そのものではない。シェルツールは同じ実装なので既定値も同じと見ているが、経路ごとには
  確かめていない。muse だけは、レビューの指摘で製品の経路（Managed のセッションを立ててプローブを実行）でも測った。
- agy は、この機械（RDRAND が壊れたホスト）では素のままだと `CRNGT failed` で止まる。製品と同じマスク
  （`agents/agy/fips.go` の `OPENSSL_ia32cap=~0x4000000000000000`）を付けて測った。
- copilot は、作業フォルダの外にあるスクリプトを `--allow-all-paths` 無しでは拒んだ（`Permission denied and
  could not request permission from user`）。タイムアウトとは関係が無い。
- codex と copilot と cursor は、10〜30 秒でエージェントに制御を返す。このとき af-aws-exec はまだ待っていて、
  エージェントが見るのは最初の「Console でログインを頼んだ」の行だけ。決定 5 がこの行を待つ前に出すのは、
  このため。

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
- **シェルコマンドのタイムアウトが無い呼び出し元**（決定 5）: kind の分からない呼び出し元と、shell/ssm の
  セッションは 5 秒待つ。どのツールのタイムアウトにも切られない短さで、依頼を出して表示し、exit 3 で終える。

## 3. 偽の aws で確かめたこと

`workspace/agent/internal/awsx/login_test.go`（11 本）。

- 承認されるまで待ってから実行する。
- 時間切れのときは、依頼を残したまま exit 3 で終わる。2 本目の実行は同じ依頼に加わり、通知は 1 件のまま。
- Settings のプロファイルでない場合、`--no-login` の場合、ワークスペースの外の場合は、依頼を出さない。
- キャンセルすると、待っている実行はすぐ終わる。抑止中は依頼を出さない（抑止は当初 10 分、改訂で 1 分。§4）。キャッシュが変われば抑止は無効になる。
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

## 4. 実機での初回（2026-09-27）とキャンセル抑止の改訂

claude のセッションから `af-aws-exec --profile acrt --account … -- aws sts get-caller-identity` を実行した。

- 依頼が出て、Agent の一覧に Settings のアカウントとロール付きで載った。
- 90 秒待って exit 3（「Console で依頼した」）で終わった。
- その 2 秒後に、利用者が「依頼を取り消す」を押した。このときは、使っていた端末のブラウザからサインインできなかった。
- URL のホストは、この回では確かめられていない（§2 のまま）。

利用者の指摘:「環境を間違えて、別の PC のブラウザですぐログインしたいとき、10 分の抑止は困る」。

- 抑止の間は、Console のどこからもログインを始められない。依頼は消え、新しい依頼も出ない。そのため利用者は
  端末のコマンドを使うしかない。
- 実はこの場面では取り消す必要が無かった。依頼はワークスペース全体のものなので、「閉じる」だけにすれば、別の
  PC の Console にも同じトーストが出る。ただ、モーダルはそれが分かる作りになっていなかった。

直し方（ADR 0102 の Revision）:

- 抑止を 1 分にした。抑止の目的は、エージェントがすぐ再実行したときにトーストが戻るのを防ぐことだけ。
- モーダルに「閉じても依頼は残り、ほかの端末の Console からもログインできる。取り消すのはこのログインを
  しないときだけ」と書いた。
