# Chord 文档

面向终端用户：安装、配置、日常使用、定制扩展、排障。

- English: 见 [index.md](./index.md)

先从开箱即用的内容开始；需要更强能力时，再进入扩展与进阶工作流。

## 按任务找文档

- **快速启动**：[快速开始](./quickstart_CN.md)
- **配置模型**：[配置与认证](./configuration_CN.md) · [示例配置库](./examples/index_CN.md)
- **远程控制**：[Headless](./headless_CN.md) · [权限与安全](./permissions-and-safety_CN.md)
- **长任务**：[使用指南 — `/loop`](./usage_CN.md#loop--持续执行模式)
- **扩展定制**：[扩展与定制](./customization_CN.md) · [Hooks](./hooks_CN.md)
- **理解性能**：[性能优化](./performance_CN.md)
- **排障**：[常见问题排查](./troubleshooting_CN.md)

## 入门

- [快速开始](./quickstart_CN.md) —— 几分钟跑起来
- [使用指南](./usage_CN.md) —— TUI 基础、会话、常用命令、headless 模式
- [术语表](./glossary_CN.md) —— 文档中反复出现的概念

## 参考

- [CLI](./cli_CN.md) —— 所有命令、子命令、flag
- [配置与认证](./configuration_CN.md) —— `config.yaml`、`auth.yaml`、provider、模型池、完整速查表
- [快捷键](./keybindings_CN.md) —— 完整键位与自定义方式
- [目录与路径](./paths_CN.md) —— 配置 / state / cache / 项目级布局，哪些可删
- [环境变量](./environment_CN.md) —— Chord 读取的所有 `CHORD_*` / `XDG_*` / 代理变量
- [平台支持](./platforms_CN.md) —— macOS / Linux / Windows / WSL 各支持到什么程度
- [性能优化](./performance_CN.md) —— TUI 流式输出、渲染、滚屏与 benchmark 指南

## 进阶

- [扩展与定制](./customization_CN.md) —— agents、skills、MCP、按需配置 LSP、自定义 slash 命令
- [Hooks](./hooks_CN.md) —— 14 个触发点、payload 协议、示例
- [示例配置库](./examples/index_CN.md) —— 4 套可直接复制粘贴的 `config.yaml`

## 集成

- [Headless](./headless_CN.md) —— `chord headless` JSON 控制面与 `chord-gateway`

## 安全

- [权限与安全](./permissions-and-safety_CN.md) —— 权限模型与安全边界

## 排障

- [常见问题排查](./troubleshooting_CN.md) —— 症状、常见原因、日志采集
