// Build-time stub for marp-core's math dependencies (mathjax-full/*, katex,
// katex/package.json). We construct Marp with { math: false }, and verified that
// in that mode marp-core never accesses these modules at runtime (rendering still
// succeeds even when they are replaced with throwing proxies). They are aliased to
// this stub in vite.config.js purely to keep ~43MB of MathJax out of the bundle —
// leaving them in made the production build hang at the minify step.
const noop = new Proxy(function () {}, {
  get: () => noop,
  apply: () => noop,
  construct: () => noop,
});

export default noop;
export const version = "0";
