# 环境变量

本页列出 Chord 读取的所有环境变量，按用途分组，并给出决定哪个值生效的优先级规则。

## 优先级

对于路径与 API base，Chord 按下列顺序解析最终值：

1. **CLI flag**（如 `--state-dir /tmp/chord-state`）
2. **Chord 自有环境变量**（如 `CHORD_STATE_DIR`）
3. **`config.yaml` 中的 `paths:` 字段**（如 `paths.state_dir`）
4. **XDG 标准环境变量**（如 `XDG_STATE_HOME`）
5. **内置默认值**（如 `~/.local/state/chord`）

`auth.yaml` 中引用的凭据变量只在标量以 `$` 或 `${...}` 开头时才会展开。见 [配置与认证 — auth.yaml 中的环境变量](./configuration_CN.md#authyaml-中的环境变量)。

## 路径覆盖

| 变量                   | 作用                                                                                             | 未设置时的默认                                                       |
| ---------------------- | ------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------- |
| `CHORD_CONFIG_HOME`    | 配置主目录（provider 配置、agent、skill、自定义命令、`auth.yaml`）                               | 已设置 `$XDG_CONFIG_HOME` 时取 `$XDG_CONFIG_HOME/chord`，否则 `~/.config/chord` |
| `CHORD_STATE_DIR`      | 持久运行时状态根目录（sessions、exports、logs、projects、worktrees）                             | 已设置 `$XDG_STATE_HOME` 时取 `$XDG_STATE_HOME/chord`，否则 `~/.local/state/chord` |
| `CHORD_CACHE_DIR`      | 可重建缓存根                                                                                     | 已设置 `$XDG_CACHE_HOME` 时取 `$XDG_CACHE_HOME/chord`，否则 `~/.cache/chord` |
| `CHORD_SESSIONS_DIR`   | 仅覆盖 sessions 根                                                                               | `<state-dir>/sessions`                                                |
| `CHORD_LOGS_DIR`       | 仅覆盖 logs 目录                                                                                 | `<state-dir>/logs`                                                    |
| `XDG_CONFIG_HOME`      | XDG 标准配置根                                                                                   | `~/.config`                                                           |
| `XDG_STATE_HOME`       | XDG 标准 state 根                                                                                | `~/.local/state`                                                      |
| `XDG_CACHE_HOME`       | XDG 标准 cache 根                                                                                | `~/.cache`                                                            |

这些变量影响的具体目录布局见 [目录与路径](./paths_CN.md)。

## auth.yaml 引用的凭据

Chord 不直接从环境读取 provider key——它读 `auth.yaml`，并展开其中的 `$VAR` / `${VAR}` 占位符。约定俗成是用 `<PROVIDER>_API_KEY` 风格命名，但你可以用任意变量名。

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
| 任意自定义 `*_API_KEY`  | 你给它 `auth.yaml` 中绑的那个 provider 名                  |

注意：

- 未设置的变量展开为空字符串后被过滤掉；除非 YAML 值就是字面空字符串。
- 这种展开**仅**作用于 `auth.yaml`，不普遍作用于 `config.yaml` 的所有字段。

## 网络代理

如果你所在网络无法直连 Anthropic、OpenAI、Google 等官方 API，需要为 Chord 配置代理；否则 provider 请求会超时或连接失败。可以使用标准环境变量，也可以在 `config.yaml` 中配置全局或 provider 级 `proxy`。

Chord 用 Go 标准的 `http.ProxyFromEnvironment` 解析出站 HTTP 代理。所以标准变量直接生效：

| 变量                | 用途                                                                            |
| ------------------- | ------------------------------------------------------------------------------- |
| `HTTP_PROXY`        | `http://` 请求的代理                                                            |
| `HTTPS_PROXY`       | `https://` 请求的代理                                                           |
| `NO_PROXY`          | 用逗号分隔的不走代理的主机模式                                                  |
| `http_proxy` / `https_proxy` / `no_proxy` | 小写变体也被识别                                            |

如果只想给某个工具单独设代理（如只让 `WebFetch` 走 SOCKS5），见 [配置与认证 — WebFetch](./configuration_CN.md#webfetch)。

## 终端检测（只读）

下面这些是标准变量，Chord 只读不写，通常你不用主动设置。

| 变量                    | 用途                                                                                          |
| ----------------------- | --------------------------------------------------------------------------------------------- |
| `TERM`                  | 识别终端类型，进行能力协商                                                                    |
| `TERM_PROGRAM`          | 识别终端 emulator（iTerm2、WezTerm、Ghostty 等）以选择图片协议                                |
| `TERM_PROGRAM_VERSION`  | 与 `TERM_PROGRAM` 配合使用                                                                    |
| `TMUX`                  | 检测 chord 跑在 tmux 内                                                                       |
| `CMUX_SOCKET` / `CMUX_SOCKET_PATH` | 检测 chord 跑在 cmux 内；影响图片协议管线                                                  |
| `NO_COLOR`              | 任意非空值会禁用启动期 stderr 的 ANSI 颜色                                                    |
| `USER` / `USERNAME`     | 部分诊断输出会用                                                                              |

## 开发与调试

| 变量                 | 用途                                                                                                                              |
| -------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| `CHORD_HOOK_DEBUG`   | 设为 `1` 时记录每次 hook 调用（输入/输出/退出码/耗时）。比较啰嗦，仅在排查 hook 行为时使用。                                       |
| `CHORD_PPROF_PORT`   | 设为端口号（如 `6060`）会在 `127.0.0.1` 暴露 Go pprof。默认关闭。                                                                 |

这些是给开发、排障、上报 bug 用的，不建议日常常开。

## 关于 `CHORD_API_BASE`

`chord --help` 中 `--api-base` flag 的描述里提到了 `CHORD_API_BASE`。实际上 flag 本身是生效的，但**当前 build 并未真正从环境读取 `CHORD_API_BASE`**——只有 `--api-base` CLI flag 会生效（或者直接给每个 provider 配 `api_url`）。如果你需要全局覆盖 API base，建议在 `config.yaml` 各 provider 上单独设 `api_url`。

## 相关

- [目录与路径](./paths_CN.md)
- [CLI — 全局 flag](./cli_CN.md#全局-flag)
- [配置与认证](./configuration_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
