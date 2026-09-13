# Brand icon attribution

Logos for the things the Console names by brand: the agent CLIs (`agents/`), the services a
workspace connects to (`services/`) and the companies that make the models (`providers/`).
Resolved in `src/lib/brandicons.ts`; rendered as a CSS mask in `currentColor`
(`styles/brandicons.css`), so every icon follows the theme and no per-icon color rule exists.

| Folder | Source | License |
|---|---|---|
| `agents/` | https://github.com/lobehub/lobe-icons | MIT |
| `services/` | https://github.com/simple-icons/simple-icons | CC0-1.0 |
| `providers/` | https://github.com/anomalyco/models.dev (`providers/<id>/logo.svg`) | MIT |

**Those licenses cover the repositories, not the marks themselves.** Each logo is a trademark
of the company it names; they are used here nominatively, to identify the CLI a session ran
through, the service an account is connected to, or the company that made a model the user is
choosing between. simple-icons says the same of its own
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
  because the agent kind says which CLI ran, not who sells the model. `providers/` is the
  other half of that distinction and holds the vendors. The one exception is
  `claude`: upstream's `claudecode` mark is a dense pixel glyph that turns to mush at the 16px
  the session rail renders it at (measured in headless Chromium), while the Claude sunburst
  stays legible — and the label beside it already says which CLI it is.
- Upstream ships these for inline `<svg>` use, with `width="1em" height="1em" style=…`.
  Those attributes are stripped on vendoring: as a mask the box comes from the caller and
  only the `viewBox` matters.
- `shell` and `ssm` are not products and keep their codicons (`terminal` / `cloud`).

## `providers/` — the model makers

Added 2026-09-13, after the launch model picker was rebuilt as a custom listbox
(`ui/ModelCombo.tsx`). It had been evaluated and dropped two days earlier for exactly one
reason: every surface that would have carried the mark was a native `<select>`, and an
`<option>` cannot hold markup. That is now true of one surface fewer.

- **Which ids exist here is decided by the Agent, not by this folder.** Each file is named
  after a provider id that `workspace/agent/model_provider.go` can answer with, and the
  Console draws `bi-provider-<that id>` without a lookup table. The Go side's
  `TestModelVendorTablesOnlyNameMarkedProviders` and this side's `lib/brandicons.test.ts` are
  what keep the two lists from drifting apart; add a maker to one and the other fails.
- **The ids are makers, never gateways.** `opencode` and `github-copilot` are absent on
  purpose although the Console has marks for both in `agents/`: those say which CLI ran, and
  an opencode id names the billing route rather than who built the model. Reusing the mark
  here would credit the gateway with someone else's model.
- `google-vertex` folds onto `google.svg` (same models, different endpoint) rather than
  getting a mark of its own.
- The remaining surfaces the earlier note listed are still native `<select>`s and still carry
  no mark: the AI-model rows in Settings, the OpenCode provider chooser, and the unit-price
  source in the Usage view (a `title=` tooltip). Each would need the same treatment.
- Makers with no logo upstream keep no mark at all — an unplaceable model shows a blank slot
  rather than a stand-in, because a wrong logo next to a model someone is about to pay for is
  worse than a plain row. Measured on the shipped catalog, that is 16 of opencode's 102 ids,
  all free-tier lines from Tencent / Xiaomi / inclusionAI / Meituan.
