# Agent Fleet 開発ドキュメント

[English](README.md) | 日本語

Audience: Agent Fleet のコードを変える人
Source of truth: コード（この棚と食い違ったらコードが正）
Updated: 2026-09

**ここは開発者のための棚です。**利用者向けの手順は、コンテナへ配られる別ツリー
[`guide/`](../guide/README.ja.md) にあります（このツリーは誰にも配られません）。

| 棚 | 中身 |
|---|---|
| [build/](build/README.ja.md) | どう動いているか——ワイヤ契約・責務・データの流れ・拡張の型 |
| [decisions/](decisions/) | なぜそうなっているか。決定の記録（追記型）。**捨てた選択肢も**書いてあるので、誰かがうっかり再挑戦せずに済む |
| [CONVENTIONS.ja.md](CONVENTIONS.ja.md) | 全ツリー共通の執筆規範。`scripts/docs-check.py` が CI と手元の両方で機械検査する |

## 3 つの読者

文書は読者で 3 つに分かれます（[ADR 0064](decisions/0064-docs-three-audiences.ja.md)）。

| 読者 | 置き場 | 配布 |
|---|---|---|
| 使ってみたい人 | ルートの [README.md](../README.md) | GitHub のみ |
| 使っている人 | [`guide/`](../guide/README.ja.md) | **コンテナへ配る**（ロールでは切らない） |
| コードを変える人 | `docs/`（ここ） | **誰にも配らない** |

**ディレクトリの境界がそのまま配布の境界です。**だから `guide/` から `docs/` へは
リンクできません（逆は自由）。詳しくは [CONVENTIONS §2](CONVENTIONS.ja.md)。

## 棚ではないもの

- **[log/](log/README.md)** — かつて `docs/NN-*.md` と `docs/history/` だった
  作業ジャーナルの凍結アーカイブ。保守しないし配りません。**実測値・本番事故の
  因果・上流 CLI の契約・何かを諦めた理由**——他のどこにも無い事実を引くために
  だけ存在します。現役の文書はここへリンクしません。
- [HANDOFF.md](HANDOFF.md) — 開発ホストの稼働状態とホスト固有の作法。毎日変わります。
- [roadmap.md](roadmap.md) / [CHANGELOG-handoff.md](CHANGELOG-handoff.md) —
  前向きの計画と、日付つきの作業ログ。
