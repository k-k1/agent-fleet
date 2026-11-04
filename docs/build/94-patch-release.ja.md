---
audience: "リリースを切るメンテナと、脆弱性トラッキング issue を読む人"
source_of_truth: ".github/workflows/image-vuln-scan.yml、deploy/local/vuln-scan-report.py、deploy/local/vuln-scan-issue.sh、.github/workflows/publish-dist.yml"
---

# 94. イメージの脆弱性スキャンと、必要なときのパッチリリース

[English](94-patch-release.md) | 日本語

配布イメージは浮動ベースタグ（`debian:trixie-slim`・`node:22-trixie-slim`・`golang:1.26-trixie`）から
`apt-get upgrade` なしでビルドされ、Go のバイナリは再ビルドされるまで新しい標準ライブラリを取り込まない。
そのためリリースにはビルド当日の状態が入り、次のリリースまで何も動かない。ホスト側は別の仕組み
（Fargate・SSM 解決の AMI）で足りている。本書はイメージ側、つまり未適用の修正を**検知**する方法と、
検知から**パッチリリース**までの安い経路を扱う。定期的なパッチリリースは採用していない。出すかどうかは
issue を見てメンテナが決める。決定の記録: [#3320](https://github.com/k-k1/agent-fleet/issues/3320)。

## 94.1 検知: `image-vuln-scan.yml`

毎週月曜、`publish-dist` が成功するたび、手動実行
（`gh workflow run image-vuln-scan.yml [-f version=0.40.0]`）で走る。

| 対象 | 取り方 | スキャナ |
|---|---|---|
| `control-plane` イメージ | `ghcr.io/k-k1/agent-fleet/control-plane:<version>`（`deploy/compose/release.sh` が push するタグ。`v` は付かない） | Trivy |
| `workspace` イメージ | `ghcr.io/k-k1/agent-fleet/workspace:<version>` | Trivy |
| native rootfs | dist リリース `v<version>` の `agent-fleet-rootfs-*-linux-amd64.tar.zst` を展開 | Trivy |
| CP バイナリ | `agent-fleet-native-<version>-linux-amd64.tar.gz` の `bin/af-cp` | `govulncheck -mode=binary` |
| Agent バイナリ | 上記 rootfs の `usr/local/bin/workspace-agent` | `govulncheck -mode=binary` |

`<version>` は dist repo の draft / prerelease でない `vX.Y.Z` のうち最大のもの。スキャンするのは amd64 側
（両アーキテクチャとも同じ Dockerfile・同じ Go 版からビルドされる）。

**対象になるもの:** Trivy は HIGH か CRITICAL で、かつ修正版があるもの。Go の脆弱性 DB には深刻度が
ないので、`govulncheck` は修正版のある finding をすべて数える（binary モードはバイナリに実在する
シンボルだけを報告する）。

**issue を持つのは最新リリースだけ。** 古い `version` を指定した dispatch もスキャンは行う（実行サマリと artifact）が、issue には触れない。issue を触る直前に最新をもう一度確認するので、スキャン中に出た新リリースの issue を古い結果で閉じたり上書きしたりしない。ダウンロードしたパッケージと rootfs はデータとして扱い、中身は実行しない。rootfs は `vuln-scan-extract.py` が、ディレクトリの外に出るパスやリンクを拒否して展開する。

**issue は 1 件・冪等。** ラベル `image-vuln-scan`、ワークフローが起票したものだけが対象。指摘があって
issue がない: 起票。あって issue もある: 本文が変わるときだけ書き換えてコメント（本文に時刻や実行 URL を
入れないのはこのため）。指摘がなく issue がある: コメントして close。どちらもない: 何もしない。

**スキャンできなかった実行は赤で、clean ではない。** Trivy は固定した sha256 で検証し、`govulncheck` は
固定版を入れ、`vuln-scan-report.py` は欠落・空・スキャナ出力でないファイルを exit 2 にする。赤い実行は
issue をそのままにする。

各行の **Remedy**:

- `rebuild`: OS パッケージ、または `af-cp` / `workspace-agent` 内の Go 標準ライブラリ。ベースイメージと
  Go toolchain を新しくして再ビルドすれば直るので、パッチリリースの対象。
- `bump`: Go モジュール、npm パッケージ、焼き込んだツール（古い toolchain でビルドされたツール自身のピン）。
  再ビルドでは動かない。通常の PR で `main` に入れ、次のリリースに載せる。待てない場合はパッチリリースにも
  その bump を載せる（下記）。

テスト: `deploy/local/vuln-scan-report-test.sh`（fixture と偽 `gh`。`ci.yml` で実行）。スケジュール実行は
執筆時点で GitHub 上では未観測で、スキャナのコマンドは公開済みの 0.40.0 に対して手元で実行した。

## 94.2 必要なときのパッチリリース

issue に `rebuild` の行があり、通常のリリースがすぐには出ないときに使う。**最後のリリースタグ**から
`0.x.y+1` を作るので、`main` の未リリースの作業は入らない。

制約:

- パッチを当てるのは**最新**の公開版だけ。それより新しい版があるなら `main` からリリースする。GitHub は
  *最後に作られた*リリースを latest とみなし、インストーラと `af update` はそれを辿るので、古い系統を
  最後に公開すると逆向きを指してしまう。
- タグやイメージの再 push はしない。公開済みのタグとダイジェストは不変で、修正は新しい版で出す。
- リリースのイメージはクリーンなランナーでレイヤキャッシュなしにビルドされる（リリースでは
  `CP_CACHE_REF` / `WS_CACHE_REF` は空）ので、浮動ベースタグと golang イメージは*ビルド時点*のものに
  解決される。これが更新のすべてで、そのための Dockerfile 変更は要らない。修正が `golang:1.26-trixie` より
  新しい Go マイナーにしかない場合は、再ビルドでは入らない。通常の PR で Dockerfile と `go` ディレクティブを上げる。

手順（自分のシェルで。例は `0.40.0` → `0.40.1`）:

```bash
# 1) 最後のタグから release ブランチを切る。publish-dist.yml も Dockerfile もタグ時点のもの（それが狙い）。
git fetch origin --tags
git switch -c release/0.40.1 v0.40.0

# 2) Debian パッケージのピン。publish の preflight は trixie-security がもう配らないピンで落ちる。
#    古いタグではまさにそれが起きる。
deploy/local/apt-pin-check.sh        # exit 0 = 何もしなくてよい
#    そうでなければ main の apt-pin-bump の ARG 値を持ち込むか deploy/local/apt-pin-bump.sh を実行し、commit する。

# 3) リリースノートは通常どおり（deploy/release/notes/README.md）: 0.40.1.md と 0.40.1.ja.md、
#    SUMMARY.md / SUMMARY.ja.md の節、index.tsv 末尾に `pending` 行、deploy/release/gen-changelog.sh。
#    ノートは利用者に何が届くかだけ書く:
#    「ベースイメージと Go toolchain を更新し、セキュリティ修正を取り込みました。対応は不要です。」
git commit -m "docs(release): draft 0.40.1 notes, ledger row and SUMMARY"
git push origin release/0.40.1

# 4) 既存の流れでそのブランチから publish する。
gh workflow run publish-dist.yml --ref release/0.40.1 -f version=0.40.1

# 5) ビルド元 commit を記録してタグを打ち push する（通常のリリースと同じ 2 手順。[deploy/release/notes](../../deploy/release/notes/README.md)）。
#    index.tsv の `pending` を、その実行がビルドした commit に置き換える。
git tag -a v0.40.1 <build commit> -m "agent-fleet 0.40.1" && git push origin v0.40.1

# 6) ノート・SUMMARY・台帳の行・CHANGELOG を PR で main に持ち込み、main の台帳を欠けなくする
#    （release-gate は台帳の全行に SUMMARY の節があることを検査する）。
```

ブランチからの `publish-dist.yml` 実行が通るのは、ワークフローのファイルが default branch と対象 ref の
両方にあるから。それ以外（スクリプト・Dockerfile）は ref から読まれる。実行が成功すると
`image-vuln-scan` が自動で走り、直せるものが残っていなければ issue を close する。close されないなら、
残りは `bump` の行か、ベースイメージにまだ修正が来ていないものである。

デプロイ側にとってパッチ版は普通の新しい `ImageTag` で、特別な展開手順はない。通常の publish 手順は
[deploy/release/notes](../../deploy/release/notes/README.md#publishing-a-version) にある。
