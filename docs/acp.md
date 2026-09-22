# ACP Agent Mode

`chord acp` serves the [Agent Client Protocol](https://agentclientprotocol.com/) (ACP) over stdio, so any ACP client can drive Chord as its agent — editors like Zed, JetBrains IDEs, and Neovim, or the `acpx` CLI. The client sends `initialize`, `session/new`, `session/prompt`, `session/cancel`, and `session/close`; Chord streams the answer, its thinking blocks, and every tool call back as `session/update` notifications.

stdout carries JSON-RPC only. Chord writes its own logs to the [logs directory](./paths.md): the `chord acp` frontend writes `chord-acp-mux-<pid>.log`, and each session's process writes `chord-acp-<session-id>.log`. A stray print from any library is redirected there too, so the protocol stream stays clean.

## Running

```bash
chord acp
```

The client starts the process with no arguments, so it works wherever the client can run the binary: the working directory, model, permissions, and MCP servers all come from Chord's own configuration. `chord acp` takes one optional flag, `--max-sessions` (default 8), covered below.

`session/new` carries the working directory for the session, and that directory is what Chord uses — the directory the client happened to start the process in does not matter. Chord resolves its project config, session files, and tool paths against it, exactly like starting the TUI there.

One `chord acp` process serves every session the client opens. The client sends `initialize` once, then one `session/new` per session, each with the working directory that session is rooted at; Chord starts a child process per session, so the working directory, model client, session storage, and MCP servers of one session stay separate from every other. There is no `--continue` or `--resume` in this mode. The `session/new` response carries `_meta.chord.sessionId`, the name of the Chord session directory created for it — that is the id `chord resume` takes and the one to quote in a bug report.

Each open session is a whole Chord runtime with its own MCP server processes, so memory grows with the number of open sessions. `--max-sessions` (default 8) caps how many can be open at once and rejects a `session/new` past the limit. Closing a thread in the client ends that session's process, and the slot it held is released once that process has exited; closing the connection ends all of them.

## Configuring a client

The client launches `chord acp`, so setup is always the same two values: the path to the binary and the `acp` argument. Where they go depends on the client.

Zed is the worked example. Open the External Agents page (`agent: open settings`) and pick `Add Agent` → `Add Custom Agent`, or add the entry to the settings file yourself:

```json
{
  "agent_servers": {
    "Chord": {
      "type": "custom",
      "command": "/absolute/path/to/chord",
      "args": ["acp"]
    }
  }
}
```

`command` must be an absolute path: Zed does not always inherit your shell `PATH`. Afterwards pick Chord in the agent panel, and use `dev: open acp logs` if a session does not come up.

JetBrains IDEs read the same `agent_servers` entry from `~/.jetbrains/acp.json`. The [ACP client list](https://agentclientprotocol.com/get-started/clients) covers the rest.

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
- ACP questions, modes, and session config options are not offered; the `question` tool fails immediately over ACP instead of waiting on an answer no client can see. `session/list`, `session/resume`, and `session/load` are not implemented, and Chord does not resume an ACP session across process restarts; `session/close` is implemented, but a closed session cannot be reopened.
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
