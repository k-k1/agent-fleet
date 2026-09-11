# Brand icon attribution

Logos for the things the Console names by brand. Today that is the agent CLIs (`agents/`)
only. Resolved in `src/lib/brandicons.ts`; rendered as a CSS mask in `currentColor`
(`styles/brandicons.css`), so every icon follows the theme and no per-icon color rule exists.

| Folder | Source | License |
|---|---|---|
| `agents/` | https://github.com/lobehub/lobe-icons | MIT |

**The MIT license covers that repository, not the marks themselves.** Each logo is a
trademark of the company it names; they are used here nominatively, to identify the CLI a
session actually ran through. Anyone redistributing the Console under their own brand should
check the vendors' brand guidelines rather than read the MIT grant as permission.

Notes:
- Only the icons actually referenced are vendored, not the upstream set (lobe-icons ships 906
  SVGs; bundling it whole would cost far more than the seven in use).
- `agents/` uses each set's **product** icon, not its vendor's: `codex` rather than `openai`,
  because the agent kind says which CLI ran, not who sells the model. The one exception is
  `claude`: upstream's `claudecode` mark is a dense pixel glyph that turns to mush at the 16px
  the session rail renders it at (measured in headless Chromium), while the Claude sunburst
  stays legible — and the label beside it already says which CLI it is.
- Upstream ships these for inline `<svg>` use, with `width="1em" height="1em" style=…`.
  Those attributes are stripped on vendoring: as a mask the box comes from the caller and
  only the `viewBox` matters.
- `shell` and `ssm` are not products and keep their codicons (`terminal` / `cloud`).

## Per-provider icons are deliberately absent

A second set, keyed by the model's provider (anthropic / openai / zhipuai / …), was
evaluated and dropped in 2026-09. The source would have been models.dev
(https://github.com/anomalyco/models.dev, MIT, `providers/<id>/logo.svg`, 200 logos), whose
ids match the ones this repo already uses for pricing in `workspace/agent/usage_catalog.go`.

It was dropped because the places where a provider mark would actually help — the launch
model picker, the AI-model rows in Settings, the OpenCode provider chooser — are native
`<select>` elements, and the one place the Console names a provider today (the unit-price
source in the Usage view) is a `title=` tooltip. An `<option>` cannot hold markup, so those
surfaces need a custom listbox first. What remained reachable (the excluded-model chips, the
`oc-keys` env list) already spells the provider out in text, which is not worth vendoring
nine more trademarked logos for. Revisit if the picker is ever rebuilt.
