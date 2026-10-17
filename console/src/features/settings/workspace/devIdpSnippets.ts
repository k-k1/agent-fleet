// Copy-ready app configuration for the dev OIDC IdP (ADR 0116), kept as data so the section
// renders them and the tests walk every combination without going through JSX strings.
//
// Invariants:
//  - The client secret is never a value: every template writes a reference to
//    AF_DEV_IDP_CLIENT_SECRET (the API never returns the secret, and a snippet is pasted into
//    files that get committed).
//  - "env" values mode references the AF_DEV_IDP_* variables the container already has, so the
//    file holds nothing workspace-specific. "fill" mode writes this workspace's issuer, discovery
//    URL and client id as literals, which makes the file workspace-bound (never to be committed).
//  - A line tagged `when: "preview"` exists only with "include preview settings" on, and is drawn
//    marked; `when: "plain"` is the line it replaces. Preview URLs come from AF_PREVIEW_URL_<port>,
//    never from the request Host (the app sees 127.0.0.1 behind the relay).

export type FrameworkId = "spring" | "authjs" | "go" | "betterauth" | "env";
export type ValuesMode = "env" | "fill";

export interface SnippetCtx {
  issuer: string;
  discovery: string;
  clientId: string;
  port: number;
  // The URL issued for `port` while the workspace runs; "" while stopped.
  previewUrl: string;
  values: ValuesMode;
  preview: boolean;
}

export interface SnippetLine {
  text: string;
  when?: "preview" | "plain";
}

export interface SnippetFile {
  file: string;
  // i18n key and params for how the file is activated; the second line of the code bar.
  activate?: { key: string; params?: Record<string, string> };
  lines: SnippetLine[];
}

export interface Variant {
  id: string;
  label: string; // i18n key
  // Whether the env-reference form is safe to commit (false: it must stay out of the repository).
  commitEnv: boolean;
  // Port the preview lines start from when the person has not picked one.
  defaultPort: number;
  // Warn that production reads this very file (a one-file variant).
  sharedWithProd?: boolean;
  files: (c: SnippetCtx) => SnippetFile[];
}

export interface Framework {
  id: FrameworkId;
  label: string; // i18n key, or a literal product name when `literal`
  literal: boolean;
  variants: Variant[];
}

const SECRET_ENV = "AF_DEV_IDP_CLIENT_SECRET";
const GOOGLE = "https://accounts.google.com";

const L = (text: string, when?: SnippetLine["when"]): SnippetLine => ({ text, when });
const fill = (c: SnippetCtx) => c.values === "fill";

// The preview URL as an expression in the host language: the issued URL (fill mode, when there
// is one) or a reference to AF_PREVIEW_URL_<port>.
function previewRef(c: SnippetCtx, style: "shell" | "ts"): string {
  const name = `AF_PREVIEW_URL_${c.port}`;
  if (fill(c) && c.previewUrl) return style === "ts" ? JSON.stringify(c.previewUrl) : c.previewUrl;
  if (style === "ts") return `process.env.${name}`;
  return `\${${name}}`;
}

// ---- Spring Boot -------------------------------------------------------------------------

function springYaml(c: SnippetCtx, oneFile: boolean): SnippetLine[] {
  const id = fill(c) ? c.clientId : oneFile ? "${AF_DEV_IDP_CLIENT_ID:${GOOGLE_CLIENT_ID:}}" : "${AF_DEV_IDP_CLIENT_ID}";
  const secret = oneFile ? `\${${SECRET_ENV}:\${GOOGLE_CLIENT_SECRET:}}` : `\${${SECRET_ENV}}`;
  const issuer = fill(c) ? c.issuer : oneFile ? `\${AF_DEV_IDP_ISSUER:${GOOGLE}}` : "${AF_DEV_IDP_ISSUER}";
  return [
    L(oneFile ? "# Falls back to Google when AF_DEV_IDP_* is not set" : "# Only in the workspace: activate with SPRING_PROFILES_ACTIVE=devidp"),
    L("spring:"),
    L("  security:"),
    L("    oauth2:"),
    L("      client:"),
    L("        registration:"),
    L("          google:"),
    L(`            client-id: ${id}`),
    L(`            client-secret: ${secret}`),
    L("            scope: openid,email,profile"),
    L("        provider:"),
    L("          google:"),
    L(`            issuer-uri: ${issuer}`),
    L("# Preview: build the callback from the public preview host", "preview"),
    L("server:", "preview"),
    L("  forward-headers-strategy: framework", "preview"),
  ];
}

