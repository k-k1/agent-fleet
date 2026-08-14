# 0043. ログイン IdP はプロバイダ別実装を増やさず「汎用 OIDC 1 本 ＋ GitHub だけ専用」にし、同一人物の保証を GitHub より先に入れる

- 状態: 採用・**未実装**（2026-08-14。設計は docs/61。同日、1 デプロイ内を部署でテナント分割する
  要件を受けて**決定 13〜28（テナント毎のログイン・責務分担・移譲と退職・P3）を追加**した。うち決定 15/16 は
  「テナント毎にログインできる ID を管理すればよい」という指摘を受けた見直しで、
  招待 API が既にあることを実測して**テナント側の email リストを設計から落とした**）
- 関連: [61-login-idp.md](../61-login-idp.md) /
  [dev/07-security.md](../dev/07-security.md) §7.3（AUTH 3 モード＝現行契約） /
  [dev/06-data-model.md](../dev/06-data-model.md)（`identity` / `user_key`） /
  [0001-self-host-vs-saas.md](0001-self-host-vs-saas.md)（各社が自社でセルフホスト＝IdP は各社のもの）

## 背景

L1 ログインの IdP が Google 固定（`control-plane/oauth_google.go`）。セルフホスト先は
「グループ各社が自社の社員をホストする」前提（roadmap §12.2）なので、Google Workspace を
使っていない会社は設置自体ができない。想定顧客の多数派は M365 で、これは配布の前提条件を欠く。

コードを実測して分かった、設計を決めた 3 点:

- **OAuth の配線は軽い。** Google 実装は id_token を検証せず、認可コード → トークン → userinfo で
  email を取るだけ（`oauth_google.go:238-256`）。JWT ライブラリを持っておらず（`go.mod`）、
  stdlib で完結している。汎用 OIDC も discovery JSON を 1 回読む差しかない。
- **重いのは identity 側。** `identity.user_key = sanitizeUser(email)`（`resolver.go:281`）が
  **workspace の home ディレクトリ名であり、暗号化 secrets の帰属先**（dev/07 §7.2）。
  つまり email が identity そのものになっている。既に `disambiguateUserKey`
  （`store_sqlite.go:337`）が sanitize 衝突による identity 併合を防いでいて、この一意性は
  意図的に守られている。
- **email の信頼根拠は IdP ごとに違う。** 現行は `email_verified` を見ている（Google は出す）が、
  **Entra ID はこのクレームを出さない**。一律 `email_verified == true` を必須にすると Entra が通らず、
  逆に外すと GitHub で「他人の会社 email を登録して許可リストを通過」が成立する。

## 決定

1. **プロバイダ別実装を増やさず、汎用 OIDC クライアントを 1 本作る。** Entra ID / Okta / Keycloak /
   Auth0 / Cognito / GitLab はこれ 1 本で載る。Google も内部的にこの実装の 1 インスタンスへ移す
   （**env 名は据え置き** — 既存デプロイは設定を 1 行も変えずに動く）。
2. **専用アダプタを書くのは GitHub だけ。** GitHub は OIDC 非対応（OIDC は Actions 用トークンで、
   ユーザーログインには無い）ため避けられない。**org メンバーシップ判定とセットでのみ**入れる —
   個人 GitHub アカウントは会社の統制外なので、email 許可リストだけで開けてはいけない。
3. ★ **同一人物の保証（P1）を GitHub（P2）より先に入れる。** GitHub の登録 email は会社 email と
   違うのが普通で、`user_key` が email 由来である以上、リンク機構なしに出すと
   **identity が 2 つ・home が 2 つ・secrets が別**になる。ユーザーからは「リポジトリが消えた」に見える。
   順番は **P0 汎用 OIDC → P1 リンク → P2 GitHub**。P0 だけで「Microsoft でログインしたい」は満たせる。
   （決定 5 の撤回に伴う補足: P1 は「リンク」ではなく **`(provider, subject)` による同一性の固定**になった。
   GitHub を出す前提は「**会社 email を GitHub に登録している人専用**」と定義することへ変わり、
   別 email なら別 WS でよい。順序 P0 → P1 → P2 自体は維持。）
