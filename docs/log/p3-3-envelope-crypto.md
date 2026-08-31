# 15. P3-3 実装プラン — per-deployment/tenant 封筒暗号鍵（custodian 抽象）

> 🗄 **歴史的記録（完了）** — 決定と正直な限界は [decisions/0005](../decisions/0005-envelope-custodian.ja.md)、現状は [HANDOFF §6.9](../HANDOFF.md)。以下は当時の実装プラン。

[12 Phase 3](../roadmap.md) の P3-3。Phase 2 / 現状の鍵は **単一 `AF_MASTER_KEY` → `HMAC(master, userKey)` を
`AF_SECRET_KEY` として注入**（manager.secretKeyFor）。これを **封筒暗号 + custodian 抽象**へ昇格する。
**オンプレ優先**（custodian = AF_MASTER_KEY 由来の KEK / 将来 Vault・KMS）。Agent (`secrets.go`) は**無改修**。

## 15.1 ゴールと不変条件

- **封筒暗号**: per-workspace の DEK（256-bit）を KEK で wrap し `wrapped_dek` に保存。CP が起動時に unwrap → **従来同様 `AF_SECRET_KEY` として注入**。
- **custodian 抽象**: `KeyCustodian{ Wrap, Unwrap }`。既定 = `localCustodian`（AF_MASTER_KEY 由来 KEK・AES-256-GCM）。Vault transit / AWS KMS は同 IF で後追い。
- **Agent 無改修**: `secrets.go` は `AF_SECRET_KEY`(hex 32B) で AES-GCM 復号する仕様のまま。変わるのは CP 側の鍵 provisioning だけ。
- **⚠️ ライブ無傷（最重要）**: 運用者の既存 `secrets.enc` は `HMAC(master, "k1-kami-gmail-com")` で暗号化済み。
  **再暗号化せず**に封筒へ移行する（下記「移行保全」）。

### スコープ外（P3-3 では「やらない」）

| 項目 | 後続 |
|------|------|
| Vault transit / AWS KMS アダプタ | 実需時（IF は用意） |
| DEK の random ローテーション（capability のみ） | 後続 |
| 完全な per-tenant HSM crypto-shred | Vault/KMS 採用時 |

## 15.2 honest な限界（on-prem localCustodian）

localCustodian の KEK は AF_MASTER_KEY 由来。よって **master を握れば全 DEK を unwrap できる**——
オンプレ単一 master のセキュリティ強度は現状と同等。P3-3 が今くれるのは:
1. **custodian 抽象**（Vault/KMS を後から差すだけで真の per-tenant 失効＝crypto-shred が入る継ぎ目）。
2. **wrapped_dek 構造**（DEK を将来 random へローテートできる土台）。
3. **per-tenant `key_ref` の配線**（テナント鍵の概念を実体化）。

**真の per-tenant crypto-shred（テナント鍵を disable してそのテナントだけ復号不能化）は Vault/KMS アダプタで達成**。
P3-3 はその手前までを安全に敷く（[12 §12.3](../roadmap.md#123-tos-と分離の留意自社ホスト前提) の正直さに準拠）。

## 15.3 スキーマ（migration `0003`）

```sql
CREATE TABLE wrapped_dek (
    workspace_id TEXT PRIMARY KEY REFERENCES workspace(id),
    ciphertext   TEXT NOT NULL,      -- KEK で wrap した DEK（base64）
    key_ref      TEXT NOT NULL,      -- どの KEK で wrap したか（tenant.key_ref）
    key_version  INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL
);
```
`tenant.key_ref`（0001 で既にある列）は localCustodian では既定で tenant.id（or slug）。Vault/KMS では transit key 名 / CMK ARN。

## 15.4 custodian（`custodian.go`）

```go
type KeyCustodian interface {
    Wrap(ctx, keyRef string, dek []byte) (ciphertext string, err error)
    Unwrap(ctx, keyRef string, ciphertext string) (dek []byte, err error)
}
```
- **localCustodian**（既定）: per-tenant KEK = `HKDF(AF_MASTER_KEY, "af-kek:"+keyRef)` 32B。AES-256-GCM で DEK を wrap（nonce 同梱・base64）。
  AF_MASTER_KEY 未設定（dev）は **no-op custodian**（DEK をそのまま注入＝現 dev の平文経路と整合）。
- 将来: `vaultCustodian`（transit `encrypt`/`decrypt`）/ `kmsCustodian`（`Encrypt`/`Decrypt` または GenerateDataKey）。`PROFILE`/env で選択。

## 15.5 鍵 provisioning の変更（`manager.go` / `main.go`）

`secretKeyFor(userKey)`（HMAC 直注入）を **workspace 単位の DEK 解決**に置換:

```
resolve/createWorkspace 時に DEK を確定（runtimeFor の前）:
  if wrapped_dek[workspace.id] あり → custodian.Unwrap(tenant.key_ref, ct) → DEK
  else（初回）:
      legacy := secretsExist(data_dir) // <data_dir>/home/.config/agent-fleet/secrets.enc があるか
      if legacy → DEK = HMAC(master32, userKey)   // 既存 secrets.enc を保つ（移行保全）
      else      → DEK = HMAC(master32, userKey)    // 新規も当面は同派生（localでは random でも等価, §15.2）
      ct := custodian.Wrap(tenant.key_ref, DEK); store wrapped_dek
  inject AF_SECRET_KEY = hex(DEK)
```
- DEK は **workspace レコードに紐づく**（runtime 構築時に解決）。manager は custodian と store を持つ。
- dev（master 未設定）: custodian no-op、DEK 空 → `AF_SECRET_KEY` 注入なし（現挙動どおり Agent は平文 `secrets.json`）。

### 移行保全（ライブ運用者）
運用者 workspace は `secrets.enc` が既存 → 初回 DEK = `HMAC(master, "k1-kami-gmail-com")`（＝今コンテナが使う値）→ wrap して保存 → **同じ値を注入** → secrets.enc はそのまま復号。コンテナ再作成・再暗号化なし。

## 15.6 検証（OOM 注意 — ホストのメモリ枯渇は稼働中フリート全体を巻き込む）

1. **ライブ回帰**: CP 差し替え後、運用者の `/api/connections` が 3 件 connected のまま（既存 secrets.enc を新経路で復号）。`wrapped_dek` に運用者 workspace の行が 1 件、unwrap→注入値が旧 HMAC と一致。
2. **新規 workspace**: throwaway identity で workspace 作成→ wrapped_dek 生成→ Start して claude/git 接続を保存→ 再 Start で復号できる（DEK が wrap/unwrap 往復で安定）。teardown。
3. **custodian 往復**: 単体テストで Wrap→Unwrap が元の DEK を返す / 異なる keyRef では復号失敗。
4. **dev 経路**: master 未設定で AF_SECRET_KEY 非注入（Agent 平文）を確認。
5. CP 再起動で DEK 安定（wrapped_dek から復号）。

## 15.7 成果物

`custodian.go`（IF + localCustodian + 単体テスト）/ `migrations/0003_wrapped_dek.sql` /
`store.go`・`store_sqlite.go`（wrapped_dek の get/put）/ `manager.go`（DEK 解決へ置換）/ `main.go`（custodian 構築）。