function springEnv(c: SnippetCtx): SnippetLine[] {
  const P = "SPRING_SECURITY_OAUTH2_CLIENT";
  const id = fill(c) ? c.clientId : "$AF_DEV_IDP_CLIENT_ID";
  const issuer = fill(c) ? c.issuer : "$AF_DEV_IDP_ISSUER";
  return [
    L(`export ${P}_REGISTRATION_GOOGLE_CLIENT_ID="${id}"`),
    L(`export ${P}_REGISTRATION_GOOGLE_CLIENT_SECRET="$${SECRET_ENV}"`),
    L(`export ${P}_REGISTRATION_GOOGLE_SCOPE="openid,email,profile"`),
    L(`export ${P}_PROVIDER_GOOGLE_ISSUER_URI="${issuer}"`),
    L("# Preview: build the callback from the public preview host", "preview"),
    L("export SERVER_FORWARD_HEADERS_STRATEGY=framework", "preview"),
  ];
}

// ---- Auth.js -----------------------------------------------------------------------------

// AUTH_URL / NEXTAUTH_URL are set in code, not in .env.local: Next's dotenv-expand turns an unset
// ${AF_PREVIEW_URL_<port>} into an empty string, and Auth.js then reads the empty AUTH_URL as
// "set" and refuses the host (UntrustedHost) on a machine outside the workspace.
function authjsPreviewCode(c: SnippetCtx, v5: boolean): SnippetLine[] {
  const name = `AF_PREVIEW_URL_${c.port}`;
  const target = v5 ? "AUTH_URL" : "NEXTAUTH_URL";
  return [
    L("// Preview: callbacks are built from the public preview host (unset outside the workspace)", "preview"),
    L(`if (process.env.${name}) process.env.${target} = process.env.${name}`, "preview"),
    L("", "preview"),
  ];
}

function authjsPreviewEnv(v5: boolean): SnippetLine[] {
  return v5 ? [L("# Preview: trust the relayed host", "preview"), L("AUTH_TRUST_HOST=true", "preview")] : [];
}

function nextConfig(c: SnippetCtx): SnippetFile {
  const name = `AF_PREVIEW_URL_${c.port}`;
  return {
    file: "next.config.ts",
    activate: { key: "env.devidp_act_next" },
    lines: [
      L('import type { NextConfig } from "next"', "preview"),
      L("", "preview"),
      L(`const previewUrl = process.env.${name}`, "preview"),
      L("const nextConfig: NextConfig = {", "preview"),
      L("  // Next 15.2+: let the dev server answer the preview host", "preview"),
      L("  allowedDevOrigins: previewUrl ? [new URL(previewUrl).hostname] : [],", "preview"),
      L("}", "preview"),
      L("export default nextConfig", "preview"),
    ],
  };
}

function authjsV5(c: SnippetCtx): SnippetFile[] {
  const dev = fill(c)
    ? [`    issuer: ${JSON.stringify(c.issuer)},`, `    clientId: ${JSON.stringify(c.clientId)},`]
    : ["    issuer: process.env.AF_DEV_IDP_ISSUER,", "    clientId: process.env.AF_DEV_IDP_CLIENT_ID,"];
  const secret = `    clientSecret: process.env.${SECRET_ENV},`;
  const body = fill(c)
    ? [L("    Google({"), ...[...dev, secret].map((t) => L("  " + t)), L("    }),")]
    : [
        L("    Google("),
        L("      process.env.AF_DEV_IDP_ISSUER"),
        L("        ? {"),
        ...[...dev, secret].map((t) => L("        " + t)),
        L("          }"),
        L("        : {},"),
        L("    ),"),
      ];
  return [
    {
      file: "auth.ts",
      activate: { key: "env.devidp_act_always" },
      lines: [
        L('import NextAuth from "next-auth"'),
        L('import Google from "next-auth/providers/google"'),
        L(""),
        ...authjsPreviewCode(c, true),
        L("export const { handlers, auth } = NextAuth({"),
        L("  providers: ["),
        ...body,
        L("  ],"),
        L("})"),
      ],
    },
    { file: ".env.local", activate: { key: "env.devidp_act_next_env" }, lines: authjsPreviewEnv(true) },
    nextConfig(c),
  ];
}

