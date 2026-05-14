# Chord

[![CI](https://github.com/keakon/chord/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/keakon/chord/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/keakon/chord?display_name=release)](https://github.com/keakon/chord/releases) [![Go Version](https://img.shields.io/github/go-mod/go-version/keakon/chord)](./go.mod) [![License](https://img.shields.io/github/license/keakon/chord)](./LICENSE)

📖 **文档站：** <https://keakon.github.io/chord/zh/> · **English：** [README.md](./README.md)

**让 AI 编码体验从容下来。** 一个轻量、本地优先的终端 Coding Agent——会话再长也不崩、模型组合可热切、脱离电脑时还能远程操控。

- 配套网关：[keakon/chord-gateway](https://github.com/keakon/chord-gateway) —— 把 Chord 接到微信、飞书等聊天平台

## 为什么选 Chord

先说最容易感受到的核心体验：

- **稳定长期运行。** Chord 会在长对话接近模型 token 上限前压缩早期内容，同时保留继续工作所需的信息，不会出现「它忘了？」的尴尬。
- **网络状态全程可见。** 等模型响应时，Chord 实时显示请求状态和已等待时间。再也不用猜「是不是卡住了」。
- **键盘优先、Vim 风格。** Insert / Normal 模式、消息搜索、Vim 风格导航、模式切换自动切输入法。退出需连按两次，避免误触 Ctrl+C 丢工作。
- **模型组合热切换。** 将模型分组到可复用的池（`fast`、`thinking`、`cheap` 等），运行时用 `/models` 或 `Ctrl+P` 切换。每个 agent 独立选池；运行时自动按池内顺序 fallback。
- **资源占用极低。** 低内存、低 CPU 占用。macOS 上电源感知：工作时阻止系统休眠，空闲后自动放行。
- **能远程操控。** `chord headless` 提供 stdio JSONL 控制面；配合 [chord-gateway](https://github.com/keakon/chord-gateway) 可从任意聊天平台驱动 Chord，离开桌面也能在手机上操控。

开箱即用，还有这些体验增强能力：

- **LSP 加持的代码上下文** —— 接入本地 language server，提供实时诊断和 definition/references/implementation 查询。
- **多模态输入** —— 粘贴剪贴板图片、附加文件、在支持的终端里预览。
- **Codex 额度可见** —— 实时显示 OpenAI Codex 订阅的剩余额度和重置时间。

熟悉之后，还可以进一步用这些进阶工作流：

- **多 Agent 协作** —— 主 agent 派出 SubAgent，每个拥有独立 context；`Shift+Tab` 切换视图。
- **基于 git worktree 的并行任务** —— `chord --worktree feat-auth` 启动独立 worktree，多任务在同仓库下互不干扰。

## 三步上手

### 1. 安装

已安装 Go 1.26+ 时：

```bash
go install github.com/keakon/chord/cmd/chord@latest
```

未安装 Go 1.26+ 时，可从 [GitHub Releases](https://github.com/keakon/chord/releases) 下载预构建二进制。选择与 OS/架构匹配的压缩包，解压后把 `chord` 放入 `PATH`，然后执行：

```bash
chord --version
```

### 2. 先运行一次初始化向导

在交互式终端里直接运行：

```bash
chord
```

如果缺少 `config.yaml`，Chord 会启动一次性的初始化向导。向导会创建最小可用的 `config.yaml`，必要时再创建 `auth.yaml`，并在结束时展示实际路径。

如果你更希望手写 YAML，或需要不同的 provider / 模型配置，见[快速开始](./docs/quickstart_CN.md)。

### 3. 在项目里启动

```bash
cd my-project && chord
```

手动配置 provider / 模型以及理解模型限制，见[快速开始](./docs/quickstart_CN.md)。按这个顺序理解模型限制：`limit.context` 是总窗口；大多数模型只要“输入 + 请求输出”不超过这个窗口即可。如果 provider 还单独列出了输入上限（例如部分 GPT 模型），再额外写 `limit.input`；未配置时，Chord 会回退到 `limit.context`。`limit.output` 是模型的最大输出能力，Chord 默认 `max_output_tokens` 仍为 `32000`，实际会取更小的值。相关术语见 [术语表](./docs/glossary_CN.md)，可直接复制粘贴的完整 `config.yaml` 见 [示例配置库](./docs/examples/index_CN.md)。

### Release 下载说明

GitHub Releases 提供多个支持平台的预构建二进制。macOS 下载版首次运行时可能因文件来自互联网且未公证而被系统阻止。遇到此情况时，移除 quarantine 属性并确保文件可执行：

```bash
xattr -dr com.apple.quarantine /path/to/chord
chmod +x /path/to/chord
/path/to/chord --version
```

若仍被 macOS 阻止，可添加本地 ad-hoc 签名：

```bash
codesign --force --sign - /path/to/chord
```

安装到 `/usr/local/bin/chord` 时，将 `/path/to/chord` 替换为 `/usr/local/bin/chord`。

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

Chord 主要在 macOS 上开发和测试。Linux 表现良好；Windows 大体可用但可能存在未发现的 bug。`prevent_sleep` 等少数能力仅 macOS 生效，其他平台静默 no-op。具体能力矩阵见 [平台支持](./docs/platforms_CN.md)。

## License

MIT License，详见 [LICENSE](./LICENSE)。
