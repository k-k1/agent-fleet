---
audience: "anyone who wants agents to work with Bitbucket pull requests and pipelines"
source_of_truth: "the bkt command in a session (Agent Fleet's wrapper) and the Console's Settings > \"Git hosting\""
---

# Use the Bitbucket CLI

English | [日本語](bitbucket-cli.ja.md)

Connect Bitbucket Cloud, and agents can create, read and comment on pull requests and check pipelines with `bkt`
([Bitbucket CLI](https://github.com/avivsinai/bitbucket-cli)). Nobody runs `bkt auth login` inside a session.

## How it works

| What you do | What the agent does |
|---|---|
| Connect Bitbucket | Uses `bkt` without logging in |
| Ask for what you need | Creates and comments on pull requests as your account |
| Fix the connection when it lacks a permission | Says so and stops when a permission is missing |

`bkt` reads your saved connection on every call. A token that expires is renewed on use, the same way git does it.
Agents already know how to use it, so there is no command to teach them.

## Before you start

Connect Bitbucket Cloud (bitbucket.org) in **⚙ Settings → "Git hosting"**. It is the same connection git uses to clone
and push; the steps are in [Connect a Git service](../code/connect-git.md#connect-bitbucket).

- Bitbucket Data Center cannot be connected from Settings. You [set it up yourself](#use-your-own-setup)
- When you connect with "**Connect with an app token**", give the token the scopes `bkt` needs as well ([Scopes](#scopes))

## Ask an agent

Just ask the way you normally would.

> Open a pull request for this change, with the description in English

> Read the review comments on PR 123, fix them and push

> Find out why the pipeline on main is failing

Agents can do things like these:

| What | Example |
|---|---|
| Create a pull request | `bkt pr create` |
| List and read pull requests | `bkt pr list`, `bkt pr view 123` |
| Read comments | `bkt pr comments 123` |
| Merge a pull request, when you ask | `bkt pr merge 123` |
| Check pipelines | `bkt pipeline` |

- **What they write is recorded under your name.** They can do only what your account and the connection allow
- **Agents use `bkt` rather than an MCP server.** They are told to. A CLI reads the connection on every call, so an
  expired login never means restarting the session

## Scopes

`bkt` can do only what the connection is allowed to. The permissions the connection itself needs (Account: Read and
Repositories: Read/Write for OAuth; `read:account`, `read:workspace:bitbucket` and `read:repository:bitbucket` for an
API token) are not enough for pull requests or pipelines.

| What | Connected with OAuth | Connected with an API token |
|---|---|---|
| Pull requests (create, comment, merge) | Pull requests: Read and Write | `read:pullrequest:bitbucket`, `write:pullrequest:bitbucket` |
| See pipelines | Pipelines: Read | `read:pipeline:bitbucket` |
| Run or stop a pipeline | Pipelines: Write | `write:pipeline:bitbucket` |

- **OAuth permissions are set by a tenant administrator** on the Bitbucket OAuth consumer. Once they are added,
  reconnect Bitbucket to get them
- **API token scopes are chosen when the token is created.** Write does not include read. When one is missing, paste a
  new token that has it
- The same permission lets you use "**Comment the work back**" on a Bitbucket pull request in the issue tracker's
  detail. The button appears only when the connection is known to have the pull request write permission
- Merging also needs your account to be allowed to merge into the destination branch. With the same permission, the detail
  offers **Merge** on a Bitbucket pull request ([Merge](../sessions/from-issues.md#merge))

## Use your own setup

When you have set something up yourself, `bkt` uses that instead.

| When you have | What happens |
|---|---|
| Run `bkt auth login`, or set `BKT_HOST` and `BKT_TOKEN` | `bkt` follows your setup and does not use the connection |

- A `bkt` setup applies to bitbucket.org too, even if you meant it only for a Data Center host. To use the connection
  on bitbucket.org, delete the config file (`~/.config/bkt/config.yml`)

## When it goes wrong

### `bkt` says "lacks a scope for this command"

The connection does not have the permission for that command. The message names the permission that is missing.

- **An OAuth connection**: have a tenant administrator add the permission, then reconnect Bitbucket in ⚙ Settings → "Git hosting"
- **An API token connection**: create a new token with the scope, and paste it

Retrying with another credential will not help. A token without the pull request scopes can list repositories, but
`bkt pr list` answers `403 Forbidden`.

### `bkt` says you are not logged in

There is no connection, or it can no longer be used. Check that the Bitbucket card in ⚙ Settings → "Git hosting" says
"Connected".

- When Bitbucket was connected with a username that is not an email address (an old app password), git works but
  `bkt` does not. Reconnect with "**Connect with an app token**"
- While [your own setup](#use-your-own-setup) is in place, the connection is not used

### "Comment the work back" does not appear

The connection is not yet known to have the pull request write permission. Add the permission as in
[Scopes](#scopes) and reconnect Bitbucket. Bitbucket issues are not listed, so only pull requests can be commented on.

## Related

- [Use the GitHub CLI](github-cli.md): the same for GitHub
- [Connect a Git service](../code/connect-git.md): connecting Bitbucket, cloning and pushing
- [Connect an issue tracker](issue-tracker.md): list pull requests in the left rail
- [Commands](../../ref/cli/README.md#clis-that-are-already-signed-in): the CLIs that use a connection