function authjsV4(c: SnippetCtx): SnippetFile[] {
  const devWell = fill(c) ? JSON.stringify(c.discovery) : "process.env.AF_DEV_IDP_WELL_KNOWN";
  const devId = fill(c) ? JSON.stringify(c.clientId) : "process.env.AF_DEV_IDP_CLIENT_ID!";
  const dev = [L(`  wellKnown: ${devWell},`), L(`  clientId: ${devId},`), L(`  clientSecret: process.env.${SECRET_ENV}!,`)];
  return [
    {
      file: "pages/api/auth/[...nextauth].ts",
      activate: { key: "env.devidp_act_always" },
      lines: fill(c)
        ? [
            L('import NextAuth from "next-auth"'),
            L('import GoogleProvider from "next-auth/providers/google"'),
            L(""),
            ...authjsPreviewCode(c, false),
            L("export default NextAuth({"),
            L("  providers: ["),
            L("    GoogleProvider({"),
            ...dev.map((l) => L("    " + l.text)),
            L("    }),"),
            L("  ],"),
            L("})"),
          ]
        : [
            L('import NextAuth from "next-auth"'),
            L('import GoogleProvider from "next-auth/providers/google"'),
            L(""),
            ...authjsPreviewCode(c, false),
            L("const dev = process.env.AF_DEV_IDP_WELL_KNOWN"),
            L("  ? {"),
            ...dev.map((l) => L("    " + l.text)),
            L("    }"),
            L("  : null"),
            L(""),
            L("export default NextAuth({"),
            L("  providers: ["),
            L("    GoogleProvider(dev ?? {"),
            L("      clientId: process.env.GOOGLE_CLIENT_ID!,"),
            L("      clientSecret: process.env.GOOGLE_CLIENT_SECRET!,"),
            L("    }),"),
            L("  ],"),
            L("})"),
          ],
    },
    { file: ".env.local", activate: { key: "env.devidp_act_next_env" }, lines: authjsPreviewEnv(false) },
    nextConfig(c),
  ];
}

// ---- Go ----------------------------------------------------------------------------------

// getenv with a fallback: the preview URL is empty outside the workspace (and the IdP only
// accepts a redirect on the current preview URL or localhost), so the callback base falls back
// to http://localhost:<port> there.
function goGetenv(): SnippetLine[] {
  const T = "\t";
  return [
    L("// getenv returns the variable, or the fallback when it is unset or empty."),
    L("func getenv(key, fallback string) string {"),
    L(`${T}if v := os.Getenv(key); v != "" {`),
    L(`${T}${T}return v`),
    L(`${T}}`),
    L(`${T}return fallback`),
    L("}"),
    L(""),
  ];
}

function goPreviewBase(c: SnippetCtx): string {
  return `getenv("AF_PREVIEW_URL_${c.port}", "http://localhost:${c.port}")`;
}

