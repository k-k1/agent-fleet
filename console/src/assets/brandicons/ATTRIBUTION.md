# Brand icon attribution

Logos for the things the Console names by brand: the agent CLIs (`agents/`) and the services
a workspace connects to (`services/`). Resolved in `src/lib/brandicons.ts`; rendered as a CSS
mask in `currentColor` (`styles/brandicons.css`), so every icon follows the theme and no
per-icon color rule exists.

| Folder | Source | License |
|---|---|---|
| `agents/` | https://github.com/lobehub/lobe-icons | MIT |
| `services/` | https://github.com/simple-icons/simple-icons | CC0-1.0 |

**Those licenses cover the repositories, not the marks themselves.** Each logo is a trademark
of the company it names; they are used here nominatively, to identify the CLI a session ran
through or the service an account is connected to. simple-icons says the same of its own
contents. Anyone redistributing the Console under their own brand should check the vendors'
brand guidelines rather than read the license grant as permission.

Notes:
- Only the icons actually referenced are vendored, not the upstream sets (lobe-icons ships 906
  SVGs and simple-icons over 3000; bundling either whole would cost far more than the handful
  in use).
- `services/` files are named after the settings card's provider id
  (`features/settings/parts/providerCard`), not after the upstream slug — `cloudwatch.svg` is
  simple-icons' `amazoncloudwatch`, `aws.svg` is `amazonwebservices`. That is what lets a card
  find its own icon without a lookup table.
- `svn` has no icon on purpose. The Apache Subversion mark is three diagonal stripes that
  carry nothing at 16px, where the "sv" monogram at least names the thing; compared at 1:1 in
  headless Chromium. `internal` (internal repositories) is not a brand at all.
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
