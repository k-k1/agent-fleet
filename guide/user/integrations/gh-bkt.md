---
audience: "anyone who wants agents to work with GitHub or Bitbucket pull requests"
source_of_truth: "the gh and bkt commands in a session (Agent Fleet's wrappers) and the Console's Settings > \"Git hosting\""
---

# Use the GitHub and Bitbucket CLIs

English | [日本語](gh-bkt.ja.md)

Connect GitHub or Bitbucket Cloud, and agents can create, read and comment on pull requests and check CI with `gh` or
`bkt`. Nobody runs `gh auth login` or `bkt auth login` inside a session.

## How it works

| What you do | What the agent does |
|---|---|
| Connect GitHub or Bitbucket | Uses `gh` (GitHub) or `bkt` (Bitbucket Cloud) without logging in |
| Ask for what you need | Creates and comments on pull requests as your account |
| Fix the connection when it lacks a permission | Says so and stops when a permission is missing |

`gh` and `bkt` read your saved connection on every call. A token that expires is renewed on use, the same way git does
it. Agents already know how to use both, so there is no command to teach them.

## Before you start

Connect GitHub or Bitbucket in **⚙ Settings → "Git hosting"**. It is the same connection git uses to clone and push;
the steps are in [Repositories and git](../../member/06-code.md#connect-a-git-provider).

| Connection | Command it signs in |
|---|---|
| GitHub (github.com) | `gh` |
| Bitbucket Cloud (bitbucket.org) | `bkt` |

- GitHub Enterprise and Bitbucket Data Center cannot be connected from Settings. You [set them up yourself](#use-your-own-setup)
- When you connect Bitbucket with "**Connect with an app token**", give the token the scopes `bkt` needs as well ([Bitbucket scopes](#bitbucket-scopes))

## Ask an agent

Just ask the way you normally would.

> Open a pull request for this change, with the description in English

> Read the review comments on PR 123, fix them and push

> Find out why the pipeline on main is failing

Agents can do things like these:

| What | GitHub | Bitbucket Cloud |
|---|---|---|
| Create a pull request | `gh pr create` | `bkt pr create` |
| List and read pull requests | `gh pr list`, `gh pr view 123` | `bkt pr list`, `bkt pr view 123` |
| Read comments | `gh pr view 123 --comments` | `bkt pr comments 123` |
| Check CI and pipelines | `gh run list`, `gh pr checks 123` | `bkt pipeline` |
| Call any other API | `gh api …` | — |

- **What they write is recorded under your name.** They can do only what your account and the connection allow
- **For Bitbucket, agents use `bkt` rather than an MCP server.** They are told to. A CLI reads the connection on every
  call, so an expired login never means restarting the session

## Fix a GitHub pull request's description

When the tenant connects through the built-in GitHub OAuth App, the token's scopes are only `repo` and `workflow`. With
that token `gh pr edit` can fail, even on your own personal repository:

```text
GraphQL: … The 'login' field requires one of the following scopes: ['read:org']
```

Agents are told to edit over the REST API in that case. Do the same when you edit it yourself:

```sh
gh api -X PATCH repos/<owner>/<repo>/pulls/<N> -F body=@<file>
gh api -X PATCH repos/<owner>/<repo>/pulls/<N> -f title="New title"
```

- The link in the error (the token settings page) does not apply to this token
- An access token you pasted yourself with `read:org` does not hit this error
- `gh pr edit` can also fail for other reasons, such as a team reviewer you cannot see

## Bitbucket scopes

`bkt` can do only what the connection is allowed to. The permissions the connection itself needs (Account: Read and
Repositories: Read/Write for OAuth; `read:account`, `read:workspace:bitbucket` and `read:repository:bitbucket` for an
API token) are not enough for pull requests or pipelines.

| What | Connected with OAuth | Connected with an API token |
|---|---|---|
| Pull requests (create, comment) | Pull requests: Read and Write | `read:pullrequest:bitbucket`, `write:pullrequest:bitbucket` |
| See pipelines | Pipelines: Read | `read:pipeline:bitbucket` |
| Run or stop a pipeline | Pipelines: Write | `write:pipeline:bitbucket` |

- **OAuth permissions are set by a tenant administrator** on the Bitbucket OAuth consumer. Once they are added,
  reconnect Bitbucket to get them
- **API token scopes are chosen when the token is created.** Write does not include read. When one is missing, paste a
  new token that has it
- The same permission lets you use "**Comment the work back**" on a Bitbucket pull request in the issue tracker's
  detail. The button appears only when the connection is known to have the pull request write permission

## Use your own setup

When you have set something up yourself, `gh` and `bkt` use that instead.

| When you have | What happens |
|---|---|
| Set `GH_TOKEN` or `GITHUB_TOKEN` | `gh` uses that token, not the connection |
| Run `gh auth login` for a GitHub Enterprise host | Your login is used for that host |
| Run `bkt auth login`, or set `BKT_HOST` and `BKT_TOKEN` | `bkt` follows your setup and does not use the connection |

- A `gh auth login` to github.com does not win: while GitHub is connected, `gh` uses the connection's token
- A `bkt` setup applies to bitbucket.org too, even if you meant it only for a Data Center host. To use the connection
  on bitbucket.org, delete the config file (`~/.config/bkt/config.yml`)

## When it goes wrong

### `bkt` says "lacks a scope for this command"

The connection does not have the permission for that command. The message names the permission that is missing.

- **An OAuth connection**: have a tenant administrator add the permission, then reconnect Bitbucket in ⚙ Settings → "Git hosting"
- **An API token connection**: create a new token with the scope, and paste it

Retrying with another credential will not help. A token without the pull request scopes can list repositories, but
`bkt pr list` answers `403 Forbidden`.

### `gh` or `bkt` says you are not logged in

There is no connection, or it can no longer be used. Check that the card in ⚙ Settings → "Git hosting" says "Connected".

- When the GitHub card says "GitHub could not renew this connection: …", disconnect and connect again
- When Bitbucket was connected with a username that is not an email address (an old app password), git works but
  `bkt` does not. Reconnect with "**Connect with an app token**"
- While [your own setup](#use-your-own-setup) is in place, the connection is not used

### "Comment the work back" does not appear for Bitbucket

The connection is not yet known to have the pull request write permission. Add the permission as in
[Bitbucket scopes](#bitbucket-scopes) and reconnect Bitbucket. Bitbucket issues are not listed, so only pull requests
can be commented on.

## Related

- [Repositories and git](../../member/06-code.md): connecting GitHub and Bitbucket, cloning and pushing
- [Connect an issue tracker](issue-tracker.md): list pull requests and issues in the left rail
- [Commands](../../ref/cli/README.md#clis-that-are-already-signed-in): the CLIs that use a connection
