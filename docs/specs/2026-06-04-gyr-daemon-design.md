# gyr: personal ops/overseer daemon (v1 design, 2026-06-04)

Staging note: this spec lives in the jess repo until gyr is scaffolded; it then moves to `gyr/docs/specs/`. gyr depends on jess (the harness) and jess/ledger (provenance).

## What gyr is

A personal ops agent that runs as a launchd daemon on the Mac. Send it a Telegram message, it acts: restart a service, set a reminder, run a small command. Dangerous actions ask for a one-tap confirm on your phone first. Everything it does is recorded in the provenance ledger. Headless, no web UI. Named in the falconry scheme (the gyrfalcon, the overseer that circles and watches, stoops only when warranted).

v1 is the REACTIVE daemon: you drive it, it responds. The ambient/proactive layer (triggers beyond Telegram, the surfacing threshold, pattern-mining) is v2, designed elsewhere and explicitly out of scope here.

## Repo and scaffold

New `gyr` repo, scaffolded from rookery with `--no-web` (headless). That yields two binaries, `gyrd` (daemon) and `gyrctl` (local CLI), on the perch lib, with TOML config, launchd targets, loopback token auth between gyrctl and gyrd, a `make check` gate, and CI. gyr adds `github.com/guygrigsby/jess` and uses `jess/ledger` as dependencies. Scaffolding (clone rookery, run `scripts/init.sh gyr --no-web`) is the first implementation task.

## Process shape

`gyrd` on start:
1. Load config (Telegram bot token, your chat-id allowlist, model/provider/key, ledger + reminders paths, confirm timeout).
2. Open the durable SQLite ledger (`ledger.OpenSQLite`).
3. Build one jess agent: `jess.New(WithModel(cloud Claude), WithTools(ops tools), WithApprover(telegramConfirm), WithLedger(ledger), WithAgentID("gyr"))`.
4. Start the Telegram long-poll loop and the reminder scheduler.

One agent, one ledger, for the single user. jess's one-active-run-per-agent invariant means gyr handles one request at a time (see Concurrency).

## Telegram channel

Long-polling (`getUpdates` in a loop). No public endpoint, no inbound port, works behind NAT. Use a maintained Go Telegram library (`github.com/go-telegram/bot`, actively maintained, zero deps) rather than hand-rolling the API.

