#!/usr/bin/env bash
# vuln-scan-report.py (scanner JSON -> verdict + issue body) and vuln-scan-issue.sh
# (open / update / close ONE issue) against fixtures and a fake `gh`.
#
#   deploy/local/vuln-scan-report-test.sh
#
# The scan runs weekly against live registries, so the first feedback on a wrong verdict
# would be a missing alert or a closed issue that should have stayed open. The cases that
# matter: an unreadable scanner file is an error, never "clean" (case 5-6), and a re-scan
# with the same findings changes nothing on the issue (case 9).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FX="$HERE/testdata/vuln-scan"
REPORT="$HERE/vuln-scan-report.py"
ISSUE="$HERE/vuln-scan-issue.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail() { echo "NG: $1"; [ -f "$WORK/out" ] && { echo "--- out ---"; cat "$WORK/out"; }; [ -f "$WORK/err" ] && { echo "--- err ---"; cat "$WORK/err"; }; exit 1; }
report() { rc=0; python3 -I "$REPORT" --release 9.9.9 --out "$WORK/body.md" "$@" > "$WORK/out" 2> "$WORK/err" || rc=$?; }
has() { grep -qF -- "$1" "$WORK/body.md" || fail "body lacks: $1"; }
hasnt() { ! grep -qF -- "$1" "$WORK/body.md" || fail "body must not contain: $1"; }

# 1. Trivy: only HIGH/CRITICAL with a fixed version; OS and own-Go stdlib rebuild, a baked tool's bumps.
report --trivy "image=$FX/trivy-findings.json"
[ "$rc" = 0 ] || fail "1: rc=$rc"
[ "$(cat "$WORK/out")" = "fixable=4" ] || fail "1: expected fixable=4"
has "CVE-2099-0001"; has "CVE-2099-0002"
hasnt "CVE-2099-0003"   # no fix available
hasnt "CVE-2099-0004"   # MEDIUM
grep -F "CVE-2099-0005" "$WORK/body.md" | grep -qF "| rebuild |" || fail "1: own-binary stdlib should be rebuild"
grep -F "CVE-2099-0006" "$WORK/body.md" | grep -qF "| bump |" || fail "1: third-party tool stdlib should be bump"
has "**3** can be cured by rebuilding"

# 2. Table cells cannot be broken by scanner text (the title is not rendered at all).
hasnt "a | b"

# 3. govulncheck: fixed findings only, de-duplicated, CVE alias shown, stdlib = rebuild.
report --govulncheck "bin=$FX/govuln-findings.json"
[ "$rc" = 0 ] && [ "$(cat "$WORK/out")" = "fixable=2" ] || fail "3: expected fixable=2"
has "GO-2099-0001 (CVE-2099-1001)"; hasnt "GO-2099-0003"
grep -F "GO-2099-0002" "$WORK/body.md" | grep -qF "| rebuild |" || fail "3: stdlib should be rebuild"

# 4. Clean inputs: fixable=0, and the body says so.
report --trivy "image=$FX/trivy-clean.json" --govulncheck "bin=$FX/govuln-clean.json"
[ "$rc" = 0 ] && [ "$(cat "$WORK/out")" = "fixable=0" ] || fail "4: expected fixable=0"
has "No fixable HIGH/CRITICAL findings."

# 5. A missing file, an empty file and a file that is not the scanner's output are errors.
report --trivy "image=$WORK/nope.json";                          [ "$rc" = 2 ] || fail "5a: missing file, rc=$rc"
: > "$WORK/empty.json"
report --trivy "image=$WORK/empty.json";                         [ "$rc" = 2 ] || fail "5b: empty file, rc=$rc"
echo '{"Results":[]}' > "$WORK/noschema.json"
report --trivy "image=$WORK/noschema.json";                      [ "$rc" = 2 ] || fail "5c: no SchemaVersion, rc=$rc"
echo 'curl: (22) 404' > "$WORK/junk.json"
report --govulncheck "bin=$WORK/junk.json";                      [ "$rc" = 2 ] || fail "5d: junk, rc=$rc"

# 6. One bad input among good ones still fails the whole report (no partial "clean").
report --trivy "image=$FX/trivy-clean.json" --govulncheck "bin=$WORK/empty.json"
[ "$rc" = 2 ] || fail "6: rc=$rc"

