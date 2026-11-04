<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./assets/logo/chord-wordmark-dark.svg">
    <img src="./assets/logo/chord-wordmark-light.svg" alt="Chord" width="360">
  </picture>
</p>

<p align="center"><strong>更快、更省、更轻的终端 Coding Agent。</strong></p>

<p align="center">同一任务实测：耗时和成本约为 Claude Code 的 1/3。</p>

<p align="center">单个会话只占几十兆内存（实测 200 条消息后 39MB），配置不高的电脑也能同时跑多个 Agent。</p>

<p align="center">
  <a href="./docs/quickstart_CN.md">快速开始</a> ·
  <a href="./docs/model-choice_CN.md#账号与-api-支持">模型接入</a> ·
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

## 实测数据

### 给 httpx 添加流式 JSON 迭代接口

六款编程 Agent 用同一个模型分别完成同一个 [DeepSWE v1.1 任务](https://deepswe.datacurve.ai/data/v1.1/tasks/httpx-streaming-json-iteration)：给 Python HTTP 客户端 `httpx` 添加流式 JSON 迭代接口，并处理解码失败、响应重复迭代等边界情况。模型均为 `deepseek-flash`（现由 DeepSeek-V4.1-Flash-1017 提供服务）。耗时包含模型交互、代码修改和测试执行，不含环境准备和最终收尾。

| 工具 | 耗时 | 估算成本 |
|---------|------|------|
| Chord 0.8.1 | **6m37s** | **￥0.348** |
| deepseek-harness 0.1.5-rc.1 | 9m50s（1.49 倍） | ￥0.527（1.51 倍） |
| pi 0.85.1 | 10m22s（1.57 倍） | ￥0.576（1.65 倍） |
| codex 0.154.0 | 17m01s（2.57 倍） | ￥0.929（2.67 倍） |
| mini-swe-agent 2.4.6 | 18m29s（2.79 倍） | ￥0.835（2.40 倍） |
| claude code 2.1.272 | 22m25s（3.39 倍） | ￥1.104（3.17 倍） |

括号内为相对 Chord 的倍数。Chord 用时最短、成本最低，输入和输出 token（54,530 / 58,559）也最少。

Chord 会在每次请求前剪裁旧工具输出、对长会话做持久压缩，并在参数完整时提前执行安全工具；实现细节见[性能](./docs/performance_CN.md#工作方式)。

### 应用内存

在 macOS 15.3.2（arm64）上，分别测量空会话和加载 200 条消息后的内存占用：

| 工具 | 空会话 | 200 条消息 | 增量 |
|---------|--------|------------|------|
| Chord 0.8.1 | 30MB | **39MB** | **+9MB** |
| codex 0.154.0 | **27MB** | 47MB | +20MB |
| claude code 2.1.273 | 143MB | 216MB | +73MB |

以上数据来自一次任务实测和一次内存测试，不代表普遍结果。[查看 token 明细与测量条件](./docs/performance_CN.md#实测数据)。

## 三步上手

### 1. 安装

从 [GitHub Releases](https://github.com/keakon/chord/releases) 下载对应系统和架构的压缩包，解压后把 `chord` 加入 `PATH`。不需要安装 Go；运行以下命令确认安装：

```bash
chord --version
```

macOS 下载版首次运行可能被系统阻止，处理步骤见[快速开始](./docs/quickstart_CN.md#1-安装)。

也可用 Go 1.27.0 或更新版本安装：

```bash
go install github.com/keakon/chord/cmd/chord@latest
```

默认的 `GOTOOLCHAIN=auto` 会在需要时自动下载所需工具链。

### 2. 在项目里启动

在交互式终端进入项目目录：

```bash
cd my-project
chord
```

首次运行且没有配置文件时，向导会引导你填写模型 API 地址、模型名称和 API key，或通过 Codex OAuth 登录。

查看已有账号或 API 是否支持，见[账号与 API 支持](./docs/model-choice_CN.md#账号与-api-支持)；详细步骤见[快速开始](./docs/quickstart_CN.md)。

### 3. 发送第一个任务

直接说明你想让它做什么，按 `Enter` 发送。比如先让它读一遍你的项目：

```text
请解释这个项目的主要模块和测试入口，先不要修改文件。
```

先看它的回答。默认配置下，Chord 会直接修改文件和执行命令，只有用 `delete` 工具删除文件时需要确认；想先审批再执行，见[权限与安全](./docs/permissions-and-safety_CN.md)。熟悉项目后，交给它一个具体改动：它负责改代码、跑测试，你在终端里检查 diff。

按 `Esc` 进入 Normal 模式，再按 `q` 退出。下次在同一项目目录运行 `chord --continue` 继续最近的会话。

## 更多功能

- [模型自动切换](./docs/configuration_CN.md#模型池)：配置多个模型，请求失败时自动尝试切换到下一个模型并重试。
- [导入已有会话](./docs/usage_CN.md#导入外部会话)：导入 Claude Code、Codex 或 OpenCode 的会话历史，在 Chord 中继续工作。
- [Vim 风格键盘操作](./docs/keybindings_CN.md)：用键盘浏览会话、操作工具卡和切换面板。

## 按主题查文档

- [快速开始](./docs/quickstart_CN.md)：安装、接入模型，完成第一个任务
- [使用指南](./docs/usage_CN.md)：日常操作、恢复会话、长任务和并行工作
- [模型配置](./docs/model-configs_CN.md) · [配置示例](./docs/examples/index_CN.md)：接入模型或自定义网关
- [权限与安全](./docs/permissions-and-safety_CN.md)：决定哪些操作需要确认
- [扩展与定制](./docs/customization_CN.md)：配置多 Agent、技能、代码诊断和外部工具
- [Headless 集成](./docs/headless_CN.md)：通过 `chord headless` 从其他入口操控
- [ACP Agent 模式](./docs/acp_CN.md)：通过 `chord acp` 让 ACP 客户端（如 Zed）直接调用 Chord
- [排障](./docs/troubleshooting_CN.md) · [完整文档目录](./docs/index_CN.md)

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
