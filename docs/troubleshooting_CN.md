# 常见问题排查

本页面向使用者，聚焦安装、配置、认证、会话、扩展和性能相关的常见问题。

## 启动失败

先检查：

- Go 版本是否满足要求
- 是否使用了正确入口：`go run ./cmd/chord/`
- `config.yaml` / `auth.yaml` 是否存在明显 YAML 格式错误

可执行：

```bash
go test ./cmd/chord/...
```

如果是构建好的二进制，建议重新运行并查看终端错误输出。

## 401 / 403 / 认证失败

检查：

- `auth.yaml` 中 provider 名称是否与 `config.yaml` 对应
- API key 是否有效
- OAuth provider 是否配置了 `preset: codex`

可执行：

```bash
chord test-providers
```

## 429 / quota exhausted

常见原因：

- key 已达配额上限
- provider 限流
- 并发或高频请求触发了速率限制

建议：

- 换一个 key
- 降低并发或减少重试
- 检查是否存在异常循环调用

## TUI 启动了，但无法正常请求

检查：

- 当前 provider / model 是否存在
- 网络是否能访问对应 API
- 代理配置是否生效

例如：

```bash
curl -I https://api.anthropic.com
curl -I https://api.openai.com/v1
```

## MCP 一直未就绪

先确认：

- MCP 地址是否可访问
- 配置名称是否正确
- 本地模式下是否只是还在异步初始化

注意：启动后的短暂灰色 pending 状态不一定是错误。

## 写文件后没有诊断

如果你已经配置了 LSP，但写文件后没有看到诊断：

- 检查本机是否安装了对应语言服务器
- 检查 `lsp` 配置格式是否正确
- 确认目标文件类型是否与 `file_types` 匹配

## 会话恢复异常

如果 `--continue` 或 `--resume` 看起来没有按预期工作：

- 确认当前目录是否与原会话属于同一项目
- 尝试显式使用 `--resume <session-id>`
- 检查是否只是恢复过程较慢而非真的丢失

## 性能问题

如果你感觉滚屏、流式输出或大消息渲染明显变慢：

- 先缩小当前会话上下文规模
- 检查是否存在异常长输出
- 尝试在不同终端中对比

如果你在维护项目本身，还可以进一步使用仓库内的性能检查脚本与 pprof。

## 何时查看日志

遇到以下问题时，优先查看日志：

- provider 请求失败但终端只显示摘要错误
- MCP / LSP 初始化异常
- hook 执行结果与预期不符
- headless 集成事件不完整

默认日志目录：`${XDG_STATE_HOME:-~/.local/state}/chord/logs/`

可以通过 `--logs-dir <path>` 或环境变量 `CHORD_LOGS_DIR=<path>` 覆盖。快速复现并收集日志：

```bash
chord --logs-dir ./chord-logs
```

默认日志目录：`${XDG_STATE_HOME:-~/.local/state}/chord/logs/`

可以通过 `--logs-dir <path>` 或环境变量 `CHORD_LOGS_DIR=<path>` 覆盖。快速复现并收集日志：

```bash
chord --logs-dir ./chord-logs
```

## 相关文档

- [快速开始](./quickstart_CN.md)
- [配置与认证](./configuration_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [Headless 集成](./headless_CN.md)
