# 快速开始

准备可用的模型 API key，或支持 Codex 的账号。具体接入条件见[账号与 API 支持](./model-choice_CN.md#账号与-api-支持)。

## 1. 安装

### Homebrew（macOS 和 Linux）

已安装 [Homebrew](https://brew.sh) 时，运行：

```bash
brew install keakon/tap/chord
```

Homebrew 会自动添加 [Chord 软件源（tap）](https://github.com/keakon/homebrew-tap)。有匹配当前系统的预编译安装包（bottle）时，安装无需 Go；否则 Homebrew 会安装构建依赖并从源码编译。

后续升级：

```bash
brew update
brew upgrade chord
```

### 下载二进制文件

从 [GitHub Releases](https://github.com/keakon/chord/releases) 下载对应系统和架构的压缩包，解压后把 `chord` 加入 `PATH`。这些二进制文件不需要 Go。

### 用 Go 安装

使用 Go 1.27.0 或更新版本：

```bash
go install github.com/keakon/chord/cmd/chord@latest
```

### 确认安装

无论选择哪种方式，安装后都可运行：

```bash
chord --version
```

### macOS 下载版被系统阻止

手动从 GitHub Releases 下载的二进制文件首次运行时，macOS 可能因文件来自互联网且未公证而阻止运行。遇到这种情况时执行：

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

- **API key**：填写服务商给出的完整 API URL、模型名称和密钥；需要代理时可在向导中填写。
- **Codex OAuth**：按提示完成登录，无需手动填写 API key。

向导会创建最小可用的 `config.yaml`，必要时创建 `auth.yaml`，并显示保存位置；已有匹配凭据时会尽量复用。首次进入项目时，Chord 也会按需创建 `.chord/`。

API 地址格式、手动配置和无交互终端的初始化见[更多配置](#更多配置)。

## 3. 发送第一个任务

直接说明你想让它做什么，按 `Enter` 发送。比如先让它读一遍你的项目：

```text
请解释这个项目的主要模块和测试入口，先不要修改文件。
```

先看它的回答。默认配置下，Chord 会直接修改文件和执行命令，只有用 `delete` 工具删除文件时需要确认；遇到确认框时，先阅读待执行操作，再决定是否允许。想让更多操作先经你审批，见[权限与安全](./permissions-and-safety_CN.md)。熟悉项目后，交给它一个具体改动：它负责改代码、跑测试，你在终端里检查 diff。

退出时按 `Esc` 进入 Normal 模式，再按 `q`；也可在 2 秒内连按两次 `Ctrl+C`。下次在项目目录中运行：

```bash
chord --continue
```

即可继续最近的会话。选择其他会话或导入外部历史，见[会话](./usage_CN.md#会话)。

## 请求失败时

若 API key 配置无法请求模型，退出 Chord 后运行：

```bash
chord doctor models
```

先解决认证或连接错误，再重新运行 `chord`。错误排查见[常见问题排查](./troubleshooting_CN.md)。

## 更多配置

### 用模型目录快速配置

已有 API key 时，可以直接跳过初始化向导：

```bash
export OPENAI_API_KEY="你的 API key"
chord config add openai/gpt-6.1-sol
chord
```

添加命令会创建配置并填入官方接入信息。想选其他模型，直接运行 `chord config add`，从已验证目录里挑一个即可。
命令会提示应设置哪个密钥环境变量；已有 provider 的地址和凭据会继续沿用。

用 `chord doctor config` 检查配置问题和当前生效的目录快照。
自定义网关及更多接入方式见[模型配置](./model-configs_CN.md)。

### 手动配置与初始化条件

初始化向导接受以 `/responses`、`/messages`、`/chat/completions` 结尾的 API URL，Gemini 则使用 `/v1beta/` 等版本根地址，向导会按后缀推荐服务商和模型的默认值。地址格式、凭据和模型池设置见[配置与认证](./configuration_CN.md)；手写配置可从[示例配置库](./examples/index_CN.md)选一个起点。

初始化需要有控制终端：仅仅重定向 stdin 不会跳过向导，只要还能打开控制 TTY，交互就发生在那里；完全没有控制终端时 Chord 直接报初始化错误，不会停下来等输入。无交互终端的初始化问题见[常见问题排查](./troubleshooting_CN.md#启动失败)。

## 按需继续

- [权限与安全](./permissions-and-safety_CN.md)：决定哪些操作需要确认。
- [使用指南](./usage_CN.md)：日常操作和会话恢复。
- [模型池](./configuration_CN.md#模型池)：配置多个模型，请求失败时自动尝试切换并重试。
- [Worktree](./usage_CN.md#worktree)：在独立工作目录中并行推进任务。
- [扩展与定制](./customization_CN.md)：配置多 Agent、技能和外部工具。
