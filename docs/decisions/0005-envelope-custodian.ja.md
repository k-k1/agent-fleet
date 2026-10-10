# 0005. at-rest 鍵 — 封筒暗号 + custodian 抽象（on-prem の限界を明記）

[English](0005-envelope-custodian.md) | 日本語

- 状態: 確定（P3-3）
- Follow-ups: #1645, #1646, #1956
- 関連: [history/p3-3-envelope-crypto](../log/p3-3-envelope-crypto.md) / [dev/07 §7.6 シークレット管理と封筒暗号](../build/07-security.ja.md#76-シークレット管理と封筒暗号)（旧 security §4.4） / [ロードマップ §12.3](../log/roadmap.md#123-tos-と分離の留意自社ホスト前提)

## 背景

Phase 2 A3 の鍵は単一 `AF_MASTER_KEY`(env) → `HMAC(SHA256(master), userKey)` を `AF_SECRET_KEY` として
注入していた。これは master が単一障害点で、テナント単位の鍵ローテ/失効ができず、鍵が CP env に常在する。
セルフホスト製品（[0001](0001-self-host-vs-saas.ja.md)）では「その社が鍵を握り、オフボードでクリーンに失効できる」
ことが要る。

## 決定

**封筒暗号 + custodian 抽象へ昇格。** per-workspace の DEK を per-tenant KEK で wrap し `WrappedDEK` に保存。
CP が Workspace 起動時に custodian で unwrap し、**Phase 2 と同じ経路で `AF_SECRET_KEY` を注入**する
（Agent の `secrets.go` は無改修）。custodian は環境で差し替え:

- `local` 既定 = `localCustodian`（KEK = `HKDF(AF_MASTER_KEY, "af-kek:"+keyRef)`・AES-256-GCM）。
- `local` 強化 = Vault transit。`aws` = KMS。いずれも `KeyCustodian{Wrap, Unwrap}` の同一 IF。
- DEK 粒度は **per-workspace**（1 ユーザーの鍵漏洩が他に波及しない。per-tenant 1 鍵にしない）。
- 移行は無改修・無停止: 既存 `secrets.enc` は再暗号化せず、初回 DEK = 旧 `HMAC(master, userKey)` を wrap 保存
  （同じ値を注入するので既存ストアがそのまま復号できる）。

## 帰結・正直な限界

- 得られるもの: ① Vault/KMS を後から差すだけで真の per-tenant 失効（crypto-shred）が入る**継ぎ目**、
  ② DEK を将来 random へローテートできる `wrapped_dek` 構造、③ per-tenant `key_ref` の配線。
- **on-prem の localCustodian は KEK が master 由来ゆえ、master を握れば全 DEK を unwrap できる**——
  強度は単一 master と同等。**真の per-tenant crypto-shred（テナント鍵を disable してそのテナントだけ
  復号不能化）は Vault/KMS 採用時に達成**する。P3-3 はその手前までを安全に敷くもの。

## 追記（2026-10-04）— AWS KMS の custodian（#969）

決定の「`aws` = KMS」の行を実装した（`control-plane/custodian_kms.go`）。上の決定はそのまま有効で、
ここには KMS の custodian がそれをどう満たし、何をしないかを記す。

- **選択。** `AF_KEY_CUSTODIAN=local`（既定）か `kms`。`kms` は `AF_KMS_KEY_ID`（鍵 ARN かエイリアス）と組。
  未知の値、鍵 ID の無い `kms`、`AF_MASTER_KEY` の無い `kms` は起動時に Control Plane を止める。
- **封筒。** `Wrap` のたびに KMS から新しい AES-256 データ鍵を得て（`GenerateDataKey`）、ペイロードを手元で
  封じる（AES-256-GCM、AAD は `keyRef`）。保存値は `kms1:` + base64（blob 長・KMS の暗号文 blob・nonce・
  封じたペイロード）。custodian は DEK 以外も封じ、セッションの引き継ぎや共有の本文は KMS `Encrypt` の
  4 KiB を超えるので、直接 `Encrypt` ではなく封筒にしている。`Unwrap` は設定された鍵 ID で `Decrypt` を呼ぶ。
- **結び付け。** どの呼び出しも暗号化コンテキスト `af:purpose=agent-fleet-custodian`、
  `af:key_ref=<keyRef>` を付けるので、別テナントの行に移した値は KMS が拒否する。配備名はあえて
  コンテキストに入れない。鍵は配備専用で、入れるとスタック名を変えただけで保存値がすべて読めなくなる。
  CP タスクロールの許可（`30-ingress` の `CpCustodianKmsPolicy`）と鍵ポリシー（`10-data` の
  `CustodianKey`）はこのコンテキストを要求する。
- **移行: 形式で振り分け、書き直さない。** `kms1:` 接頭辞の無い保存値は local の custodian が封じたもので、
  local が開く。切り替え前に保存されたものは `AF_MASTER_KEY` がある限り読める。どちらが開くかは値の形式で
  決まり、KMS の失敗で決まることは無い。元の移行の「コード変更なし・停止なし・再暗号化なし」の方針に沿う。
- **フェイルクローズ。** KMS のエラーは KMS を名指すエラーで封じ・開きを失敗させ、`kms1:` の値に local の鍵を
  試すことは無い。local の custodian は `kms1:` の値（local に戻した配備）を、理由を示して拒否する。
- **キャッシュ。** 開いたデータ鍵を（keyRef, blob）ごとにメモリに `AF_KMS_DATA_KEY_CACHE_TTL`
  （既定 5 分、`0` で無効）保持する。上限 1,024 件。

限界をはっきり書く:

- **切り替え前に保存された行は master 鍵だけの守りのまま。** ワークスペースの DEK は初回起動時に一度だけ
  包まれるので、既存のワークスペースもそのまま。**KMS 鍵の無効化による crypto-shred が効くのは切り替え後に
  封じた値だけ。**
- **ワークスペースの DEK は今も旧来の `HMAC(master, userKey)`。** KMS 下で初めて起動したワークスペースでも
  同じで、メンバーのホームにその鍵で書かれた `secrets.enc` が既にあるかを CP は知れない（残したホームの上に
  ワークスペースの行を作り直せる）からだ。KMS が包みはするが master 鍵を持つ人は導けるので、メンバーが
  保存した資格情報は KMS 鍵の無効化では crypto-shred されない。KMS が shred するのは custodian が直接封じる
  もの: MCP ヘッダ・サインインのクライアントシークレット・エンジンのトークン・引き継ぎ・共有。
- **`kms` でも `AF_MASTER_KEY` は必須。** 旧形式の値を開き、旧来の DEK とブリッジの署名鍵も引き続きこれから導く。
- KMS 鍵は配備に 1 本。無効化すると全テナントの切り替え後の値が一斉に shred される。1 テナントだけは
  その `af:key_ref` を拒否する鍵ポリシーで止められるが、鍵の管理者が戻せる失効であって shred ではない。
  テナントごとの鍵は作っていない。
- 無効化した鍵でも、キャッシュ済みのデータ鍵は最大 1 TTL のあいだ開ける。

残り: ワークスペースのランダムな DEK（既存の `secrets.enc` を暗号化し直す手段と組で。#1646）、旧形式の値（`wrapped_dek`・MCP ヘッダ・サインインのクライアントシークレット・エンジンのトークン・
引き継ぎ・共有）をすべて KMS で封じ直す一回限りの rewrap コマンド（#1645）、テナントごとの KMS 鍵、Vault transit。
KMS 鍵のローテーションは AWS の自動ローテーション（`EnableKeyRotation`）で、Control Plane 側は何も要らない。

## 追記（2026-10-10）— 旧形式の値を封じ直す（#1645）

2026-10-04 の追記はそのまま有効。`af-cp rewrap-keys`（`control-plane/rewrap_keys.go`）は、そこで後回しに
した一回限りの書き直しである。Control Plane が `AF_KEY_CUSTODIAN=kms` になってから運用者が実行し、
`kms1:` 接頭辞の無い値をすべて local の custodian で開いて KMS で封じ直す。形式での振り分けはそのままで、
コマンドが変えるのは行がどちらの形式かだけ。

- **範囲。** `store.sealedColumns` の 6 表（`wrapped_dek`・`mcp_server`・`tenant_idp`・`tenant_git_oauth`・
  `session_share_proposal`・`session_handoff_offer`）と、エンジンのトークンの設定行 3 つ（Hugging Face・
  Civitai・ComfyUI のレコード）。一覧の漏れは 2 つのテストで防ぐ。封じた値らしい列（`key_ref`・`*_enc`・
  `ciphertext`）が一覧に無ければ落ちるものと、モジュール内の `Wrap` / `sealTenantSecret` の呼び出しが
  対象に対応付いていなければ落ちるもの。封じずに保存された値（key ref が空）は数えるだけで触らない。
- **行ごと・フェイルクローズ。** master 鍵で開き、KMS で封じ、データ鍵キャッシュを切った状態で新しい値を
  KMS で開き直し、そのうえで古い値との比較付き更新で書く。KMS のエラー・`Decrypt` の拒否・読み戻しの
  不一致では、その行を書く前に止まる。途中で変わった行は新しい値に任せる。したがってどの瞬間も各行は
  2 つの形式のどちらかで、どちらも開ける。`--dry-run` は KMS を呼ばずに数える。
- **終了コード `0` は最後の読み取り専用の確認から決まる**（`--dry-run` も通常の実行も同じ）。コマンドが
  最後に見た時点で旧形式も読めない行も無い、という意味で、ロックではない。保存値を引き継ぐ Control Plane の
  編集（秘密を入れずに保存した IdP や Git OAuth アプリ、鍵を入れずに保存した ComfyUI パネル）は読んだ値を書き戻すので、
  入れ替え前に旧形式を読んだ編集はそれを戻しうる。最終確認は実行中のものは捕まえるが、その後のものは
  捕まえない。そのため手順書では、管理者が編集していないときに実行し `--dry-run` で確かめるよう書いた。
  変更をコマンドに留めるため、それらの編集経路で封じ直すことはしていない。
- **`AF_MASTER_KEY` はやはり外せない。** ワークスペースの DEK（`HMAC(master, userKey)`）をこれから導く
  （`wrapped_dek` は KMS に移るが、中の DEK は導けるので、資格情報ストアはまだ crypto-shred されない。#1646）。
  ブリッジの署名鍵もすべてこれから導き、`kms` でこれが無いと Control Plane は起動時に止まる。rewrap で
  得られるもの: 編集中のものが無い状態で `--dry-run` が `0` で終われば、KMS 鍵の無効化は切り替え前に
  保存された分も含めて custodian が封じた値をすべて shred し、master 鍵ではもう開けない。

実際の KMS 鍵では未検証で、テストはメモリ上の KMS を使う。実配備での最初の実行は `--dry-run` にすること。

## 追記（2026-10-10）— ホームごとのランダムな資格情報ストア鍵、パート A（#1646）

決定にある「DEK を将来 random へローテートできる `wrapped_dek` 構造」を、明示的に有効にする形で実装した。
導いた DEK と上の決定はすべてそのまま有効。

- **ワークスペースではなくホームごと。** `home_dek`（migration 0090 / pg 0075）はメンバーシップごとに
  ランダムな鍵を 1 つ持ち、custodian がテナントの key ref で封じる。`DeleteWorkspace` はホームを残したまま
  `wrapped_dek` を消すので、ワークスペースの行と運命を共にするランダムな鍵では残したホームを誤って shred
  してしまう。今は Destroy も含めて何も行を消さない。アダプタの Destroy はホームが丸ごと消えたことを示さない
  （home task の無い ecs はアクセスポイントだけを消し、途中で失敗した後の再試行は残り物なしと報告する）。
  残った行は使われない封じた鍵にとどまり、読めないストアにはならない。Follow-ups: #1956
- **明示的に有効化・kms 限定。** `AF_WORKSPACE_DEK=random`。`AF_KEY_CUSTODIAN=kms` でなければ起動時に拒否する。
  local の custodian では鍵が master 由来の KEK で包まれ、得るものが無い。鍵を持つホームには、フラグを外した後も
  その鍵を渡し続ける。ストアがすでにそれで封じられているかもしれないからだ。
- **推測しない移行。** CP は残したホームの `secrets.enc` を見られないので、ストアがどちらの鍵を使うかを決めない。
  ホームが `migrating` の間は両方を各ランタイムの秘密の経路で渡す。`AF_SECRET_KEY` は導いた鍵のまま、
  `AF_SECRET_KEY_NEXT` がホームの鍵。Agent はどちらでも開き、起動時に NEXT で封じ直し（ストアのロック下で
  一時ファイル + rename）、どちらでも開けないストアは書き換えない。名前の付け方は版の食い違いのため:
  NEXT を知らない Agent はそれを無視して導いた鍵のままにするので、新しい CP と古いワークスペースイメージの
  組でも、ストアが移るまでは何も失わない。`/healthz` は `secrets_key`（状態名のみ。鍵や長さは出さない）を返す。
- **フェイルクローズ。** custodian がホームの鍵を封じられない・開けないときは起動を失敗させ、導いた鍵だけで
  起動することは無い。鍵はメモ化したランタイムから取らず、実際の起動のたびに解決し直す。

限界をはっきり書く:

- フラグを有効にしてから起動していないホームは導いた鍵のままで、KMS 鍵の無効化では **shred されない**。
  `af-cp home-dek-status` がその数を数える。ストアが移る前のスナップショットやバックアップは導いた鍵のファイルを持つ。
- 鍵の無効化はそれだけでは移ったストアの shred ではない。master 由来の鍵ではもう開けず、データ鍵キャッシュが
  切れた後は CP がホームの鍵を開けずに次の起動を拒否する、ということ（TTL の間は起動が成功し得る）。すでに渡した
  ホームの鍵の写しは今もストアを開け、それぞれ custodian の鍵とは別のもので守られている。動いているワークスペースの
  環境変数と、停止しても残るランタイム側の写し（docker のコンテナ環境、Kubernetes の Secret、アカウントの SSM 用の
  鍵で暗号化された ECS の SSM SecureString）だ。ホームを残したままストアを shred するにはそれらも取り除く必要があり、
  自動でするものは無い。Destroy はその手順ではない。それらを消してホームも消そうとするが、home task の無い ecs では
  ホームの EFS ディレクトリが残る。
- Agent の `Save` は、開けないストアの上には書かなくなった（`Update` はもともとそうだった）。ストアが読めない
  メンバーは、それが取り除かれるまで上書きできない。
- パート A は導いた鍵を渡すのをやめない。移ったストアはもうそれでは開けず、ホームを `random` にして渡すのを
  やめる確認の手順はパート B。
- ストアが移った後に、ホームの鍵を知らない CP に戻す、または `home_dek` を失うと、そのストアは読めなくなり、
  この版の Agent は接続し直しを含めてそのストアへの書き込みをすべて拒否する。この版より前の Agent にはこの保護が無い
  （その `Save` は読めなかった後にも書く）ので、移ったホームではこの版より前のワークスペースイメージを起動しては
  ならない。空のストアで上書きし得る。手順書では、そのホームでワークスペースを起動する前に CP の版・`home_dek`・
  KMS 鍵を戻すこと、資格情報をあきらめる場合に限りワークスペースを止めて読めない `secrets.enc` を手で退避すること
  を書いた。自動で消したり上書きしたりはしない。
- 検証はフェイク相手の単体テストのみ。実際の ECS/EFS で残したホームを作り直すこと、版を戻すことは試していない。
  ECS のスタックはまだこのフラグを出していない。

## 追記（2026-10-10）— ホームの鍵の確認の手順、パート B（#1646）

パート A はそのまま有効。ここではホームのストアが移った後に導いた鍵を渡すのをやめる手順を加える。
migration 0091 / pg 0076 で `home_dek.confirm_epoch` を足す（後述）。

- **何で確認するか。** `AF_SECRET_KEY_NEXT` を渡した起動の後、CP は新しい Agent の `/healthz` を（最大 15 分）
  ポーリングする。ホームを `random` にするのは、Agent が NEXT で封じた（このために足した `secrets_key_next: true`）
  うえで `migrated`・`current` を返した報告だけ。このフラグが無ければ `current` は `AF_SECRET_KEY` で
  開けるという意味でしかないので、パート A の Agent や、不正な NEXT を無視した Agent は確認しない。更新は封じた鍵と
  `migrating` を条件にするので、別の鍵についての報告や二度目の報告は何も変えない。`unreadable` と `derived` は
  ログに残すだけで何も変えず、何も消さない。`none` では確認しない。「起動時にストアが無かった」は、導いた鍵を
  持つ書き手（まだ動いている以前のタスクや、その git の資格情報ヘルパー）が後から作るストアについて何も言わない。
  そのため NEXT を持つ Agent は、ストアが無ければロック下で NEXT で封じた空のストアを作って `current` を返す。
  導いた鍵しか持たない書き手は開けないストアを見つけ、書き込みを拒む（パート A 以降の Agent の場合。それより前の
  Agent にはこの保護が無いのは前述のとおり）。取れないロックは `unreadable` になる。
- **誰の報告か。** エンドポイントは Agent を特定しない。Service Connect は以前の起動のまだ止まりきっていない
  タスクに振り分けうるし、native のポートはもう別のホームのものかもしれない。鍵を持つホームの起動ごとに新しい
  nonce（`AF_HOME_KEY_START`、平の環境変数で秘密ではない）を渡し、Agent はそれを `secrets_key_start` として返し、
  CP はそれを持たない報告を待ち越す。ポーリングはリダイレクトに従わない。自分のワークスペースを操れるメンバーは
  報告を偽れるが、害が及ぶのは自分のホームだけ。
- **remigrate の柵。** `--remigrate` は `confirm_epoch` を進め、確認はその起動が読んだ epoch を伴わなければならない。
  これが無いと、remigrate の前に取った報告が membership・鍵・scheme で再び一致し、どのレプリカからでも remigrate を
  打ち消してしまう。
- **確認の後。** `random` のホームは `AF_SECRET_KEY` = ホームの鍵で起動し、NEXT も導いた鍵も渡さない（その
  `wrapped_dek` の行は開きもしない）。ECS ではその起動で `secret-key-next` パラメータを消す。動いているコンテナは
  再起動するまで両方の鍵を環境変数に持つ。
- **障害をまたいで守ること。** 確認は冪等で、保存された暗号文と epoch を条件にする。待っている間に CP が
  再起動すればホームは `migrating` のままで、後の起動で確認される。同時の起動はこれまでどおりワークスペースごとに
  直列化され、確認を競うレプリカは同じ条件付き更新に当たる。
- **戻すとき。** 確認後にパート A の CP に戻すと両方の鍵を再び渡し、ストアはそれで開く。パート A より前の CP では
  開けず（手順書の復旧の後でメンバーが接続し直す）、パート A より前の Agent は移ったホームで起動してはならない
  （その `Save` が上書きし得る）。移す前の写しを戻すと導いた鍵で封じられていて、確認済みのホームにはもう渡されない。
  ワークスペースは `unreadable` を返して書き込みを拒む。`af-cp home-dek-status --remigrate <membership-id>` で
  ホームを `migrating` に戻せば、次の起動で戻したストアが封じ直される。

検証はフェイク（httptest の Agent とメモリ上の KMS）相手の単体テストのみ。実際の ECS/EFS での起動、止まりきって
いないタスクの報告、スナップショットの復元、実際の KMS 鍵は試していない。
