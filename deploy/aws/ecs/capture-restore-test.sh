#!/usr/bin/env bash
# Agent Fleet — 「控えの復旧点」まわりの回帰（deploy/aws/ecs/env.sh の解説が本体）。
#
#   deploy/aws/ecs/capture-restore-test.sh
#
# ## なぜ要るか
#
# 配備物のスクリプトは **実 AWS でしか全体は動かない**。だからといって「実配備で
# 確かめるまで何も確かめられない」わけではない —— 3 点セット（ECR 内の再タグ ／
# 控えが --force 無しでは古いまま ／ ECR の EmptyOnDelete）が作る**分岐**は、
# `aws` と `crane` を差し替えれば手元で踏める。ここで測るのはその分岐だけで、
# **CFN が通るか・実際に立つかは測っていない**（それは実配備でしか分からない）。
#
# ## 差し替え方
#
#   - `aws` / `crane` … PATH の先頭に置いた偽物。呼ばれた引数で答えを決める
#   - 控えの置き場   … `AF_DEPLOY_STATE_DIR`（env.sh の af_state_root が読む）
#   - GHCR の中身    … `AF_TEST_GHCR`（偽 crane が読む「在るタグ」の一覧）
#
# ⚠️ **偽物が黙って何も答えないと、検査は「緑」になる。** だから各ケースは
# **「その分岐でしか出ない文字列」を要求**し、最後に**偽物が実際に呼ばれた回数**も見る。
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
PASS=0
FAIL=0

ok()   { PASS=$((PASS + 1)); echo "  OK   $1"; }
bad()  { FAIL=$((FAIL + 1)); echo "  NG   $1"; [ -n "${2:-}" ] && echo "       $2"; return 0; }
# want <名前> <ファイル> <部分文字列> — 出力にその文字列が在ること。
want() { if grep -qF "$3" "$2"; then ok "$1"; else bad "$1" "出力に無い: $3"; fi; }
# nowant <名前> <ファイル> <部分文字列>
nowant() { if grep -qF "$3" "$2"; then bad "$1" "出てはいけない文字列が出た: $3"; else ok "$1"; fi; }

# --- 偽の aws（この配備の「生きている姿」を 1 か所に持つ）--------------------
mkdir -p "$T/bin"
cat > "$T/bin/aws" <<'STUB'
#!/usr/bin/env bash
# 呼ばれた引数を記録する（「偽物が呼ばれていない緑」を見分けるため）。
echo "$*" >> "${AF_TEST_AWS_LOG:-/dev/null}"
args="$*"
stack=""
prev=""
for a in "$@"; do
  [ "$prev" = "--stack-name" ] && stack="$a"
  prev="$a"
done
param() {  # param <stack> <key>
  case "$1.$2" in
    af-ecs-ingress.Fqdn)             echo "af.example.test" ;;
    af-ecs-ingress.NetworkStackName) echo "af-ecs-network" ;;
    af-ecs-ingress.DataStackName)    echo "af-ecs-data" ;;
    af-ecs-ingress.PlatformStackName) echo "af-ecs-platform" ;;
    af-ecs-ingress.WsRuntime)        echo "ecs" ;;
    af-ecs-ingress.ImageTag)         echo "${AF_TEST_LIVE_TAG:-0.0.9-dev-abcd1234}" ;;
    af-ecs-ingress.SsmPrefix)        echo "/af-cp" ;;
    af-ecs-ingress.CpArch)           echo "x86_64" ;;
    af-ecs-ingress.Ec2SlotLaunchTemplate) echo "" ;;
    af-ecs-data.Persistence)         echo "delete" ;;
    *) echo "" ;;
  esac
}
case "$args" in
  *"ParameterKey=='"*)
    key="${args#*ParameterKey==\'}"; key="${key%%\'*}"
    param "$stack" "$key" ;;
  *"OutputKey=='"*) echo "" ;;
  *"Parameters[].join"*)
    printf 'Fqdn=af.example.test\tImageTag=%s\tSsmPrefix=/af-cp\n' "${AF_TEST_LIVE_TAG:-0.0.9-dev-abcd1234}" ;;
  *"Outputs[].join"*) echo "" ;;
  *"cloudformation describe-stacks"*)
    case "$stack" in af-ecs-*) exit 0 ;; *) exit 254 ;; esac ;;
  *"list-exports"*) echo "" ;;
  *"sts get-caller-identity"*) echo "111122223333" ;;
  *) echo "" ;;
esac
exit 0
STUB

# --- 偽の crane（GHCR に何が在るかは AF_TEST_GHCR が決める）------------------
cat > "$T/bin/crane" <<'STUB'
#!/usr/bin/env bash
echo "$*" >> "${AF_TEST_CRANE_LOG:-/dev/null}"
[ "${1:-}" = "manifest" ] || exit 0
ref="${2##*/}"          # <repo>:<tag>
for have in ${AF_TEST_GHCR:-}; do
  [ "$have" = "$ref" ] && exit 0
