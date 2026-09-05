// Rebuild the stencil manifest (`control-plane/assets/drawio-stencils.json`), docs/log/65 §65.5.3.
//
// The manifest lives on the CP side because CP is what verifies against it (go:embed). A manifest
// kept in Console would be decoration, not a barrier.
//
// It serves two purposes at once:
//   1. An SSRF barrier. The set names to fetch come from untrusted `.drawio` content
//      (`shape=mxgraph.<set>.<name>`). An implementation where CP fetches a name absent from the
//      manifest becomes a tool for making CP hit an arbitrary URL just by opening a diagram.
//   2. Integrity. CP checks the fetched bytes against sha256 before storing them.
//
// Do not narrow this list. A set left out is a 404, i.e. that diagram silently degrades. The whole
// manifest is only about 20 KB, so the motive for narrowing it (distribution size) does not hold.
// What gets narrowed is not the manifest but the air-gapped preseed (`stencils-preseed.mjs`).
//
// The key is the path the viewer actually requests. `mxStencilRegistry` folds
// `mxgraph.<a>.<b>.<name>` into `<a>/<b>` (getBasenameForStencil), replaces `_-_` with `_`, and
// then looks up `<basename>.xml`. So the key is the path relative to `stencils/`
// (`aws4.xml` / `rack/f5.xml` / `cisco_safe/threat.xml`).
//
//   node console/scripts/drawio/stencils-manifest.mjs            # only show the diff
//   node console/scripts/drawio/stencils-manifest.mjs --write    # write it out
//
// The version must match the bundled viewer in `console/vendor/drawio/README.md` (`--tag`
// overrides it).
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.join(HERE, "../../..");
const OUT = path.join(REPO, "control-plane/assets/drawio-stencils.json");
const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const TAG = arg("tag", "v31.1.8");
const WRITE = argv.includes("--write");
const PREFIX = "src/main/webapp/stencils/";
const RAW = `https://raw.githubusercontent.com/jgraph/drawio/${TAG}/${PREFIX}`;

// A manifest whose version disagrees with the bundled viewer silently 404s any renamed set.
function viewerTag() {
  const md = fs.readFileSync(path.join(REPO, "console/vendor/drawio/README.md"), "utf8");
  return md.match(/\*\*(v\d+\.\d+\.\d+)\*\*/)?.[1] ?? null;
}

const pinned = viewerTag();
if (pinned && pinned !== TAG) {
  console.error(`the bundled viewer is ${pinned} but the manifest is being baked for ${TAG}. Match the README.`);
  process.exit(2);
}

console.error(`listing stencils/ of drawio ${TAG}...`);
const tree = await (await fetch(`https://api.github.com/repos/jgraph/drawio/git/trees/${TAG}?recursive=1`, {
  headers: { "User-Agent": "agent-fleet", ...(process.env.GH_TOKEN ? { Authorization: `Bearer ${process.env.GH_TOKEN}` } : {}) },
})).json();
if (!tree.tree) throw new Error(`cannot fetch the tree: ${JSON.stringify(tree).slice(0, 200)}`);

// `.xml` only. `stencils/` also holds LICENSE and clipart/*.png, but the viewer only ever requests
// `<basename>.xml`, so putting them in the manifest gains nothing and only loosens the SSRF
// barrier.
const files = tree.tree
  .filter((e) => e.type === "blob" && e.path.startsWith(PREFIX) && e.path.endsWith(".xml"))
  .map((e) => ({ name: e.path.slice(PREFIX.length), size: e.size }))
  .sort((a, b) => (a.name < b.name ? -1 : 1));
console.error(`${files.length} files / ${(files.reduce((t, f) => t + f.size, 0) / 1048576).toFixed(1)} MB. Fetching all of them to compute sha256...`);

const sets = {};
let done = 0;
// Raising the concurrency makes raw.githubusercontent return ECONNRESET (measured: it broke at 8).
// The manifest is only baked when the version is bumped, so take the slow but reliable option.
const QUEUE = 3;

async function get(url, tries = 4) {
  for (let i = 1; ; i++) {
    try {
      const res = await fetch(url);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      return Buffer.from(await res.arrayBuffer());
    } catch (e) {
      if (i >= tries) throw e;
      await new Promise((r) => setTimeout(r, 400 * i * i));
    }
  }
}

await Promise.all(
  Array.from({ length: QUEUE }, async () => {
    for (;;) {
      const f = files.shift();
      if (!f) return;
      const buf = await get(RAW + f.name.split("/").map(encodeURIComponent).join("/")).catch((e) => {
        throw new Error(`${f.name}: ${e.message}`);
      });
      // git's size is the blob's byte count. A mismatch means the listing and the actual files
      // have drifted apart.
      if (buf.length !== f.size) throw new Error(`${f.name}: ${buf.length} bytes (tree says ${f.size})`);
      sets[f.name] = { sha256: crypto.createHash("sha256").update(buf).digest("hex"), size: buf.length };
      if (++done % 25 === 0) console.error(`  ${done} done...`);
    }
  }),
);

const manifest = {
  version: TAG,
  base: RAW,
  // Fix the key order so the diff stays readable.
  sets: Object.fromEntries(Object.keys(sets).sort().map((k) => [k, sets[k]])),
};
const json = JSON.stringify(manifest, null, 1) + "\n";

const before = fs.existsSync(OUT) ? fs.readFileSync(OUT, "utf8") : "";
if (before === json) {
  console.log(`no change (${Object.keys(manifest.sets).length} sets / ${(json.length / 1024).toFixed(1)} KB)`);
} else if (WRITE) {
  fs.writeFileSync(OUT, json);
  console.log(`wrote ${OUT} (${Object.keys(manifest.sets).length} sets / ${(json.length / 1024).toFixed(1)} KB)`);
} else {
  const old = before ? JSON.parse(before) : { sets: {} };
  const added = Object.keys(manifest.sets).filter((k) => !old.sets[k]);
  const removed = Object.keys(old.sets).filter((k) => !manifest.sets[k]);
  const changed = Object.keys(manifest.sets).filter((k) => old.sets[k] && old.sets[k].sha256 !== manifest.sets[k].sha256);
  console.log(`diff: added ${added.length} / removed ${removed.length} / changed ${changed.length}`);
  for (const k of [...added.map((k) => `+ ${k}`), ...removed.map((k) => `- ${k}`), ...changed.map((k) => `~ ${k}`)].slice(0, 40)) console.log(`  ${k}`);
  console.log("pass --write to write it out");
}
