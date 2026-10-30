# docs/79 — 設定の書き出し / 取り込み

決定は [ADR 0060](../decisions/0060-settings-export-import.ja.md)。利用者の設定を **1 個の JSON**
にまとめて持ち出し、別のデプロイ / 別テナント / 新しいアカウントで読み戻せるようにする。

## 0. きっかけと、範囲を広げた理由

最初の依頼は「**SSM の設定を export / import したい**」だった。SSM の登録（プロファイルと
ホスト）は手で入れるには重く、環境を移るたびに入れ直しているのだから、当然の要望である。

ただし SSM だけに付けると、同じ形の要望が来るたびにボタンが増える。移りたいのは SSM では
なく **「自分の環境」** で、SSM はその一部でしかない（表示の好み、キー割当、エージェントの
既定モデル、エージェントへの指示…）。器を先に 1 つ作り、SSM をその 1 カテゴリとして載せる
方が安い。範囲は利用者と相談して **個人設定 ＋ SSM ＋ エージェントへの指示**（秘密は常に
除外）に確定した。

## 1. 設定はどこに置かれているか（範囲を決めた根拠）

| 層 | 中身 | 経路 | 判断 |
|----|------|------|------|
| ui-prefs（Workspace home の 1 blob） | 表示・キー操作・通知・エージェント既定・アシスタント・読み上げ・作業グループ・返信候補 | `GET/PUT api/env/ui-prefs` | **入れる** |
| CP DB（メンバー単位） | SSM プロファイル / ホスト | `api/ssm/profiles`・`api/ssm/hosts` | **入れる** |
| Agent home の 1 ファイル | ユーザー指示（`~/.config/agent-fleet/user-notes.md`） | `api/user-notes` | **入れる** |
| CP DB（暗号化） | 接続（Git / エージェント / AWS / チャットのトークン・API キー） | — | **入れない**（§2） |
| Workspace | 環境変数・ツールチェーン | `api/env/ws-settings`・`api/env/toolchains` | 今回は入れない（環境変数に秘密が混ざる） |
| CP DB | MCP サーバー登録 | `api/mcp-servers` | 今回は入れない（`headers_enc` に秘密） |
| Agent | エージェントメモリ | 既存の書き出し / 取り込み（[docs/39](39-agent-memory-management.md)） | 既存に任せる |
| CP DB | 内部リポジトリ登録 | `api/internal-git/repos` | 今回は入れない |

後半 4 つは器ができた後にカテゴリを足すだけで載る。**バンドルの形式（§3）はそれを見越して
「セクションの集合」にしてある** —— 知らないセクションが増えても、古い Console はそれを
読まないだけで壊れない。

## 2. 秘密を入れない

このファイルは「設定ファイル」として気軽に扱われる —— メールで送られ、チャットに貼られ、
リポジトリにコミットされる。1 つでも秘密が混ざると、その扱いの軽さが**そのまま漏洩の経路**
になる。だから接続は含めず、移った先ではもう一度サインインしてもらう。画面にもそう書く
（`backup.secrets_note`）。

副次的な効果として、**バンドルは人に渡せる**。「うちのチームの SSM 登録一式」を配る、が
そのまま成立する。

## 3. バンドルの形式（v1）

```json
{
  "kind": "agent-fleet-settings",
  "version": 1,
  "exportedAt": "2026-08-26T06:00:00.000Z",
  "sections": {
    "prefs": { "termSize": 15, "locale": "ja", "...": "..." },
    "ssm": {
      "profiles": [{ "label": "prod", "startUrl": "https://…/start", "ssoRegion": "…", "accountId": "", "roleName": "", "region": "" }],
      "hosts":    [{ "alias": "mng@web-01", "profile": "prod", "instanceId": "i-…", "documentName": "", "region": "" }]
    },
    "instructions": { "text": "…", "enabled": true, "targets": { "claude": true, "codex": false } }
  }
}
```

要点は 2 つ。

**① ホストはプロファイルを「表示名」で参照する。** CP の id は環境ごとに違うので、id のまま
運ぶと取り込み先で必ず張り替えが要る（しかも張り替えを忘れると、他人のプロファイルを指す
可能性すらある）。表示名は `~/.aws` のプロファイル名の素でもあり、人が見て分かる自然キー
なので、**形式の側で id 問題を消す**。

**② 版は前方互換にしない。** 知らない `version` は読まずに断る。部分的に読めるふりをすると
「何が入って何が入らなかったのか」を利用者が確かめる手段が無くなる。

## 4. 取り込みの規則

