---
audience: "the deployment administrator who lets members get push notifications on phones"
source_of_truth: "the Control Plane's Web Push service (`control-plane/internal/pushsrv`, `control-plane/internal/webpush`) and its environment variables"
---

# Switch on Web Push

English | [日本語](web-push.ja.md)

Web Push delivers a notification to a member's phone or computer while the Console is closed, when a session asks a question, needs an approval or finishes a turn.
It is off by default. You switch it on with environment variables on the Control Plane, and open outbound HTTPS to the browsers' push services.

## How it works

| What you do | What happens |
|---|---|
| Set `AF_WEB_PUSH=true`, a VAPID private key and a contact, then restart | The Control Plane starts the sender and the subscription API. Members see "Push to this device" under Settings → "Notifications" |
| Leave `AF_WEB_PUSH` unset | Nothing changes: no sender, no routes, and the section stays hidden in the Console |
| Allow the push services' hosts on the Control Plane's outbound path | Pushes reach the devices |

The key pair belongs to this deployment. There is no relay: the Control Plane signs each push itself and sends it to the push service the browser named (Google, Mozilla, Apple or Microsoft).

## Set it up

1. Make a private key: 32 random bytes as unpadded base64url. For example

   ```
   openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n'
   ```

2. Put the variables in the Control Plane's environment (for compose, in `.env`) and restart it

   | Variable | Meaning |
   |---|---|
   | `AF_WEB_PUSH=true` | Turns Web Push on. Anything else leaves it off |
   | `AF_WEB_PUSH_VAPID_PRIVATE_KEY` | The key from step 1. Required. Keep it secret like the other deployment secrets; the public half is derived from it |
   | `AF_WEB_PUSH_SUBJECT` | A contact for the push services, `mailto:you@example.com` or an `https://` URL. Required. A value in any other form leaves Web Push off, with a warning in the log |
   | `AF_WEB_PUSH_ENDPOINT_HOSTS` | Optional. Extra push-service hosts to accept, comma-separated. A leading dot means any subdomain (`.push.example.com`) |
   | `AF_NOTIFICATION_DRAIN_INTERVAL` | Optional, default `15s`. How often the Control Plane collects each running workspace's notifications, so a question reaches the phone while no Console is open. It runs only while Web Push is on |

3. Check the log for `web push enabled`, then open Settings → "Notifications" as a member and turn push on for a device ([Get notified](../../user/settings/notifications.md#get-push-notifications-on-your-phone))

If `AF_WEB_PUSH=true` is set but the key or the contact is missing or malformed, the Control Plane logs a warning and runs with Web Push off. It does not stop.

## Open the egress

The Control Plane POSTs each push to the endpoint the member's browser registered. Allow outbound HTTPS (port 443) from the Control Plane to these hosts:

| Push service | Host |
|---|---|
| Chrome, Edge and other Chromium browsers, Android | `fcm.googleapis.com` |
| Firefox | `updates.push.services.mozilla.com` and `*.push.services.mozilla.com` |
| Safari, iPhone and iPad home-screen apps | `*.push.apple.com` |
| Windows notification service | `*.notify.windows.com` |

- A subscription whose endpoint is on any other host is refused when it is registered, so a member cannot aim the Control Plane at an internal address. Add a host with `AF_WEB_PUSH_ENDPOINT_HOSTS` and open the same host in your firewall or proxy
- Admin → Egress governs what workspaces reach, not the Control Plane's own outbound traffic. Open these hosts in the network the Control Plane runs in
- Members' browsers also need to reach the push services, which they do on their own. Nothing is opened inbound

## What is pushed

- Only four kinds are pushed: a question, a plan waiting for approval, a permission request and a finished turn. Everything else stays in the notification center
- The payload carries the session name and the kind. The text of a question is added only when a tenant administrator allows it under the tenant's "Limits & idle" settings, and it is cut short. The setting can be prepared before Web Push is switched on
- A session on screen in another Console of the same member is not pushed, and members can switch each kind off. Members who are no longer active are never pushed
- A push the service cannot deliver within an hour is dropped. One member can register up to 20 devices

## When something goes wrong

| What you see | Cause and fix |
|---|---|
| The section is missing in Settings | Web Push is off or its settings are invalid. Check `AF_WEB_PUSH`, the key and the contact, and the log line at start |
| Devices register but nothing arrives | Outbound HTTPS to the push hosts is blocked. Test from the Control Plane's network. On an iPhone the Console must be opened from the Home Screen icon |
| Every device says it was set up before the push key changed | The private key was replaced. Every subscription is invalid by design: each member turns push off and on again on each device. Keep the key stable |
| A device registered on an unusual host is skipped | Add the host to `AF_WEB_PUSH_ENDPOINT_HOSTS`. The subscription is kept, not deleted, so it works once the host is allowed |

## Related

- [Get notified](../../user/settings/notifications.md): what members set
- [Use it on a phone](../../user/start/phone.md): adding the Console to the Home Screen
- [Control outbound traffic](../secure/egress.md): the workspaces' egress, which is separate from this
