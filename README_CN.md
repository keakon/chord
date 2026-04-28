# Chord

[![CI](https://github.com/keakon/chord/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/keakon/chord/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/keakon/chord?display_name=release)](https://github.com/keakon/chord/releases) [![Go Version](https://img.shields.io/github/go-mod/go-version/keakon/chord)](./go.mod) [![License](https://img.shields.io/github/license/keakon/chord)](./LICENSE)

一款轻量、本地优先的终端 Coding Agent。低资源占用、可靠长会话、灵活模型编排、Vim-like 操作、多 Agent 协作，以及可通过网关远程操控的 headless 模式——让 AI 编码体验更稳定、可预期。

- English: [README.md](./README.md)
- 用户文档：[docs/index_CN.md](./docs/index_CN.md)（建议从 [快速开始](./docs/quickstart_CN.md) 开始）
- Gateway: [keakon/chord-gateway](https://github.com/keakon/chord-gateway)

## 为什么值得一试？

- **轻量，适合长期在线**：低内存、低 CPU 占用，适合部署在小内存 VPS 上，也适合作为随时可用的个人 Coding Agent。
- **不用再猜“是不是卡住了”**：调用模型时会展示精确的网络/请求状态和已等待时间。
- **键盘优先的终端 UI**：Vim-like normal/input 模式、消息历史搜索、快速切换模型，模式切换时自动切换输入法。
- **图片输入**：粘贴或附加图片，在支持的终端中预览，并发送给多模态模型。
- **LSP 加持的编码上下文**：连接本地 language server，获取静态诊断和 definition、references、implementation 等语义导航能力。
- **长会话更可靠**：compaction 算法将长会话压缩为上下文摘要，尽量保留后续工作所需的信息。
- **Provider / model / key 调度**：多 provider、model 和 API key 配置，支持自动重试、故障切换和负载均衡。
- **Codex 额度实时可见**：实时显示 Codex 订阅的剩余额度和重置时间。
- **多 Agent 协作**：主 Agent 与多个 SubAgent 协作，可查看各自上下文并切换视图。
- **远程操控**：`chord headless` 提供 stdio JSONL 控制面，配合 `chord-gateway` 可通过微信、飞书等聊天入口操控。
- **电源状态友好**：工作进行中自动阻止系统休眠，空闲后允许系统恢复正常休眠。

## 快速开始

需要 Go 1.26+。

```bash
go install github.com/keakon/chord/cmd/chord@latest
chord
```

用于远程 gateway、bot 或自动化脚本：

```bash
chord headless
```

凭据配置、provider 设置和首次运行说明见 [快速开始](./docs/quickstart_CN.md)。

## 文档

- [文档首页](./docs/index_CN.md)
- [快速开始](./docs/quickstart_CN.md)
- [使用指南](./docs/usage_CN.md)
- [配置与认证](./docs/configuration_CN.md)
- [权限与安全](./docs/permissions-and-safety_CN.md)
- [扩展与定制](./docs/customization_CN.md)
- [常见问题排查](./docs/troubleshooting_CN.md)
- [Headless 集成](./docs/headless_CN.md)

## 项目链接

- 贡献指南：[CONTRIBUTING.md](./CONTRIBUTING.md)
- 版本变更记录：[CHANGELOG_CN.md](./CHANGELOG_CN.md)
- Gateway: [keakon/chord-gateway](https://github.com/keakon/chord-gateway)

## License

MIT License.
