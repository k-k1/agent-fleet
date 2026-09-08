#!/usr/bin/env bash
# Ask the deployment's `llm` engine for an answer from EACH model, from inside the VPC.
#
#   harness/probe-llm-engine.sh --profile <p> --region <r> [--wake] [--stop]
#                               [--models a,b] [--prompt TEXT] [--engine URL] [--watch]
#
# ## Why this exists
#
# The sibling of `probe-image-engine.sh`, for the role that answers in text. The engine sits on
# a private subnet behind a security group that admits the Control Plane and nobody else, so the
# only ways to reach it are a member's session inside a Workspace or a task inside the VPC
# wearing the CP's security group. ADR 0072 P1's definition of done — "two models in the launch
# menu, each usable, and the swap between them answers on one attempt" — needs the second: the
# admin API that writes the catalogue rows is driven by a super_admin browser session, which AWS
# credentials are not.
#
# It reuses the ingest task definition for the same reason the image probe does (two containers,
# a shared volume, an `sh -c` entry point on both, and 60-engines has no template budget for a
# third task definition). The `fetch` container is overridden to a no-op because `upload`
# depends on it having SUCCEEDED.
#
# ## The engine's own API key
#
# llama-server is started with `--api-key` (a SecureString in SSM), so the probe has to present
# it. It is read INSIDE the task, by overriding the task role with the Control Plane's — the one
# role that already has `ssm:GetParameter` on `/af-ws/*`. Passing the key in a container
# override instead would put it in the `RunTask` request, i.e. in CloudTrail, for ever.
#
# ⚠️ An override takes ONE string per command: the EntryPoint is already ["sh","-c"], so
# passing ["sh","-c",<script>] runs `sh -c sh -c <script>`, which exits 0 having done nothing.
set -euo pipefail

PROFILE=""; REGION=""; WAKE=0; STOP=0; WATCH=0
MODELS=""; PROMPT="In one short sentence: what is a control plane?"
ENGINES_STACK="af-ecs-engines"; NET_STACK="af-ecs-network"; PLATFORM_STACK="af-ecs-platform"
KEY_PARAM="/af-ws/engine-llm-key"
# Cloud Map only has an A record while the SERVICE is up. --engine is for the other case: an
# engine task run standalone (the controller stops an on-demand SERVICE with a stale demand
# mark within a minute, whatever the desired count is set to by hand), reached by task IP.
ENGINE_URL="http://llm.af.internal:8080"

usage() { sed -n '2,30p' "$0" >&2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --profile) PROFILE="${2:?}"; shift ;;
    --region)  REGION="${2:?}"; shift ;;
    --models)  MODELS="${2:?}"; shift ;;
    --prompt)  PROMPT="${2:?}"; shift ;;
    --engine)  ENGINE_URL="${2:?}"; shift ;;
    --watch)   WATCH=1 ;;
    --stack)   ENGINES_STACK="${2:?}"; shift ;;
    --key-param) KEY_PARAM="${2:?}"; shift ;;
    --wake)    WAKE=1 ;;
    --stop)    STOP=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
[ -n "$PROFILE" ] && [ -n "$REGION" ] || { usage; exit 2; }
AWS=(aws --profile "$PROFILE" --region "$REGION")

out() { "${AWS[@]}" cloudformation describe-stacks --stack-name "$1" \
  --query "Stacks[0].Outputs[?OutputKey=='$2'].OutputValue" --output text; }

CLUSTER="$(out "$PLATFORM_STACK" ClusterName)"
CPROLE="$(out "$PLATFORM_STACK" CpTaskRoleArn)"
FAMILY="$(out "$ENGINES_STACK" IngestTaskDefFamily)"
SERVICE="$(out "$ENGINES_STACK" LlmServiceName)"
SUBNETS="$(out "$NET_STACK" PrivateSubnets)"
CPSG="$(out "$NET_STACK" CpSgId)"
[ -n "$CPSG" ] && [ "$CPSG" != "None" ] || { echo "ERROR: no CpSgId output on $NET_STACK" >&2; exit 1; }