done
exit 1
STUB
chmod +x "$T/bin/aws" "$T/bin/crane"
PATH="$T/bin:$PATH"
export PATH

export AF_DEPLOY_STATE_DIR="$T/state"
export AF_TEST_AWS_LOG="$T/aws.log"
export AF_TEST_CRANE_LOG="$T/crane.log"
ENVDIR="$T/state/dev.ap-northeast-1"

capture() { "$HERE/capture-env.sh" --profile dev --region ap-northeast-1 "$@"; }

echo "== 1. 復旧点が GHCR に揃っている配備を捕まえる =="
rm -rf "$T/state"
AF_TEST_GHCR="control-plane:0.0.9-dev-abcd1234 workspace:0.0.9-dev-abcd1234" \
  capture > "$T/c1.out" 2> "$T/c1.err" || bad "capture が落ちた" "$(tail -3 "$T/c1.err")"
want "控えに AF_IMAGE_TAG が入る" "$ENVDIR/env" "AF_IMAGE_TAG=0.0.9-dev-abcd1234"
want "復旧点は yes と記録される" "$ENVDIR/env" "AF_IMAGE_RECOVERABLE=yes"
nowant "揃っているときは警告を出さない" "$T/c1.err" "復旧点は**このままでは失われる**"

echo "== 2. workspace が GHCR に無い（ECR 内で再タグしただけのタグ）=="
rm -rf "$T/state"
AF_TEST_GHCR="control-plane:0.0.9-dev-abcd1234" \
  capture > "$T/c2.out" 2> "$T/c2.err" || bad "capture が落ちた" "$(tail -3 "$T/c2.err")"
want "復旧点は no と記録される" "$ENVDIR/env" "AF_IMAGE_RECOVERABLE=no"
want "撤収前に持ち出せと言う" "$T/c2.err" "crane copy"
want "どのタグが失われるかを名指しする" "$T/c2.err" "0.0.9-dev-abcd1234"

echo "== 3. crane が無い＝測れない（「無い」と決めつけない）=="
# ⚠️ **偽物を消すだけでは足りない。** この Workspace には本物の crane が入っていて
# （`~/.local/bin/crane`）、消した瞬間にそちらへ落ちる —— 本物は GHCR を実際に引きに
# 行って「無い」を返すので、**測れなかった（unknown）が測った結果（no）に化ける。**
# だから PATH ごと最小に絞って、crane がどこにも無い状態を作る。
rm -rf "$T/state"
mv "$T/bin/crane" "$T/crane.hidden"
env PATH="$T/bin:/usr/bin:/bin" AF_DEPLOY_STATE_DIR="$AF_DEPLOY_STATE_DIR" AF_TEST_AWS_LOG="$AF_TEST_AWS_LOG" \
  "$HERE/capture-env.sh" --profile dev --region ap-northeast-1 > "$T/c3.out" 2> "$T/c3.err" \
  || bad "capture が落ちた" "$(tail -3 "$T/c3.err")"
want "測れないときは unknown" "$ENVDIR/env" "AF_IMAGE_RECOVERABLE=unknown"
nowant "unknown を「失われる」と言わない" "$T/c3.err" "復旧点は**このままでは失われる**"
mv "$T/crane.hidden" "$T/bin/crane"

echo "== 4. 既に捕まえてある＋配備が動いた → 断りに「何が古いか」が出る =="
rm -rf "$T/state"
AF_TEST_GHCR="control-plane:0.0.9-dev-abcd1234 workspace:0.0.9-dev-abcd1234" capture > /dev/null 2>&1
if AF_TEST_LIVE_TAG="0.1.0-dev-99999999" capture > "$T/c4.out" 2> "$T/c4.err"; then
  bad "2 度目の capture は落ちるべき" "落ちなかった"
else
  ok "2 度目の capture は --force を要求して落ちる"
fi
want "控えの古いタグを名指しする" "$T/c4.err" "AF_IMAGE_TAG=0.0.9-dev-abcd1234"
want "いま動いているタグを名指しする" "$T/c4.err" "0.1.0-dev-99999999"

echo "== 5. --force で取り直すと、印（AF_DEV_DEPLOY=1）が残ったまま新しくなる =="
sed -i 's/^AF_DEV_DEPLOY=0/AF_DEV_DEPLOY=1/' "$ENVDIR/env"
AF_TEST_LIVE_TAG="0.1.0-dev-99999999" AF_TEST_GHCR="control-plane:0.1.0-dev-99999999 workspace:0.1.0-dev-99999999" \
  capture --force > "$T/c5.out" 2> "$T/c5.err" || bad "--force の capture が落ちた" "$(tail -3 "$T/c5.err")"
want "タグが新しくなる" "$ENVDIR/env" "AF_IMAGE_TAG=0.1.0-dev-99999999"
want "開発配備の印が残る" "$ENVDIR/env" "AF_DEV_DEPLOY=1"