4. **`user_key` は不変とし、`(provider, subject)` を横に足す。** 新テーブル `identity_provider`
   （`0038`）を作り、`identity` 本体は触らない。`user_key` を `sub` ベースへ作り替える案は、
   home ディレクトリ名なので**既存デプロイ全員のデータ移行**が要る上、`af-ws-<user>` が人に読めなくなる。
   既存デプロイの移行は初回ログイン時に `(google, sub)` 行を書くだけで済む（移行ゼロ）。
5. ~~**別 email の結合はログイン画面から行わない。** サインイン済みの状態で Console の
   「アカウントを追加」から 2 つ目の IdP を通したときだけ結合する（＝両方にログインできることが証明）。~~
   ★ **撤回（2026-08-14・P1 実装時）— 別 email の結合機構そのものを作らない。**
   両方にログインできることが証明するのは「その 2 つのアカウントを操作できる」ことまでで、
   **同一人物であること**ではない。弱い検証の IdP が 1 つでも有効なら、そこで取ったアカウントを
   会社アカウントの home（＝会社の secrets が入っている側）へ合流させる経路になり、しかも
   **結合を解く導線は無く一度合流すると戻せない**。加えて、入口の許可リストが会社ドメインに
   限定されていれば**ログインできる email は会社 email だけ**で、別 email が現れるのは
   ドメインを緩めた運用を選んだ場合＝ WS が分かれるのは設定どおりの結果。GitHub（P2）も
   「会社 email を GitHub に登録している人専用」と定義すれば同じ線に収まる。
   結果として P1 は §61.5 の規則 1〜3 だけになり、**Console 側の作業はゼロ**。
   なお管理者が対応表で結合する案は、初版どおり却下のまま（表が保守されない）。
6. **email の信頼根拠を provider ごとの宣言（`trust`）にする。** `email_verified` /
   `issuer`（テナント固定で担保・Entra） / `api`（別 API の検証フラグ・GitHub）の 3 種。
   **宣言の無い provider は起動時に拒否**して fail-closed を落とさない。
7. **Entra を `common` エンドポイントで受けさせない。** issuer をテナント固定にするか、
   `tid` 許可リストを必須にする。`common` かつ `ALLOWED_TIDS` 未設定は **fatal**。
   これを許すと「Microsoft アカウントを持つ全人類」が入口に立ち、個人 MSA は email を
   付け替えられるので email 許可リストが意味を失う。
8. **redirect_uri は `/oauth2/callback` 1 本のまま**にし、provider は署名済み state cookie に載せる。
   プロバイダごとに URI を分けると、設置する会社が IdP 側へ登録する手数が IdP の数だけ増える。
   コールバックでは state の provider id を**設定済みの集合と突合してから**分岐する。
9. **id_token の署名検証は引き続き行わない。** 認可コードフロー・client_secret 付き・
   トークンエンドポイントから TLS 直受けだから（OIDC Core §3.1.3.7 の注記と同じ論拠）。
   `tid` のように userinfo に出ないクレームは、**同一レスポンス内の id_token ペイロードを
   署名検証なしで読む**。この前提は `oauth_oidc.go` 冒頭に固定し、
   **フロントチャネルで id_token を受ける経路を足すなら JWKS 検証が必須**と明記する。
   これにより Go の依存追加はゼロのまま。
10. **SAML は CP に実装しない。** 日本企業の IdP（HENNGE One / TrustLogin / CloudGate 等）は
    SAML 前提が多いが、SP 実装は OIDC の数倍の面積で stdlib に収まらない。
    **既存の `AUTH=proxy` ＋ oauth2-proxy / Keycloak のブリッジ**を正式な回答として文書化する。
11. **1 つの IdP の設定ミスで全員を締め出さない。** 個々の provider は設定不足なら無効化＋警告、
    **有効な provider がゼロのときだけ fatal**（現行の挙動を維持）。許可リストが全部空なら
    現行どおり警告つき全拒否。
12. **オフボーディングの性質（毎リクエスト再判定）を落とさない。** GitHub の org 判定だけは
    ローカル判定できないので、`(provider, subject)` キーの TTL キャッシュ（既定 10 分）＋
    API 障害時は最後の肯定結果を猶予（既定 1 時間）だけ延命し、超えたら拒否する。

