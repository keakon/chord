# Chord

[![CI](https://github.com/keakon/chord/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/keakon/chord/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/keakon/chord?display_name=release)](https://github.com/keakon/chord/releases) [![Go Version](https://img.shields.io/github/go-mod/go-version/keakon/chord)](./go.mod) [![License](https://img.shields.io/github/license/keakon/chord)](./LICENSE)

📖 **文档站：** <https://keakon.github.io/chord/zh/>

🌐 [English introduction](./README.md)

**花更少的 token，干更难的活。** 一个轻量的终端 Coding Agent：告别上下文腐烂，模型不可用时自动切换。

<p align="center">
  <img src="./docs/assets/screenshot.png" alt="Chord 终端界面截图" width="900">
</p>

## 为什么选 Chord

- **长会话更省 token**
- **启动快、内存占用低**
- **尽可能展示所有细节**
- **键盘优先、Vim 风格**
- **需要你处理时才通知**
- **模型池热切换**
- **支持远程操控**
- **支持导入 Claude Code、Codex、OpenCode 的会话**
- **LSP 集成**
- **支持预览图片**
- **显示 Codex 订阅额度与重置时间**
- **健壮且可自定义的 Agent 团队**
- **基于 git worktree 的并行任务**

## 三步上手

### 1. 安装

已安装 Go 1.27.0+ 时：

```bash
go install github.com/keakon/chord/cmd/chord@latest
```

源码构建要求 Go 1.27.0 或更新版本；默认的 `GOTOOLCHAIN=auto` 会在需要时自动下载所需 toolchain。

未安装 Go 1.27.0+ 时，从 [GitHub Releases](https://github.com/keakon/chord/releases) 下载与 OS/架构匹配的压缩包，解压后把 `chord` 放入 `PATH`，再运行：

```bash
chord --version
```

macOS 上首次运行下载的二进制可能被系统阻止（文件来自互联网且未公证），解除阻止的 `xattr` / `codesign` 命令见[快速开始](./docs/quickstart_CN.md#1-安装)。

### 2. 运行初始化向导

在交互式终端运行：

```bash
chord
```

缺少 `config.yaml` 时，Chord 会启动一次性的初始化向导：创建最小可用的 `config.yaml`，必要时再创建 `auth.yaml`，结束时展示实际路径。

想手写 YAML 或需要不同的 provider / 模型配置，见[快速开始](./docs/quickstart_CN.md)。

### 3. 在项目里启动

```bash
cd my-project && chord
```

手动配置 provider / 模型以及 `limit` 字段的规则见[快速开始](./docs/quickstart_CN.md)与[术语表](./docs/glossary_CN.md)；可直接复制的 `config.yaml` 见[示例配置库](./docs/examples/index_CN.md)。

## 文档

- [文档首页](./docs/index_CN.md)
- 入门：[快速开始](./docs/quickstart_CN.md) · [使用指南](./docs/usage_CN.md) · [术语表](./docs/glossary_CN.md)
- 参考：[CLI](./docs/cli_CN.md) · [配置与认证](./docs/configuration_CN.md) · [上下文管理](./docs/context-management_CN.md) · [模型配置速查](./docs/model-configs_CN.md) · [内置工具](./docs/tools_CN.md) · [编辑工具](./docs/edit-tools_CN.md) · [快捷键](./docs/keybindings_CN.md) · [目录与路径](./docs/paths_CN.md) · [环境变量](./docs/environment_CN.md) · [平台支持](./docs/platforms_CN.md) · [性能](./docs/performance_CN.md)
- 进阶：[扩展与定制](./docs/customization_CN.md) · [Hooks](./docs/hooks_CN.md) · [示例配置库](./docs/examples/index_CN.md)
- 集成：[Headless](./docs/headless_CN.md)（`chord headless`）
- 安全：[权限与安全](./docs/permissions-and-safety_CN.md)
- 排障：[常见问题排查](./docs/troubleshooting_CN.md)

## 性能摘要

在 Chord v0.8.1 的一次 [DeepSWE v1.1 任务](https://deepswe.datacurve.ai/data/v1.1/tasks/httpx-streaming-json-iteration)测试中（给 `httpx` 加流式 JSON 迭代接口），Chord 用时 6m37s，输入 54.5K token、缓存读取 2.96M token、输出 58.6K token，估算成本 ￥0.348。同样使用 deepseek-v4.1-flash 的六款 agent harness 里，Chord 用时最短、成本最低：比第二名快 33%、便宜 34%。

这只是单次场景实测，不代表普遍结果；完整数据表（含应用内存）和测量方法见[性能 — 实测数据](./docs/performance_CN.md#实测数据)。

## 项目链接

- 配套：[keakon/chord-gateway](https://github.com/keakon/chord-gateway)
- [贡献指南](./CONTRIBUTING.md)
- [Changelog（中文）](./CHANGELOG_CN.md)
- [问题反馈](https://github.com/keakon/chord/issues)

## 平台支持

Chord 主要在 macOS 上开发和测试。Linux 表现良好；Windows 大体可用但可能存在未发现的 bug。`prevent_sleep` 等少数能力仅 macOS 生效，其他平台静默 no-op。具体能力矩阵见 [平台支持](./docs/platforms_CN.md)。

## 致谢

Chord 基于 [Bubble Tea](https://github.com/charmbracelet/bubbletea) 构建，设计与功能借鉴了 Claude Code、Codex、OpenCode 和 Crush，主要使用 GPT-5.4/5.5 辅助开发。感谢 [linux.do](https://linux.do/) 上大量公益站提供 tokens。

## License

MIT License，详见 [LICENSE](./LICENSE)。