### 4.1 個人設定 — 重ねる。空で潰さない

ui-prefs は **state をまるごと PUT・最後の書き手が勝つ**（`console/src/lib/settings.ts`
`scheduleServerSave`）。2026-08-18 に、この性質と hydrate の無言失敗が重なって
**全端末の学習済み返信候補が消える事故**が起きている。取り込みは「他所から来た blob を
一発で書き込む」操作なので、素直に実装すると同じ穴をもう一度開ける。

そこで 2 段にした（`console/src/lib/settingsBundle.ts`）。

- `sanitizeImportedPrefs` — **この Console が知っているキーで、既定値と形が合う値**だけを残す。
  他版が書いたキー・手編集で型が壊れた値は落とし、件数を画面に出す。
- `mergeImportedPrefs` — 現在値へ**重ねる**。累積データ（返信候補・キー割当・作業グループ・
  読み仮名辞書・ホスト色…、`ACCUMULATED`）は **空で上書きしない**、オブジェクトは**足し算**
  にする。配列の累積は中身の同一性を判定できないので置き換えるが、空配列では置き換えない。

適用は `setSettings(patch)` 1 回。現在の state に重なった結果が保存されるので、まるごと PUT
の性質はそのままに、**取り込みが引き算になることだけ**を消している。

### 4.2 SSM — 足すだけ

- プロファイルは**表示名**（trim + 大文字小文字無視）が一致すれば既存とみなし、作らない・
  書き換えない。
- ホストは **alias ＋ インスタンス id** が一致すれば既存とみなす。
- 参照先プロファイルが「既存 ∪ この取り込みで作る分」に無いホストは `no_profile` でスキップ。
- CP の `validateProfile` / `validateHost` と同じ最低条件（label / `https://` の start URL /
  SSO リージョン、alias / instance id）を**画面側でも先に**見る。ここで落としておかないと 400 が
  並ぶだけで、何件入ったのかが利用者に見えない。
- 作る順は **プロファイル → ホスト**。逆順ではホストの参照先が存在せず必ず 400 になる。

既存を書き換えないのは、設定の移送で既存環境を削るのが割に合わないから。同名で中身が違う
プロファイルは「既にある」と報告してスキップする —— どちらが正しいかは Console には判断
できず、上書きが正解であることは滅多にない。

### 4.3 エージェントへの指示 — 置き換え（確認する）

`user-notes` は 1 本の文章なのでマージのしようがない。取り込みは置き換えになるため、確認
ダイアログで**そう明示する**。Agent が持つ層なので、**ワークスペース停止中は選ばせない**
（読めない・書けないものを選べると「入ったつもり」になる）。

## 5. 画面

設定モーダル > ワークスペース > **書き出し / 取り込み**（`features/settings/BackupTab.tsx`）。

- 書き出し: カテゴリのチェックリスト（各行に件数）→ `af-settings-YYYYMMDD-HHmm.json` を
  ダウンロード。
- 取り込み: ファイル選択 → **中身の要約**（書き出し日時・カテゴリごとの件数）→ 取り込む
  カテゴリを選ぶ → 確認 → 適用。
- 結果は件数の箇条書きで残す。**入らなかった数を同じ行に出す**（`res_prefs` の「対象外」、
  `res_ssm` の「既存・対象外」）—— 黙って減っているのが一番たちが悪い。

書き出しと取り込みで**同じ見た目のチェックリスト**を使っているのは、「何が入っている
ファイルなのか」を書き出したときと同じ並びで確かめられるようにするため。

## 6. サーバ側の変更はゼロ

必要な REST は全て既にある（`api/env/ui-prefs`・`api/ssm/*`・`api/user-notes`）。新しい REST を
足すと CP のプロキシ許可リスト（`control-plane/routes.go`）と Agent の両方に入れる必要があり、
片方だけ入れて 404 にするのは何度も繰り返している事故（[dev/05](../build/05-api.ja.md)）。
**フロント完結で足りるならそれが最も安い**。

原子性は諦めている（プロファイルを 3 件作った後にホストで失敗し得る）。CP に一括取り込み
API を足せば原子的にできるが、失敗しても**もう一度同じファイルを取り込めば残りだけが入る**
（足すだけ・既存はスキップ）ので、冪等性で足りると判断した。

## 7. 実装