### テナント毎のログイン（P3・部署分割・docs/61 §61.9）

13. ★ **「入口の門」と「テナントの門」を別の層に置く。** 入口（このデプロイにサインインできるか）は
    `authGate` が毎リクエスト判定し、テナント（いまこのテナントを使ってよいか）は
    `resolveFull` / `selectMembership` 側で判定する。**テナント規則を `authGate` に持ち込まない** —
    `authGate` はテナントを知らない（`X-AF-Tenant` を読むのは `httpapi.go:34`）ので、
    「どのテナントで判定するのか」が決まらず必ず穴か過剰拒否になる。
14. ★ **URL のテナント指定は認可の根拠にしない。** `/login/<slug>` は「どの画面を出すか」のヒントに
    過ぎず、利用者が書き換えられる。実際にどのテナントへ入れるかはサーバ側の membership ＋
    テナント規則だけで決める。同じ理由で、テナント毎の `allowed_providers` は**画面のボタンを
    絞るだけでは不十分**で、セッションの `prov` をテナント解決時に突合して強制する
    （さもないと汎用 `/login` で GitHub ログイン → `X-AF-Tenant` 差し替えで抜けられる）。
    決定 1 で `prov` クレームを足す価値の半分はここにある。
15. ★ **「テナント毎にログインできる ID の名簿」は membership が持ち、テナントに `allowed_emails` を
    足さない。** 実測すると器は既にある — `POST /api/admin/memberships`（`tenants.go:254`）は
    email から**未ログインの人の identity を先に作って** membership を張る＝招待そのもので、
    Console の管理タブに UI もある（`AdminTab.tsx:1593`）。テナント側に email リストを足すと
    同じ「誰が入れるか」を 2 箇所で管理する**二重台帳**になり必ずずれる。
    これにより**全社共通ドメインの会社でも部署分割が成立**する（名簿はドメインに依存しない）。
    テナントが持つのは `auto_join_domains`（自動参加・省力化）と `allowed_domains`
    （**招待時のガードのみ**。tenant_admin が自部署ドメイン外を勝手に足すのを防ぐ）の 2 つだけ。
    `allowed_domains` を毎リクエストの制約にはしない — 正規に招待した業務委託（別ドメイン）が
    締め出され、例外リストが要り、結局二重台帳へ戻るため。**継続的な可否は membership が持つ**。
16. **入口の判定は「和」にし、そこに「membership を持つこと」を含める**
    （デプロイ全体の許可リスト ∪ 各テナントの `auto_join_domains` ∪ membership）。
    ★ 最後の項が現状で欠けている接続で、いまは招待済みでも `AF_OAUTH_ALLOWED_*`(env) に
    載っていなければ `authGate` が入口で弾く。繋ぐと**招待運用のデプロイでは env の許可リストが
    不要**になり、名簿が membership 1 箇所に寄る。積にすると招待のたびに env にも足す二重管理になる。
    和でも危険が増えないのは、入口通過が「どこかに入れる」を意味しないため（決定 13）。
    **すべて空なら現行どおり全拒否**。
17. **ログイン画面の分割はパス方式（`/login/<slug>`）**。サブドメイン方式はワイルドカード DNS と
    証明書が要り、Funnel は 1 ホスト名しか出せず、redirect_uri が増えて決定 8 を壊す。
    **未知の slug は 404 にせず汎用画面**を返す（テナント slug の存在有無を未認証者に漏らさない）。
18. **セッションは provider を 1 つしか持たない。** テナント間移動で再サインインが要る場合は
    `provider_required` を返し、**403 で終わらせず再ログインへ誘導**する（「このテナントには
    Microsoft でのサインインが必要です」）。複数 provider の同時保持は cookie を
    「認可状態の集合」にし、失効とオフボーディングの意味を曖昧にする。
19. **テナント規則は env ではなく DB（`tenant` のカラム・`0039`）に置く。** テナントは実行時に増えるので
    `AF_TENANT_<SLUG>_…` は必ずずれる。管理 API は既にある（`routes.go:129-137`）。
    毎リクエスト参照は短 TTL（30 秒）キャッシュ＋管理 API 書き込みで破棄。
    なお現行 `emailAllowed` は許可ファイル指定時に**毎リクエスト `os.ReadFile`**しており
    （`oauth_google.go:130`）、DB＋キャッシュはこれより軽い。
