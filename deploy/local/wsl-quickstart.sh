#!/usr/bin/env bash
# 後方互換ラッパー: 起動スクリプトは run-dev.sh に一本化された（`run-dev.sh wsl` が
# 本体）。挙動は従来どおり — 認証もテナントも無しの単一ユーザー(AUTH=dev)で CP を
# ホスト起動し、ワークスペースはローカル Docker(AF_RUNTIME=local)で回す。rtk は
# 全ビルド共通で常時イメージ焼き込み、JDK は共有 bind-mount(WS_JDK=0 で省略)。
# セットアップ手順（native dockerd 導入・依存）は deploy/local/README-wsl.md。
#
# 使い方（従来と同じ。WS_JDK / WS_SMOKE 等の env もそのまま効く）:
#   deploy/local/wsl-quickstart.sh
#   WS_JDK=0 deploy/local/wsl-quickstart.sh
# データ初期化は: deploy/local/run-dev.sh reset [--all]
exec "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/run-dev.sh" wsl "$@"