# 6b. A scan that inspected nothing is not clean: Trivy without an OS / os-pkgs result,
# govulncheck with only config/progress or a non-binary config.
echo '{"SchemaVersion":2,"ArtifactName":"x"}' > "$WORK/noos.json"
report --trivy "image=$WORK/noos.json";                          [ "$rc" = 2 ] || fail "6b: Trivy without OS, rc=$rc"
echo '{"SchemaVersion":2,"Metadata":{"OS":{"Family":"debian"}},"Results":[{"Class":"lang-pkgs","Target":"t"}]}' > "$WORK/noospkgs.json"
report --trivy "image=$WORK/noospkgs.json";                      [ "$rc" = 2 ] || fail "6c: Trivy without os-pkgs, rc=$rc"
head -2 "$FX/govuln-clean.json" > "$WORK/cfgonly.json"
report --govulncheck "bin=$WORK/cfgonly.json";                   [ "$rc" = 2 ] || fail "6d: config-only stream, rc=$rc"
sed 's/"scan_mode":"binary"/"scan_mode":"source"/' "$FX/govuln-clean.json" > "$WORK/src.json"
report --govulncheck "bin=$WORK/src.json";                       [ "$rc" = 2 ] || fail "6e: source-mode output, rc=$rc"

# 6f. The body stays under GitHub's 65536-character limit with 5 targets of long values,
# and the count is the full count, not the shown one.
python3 -I - "$WORK" <<'P'
import json, sys
w = sys.argv[1]
vulns = [{"VulnerabilityID": "CVE-2099-%05d" % i, "PkgName": "p" * 130, "InstalledVersion": "v" * 130,
          "FixedVersion": ", ".join("1.%d.%d" % (i, j) for j in range(15)), "Severity": "HIGH"} for i in range(60)]
doc = {"SchemaVersion": 2, "Metadata": {"OS": {"Family": "debian"}},
       "Results": [{"Target": "t" * 130, "Class": "lang-pkgs", "Vulnerabilities": vulns},
                   {"Target": "debian", "Class": "os-pkgs", "Vulnerabilities": vulns}]}
json.dump(doc, open(w + "/big.json", "w"))
P
report --trivy "a=$WORK/big.json" --trivy "b=$WORK/big.json" --trivy "c=$WORK/big.json" --trivy "d=$WORK/big.json" --trivy "e=$WORK/big.json"
[ "$rc" = 0 ] && [ "$(cat "$WORK/out")" = "fixable=600" ] || fail "6f: expected fixable=600, rc=$rc"
[ "$(wc -m < "$WORK/body.md")" -lt 60000 ] || fail "6f: body is $(wc -m < "$WORK/body.md") chars"
has "**600** fixable"; has "more (see the workflow run"

# 7. The body is deterministic: same input, same bytes.
report --trivy "image=$FX/trivy-findings.json"; cp "$WORK/body.md" "$WORK/body1.md"
report --trivy "image=$FX/trivy-findings.json"
cmp -s "$WORK/body.md" "$WORK/body1.md" || fail "7: body differs between runs"

# 8-11. Issue sync against a fake gh backed by files: $FAKE/issue (body; present = open issue),
# $FAKE/log (one line per mutating call).
FAKE="$WORK/fake"; mkdir -p "$FAKE/bin"
cat > "$FAKE/bin/gh" <<'G'
#!/usr/bin/env bash
F="${FAKE:?}"
case "$1 $2" in
  "issue list")    if [ -f "$F/issue" ]; then echo 7; fi ;;
  "issue view")    cat "$F/issue" ;;
  "issue create")  shift 2; while [ $# -gt 0 ]; do [ "$1" = --body-file ] && cp "$2" "$F/issue"; shift; done; echo "create" >> "$F/log" ;;
  "issue edit")    shift 2; while [ $# -gt 0 ]; do [ "$1" = --body-file ] && cp "$2" "$F/issue"; shift; done; echo "edit" >> "$F/log" ;;
  "issue comment") echo "comment" >> "$F/log" ;;
  "issue close")   rm -f "$F/issue"; echo "close" >> "$F/log" ;;
  "label create")  echo "label" >> "$F/log" ;;
  *) echo "unexpected gh $*" >&2; exit 99 ;;
esac
G
chmod +x "$FAKE/bin/gh"
export FAKE
PATH="$FAKE/bin:$PATH"
sync() { : > "$FAKE/log"; "$ISSUE" sync "$@" > "$WORK/out" 2> "$WORK/err" || fail "sync $* failed"; }
log() { tr '\n' ' ' < "$FAKE/log"; }

report --trivy "image=$FX/trivy-findings.json"; cp "$WORK/body.md" "$WORK/with.md"
report --trivy "image=$FX/trivy-clean.json";    cp "$WORK/body.md" "$WORK/clean.md"

