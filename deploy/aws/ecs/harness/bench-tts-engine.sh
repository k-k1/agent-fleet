#!/usr/bin/env bash
# VOICEVOX engine sizing — what does the cheaper task actually cost you? (ADR 0070 P3)
#
#   AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1 \
#     deploy/aws/ecs/harness/bench-tts-engine.sh [--sizes 2048x4096,1024x2048] [--stack af-ecs-tts]
#
# For each "<cpu>x<memory>" it starts ONE engine task at that size, times synthesis from
# inside the VPC against it, prints a table and stops it. It also prints the cold-start
# breakdown ECS records on the task (image pull included), which is the input to the SOCI
# question.
#
# ## Why it exists
#
# P0 chose 2 vCPU / 4 GiB by measuring one 42-character sentence, and P0's own numbers
# then showed why that is not enough evidence: arm64 looked 20 % cheaper and lost on a
# SHORT sentence, where the real-time ratio (synthesis seconds ÷ audio seconds) crosses
# 1.0 and playback stops keeping up. So every size here is measured on the same four
# workloads, in the same run, against the same texts:
#
#   - short / medium / long single sentences (the ratio, where it is worst and best);
#   - a whole answer read one sentence at a time, which is the ONLY shape the real client
#     produces (it cuts at sentence ends and splits anything over 60 characters before it
#     calls the CP);
#   - concurrency at the client's real request size, because a reading keeps two syntheses
#     in flight and several members can listen at once;
#   - a ladder of single-request sizes, because 4 GiB was measured to OOM-kill the engine
#     and the number decision 17's cap has to sit under is that limit, not a yes/no. It is
#     deliberately the LAST thing each size does: the step that finds it kills the engine.
#
# ## What it does NOT touch
#
# ⚠️ Not the ECS service, and not the stack's parameters. The engine's desired count
# belongs to the Control Plane's on-demand controller: measured the hard way, an engine
# started here by scaling the service is stopped again within a minute or two by the
# controller's idle rule, and the run then hangs waiting for a task that has been taken
# away. So the engine is started with RunTask, at a size given by a task-level override,
# outside the service and outside Cloud Map — the CP neither sees it nor drives it, and
# nothing here can leave the real service running.
#
# ## Cost
#
# Two Fargate tasks (engine + bench) for about ten minutes per size: well under $0.10 at
# the measured $0.1232/hour for 2 vCPU / 4 GiB. Both are stopped on every exit path.
set -euo pipefail

REGION="${AWS_REGION:-ap-northeast-1}"
PROFILE_ARG=()
[ -n "${AWS_PROFILE:-}" ] && PROFILE_ARG=(--profile "$AWS_PROFILE")
AWS=(aws "${PROFILE_ARG[@]+"${PROFILE_ARG[@]}"}" --region "$REGION")

STACK=af-ecs-tts
NETWORK_STACK=af-ecs-network
PLATFORM_STACK=af-ecs-platform
SIZES="2048x4096,1024x2048"

while [ $# -gt 0 ]; do
  case "$1" in
    --stack) STACK="${2:?--stack needs a value}"; shift ;;
    --network-stack) NETWORK_STACK="${2:?--network-stack needs a value}"; shift ;;
    --platform-stack) PLATFORM_STACK="${2:?--platform-stack needs a value}"; shift ;;
    --sizes) SIZES="${2:?--sizes needs a value}"; shift ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '==> %s\n' "$*" >&2; }
export_of() { "${AWS[@]}" cloudformation list-exports --query "Exports[?Name=='$1'].Value" --output text; }

SERVICE="$(export_of "$STACK-TtsEcsService")"
CLUSTER="$(export_of "$PLATFORM_STACK-ClusterName")"
CP_SG="$(export_of "$NETWORK_STACK-CpSgId")"
TD="af-$STACK-voicevox"
LOG_GROUP="/af/$STACK/voicevox"
# Subnets and the engine's own security group are read off the service rather than from
# exports: they are the ones the engine really runs with, including the ingress rule that
# admits the CP's security group and nothing else.
SUBNETS="$("${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services "$SERVICE" \
  --query 'join(`,`, services[0].networkConfiguration.awsvpcConfiguration.subnets)' --output text)"
