#!/usr/bin/env bash
# set-kiro-ci-auth.sh against a fixture Kiro DB and a stub gh that keeps secrets as files.
#
#   deploy/local/set-kiro-ci-auth-stub-test.sh
#
# The script runs by hand on an operator's machine, where no CI reaches, and a bad upload
# only shows as a red kiro contract. Pinned here: the reassembled secrets decode to the
# credential without the conversation and shell history (case 1), stale trailing fragments
# are deleted (case 1), a failing delete stops the script (case 2), and a DB that is not
# Kiro's is refused before anything is written (case 3).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/set-kiro-ci-auth.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail() {
  echo "NG: $CASE: $1"
  for f in out err; do
    [ -s "$WORK/$f" ] || continue
    echo "--- $f ---"; cat "$WORK/$f"
  done
  exit 1
}

mkdir -p "$WORK/bin"
cat > "$WORK/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
store="$STUB_SECRETS"
case "$1 ${2:-}" in
  "auth status") exit 0 ;;
  "secret list") ls "$store" ;;
  "secret set") cat > "$store/$3" ;;
  "secret delete")
    [ "$#" -eq 3 ] || { echo "unknown flag: $4" >&2; exit 1; }
    [ -z "${STUB_DELETE_FAILS:-}" ] || { echo "HTTP 403" >&2; exit 1; }
    rm "$store/$3" ;;
  *) echo "stub gh: unexpected: $*" >&2; exit 1 ;;
esac
EOF
chmod +x "$WORK/bin/gh"

# fixture <path> <with-auth>: a DB shaped like kiro-cli's, with ~400 KB of conversation.
fixture() {
  python3 -I - "$1" "$2" <<'PY'
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
if sys.argv[2] == "1":
    c.execute("CREATE TABLE auth_kv (key TEXT PRIMARY KEY, value TEXT)")
    c.execute("INSERT INTO auth_kv VALUES ('token', 'fixture-credential')")
c.execute("CREATE TABLE state (key TEXT PRIMARY KEY, value TEXT)")
c.execute("INSERT INTO state VALUES ('setting', 'kept')")
c.execute("CREATE TABLE conversations_v2 (key TEXT, value TEXT)")
c.execute("CREATE TABLE history (command TEXT)")
for i in range(400):
    c.execute("INSERT INTO conversations_v2 VALUES (?, ?)", (str(i), "private chat " * 80))
    c.execute("INSERT INTO history VALUES ('private command')")
c.commit()
PY
}

run() {
  env PATH="$WORK/bin:$PATH" STUB_SECRETS="$WORK/secrets" KIRO_DB="$WORK/data.sqlite3" "$@" \
    bash "$SCRIPT" > "$WORK/out" 2> "$WORK/err"
}

reset_secrets() {
  rm -rf "$WORK/secrets" "$WORK/data.sqlite3"
  mkdir -p "$WORK/secrets"
  for i in 1 2 3 4 5 6 7 8; do echo stale > "$WORK/secrets/E2E_KIRO_AUTH_DB_B64_$i"; done
}

CASE="1 upload"
reset_secrets
fixture "$WORK/data.sqlite3" 1
[ "$(stat -c %s "$WORK/data.sqlite3")" -gt 360000 ] || fail "fixture too small to need more than eight fragments"
run || fail "exit $?"
n="$(find "$WORK/secrets" -type f | wc -l)"
[ "$n" -ge 1 ] && [ "$n" -lt 8 ] || fail "expected fewer than eight fragments, got $n"
for i in $(seq "$((n + 1))" 8); do
  [ ! -e "$WORK/secrets/E2E_KIRO_AUTH_DB_B64_$i" ] || fail "stale fragment $i left behind"
done
for i in $(seq 1 8); do
  [ ! -e "$WORK/secrets/E2E_KIRO_AUTH_DB_B64_$i" ] || cat "$WORK/secrets/E2E_KIRO_AUTH_DB_B64_$i"
done | base64 --decode > "$WORK/restored.sqlite3" || fail "fragments do not decode"
got="$(python3 -I - "$WORK/restored.sqlite3" <<'PY'
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
print(c.execute("SELECT value FROM auth_kv").fetchone()[0],
      c.execute("SELECT value FROM state").fetchone()[0],
      c.execute("SELECT count(*) FROM conversations_v2").fetchone()[0],
      c.execute("SELECT count(*) FROM history").fetchone()[0])
PY
)" || fail "restored DB does not open"
[ "$got" = "fixture-credential kept 0 0" ] || fail "restored DB: $got"
echo "OK: $CASE"

CASE="2 delete fails"
reset_secrets
fixture "$WORK/data.sqlite3" 1
if run STUB_DELETE_FAILS=1; then fail "exit 0 although a stale fragment could not be deleted"; fi
grep -q "HTTP 403" "$WORK/err" || fail "the delete error was not shown"
echo "OK: $CASE"

CASE="3 not a Kiro DB"
reset_secrets
fixture "$WORK/data.sqlite3" 0
if run; then fail "exit 0 for a DB without auth_kv"; fi
grep -q "no auth_kv table" "$WORK/err" || fail "no reason given"
[ "$(cat "$WORK/secrets/E2E_KIRO_AUTH_DB_B64_1")" = stale ] || fail "a secret was written"
echo "OK: $CASE"
