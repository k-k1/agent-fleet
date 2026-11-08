// Classic, parser-blocking script loaded from index.html's <head>, before the module entry.
// It fixes up <base href> so relative asset/API/WS URLs resolve under the mount point when
// the Console sits behind a path-stripping proxy (e.g. Caddy strips /agent-fleet).
//
// It lives in a file, not inline, so the Console's CSP can say script-src 'self'.
// index.html references it as "?af-bootstrap" (query only) on purpose: before this script
// runs there is no <base>, and a path-relative URL such as "base-bootstrap.js" resolves
// against "/agent-fleet" (no trailing slash) to "/base-bootstrap.js", outside the mount.
// A query-only reference keeps the document's own path; the Control Plane answers it
// with this file (registerStatic).
(function () {
  var p = location.pathname;
  // Action URLs render the same Console shell. Point relative assets/API
  // back at the Workspace root, not beneath the opaque attachment path.
  var action = p.match(/^(.*\/)open\/browser-attachment\/[^/]+\/?$/);
  if (action) {
    document.write('<base href="' + action[1] + '">');
  } else if (!p.endsWith("/") && !/\.[a-z0-9]+$/i.test(p.split("/").pop())) {
    document.write('<base href="' + p + '/">');
  }
})();