| 置き場 | 役割 |
|--------|------|
| `console/src/lib/settingsBundle.ts` | バンドルの組み立て・読み取り・取り込み計画（純ロジック。fetch も React も import しない） |
| `console/src/lib/settingsBundle.test.ts` | 上の全規則（node プロジェクト・17 ケース） |
| `console/src/features/settings/BackupTab.tsx` | 画面と REST 呼び出し |
| `console/src/features/settings/BackupTab.dom.test.tsx` | 停止中ゲート / 要約の出し分け / 作成順と既存スキップ |
| `console/src/lib/settings.ts` | `settingsDefaults()` を追加（既知のキーと形の単一の真実源） |

`settingsBundle.ts` が settings.ts を import していないのは意図的で、settings.ts は
localStorage に触るため node のテストから import できない（混ぜると全部 DOM テストになる）。
既定値と累積キーの判定は呼び手から渡す。

## 8. 残り

- 実機目視（実フリート再ビルド後）。
- カテゴリの追加余地: 環境変数 / ツールチェーン / MCP 定義 / 内部リポジトリ登録。いずれも
  **秘密の混入判定**（マスクするか、検出して除外を知らせるか）を先に決める必要がある。

## 9. 手元の ~/.aws/config を読む（#3010）

決定は [ADR 0060 の追記](../decisions/0060-settings-export-import.ja.md#追記2026-10-30-手元の-awsconfig-を読む3010)。
AF へ移る妨げとして「作り込んだ AWS プロファイルを設定画面で 1 つずつ手入力するのが面倒」という声が出た。
§0 のバンドルは AF が書き出したものしか読まないので、手元の PC から来る人には効かない。

### 9.1 流れ

| 段 | 置き場 | 役割 |
|----|--------|------|
| 読む | `console/src/lib/awsConfigIni.ts` | configparser（strict）と同じ読み方。値をエラーに入れない |
| 意味づけ | `console/src/lib/awsConfigImport.ts` `readAwsConfig` | botocore のセクション対応・`[DEFAULT]`・sso-session の解決、認証方式の分類と Agent 許可リストでの検証 |
| 解決 | 同 `resolveImport` | 既存行・プレビューの編集（選択・改名・duration・引き受け元の選択）と突き合わせ、名前の衝突を見る。編集のたびに走り直す |
| 計画 | `settingsBundle.ts` `planSsmImport` | バンドルと同じ「足すだけ」の最終段 |
| 実行 | `console/src/features/settings/workspace/ssmImportExec.ts` | 一覧を読めなければ送らない・元が失敗したら依存を送らない・応答なしで止まる。バンドル取り込みと共用 |
| 画面 | `AwsConfigImport.tsx`（AWS タブ）、バックアップタブからは `openSettings("ssm:import")` | |

### 9.2 実測と根拠

- 読み方の期待値は Python の `configparser.RawConfigParser` に同じ入力を与えて取った（継続行、`[DEFAULT]` の
  再開は可・キー重複は不可、行中の `#` は値の一部、BOM は CLI も読めない）。`awsConfigIni.test.ts` がその値。
- 継続行の危険：`sso_role_name = Admin` の次の字下げ行 `aws_secret_access_key = …` は role 名の続きになる。
  Agent はこれを不正な値として**値ごと**ログに出す（`awsx/profiles.go` `InvalidReason`）ので、CP の最低条件
  （空でなければ通す）では止まらない。Agent の許可リストで弾く。
- 既存の取り込みは GET の失敗（`api()` はエラー本文を返す）を「0 件」と読み、CP にラベルの一意制約がないため
  全部を二重に作っていた。実行部を分けて直した。
- 変異確認：GET のガード・混在認証の拒否・未選択の引き受け元・role 名の許可リスト・継続行・依存の送信止めを
  1 つずつ壊し、それぞれ新しいテストが落ちるのを見た。
- sso-session の解決は botocore（`credentials.py` `_resolve_sso_session_reference`）に合わせた：セッションの
  **全キー**をプロファイルに合わせ、両方にあって値が違えばエラー。`[DEFAULT]` は両側に届くので、プロファイルだけで
  region を変えると食い違いになる。値は trim しない（次の行から始まる値は botocore では入れ子の表で、trim すると
  その 1 行目がもっともらしいスカラーになる）。

### 9.3 残り

- 実機での目視（実フリート反映後）。
- ログインの共有（同じ sso-session の 20 個が 20 回のログインになる）：#3014。
- `~/.ssh/config` の SSM ホスト・gcloud の構成・`~/.aws/config` への書き出し：#3017。

### 9.4 gcloud の構成を読む（#3017 第 2 部、2026-10-30）

`~/.config/gcloud/configurations/config_<名前>` を、AWS タブの取り込みと同じ型で Google Cloud タブに足した
（`GcpConfigImport.tsx`）。ファイルはブラウザの中だけで読み、送るのは既存の `POST api/gcp/profiles` へ
プロファイルの 6 項目（project / quotaProject / account / region / zone / impersonateServiceAccount）と label だけ。
新しい REST は足していない。

| 段 | 置き場 | 役割 |
|----|--------|------|
| 読む | `awsConfigIni.ts` `parseIni(text, false)` | gcloud も `configparser.ConfigParser()`（`core/configurations/properties_file.py`）なので同じ読み方。AWS 専用の「入れ子の値」検査だけを外す。`parseAwsIni` の挙動は変えていない |
| 意味づけ・検証 | `console/src/lib/gcloudConfigImport.ts` `readGcloudConfig` | 対応する 6 項目の取り出し、対象外の判定、CP の `validateGCPProfile` と同じ正規表現での事前検証 |
| 解決 | 同 `resolveGcpImport` | 既存行（label か CP が返す `name` が同じ）と突き合わせ、新規 / 登録済み / 取り込めない（理由つき）に分ける |
| 実行 | `gcpImportExec.ts` | 一覧を読めなければ送らない・CP が断った行は 1 行だけの失敗・応答が来なければ止める（ssmImportExec と同じ規則） |

- **対象外にして理由を出すもの**：`auth/` の空でないキー（`credential_file_override`・`access_token_file`・独自の
  OAuth クライアントやホスト、未知のキー。`disable_credentials = false` と空の値は通す）、`impersonate_service_account`
  のカンマ区切り（委任チェーン）、`[proxy]`・`[api_endpoint_overrides]`、`core/universe_domain`（既定以外）、
  `core/custom_ca_certs_file`、`core/account` がサービスアカウント（鍵なしではログインできない。CP は通すが
  取り込むと動かない行になる）、project 無し。`[DEFAULT]` から継がれたものも同じ。
- 理由に出すのはキー名（既知のものだけ。未知は `?`）と行番号。値、パス、トークンファイルは表示も送信もしない。
  DOM テストが `credential_file_override`・`access_token_file`・未知のキーと値・`[proxy]` に歩哨文字列を置いて確かめる。
- label は gcloud の構成名。取り込みが作る label は `^[a-z][a-z0-9]*(-[a-z0-9]+)*$` に絞った。CP の `gcpProfileName`
  が label そのものを返す範囲なので、Console が名前を計算し直さずに衝突を見られる（Console は CP の `name` を
  導出しない、という GcpTab の規則を保つ）。
- 変異確認：一覧の GET のガードと `auth/` の拒否を 1 つずつ壊し、新しいテストが落ちるのを見た。
- gcloud は `ConfigParser()` の既定（BasicInterpolation）で読むので、値に `%` を含むファイルは展開結果が読めない。補間は実装せず、ファイル全体を理由つきで断る（誤ったアカウントを取り込まないため）。`disable_credentials` は gcloud の真偽値の綴りだけ通し、不明な綴りは断る。構成名は gcloud の規則（末尾 `-` も有効）に合わせ、label の段階で直させる。5xx は作成結果不明として止める（CP は INSERT 後の一覧再読込で 500 を返しうる）。

残り：実機での目視（#3017 は第 1 部 ssh-config が別 PR）。この PR は実フリートでは未確認。

### 9.5 `~/.ssh/config` から SSM ホストを読む（#3017 第 1 部、2026-10-30 追記）

決定は ADR 0060 の同日付の追記（`~/.ssh/config`）。

| 段 | 置き場 | 役割 |
|----|--------|------|
| 読む | `console/src/lib/sshConfigIni.ts` | ssh_config(5) の行形式（`Keyword args` / `Keyword=args`、`Host` / `Match` がブロックを開く、継続行なし）。AWS の INI とは別の読み手。ブロックはファイル順のまま持つ |
| 意味づけ | `console/src/lib/sshConfigImport.ts` `readSshConfig` | ホスト名ごとに、当てはまるブロックを順に通して最初の値を取り（OpenSSH と同じ）、`ProxyCommand` を固定文法で検証して**捨てる**。残るのはインスタンス ID・プロファイル名・リージョンだけ |
| 解決 | 同 `resolveSshImport` | Settings のプロファイルと AWS 名（`awsProfileName`）で突き合わせ、別名の重複・既存を見る。編集のたびに走り直す |
| 計画・実行 | `planSsmImport` / `ssmImportExec.ts` | 第 1 段と共用。ホストだけのプランを流す |
| 画面 | `SshConfigImport.tsx`（`AwsConfigImport.tsx` には触れず別部品） | AWS タブのボタン |

**受け付ける形（これだけ）。** 具体的な 1 名の `Host`、`ProxyCommand` が `aws ssm start-session`
（`sh -c "…"` で包んだ形も可）で、`--target`（`i-…` / `mi-…`、または `%h` = `HostName` の値、なければ `Host` 名）・
`--document-name AWS-StartSSHSession`・`--parameters portNumber=%p`（または 22）・`--profile`・`--region` を各 1 回まで。
拒否して理由を出すもの：他のドキュメント、他のオプション、他の `%`、シェル構文、`ProxyJump`、
`CanonicalizeHostname`、ワイルドカード・否定（行として 1 つずつ表示）、`Match`・`Include`。

**first-match-wins の扱い。** 上にある `Host *` やパターンから来た値は拒否（`inherited`）。`ProxyCommand` / `ProxyJump` /
`HostName` / `CanonicalizeHostname` を設定する `Match` や、`Include` より後ろの値も拒否（`barrier`、中身が見えないため
先に決められた可能性が消せない）。これらを設定しない `Match` と、値より後ろの `Match` / `Include` は影響しない。
ホスト名の比較は大文字小文字を区別する（ssh_config(5) の通り。畳むと否定パターンの一致まで増え、本来効く先行ブロックを落とす）。Host の単一引用符・バックスラッシュは OpenSSH が別に解釈するのでファイルごと拒否する。`%h` で HostName がなく、後ろに HostName を設定しうる Match / Include があるときも拒否する。行数の上限は評価の前に数える。

**接続方式の変更。** トンネル（`AWS-StartSSHSession`）をシェルのブックマークにするので、作る行すべてに
「SSH トンネルではなく SSM シェル」と表示し、プレビューの下に注意を出す。`User` / `IdentityFile` / `Port` などは名前だけ出す（値は出さない）。

**値を出さない・送らない。** ProxyCommand は実行しない。読み取り部で捨て、プレビュー・リクエスト・ログのどこにも
出ない（DOM テストで、ProxyCommand 内のトークン・IdentityFile のパス・`SetEnv` の鍵がどこにも現れないのを確かめた）。

変異確認：`inherited` の判定と未知オプションの拒否を外すと、該当するテストが落ちるのを見た。
実機では未確認（実フリートでの目視は残り）。

残り：gcloud の構成の取り込み（第 2 部）、`~/.aws/config` への書き出し（第 3 部）：#3017。

### 9.6 `~/.aws/config` として書き出す（#3017 第 3 部、2026-10-30 追記）

決定は ADR 0060 の同日付の追記（書き出し）。

- 置き場：`console/src/lib/awsConfigExport.ts`（純粋な描画器 `renderAwsConfig`）、`AwsConfigExport.tsx`（AWS タブのボタン）。
  入力は `GET api/ssm/profiles` の行。新しいエンドポイントなし、送信なし。
- **検証してから書く。** CP は SSO 行の項目を Agent より緩く見る（開始 URL は `https://\S+` で通る）ので、保存値を
  そのまま連結すると鍵やセクションを紛れ込ませられる。値はすべて Agent の許可リスト（`agents/ssm/config.go`）で
  確かめ、通らない行は除外して項目名だけを理由に出す（値は出さない）。さらに出力の全行が描画器の書く形のどれかに
  合うことを `assertSafe` で確かめ、合わなければ投げる（ダウンロードさせない）。
- **除外の規則は Agent と同じ。** `default`（予約）、同じ AWS 名になる複数ラベルは全部除外
  （`aws_profiles_bridge.go`）、アカウント・ロールの片方または両方がない SSO 行（`IncompleteReason`）、
  引き受け元が除外・不在・SSO でないロール行。理由はすべて画面に出す。
- **置き換えと取り違えない。** ファイル名は `agent-fleet-aws-profiles.aws-config-fragment.txt`（`config` ではない）、
  先頭コメントと画面に「手で取り込む断片で置き換えではない」と書く。ラベルはファイルに入れない（コメントにも）。
- **ADR 0125（共有サインイン）の足場。** sso-session の名前の決め方は `sessionOf()` の 1 か所で、`[sso-session]` は名前ごとに
  1 回だけ出す。サインインを共有する行が入ったら、この関数を変えるだけで共有セッションの書き出しになる（今は実装しない）。
- 往復：描画結果を第 1 段の読み手に通すと、同じプロファイルが「作成」と判定されるのをテストで確かめた。
  変異確認：許可リストの検査を外すと 8 件が落ちる。実機では未確認。
