# 変更履歴

[Agent Fleet](https://github.com/k-k1/agent-fleet-dist) のリリースノート索引です。各項目はリリース
ページへのリンクで、完全なノートはそちらにあります。English: [CHANGELOG.md](CHANGELOG.md).

## [0.8.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.8.0) — 2026-08-13

セッションの会話を同じテナントの他のメンバーへ読み取り専用で見せられるようになり、
過去の発言から会話をそのまま引き継いだ新しいセッションへ分岐できるようになりました。
メイン領域は分割ペインに加えてタブ付きグリッドで並べられ、セッション同士はあなたを
経由せずに一行のメッセージを送り合えます。

## [0.7.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.7.0) — 2026-08-07

opencode.ai のアカウントで Console からサインインできるようになり、セッションが使う枠も
選べます。中断した Claude のターンはアシスタントを介さずエージェント自身が再開し、
AWS の Agent Toolkit が組み込み MCP 連携に加わりました。

## [0.6.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.6.0) — 2026-08-02

エージェントが操作中のブラウザページを人へ引き渡せるようになり、最後のクリックだけを
自分で行えます。Console は日本語に倒れることなく表示言語で一貫して応答します。
**本リリースで Docker Compose の導入手順が変わります: コンテナイメージは image tar の
読み込みではなく GHCR からの pull で取得します。**

## [0.5.1](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.5.1) — 2026-08-01

Console のバンドルに焼き込まれていた内部文字列を除去した再ビルドと、再発を止める
ゲートの追加です。**0.1.0〜0.5.0 のダウンロードは撤回しました。本リリースをご利用
ください。**

## [0.5.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.5.0) — 2026-08-01

作業グループで Console の左ペインを案件ごとに分けられるようになり、独自の MCP
サーバーを登録でき、エージェントのメモリはバージョン管理されて持ち運べるように
なりました。エディタは AI の変更案を差分でレビューしてから取り込めます。

## [0.4.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.4.0) — 2026-07-27

機能別のトークン使用量が見えるようになり、Console の File ペインで本文を編集でき、
ロックした対象は消えなくなりました。途中で切れたターンの自動再開も入ります。

## [0.3.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.3.0) — 2026-07-25

エージェント CLI に Kiro が加わり、コンポーサーに返信サジェストが付き、フリート・
オペレーターが質問への回答とプランのレビューを代行できるようになりました。

## [0.2.3](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.2.3) — 2026-07-24

メモをセッションへ直接ドラッグできるようになりました。あわせて小さな修正をいくつか
入れています。

## [0.2.2](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.2.2) — 2026-07-24

ペインをブラウザの別タブへ切り離せるようになり、スケジュールに詳細・編集画面が付き、
アシスタントチャットのバックエンドに Cursor を選べるようになりました。

## [0.2.1](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.2.1) — 2026-07-23

Bitbucket 接続の修正と、セッション引き継ぎモーダルの統一です。

## [0.2.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.2.0) — 2026-07-23

これまでで最大のリリースです。Discord / Slack からセッションを操縦できるようになり、
プロンプトを定時実行でき、Cursor CLI が加わり、Subversion の作業コピーに対応し、
`native` のホストバイナリが自己更新するようになりました。

## [0.1.2](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.1.2) — 2026-07-21

`native` ランタイムで Claude セッションが入力を一切受け付けなくなる不具合の hotfix
リリースです。あわせて配布リポジトリのドキュメントを大幅に充実させました。

## [0.1.1](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.1.1) — 2026-07-21

最初のリリースに対するドキュメント・パッケージングの追い込みと、Console の変更 1 件です。

## [0.1.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.1.0) — 2026-07-21

最初の公開リリースです。Agent Fleet は、AI コーディングエージェントをフリートとして
運用するためのセルフホスト型コンソールです。メンバーごとに隔離された環境が与えられ、
エージェントのセッションをブラウザから起動・操作・管理できます。
