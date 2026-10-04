# 0005. at-rest 鍵 — 封筒暗号 + custodian 抽象（on-prem の限界を明記）

[English](0005-envelope-custodian.md) | 日本語

- 状態: 確定（P3-3）
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

残り: ワークスペースのランダムな DEK（既存の `secrets.enc` を暗号化し直す手段と組で）、旧形式の値（`wrapped_dek`・MCP ヘッダ・サインインのクライアントシークレット・エンジンのトークン・
引き継ぎ・共有）をすべて KMS で封じ直す一回限りの rewrap コマンド、テナントごとの KMS 鍵、Vault transit。
KMS 鍵のローテーションは AWS の自動ローテーション（`EnableKeyRotation`）で、Control Plane 側は何も要らない。
