#!/usr/bin/env bash
# GitHub CLI（gh）透過認証ラッパー。
#
# gh は git の credential.helper を参照しないため、そのままでは Connections
# （`workspace-agent cred`）で保存した GitHub トークンを使えず `gh auth login` が
# 別途必要になる。このラッパーを PATH 上の実 gh（/usr/local/libexec/gh）より前段に
# 置き、呼び出しのたびに git と同一のヘルパーからトークンを取り出して GH_TOKEN に
# 注入することで、全ユーザーが意識せず gh を使えるようにする（docs/build/08 §8.3）。
#
# - 明示的な GH_TOKEN / GITHUB_TOKEN があればそれを尊重（上書きしない）。
# - 都度取得なので git と同じ鮮度。トークン失効/ローテーションに自己修復する。
# - 注入対象は github.com のみ。GitHub Enterprise は従来どおり利用者が自分で設定する。
if [ -z "${GH_TOKEN:-}" ] && [ -z "${GITHUB_TOKEN:-}" ]; then
  tok="$(printf 'protocol=https\nhost=github.com\n\n' | git credential fill 2>/dev/null | sed -n 's/^password=//p')"
  [ -n "${tok:-}" ] && export GH_TOKEN="$tok"
fi
exec /usr/local/libexec/gh "$@"
