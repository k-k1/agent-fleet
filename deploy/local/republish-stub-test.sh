#!/usr/bin/env bash
# Stub run test for republish-dist.sh (docs/log/35 §35.7.6).
# Uses no real GitHub: a fake gh (prepended to PATH) serves the release body and
# records calls. Covers the two ways this script could destroy something that
# cannot be got back — eating the release notes while rewriting the notice, and
# attaching a native tar that points at a different rootfs than the one claimed.
# Runs in CI (release-gate.yml dist-gate) and locally (no docker / network).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
REPUBLISH="$ROOT/deploy/release/republish-dist.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STUB="$WORK/bin"; LOG="$WORK/calls.log"; BODY="$WORK/body.md"
mkdir -p "$STUB"

V=9.9.9
R=abc123def456
export ARCH=amd64
export REPO=example/dist

# fake gh: `release view --json body` prints $STUB_BODY, every call is logged,
# and `release view` of a tag in $STUB_MISSING fails (so the "must already exist"
# guards can be exercised).
cat > "$STUB/gh" <<'FAKE'
#!/usr/bin/env bash
echo "gh $*" >> "$STUB_LOG"
for a in "$@"; do
  case " $STUB_MISSING " in *" $a "*) exit 1 ;; esac
done
case "$2 $3" in
  "view --json"|"view -R") ;;
esac
if [ "$1 $2" = "release view" ]; then
  for a in "$@"; do
    [ "$a" = "body" ] && { cat "$STUB_BODY"; exit 0; }
    case "$a" in *assets*) echo "${STUB_ASSETS:-0}"; exit 0 ;; esac
  done
  exit 0
fi
exit 0
FAKE
chmod +x "$STUB/gh"
export PATH="$STUB:$PATH" STUB_LOG="$LOG" STUB_BODY="$BODY" STUB_MISSING="" STUB_ASSETS=0

# A release body shaped like the real ones: a withdrawal notice, then notes that
# themselves contain `---` separators (the reason the rewrite cannot just cut at
# the first one it sees).
cat > "$BODY" <<'BODYEOF'
> [!WARNING]
> **Withdrawn — the binaries for this release have been removed.**
> Use a later version.

---

Summary line of the real notes.

### New

- something

---

## 日本語

日本語の本文。

---

**Install (native)** — one-liner here
BODYEOF
cp "$BODY" "$WORK/body-orig.md"

# A dist dir holding the four assets, with a real (tiny) C tar whose rootfs.json
# points at the rootfs we claim to be reusing.
DIST="$WORK/dist"
C="agent-fleet-native-$V-linux-$ARCH"
mkdir -p "$DIST/$C"
cat > "$DIST/$C/rootfs.json" <<JSON
{
  "version": "$R",
  "url": "https://github.com/$REPO/releases/download/rootfs-$R/agent-fleet-rootfs-$R-linux-$ARCH.tar.zst",
  "sha256": "0000",
  "size": 1
}
JSON
tar czf "$DIST/$C.tar.gz" -C "$DIST" "$C"
rm -rf "$DIST/${C:?}"
echo x > "$DIST/agent-fleet-$V.tar.gz"
echo x > "$DIST/agent-fleet-images-$V.tar.gz"
echo x > "$DIST/SHA256SUMS"

fail() { echo "NG: $*" >&2; exit 1; }

# --- 1. happy path: uploads the four assets, rewrites the notice --------------
: > "$LOG"
VERSION=$V ROOTFS=$R \
  "$REPUBLISH" --dist-dir "$DIST" --repo "$REPO" --dry-run > "$WORK/out.log" 2>&1 \
  || { cat "$WORK/out.log"; fail "happy path failed"; }

grep -q "DRY: gh release upload v$V" "$WORK/out.log" || fail "no upload call"
for f in "agent-fleet-$V.tar.gz" "agent-fleet-images-$V.tar.gz" "$C.tar.gz" SHA256SUMS; do
  grep -q -- "$f" "$WORK/out.log" || fail "asset missing from upload: $f"
done
grep -q -- "--clobber" "$WORK/out.log" || fail "upload is not --clobber"

# --- 2. the rewritten body keeps every line of the notes ----------------------
NEW="$WORK/new.md"
# Re-run without --dry-run but with a gh that captures --notes-file content.
cat > "$STUB/gh" <<'FAKE'
#!/usr/bin/env bash
echo "gh $*" >> "$STUB_LOG"
for a in "$@"; do
  case " $STUB_MISSING " in *" $a "*) exit 1 ;; esac
done
prev=""
for a in "$@"; do
  if [ "$prev" = "--notes-file" ]; then cp "$a" "$STUB_CAPTURED"; prev=""; continue; fi
  [ "$a" = "--notes-file" ] && prev="--notes-file"
