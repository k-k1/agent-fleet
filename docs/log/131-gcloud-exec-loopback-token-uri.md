# 131. af-gcloud-exec: a loopback `token_uri` for programs that read the token once — re-measured

- Issue: #1879. Design record: [decisions/0107](../decisions/0107-google-cloud-profiles.md), the notes
  "a loopback `token_uri` … measured, not built (2026-10-09)" and the maintainer's decision of 2026-10-10.
- Status: **measured again on newer versions (2026-10-17); recommendation: do not build.** No production code
  changed. The ADR already carries the first measurement and the decision; this entry confirms the table on
  current versions, adds the source-level reason each library behaves as it does, and writes the loopback
  assessment out in full. The ADR is left as it is.

## 131.1 What was measured, and how

Every program ran in a network namespace that holds only loopback (`unshare -rn`, then `lo` brought up with an
`ioctl`, because `ip` is not installed here). A connection to an outside address failed with "Network is
unreachable", so nothing could reach Google; a DNS lookup of `oauth2.googleapis.com` failed. The fake token
endpoint is a 20-line Python `http.server` on `127.0.0.1` with port 0 (the port was read back from a file), which
logs the path, the content type and the **names** of the form fields it receives (never their values) and
answers `{"access_token":"fake-access","expires_in":3600,"token_type":"Bearer"}`. The credential is a synthetic
`authorized_user` file: made-up `client_id`, `client_secret`, `refresh_token`, and `token_uri` set to the fake.
It is passed as `GOOGLE_APPLICATION_CREDENTIALS` with an empty `$HOME`. Verdict per library: did the fake log a
request, and did the program come back with `fake-access`. No real credential, no call to a Google API.

## 131.2 Results

| Consumer | Version | Honours `token_uri` of an `authorized_user` file? | Evidence |
|---|---|---|---|
| Go `golang.org/x/oauth2/google` | 0.37.0 | **Yes** | `FindDefaultCredentials` then `TokenSource.Token()` returned `fake-access`; the fake saw `POST /token`, form `grant_type=refresh_token`, `client_id`, `client_secret`, `refresh_token`. `google.go` copies the file's `token_uri` into `oauth2.Endpoint.TokenURL`. |
| Go `cloud.google.com/go/auth` | 0.24.1 | **No** (only programmatically) | `credentials.DetectDefault` then `Token()` tried `POST https://oauth2.googleapis.com/token` (lookup failed in the namespace); the fake saw nothing. The file's field is ignored: `DetectOptions.TokenURL` exists, but only the caller can set it, and no environment variable does. |
| Python `google-auth` | 2.62.0 | **No** | `google.auth.default()` returned credentials whose `_token_uri` is `https://oauth2.googleapis.com/token`; `refresh()` tried that host. `credentials.py` builds them with `token_uri=_GOOGLE_OAUTH2_TOKEN_ENDPOINT,  # always overrides`. |
| Node `google-auth-library` | 11.3.1 | **No** | `GoogleAuth.getAccessToken()` tried `https://oauth2.googleapis.com/token`. The refresh client sends to `endpoints.oauth2TokenUrl`, a constant of `OAuth2Client`; the file's value is not read. |
| Node `google-auth-library` | 9.15.1 (what `@google-cloud/storage` bundles) | **No** | Same result when run. |
| Terraform Google provider | 8.7.0 (Terraform 1.16.4) | **Yes, but not with the variable the wrapper sets** | See below. |

Terraform ran `plan` on a configuration with `provider "google" {}` and one `google_client_config` data source,
the provider downloaded once outside the namespace and `plan` run inside. Counted by the fake's log:

| Environment | Requests to the fake |
|---|---|
| `GOOGLE_APPLICATION_CREDENTIALS` = the file | 1 |
| `GOOGLE_CREDENTIALS` = the file (the provider's own variable, a path or JSON) | 1 |
| `GOOGLE_OAUTH_ACCESS_TOKEN` set beside `GOOGLE_APPLICATION_CREDENTIALS` | 0 |

The provider then hangs on its fixed `openidconnect.googleapis.com` user-info call, which cannot succeed in the
namespace, and `plan` was stopped by `timeout`, so a whole plan was not run; the evidence is the token request.
Which credential branch the provider chose was **not** read from its log (`TF_LOG=INFO` does not print it; the
2026-10-09 note read "Authenticating using …" at debug level on 8.6.0) — here it is inferred from the request counts.

No host allow-list or `universe_domain` check got in the way of the two consumers that follow `token_uri` (the
synthetic file has no `universe_domain`; a check that only a real credential would trip was not exercised).

Results match the 2026-10-09 note (Go oauth2 yes; Go auth, Python, Node no; Terraform yes without the static token
variable). Versions that moved since: `google-auth` 2.61.0 → 2.62.0, `google-auth-library` 11.2.0 → 11.3.1,
`cloud.google.com/go/auth` 0.24.0 → 0.24.1, provider 8.6.0 → 8.7.0.

## 131.3 The loopback listener

What the listener would be: `127.0.0.1:<port>` in the workspace's network namespace, served by the wrapper for
the life of one command, accepting a refresh request whose body carries a random per-run secret as
`client_secret` and `refresh_token`, replying with the run's current token.

**Who can connect.** Every process in the container, of every session: they share one network namespace and one
uid (`dev`). The port is not secret (it is in the credential file and in any `/proc/net/tcp` listing). So the gate
is the secret and nothing else; a process cannot be refused by address or by uid.

**What the per-run secret protects.**

- A process that has never seen the secret cannot get a token out of the listener. 256 random bits are not
  guessable; the comparison is constant-time.
- A web page or a server-side request forgery in an app running in the workspace that can send a POST to a loopback
  port (a DNS-rebinding page, an SSRF in a dev server) cannot get a token without the secret. This is the case where a
  secretless endpoint is dangerous, and where the secret does its work.
- A secret in a 0600 file in the private run directory, never in argv or environment, stays out of `ps` and of
  `/proc/<pid>/cmdline`.

**What it does not protect.**

- Another process of the same uid. It can read the 0600 file (same uid), and `/proc/<child>/environ`, which holds
  the file's path. It then asks the listener. But the token file in the same directory, and `GOOGLE_OAUTH_ACCESS_TOKEN`
  in the child's environment, are readable the same way today. A same-uid reader with continuing access already has
  the run's whole lifetime of tokens: the wrapper keeps replacing that token file, and re-reading it yields each
  renewed token. The listener adds no reader and no reach for such a process. The only difference is a narrower
  case: someone who copies the secret **once** (a leaked file, a log) and can reach the loopback port afterwards
  keeps minting tokens until the command ends, while a single copied access token dies in at most an hour.
- The renewed tokens are what the wrapper's renewer already makes (same account, same reach, no longer-lived), so
  the extra reach is time in that single-leak case, not scope. The ADR's rules for a renewal (refuse a different
  account, refuse a token that lasts no longer) would have to apply to the listener's replies too.
