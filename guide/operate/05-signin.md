---
audience: "someone wiring an identity provider into the deployment"
source_of_truth: "the scripts under `deploy/` — a command here that contradicts the script it describes is a bug in this page"
---

# 05. Sign-in Methods — Setting Up an IdP End to End

English | [日本語](05-signin.ja.md)

This chapter has moved to pages organised by task.

| What you want to do | Page |
|---|---|
| What every IdP has in common, the redirect URI, where each setting lives, checking that it worked | [Set up how people sign in](signin/overview.md) |
| Create a Google client and put it in `.env` | [Sign in with Google](signin/google.md) |
| The Entra app registration, pinning the issuer, one directory behind two app registrations | [Sign in with Microsoft Entra ID](signin/entra.md) |
| The GitHub OAuth App, the organisation's approval, signing in again after a restart | [Sign in with GitHub](signin/github.md) |
| Okta, Keycloak, Auth0, Cognito, GitLab and others | [Sign in with another OIDC IdP](signin/oidc.md) |
| Check and approve an IdP a tenant registered | [Approve a tenant's own sign-in](signin/tenant-idp.md) |
| When it still does not work | [Cannot log in](diagnose/cannot-login.md) |