sync "$WORK/with.md" 4 http://run/1 9.9.9 9.9.9
[ "$(log)" = "label create " ] || fail "8: findings, no issue -> label+create, got '$(log)'"
sync "$WORK/with.md" 4 http://run/2 9.9.9 9.9.9
[ "$(log)" = "" ] || fail "9: same findings -> nothing, got '$(log)'"
printf '%s\r\n' "$(cat "$WORK/with.md")" > "$FAKE/issue"   # GitHub may hand the body back with CRLF
sync "$WORK/with.md" 4 http://run/2 9.9.9 9.9.9
[ "$(log)" = "" ] || fail "9b: CRLF body must not look changed, got '$(log)'"
echo "stale" > "$FAKE/issue"
sync "$WORK/with.md" 4 http://run/3 9.9.9 9.9.9
[ "$(log)" = "edit comment " ] || fail "10: changed findings -> edit+comment, got '$(log)'"
sync "$WORK/clean.md" 0 http://run/4 9.9.9 9.9.9
[ "$(log)" = "comment close " ] || fail "11: clean with open issue -> comment+close, got '$(log)'"
sync "$WORK/clean.md" 0 http://run/5 9.9.9 9.9.9
[ "$(log)" = "" ] || fail "12: clean, no issue -> nothing, got '$(log)'"

# 12b. An older version scanned while a newer one is latest must not touch the issue, whether
# it is clean (would close it) or has other findings (would overwrite it).
echo "latest-vulnerable" > "$FAKE/issue"
sync "$WORK/clean.md" 0 http://run/7 9.9.8 9.9.9
[ "$(log)" = "" ] && [ "$(cat "$FAKE/issue")" = "latest-vulnerable" ] || fail "12b: older clean scan touched the issue, got '$(log)'"
sync "$WORK/with.md" 4 http://run/8 9.9.8 9.9.9
[ "$(log)" = "" ] && [ "$(cat "$FAKE/issue")" = "latest-vulnerable" ] || fail "12c: older vulnerable scan touched the issue, got '$(log)'"
rm -f "$FAKE/issue"

# 13. Bad arguments fail instead of guessing.
rc=0; "$ISSUE" sync "$WORK/with.md" many http://run/6 9.9.9 9.9.9 > "$WORK/out" 2> "$WORK/err" || rc=$?
[ "$rc" = 2 ] || fail "13: non-numeric count, rc=$rc"

# 14. The rootfs is extracted as data: escapes are refused, plain trees and absolute symlinks are kept.
EXTRACT="$HERE/vuln-scan-extract.py"
python3 -I - "$WORK" <<'P'
import io, sys, tarfile
w = sys.argv[1]
def mk(name, members):
    with tarfile.open(f"{w}/{name}.tar", "w") as t:
        for m in members:
            t.addfile(*m)
def f(path, data=b"x"):
    i = tarfile.TarInfo(path); i.size = len(data); i.mode = 0o4755; return (i, io.BytesIO(data))
def l(path, target):
    i = tarfile.TarInfo(path); i.type = tarfile.SYMTYPE; i.linkname = target; return (i,)
mk("good", [f("usr/bin/tool"), l("etc/abs", "/usr/bin/tool")])
mk("dotdot", [f("../escape")])
mk("viasym", [l("lnk", "/tmp"), f("lnk/pwned")])
P
rc=0; python3 -I "$EXTRACT" "$WORK/x-good" < "$WORK/good.tar" > "$WORK/out" 2> "$WORK/err" || rc=$?
[ "$rc" = 0 ] && [ -f "$WORK/x-good/usr/bin/tool" ] && [ -L "$WORK/x-good/etc/abs" ] || fail "14a: good tar, rc=$rc"
[ ! -u "$WORK/x-good/usr/bin/tool" ] || fail "14a: setuid bit kept"
rc=0; python3 -I "$EXTRACT" "$WORK/x-dd" < "$WORK/dotdot.tar" > "$WORK/out" 2> "$WORK/err" || rc=$?
[ "$rc" = 1 ] && [ ! -e "$WORK/escape" ] || fail "14b: .. member, rc=$rc"
rc=0; python3 -I "$EXTRACT" "$WORK/x-vs" < "$WORK/viasym.tar" > "$WORK/out" 2> "$WORK/err" || rc=$?
[ "$rc" = 1 ] && [ ! -e /tmp/pwned ] || fail "14c: write through a symlink, rc=$rc"

echo "OK: vuln-scan report + issue sync (all cases)"
