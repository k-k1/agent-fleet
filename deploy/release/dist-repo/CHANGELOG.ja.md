# 変更履歴

[Agent Fleet](https://github.com/k-k1/agent-fleet-dist) のリリースノート索引です。各項目はリリース
ページへのリンクで、完全なノートはそちらにあります。English: [CHANGELOG.md](CHANGELOG.md).

## [0.12.2](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.12.2) — 2026-08-25

エージェントのメモリを履歴ごと引き継げるようになり、ワークスペースのリソースが ECS 構成でも
測れるようになりました。管理画面の「通信」が Postgres 配備で開けなかったのも直しています。

## [0.12.1](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.12.1) — 2026-08-25

0.12.0 で実際の `ecs-ec2` 配備を立てて見つかった修正が 3 件です。Graviton で立てたはずの
Control Plane が黙って x86_64 で上がる、AWS Organizations のメンバーアカウントでは
メンバー別の費用が出ない、作ったばかりのワークスペースにエージェントのメモリを取り込めない、
の 3 つを直しました。

## [0.12.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.12.0) — 2026-08-25

人の返事を待っているだけの Workspace が、起動したまま課金され続けることはなくなりました。
畳むときに画面に出ていた質問・計画の承認・許可要求は取っておき、あとからセッション一覧で
答えられます。ツールの承認は「毎回スキップ」ではなく選べるようになり、セッションを同じ
テナントの別メンバーへ引き継げるようになりました。

## [0.11.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.11.0) — 2026-08-23

git の OAuth アプリをテナント管理者が自分で登録できるようになり、Control Plane を
Graviton で動かせるようになりました。「起動中」から進まなくなる、あるいは黙って費用が
かかり続ける `ecs-ec2` の不具合もまとめて直しています。

## [0.10.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.10.0) — 2026-08-22

Workspace が動くマシンを、テナントがメンバーごとに選べるようになりました（Graviton
を含みます）。`.drawio` のベンダーアイコンが描かれるようになり、管理画面に「作る」
しか無かった操作へ削除が加わりました。壊れて見えていたセッションの修正も入っています。

## [0.9.3](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.9.3) — 2026-08-21

管理者向けの修正が 1 件です。自分自身を名簿から外せるようになりました（外せないのは
「最後の 1 つ」だけです）。

## [0.9.2](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.9.2) — 2026-08-21

0.9.0 の EC2 永続 Workspace を実デプロイとして動かして分かった問題の追補です。
事前に焼いた *golden* スナップショットから作った home はまったく起動できず、golden を
手で焼く手順には終わらない工程がありました。

## [0.9.1](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.9.1) — 2026-08-21

AWS デプロイ向けの保守リリースです。実際に 1 本立ち上げて分かった問題を直しました。
ECS では読み上げがそもそも動いておらず、Control Plane の起動後に作られた Workspace に
到達できず、新規デプロイが最初に見せる 2 つの画面が空白でした。

## [0.9.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.9.0) — 2026-08-21

サインインが Google 固定ではなくなり、任意の OpenID Connect プロバイダと GitHub を
使えるようになりました。1 つのデプロイを、独自のサインインページ・名簿・接続元制限を
持つ部署に分けられます。AWS には、各人の home を停止しても消えないディスクに置く
新しいランタイムが加わり、クラウドの請求額をメンバーごとに表示します。Console では
`.drawio` が図として開き、セッションごとに変更したファイルが並び、共有した相手にも
届くマーカーを会話に引けます。

## [0.8.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.8.0) — 2026-08-13

セッションの会話を同じテナントの他のメンバーに見せられるようになり、過去の発言
時点から会話ごと分岐した新しいセッションを作れるようになりました。メイン領域は
分割だけでなくタブのグリッドにもでき、セッション同士が人を介さず一言メッセージを
送れるようになりました。

## [0.7.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.7.0) — 2026-08-07

OpenCode に Console から opencode.ai アカウントでサインインでき、セッションを動かす
プランを選べるようになりました。中断した Claude のターンはアシスタントを介さずに
自分で再開し、AWS の Agent Toolkit が内蔵 MCP 連携に加わりました。

## [0.6.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.6.0) — 2026-08-02

エージェントが操作しているブラウザのページを利用者に渡し、最後のクリックだけ人が
引き取れるようになりました。Console は日本語へ落ちずに英語のまま通します。
**本リリースで Docker Compose 版の導入方法が変わります。コンテナイメージは tar から
読み込むのではなく GHCR から pull します。**

## [0.5.1](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.5.1) — 2026-08-01

Console のバンドルが抱えていた内部文字列を取り除いた再ビルドと、再発を止めるための
ゲートです。**0.1.0 から 0.5.0 のダウンロードは取り下げました。本リリースをお使い
ください。**

## [0.5.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.5.0) — 2026-08-01

作業グループで左ペインをプロジェクト単位に分けられるようになり、自分の MCP サーバー
を登録でき、エージェントのメモリが版管理され、エディタが変更案を出して差分で確認
できるようになりました。

## [0.4.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.4.0) — 2026-07-27

トークン使用量を機能別に見られるようになり、Console でファイルを編集でき、ロック
したものが消えなくなり、途中で落ちたターンが自分で再開するようになりました。

## [0.3.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.3.0) — 2026-07-25

Kiro がエージェント CLI に加わり、入力欄が返信候補を出すようになり、fleet
オペレーターが代わりに質問へ答えプランを審査できるようになりました。

## [0.2.3](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.2.3) — 2026-07-24

メモをセッションへ直接ドラッグできるようになりました。細かい修正も入っています。

## [0.2.2](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.2.2) — 2026-07-24

ペインを別のブラウザタブに切り離せるようになり、スケジュールに詳細・編集画面が
付き、Cursor でアシスタントチャットを動かせるようになりました。

## [0.2.1](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.2.1) — 2026-07-23

Bitbucket 接続の修正と、セッション引き継ぎモーダルの統一です。

## [0.2.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.2.0) — 2026-07-23

これまでで最大のリリースです。Discord と Slack からセッションを動かせるように
なり、プロンプトを定時実行でき、Cursor CLI が加わり、Subversion の作業コピーに
対応し、`native` のホストバイナリが自分で更新するようになりました。

## [0.1.2](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.1.2) — 2026-07-21

`native` ランタイムで Claude セッションが入力をまったく受け付けなくなる問題の
hotfix です。

## [0.1.1](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.1.1) — 2026-07-21

最初のリリースに続く、ドキュメントとパッケージングの追補です。Console の変更が
1 件入っています。

## [0.1.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.1.0) — 2026-07-21

最初の公開リリースです。Agent Fleet は AI コーディングエージェントを群れとして
動かすためのセルフホスト Console で、メンバーごとに隔離された環境が与えられ、
ブラウザからエージェントのセッションを動かします。
