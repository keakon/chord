# 环境变量

Chord 读取的所有环境变量，按用途分组，附优先级规则。

## 优先级

路径与 API 基础地址按以下顺序解析最终值：

1. **CLI flag**（如 `--state-dir /tmp/chord-state`）
2. **Chord 自有环境变量**（如 `CHORD_STATE_DIR`）
3. **`config.yaml` 中的 `paths:` 字段**（如 `paths.state_dir`）
4. **XDG 标准环境变量**（如 `XDG_STATE_HOME`）
5. **内置默认值**（如 `~/.local/state/chord`）

`auth.yaml` 中的凭据变量仅在标量以 `$` 或 `${...}` 开头时展开。详见 [配置与认证 — auth.yaml 中的环境变量](./configuration_CN.md#authyaml-中的环境变量)。

## 路径覆盖

| 变量                   | 作用                                                                                             | 未设置时的默认                                                       |
| ---------------------- | ------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------- |
| `CHORD_CONFIG_HOME`    | 配置主目录（provider 配置、agent、skill、自定义命令、`auth.yaml`）                               | 已有 `$XDG_CONFIG_HOME` 时取 `$XDG_CONFIG_HOME/chord`，否则 `~/.config/chord` |
| `CHORD_STATE_DIR`      | 持久运行时状态根目录（sessions、exports、logs、projects、worktrees）                             | 已有 `$XDG_STATE_HOME` 时取 `$XDG_STATE_HOME/chord`，否则 `~/.local/state/chord` |
| `CHORD_CACHE_DIR`      | 可重建缓存根                                                                                     | 已有 `$XDG_CACHE_HOME` 时取 `$XDG_CACHE_HOME/chord`，否则 `~/.cache/chord` |
| `CHORD_SESSIONS_DIR`   | 仅覆盖 sessions 根                                                                               | `<state-dir>/sessions`                                                |
| `CHORD_LOGS_DIR`       | 仅覆盖 logs 目录                                                                                 | `<state-dir>/logs`                                                    |
| `XDG_CONFIG_HOME`      | XDG 标准配置根                                                                                   | `~/.config`                                                           |
| `XDG_STATE_HOME`       | XDG 标准 state 根                                                                                | `~/.local/state`                                                      |
| `XDG_CACHE_HOME`       | XDG 标准 cache 根                                                                                | `~/.cache`                                                            |

具体目录布局见 [目录与路径](./paths_CN.md)。

## auth.yaml 引用的凭据

Chord 不直接从环境读取 provider key——它读 `auth.yaml`，展开其中的 `$VAR` / `${VAR}` 占位符。惯例是用 `<PROVIDER>_API_KEY` 风格命名，但变量名随意。

```yaml
# ~/.config/chord/auth.yaml
anthropic:
  - "$ANTHROPIC_API_KEY"
openai:
  - "${OPENAI_API_KEY}"
gemini:
  - "$GEMINI_API_KEY"
my-gateway:
  - "$MY_GATEWAY_KEY"        # 任意变量名都可以
```

| 常见命名                | 作用                                                       |
| ----------------------- | ---------------------------------------------------------- |
| `ANTHROPIC_API_KEY`     | 配 `anthropic` provider 时在 `auth.yaml` 引用              |
| `OPENAI_API_KEY`        | `openai` 或 `openai-compatible` provider                   |
| `GEMINI_API_KEY`        | Google Gemini provider                                     |
| 任意自定义 `*_API_KEY`  | 对应 `auth.yaml` 中绑的那个 provider 名                    |

注意事项：

- 未设置的变量会展开为空字符串并被过滤；除非 YAML 值本身就是字面空字符串。
- 该展开仅作用于 `auth.yaml`，不会作用于 `config.yaml` 的所有字段。

## 网络代理

所在网络无法直连 Anthropic、OpenAI、Google 等官方 API 时，需要给 Chord 配代理，否则 provider 请求会超时或连接失败。可用标准环境变量，也可在 `config.yaml` 中配全局或 provider 级 `proxy`。

Chord 使用 Go 标准的 `http.ProxyFromEnvironment` 解析出站 HTTP 代理，标准变量直接生效：

| 变量                | 用途                                                                            |
| ------------------- | ------------------------------------------------------------------------------- |
| `HTTP_PROXY`        | `http://` 请求的代理                                                            |
| `HTTPS_PROXY`       | `https://` 请求的代理                                                           |
| `NO_PROXY`          | 用逗号分隔的不走代理的主机模式                                                  |
| `http_proxy` / `https_proxy` / `no_proxy` | 小写变体也会识别                                            |

只想给某个工具单独设代理（如只让 `web_fetch` 走 SOCKS5），见 [配置与认证 — WebFetch](./configuration_CN.md#webfetch)。

## 终端检测（只读）

下面是标准变量，Chord 只读不写，通常不需主动设置。

| 变量                    | 用途                                                                                          |
| ----------------------- | --------------------------------------------------------------------------------------------- |
| `TERM`                  | 识别终端类型，做能力协商                                                                      |
| `TERM_PROGRAM`          | 识别终端模拟器（iTerm2、WezTerm、Ghostty 等），选择图片协议与通知协议                                |
| `TERM_PROGRAM_VERSION`  | 与 `TERM_PROGRAM` 配合使用                                                                    |
| `TMUX`                  | 检测 Chord 跑在 tmux 内                                                                       |
| `CMUX_SOCKET` / `CMUX_SOCKET_PATH` | 检测 Chord 跑在 cmux 内；影响图片协议管线                                                  |
| `NO_COLOR`              | 任意非空值会禁用启动时 stderr 的 ANSI 颜色                                                    |
| `USER` / `USERNAME`     | 部分诊断输出会用到                                                                              |

## 终端能力覆盖（图片）

以下变量仅在排查或强制覆盖自动检测时使用：

| 变量                     | 用途                                                                 |
| ------------------------ | -------------------------------------------------------------------- |
| `CHORD_IMAGE_BACKEND`    | 强制图片后端：`kitty` / `iterm2` / `none` / `auto`（默认自动检测）   |
| `CHORD_IMAGE_INLINE`     | 强制是否启用 inline 图片：`1` / `0`                                  |
| `CHORD_IMAGE_FULLSCREEN` | 强制是否启用全屏图片查看：`1` / `0`                                  |

说明：

- `tmux` / `zellij` 内默认会保守禁用图片预览；这些变量主要用于调试或已知兼容环境下的手动覆盖。
- WezTerm 当前会自动走 iTerm2 图片协议；Ghostty / kitty 自动走 Kitty graphics。

## 开发与调试

| 变量                 | 用途                                                                                                                              |
| -------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| `CHORD_HOOK_DEBUG`   | 设为 `1` 时记录每次 hook 调用（输入/输出/退出码/耗时）。输出较多，只在排查 hook 行为时用。                                       |
| `CHORD_PPROF_PORT`   | 设为端口号（如 `6060`）会在 `127.0.0.1` 暴露 Go pprof。默认关闭。                                                                 |

这些用于开发、排障、上报 bug，不建议日常常开。

## 关于 `CHORD_API_BASE`

`chord --help` 中 `--api-base` flag 描述提到了 `CHORD_API_BASE`。flag 本身生效，但当前构建并未真正从环境读取 `CHORD_API_BASE`——只有 `--api-base` CLI flag 生效（或直接给每个 provider 配 `api_url`）。需要全局覆盖 API base 时，建议在 `config.yaml` 各 provider 上单独设 `api_url`。

## 相关

- [目录与路径](./paths_CN.md)
- [CLI — 全局 flag](./cli_CN.md#全局-flag)
- [配置与认证](./configuration_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
