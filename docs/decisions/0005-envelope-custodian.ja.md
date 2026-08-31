# 0005. at-rest 鍵 — 封筒暗号 + custodian 抽象（on-prem の限界を明記）

[English](0005-envelope-custodian.md) | 日本語

- 状態: 確定（P3-3）
- 関連: [history/p3-3-envelope-crypto](../log/p3-3-envelope-crypto.md) / [dev/07 §7.6 シークレット管理と封筒暗号](../build/07-security.ja.md#76-シークレット管理と封筒暗号)（旧 security §4.4） / [ロードマップ §12.3](../roadmap.md#123-tos-と分離の留意自社ホスト前提)

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
