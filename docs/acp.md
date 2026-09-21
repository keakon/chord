# ACP Agent Mode

`chord acp` serves the [Agent Client Protocol](https://agentclientprotocol.com/) (ACP) over stdio, so an ACP client can drive Chord as its agent — Zed does this when you add Chord as a custom agent server. The client sends `initialize`, `session/new`, `session/prompt`, and `session/cancel`; Chord streams the answer, its thinking blocks, and every tool call back as `session/update` notifications.

stdout carries JSON-RPC only. Chord writes its own logs to `chord.log` in the [logs directory](./paths.md), and a stray print from any library is redirected there too, so the protocol stream stays clean.

## Running

```bash
chord acp
```

The client starts the process, so it works wherever the client can run the binary. There are no flags: the working directory, model, permissions, and MCP servers all come from Chord's own configuration.

`session/new` carries the working directory for the session, and that directory is what Chord uses — the directory the client happened to start the process in does not matter. Chord resolves its project config, session files, and tool paths against it, exactly like starting the TUI there.

One process serves one session. Every `chord acp` start begins a fresh session in the working directory the client asks for; there is no `--continue` or `--resume` in this mode. The `session/new` response carries `_meta.chord.sessionId`, the name of the Chord session directory created for it — that is the id `chord resume` takes and the one to quote in a bug report.

## Configuring Zed

Add Chord as a custom agent server in Zed's settings (`dev: open settings`):

```json
{
  "agent_servers": {
    "Chord": {
      "command": "/absolute/path/to/chord",
      "args": ["acp"]
    }
  }
}
```

`command` must be an absolute path, since Zed does not inherit your shell `PATH`. Afterwards pick Chord in the agent panel, and use `dev: open acp logs` if a session does not come up.

## What the client sees

- Answer text arrives as it is generated, so the client renders it progressively instead of waiting for the turn to end. If the model retries mid-stream, text already shown stays on screen: ACP v1 cannot retract a chunk, and the retried answer is appended after it.
- Thinking blocks stream while the model reasons; a block that was never streamed is delivered once, in full.
- Tool calls arrive with a category (`read`, `edit`, `search`, `execute`, and so on), a title naming the file or command, the target file location, and the model's raw arguments. Each call then closes as completed or failed, with the tool's output and, for file edits, the diff.
- `@`-style file references work: when a client sends a `file://` resource link for a readable local file, Chord loads it as a `<file path="...">` context block, the same shape the TUI's file references produce.
- Image attachments work when the model accepts images; Chord persists them with the session like any other attachment.
- Cancelling in the client aborts the turn and answers with `cancelled`, after the tool cards are closed out.
- One turn runs at a time. A prompt that arrives while a turn is running cancels that turn: the earlier prompt answers `cancelled`, and the new one starts right after. Zed queues messages while the agent is generating, so this only shows up with clients that send during a turn.

## Current limits

- **Confirmation prompts are not bridged yet.** When a tool needs your permission, Chord waits on its own confirmation timeout instead of asking the client, and the tool then fails as an unconfirmed call. Until ACP permission requests are wired up, prompts that only need reads work best, or set a permission rule in `config.yaml` so the tools you want to allow never ask.
- ACP questions, modes, and session config options are not offered; the `question` tool fails immediately over ACP instead of waiting on an answer no client can see. `session/list`, `session/resume`, and `session/load` are not implemented, and Chord does not resume an ACP session across process restarts.
- MCP servers and additional directories sent with `session/new` are ignored: Chord takes its MCP servers and workspace roots from its own configuration, and logs what the client asked for.
- Chord does not use the client's filesystem or terminal methods (`fs/read_text_file`, `fs/write_text_file`, `terminal/*`): it reads and writes files, runs `shell`, and talks to MCP servers itself.
- **Delegated sub-agents are not streamed separately.** A worker's own text and thinking are dropped; what reaches the client is the main agent's tool cards for the delegation and the result it returns.
- No authentication method is advertised — ACP does not drive `chord auth`. Configure providers before starting the client.
- ACP v1 only. Chord implements the stable method and update surface, not the unstable extensions.
- **Windows is not supported.** `chord acp` exits with an error there: the stdout guard that keeps JSON-RPC clean cannot be installed without the fd duplication Unix provides, and running without it risks a stray print corrupting the stream.

## Related

- [CLI reference](./cli.md)
- [Headless](./headless.md): the JSON control plane for scripts and gateways
- [Permissions and safety](./permissions-and-safety.md)
