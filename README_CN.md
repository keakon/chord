<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./assets/logo/chord-wordmark-dark.svg">
    <img src="./assets/logo/chord-wordmark-light.svg" alt="Chord" width="360">
  </picture>
</p>

<p align="center"><strong>更快、更省、更轻的终端 Coding Agent。</strong></p>

<p align="center">
  <a href="https://keakon.github.io/chord/zh/">文档站</a> ·
  <a href="./README.md">English README</a>
</p>

<p align="center">
  <a href="https://github.com/keakon/chord/actions/workflows/ci.yml"><img src="https://github.com/keakon/chord/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/keakon/chord/releases"><img src="https://img.shields.io/github/v/release/keakon/chord?display_name=release" alt="Release"></a>
  <a href="./go.mod"><img src="https://img.shields.io/github/go-mod/go-version/keakon/chord" alt="Go Version"></a>
  <a href="./LICENSE"><img src="https://img.shields.io/github/license/keakon/chord" alt="License"></a>
</p>

<p align="center">
  <img src="./docs/assets/screenshot.png" alt="Chord 终端界面：左侧是工具调用、补丁和诊断，右侧是模型、用量、待办和变更文件" width="900">
</p>

## 亮点功能

- [模型自动切换](./docs/configuration_CN.md#模型池)
- [请求级剪裁＋持久压缩](./docs/context-management_CN.md)
- [流式工具早执行](./docs/performance_CN.md#流式工具早执行)
- [内存占用小](./docs/performance_CN.md#应用内存)
- [导入 Claude Code、Codex、OpenCode 会话](./docs/usage_CN.md#导入外部会话)
- [Vim 风格键盘操作](./docs/keybindings_CN.md)

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

### 2. 在项目里启动

在交互式终端进入项目目录：

```bash
cd my-project
chord
```

缺少 `config.yaml` 时，Chord 会启动一次性的初始化向导：创建最小可用的 `config.yaml`，必要时再创建 `auth.yaml`，结束时展示实际路径。

想手写 YAML 或需要不同的 provider / 模型配置，见[快速开始](./docs/quickstart_CN.md)。

### 3. 发送第一个任务

直接说明你想让它做什么，按 `Enter` 发送。比如先让它读一遍你的项目：

```text
请解释这个项目的主要模块和测试入口，先不要修改文件。
```

查看回答和工具执行结果。熟悉项目后，再让 Chord 实现具体改动。

手动配置 provider / 模型以及 `limit` 字段的规则见[快速开始](./docs/quickstart_CN.md)与[术语表](./docs/glossary_CN.md)；可直接复制的 `config.yaml` 见[配置示例](./docs/examples/index_CN.md)。

## 文档

- [快速开始](./docs/quickstart_CN.md)：安装并完成第一个任务
- [使用指南](./docs/usage_CN.md)：日常操作、恢复会话和长任务
- [按工作选模型](./docs/model-choice_CN.md) · [模型配置速查](./docs/model-configs_CN.md) · [配置示例](./docs/examples/index_CN.md)：先选渠道，再接入
- [权限与安全](./docs/permissions-and-safety_CN.md)：决定哪些操作需要确认
- [长任务](./docs/usage_CN.md#loop持续执行模式)：让实现、检查和修复连续推进
- [扩展与定制](./docs/customization_CN.md)：配置角色、技能、代码诊断和外部工具
- [Headless 集成](./docs/headless_CN.md)：通过 `chord headless` 从其他入口操控
- [排障](./docs/troubleshooting_CN.md) · [完整文档目录](./docs/index_CN.md)

## 实测数据

六款同类编码 agent 用同一个模型（deepseek-v4.1-flash）跑同一个 [DeepSWE v1.1 任务](https://deepswe.datacurve.ai/data/v1.1/tasks/httpx-streaming-json-iteration)：给 `httpx` 加上流式 JSON 迭代接口。Chord 0.8.1 用时最短、成本最低：6m37s、￥0.348，输入 54.5K token、缓存读取 2.96M token、输出 58.6K token。第二名的耗时是它的 1.49 倍、成本是 1.51 倍。

### 真实编码任务

| 工具 | 耗时 | 成本 |
|---------|------|------|
| Chord 0.8.1 | **6m37s** | **￥0.348** |
| deepseek-harness 0.1.5-rc.1 | 9m50s（1.49 倍） | ￥0.527（1.51 倍） |
| pi 0.85.1 | 10m22s（1.57 倍） | ￥0.576（1.65 倍） |
| codex 0.154.0 | 17m01s（2.57 倍） | ￥0.929（2.67 倍） |
| mini-swe-agent 2.4.6 | 18m29s（2.79 倍） | ￥0.835（2.40 倍） |
| claude code 2.1.272 | 22m25s（3.39 倍） | ￥1.104（3.17 倍） |

括号里是相对 Chord 的倍数。

### 应用内存

测试环境：macOS 15.3.2（arm64）。

| 工具 | 空会话 | 200 条消息 | 增量 |
|---------|--------|------------|------|
| Chord 0.8.1 | 30MB | **39MB** | **+9MB** |
| codex 0.154.0 | **27MB** | 47MB | +20MB |
| claude code 2.1.273 | 143MB | 216MB | +73MB |

以上数据来自一次任务实测和一次内存场景，不代表普遍结果。完整数据表、测量方法和实现说明见[性能](./docs/performance_CN.md#实测数据)。

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
