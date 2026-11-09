---
audience: "anyone looking up af-watch-hook's options, output and exact behaviour"
source_of_truth: "af-watch-hook --help, from the workspace-agent in the workspace"
---

# af-watch-hook

English | [日本語](af-watch-hook.ja.md)

Adds a webhook to a private GitHub or Bitbucket Cloud repository so that watches on its pull requests are poked at
once instead of waiting for the next poll. The background is in [Faster notices with webhooks](../../user/fleet/pr-feedback.md#faster-notices-with-webhooks).

## Synopsis

```sh
af-watch-hook add github|bitbucket <owner/name>
af-watch-hook list
af-watch-hook rm <id>
af-watch-hook --help
```

## Commands

| Command | What it does |
|---|---|
| `add` | Creates a hook at the Control Plane, then installs it at the repository with your own connection. A refused install deletes the Control Plane hook again |
| `list` | Prints your hooks: id, source, repository, creation time |
| `rm` | Deletes the webhook at the repository, then the hook at the Control Plane |

## What `add` installs

| | GitHub | Bitbucket Cloud |
|---|---|---|
| Content | JSON | JSON |
| Signature | `X-Hub-Signature-256` (HMAC-SHA256 with the hook secret) | `X-Hub-Signature` (the same) |
| Events | `pull_request`, `pull_request_review`, `pull_request_review_comment`, `pull_request_review_thread`, `issue_comment`, `check_run`, `check_suite`, `workflow_run` | `pullrequest:*` (created, updated, approved, fulfilled, rejected, comments, change requests) |
| Credential | the GitHub connection | the Bitbucket connection (OAuth or API token) |

## Behaviour

- **The secret is never shown.** The Control Plane returns it once; it goes to the repository's webhook settings and nowhere else
- **Admin rights are required** on the repository. When the token lacks them, `add` exits 1 with what to grant, and no hook is left at the Control Plane
- **Idempotent.** `add` for a repository that already has a hook changes nothing and prints the existing id
- **`rm` always retires the Control Plane hook**, even when the repository step fails (no token, no admin rights). It then prints a warning with the URL of the webhook to delete by hand; that webhook no longer verifies, so it is harmless until you do
- At most 20 hooks per member

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Done (including "already exists") |
| 1 | Failed; the message says why |
| 2 | Usage error |