20. **P3（テナント毎のログイン）は P1 / P2 と独立**で、P0 の直後に着手してよい（依存は `prov` だけ）。
    GitHub を入れない会社でも「部署ごとにテナントを分け、Entra 限定にする」だけで価値が出る。
21. **IdP のグループ（Entra `groups` / GitHub team）→ テナント同期は入れない。** 決定 15 で名簿を
    membership に寄せたため必須ではなくなり、残る利点は異動の自動追従だけ。入れると
    「membership が正」という単一の正が崩れて同期衝突を扱うことになる。将来入れるとしても
    membership を上書きせず、**管理画面に差分を出して人が承認**する形にする。
    （Entra の `groups` は overage で Graph 参照に化けるため、実装も見た目より重い。）
22. ★ **membership の削除／無効化 API を P3 のスコープに必ず含める。** 運用を書き下して見つかった穴で、
    現状 `MembershipStore`（`store.go:383-402`）にあるのは `EnsureMembership`（挿入のみ）と
    `SetMembershipRole` だけ、`routes.go` にも `DELETE /api/admin/memberships` が無い。
    今それで足りているのは、オフボーディングが **env / ファイルの許可リストから消す**こと
    （`oauth_google.go:299-309` の毎リクエスト再判定）で成立していたため。決定 15/16 で名簿を
    membership に寄せると**この経路が消える** — IdP 側で無効化しても署名済みセッション cookie は
    最大 `AF_SESSION_TTL`（既定 168h＝7 日）有効なので、外せなければ最大 7 日残る。
    **異動（旧部署から外す）も退職（全部から外す）も実行できない。**
    `Membership` には `Status` があり `GetMembershipByID` も missing/inactive を想定しているので、
    **論理削除（`status='inactive'`）で足りる**（workspace / home は残したまま締め出せる）。
    外した後の workspace / home の扱いは [0028-deletion-lock](0028-deletion-lock.md) と
    掃除の段階制に合わせ、即削除しない。
23. **`super_admin` は membership が無くても管理画面に入れるようにする（P3）。**
    現状コードを追う限り、`AF_PROVISION=invite` で立ち上げると最初の 1 人が入れない —
    membership ゼロだと `GET /api/tenants` が 403 `not_provisioned`（`resolver.go:69`）、
    Console は `data.error` 分岐で `superAdmin` を立てず（`tenant.ts:93-100`・既定 `false`）、
    管理メニューの表示条件 `superAdmin || tenant_admin`（`TopBar.tsx:319`）が偽になる。
    admin API 自体は `identityFor` だけで通るので API 直叩きなら可能だが、手順として成立しない。
    直すまでの運用は「**`auto` で立ち上げ → テナント作成と招待 → `invite` へ切替**」（docs/61 §61.10.2）。

### 責務分担（2026-08-14 確認）

24. **`super_admin` の指定はホスト側の env（`SUPER_ADMIN_EMAILS`・`main.go:85`）のままとし、
    Console からの昇格は作らない。** デプロイ全体を動かせる権限は、そのデプロイを設置した人＝
    ホストのファイルに触れる人だけが持つ。アプリ内で増やせると設置者以外が権限を作れてしまう。
    許可リストと違い **live-read ではなく起動時に 1 度読むだけ**なので、変更には CP 再起動が要る。
    ★ **ただし剥奪ができない（現状の穴・P3 で塞ぐ）。** `UpsertIdentity` は
    「Upgrade (never downgrade)」（`store_sqlite.go:314-317`）なので、`SUPER_ADMIN_EMAILS` から
    消して再起動しても `identity.role` は `super_admin` のまま残り、降格 API も無い
    （`setMembershipRole` はテナント役割のみ）＝ DB 直編集以外に手段が無い。
    env を単一の正とし、**CP 起動時に一括同期する**（`SUPER_ADMIN_EMAILS` に無い `super_admin` を
    `user` へ落とす）。★ ログイン時同期ではなく**起動時**にする理由は移譲・退職ケース —
    退職者はもう二度とログインしないので、ログイン時同期では DB に `super_admin` が残り続ける
    （docs/61 §61.10.7）。起動時なら env を書き換えて再起動する移譲手順とタイミングが一致する。
    実装注意 — **降格を `UpsertIdentity` の `roleHint` に持たせない**。
    `addMembership`（`tenants.go:285`）/ `cleanHome`（`:195`）/ `stopWorkspace`（`:149`）は
    `roleHint=""` で呼ぶため、誰かをテナントに追加しただけで super_admin が落ちる。
    起動時の一括 UPDATE なら `roleHint` を通らないので、この罠を構造的に回避できる。
