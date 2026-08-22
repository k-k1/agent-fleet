// ステンシル台帳（`control-plane/assets/drawio-stencils.json`）を焼き直す（docs/65 §65.5.3）。
//
// 台帳が CP 側に置いてあるのは、照合するのが CP だからである（go:embed）。Console 側に
// 置いた台帳はただの飾りで、防壁にはならない。
//
// 台帳は 2 つの役割を兼ねる:
//   1. **SSRF の防壁**。取りに行くセット名は、信用できない `.drawio` の中身
//      （`shape=mxgraph.<set>.<name>`）から来る。台帳に無い名前を CP が取りに行く実装は
//      「図を開かせるだけで CP に任意 URL を叩かせる」道具になる。
//   2. **完全性の担保**。CP は取得したバイト列を sha256 で突き合わせてから保存する。
//
// **絞ってはいけない。** 載せなかったセットは 404 ＝ その図が黙って劣化する。全件で
// 20 KB 程度しかないので、絞る動機（配布サイズ）はそもそも成り立たない。絞るのは
// 台帳ではなく、閉域向けの事前投入（`stencils-preseed.mjs`）の方。
//
// 鍵は **ビューアが実際に要求するパス**である。`mxStencilRegistry` は
// `mxgraph.<a>.<b>.<name>` を `<a>/<b>` に畳み（getBasenameForStencil）、
// `_-_` を `_` に置換してから `<basename>.xml` を引く。したがって鍵は
// `stencils/` からの相対パス（`aws4.xml` / `rack/f5.xml` / `cisco_safe/threat.xml`）。
//
//   node console/scripts/drawio/stencils-manifest.mjs            # 差分を表示するだけ
//   node console/scripts/drawio/stencils-manifest.mjs --write    # 書き出す
//
// 版は `console/vendor/drawio/README.md` の同梱ビューアと必ず揃える（`--tag` で上書き可）。
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

// 同梱ビューアと版が食い違った台帳は、名前が変わったセットを黙って 404 にする。
function viewerTag() {
  const md = fs.readFileSync(path.join(REPO, "console/vendor/drawio/README.md"), "utf8");
  return md.match(/\*\*(v\d+\.\d+\.\d+)\*\*/)?.[1] ?? null;
}

const pinned = viewerTag();
if (pinned && pinned !== TAG) {
  console.error(`同梱ビューアは ${pinned} なのに台帳を ${TAG} で焼こうとしている。README と揃えること。`);
  process.exit(2);
}

console.error(`drawio ${TAG} の stencils/ を列挙中…`);
const tree = await (await fetch(`https://api.github.com/repos/jgraph/drawio/git/trees/${TAG}?recursive=1`, {
  headers: { "User-Agent": "agent-fleet", ...(process.env.GH_TOKEN ? { Authorization: `Bearer ${process.env.GH_TOKEN}` } : {}) },
})).json();
if (!tree.tree) throw new Error(`tree を取得できない: ${JSON.stringify(tree).slice(0, 200)}`);

// `.xml` だけ。`stencils/` には LICENSE と clipart/*.png も居るが、ビューアは
// `<basename>.xml` しか要求しないので台帳に入れる意味が無い（入れると SSRF の
// 防壁が緩むだけ）。
const files = tree.tree
  .filter((e) => e.type === "blob" && e.path.startsWith(PREFIX) && e.path.endsWith(".xml"))
  .map((e) => ({ name: e.path.slice(PREFIX.length), size: e.size }))
  .sort((a, b) => (a.name < b.name ? -1 : 1));
console.error(`${files.length} 件 / ${(files.reduce((t, f) => t + f.size, 0) / 1048576).toFixed(1)} MB。sha256 を取るため全件を取得する…`);

const sets = {};
let done = 0;
// 並列を上げると raw.githubusercontent が ECONNRESET を返す（実測 8 並列で落ちた）。
// 台帳を焼くのは版を上げたときだけなので、遅くても確実な方を採る。
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
      // git の size は blob のバイト数。取得結果と食い違ったら列挙と実体がずれている。
      if (buf.length !== f.size) throw new Error(`${f.name}: ${buf.length} bytes（tree は ${f.size}）`);
      sets[f.name] = { sha256: crypto.createHash("sha256").update(buf).digest("hex"), size: buf.length };
      if (++done % 25 === 0) console.error(`  ${done} 件…`);
    }
  }),
);

const manifest = {
  version: TAG,
  base: RAW,
  // 鍵の並びを固定して差分を読めるようにする。
  sets: Object.fromEntries(Object.keys(sets).sort().map((k) => [k, sets[k]])),
};
const json = JSON.stringify(manifest, null, 1) + "\n";

const before = fs.existsSync(OUT) ? fs.readFileSync(OUT, "utf8") : "";
if (before === json) {
  console.log(`変更なし（${Object.keys(manifest.sets).length} 件 / ${(json.length / 1024).toFixed(1)} KB）`);
} else if (WRITE) {
  fs.writeFileSync(OUT, json);
  console.log(`${OUT} を書き出した（${Object.keys(manifest.sets).length} 件 / ${(json.length / 1024).toFixed(1)} KB）`);
} else {
  const old = before ? JSON.parse(before) : { sets: {} };
  const added = Object.keys(manifest.sets).filter((k) => !old.sets[k]);
  const removed = Object.keys(old.sets).filter((k) => !manifest.sets[k]);
  const changed = Object.keys(manifest.sets).filter((k) => old.sets[k] && old.sets[k].sha256 !== manifest.sets[k].sha256);
  console.log(`差分あり: 追加 ${added.length} / 削除 ${removed.length} / 変更 ${changed.length}`);
  for (const k of [...added.map((k) => `+ ${k}`), ...removed.map((k) => `- ${k}`), ...changed.map((k) => `~ ${k}`)].slice(0, 40)) console.log(`  ${k}`);
  console.log("書き出すなら --write");
}
