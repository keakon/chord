# 权限与安全

Chord 是一个可以读取文件、修改文件、执行命令并调用外部工具的 coding agent。公开使用前，你应该先明确它的权限模型和安全边界。

## 核心原则

- 默认把高风险能力设为 `ask`
- 对明显危险或不需要的能力使用 `deny`
- 只对低风险、可预期的动作使用 `allow`
- 把 API keys 放在 `auth.yaml` 或环境变量里，不要写进项目文件

## 权限模型

常见权限状态：

- `allow`：自动允许
- `ask`：执行前要求确认
- `deny`：直接拒绝

权限可以在 Agent 配置中定义，例如：

```yaml
permission:
  Read: allow
  Grep: allow
  Glob: allow
  Write: ask
  Edit: ask
  Bash:
    "go test ./...": allow
    "rm *": deny
```

> 权限属于 Agent 级配置，而不是简单的全局开关。

## Bash 与 shell 风险

`Bash` 能执行系统命令，因此应格外谨慎。

建议：

- 默认把文件删除、批量改写、网络下载、数据库操作保留为 `ask` 或 `deny`
- 只对少量可预期的开发命令设置 `allow`
- 不要把“权限匹配”理解为安全沙箱

**重要**：Chord 的权限匹配是产品层面的风险控制，不是操作系统级隔离或安全沙箱。

## 文件修改风险

`Write`、`Edit`、`Delete` 都会直接改动工作区文件。

建议：

- 在重要仓库中配合 Git 使用，方便回滚
- 对生产配置、部署脚本、密钥文件保持 `ask`
- 对生成文件或测试工件目录做更细粒度规则

## 凭据与配置

- API keys 建议放在 `~/.config/chord/auth.yaml`
- 也可以通过环境变量引用
- 不要把真实密钥写入示例配置、脚本或项目仓库
- 为 `auth.yaml` 设置严格权限，例如 `chmod 600 ~/.config/chord/auth.yaml`

## Headless 模式安全边界

`chord headless` 适合作为 bot / gateway 的底层控制面，但它本身不负责多租户隔离、浏览器安全边界或权限托管。

如果你把它接到聊天平台、自动化系统或团队服务中，应该在外层额外控制：

- 允许访问哪些工作目录
- 允许调用哪些命令
- 谁可以批准高风险操作
- 事件如何审计与留痕

## 网络与外部集成

Chord 支持接入：

- provider API
- LSP
- MCP
- Hooks
- 本地 shell 命令

这些能力都会扩大运行时边界。接入前建议逐项确认：

- 是否真的需要该能力
- 它会读写哪些资源
- 出错时如何回滚或停用
- 是否会把敏感数据带到外部服务

## 使用建议

- 初次使用时，从最小 provider 配置和最小权限开始
- 在个人仓库中先观察一段时间，再逐步放宽权限
- 在共享仓库或团队环境中，不要默认全局 `allow`
- 对自动化 Hook 和 MCP 工具做最小权限暴露

## 相关文档

- [配置与认证](./configuration_CN.md)
- [扩展与定制](./customization_CN.md)
- [Headless 集成](./headless_CN.md)
