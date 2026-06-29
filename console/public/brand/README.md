# Brand assets

Files here are copied verbatim into `dist/` by Vite (so they are served by the
Control Plane at the site root) and are reachable **without authentication**
(authGate exempts `/brand/`), since the login page must render before sign-in.

## `agent-fleet-banner.png`

The login landing page (`GET /login`, served by the CP — see
`control-plane/oauth_google.go`) embeds this banner as its hero:

    <img class="hero" src="/brand/agent-fleet-banner.png" …>

Drop the Agent Fleet banner here with exactly this name:

    console/public/brand/agent-fleet-banner.png

Recommended: a wide banner (~2000×860) carrying the wordmark + "Deploy. Connect.
Scale." tagline. Until the file exists the login page falls back to a plain text
wordmark (the `onerror` handler), so sign-in still works.
