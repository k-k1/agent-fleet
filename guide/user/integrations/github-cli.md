---
audience: "anyone who wants agents to work with GitHub pull requests"
source_of_truth: "the gh command in a session (Agent Fleet's wrapper) and the Console's Settings > \"Git hosting\""
---

# Use the GitHub CLI

English | [日本語](github-cli.ja.md)

Connect GitHub, and agents can create, read and comment on pull requests and check CI with `gh`
([GitHub CLI](https://cli.github.com/)). Nobody runs `gh auth login` inside a session.

## How it works

| What you do | What the agent does |
|---|---|
| Connect GitHub | Uses `gh` without logging in |
| Ask for what you need | Creates and comments on pull requests as your account |
| Fix the connection when it lacks a permission | Says so and stops when a permission is missing |

`gh` reads your saved connection on every call. A token that expires is renewed on use, the same way git does it.
Agents already know how to use it, so there is no command to teach them.

## Before you start

Connect GitHub (github.com) in **⚙ Settings → "Git hosting"**. It is the same connection git uses to clone and push;
the steps are in [Connect a Git service](../code/connect-git.md#connect-github).

- GitHub Enterprise cannot be connected from Settings. You [set it up yourself](#use-your-own-setup)

## Ask an agent

Just ask the way you normally would.

> Open a pull request for this change, with the description in English

> Read the review comments on PR 123, fix them and push

> Find out why CI on main is failing

Agents can do things like these:

| What | Example |
|---|---|
| Create a pull request | `gh pr create` |
| List and read pull requests | `gh pr list`, `gh pr view 123` |
| Read comments | `gh pr view 123 --comments` |
| Check CI | `gh run list`, `gh pr checks 123` |
| Call any other API | `gh api …` |

- **What they write is recorded under your name.** They can do only what your account and the connection allow

## Fix a pull request's description

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

## Use your own setup

When you have set something up yourself, `gh` uses that instead.

| When you have | What happens |
|---|---|
| Set `GH_TOKEN` or `GITHUB_TOKEN` | `gh` uses that token, not the connection |
| Run `gh auth login` for a GitHub Enterprise host | The account you logged in with is used for that host |

- A `gh auth login` to github.com does not win: while GitHub is connected, `gh` uses the connection's token

## When it goes wrong

### `gh` says you are not logged in

There is no connection, or it can no longer be used. Check that the GitHub card in ⚙ Settings → "Git hosting" says
"Connected".

- When the card says "GitHub could not renew this connection: …", disconnect and connect again
- While `GH_TOKEN` or `GITHUB_TOKEN` is set, the connection is not used

## Related

- [Use the Bitbucket CLI](bitbucket-cli.md): the same for Bitbucket Cloud
- [Connect a Git service](../code/connect-git.md): connecting GitHub, cloning and pushing
- [Connect an issue tracker](issue-tracker.md): list pull requests and issues in the left rail
- [Commands](../../ref/cli/README.md#clis-that-are-already-signed-in): the CLIs that use a connection