if [ "$WAKE" = 1 ]; then
  echo "==> waking $SERVICE (desired 1)"
  "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$SERVICE" --desired-count 1 \
    --query 'service.desiredCount' --output text >/dev/null
  # A GPU box, an image pull and every enabled GGUF out of S3: measured 527-586 s for one 18.5 GB
  # model, and a router syncs all of them (ADR 0072 decision 9).
  echo "==> waiting for a running task (this is ten minutes, not seconds)"
  n=0
  for _ in $(seq 1 150); do
    n="$("${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services "$SERVICE" \
      --query 'services[0].runningCount' --output text)"
    [ "$n" = "1" ] && break
    sleep 10
  done
  echo "==> runningCount=$n"
fi

# One python3 script, in the aws-cli image (python3, jq and bash are all in it — verified with
# `crane export`). urllib rather than curl so nothing depends on which client the base image
# happens to carry.
PROBE='set -e;
python3 - <<PY
import json, os, subprocess, threading, time, urllib.error, urllib.request

engine = os.environ["ENGINE"]
models = [m for m in os.environ["MODELS"].split(",") if m]
prompt = os.environ["PROMPT"]

key = subprocess.run(
    ["aws", "ssm", "get-parameter", "--name", os.environ["KEY_PARAM"], "--with-decryption",
     "--query", "Parameter.Value", "--output", "text"],
    capture_output=True, text=True).stdout.strip()
# Never printed and never put in the transcript: it is the second lock on the engine API
# (ADR 0071 decision 4d), and the transcript goes to CloudWatch.
print("probe: api key", "read" if key and key != "None" else "MISSING (the engine may refuse)")

def call(path, body=None, timeout=900):
    req = urllib.request.Request(engine + path,
        data=json.dumps(body).encode() if body is not None else None,
        headers={"Content-Type": "application/json",
                 **({"Authorization": "Bearer " + key} if key and key != "None" else {})})
    t0 = time.time()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, json.loads(r.read().decode()), round(time.time() - t0, 1)
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()[:400], round(time.time() - t0, 1)

def listing():
    st, doc, _ = call("/models", timeout=60)
    if st != 200 or not isinstance(doc, dict):
        return {"status": st, "body": doc}
    return {m["id"]: m.get("status", {}).get("value") for m in doc.get("data", [])}

# /health and /models in the same breath, and the pair is the point (ADR 0072 P1): a router
# answers /health with ok whatever it is holding, so warmth has to be read from the model list.
hst, hbody, _ = call("/health", timeout=30)
report = {"engine": engine, "health": {"status": hst, "body": hbody},
          "models_before": listing(), "calls": []}
print("probe: /health ->", hst, json.dumps(hbody)[:120])
print("probe: /models before =", json.dumps(report["models_before"]))

# Each model in turn, then back to the first: the swap is the measurement. With
# LlmModelsMax=1 the second call unloads the first model and loads the second, and the third
# call pays it again in the other direction.
for model in models + models[:1]:
    st, doc, secs = call("/v1/chat/completions", {
        "model": model, "messages": [{"role": "user", "content": prompt}], "max_tokens": 64})
    text, usage, answered = "", None, model
    if st == 200 and isinstance(doc, dict):
        text = doc["choices"][0]["message"]["content"][:200]
        usage, answered = doc.get("usage"), doc.get("model")
    else:
        text = str(doc)[:200]
    report["calls"].append({"asked": model, "answered_as": answered, "status": st,
                            "secs": secs, "usage": usage, "text": text})
    print("probe: %s -> %s in %ss: %s" % (model, st, secs, text.replace(chr(10), " ")[:120]))

# A model the catalogue does not hold. The gateway answers 404 model_unknown before a request
# ever gets here (ADR 0072 decision 7); the router own answer is 400, and the two numbers are
# deliberately different.
st, doc, _ = call("/v1/chat/completions", {
    "model": "not-in-this-catalogue", "messages": [{"role": "user", "content": "hi"}]}, timeout=60)
