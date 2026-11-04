#!/usr/bin/env python3
"""Turn Trivy / govulncheck JSON into the vulnerability-tracking issue body.

    vuln-scan-report.py --release 0.40.0 --out body.md \
        --trivy 'control-plane image=cp.json' --govulncheck 'af-cp=cp-bin.json' ...

Prints `fixable=<n>` on stdout (the workflow appends it to $GITHUB_OUTPUT). The body goes
to --out and is a pure function of the inputs: no timestamp, no run URL, so a re-scan that
finds the same things produces the same bytes and the issue is not rewritten.

What counts as a finding ("fixable HIGH/CRITICAL"):
  - Trivy: Severity is HIGH or CRITICAL and FixedVersion is non-empty. The scan also
    passes --ignore-unfixed; the filter is repeated here so the verdict does not depend
    on a flag.
  - govulncheck -mode=binary: every finding with a fixed_version. The Go vulnerability
    database carries no severity, so there is nothing to grade; binary mode only reports
    vulnerable symbols present in the binary.

Exit status: 0 = report written (zero or more findings), 2 = an input is missing, empty or
not the scanner's output. An input that cannot be read is never "clean": a scanner that
failed to run must not close the tracking issue.
"""
import argparse
import json
import sys

MARKER = "<!-- af-image-vuln-scan -->"
SEVERITIES = ("CRITICAL", "HIGH")
# Our own Go binaries: their standard library comes from the build's Go toolchain.
OWN_BINARIES = ("af-cp", "workspace-agent")
# An issue body is limited to 65536 characters; stay well under it.
MAX_ROWS_PER_TARGET = 40
DOC_URL = "https://github.com/k-k1/agent-fleet/blob/main/docs/build/94-patch-release.md"


class InputError(Exception):
    pass


def load_json(path):
    try:
        with open(path, encoding="utf-8") as f:
            text = f.read()
    except OSError as e:
        raise InputError(f"{path}: {e.strerror}")
    if not text.strip():
        raise InputError(f"{path}: empty")
    return text


def parse_trivy(path):
    """Rows (id, severity, package, installed, fixed, remedy, where) from a `trivy -f json` file."""
    try:
        doc = json.loads(load_json(path))
    except json.JSONDecodeError as e:
        raise InputError(f"{path}: not JSON ({e.msg})")
    # Trivy leaves out `Results` when nothing was found, so SchemaVersion is what proves
    # the file is a Trivy report at all.
    if not isinstance(doc, dict) or "SchemaVersion" not in doc:
        raise InputError(f"{path}: not a Trivy report (no SchemaVersion)")
    rows = []
    for res in doc.get("Results") or []:
        # OS packages are cured by a base image refresh. A language package is cured by a
        # rebuild only when it is the Go standard library inside one of OUR binaries; a
        # module, an npm package or a third-party tool's own runtime is a pin to bump.
        target = res.get("Target", "?")
        is_os = res.get("Class") == "os-pkgs"
        for v in res.get("Vulnerabilities") or []:
            sev = v.get("Severity")
            fixed = (v.get("FixedVersion") or "").strip()
            if sev not in SEVERITIES or not fixed:
                continue
            own_stdlib = v.get("PkgName") == "stdlib" and target.rsplit("/", 1)[-1] in OWN_BINARIES
            rows.append((v.get("VulnerabilityID", "?"), sev, v.get("PkgName", "?"),
                         v.get("InstalledVersion", "?"), fixed,
                         "rebuild" if is_os or own_stdlib else "bump",
                         "" if is_os else target))
    return rows