echo "== 6. af_env_set は 1 行だけ差し替える（他の行を落とさない）=="
# ★ dev-deploy.sh が「デプロイのたびに控えを追従させる」ときに使う関数。
# **控えを丸ごと書き直すと AF_DEV_DEPLOY のような AWS 側に無い情報が消える**ので、
# 1 行だけを差し替えることそのものが仕様。
(
  # shellcheck source=deploy/aws/ecs/env.sh
  . "$HERE/env.sh"
  AF_ENV_DIR="$ENVDIR"
  af_env_set AF_IMAGE_TAG "9.9.9-dev-cafe0000"
  af_env_set AF_IMAGE_RECOVERABLE no
  af_env_set AF_BRAND_NEW_KEY 1
)
want "既存の行を差し替える" "$ENVDIR/env" "AF_IMAGE_TAG=9.9.9-dev-cafe0000"
want "他の行は残る" "$ENVDIR/env" "AF_DEV_DEPLOY=1"
want "無い行は足す" "$ENVDIR/env" "AF_BRAND_NEW_KEY=1"
if [ "$(grep -c '^AF_IMAGE_TAG=' "$ENVDIR/env")" = 1 ]; then ok "AF_IMAGE_TAG は 1 行のまま"; else bad "AF_IMAGE_TAG が増えた"; fi

echo "== 7. teardown は消す前に「復旧点が失われる」と言う（--yes 無し＝計画だけ）=="
rm -rf "$T/state"
AF_TEST_GHCR="control-plane:0.0.9-dev-abcd1234 workspace:0.0.9-dev-abcd1234" capture > /dev/null 2>&1
# 7-a. GHCR に揃っている → 立て直せると言う
AF_TEST_GHCR="control-plane:0.0.9-dev-abcd1234 workspace:0.0.9-dev-abcd1234" \
  "$HERE/teardown.sh" --profile dev --region ap-northeast-1 > "$T/t1.out" 2>&1 || true
want "揃っていれば「立て直せる」と言う" "$T/t1.out" "GHCR に両方ある"
nowant "揃っているのに脅かさない" "$T/t1.out" "復旧点が失われる"
# 7-b. workspace が GHCR に無い → 失われると言い、持ち出し方まで出す
AF_TEST_GHCR="control-plane:0.0.9-dev-abcd1234" \
  "$HERE/teardown.sh" --profile dev --region ap-northeast-1 > "$T/t2.out" 2>&1 || true
want "失われることを言う" "$T/t2.out" "復旧点が失われる"
want "いま何をすればよいかを出す" "$T/t2.out" "crane copy"
# 7-c. 控えが古い（配備が先へ動いている）→ そう言う
AF_TEST_LIVE_TAG="0.2.0-dev-11112222" AF_TEST_GHCR="control-plane:0.0.9-dev-abcd1234 workspace:0.0.9-dev-abcd1234" \
  "$HERE/teardown.sh" --profile dev --region ap-northeast-1 > "$T/t3.out" 2>&1 || true
want "控えが古いことを言う" "$T/t3.out" "控えが古い"
want "取り直し方を出す" "$T/t3.out" "capture-env.sh"
nowant "計画だけの実行では何も消さない" "$T/t3.out" "==> 1. stopping the control plane"

echo "== 8. 偽物が実際に呼ばれたか（「呼ばれていないから緑」を潰す）=="
# 🔴 上の判定は全部「文字列が出たか」なので、**偽の aws / crane が 1 度も呼ばれて
# いなくても、たまたま同じ文字列が出れば緑になりうる**。道具が動いた証拠を別に持つ。
n_aws="$(wc -l < "$AF_TEST_AWS_LOG")"
n_crane="$(wc -l < "$AF_TEST_CRANE_LOG")"
if [ "$n_aws" -gt 20 ]; then ok "偽の aws が呼ばれた（$n_aws 回）"; else bad "偽の aws がほとんど呼ばれていない（$n_aws 回）"; fi
if [ "$n_crane" -gt 5 ]; then ok "偽の crane が呼ばれた（$n_crane 回）"; else bad "偽の crane がほとんど呼ばれていない（$n_crane 回）"; fi
if grep -q 'manifest .*workspace:' "$AF_TEST_CRANE_LOG"; then ok "workspace の実在を GHCR に問い合わせている"; else bad "workspace を 1 度も問い合わせていない"; fi

echo ""
echo "== 9. 構文（実 AWS が要らない最低限の網）=="
for f in "$HERE"/*.sh "$HERE"/harness/*.sh; do
  if bash -n "$f"; then ok "bash -n $(basename "$f")"; else bad "bash -n $(basename "$f")"; fi
done

echo ""
if [ "$FAIL" = 0 ]; then
  echo "$PASS 件すべて OK"
else
  echo "$FAIL 件が NG（OK $PASS 件）"
  exit 1
fi
