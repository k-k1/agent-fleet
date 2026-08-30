import type { Extension } from "@codemirror/state";
import { baseName } from "../../lib/filemeta.ts";

type Loader = () => Promise<Extension>;

const byExtension: Record<string, Loader> = {
  js: async () => (await import("@codemirror/lang-javascript")).javascript(),
  mjs: async () => (await import("@codemirror/lang-javascript")).javascript(),
  cjs: async () => (await import("@codemirror/lang-javascript")).javascript(),
  jsx: async () => (await import("@codemirror/lang-javascript")).javascript({ jsx: true }),
  ts: async () => (await import("@codemirror/lang-javascript")).javascript({ typescript: true }),
  tsx: async () => (await import("@codemirror/lang-javascript")).javascript({ jsx: true, typescript: true }),
  json: async () => (await import("@codemirror/lang-json")).json(),
  // Markdown loads without `codeLanguages`: nesting every fenced block's grammar
  // would pull the other language packages into this chunk (docs/log/44 §6 Phase 3).
  md: async () => (await import("@codemirror/lang-markdown")).markdown(),
  markdown: async () => (await import("@codemirror/lang-markdown")).markdown(),
  mdx: async () => (await import("@codemirror/lang-markdown")).markdown(),
  jsonc: async () => (await import("@codemirror/lang-javascript")).javascript(),
  css: async () => (await import("@codemirror/lang-css")).css(),
  html: async () => (await import("@codemirror/lang-html")).html(),
  htm: async () => (await import("@codemirror/lang-html")).html(),
  svg: async () => (await import("@codemirror/lang-xml")).xml(),
  xml: async () => (await import("@codemirror/lang-xml")).xml(),
  py: async () => (await import("@codemirror/lang-python")).python(),
  java: async () => (await import("@codemirror/lang-java")).java(),
  c: async () => (await import("@codemirror/lang-cpp")).cpp(),
  h: async () => (await import("@codemirror/lang-cpp")).cpp(),
  cc: async () => (await import("@codemirror/lang-cpp")).cpp(),
  cpp: async () => (await import("@codemirror/lang-cpp")).cpp(),
  cxx: async () => (await import("@codemirror/lang-cpp")).cpp(),
  hpp: async () => (await import("@codemirror/lang-cpp")).cpp(),
  rs: async () => (await import("@codemirror/lang-rust")).rust(),
  go: async () => (await import("@codemirror/lang-go")).go(),
  php: async () => (await import("@codemirror/lang-php")).php(),
  sql: async () => (await import("@codemirror/lang-sql")).sql(),
  yml: async () => (await import("@codemirror/lang-yaml")).yaml(),
  yaml: async () => (await import("@codemirror/lang-yaml")).yaml(),
};

export async function loadLanguageExtension(path: string): Promise<Extension> {
  const name = baseName(path).toLowerCase();
  const extension = name.includes(".") ? name.slice(name.lastIndexOf(".") + 1) : "";
  const loader = byExtension[extension];
  return loader ? loader() : [];
}
