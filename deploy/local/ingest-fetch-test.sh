#!/bin/sh
# Runs deploy/aws/ecs/engine-tools/ingest-fetch.sh against a stub `curl` and asserts where the
# deployment-wide Hugging Face / Civitai token goes: only to the service's exact hostnames, only
# when the Control Plane marked the fetch (CREDENTIAL_FOR), and never across a redirect to
# another host. The stub records "<url> auth=<yes|no>" per request and answers from a redirect
# table, so no network is involved.
set -u
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TOOLS="${AF_TEST_TOOLS:-$ROOT/deploy/aws/ecs/engine-tools}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

mkdir -p "$WORK/bin" "$WORK/scratch"
cat > "$WORK/bin/curl" <<'STUB'
#!/bin/sh
# usage seen: curl -sS --proto =https [-H "Authorization: ..."] -D HDR -o OUT -w CODE URL
auth=no; hdr=; out=; url=
while [ $# -gt 0 ]; do
  case "$1" in
    -H) case "$2" in Authorization:*) auth=yes ;; esac; shift 2 ;;
    -D) hdr="$2"; shift 2 ;;
    -o) out="$2"; shift 2 ;;
    -w|--proto) shift 2 ;;
    -L|--location|--location-trusted) echo "stub curl: -L must not be used" >&2; exit 99 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
echo "$url auth=$auth" >> "$AF_TEST_LOG"
# $AF_TEST_REDIRECTS: lines "<from-url> <location>"
loc=$(awk -v u="$url" '$1==u {print $2}' "$AF_TEST_REDIRECTS")
: > "$hdr"
if [ -n "$loc" ]; then
  printf 'HTTP/2 302\r\nLocation: %s\r\n\r\n' "$loc" > "$hdr"
  : > "$out"; printf 302
else
  printf 'weights' > "$out"; printf 200
fi
STUB
chmod +x "$WORK/bin/curl"
export PATH="$WORK/bin:$PATH"
export AF_FETCH_SCRATCH="$WORK/scratch" AF_TEST_LOG="$WORK/log" AF_TEST_REDIRECTS="$WORK/redirects"
export ENGINE_TOOLS_CONTRACT="$(cat "$TOOLS/CONTRACT")"
export HF_TOKEN=hf-secret CIVITAI_TOKEN=cv-secret

# run <credential-for> <url> -> sets RC; $WORK/log holds one line per request
run() {
  : > "$WORK/log"
  CREDENTIAL_FOR="$1" URL="$2" SHA256= sh "$TOOLS/ingest-fetch.sh" > "$WORK/out" 2>&1
  RC=$?
}
# expect_auth <label> <credential-for> <url> <yes|no> : the first request carries (or not) a token
expect_auth() {
  run "$2" "$3"
  [ "$RC" = 0 ] || fail "$1: exited $RC: $(cat "$WORK/out")"
  grep -q " auth=$4\$" "$WORK/log" || fail "$1: wanted auth=$4, got: $(cat "$WORK/log")"
  if [ "$4" = no ] && grep -q "auth=yes" "$WORK/log"; then fail "$1: a token was sent: $(cat "$WORK/log")"; fi
}
# expect_refused <label> <credential-for> <url> : refused before any request is made
expect_refused() {
  run "$2" "$3"
  [ "$RC" != 0 ] || fail "$1: accepted"
  [ ! -s "$WORK/log" ] || fail "$1: a request was made: $(cat "$WORK/log")"
}

: > "$WORK/redirects"
echo "== the token goes to the service's own host when the fetch is marked =="
expect_auth "hf exact host" hf https://huggingface.co/a/b/resolve/main/m.gguf yes
expect_auth "civitai exact host" civitai https://civitai.com/api/download/models/1 yes
grep -q "auth=yes" "$WORK/log" || fail "civitai: no token sent"

echo "== a plain url never gets a token, even on a service host =="
expect_auth "plain url on hf host" "" https://huggingface.co/a/b/resolve/main/m.gguf no
expect_auth "plain url on civitai host" "" https://civitai.com/api/download/models/1 no

echo "== lookalike hosts: no token, or refused =="
expect_auth "path contains the host" hf https://evil.example/huggingface.co/x no
expect_auth "service name as a subdomain of another host" hf https://huggingface.co.evil.example/x no
expect_auth "other-service host under hf mark" hf https://civitai.com/x no
expect_auth "subdomain is not a credential host" hf https://cdn-lfs.huggingface.co/x no
expect_auth "civitai path trick" civitai https://evil.example/x/civitai.com/ no
expect_refused "userinfo" hf "https://huggingface.co@evil.example/x"
expect_refused "userinfo with password" hf "https://huggingface.co:pw@evil.example/x"
expect_refused "non-default port" hf "https://huggingface.co:8443/x"
expect_refused "plain http" hf "http://huggingface.co/x"
expect_refused "ipv6 literal" hf "https://[::1]/x"

echo "== a redirect to another host drops the token, back to the service restores it =="
cat > "$WORK/redirects" <<'R'
https://huggingface.co/a/m.gguf https://cdn.example/blob?sig=1
R
run hf https://huggingface.co/a/m.gguf
[ "$RC" = 0 ] || fail "redirect: exited $RC: $(cat "$WORK/out")"
[ "$(cat "$WORK/log")" = "https://huggingface.co/a/m.gguf auth=yes
https://cdn.example/blob?sig=1 auth=no" ] || fail "redirect: $(cat "$WORK/log")"

echo "== a relative redirect stays on the host and keeps the token =="
cat > "$WORK/redirects" <<'R'
https://huggingface.co/a/m.gguf /b/m.gguf
R
run hf https://huggingface.co/a/m.gguf
[ "$(cat "$WORK/log")" = "https://huggingface.co/a/m.gguf auth=yes
https://huggingface.co/b/m.gguf auth=yes" ] || fail "relative redirect: $(cat "$WORK/log")"

echo "== a redirect off the service to a lookalike or to http gets no token / is refused =="
cat > "$WORK/redirects" <<'R'
https://huggingface.co/a/m.gguf https://huggingface.co.evil.example/m
R
run hf https://huggingface.co/a/m.gguf
grep -q "evil.example/m auth=no" "$WORK/log" || fail "lookalike redirect: $(cat "$WORK/log")"
cat > "$WORK/redirects" <<'R'
https://huggingface.co/a/m.gguf http://huggingface.co/m
R
run hf https://huggingface.co/a/m.gguf
[ "$RC" != 0 ] || fail "http redirect accepted"
[ "$(wc -l < "$WORK/log")" = 1 ] || fail "http redirect was followed: $(cat "$WORK/log")"
cat > "$WORK/redirects" <<'R'
https://huggingface.co/a/m.gguf https://huggingface.co@evil.example/m
R
run hf https://huggingface.co/a/m.gguf
[ "$RC" != 0 ] || fail "userinfo redirect accepted"
[ "$(wc -l < "$WORK/log")" = 1 ] || fail "userinfo redirect was followed: $(cat "$WORK/log")"

echo "ingest-fetch token routing: ok"
