# ACP Agent 模式

`chord acp` 通过 stdio 提供 [Agent Client Protocol](https://agentclientprotocol.com/)（ACP），任何 ACP 客户端都能把 Chord 当成自己的 agent 使用：编辑器如 Zed、JetBrains IDE、Neovim，以及 `acpx` 这类命令行客户端。客户端发 `initialize`、`session/new`、`session/prompt`、`session/cancel`，Chord 把回答、思考块和每个工具调用作为 `session/update` 通知流式回传。

stdout 只跑 JSON-RPC。Chord 自己的日志写进[日志目录](./paths_CN.md)下的 `chord.log`，第三方库误写到 stdout 的内容也会被重定向过去，协议流不会被污染。

## 启动

```bash
chord acp
```

进程由客户端拉起，客户端能跑这个二进制就行。没有命令行参数：工作目录、模型、权限、MCP server 全都来自 Chord 自己的配置。

`session/new` 里带的工作目录就是会话目录，Chord 以它为准，客户端在哪个目录启动进程无关紧要。项目配置、会话文件、工具路径都按这个目录解析，和在那个目录启动 TUI 完全一致。

一个进程只服务一个会话。每次 `chord acp` 都在客户端指定的工作目录里新建会话；这个模式没有 `--continue` / `--resume`。`session/new` 的响应里带 `_meta.chord.sessionId`，也就是为它新建的 Chord 会话目录名 —— 要 `chord resume` 或写 bug 报告时用的就是这个 id。

## 配置客户端

进程由客户端拉起，所以配置永远是两个值：二进制的路径，加上 `acp` 参数；放在哪里由客户端决定。

下面用 Zed 举例。打开 External Agents 页面（`agent: open settings`），选 `Add Agent` → `Add Custom Agent`，或者自己往设置文件里加：

```json
{
  "agent_servers": {
    "Chord": {
      "type": "custom",
      "command": "/绝对路径/chord",
      "args": ["acp"]
    }
  }
}
```

`command` 必须写绝对路径：Zed 不一定会继承你 shell 的 `PATH`。配好后在 agent 面板里选中 Chord；起不来的话用 `dev: open acp logs` 看 ACP 日志。

JetBrains IDE 从 `~/.jetbrains/acp.json` 读同一个 `agent_servers` 条目。其余客户端见 [ACP client list](https://agentclientprotocol.com/get-started/clients)。

## 客户端能看到什么

- 回答边生成边到达，客户端可以逐字渲染，不用等整轮结束。模型中途重试时，已经显示的文字会留在屏幕上：ACP v1 撤不回已发出的片段，重试后的回答接在后面。
- 模型思考时思考块会流式推送；没流式过完整块的会一次性补齐。
- 工具调用带分类（`read`、`edit`、`search`、`execute` 等）、标题（文件名或命令）、目标文件与模型原始参数；随后以完成或失败收口，附上工具输出，文件编辑还带 diff。
- `@` 形式的文件引用可用：客户端发来的 `file://` 资源链接指向可读的本地文件时，Chord 按 `<file path="...">` 上下文块加载，与 TUI 文件引用是同一种形态。
- 模型支持图片时图片附件可用；Chord 会和其他附件一样把它随会话落盘。
- 在客户端取消会中断本轮，卡片收口后返回 `cancelled`。
- 同一时刻只跑一轮。一轮还在跑时又收到提示词，会先取消当前这轮：原提示词以 `cancelled` 收口，新的一轮紧接着开始。Zed 在生成期间会自己排队，只有会在一轮中途发提示词的客户端才会碰到这个行为。

## 当前限制

- **确认弹窗还没接过来。** 工具需要你授权时，Chord 等的是自己的确认超时，而不是问客户端，超时后该调用以未确认失败。在 ACP 的权限请求接通之前，只用读操作的提示词最稳，或者在 `config.yaml` 里写权限规则，让想放行的工具不再询问。
- 不提供 ACP 提问、mode 和 session config option；`question` 工具在 ACP 下会直接报错，不会去等一个客户端看不到也无法回答的问题。`session/list`、`session/resume`、`session/load` 都未实现，进程重启后也不会恢复 ACP 会话。
- `session/new` 里带的 MCP server 和额外目录会被忽略：Chord 只用自己的配置决定 MCP server 和工作区根目录，客户端发来的内容会记进日志。
- 不使用客户端的文件与终端方法（`fs/read_text_file`、`fs/write_text_file`、`terminal/*`）：读写文件、跑 `shell`、连 MCP server 都由 Chord 自己做。
- **委派出去的子代理不会单独流式呈现。** worker 自己的正文与思考块不予转发，客户端看到的是主 agent 的委派工具卡和它带回的结果。
- 不宣告任何认证方式 —— ACP 不驱动 `chord auth`。请先配好 provider 再启动客户端。
- 只做 ACP v1，实现的是稳定面，不含 unstable 扩展。
- **暂不支持 Windows。** 在 Windows 上 `chord acp` 会直接报错退出：保证 JSON-RPC 流干净的 stdout 守卫依赖 Unix 的 fd 复制，装不上它就不带守卫运行 —— 否则任何误写都可能污染协议流。

## 相关

- [CLI 参考](./cli_CN.md)
- [Headless 集成](./headless_CN.md)：给脚本与网关用的 JSON 控制面
- [权限与安全](./permissions-and-safety_CN.md)
