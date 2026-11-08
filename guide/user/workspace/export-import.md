---
audience: "People who want to take their settings to another environment or account"
source_of_truth: "The Console's Settings > Export / import"
---

# Export and import your settings

English | [日本語](export-import.ja.md)

Put your settings into one file, and import it into another Agent Fleet or another account.

## How it works

| What you do | What happens |
|---|---|
| Press **Export** | The settings you picked download as `af-settings-<date>-<time>.json` |
| Press **Import** where you moved to and pick the file | After you review it, it is added to the settings there |
| Log in again where you moved to | The connections the file does not carry are connected again |

## What the file carries, and what it does not

| What it carries | Contents |
|---|---|
| Personal settings | Display, keyboard, notifications, agent defaults and so on |
| AWS profiles/SSM | Profiles and SSM hosts |
| Google Cloud profiles | Profiles |
| Agent instructions | The instructions' text. Exported only while the workspace is running |

**Connections are not in it.** Git, agent and AWS tokens and API keys, and Google logins: log in again where you moved to.
With no secrets in it, the file can be handed to other people, for example to give a team its set of SSM hosts.

## Export

1. Open **⚙ Settings → Export / import**
2. Under **Export**, tick what to include. Each shows how many items it holds
3. Press **Export**, and the file downloads

## Import

1. Where you moved to, open **Import** on the same tab
2. Pick the exported file. Its export time and contents appear
3. Tick what to import, press **Import**, and confirm

An import **only adds**. It never deletes the settings you have.

| Kind | How it is imported |
|---|---|
| Personal settings | Laid over your current settings. Things that accumulate, such as learned reply suggestions and key bindings, are not cleared even when the file has none |
| AWS profiles | A profile with the same name is left as it is |
| SSM hosts | The same host (alias and instance) is left as it is |
| AWS profiles that assume a role | Imported only when the source profile, with the same label and the same sign-in, already exists or is in the same file |
| Google Cloud profiles | A profile with the same label (case-insensitive) is left as it is. One without a label or a project is skipped |
| Agent instructions | **Replace the text you have now**. You are asked to confirm first |

The result is reported as counts per kind, with the items already there and the ones that could not be registered on
the same line.

## Move from your own computer's settings

When you move from your own computer rather than another Agent Fleet, each tab reads the file directly.

| Your file | Where to read it |
|---|---|
| `~/.aws/config` | The AWS profiles/SSM tab (**Import from ~/.aws/config** on this tab opens it too). [Use AWS SSO](../integrations/aws-sso.md#import-from-your-own-awsconfig) |
| `~/.config/gcloud/configurations/config_<name>` | The Google Cloud tab. [Use Google Cloud](../integrations/gcloud.md#import-from-your-own-gcloud-configurations) |

## When it goes wrong

### "AWS profiles/SSM: the current profiles could not be read, so nothing was added"

Your current AWS profiles could not be read, so nothing AWS was added: carrying on without them would duplicate every
one. Try again.

### "This is not a settings export file" or "This file's format version is unknown to this Console"

The file was not made by Export, or it was exported by a newer Agent Fleet. Import it into an Agent Fleet of the same
version as the one that exported it, or newer.

### The agent instructions cannot be exported

When it says **Only while the workspace is running**, start the workspace and export again.

## Related

- [Use AWS SSO](../integrations/aws-sso.md)
- [Use Google Cloud](../integrations/gcloud.md)
- [Log in to EC2 with SSM](../integrations/aws-ssm.md)
