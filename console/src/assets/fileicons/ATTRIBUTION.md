# File icon attribution

The file-type icons here are imported from these open-source icon sets (all MIT).
The extension → type-key mapping (`src/lib/fileicons.js`) and the Seti per-type color
palette are ported from CodeLeaf's `ui/FileIcons.kt`. The active set is chosen in the
Console's Display settings (`iconSet`).

| Set (id) | folder | Source | License |
|---|---|---|---|
| VS Code Icons (`vscode`) | `vscode/` | https://github.com/vscode-icons/vscode-icons | MIT |
| Material | `material/` | https://github.com/material-extensions/vscode-material-icon-theme | MIT |
| Devicon | `devicon/` | https://github.com/devicons/devicon | MIT |
| Seti | `seti/` | https://github.com/jesseweed/seti-ui | MIT |

Notes:
- Only the subset of icons mapped by `fileicons.js` is bundled (kept small).
- Seti glyphs are monochrome; they are tinted per type via CSS mask (palette from
  seti-ui `styles/components/icons/mapping.less`). Seti lacks groovy / nodejs → those
  fall back to a generic codicon.
- Devicon's markdown/rust/json/yaml are black logos → tinted to the theme text color.
- Files with no brand icon fall back to a monochrome codicon; folders use codicons.