report["unknown_model"] = {"status": st, "body": str(doc)[:200]}
print("probe: unknown model ->", st, str(doc)[:160])

# The pair the warm rule turns on, observed on the real engine: while a model is loading the
# router already answers /health with ok, and only /models knows better. Off by default — it
# costs a full reload, and on a GPU box that is minutes and money.
if os.environ.get("WATCH") == "1" and len(models) > 1:
    print("probe: watch: loading the small model first, so the swap back is the long one")
    call("/v1/chat/completions", {"model": models[1],
        "messages": [{"role": "user", "content": "hi"}], "max_tokens": 4})
    done = []
    def swap():
        done.append(call("/v1/chat/completions", {"model": models[0],
            "messages": [{"role": "user", "content": prompt}], "max_tokens": 32}))
    th = threading.Thread(target=swap)
    th.start()
    samples, t0 = [], time.time()
    while th.is_alive() and time.time() - t0 < 600:
        hs, hb, _ = call("/health", timeout=30)
        samples.append({"at": round(time.time() - t0), "health": hs, "health_body": hb,
                        "models": listing()})
        print("probe: watch +%ss health=%s %s models=%s" % (
            samples[-1]["at"], hs, json.dumps(hb), json.dumps(samples[-1]["models"])))
        time.sleep(20)
    th.join()
    report["watch"] = {"samples": samples, "swap": {"status": done[0][0], "secs": done[0][2]}}
    print("probe: watch: the swap answered %s in %ss" % (done[0][0], done[0][2]))

report["models_after"] = listing()
print("probe: /models after =", json.dumps(report["models_after"]))
# ⚠️ The transcript goes to the LOG and nowhere else. This task wears the CONTROL PLANE task
# role — the only one holding ssm:GetParameter on /af-ws/* — and that role has no S3 permission
# at all: writing the transcript to the models bucket came back `AccessDenied on s3:PutObject`,
# which is ADR 0072 review R3 restated by AWS itself.
print("probe: transcript", json.dumps(report))
PY'

echo "==> run-task $FAMILY (llm probe)"
TASK="$("${AWS[@]}" ecs run-task --cluster "$CLUSTER" --launch-type FARGATE \
  --task-definition "$FAMILY" \
  --network-configuration "awsvpcConfiguration={subnets=[${SUBNETS//,/,}],securityGroups=[$CPSG],assignPublicIp=DISABLED}" \
  --overrides "$(python3 - "$PROBE" "$MODELS" "$PROMPT" "$KEY_PARAM" "$ENGINE_URL" "$WATCH" "$CPROLE" <<'PY'
import json, sys
probe, models, prompt, keyparam, engine, watch, role = sys.argv[1:8]
env = lambda **kw: [{"name": k, "value": v} for k, v in kw.items()]
print(json.dumps({
    # The CP's role, for one reason only: reading the engine's own API key out of SSM inside
    # the task instead of shipping it through the RunTask request.
    "taskRoleArn": role,
    "containerOverrides": [
        {"name": "fetch", "command": ["true"]},
        {"name": "upload", "command": [probe],
         "environment": env(MODELS=models, PROMPT=prompt, WATCH=watch,
                            KEY_PARAM=keyparam, ENGINE=engine)},
    ]}))
PY
)" --query 'tasks[0].taskArn' --output text)"
echo "==> task ${TASK##*/}"
"${AWS[@]}" ecs wait tasks-stopped --cluster "$CLUSTER" --tasks "$TASK" || true
"${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$TASK" \
  --query 'tasks[0].containers[].{name:name,exit:exitCode,reason:reason}' --output table

echo "==> probe log"
"${AWS[@]}" logs tail "/af/${ENGINES_STACK}/engines" --since 30m \
  --filter-pattern "probe" 2>/dev/null | tail -40 || true

if [ "$STOP" = 1 ]; then
  echo "==> stopping $SERVICE (desired 0) — a GPU box is \$1.26/hour"
  "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$SERVICE" --desired-count 0 \
    --query 'service.desiredCount' --output text >/dev/null
fi
