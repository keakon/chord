# Headless

`chord headless` is Chord's lightweight control-plane entry point, suitable for bot, gateway, or automation-script integration.

## What it is

- No TUI
- Interacts over stdio
- Input is JSON commands
- Output is JSONL events

It is suitable for outer-layer integration, but it does not provide a browser frontend, multi-tenant isolation, or complete permission hosting.

## Start

```bash
chord headless
# or
go run ./cmd/chord/ headless
```

## Basic protocol

- stdin: one JSON command per line
- stdout: one JSON envelope per line

Common commands:

- `subscribe`: subscribe to event types
- `status`: get the current status snapshot
- `send`: send a user message
- `confirm`: approve or reject a confirmation request
- `question`: answer a question
- `cancel`: cancel the current turn

Example — send a user message:

```json
{"type":"send","content":"Please summarize the project structure."}
```

## Typical events

- `ready`: headless has started
- `activity`: the agent entered a new phase
- `assistant_message`: assistant message is complete and safe to consume
- `confirm_request`: user confirmation is required
- `question_request`: user input is required
- `idle`: the agent is ready to receive input again
- `error`: runtime error

Example — assistant message event:

```json
{"type":"assistant_message","payload":{"agent_id":"main","text":"The project has three main modules: ...","tool_calls":null}}
```

## Suitable usage

- Let the outer gateway manage process lifecycle
- Let the outer system decide which events to show to end users
- Enforce working-directory, permission, audit, and tenant-isolation controls in the outer layer

## Not a replacement for

`chord headless` is not:

- a browser application
- a multi-tenant security boundary
- a complete permission sandbox

## Related

- [Usage](./usage.md)
- [Permissions & Safety](./permissions-and-safety.md)
- [Troubleshooting](./troubleshooting.md)
