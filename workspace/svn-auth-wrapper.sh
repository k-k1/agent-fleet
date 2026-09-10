#!/usr/bin/env bash
# Subversion (svn) transparent-auth wrapper — the SVN counterpart of gh-auth-wrapper.sh.
#
# svn speaks no credential-helper protocol, and the only place it looks for a password by
# itself is ~/.subversion/auth, which we refuse to write (plaintext at rest, ADR 0024). So
# the credential saved through the Console reached the Console's own update button and
# nothing else: `svn update` typed in a session failed on a working copy the Console
# could update, which reads as the workspace being broken.
#
# This shim sits ahead of the real binary (/usr/bin/svn) on PATH and hands the call to
# `workspace-agent svn-run`, which resolves the credential from the ENCRYPTED store and
# passes it to the real svn on stdin (never in argv, so it stays out of `ps`). The
# decisions live in Go (workspace/agent/svn_wrapper.go) because they are argv analysis
# with unit tests, not shell.
#
# Everything here is a fallback: if the agent binary is missing, or we are already inside
# a wrapped call, the real svn runs exactly as it would have.
# A fixed list, never a PATH search: this script IS `svn` on PATH, so searching for the
# next one is how a wrapper execs itself forever.
real=""
for c in /usr/bin/svn /bin/svn; do [ -x "$c" ] && { real="$c"; break; }; done
[ -n "$real" ] || { echo "svn: not found" >&2; exit 127; }
if [ -n "${AF_SVN_WRAPPED:-}" ] || ! command -v workspace-agent >/dev/null 2>&1; then
  exec "$real" "$@"
fi
export AF_SVN_REAL="$real"
exec workspace-agent svn-run "$@"
