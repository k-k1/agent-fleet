---
audience: "anyone whose clone, push or submodule fetch does not work"
source_of_truth: "the Console's Settings > Git hosting and the clone screen"
---

# Fix a failed clone or Git authentication

English | [日本語](git.ja.md)

Find the cause and the fix when cloning a repository, authenticating to a private repository, or fetching submodules does not work.

## Cloning fails

- **Check the connection.** A private repository needs GitHub or Bitbucket connected first. In ⚙ Settings → Git hosting, see whether that service says **"Not connected"** ([Connect a Git service](../code/connect-git.md))
- **Check the spelling of the URL and the branch name.**
- If submodules fail, the parent clone still succeeds. For submodules, see the section below

## A private repository gives an authentication error

In ⚙ Settings → Git hosting, check that the service is **"Connected"**.

- If it is not, connect with OAuth or a token. Once connected, cloning and pushing work without entering a token
- If it is connected and still fails, read the message on the card. What to do about **"GitHub could not renew this connection: …"**, or a token Bitbucket rejects, is in [Connect a Git service](../code/connect-git.md#when-it-goes-wrong)

## A session says the submodules are missing or broken

A large submodule may not finish fetching before the session starts. The fetch carries on in the background, and a notification arrives when it is done.

- A submodule left half-fetched is repaired the next time a session starts in that working copy
- To fix it right away, run `git submodule update --init --recursive` in that working copy in a terminal

The details are in [Clone a repository](../code/clone.md#when-it-goes-wrong).

## Related

- [Connect a Git service](../code/connect-git.md)
- [Clone a repository](../code/clone.md)
- [Use the built-in Git](../code/internal-git.md)