ENGINE_SG="$("${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services "$SERVICE" \
  --query 'services[0].networkConfiguration.awsvpcConfiguration.securityGroups[0]' --output text)"
[ -n "$CLUSTER" ] && [ -n "$SUBNETS" ] && [ -n "$CP_SG" ] && [ -n "$ENGINE_SG" ] || {
  echo "missing coordinates: cluster=$CLUSTER subnets=$SUBNETS cpsg=$CP_SG enginesg=$ENGINE_SG" >&2
  exit 1
}
say "cluster=$CLUSTER td=$TD engine-sg=$ENGINE_SG"

TASKS=()
# ⚠️ Armed before the first RunTask and covering every exit path: a run that dies
# mid-measurement must not leave a $0.12/hour task up for the night.
cleanup() {
  for t in ${TASKS[@]+"${TASKS[@]}"}; do
    "${AWS[@]}" ecs stop-task --cluster "$CLUSTER" --task "$t" --reason "bench-tts-engine cleanup" >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT

netcfg() { printf 'awsvpcConfiguration={subnets=[%s],securityGroups=[%s],assignPublicIp=DISABLED}' "$SUBNETS" "$1"; }

# run_task <security-group> <overrides-json> -> task arn on stdout
run_task() {
  "${AWS[@]}" ecs run-task --cluster "$CLUSTER" --launch-type FARGATE --task-definition "$TD" \
    --network-configuration "$(netcfg "$1")" --overrides "$2" \
    --query 'tasks[0].taskArn' --output text
}

wait_status() { # <task arn> <RUNNING|STOPPED> <tries>
  local i st
  for i in $(seq 1 "$3"); do
    st=$("${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$1" --query 'tasks[0].lastStatus' --output text)
    if [ "$st" = "$2" ] || [ "$st" = STOPPED ]; then echo "$st"; return 0; fi
    sleep 5
  done
  echo TIMEOUT
}

# --- the in-container bench ------------------------------------------------------
#
# It runs in the ENGINE's own image (curl is in it — measured on the Dockerfile's
# `apt-get install -y curl gosu`) with the CP's security group, which is the only thing
# the engine's ingress rule admits. Every duration comes from curl's own -w timers rather
# than date arithmetic, so it does not depend on awk or bc being present; the ratios are
# computed on this side from the raw fields.
#
# ⚠️ RunTask's overrides are capped at 8 KiB, so the long texts are BUILT here by
# repetition rather than passed in.
read -r -d '' BENCH_BODY <<'BENCH_EOF' || true
set -u
# Without a UTF-8 locale `wc -m` counts BYTES, and every "chars" below would read three
# times too large for Japanese — the one number the whole comparison is keyed on.
export LC_ALL=C.UTF-8
S=3
say() { echo "BENCH $*"; }
for i in $(seq 1 120); do curl -fsS -m 5 "$B/version" >/dev/null 2>&1 && break; sleep 2; done
curl -fsS -m 10 "$B/version" >/dev/null || { say "FATAL engine unreachable at $B"; exit 1; }
say "version=$(curl -fsS -m 10 "$B/version")"

# one synthesis: prints "RESULT <label> <chars> <aq_sec> <syn_sec> <wav_bytes> <rate>"
syn() {
  label="$1"; text="$2"
  chars=$(printf '%s' "$text" | wc -m)
  # Per-label temp files: the concurrency step below runs several of these at once, and a
  # shared /tmp/q.json would have them synthesising each other's parameters.
  q="/tmp/q.$label.json"; a="/tmp/a.$label.wav"
  aq=$(curl -sS -m 900 -o "$q" -w '%{time_total}' -X POST -G \
        --data-urlencode "text=$text" --data-urlencode "speaker=$S" "$B/audio_query") || { say "RESULT $label $chars FAILED audio_query"; return 1; }
  if ! grep -q outputSamplingRate "$q"; then say "RESULT $label $chars FAILED aq_body=$(head -c 120 "$q")"; return 1; fi
  rate=$(tr ',' '\n' < "$q" | sed -n 's/.*"outputSamplingRate": *\([0-9]*\).*/\1/p' | head -1)
  sy=$(curl -sS -m 900 -o "$a" -w '%{time_total} %{size_download}' -X POST \
        -H 'Content-Type: application/json' --data-binary @"$q" "$B/synthesis?speaker=$S") || { say "RESULT $label $chars FAILED synthesis"; return 1; }
  rm -f "$q" "$a"
  say "RESULT $label $chars $aq $sy ${rate:-0}"
}
alive() { curl -fsS -m 10 "$B/version" >/dev/null 2>&1 && echo yes || echo no; }

# The sentence set. Japanese prose of the shape the feature actually reads: an agent
# answer, not a notification chime.
S1="準備ができました。"
M1="読み上げの速さは、文章全体の長さよりも一文あたりの長さに強く効きます。"
P1="オンデマンド運転では、エンジンが停止している状態が通常です。"
P2="読み上げの需要が溜まった時点で自動的に起動し、誰も聞かなくなれば自動的に停止します。"
P3="起動には七十秒から八十秒ほどかかるため、その間の日本語はポリーが代読します。"
P4="ここで測っているのは、合成にかかった秒数を音声の秒数で割った実時間比です。"
P5="この値が一を超えると、再生が合成に追いつかなくなり、文と文の間に沈黙が生まれます。"
P6="短い文ほど比は悪くなるので、短文だけを見て構成を決めると同じ判断を繰り返します。"

# 1) single sentences. The first call pays the lazy model load (measured: 650 ms), so it
#    is made first and reported separately rather than averaged into the rest.
syn warmup "$S1"
syn short "$S1"
syn medium "$M1"
syn long "$P1$P2$P3$P4$P5$P6"

# 2) a whole answer, one sentence at a time — the only shape the real client produces.
ANSWER=""
for i in 1 2 3 4 5 6; do ANSWER="$ANSWER$P1$P2$P3$P4$P5$P6"; done
say "ANSWER_CHARS=$(printf '%s' "$ANSWER" | wc -m)"
n=0
# ⚠️ Split with sed, not IFS='。': IFS splits on BYTES and would cut multibyte characters
# in half.
printf '%s' "$ANSWER" | sed 's/。/。\n/g' > /tmp/lines.txt
while IFS= read -r line; do
  [ -n "$line" ] || continue
  n=$((n+1))
  syn "answer_$n" "$line" || break
done < /tmp/lines.txt

# 3) concurrency, at the shape the real client produces: it cuts at sentence ends and
#    splits anything over 60 characters, and every reading keeps two syntheses in flight —
#    so N listeners means 2N requests of at most 60 characters. "It fits in 2 GiB" has to
#    mean under that, not under one sentence at a time.
#
#    ⚠️ This runs BEFORE the ladder below, because both can kill the engine and there is
#    one engine per size. A death here is the headline result and makes the rest moot.
build() { # build <chars> -> a text of at least that many characters
  out=""
  while [ "$(printf '%s' "$out" | wc -m)" -lt "$1" ]; do out="$out$P4"; done
  printf '%s' "$out"
}
UNIT="$(build 60)"
say "CONC_UNIT $(printf '%s' "$UNIT" | wc -m)"
for n in 2 4 8; do
  say "CONC_TRY $n"
  i=1
  while [ "$i" -le "$n" ]; do
    syn "conc${n}_$i" "$UNIT" &
    i=$((i+1))
  done
  wait
  a="$(alive)"
  say "CONC_ALIVE $n $a"
  [ "$a" = yes ] || break
done

# 4) the single-request ceiling, as a ladder rather than a yes/no: this is the number
#    decision 17's cap has to sit under, and P0 only knew "400 survives, about 2,000
#    kills". Last, because the step that finds it kills the engine.
for chars in 250 350 500 700; do
  say "SINGLE_TRY $chars"
  syn "single_$chars" "$(build $chars)" || { say "SINGLE_FAILED $chars"; }
  a="$(alive)"
  say "SINGLE_ALIVE $chars $a"
  [ "$a" = yes ] || break
done
say "DONE"
BENCH_EOF

overrides_json() { # <cpu> <memory> <command-json-array>
  CPU="$1" MEM="$2" CMD="$3" python3 - <<'PY'
import json, os
print(json.dumps({
    "cpu": os.environ["CPU"],
    "memory": os.environ["MEM"],
    "containerOverrides": [{"name": "voicevox", "command": json.loads(os.environ["CMD"])}],
}))
PY
}

json_array() { python3 -c 'import json,sys;print(json.dumps(sys.argv[1:]))' "$@"; }

logs_of() { # <task arn>
  "${AWS[@]}" logs get-log-events --log-group-name "$LOG_GROUP" \
    --log-stream-name "voicevox/voicevox/${1##*/}" --start-from-head --limit 10000 \
    --query 'events[].message' --output text | tr '\t' '\n'
}

report() { # reads BENCH lines on stdin and turns them into a table
  awk '
    /^BENCH RESULT/ {
      label=$3; chars=$4; aq=$5; sy=$6; bytes=$7; rate=$8
      if (aq == "FAILED" || bytes+0 <= 44 || rate+0 == 0) { printf "  %-12s %s\n", label, "FAILED: " $0; next }
      total=aq+sy; audio=(bytes-44)/(rate*2)
      printf "  %-12s chars=%-5s synth=%7.2fs audio=%7.2fs ratio=%5.2f\n", label, chars, total, audio, total/audio
      if (label ~ /^answer_/) { asy+=total; aau+=audio; an++ }
      # A concurrent batch is reported per request and never folded into the serial
      # ANSWER ratio: its wall clock includes waiting for the rest of the batch.
      next
    }
    /^BENCH (ANSWER_CHARS|SINGLE_|CONC_|FATAL|version)/ { sub(/^BENCH /, "  "); print }
    END { if (an>0) printf "  %-12s %d sentences synth=%7.1fs audio=%7.1fs ratio=%5.2f\n", "ANSWER", an, asy, aau, asy/aau }
  '
}

IFS=',' read -r -a SIZE_LIST <<<"$SIZES"
for size in "${SIZE_LIST[@]}"; do
  cpu="${size%x*}"; mem="${size#*x}"; threads=$((cpu/1024))
  say "=== $cpu CPU units / $mem MiB (--cpu_num_threads $threads) ==="

  # The engine, at this size, outside the service. The command repeats the image's own
  # (the entrypoint only exec "$@"), because a container override replaces it whole.
  engine_cmd="$(json_array gosu user /opt/voicevox_engine/run --host 0.0.0.0 --disable_mutable_api --cpu_num_threads "$threads")"
  engine="$(run_task "$ENGINE_SG" "$(overrides_json "$cpu" "$mem" "$engine_cmd")")"
  [ -n "$engine" ] && [ "$engine" != None ] || { echo "run-task (engine) failed" >&2; exit 1; }
  TASKS+=("$engine")
  say "engine task ${engine##*/} — waiting for RUNNING"
  st="$(wait_status "$engine" RUNNING 60)"
  [ "$st" = RUNNING ] || { echo "engine task did not run ($st)" >&2; exit 1; }

  # The cold-start breakdown, and the private address the bench talks to. Cloud Map is
  # not involved here (this task is not in the service), so the IP is read directly.
  info="$("${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$engine" \
    --query 'tasks[0].{created:createdAt,conn:connectivityAt,pullStart:pullStartedAt,pullStop:pullStoppedAt,started:startedAt,cpu:cpu,memory:memory,ip:attachments[0].details[?name==`privateIPv4Address`].value|[0]}' \
    --output json)"
  echo "$info"
  ip="$(printf '%s' "$info" | python3 -c 'import json,sys;print(json.load(sys.stdin)["ip"])')"
  say "engine at $ip:50021"

  bench_cmd="$(json_array bash -c "B=http://$ip:50021
$BENCH_BODY")"
  bench="$(run_task "$CP_SG" "$(overrides_json 1024 2048 "$bench_cmd")")"
  [ -n "$bench" ] && [ "$bench" != None ] || { echo "run-task (bench) failed" >&2; exit 1; }
  TASKS+=("$bench")
  say "bench task ${bench##*/} — waiting (several minutes)"
  st="$(wait_status "$bench" STOPPED 240)"
  say "bench finished ($st)"

  out="$(logs_of "$bench" | grep '^BENCH ' || true)"
  printf '%s\n' "$out" > "/tmp/bench-tts-$size.log"
  echo
  echo "--- $cpu CPU units / $mem MiB ---"
  printf '%s\n' "$out" | report
  echo
  "${AWS[@]}" ecs stop-task --cluster "$CLUSTER" --task "$engine" --reason "bench done" >/dev/null
done
say "done — raw logs in /tmp/bench-tts-*.log"
