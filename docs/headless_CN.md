# Headless 集成

`chord headless` 是 Chord 的轻量控制面入口，适合 bot、gateway 或自动化脚本集成。

## 定位

- 无 TUI
- 通过 stdio 交互
- 输入为 JSON 命令
- 输出为 JSONL 事件

它适合做外层集成，但不负责浏览器前端、多租户隔离或完整权限托管。

## 启动

```bash
chord headless
# 或
go run ./cmd/chord/ headless
```

## 基本协议

- stdin：每行一个 JSON 命令
- stdout：每行一个 JSON envelope

常见命令：

- `subscribe`：订阅事件类型
- `status`：获取当前状态快照
- `send`：发送用户消息
- `confirm`：批准或拒绝确认请求
- `question`：回答问题
- `cancel`：取消当前 turn

示例——发送用户消息：

```json
{"type":"send","content":"请总结一下项目结构。"}
```

## 常见事件

- `ready`：headless 已启动
- `activity`：agent 进入新阶段
- `assistant_message`：assistant 消息已完成并可安全消费
- `confirm_request`：需要用户确认
- `question_request`：需要用户回答
- `idle`：agent 已回到可接收输入状态
- `error`：运行时出错
- `notification`：适合外层系统转成用户提醒

示例——assistant 消息事件：

```json
{"type":"assistant_message","payload":{"agent_id":"main","text":"该项目有三个主要模块：...","tool_calls":null}}
```

## 适合的使用方式

- 由外层 gateway 管理进程生命周期
- 由外层系统决定哪些事件需要展示给终端用户
- 在外层做工作目录、权限、审计与租户隔离控制

## 不适合替代什么

`chord headless` 不是：

- 浏览器应用
- 多租户安全边界
- 完整的权限沙箱

## 相关文档

- [使用指南](./usage_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