25. **テナントの新設と `tenant_admin` の任命は `super_admin`。** 実装は既にこのとおりで変更不要
    （`POST /api/admin/tenants` は `withSuperAdmin`＝`routes.go:133`、`tenant_admin` を付けられるのは
    super_admin だけ＝`tenants.go:280`、`PUT /api/admin/membership-role` も `withSuperAdmin`）。
    super_admin が日常運用に出るのは**この 2 つだけ**にする。
26. ★ **workspace / home の削除は `tenant_admin` の責務。** 部署の人員を把握しているのは部署側で、
    情シスに毎回頼む形にしない。**現状 `clean-home` は super_admin 限定**
    （`routes.go:136` の `adm.withSuperAdmin(adm.cleanHome)`。ハンドラもテナント所属を見ていない）
    なので、**P3 でハンドラ内 `tenantAdminFor` ゲートへ付け替える**。前例は同ファイルの
    `stopWorkspace`（`tenants.go:145`）で、これにより tenant_admin は自部署のメンバーの home しか
    消せない。順序は「membership 無効化 → workspace 停止 → home 削除」で、停止は既に
    tenant_admin ができるため揃えるのは `clean-home` だけ。即時削除にしない扱いは
    [0028-deletion-lock](0028-deletion-lock.md) と掃除の段階制に合わせる。
    決定 22 の membership 無効化 API も同じく tenant_admin（自テナント分）に開く。
27. ★ **セッションの即時失効は「`AF_COOKIE_SECRET` のローテーション」しか無い。これを runbook に書く。**
    セッション cookie は stateless（HMAC over `{email, exp}`・`oauth_google.go:85-93`）で、
    サーバ側にセッションストアも個別失効も「全端末からログアウト」も無い。今それで足りていたのは、
    **許可リストから消せば `authGate` が毎リクエスト弾く**（`:299-309`）のが実質の失効機構だったため。
    決定 15/16 で名簿を membership に寄せると、その役割は membership 無効化が担うが、
    ★ **`AF_OAUTH_ALLOWED_DOMAINS` を併用しているデプロイでは塞がらない** — 前任は membership を
    全部外されてもドメイン一致だけで入口を通り、管理 API は `identityFor` だけで通って membership を
    要求せず、決定 24 未修正なら `role` は `super_admin` のままなので `withSuperAdmin` を通過して
    **自分を復帰させられる**（手元の cookie は最大 `AF_SESSION_TTL`＝既定 168h 有効）。
    対策は決定 24 の起動時同期＋このローテーション手順の 2 段。**サーバ側セッションストアは作らない**
    — stateless cookie の軽さは CP の設計上の利点で、失効のために状態を持つのは代償が大きい。
28. **招待の通知機能は作らない。** 招待 URL（`/login/<slug>`）は tenant_admin が口頭 / 社内チャットなど
    **CP の外**で伝える。CP にメール送信を持たせない方針（決定 10 の magic link 却下と同じ理由）で、
    通知経路を足しても「本人に届いたか」の保証は結局得られない。

### テナント定義の認証方式（P4・子会社ごとに Entra が違う場合・docs/61 §61.11）

29. **テナント毎に provider 定義そのものを持てるようにする（P4）。** グループ各社・分社では
    テナントごとに Entra テナントが違う（issuer も client_id/secret も別）。P0 の env でも
    `AF_OIDC_PROVIDERS=entra_a,entra_b` と並べれば動く（実測済み）が、**テナントを増やすたびに
    ホストのファイル編集＋CP 再起動**になり、「テナント新設は再起動不要」（決定 25・docs/61 §61.10.3）と
    矛盾する。定義を DB へ移し、tenant_admin が Console から編集できるようにする。