- **Allowlist is the perimeter.** Only updates whose chat-id is in the configured allowlist are processed; everything else is ignored and logged. gyr has shell access, so this is the security boundary that keeps anyone but you from driving it. (Same posture as the iMessage channel's allowlist.)
- A **text message** from an allowed chat is a new request: `jess.Stream(ctx, agent, text)`, stream the events, send the assistant's final reply back to that chat.
- A **callback query** (inline-button tap) is an answer to a parked confirm: route it to the confirm registry (below).
- A **`stop`** message aborts the active run (`agent.Abort()` + cancel its context). The reachable kill switch.

## The async approver bridge (the core mechanism)

jess's `Approver` is synchronous: `func(ctx, gate.Request) (allow bool, reason string)`, and it blocks the tool call until it returns. A Telegram confirm is asynchronous (send, await a tap). gyr bridges them:

- A `confirms` registry: `map[string]chan decision`, mutex-guarded.
- The approver, when called for a non-safe tool:
  1. Mint a confirm id.
  2. Send a Telegram message to you showing the exact action (the gate `Request.Preview` / `Args`, e.g. `launchctl kickstart -k system/nginx`) with an inline keyboard: `[Approve]` `[Deny]`, the callback data carrying the confirm id.
  3. Register a channel under the id.
  4. Block on the channel, bounded by a configurable timeout and by `ctx`. Timeout or context cancel => deny (fail-closed).
  5. Return the decision.
- The callback handler matches a tap's confirm id to the registered channel, sends the decision, answers the callback, and edits the message to "approved" / "denied" so the buttons can't be tapped twice.

While a confirm is pending the agent run is parked (the tool call is blocking). That is intended: gyr is doing one thing and waiting on you.

## Tools

Narrow and specific, per the security tenet (specific tools beat a gated firehose). Each is a jess/agentcore tool; safe ones implement `gate.SafeTool` (run without a confirm), non-safe ones are confirmed and durably recorded.

- `service_status(name)` — SafeTool. Read-only `launchctl` status.
- `restart_service(name)`, `stop_service(name)` — non-safe. `launchctl` mutations. Always confirmed.
- `set_reminder(text, when)`, `list_reminders()`, `cancel_reminder(id)` — SafeTool. Scheduling a future ping is not dangerous; no confirm.
- `bash(command)` — non-safe. The escape hatch. **Every bash call is confirmed; there is no command classifier in the trust path.** Classifying arbitrary shell for danger is unreliable, and a false negative (auto-allowing something unrecognized) is the catastrophic case for a shell-capable daemon reachable from a phone. Unknown equals confirm. The confirm cost is the forcing function to add specific tools and stop reaching for bash. `gate.Dangerous` may decorate the confirm message ("this looks destructive") but never reduces the confirmation requirement.

## Reminders

A small SQLite table in gyr's own db (separate from the ledger), rows `{id, text, due_at, chat_id, created_at, fired_at}`. `set_reminder` inserts, `list`/`cancel` query/delete. A scheduler goroutine sleeps until the next due reminder (a timer over the earliest `due_at`, re-armed on insert/cancel), fires due reminders as Telegram messages, and marks them fired. On boot it loads unfired reminders and re-arms, so reminders survive restart.

## Config (TOML)

```
[telegram]
token = "..."                 # bot token (or read from env / 1Password per the secrets pattern)
allowed_chat_ids = [123456]   # the perimeter

[model]
provider = "anthropic"        # cloud Claude via litellm-backed model
model    = "claude-..."
api_key  = "..."              # or env

[paths]
ledger    = "~/.local/share/gyr/ledger.db"
reminders = "~/.local/share/gyr/reminders.db"

[confirm]
timeout = "2m"                # no tap by then => deny
```

Secrets (token, api_key) should resolve from env / the 1Password cache rather than living in the file where practical.

## gyrctl

The local window, over perch's loopback token auth to gyrd:
- `gyrctl status` — daemon up, current run (if any), counts.
- `gyrctl reminders` — list pending reminders.
- `gyrctl why <run-id>` — read a chain back from the ledger and print the triad: the request, what was available, the actions taken with their gate verdicts. This is the payoff of the ledger: answering "why did you restart nginx" locally, offline.

## Security boundaries (the overseer has shell access)

- **Allowlist** on the Telegram channel: only you can drive it.
- **Fail-closed gate**: non-safe tools require a confirm; no approver or a timeout means deny.
- **No record, no action**: inherited from jess/ledger. The durable SQLite ledger backs the agent; a non-durable ledger would deny every non-safe action.
- **Abort**: `stop` over Telegram cancels the active run.
- **Bash is always-confirm**: no classifier can silently allow it.

## Concurrency and errors

- **One run at a time.** A request arriving while a run is active (including while a confirm is parked) gets a "busy, finish the current one" reply rather than starting a second run (jess enforces one active run per agent; `Stream`'s begin fails otherwise).
- **Telegram API errors**: retry the long-poll with backoff; never crash the daemon.
- **Agent/tool errors**: report back to the chat ("failed: <err>"), don't crash.
- **Ledger write failures**: handled by jess (action denied if not durable; observation best-effort).

## Model

Cloud Claude via a litellm-backed `agentcore.ChatModel`. Note: the jess simplify refactor removed `jess.LiteLLM`; the exact agentcore cloud-model constructor is confirmed at plan time. `WithModel` takes whatever `agentcore.ChatModel` that constructor returns. Configurable to a local model later.

## Testing

- Approver bridge: register a confirm, signal it from a fake callback, assert allow; let it time out, assert deny (fail-closed). Fake Telegram sender.
- Allowlist: a non-allowed chat-id update is ignored.
- Tools: `service_status`/`restart_service` against a faked `launchctl` runner; reminders set/list/cancel; bash runs and is recorded.
- Reminder scheduler: fires at due time (fake clock), survives a simulated restart (reload + re-arm).
- One-run-at-a-time: a second request during an active run gets the busy reply.
- Integration: a fake Telegram update drives a fake model that calls `restart_service`; assert a confirm is sent, a tap approves, the action runs, and the ledger chain reconstructs the why.
- The whole thing through rookery's `make check`.

## Deferred to v2 (separate spec)

The ambient/proactive layer: triggers beyond Telegram (watching logs, time, learned patterns), the surfacing threshold (act silently on the reversible, surface only the notable or the irreversible), and pattern-mining over the provenance ledger. v1 builds the reactive spine and the accountability the ambient layer will stand on.
