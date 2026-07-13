# Brand assets

Files here are copied verbatim into `dist/` by Vite (so they are served by the
Control Plane at the site root) and are reachable **without authentication**
(authGate exempts `/brand/`), since the login page must render before sign-in.

## `agent-fleet-banner.webp`

The login landing page (`GET /login`, served by the CP — see
`control-plane/oauth_google.go`) embeds this banner as its hero:

    <img class="hero" src="/brand/agent-fleet-banner.webp" …>

Drop the Agent Fleet banner here with exactly this name:

    console/public/brand/agent-fleet-banner.webp

Recommended: a wide banner (~1120px wide, WebP q≈72, ≲100KB) carrying the
wordmark + "Deploy. Connect. Scale." tagline. It is only ever rendered at
`width:min(94vw,560px)`, so there is no need to ship a 2000px source. Until the
file exists the login page falls back to a plain text wordmark (the `onerror`
handler), so sign-in still works.

## `idle-1.webp` … `idle-7.webp`

Brand artwork shown over an **unattached** terminal pane (see
`console/src/features/terminal/TerminalView.tsx`). Each empty pane picks one at
random. They are only ever rendered small (`max-width ~440px`) at `opacity:0.16`
as a faint watermark, so keep them lightweight: ~480px square, WebP q≈60,
≲30KB each (downscale + lossy-encode the source art).