30. ★ **有効化は `super_admin` の承認を要する。編集と保存は tenant_admin、有効化は別の人。**
    プロジェクト MCP（[0031](0031-mcp-registry.md)）は tenant_admin が単独で登録できるが、
    あれは「そのテナントのエージェントが**外へ**叩きに行く先」で、**IdP は「誰であるか」を宣言する
    主体**だから同じ扱いにはできない。実測した乗っ取り経路: `user_key = sanitizeUser(email)`
    （`resolver.go:281`）は**デプロイ全体で 1 つの名前空間**で、デプロイ役割も **email 一致**で決まる
    （`roleHintFor`・`resolver.go:28`）。よって自分の支配下の IdP を登録できる tenant_admin は
    `email=<super_admin>` を主張するトークンを自分で発行してサインインでき、`UpsertIdentity` が
    role を上げ、**never downgrade（`store_sqlite.go:314-317`）なので不正 provider を消しても残る**。
    `trust: "issuer"` は防波堤にならない（issuer が攻撃者自身）。★ **悪意が無くても起きる** —
    セルフサインアップ有効な Auth0 テナントを善意で登録した瞬間にデプロイ全体が開く。
    承認は子会社あたり 1 回きりなので、運用コストは失うものに対して圧倒的に小さい。
    無効化（`suspended`）は tenant_admin も打てる — **止める方向は誰でも早く**。
    ★ **issuer / client_id / trust を変更したら `pending` へ戻す**。承認は「この issuer を信じてよい」に
    対して与えたもので、issuer が変われば承認の対象そのものが変わる。
31. ★ **テナント定義の provider からは `super_admin` を取れない。** `roleHintFor` は
    **env 由来 provider のログインにだけ**効かせる。承認済みでも、その IdP の管理者はその会社の
    情シスであってこのデプロイの設置者ではない（決定 24 の「デプロイ全体の権限はホストに触れる人だけ」）。
32. ★ **P4 は P1（`0038`）が前提。** テナント定義の provider は `(provider, subject)` で identity を
    作り、**決定 4 / docs/61 §61.5 の「email 一致で既存 identity へ結合」を無効化する**。
    これが無いと、email を騙るだけで既存の identity＝home＝secrets を乗っ取れる。
    あわせて、テナント定義の provider は**自テナントにしか入れない**（`prov` を `resolveFull` で突合。
    入口の許可リストもそのテナントのものを使い、デプロイ共通リストへフォールバックしない）し、
    **素の `/login` には出さず `/login/<slug>` 限定**にする（全子会社のボタンが未認証者に並ぶと
    組織構成が漏れる）。依存関係は実質 **P1 → P3 → P4**。
33. **`client_secret` は DB にテナント鍵で封印して置く。前例は `mcp_server`。**
    `headers_enc` ＋ `key_ref` を `custodian.Wrap(tenantID, …)` で封印し（`mcp_server.go:146`・
    AES-256-GCM・AAD=keyRef）、UI へは `***` でマスク、更新は merge で未編集の値を残し、
    復号不能は空にせず明示エラー（`mcp_headers_unreadable`）——この 4 点をそのまま踏襲する。
    正直に添える限界 2 つ: `localCustodian` の KEK は master 由来なので**テナント間の暗号学的分離では
    ない**（[0005](0005-envelope-custodian.md)）／env から DB へ移すと秘密が **`DATA_DIR`＝
    バックアップの中**に入る（`AF_MASTER_KEY` をデータ領域の外に置く既存ルールが前提）。
    ★ **provider id の名前空間を分ける**: env 由来は `entra`、DB 由来は `t:<tenant-slug>:<name>`。
    混ぜるとテナントが `google` という名前の行を作って env の Google を上書きできる。
    `common` / `organizations` ＋ TIDs 空の禁止（決定 7）は、DB 側では**保存時 400** で効かせる
    （実行中の CP は落とせないため）。

## 却下した案

- **Entra 専用実装を足すだけ。** 最短だが、Okta / Keycloak の要望のたびに同じ作業を繰り返す。
  汎用 OIDC との差は discovery を読む数十行しかない（決定 1）。
