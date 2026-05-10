# 快速开始

几分钟跑通一个最小可用流程。

## 1. 安装

需要 Go 1.26 或更高版本。

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

## 2. 配置 API Key

先创建配置目录：

```bash
mkdir -p ~/.config/chord
chmod 700 ~/.config/chord
```

然后编辑 `~/.config/chord/auth.yaml`。以默认的 ModelScope 示例为例：

```yaml
modelscope:
  - "$MODELSCOPE_API_KEY"
```

其他 provider 同理——用 provider 名作为 key，如 `anthropic`、`openai`，或任意自定义的 OpenAI 兼容 provider 名。

OpenAI ChatGPT / Codex OAuth 需先在 `~/.config/chord/config.yaml` 中添加 provider：

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
  modelscope:
    type: chat-completions
    api_url: https://api-inference.modelscope.cn/v1/chat/completions
    models:
      Qwen/Qwen3.5-397B-A17B:
        limit:
          context: 262144
          input: 262144
          output: 65536
        modalities:
          input: [text, image]

model_pools:
  default:
    - modelscope/Qwen/Qwen3.5-397B-A17B
```

`providers` 定义可用的 API 端点和模型；`model_pools.default` 定义内置 `builder` / `planner` 默认使用的模型池。两者都需要配置——只配 provider 不配模型池的话，启动会报找不到默认模型池。内置 `builder` 只引用 `default` 池，不会自动使用全局 `model_pools`；用自定义 `builder` agent 覆盖内置配置时，也必须显式配置 `model_pools` 或 `models`。

换用其他 ModelScope 模型或其他 OpenAI 兼容接口时，把 `api_url`、provider 名和模型名改成对应值，同步更新 `model_pools.default` 中的 `provider/model` 引用即可。按这个顺序理解模型限制：`limit.context` 是总窗口；大多数模型只要“输入 + 请求输出”不超过这个窗口即可。如果 provider 还单独列出了输入上限（例如部分 GPT 模型），再额外写 `limit.input`；未配置时，Chord 会回退到 `limit.context`。`limit.output` 是模型自己的输出能力。相关术语见 [术语表](./glossary_CN.md)。

## 4. 运行

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

## 5. 首次交互

启动后：

1. 直接输入问题
2. 按 `Enter` 发送
3. 按 `Esc` 进入 Normal 模式
4. 按 `q` 退出，或 2 秒内连按两次 `Ctrl+C`

试试一条简单消息：

```text
请先阅读当前项目结构，然后总结它的主要模块。
```

## 6. 常用启动方式

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

## 7. 下一步阅读

- [使用指南](./usage_CN.md)
- [配置与认证](./configuration_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [扩展与定制](./customization_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
