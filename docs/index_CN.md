# Chord 文档

**任务跑得更快，单次花费更低，内存占用保持很小。** Chord 为长会话设计：key 或模型失败时自动切换，用请求级剪裁加压缩控制成本，内存占用保持低位。

第一次使用时，建议按这个顺序走：先看[快速开始](./quickstart_CN.md)跑通第一个任务，再从[模型配置速查](./model-configs_CN.md)复制服务商配置，最后在[权限与安全](./permissions-and-safety_CN.md)定好审批规则；已经在用时，按下面的目标找答案。

一次 DeepSWE v1.1 任务实测里 Chord 用时最短、成本最低（6m37s，￥0.348），完整表格与测量方法见[性能](./performance_CN.md)。

[English](./index.md)

## 开始使用

- [快速开始](./quickstart_CN.md)：安装并完成第一次任务
- [使用指南](./usage_CN.md)：日常操作、会话恢复、长任务与并行工作
- [快捷键](./keybindings_CN.md)：查找按键和自定义键位

## 配置模型

- [配置与认证](./configuration_CN.md)：配置文件、凭据、模型池与字段参考
- [模型配置速查](./model-configs_CN.md)：按服务商选择可复制的配置
- [推理与思考](./reasoning_CN.md)：选择思考设置，了解用量影响
- [上下文管理](./context-management_CN.md)：了解压缩、剪裁和长会话调优

## 工具与安全

- [权限与安全](./permissions-and-safety_CN.md)：设置审批规则，了解操作风险
- [内置工具](./tools_CN.md)：查找工具名、用途和重要限制
- [编辑工具](./edit-tools_CN.md)：理解文件修改、部分成功和重试

## 扩展与集成

- [扩展与定制](./customization_CN.md)：配置角色、技能、代码诊断和外部工具
- [Hooks](./hooks_CN.md)：自动通知、检查和处理工具结果
- [Headless 集成](./headless_CN.md)：从脚本或其他界面控制 Chord

## 示例配置

- [示例配置库](./examples/index_CN.md)：按场景选择完整配置
- [最小可用](./examples/examples-minimal_CN.md)：一个服务商、一个模型池
- [Codex + LSP](./examples/examples-codex-workstation_CN.md)：登录、代码诊断和审查角色
- [OpenAI 兼容网关](./examples/examples-openai-compat_CN.md)：多密钥和备用接口
- [团队方案](./examples/examples-team_CN.md)：项目配置和多角色分工

## 查阅与排障

- [CLI 参考](./cli_CN.md)：命令、选项和示例
- [目录与路径](./paths_CN.md)：配置、会话、缓存放在哪里，哪些可以删
- [环境变量](./environment_CN.md)：路径、代理和调试设置
- [平台支持](./platforms_CN.md)：按系统查找功能差异
- [性能](./performance_CN.md)：实测数据、测试条件和变慢时的排查
- [常见问题排查](./troubleshooting_CN.md)：从症状找到下一步操作
- [术语表](./glossary_CN.md)：查找文档中的术语
