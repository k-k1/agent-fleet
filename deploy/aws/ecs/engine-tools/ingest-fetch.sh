#!/bin/sh
# Ingest, step 1: fetch one file from its source and verify it (ADR 0072 decision 6).
#
# Runs as the non-essential `fetch` container of the ingest task definition; the `upload`
# container depends on it with `Condition: SUCCESS`, so a file that fails its checksum never
# reaches the bucket. `cfn/PARAMETERS-60-engines.md`, "The ingest containers".
#
# Environment (the contract, version CONTRACT):
#   URL           what to fetch                              (required unless MODE is set)
#   SHA256        the expected digest                        (optional, and warned about when absent)
#   MODE          "delete" or "move" skip this step entirely (optional)
#   CREDENTIAL_FOR "hf" or "civitai" when the Control Plane resolved the source from that
#                 service; empty for a plain url, which is fetched with no Authorization at all
#   HF_TOKEN      injected from Secrets Manager; "-" is the sentinel for "no token registered"
#   CIVITAI_TOKEN the same, for a Civitai account (engine_civitai_token.go). Only one of the two
#                 is ever sent, chosen by CREDENTIAL_FOR and only to that service's own exact hostnames: a foreign Bearer token offered to
#                 the other service is not "harmless" — a value it does not recognise is not
#                 guaranteed to be read as anonymous.

# --- the contract gate -----------------------------------------------------------------
#
# 🔴 This is the whole answer to "the script and the template no longer ship together".
# Since the scripts moved out of `60-engines.yaml` into this image, a stack update and an image
# copy are two acts, and `standup.sh` copies images one step BEFORE it deploys the stack. The
# failure that buys is silent: an old image runs the old script, the service reaches a steady
# state, and the engine does the previous release's thing for as long as nobody looks.
#
# So the template declares which contract it was written against and every entry point refuses
# a number it does not recognise -- loudly, in the task's own log, at the first second rather
# than at the first surprise. What the number covers is the INTERFACE, not the code: the
# environment variables below, the SSM keys they name and the files written under $MODELS_DIR.
# Bump it in deploy/aws/ecs/engine-tools/CONTRACT and in 60-engines.yaml together; changing the
# script's behaviour without changing that interface does not need a bump.
#
# The number is read relative to THIS FILE so the same script works from the image (/opt/af)
# and straight out of the repository, which is what deploy/local/engine-sidecar-test.sh runs.
af_check_contract() {
  _want="$(cat "$(dirname "$0")/CONTRACT" 2>/dev/null || echo '?')"
  if [ "${ENGINE_TOOLS_CONTRACT:-}" = "$_want" ]; then return 0; fi
  echo "engine-tools: CONTRACT MISMATCH. This image speaks '$_want'; the task definition asked for '${ENGINE_TOOLS_CONTRACT:-<unset>}'." >&2
  echo "engine-tools: 60-engines.yaml and EngineToolsImageTag are out of step - nothing has run." >&2
  echo "engine-tools: PARAMETERS-60-engines.md, \"The engine tools image\"." >&2
  exit 78 # EX_CONFIG: a configuration error, not a failure of the work
}
af_check_contract

set -e
[ "$MODE" = delete ] && { echo "ingest: delete mode, nothing to fetch"; exit 0; }
# A move is bytes this deployment already owns changing key. S3 copies them server-side, so
# nothing is downloaded here and the ephemeral disk never has to hold a second copy of a 13 GB
# file — which is what makes repairing a misplaced key possible at all.
[ "$MODE" = move ] && { echo "ingest: move mode, nothing to fetch"; exit 0; }
[ -n "$URL" ] || { echo "ingest: URL is required"; exit 2; }
start=$(date +%s)
# Overridable only so deploy/local/ingest-fetch-test.sh can run without the task's /scratch.
SCRATCH="${AF_FETCH_SCRATCH:-/scratch}"

