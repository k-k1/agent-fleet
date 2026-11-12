---
audience: "a deployment administrator (super_admin) who provides and runs the read-aloud speech engine"
source_of_truth: "the Admin modal's Read aloud (`console/src/features/settings/admin/adminTts.tsx`) and the Control Plane's speech code (`control-plane/tts*.go`)"
---

# Run the read-aloud engine

English | [日本語](read-aloud.ja.md)

Provide and run the speech engine (VOICEVOX's Zundamon) that members hear replies read aloud with.
The reading dictionary that applies to every member is kept on the same screen.

## How it works

| What you do | What happens |
|---|---|
| Provide a VOICEVOX engine the Control Plane can reach | Japanese read-aloud speaks in Zundamon's voice |
| Set up Amazon Polly | Polly reads in its place while the engine is stopped or getting ready |
| Choose the engine's mode under **Read aloud** | The engine starts and stops. On demand, it follows the demand by itself |
| Write readings into the **Tenant-wide reading dictionary** | Every member's read-aloud says that spelling with that reading |

Whether to use read-aloud, and which voice, each member decides ([Use read-aloud](../../user/settings/read-aloud.md)).
A speech engine cannot be borrowed from another deployment.

## Provide the engine

The VOICEVOX engine is not in the package. Provide it for your deployment target.

| Target | What to provide |
|---|---|
| AWS ECS | The speech engine stack (`50-tts`). It normally runs at zero and is started and stopped from this screen ([ECS runbook](../../../deploy/aws/ecs/README.md#optional-japanese-speech-the-voicevox-engine-cfn50-ttsyaml)) |
| native, WSL | Run VOICEVOX on Windows, or VOICEVOX ENGINE on Linux ([native runbook](../../../deploy/native/README.md#text-to-speech-tts--zundamon--optional)) |
| Others | Run a VOICEVOX ENGINE the Control Plane can reach over HTTP |

- The Control Plane's `AF_VOICEVOX_URL` says where the engine is. The default is `http://127.0.0.1:50021`
- For Polly, set `AF_POLLY_REGION` and AWS credentials the Control Plane's process can see
- While the engine is down, only speech fails. When it is back, read-aloud recovers from the next sentence

## Choose the engine's mode

Open **Admin** from the account menu and choose **Read aloud** under **Deployment**. Only a deployment administrator (super_admin) can open it.

![The Read aloud screen. Of VOICEVOX engine (Zundamon)'s Disabled, On demand and Always on, On demand is selected, and it reads Engine: Running (ECS-managed) / Polly: Available. Below is the Tenant-wide reading dictionary field with three readings written as spelling=reading](../../assets/operate-gpu-tts-en-light.webp)

| Mode | What happens |
|---|---|
| **Disabled** | Read-aloud switches to Polly at once. On AWS, the ECS desired count goes to 0 about a minute later |
| **On demand** | The engine starts by itself once demand builds up (2,000 characters in 5 minutes), and stops by itself after 30 minutes with nobody listening |
| **Always on** | The engine is never stopped |

- Starting takes 1–2 minutes. Until it is ready, Polly reads Japanese in its place. Without Polly there is no sound in the meantime
- Every automatic start and stop is written to the audit log
- The **Engine:** line shows the engine's state, whether it is ECS-managed or externally managed, and whether Polly is available

For an engine ECS does not manage (a standalone VOICEVOX, say), the screen changes as follows.

| Engine | Screen |
|---|---|
| An externally managed engine is reachable | There is no On demand. The switch only opens and closes the route; the engine itself is never started or stopped |
| There is no engine | There is nothing to route to, so the switch stays at Disabled. Once an engine is provided it can be used |

## Build the pronunciation dictionary

Write the readings of product names and in-house terms into the **Tenant-wide reading dictionary** and press **Save**.

```
# Product names
agent-fleet=エージェントフリート
Webshop=ウェブショップ
```

- One `spelling=reading` per line. A line starting with `#` is a comment
- It applies to every member's read-aloud. When a member's own dictionary has the same spelling, the member's wins
- Other members get a saved change from their next Console load

Tenant administrators and members may ask you to add words that keep being misread.

## When it goes wrong

| Symptom | Cause and fix |
|---|---|
| The voice is Polly, not Zundamon | The engine is starting or stopped. On demand, Polly reads until demand builds up and the engine starts |
| There is no sound at all | The engine is stopped and Polly is not set up. Provide one of them |
| The switch is stuck at Disabled | No engine is reachable from the Control Plane. Run one at `AF_VOICEVOX_URL` |
| A dictionary change does not change the reading | Other members get it from their next Console load. A member's own dictionary with the same spelling wins |

## Related

- [Use read-aloud](../../user/settings/read-aloud.md): the member's setting
- [Run inference engines on your own GPUs](gpu.md)
- [Read the audit log](../../admin/audit.md)
