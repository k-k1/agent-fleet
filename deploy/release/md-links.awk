# Markdown-aware link handling, shared by stage-docs.sh and pin-md-links.sh. Only links
# in prose are touched: a `](x.md)` inside a fenced block or an inline code span is an
# example, not a link, and must reach the reader exactly as written. Two modes (-v mode=):
#   links    print the target of every prose link, one per line
#   rewrite  apply -v map=FILE (lines "old<TAB>new") to prose links, literally, and print
#            the file
# A fence is ``` or ~~~ (three or more, up to three spaces of indent) and is closed by the
# same character at least as long; a code span is a backtick run closed by a run of the
# same length on the same line (an unmatched run is literal text). Written for mawk (the
# Debian default, also in the CP image's docs stage): no interval expressions, and no
# variable may be named after a builtin such as close().
function prose(seg,   out, m, t, i, pre) {
  if (mode == "links") {
    while (match(seg, /\]\([^) ]+\)/)) {
      print substr(seg, RSTART + 2, RLENGTH - 3)
      seg = substr(seg, RSTART + RLENGTH)
    }
    return ""
  }
  for (i = 1; i <= nmap; i++) {
    out = ""
    while ((m = index(seg, "](" from[i] ")")) > 0) {
      out = out substr(seg, 1, m - 1) "](" to[i] ")"
      seg = substr(seg, m + length(from[i]) + 3)
    }
    seg = out seg
  }
  return seg
}
function line_out(s,   out, n, run, rest, cl, k) {
  out = ""
  while ((k = index(s, "`")) > 0) {
    out = out prose(substr(s, 1, k - 1))
    s = substr(s, k)
    match(s, /^`+/); run = substr(s, 1, RLENGTH); rest = substr(s, RLENGTH + 1)
    cl = 0
    for (n = 1; n <= length(rest); n++) {
      if (substr(rest, n, length(run)) == run && substr(rest, n + length(run), 1) != "`" && (n == 1 || substr(rest, n - 1, 1) != "`")) { cl = n; break }
    }
    if (cl) { out = out run substr(rest, 1, cl - 1) run; s = substr(rest, cl + length(run)) }
    else { out = out run; s = rest }
  }
  return out prose(s)
}
BEGIN {
  nmap = 0
  if (mode == "rewrite") while ((getline l < map) > 0) { t = index(l, "\t"); nmap++; from[nmap] = substr(l, 1, t - 1); to[nmap] = substr(l, t + 1) }
}
{
  if (fence != "") {
    if (match($0, /^ ? ? ?(```+|~~~+)[ \t]*$/)) { f = $0; sub(/^ */, "", f); sub(/[ \t]*$/, "", f)
      if (substr(f, 1, 1) == substr(fence, 1, 1) && length(f) >= length(fence)) fence = "" }
    if (mode == "rewrite") print
    next
  }
  if (match($0, /^ ? ? ?(```+|~~~+)/)) { f = substr($0, RSTART, RLENGTH); sub(/^ */, "", f); fence = f
    if (mode == "rewrite") print
    next
  }
  r = line_out($0)
  if (mode == "rewrite") print r
}
