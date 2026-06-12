# 快速开始

几分钟跑通一个最小可用流程。

## 1. 安装

需要 Go 1.26.3+。

```bash
# 从 GitHub 安装
go install github.com/keakon/chord/cmd/chord@latest

# 或从源码构建
go build -o chord ./cmd/chord/
```

也可从 [GitHub Releases](https://github.com/keakon/chord/releases) 下载预构建二进制。macOS 下载版首次运行时，系统可能因文件来自互联网且未公证而阻止运行，执行以下命令即可：

```bash
xattr -dr com.apple.quarantine /path/to/chord
chmod +x /path/to/chord
/path/to/chord --version
```

若仍被 macOS 阻止，添加本地 ad-hoc 签名：

```bash
codesign --force --sign - /path/to/chord
```

把 `/path/to/chord` 换成实际安装路径，如 `/usr/local/bin/chord`。

> 运行源码入口时用 `go run ./cmd/chord/`，不要用 `go run cmd/chord/main.go`。

## 2. 第一次运行

在交互式终端里直接运行 `chord`。如果缺少 `config.yaml`，Chord 会启动一次性的初始化向导。
向导会创建最小可用的 `config.yaml`，必要时再创建 `auth.yaml`，如果已有匹配的 `auth.yaml` 凭据则尽量直接复用，并在结束时展示实际路径。
即使 stdin 被重定向，只要还能拿到控制 TTY，向导仍会使用该 TTY 交互；只有没有控制 TTY 时，Chord 才会立即返回初始化错误而不是等待输入。

如果你更希望手写 YAML 而不是使用向导，见[配置与认证](./configuration_CN.md)或可直接复制粘贴的[示例配置库](./examples/index_CN.md)。

对于 API key 配置，向导提供一个通用的 API key provider 路径。它会要求你输入一个以下列后缀结尾的 API URL，并在提示里给出示例：

- `/responses` —— OpenAI Responses API / 兼容网关
- `/messages` —— Anthropic Messages API / 兼容网关
- `/chat/completions` —— OpenAI Chat Completions 兼容网关
- `/models` —— Gemini Generate Content 基础路径

Chord 会根据这个端点推荐起始 provider 名和模型名，例如 `openai` / `gpt-5.5`、`anthropic` / `claude-opus-4.8`、`gemini` / `gemini-3.5-flash`。

如果 provider 需要代理，向导也可以把 proxy URL 写入 `config.yaml`，并给出 `http://127.0.0.1:1080`、`socks5://127.0.0.1:1080` 这类示例。

如果你选择的是 API key provider 路径，可用下面的命令检查已配置模型：

```bash
chord doctor models
```

如果你选择的是 Codex OAuth 路径，向导会在结束前直接完成 OAuth 登录。它会创建一个 `preset: codex` provider，并自动配置这些起始模型：`gpt-5.2`、`gpt-5.3-codex`、`gpt-5.4`、`gpt-5.5`。

## 3. 运行

在项目目录中执行：

```bash
cd my-project
chord
# 或
go run ./cmd/chord/
```

首次运行时，Chord 会按需创建项目级 `.chord/` 目录。

无界面控制面模式：

```bash
chord headless
# 或
go run ./cmd/chord/ headless
```

headless 模式说明见 [Headless 集成](./headless_CN.md)。

## 4. 首次交互

启动后：

1. 直接输入问题
2. 按 `Enter` 发送
3. 按 `Esc` 进入 Normal 模式
4. 按 `q` 退出，或 2 秒内连按两次 `Ctrl+C`

试试一条简单消息：

```text
请先阅读当前项目结构，然后总结它的主要模块。
```

## 5. 常用启动方式

```bash
# 正常启动；当前模型取自该 agent 的 model_pools 列表中的第一个池。
# 启动后用 /models 查看池状态，或 /models <pool> / Ctrl+P 切换池。
# 完整配置说明：./configuration_CN.md#模型池
chord

# 恢复最近会话
chord --continue

# 恢复指定会话
chord --resume 20260428064910975

# 创建或进入 chord 管理的 git worktree，让该任务的 session、缓存与项目主干隔离；
# 可与 --continue / --resume 组合，作用于该 worktree 自身的会话历史。
chord --worktree feat-auth
```

worktree 列表/移除、跨 worktree resume 与 headless 集成等完整用法见 [Worktree 用法](./usage_CN.md#worktree)。

## 6. 下一步阅读

- [使用指南](./usage_CN.md)
- [配置与认证](./configuration_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [扩展与定制](./customization_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
