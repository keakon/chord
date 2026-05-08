# 快速开始

本文档面向第一次使用 Chord 的用户，目标是在几分钟内跑通一次最小可用流程。

## 1. 安装

需要 Go 1.26 或更高版本。

```bash
# 从 GitHub 安装
go install github.com/keakon/chord/cmd/chord@latest

# 或从源码构建
go build -o chord ./cmd/chord/
```

你也可以从 [GitHub Releases](https://github.com/keakon/chord/releases) 下载预构建二进制。macOS 下载版首次运行时可能会因为文件来自互联网且未公证而被系统阻止。遇到这种情况时执行：

```bash
xattr -dr com.apple.quarantine /path/to/chord
chmod +x /path/to/chord
/path/to/chord --version
```

如果仍然被 macOS 阻止，可以添加本地 ad-hoc 签名：

```bash
codesign --force --sign - /path/to/chord
```

请把 `/path/to/chord` 替换为实际安装路径，例如 `/usr/local/bin/chord`。

> 运行源码入口时请使用 `go run ./cmd/chord/`，不要使用 `go run cmd/chord/main.go`。

## 2. 配置 API Key

先创建配置目录：

```bash
mkdir -p ~/.config/chord
chmod 700 ~/.config/chord
```

然后编辑 `~/.config/chord/auth.yaml`，选择一种方式配置凭据。

### 方案 A：Anthropic

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"
```

### 方案 B：OpenAI 兼容接口

```yaml
openai-compatible:
  - "$OPENAI_API_KEY"
```

### 方案 C：OpenAI ChatGPT / Codex OAuth

先在 `~/.config/chord/config.yaml` 中添加 provider：

```yaml
providers:
  openai:
    type: openai
    preset: codex
```

然后执行：

```bash
chord auth openai
```

## 3. 创建最小配置

编辑 `~/.config/chord/config.yaml`：

```yaml
providers:
  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-opus-4.7:
        limit:
          context: 1000000
          output: 128000
```

如果你使用 OpenAI 兼容接口，可以把 `type` 和 `api_url` 改成对应值。

## 4. 运行

在你的项目目录中执行：

```bash
cd my-project
chord
# 或
go run ./cmd/chord/
```

首次运行时，Chord 会按需创建项目级 `.chord/` 目录。

如果需要无界面的控制面模式：

```bash
chord headless
# 或
go run ./cmd/chord/ headless
```

headless 模式说明见 [Headless 集成](./headless_CN.md)。

## 5. 首次交互

启动后：

1. 直接输入你的问题
2. 按 `Enter` 发送
3. 按 `Esc` 进入 Normal 模式
4. 按 `q` 退出，或在 2 秒内连续按两次 `Ctrl+C`

你可以先试一条简单消息，例如：

```text
请先阅读当前项目结构，然后总结它的主要模块。
```

## 6. 常用启动方式

```bash
# 正常启动；当前模型取自该 agent 的 model_pools 列表中的第一个池。
# 启动后用 /models 查看池状态，或 /models <pool> / Ctrl+P 切换池。
# 完整配置说明：./configuration_CN.md#模型池指定-provider-与-model
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

## 7. 下一步阅读

- [使用指南](./usage_CN.md)
- [配置与认证](./configuration_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [扩展与定制](./customization_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