done
if [ "$1 $2" = "release view" ]; then
  for a in "$@"; do
    [ "$a" = "body" ] && { cat "$STUB_BODY"; exit 0; }
    case "$a" in *assets*) echo "${STUB_ASSETS:-0}"; exit 0 ;; esac
  done
fi
exit 0
FAKE
chmod +x "$STUB/gh"
export STUB_CAPTURED="$NEW"

VERSION=$V ROOTFS=$R \
  "$REPUBLISH" --dist-dir "$DIST" --repo "$REPO" > "$WORK/out2.log" 2>&1 \
  || { cat "$WORK/out2.log"; fail "second run failed"; }

[ -s "$NEW" ] || fail "release body was never rewritten"
grep -q '\[!NOTE\]' "$NEW" || fail "new notice missing"
grep -q 'Withdrawn' "$NEW" && fail "the old withdrawal notice survived"
# every non-empty line of the original notes (everything after the first ---)
# must still be present
awk 'seen { print; next } /^---$/ { seen = 1; getline }' "$WORK/body-orig.md" \
  | while IFS= read -r line; do
      [ -z "$line" ] && continue
      grep -qxF -- "$line" "$NEW" || { echo "NG: notes line lost: $line" >&2; exit 1; }
    done

# --- 3. idempotent: running again against the rewritten body is a no-op -------
cp "$NEW" "$BODY"
: > "$NEW"
VERSION=$V ROOTFS=$R \
  "$REPUBLISH" --dist-dir "$DIST" --repo "$REPO" > "$WORK/out3.log" 2>&1 \
  || { cat "$WORK/out3.log"; fail "re-run failed"; }
diff -u "$BODY" "$NEW" > /dev/null || fail "re-running changed the body (not idempotent)"

# --- 4. C pointing at a different rootfs must be refused ---------------------
cp "$BODY" "$WORK/body-keep.md"
mkdir -p "$DIST/$C"
sed 's/rootfs-'"$R"'/rootfs-deadbeef0000/g; s/"version": "'"$R"'"/"version": "deadbeef0000"/' \
  "$WORK/body-keep.md" > /dev/null
cat > "$DIST/$C/rootfs.json" <<JSON
{ "version": "deadbeef0000",
  "url": "https://github.com/$REPO/releases/download/rootfs-deadbeef0000/agent-fleet-rootfs-deadbeef0000-linux-$ARCH.tar.zst",
  "sha256": "0000", "size": 1 }
JSON
tar czf "$DIST/$C.tar.gz" -C "$DIST" "$C"
rm -rf "$DIST/${C:?}"
if VERSION=$V ROOTFS=$R \
    "$REPUBLISH" --dist-dir "$DIST" --repo "$REPO" --dry-run > "$WORK/out4.log" 2>&1; then
  fail "a C pointing at another rootfs was accepted"
fi
grep -q "does not match the target" "$WORK/out4.log" || fail "wrong error for rootfs mismatch"

# --- 5. a release that does not exist yet must be refused --------------------
export STUB_MISSING="v$V"
if VERSION=$V ROOTFS=$R \
    "$REPUBLISH" --dist-dir "$DIST" --repo "$REPO" --dry-run > "$WORK/out5.log" 2>&1; then
  fail "republish accepted a version that was never published"
fi
grep -q "only re-attaches assets" "$WORK/out5.log" || fail "wrong error for missing release"

# --- 6. a release that still holds its assets must be refused ----------------
export STUB_MISSING="" STUB_ASSETS=4
if VERSION=$V ROOTFS=$R \
    "$REPUBLISH" --dist-dir "$DIST" --repo "$REPO" --dry-run > "$WORK/out6.log" 2>&1; then
  fail "republish replaced the assets of a healthy release"
fi
grep -q "still has 4 asset" "$WORK/out6.log" || fail "wrong error for a populated release"

# --- 7. the notice stage refreshes the body of a release that HAS assets ------
# (the assets-must-be-gone guard applies to uploading, not to rewording)
export STUB_MISSING="" STUB_ASSETS=4
: > "$NEW"
AF_REPUBLISH_STAGE=notice VERSION=$V ROOTFS=$R \
  "$REPUBLISH" --dist-dir "$DIST" --repo "$REPO" > "$WORK/out7.log" 2>&1 \
  || { cat "$WORK/out7.log"; fail "notice stage failed on a populated release"; }
[ -s "$NEW" ] || fail "notice stage did not rewrite the body"
grep -q 'do not receive security updates' "$NEW" \
  || fail "the support policy is missing from the notice"
grep -q -- "--clobber" "$WORK/out7.log" && fail "notice stage must not upload assets"

echo "republish-stub-test: OK"
