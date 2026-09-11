# Brand icon attribution

Logos for the things the Console names by brand: the agent CLIs (`agents/`) and the model
providers (`providers/`). Resolved in `src/lib/brandicons.ts`; rendered as a CSS mask in
`currentColor` (`ui/Icon.tsx`), so every icon follows the theme and no per-icon color rule
exists.

| Folder | Source | License |
|---|---|---|
| `agents/` | https://github.com/lobehub/lobe-icons | MIT |
| `providers/` | https://github.com/anomalyco/models.dev (`providers/<id>/logo.svg`) | MIT |

**The MIT license covers those repositories, not the marks themselves.** Each logo is a
trademark of the company it names; they are used here nominatively, to identify the CLI or
the provider a session or a price actually ran through. Anyone redistributing the Console
under their own brand should check the vendors' brand guidelines rather than read the MIT
grant as permission.

Notes:
- Only the icons actually referenced are vendored, not the upstream sets (lobe-icons ships
  906 SVGs; models.dev, 200 provider logos at 558KB — bundling either whole would cost far
  more than the handful in use).
- `agents/` uses each set's **product** icon, not its vendor's: `codex` rather than `openai`.
  The agent kind says which CLI ran, which is a different axis from `providers/`, where the
  id is the model's billing provider. The one exception is `claude`: upstream's `claudecode`
  mark is a dense pixel glyph that turns to mush at the 16px the session rail renders it at
  (measured in headless Chromium), while the Claude sunburst stays legible — and the label
  beside it already says which CLI it is.
- Upstream ships these for inline `<svg>` use, with `width="1em" height="1em" style=…`.
  Those attributes are stripped on vendoring: as a mask the box comes from the caller and
  only the `viewBox` matters.
- `shell` and `ssm` are not products and keep their codicons (`terminal` / `cloud`).
- models.dev has no logo for some providers we name (`sakana`) and none for the agent kinds
  that are not providers (`cursor`, `kiro`, `agy`) — both resolve to null and fall back.