- **`AUTH=proxy` で全部済ませる（CP は何もしない）。** 実際 SAML の答えはこれ（決定 10）だが、
  これを既定にすると「compose を上げれば動く」というセルフホストの前提が崩れ、
  設置する会社に oauth2-proxy の運用を丸ごと負わせることになる。
- **email が一致したら常に自動結合し、別 email は管理者の対応表で結合。** 決定 5 のとおり却下。
- **magic link / パスワードログイン。** IdP を持たない小さな会社には効くが、CP が資格情報と
  SMTP を背負う。需要が出てから別 ADR で判断する。
- **Apple / LINE / Slack / Atlassian / Discord。** 会社が入退社を統制する手段にならず、B2B の入口に値しない。
- **`super_admin` を Console から昇格できるようにする。** 運用は楽になるが、設置者以外が
  デプロイ全体の権限を作れてしまう（決定 24）。
- **招待メール / 通知を CP から送る。** URL を人が伝える手間は残るが、SMTP 依存が増え、
  「本人に届いたか」の保証も得られない（決定 27）。
- **workspace / home の削除を super_admin 限定のままにする。** 現状の実装はこれだが、
  部署の人員を把握していない情シスに毎回頼む形になる（決定 26）。
- **サーバ側セッションストアを持って個別失効できるようにする。** 退職者を即座に切れるが、
  stateless cookie の軽さ（DB 参照ゼロで認証が済む）を捨てることになる。
  鍵ローテーションという即時手段が既にあるので、そこまでの代償は払わない（決定 27）。
- **`super_admin` の降格をログイン時だけに同期する。** 実装は素直だが、
  **退職者はもうログインしない**ので DB に `super_admin` が残り続ける（決定 24）。
- **テナントに `allowed_emails` カラムを持たせる。** 「テナント毎にログインできる ID を管理する」の
  最も素直な実装だが、membership が既に同じ名簿なので二重台帳になる（決定 15）。
- ★ **tenant_admin が単独で認証方式を有効化できるようにする。** 「テナントのことはテナントで完結」
  という決定 25/26 の線には最も忠実だが、**IdP を足せる人はそのデプロイの誰にでもなれる**（決定 30）。
  tenant_admin の任命が super_admin の任命と同義になってしまう。
- **承認を省略する env（`AF_ALLOW_TENANT_IDP=1`）を用意する。** 「自社の tenant_admin は信頼できる」
  デプロイ向けの逃げ道だが、**fail-closed を env 1 行で外せる形にすると、それが既定の設置手順になる**。
  承認は子会社あたり 1 回きりなので、外す価値が無い（決定 30）。
- **テナントの `client_secret` は env に置いたまま、DB にはテナントの選択だけ持たせる。**
  秘密がバックアップに入らない利点はあるが、テナント追加のたびにホスト編集＋再起動が残る。
  MCP のヘッダで既に同じ posture を受容している以上、ここだけ厳しくする理由が無い（決定 33）。
- **テナント定義の provider に env と同じ id 空間を使う。** 実装は短いが、テナントが `google` という
  行を作って env の Google を上書きできる（決定 33）。
- **テナント毎のログインをサブドメインで分ける / URL・cookie のテナント指定を認可の根拠にする /
  テナント規則を `authGate` に置く / `allowed_domains` を毎リクエストの制約にする /
  `auto_join_domains` を唯一の帰属手段にする / テナント規則を env に置く /
  セッションに複数 provider を持たせる。** それぞれ決定 13〜19 の裏返しで、理由は docs/61 §61.13。

## 影響

- `oauth_google.go`（461 行）は `oauth.go` / `oauth_oidc.go` / `oauth_github.go` に分割される。
  ファイル名の Google 色は消える（`dev/90-code-map.md` の更新が要る）。
- `sessionClaims` に `prov` / `sub` が増える。JSON なので**既存 cookie は欠損フィールドとして読め**、
  移行時の強制ログアウトは不要。ただし `prov` 欠損を `"google"` とみなす暫定規則を 1 版だけ置く。