# --- which hosts may receive a token ------------------------------------------------------
#
# A token is sent only when the Control Plane marked this fetch as coming from that service
# (CREDENTIAL_FOR=hf|civitai, set by the hf / civitai resolvers and never for a plain url) AND
# the hop's hostname is on that service's list, compared exactly. The list of hosts we may
# CREDENTIAL is deliberately separate from the hosts we may DOWNLOAD from: a redirect may land on
# any https host (a CDN or object store whose name this script cannot know), and gets no token.
# Matching the whole URL with a `case` glob is what this replaces: `*` also matches `/`, so a
# path that merely contained `huggingface.co/` on another host matched.
af_cred_hosts() { # af_cred_hosts <service> -> exact hostnames, one per line
  case "$1" in
    hf) echo huggingface.co ;;
    civitai) printf '%s\n' civitai.com www.civitai.com ;;
  esac
}

# af_parse_url <url> sets AF_HOST and AF_PATH, or returns 1: https only, no userinfo, no port
# (default 443), a plain DNS-style hostname.
af_parse_url() {
  case "$1" in
    https://*|HTTPS://*) ;;
    *) return 1 ;;
  esac
  _rest="${1#*://}"
  _auth="${_rest%%[/?#]*}"
  AF_PATH="${_rest#"$_auth"}"
  case "$_auth" in
    ""|*[!A-Za-z0-9.-]*) return 1 ;; # rejects '@' (userinfo), ':' (port, IPv6), '[' and the like
  esac
  AF_HOST=$(printf '%s' "$_auth" | tr 'A-Z' 'a-z')
}

# af_token_for <service> <host> prints the token when both the service is known and the host is
# one of its credential hosts; prints nothing otherwise.
af_token_for() {
  case "$1" in
    hf) _tok="$HF_TOKEN" ;;
    civitai) _tok="$CIVITAI_TOKEN" ;;
    *) return 0 ;;
  esac
  [ -n "$_tok" ] && [ "$_tok" != - ] || return 0
  for _h in $(af_cred_hosts "$1"); do
    if [ "$_h" = "$2" ]; then printf '%s' "$_tok"; return 0; fi
  done
}

# Redirects are followed here, not by `curl -L`: the host is re-judged at every hop and the
# Authorization header is built per hop, so it can never outlive a change of host.
# `--location-trusted` must never be used.
af_parse_url "$URL" || { echo "ingest: refusing url: it must be https with a plain hostname, no user info and no port"; exit 2; }
cur="$URL"
hops=0
while :; do
  af_parse_url "$cur" || { echo "ingest: refusing redirect target: not an https url with a plain hostname"; exit 1; }
  tok=$(af_token_for "${CREDENTIAL_FOR:-}" "$AF_HOST")
  set --
  [ -z "$tok" ] || set -- -H "Authorization: Bearer $tok"
  code=$(curl -sS --proto '=https' "$@" -D "$SCRATCH/hdr" -o "$SCRATCH/blob" -w '%{http_code}' "$cur") \
    || { echo "ingest: fetch failed"; exit 1; }
  case "$code" in
    2??) break ;;
    301|302|303|307|308)
      hops=$((hops + 1))
      [ "$hops" -le 10 ] || { echo "ingest: too many redirects"; exit 1; }
      loc=$(grep -i '^location:' "$SCRATCH/hdr" | tail -n 1 | sed 's/^[^:]*:[[:space:]]*//' | tr -d '\r')
      [ -n "$loc" ] || { echo "ingest: redirect without a Location"; exit 1; }
      case "$loc" in
        /*) cur="https://$AF_HOST$loc" ;;
        *) cur="$loc" ;;
      esac
      ;;
    *) echo "ingest: HTTP $code"; exit 1 ;;
  esac
done
size=$(stat -c %s "$SCRATCH/blob")
secs=$(( $(date +%s) - start ))
echo "ingest: fetched $size bytes in ${secs}s"
if [ -n "$SHA256" ]; then
  got=$(sha256sum "$SCRATCH/blob" | cut -d" " -f1)
  [ "$got" = "$SHA256" ] || { echo "ingest: sha256 mismatch: got $got want $SHA256"; exit 1; }
  echo "ingest: sha256 ok $got"
else
  echo "ingest: WARNING no SHA256 given - the upload is unverified"
fi