def parse_govulncheck(path):
    """Rows from `govulncheck -mode=binary -format json`, a stream of JSON objects."""
    text = load_json(path)
    dec = json.JSONDecoder()
    msgs, i = [], 0
    try:
        while i < len(text):
            while i < len(text) and text[i].isspace():
                i += 1
            if i >= len(text):
                break
            obj, i = dec.raw_decode(text, i)
            msgs.append(obj)
    except json.JSONDecodeError as e:
        raise InputError(f"{path}: not JSON ({e.msg})")
    if not any(isinstance(m, dict) and "config" in m for m in msgs):
        raise InputError(f"{path}: not govulncheck output (no config message)")
    osv = {m["osv"]["id"]: m["osv"] for m in msgs if isinstance(m, dict) and "osv" in m}
    rows, seen = [], set()
    for m in msgs:
        f = m.get("finding") if isinstance(m, dict) else None
        if not f or not f.get("fixed_version"):
            continue
        trace = f.get("trace") or [{}]
        mod = trace[0].get("module", "?")
        ver = trace[0].get("version", "?")
        key = (f["osv"], mod)
        if key in seen:
            continue
        seen.add(key)
        aliases = osv.get(f["osv"], {}).get("aliases") or []
        cve = next((a for a in aliases if a.startswith("CVE-")), "")
        vid = f"{f['osv']} ({cve})" if cve else f["osv"]
        # stdlib is fixed by the Go toolchain; any other module by go.mod.
        rows.append((vid, "-", mod, ver, f["fixed_version"],
                     "rebuild" if mod == "stdlib" else "bump", ""))
    return rows


def cell(s):
    """Scanner text lands in a Markdown table on a public issue: no pipes, no newlines."""
    return str(s).replace("|", "/").replace("\n", " ").replace("\r", " ").strip()


def render(release, targets):
    total = sum(len(rows) for _, rows in targets)
    rebuild = sum(1 for _, rows in targets for r in rows if r[5] == "rebuild")
    out = [MARKER, "",
           f"The latest published release is **{release}**. Its images and native rootfs "
           f"contain **{total}** fixable finding(s) "
           "(HIGH/CRITICAL with a fixed version; Go findings from `govulncheck`, which has no severity).", ""]
    if total:
        out += [f"- **{rebuild}** can be cured by rebuilding with a refreshed base image or Go toolchain "
                "(remedy `rebuild`): cut a patch release.",
                f"- **{total - rebuild}** need a pin or dependency bump (remedy `bump`: a Go module, an npm "
                "package, a baked tool): fix those through a normal PR first; a rebuild alone does not move them.",
                f"- How to cut the patch release: [{DOC_URL.rsplit('/', 1)[1]}]({DOC_URL}).", ""]
    for label, rows in targets:
        out.append(f"### {label}")
        out.append("")
        if not rows:
            out += ["No fixable HIGH/CRITICAL findings.", ""]
            continue
        order = {"CRITICAL": 0, "HIGH": 1}
        rows = sorted(rows, key=lambda r: (order.get(r[1], 2), r[0], r[2]))
        out += ["| ID | Severity | Package | Installed | Fixed in | Remedy | Where |",
                "|---|---|---|---|---|---|---|"]
        for r in rows[:MAX_ROWS_PER_TARGET]:
            out.append("| " + " | ".join(cell(c) for c in r) + " |")
        if len(rows) > MAX_ROWS_PER_TARGET:
            out.append("")
            out.append(f"…and {len(rows) - MAX_ROWS_PER_TARGET} more (see the workflow run's artifact).")
        out.append("")
    out += ["---",
            "Opened and updated by `image-vuln-scan.yml`. It closes when a scan of the latest "
            "published release finds nothing fixable. Published tags are immutable: the fix "
            "ships as a new patch version, never a re-pushed tag.", ""]
    return "\n".join(out), total


def main(argv):
    ap = argparse.ArgumentParser()
    ap.add_argument("--release", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--trivy", action="append", default=[], metavar="LABEL=FILE")
    ap.add_argument("--govulncheck", action="append", default=[], metavar="LABEL=FILE")
    a = ap.parse_args(argv)
    specs = [(p, parse_trivy) for p in a.trivy] + [(p, parse_govulncheck) for p in a.govulncheck]
    if not specs:
        print("no inputs", file=sys.stderr)
        return 2
    targets = []
    try:
        for spec, parser in specs:
            label, sep, path = spec.rpartition("=")
            if not sep or not label:
                raise InputError(f"bad spec {spec!r}, want LABEL=FILE")
            targets.append((label, parser(path)))
    except InputError as e:
        print(f"ERROR: {e}", file=sys.stderr)
        return 2
    body, total = render(a.release, targets)
    with open(a.out, "w", encoding="utf-8") as f:
        f.write(body)
    print(f"fixable={total}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