function goOidc(c: SnippetCtx): SnippetFile[] {
  const T = "\t";
  const redirect = (path: string) => [
    L(`${T}${T}RedirectURL:  "http://localhost:${c.port}${path}",`, "plain"),
    L(`${T}${T}RedirectURL:  ${goPreviewBase(c)} + "${path}",`, "preview"),
  ];
  const creds = fill(c)
    ? [
        L(`${T}issuer := ${JSON.stringify(c.issuer)}`),
        L(`${T}clientID := ${JSON.stringify(c.clientId)}`),
        L(`${T}clientSecret := os.Getenv("${SECRET_ENV}")`),
      ]
    : [
        L(`${T}issuer := getenv("AF_DEV_IDP_ISSUER", "${GOOGLE}")`),
        L(`${T}clientID := getenv("AF_DEV_IDP_CLIENT_ID", os.Getenv("GOOGLE_CLIENT_ID"))`),
        L(`${T}clientSecret := getenv("${SECRET_ENV}", os.Getenv("GOOGLE_CLIENT_SECRET"))`),
      ];
  return [
    {
      file: "auth.go",
      activate: { key: "env.devidp_act_go_get" },
      lines: [
        L("package main"),
        L(""),
        L("import ("),
        L(`${T}"context"`),
        L(`${T}"os"`),
        L(""),
        L(`${T}"github.com/coreos/go-oidc/v3/oidc"`),
        L(`${T}"golang.org/x/oauth2"`),
        L(")"),
        L(""),
        ...goGetenv(),
        L("func newOIDC(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {"),
        ...creds,
        L(`${T}provider, err := oidc.NewProvider(ctx, issuer)`),
        L(`${T}if err != nil {`),
        L(`${T}${T}return nil, nil, err`),
        L(`${T}}`),
        L(`${T}cfg := &oauth2.Config{`),
        L(`${T}${T}ClientID:     clientID,`),
        L(`${T}${T}ClientSecret: clientSecret,`),
        L(`${T}${T}Endpoint:     provider.Endpoint(),`),
        ...redirect("/auth/callback"),
        L(`${T}${T}Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},`),
        L(`${T}}`),
        L(`${T}return cfg, provider.Verifier(&oidc.Config{ClientID: clientID}), nil`),
        L("}"),
      ],
    },
  ];
}

function goGoth(c: SnippetCtx): SnippetFile[] {
  const T = "\t";
  const id = fill(c) ? JSON.stringify(c.clientId) : 'os.Getenv("AF_DEV_IDP_CLIENT_ID")';
  const well = fill(c) ? JSON.stringify(c.discovery) : 'os.Getenv("AF_DEV_IDP_WELL_KNOWN")';
  const cb = "/auth/openid-connect/callback";
  return [
    {
      file: "auth.go",
      activate: { key: "env.devidp_act_go_get" },
      lines: [
        L("package main"),
        L(""),
        L("import ("),
        L(`${T}"log"`),
        L(`${T}"os"`),
        L(""),
        L(`${T}"github.com/markbates/goth"`),
        L(`${T}"github.com/markbates/goth/providers/openidConnect"`),
        L(")"),
        L(""),
        ...goGetenv(),
        L("func setupGoth() {"),
        L(`${T}provider, err := openidConnect.New(`),
        L(`${T}${T}${id},`),
        L(`${T}${T}os.Getenv("${SECRET_ENV}"),`),
        L(`${T}${T}"http://localhost:${c.port}${cb}",`, "plain"),
        L(`${T}${T}${goPreviewBase(c)}+"${cb}",`, "preview"),
        L(`${T}${T}${well},`),
        L(`${T}${T}"openid", "email", "profile",`),
        L(`${T})`),
        L(`${T}if err != nil {`),
        L(`${T}${T}log.Fatal(err)`),
        L(`${T}}`),
        L(`${T}goth.UseProviders(provider)`),
        L("}"),
      ],
    },
  ];
}

// ---- Better Auth ---------------------------------------------------------------------------

function betterAuth(c: SnippetCtx): SnippetFile[] {
  const well = fill(c) ? JSON.stringify(c.discovery) : "process.env.AF_DEV_IDP_WELL_KNOWN!";
  const id = fill(c) ? JSON.stringify(c.clientId) : "process.env.AF_DEV_IDP_CLIENT_ID!";
  return [
    {
      file: "auth.ts",
      activate: { key: "env.devidp_act_always" },
      lines: [
        L('import { betterAuth } from "better-auth"'),
        L('import { genericOAuth } from "better-auth/plugins"'),
        L(""),
        L("export const auth = betterAuth({"),
        L("  // Preview: callbacks are built from the public preview host", "preview"),
        L(`  baseURL: ${previewRef(c, "ts")},`, "preview"),
        L("  plugins: ["),
        L("    genericOAuth({"),
        L("      config: ["),
        L("        {"),
        L('          providerId: "af-dev-idp",'),
        L(`          discoveryUrl: ${well},`),
        L(`          clientId: ${id},`),
        L(`          clientSecret: process.env.${SECRET_ENV}!,`),
        L('          scopes: ["openid", "email", "profile"],'),
        L("        },"),
        L("      ],"),
        L("    }),"),
        L("  ],"),
        L("})"),
      ],
    },
  ];
}

