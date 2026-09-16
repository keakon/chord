# 快速开始

## 1. 安装

下载预构建二进制不需要安装 Go。用 `go install` 或从源码构建时，需要 Go 1.27.0+。

```bash
go install github.com/keakon/chord/cmd/chord@latest
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

## 2. 第一次运行

在交互式终端里进入你的项目，再启动 Chord：

```bash
cd my-project
chord
```

缺少 `config.yaml` 时，初始化向导会引导你选择接入方式：

- **API key**：准备服务商给出的完整 API URL、模型名称和密钥；需要代理时可在向导中填写。
- **Codex OAuth**：按提示完成登录，无需手动填写 API key。

向导会创建最小可用的 `config.yaml`，必要时创建 `auth.yaml`，并显示保存位置；已有匹配凭据时会尽量复用。首次进入项目时，Chord 也会按需创建 `.chord/`。

想手写配置？从[示例配置库](./examples/index_CN.md)选一个起点。API URL 格式、凭据和模型池设置见[配置与认证](./configuration_CN.md)。无交互终端的初始化问题见[常见问题排查](./troubleshooting_CN.md)。

## 3. 检查连接

完成配置后就可以发送消息。若 API key 配置无法请求模型，退出 Chord 后运行：

```bash
chord doctor models
```

先解决认证或连接错误，再重新运行 `chord`。错误排查见[常见问题排查](./troubleshooting_CN.md)。

## 4. 首次交互

直接说明你想让它做什么，按 `Enter` 发送。比如先让它读一遍你的项目：

```text
请解释这个项目的主要模块和测试入口，先不要修改文件。
```

查看回答和工具执行结果；遇到权限确认时，先阅读待执行操作，再决定是否允许。熟悉项目后，可以继续要求实现一个具体改动，并检查产生的差异。

退出时按 `Esc` 进入 Normal 模式，再按 `q`；也可在 2 秒内连按两次 `Ctrl+C`。

## 5. 常用启动方式

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

## 6. 下一步阅读

按这个顺序读：

1. [权限与安全](./permissions-and-safety_CN.md)：第一次改文件前先定好审批规则。
2. [使用指南](./usage_CN.md)：日常操作、会话与长任务。
3. [配置与认证](./configuration_CN.md)：服务商、凭据和模型池。
4. [扩展与定制](./customization_CN.md)：角色、技能与项目配置。
5. [常见问题排查](./troubleshooting_CN.md)：出错时看这里。
