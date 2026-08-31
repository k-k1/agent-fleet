# 0012. Go バックエンド内部リファクタ — internal 層化・2 バイナリ分離維持・共有モジュール見送り

[English](0012-go-internal-refactor.md) | 日本語

- 状態: 確定（2026-07-08）
- 関連: [23-go-refactor.md](../log/23-go-refactor.md)（設計本体）/
  [0011-console-rebuild.md](0011-console-rebuild.ja.md)（Console 側の先行事例）

## 背景

CP（約 13.8k 行・54 ファイル）と Workspace Agent（約 16k 行・61 ファイル）はどちらもフラットな
単一 `package main` で、境界がコンパイラに強制されていない。帰結として CP は god オブジェクト×2
（`config` 133 ハンドラ / `manager` 責務過多 + DB I/O 跨ぎのロック）、Agent は CLI 3 種の
並列コピー実装（transcript / auth / usage）と 11 のグローバルロック島を抱える。CI は存在せず、
Console との API 契約は TS 型・Go 構造体・エラーコード文字列の三重手動同期。一方でロジック自体は
健全で、Store / Runtime / Agent インターフェースという良い抽象が既にある — Console（docs/22）の
「リビルドでないと解けない」とは逆に、こちらは**構造だけの問題**でありリファクタで解ける。

## 決定

1. **各モジュール内で `internal/` パッケージに層化する**（機能不変・ワイヤ API 完全互換）。
   両 Dockerfile はモジュールディレクトリ丸ごと COPY のため、モジュール内分割はビルド無変更で
   通る。`//go:embed` 対象（migrations / knowledge）はパッケージと同伴移動。
2. **CP↔Agent の 2 バイナリ分離は維持**。`preview` や SSM 系の「重複に見える」コードは
   プロトコルの両端であり、信頼境界（Agent は VPC 内部のみ）として設計・文書化済み。統合しない。
3. **共有 Go モジュール（go.work）は見送り**。モジュール横断の真の重複は `writeJSON` と ID 生成
   程度で、Dockerfile 2 本のコンテキスト変更コストに見合わない。契約の型化（tygo 等による
   TS 型生成）を本気でやる時に再検討。
4. **安全網を先に敷く**（P0）: CI（gofmt / vet / test / build + Console build）、`main()` からの
   `buildMux()` 抽出 + httptest スモーク、エラーコード文字列の const 化。golangci-lint は既存
   コードへの指摘で red 開始になるため初期スコープから除外（後続 opt-in）。

## 帰結

- フェーズ順は P0（安全網）→ P1（Agent: runGit / fileStore[T] / decodeJSON 畳み込み → CLI 縦割り
  `internal/agents/{claude,codex,opencode}` ほか）→ P2（CP: プリアンブルラッパー / ルート登録分散 /
  `config`・`manager` 分解 / Store サブインターフェース）→ P3（任意: 契約の型化）。詳細は docs/23。
- 挙動に触るのは `manager.mu` のロックスコープ修正 1 点のみ（単独 PR）。それ以外の wave は
  純粋な再配置 + 畳み込みで、移動 wave とロジック wave を混ぜない。
- transcript パーサ 3 本は統合せず、同一パッケージ境界・同一出力型に揃えて並列性を構造として
  固定する（opencode usage 欠落等はインターフェースの未実装として可視化）。