// ---- Environment only ----------------------------------------------------------------------

function envOnly(c: SnippetCtx): SnippetFile[] {
  const v = (envName: string, literal: string) => (fill(c) ? literal : `\${${envName}}`);
  return [
    {
      file: ".env.devidp",
      activate: { key: "env.devidp_act_dotenv" },
      lines: [
        L(`OIDC_ISSUER=${v("AF_DEV_IDP_ISSUER", c.issuer)}`),
        L(`OIDC_DISCOVERY_URL=${v("AF_DEV_IDP_WELL_KNOWN", c.discovery)}`),
        L(`OIDC_CLIENT_ID=${v("AF_DEV_IDP_CLIENT_ID", c.clientId)}`),
        L(`OIDC_CLIENT_SECRET=\${${SECRET_ENV}}`),
        L("# Preview: the public base URL the callback is built from", "preview"),
        L(`APP_BASE_URL=${previewRef(c, "shell")}`, "preview"),
      ],
    },
  ];
}

export const FRAMEWORKS: Framework[] = [
  {
    id: "spring",
    label: "Spring Boot",
    literal: true,
    variants: [
      {
        id: "profile",
        label: "env.devidp_v_spring_profile",
        commitEnv: true,
        defaultPort: 8080,
        files: (c) => [
          {
            file: "src/main/resources/application-devidp.yml",
            activate: { key: "env.devidp_act_spring_profile" },
            lines: springYaml(c, false),
          },
        ],
      },
      {
        id: "onefile",
        label: "env.devidp_v_spring_onefile",
        commitEnv: true,
        defaultPort: 8080,
        sharedWithProd: true,
        files: (c) => [
          { file: "src/main/resources/application.yml", activate: { key: "env.devidp_act_always" }, lines: springYaml(c, true) },
        ],
      },
      {
        id: "env",
        label: "env.devidp_v_spring_env",
        commitEnv: false,
        defaultPort: 8080,
        files: (c) => [{ file: "(shell)", activate: { key: "env.devidp_act_shell" }, lines: springEnv(c) }],
      },
    ],
  },
  {
    id: "authjs",
    label: "Auth.js",
    literal: true,
    variants: [
      { id: "v5", label: "env.devidp_v_authjs_v5", commitEnv: true, defaultPort: 3000, files: authjsV5 },
      { id: "v4", label: "env.devidp_v_authjs_v4", commitEnv: true, defaultPort: 3000, files: authjsV4 },
    ],
  },
  {
    id: "go",
    label: "Go",
    literal: true,
    variants: [
      { id: "oidc", label: "env.devidp_v_go_oidc", commitEnv: true, defaultPort: 8080, files: goOidc },
      { id: "goth", label: "env.devidp_v_go_goth", commitEnv: true, defaultPort: 8080, files: goGoth },
    ],
  },
  {
    id: "betterauth",
    label: "Better Auth",
    literal: true,
    variants: [{ id: "generic", label: "env.devidp_v_betterauth_generic", commitEnv: true, defaultPort: 3000, files: betterAuth }],
  },
  {
    id: "env",
    label: "env.devidp_fw_env",
    literal: false,
    variants: [{ id: "dotenv", label: "env.devidp_v_env_dotenv", commitEnv: true, defaultPort: 8080, files: envOnly }],
  },
];

// The lines a file shows under the current options.
export function visibleLines(f: SnippetFile, preview: boolean): SnippetLine[] {
  return f.lines.filter((l) => (l.when === "preview" ? preview : l.when === "plain" ? !preview : true));
}

// The file's text exactly as shown (what Copy puts on the clipboard).
export function fileText(f: SnippetFile, preview: boolean): string {
  return visibleLines(f, preview)
    .map((l) => l.text)
    .join("\n");
}

// Files that end up with no visible line (the preview-only ones with the option off) are dropped.
export function renderFiles(v: Variant, c: SnippetCtx): SnippetFile[] {
  return v.files(c).filter((f) => visibleLines(f, c.preview).length > 0);
}