- A hostile process that can bind the port first. The port is chosen by the wrapper (bind 0) and written to the
  file before the child starts, so it cannot be pre-empted; but a program that sends the secret to a port it did not
  expect would give the secret away. Not measured.
- A library that ignores `token_uri` (three of the five consumers) sends the body to Google instead, with the secret
  as client secret and refresh token. The secret is random and useless there, but it leaves the workspace and the
  request fails with a Google error (`invalid_client` was observed for Python in the 2026-10-09 note, in an early run
  outside the namespace) in place of today's local "file not found". Nothing was sent to Google in this re-measurement.

**Compared with a metadata-server shim.** Go, Python and Node libraries find a metadata server through
`GCE_METADATA_HOST` (measured with a mock in the 2026-10-02 note). A shim carries no secret by design: the libraries
ask with a public header (`Metadata-Flavor: Google`), which a real metadata server also requires and rejects the
request without. Assuming the shim checks that header (a shim that does not would be open to any GET), a caller that
can send a GET with that header, such as an SSRF that controls headers or any local process, gets a token without
holding anything secret. An SSRF that controls only the URL and the method cannot add the header, so against it the
two designs are not distinguished by this measurement. Against an HTTP-only caller that cannot read the secret file,
the `token_uri` endpoint is the narrower one, because it needs a secret that caller never sees. A shim would also
make every library that works today with `GOOGLE_APPLICATION_CREDENTIALS` unset find a credential, taking away the
fail-closed rule. The loopback `token_uri` is the only one of the two whose request carries a secret, and it applies
only to `authorized_user`.

Assessment: against a same-uid reader the loopback listener adds nothing the token file does not already give
(the file is renewed during the run); it extends a single leaked secret past a single leaked token. Against an
HTTP-only caller without file access it is narrower than the shim. The cost is not the listener's exposure; it is
the next section.

## 131.4 Recommendation: do not build

1. **It does not reach the consumers that matter beyond Terraform.** Python, Node and `cloud.google.com/go/auth`
   ignore the field. A file at `GOOGLE_APPLICATION_CREDENTIALS` therefore helps Go's `oauth2` library and
   Terraform only, and makes the other three send the per-run secret to Google.
2. **The Terraform-only variant (the file in `GOOGLE_CREDENTIALS`, `GOOGLE_OAUTH_ACCESS_TOKEN` removed) has a cost
   that was measured, not guessed:** with the static token variable set beside the file the provider never asks the
   fake (0 requests), so the variable must go. The documented contract of the child's environment, and the
   client-library recipes in the guide, rely on it. It also puts a long-lived-in-effect secret and a listener in
   every run of a command that might be a plain `terraform output`.
3. **The gain is narrow.** It is one program (a `terraform apply` longer than the remaining token life, at most
   about an hour), and the guide's advice, to split work into commands shorter than the printed token life, covers
   it at no cost in code or exposure.

Revisit if one of these changes: split runs prove too costly in practice (the maintainer's own condition), Python
or Node's libraries start to honour `token_uri` (watch `google-auth`'s "always overrides" line), or
`cloud.google.com/go/auth` reads the file's field. Then the narrower variant is the one to build, scoped to the
provider (`GOOGLE_CREDENTIALS`), opt-in per run, with the listener on `127.0.0.1` only. No follow-up issue is filed:
#1879 stays open for this and for longer impersonated lifetimes, as the ADR says.

## 131.5 Not measured

- A refresh against Google, a whole `terraform plan`/`apply` (the provider's user-info call needs Google).
- `universe_domain` and any host allow-list with a real credential type other than `authorized_user`.
- That a hostile process can race the port, and the listener's behaviour under load (no listener was built).
- The Terraform provider's choice of credential branch from its own log (inferred from request counts here).
- `kubectl` against GKE, `gcloud` and `bq` (not consumers of `token_uri`; covered by the file renewal in the ADR).
