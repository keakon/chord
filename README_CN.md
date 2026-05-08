# Chord

[![CI](https://github.com/keakon/chord/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/keakon/chord/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/keakon/chord?display_name=release)](https://github.com/keakon/chord/releases) [![Go Version](https://img.shields.io/github/go-mod/go-version/keakon/chord)](./go.mod) [![License](https://img.shields.io/github/license/keakon/chord)](./LICENSE)

📖 **文档站：** <https://keakon.github.io/chord/zh/> · **English：** [README.md](./README.md)

**让 AI 编码体验从容下来。** 一个轻量、本地优先的终端 Coding Agent——会话再长也不崩、模型组合可热切、脱离电脑时还能远程操控。

- 配套网关：[keakon/chord-gateway](https://github.com/keakon/chord-gateway) —— 把 Chord 接到微信、飞书等聊天平台

## 为什么选 Chord

- **长会话不崩。** 自动 compaction 把长对话压缩成上下文摘要，让会话在超过 context window 之后还能继续——保留继续工作所需的关键信息，不会出现"它忘了？"的尴尬。
- **网络状态全程可见。** 等模型响应时，Chord 实时显示请求状态和已等待时间。再也不用猜"是不是卡住了"。
- **键盘优先、Vim 风格。** Insert / Normal 模式、消息搜索、Vim 风格导航、模式切换时自动切输入法。退出需要连按两次，避免误触 Ctrl+C 丢工作。
- **模型组合热切换。** 把模型分组到可复用的池（`fast`、`thinking`、`cheap` 等），运行时用 `/models` 或 `Ctrl+P` 切。每个 agent 独立选池；运行时自动按池内顺序 fallback。
- **小 VPS 上能跑很久。** 低内存、低 CPU 占用。macOS 上电源感知：工作时阻止系统休眠，空闲后自动放行。
- **能远程操控。** `chord headless` 提供 stdio JSONL 控制面；配合 [chord-gateway](https://github.com/keakon/chord-gateway) 可从任意聊天平台驱动 Chord。

后期会用得上的额外能力：

- **多 Agent 协作** —— 主 agent 派出 SubAgent，每个有独立 context；`Shift+Tab` 切换视图。
- **基于 git worktree 的并行任务** —— `chord --worktree feat-auth` 启一个独立 worktree，多任务在同仓库下互不干扰。
- **LSP 加持的代码上下文** —— 接本地 language server，提供实时 diagnostics 和 definition/references/implementation 查询。
- **多模态输入** —— 粘贴剪贴板图片、附加文件、在支持的终端里预览。
- **Codex 额度可见** —— 实时显示 OpenAI Codex 订阅的剩余额度和重置时间。

## 适合你的场景

- **小 VPS 上的常驻助手。** 低资源占用 + 长会话稳，可以挂着随时回来用。
- **混搭多模型。** 思考用强模型、导航用快模型、压缩用便宜模型——一键切换。
- **从手机上操控。** 把 `chord headless` 通过 chord-gateway 暴露出来，离开桌面也能在聊天里调用真正的编码 agent。

## 三步上手

### 1. 安装

如果你已经安装 Go 1.26+：

```bash
go install github.com/keakon/chord/cmd/chord@latest
```

如果你没有安装 Go 1.26+，可以从 [GitHub Releases](https://github.com/keakon/chord/releases) 下载预构建二进制。选择与你的 OS/架构匹配的压缩包，解压后把 `chord` 放到 `PATH` 中，然后执行：

```bash
chord --version
```

### 2. 配置 provider、模型池与凭据

```bash
mkdir -p ~/.config/chord && chmod 700 ~/.config/chord
cat > ~/.config/chord/config.yaml <<'YAML'
providers:
  modelscope:
    type: chat-completions
    api_url: https://api-inference.modelscope.cn/v1/chat/completions
    models:
      Qwen/Qwen3.5-397B-A17B:
        limit:
          context: 262144
          output: 65536
        modalities:
          input: [text, image]
model_pools:
  default:
    - modelscope/Qwen/Qwen3.5-397B-A17B
YAML
cat > ~/.config/chord/auth.yaml <<'YAML'
modelscope:
  - "$MODELSCOPE_API_KEY"
YAML
```

### 3. 在项目里启动

```bash
cd my-project && chord
```

其他 ModelScope 模型或 OpenAI 兼容 API 的配置方式见 [快速开始](./docs/quickstart_CN.md)。可直接复制粘贴的完整 `config.yaml` 见 [示例配置库](./docs/examples/index_CN.md)。

### Release 下载说明

GitHub Releases 提供多个受支持平台的预构建二进制。macOS 下载版首次运行时可能会因为文件来自互联网且未公证而被系统阻止。遇到这种情况时，可以移除 quarantine 属性并确保文件可执行：

```bash
xattr -dr com.apple.quarantine /path/to/chord
chmod +x /path/to/chord
/path/to/chord --version
```

如果仍然被 macOS 阻止，可以添加本地 ad-hoc 签名：

```bash
codesign --force --sign - /path/to/chord
```

例如安装到 `/usr/local/bin/chord` 时，把 `/path/to/chord` 替换为 `/usr/local/bin/chord`。

## 文档

- [文档首页](./docs/index_CN.md)
- 入门：[快速开始](./docs/quickstart_CN.md) · [使用指南](./docs/usage_CN.md) · [术语表](./docs/glossary_CN.md)
- 参考：[CLI](./docs/cli_CN.md) · [配置与认证](./docs/configuration_CN.md) · [快捷键](./docs/keybindings_CN.md) · [目录与路径](./docs/paths_CN.md) · [环境变量](./docs/environment_CN.md) · [平台支持](./docs/platforms_CN.md)
- 进阶：[扩展与定制](./docs/customization_CN.md) · [Hooks](./docs/hooks_CN.md) · [示例配置库](./docs/examples/index_CN.md)
- 集成：[Headless](./docs/headless_CN.md)
- 安全：[权限与安全](./docs/permissions-and-safety_CN.md)
- 排障：[常见问题排查](./docs/troubleshooting_CN.md)

## 项目链接

- 配套：[keakon/chord-gateway](https://github.com/keakon/chord-gateway)
- [贡献指南](./CONTRIBUTING.md)
- [Changelog（中文）](./CHANGELOG_CN.md)
- [问题反馈](https://github.com/keakon/chord/issues)

## 平台支持

Chord 主要在 macOS 上开发和测试。Linux 表现良好；Windows 大体可用但可能有未发现的 bug。`prevent_sleep` 等少数能力仅 macOS 生效，其他平台静默 no-op。具体能力矩阵见 [平台支持](./docs/platforms_CN.md)。

## License

MIT License，详见 [LICENSE](./LICENSE)。