- `oauthState` に provider id が増える（state cookie は署名済みなので追加は安全）。
- `authGate` の毎リクエスト再判定が provider 分岐を持つ（`oauth_google.go:299-309`）。
- 起動時バリデーション（`main.go:278-284`）が provider 単位になる。
- 配布物 6 箇所に設定例が増える: `deploy/compose/.env.example` / `deploy/local/oauth.env.example` /
  `deploy/aws/ecs/cfn/30-ingress.yaml` / `deploy/aws/ec2-single/README.md` /
  `deploy/compose/README.md` / `docs/guide/operator/*`（guide は**二言語とも**）。
- `dev/07-security.md` §7.3 の「AUTH 3 モード」表は、`oauth` 行が「Google」ではなくなるので書き換え。
- GitHub を入れる会社には **org の OAuth App 承認**という設置手順が 1 つ増える
  （org が OAuth App access restrictions を有効にしていると、承認前は membership が見えず全員拒否になる）。
- P3 で `authGate` の入口判定が membership を参照するようになる（決定 16）。招待運用のデプロイでは
  `AF_OAUTH_ALLOWED_*` を空にできるようになるので、**「空＝全拒否」の警告文
  （`main.go:283`）は「かつ membership も無い」まで含めた条件に直す**必要がある。
- P3 で `tenant` に 3 カラム増える（`0039`）。`Tenant` 構造体（`store.go:15`）と
  `CreateTenant`（`store.go:364`）まわり、テナント管理 API と Console の管理 UI に編集面が要る。
- P3 で `selectMembership`（`resolver.go:128`）にテナント規則と `prov` の突合が入り、
  戻り値のエラーコードに `provider_required` が増える。Console のテナント切替は
  これを 403 として扱わず**再サインイン導線**に変える必要がある。
- P3 で `clean-home` の権限が super_admin 限定から tenant_admin（自テナント分）へ広がる（決定 26）。
  `routes.go:136` の `withSuperAdmin` を外し、ハンドラ内で `tenantAdminFor` を取る形に変える。
  **権限を広げる変更なので、監査（`audit.go`）に誰がどのテナントの誰の home を消したかを必ず残す。**
- P3 で `identity.role` の降格経路ができる（決定 24）。`UpsertIdentity` の
  「never downgrade」は**維持したまま**、起動時に一括同期する処理を `main.go` に足す形にする。
- `AF_COOKIE_SECRET` のローテーション手順（＝全員ログアウト）を **operator の runbook に新設**する
  （決定 27）。現状 `deploy/**` にも `docs/guide/operator/**` にも記述が無い。guide は二言語とも。
- 退職・移譲の棚卸し表（docs/61 §61.10.7）を operator guide にも出す。とくに
  **定時実行は `Schedule.MembershipID`＝個人所有なので止まる**（内部 git は `git_repo.tenant_id`＝
  テナント所有なので残る）という非対称は、事前に知らないと退職後に気づくことになる。
- P3 で `AF_PROVISION` の意味が広がる（`auto_join_domains` 一致が `auto` / `invite` より先に効く）。
  一致しないときの挙動は現行と同じなので、既存デプロイの見え方は変わらない。
- P4 で `tenant_idp`（`0040`）が増え、**provider の一覧が env 固定ではなくなる**（決定 29）。
  P0 の `buildLoginProviders` は起動時に 1 回読むだけなので、DB 由来の provider を扱うには
  **実行時に足し引きできる形へ変える**（短 TTL キャッシュ＋管理 API の書き込みで破棄。決定 19 の
  テナント規則と同じ扱い）。承認・停止が再起動なしで効くことが P4 の前提なので、ここは避けられない。
- P4 で `roleHintFor`（`resolver.go:28`）に「どの provider で入ったか」が渡るようになる（決定 31）。
  `sessionClaims.prov` は P0 で入っているので、経路としては `authGate` から下ろすだけ。
- P4 で Console に面が 2 つ増える: テナント詳細の「認証方式」（tenant_admin）と、
  super_admin の**承認キュー**。承認は権限を作る操作なので、**`audit.go` に必ず残す**
  （誰がどのテナントのどの issuer を承認したか）。
- P4 の秘密は `DATA_DIR` の DB に入るため、**バックアップの取り扱い（`AF_MASTER_KEY` を
  データ領域の外に置く）が今より効く**。operator guide の該当箇所に一言足す（二言語とも）。
