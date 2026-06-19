# 编辑工具：Patch vs Edit

Chord 提供两种互补的文件编辑工具，针对不同模型的训练背景进行了优化。

## 快速对比

| 特性 | **Patch 工具** | **Edit（替换）工具** |
|---------|----------------|-------------------------|
| **格式** | 统一差异块（`@@` 风格） | 文本匹配（old_string → new_string） |
| **最适合** | 使用 OpenAI `apply_patch` 训练的模型 | 使用 Claude Code 或类似替换接口训练的模型 |
| **位置控制** | 上下文行 + 可选的头部锚点 | 精确字符串匹配 |
| **多次出现** | 不适用（基于上下文） | `replace_all` 参数 |
| **典型模型** | gpt-5.5, gpt-5.3-codex, o3, o4 | Claude, Qwen, GLM, MiniMax, DeepSeek, Gemini |

## 工具选择

Chord **自动根据当前模型选择**合适的工具：

- **GPT/o 系列模型** → Patch 工具（@@-风格块）
- **所有其他模型** → Edit 工具（old_string/new_string）

你无需手动选择——系统只会向每个模型暴露合适的工具。

---

## Patch 工具（@@-风格）

### 格式

```diff
@@
 上下文行
-删除的行
+添加的行
 上下文行
```

### 何时使用

- 你的模型使用 OpenAI 的 `apply_patch` 或类似的基于补丁的接口训练
- 需要通过上下文行进行精确的位置控制
- 改动是局部的，可以用周围代码进行锚定

### 示例

```json
{
  "path": "main.go",
  "patch": "@@\n func main() {\n-\tfmt.Println(\"hello\")\n+\tfmt.Println(\"hello, world\")\n }"
}
```

### 可选的头部锚点

你可以添加头部行来帮助定位模糊的代码块：

```diff
@@ func processUser(id int) error {
 if id < 0 {
-  return errors.New("invalid")
+  return fmt.Errorf("invalid user ID: %d", id)
 }
```

**重要提示**：只使用你已验证存在于文件中的头部。通用头部（例如单独的 `@@`）会被视为软锚点，如果找不到会回退到主体匹配。

### 错误消息

- **"hunk not found"**：上下文行与当前文件状态不匹配。重新读取文件并重新生成补丁。
- **"patch makes no changes"**：所有行都是上下文（空格前缀）；添加 `-` 和 `+` 行进行实际更改。
- **"multiple possible locations"**：块主体匹配多个位置。添加更多上下文或使用头部锚点。

---

## Edit（替换）工具

### 格式

```json
{
  "path": "main.go",
  "old_string": "fmt.Println(\"hello\")",
  "new_string": "fmt.Println(\"hello, world\")",
  "replace_all": false
}
```

### 何时使用

- 你的模型没有专门针对补丁格式进行训练
- 改动很直接：查找精确文本 → 替换为新文本
- 你想在文件中重命名变量/标识符（`replace_all: true`）

### 参数

- **`old_string`**（必需）：要查找的精确文本。必须精确匹配缩进、空白和换行符。
- **`new_string`**（必需）：替换文本。
- **`replace_all`**（可选）：`true` 替换所有出现，`false`（默认）仅替换第一个。

### 示例：单次替换

```json
{
  "path": "server.go",
  "old_string": "const port = 8080",
  "new_string": "const port = 3000"
}
```

### 示例：重命名变量

```json
{
  "path": "handler.go",
  "old_string": "userID",
  "new_string": "userId",
  "replace_all": true
}
```

### 错误消息

- **"old_string not found in file"**：精确文本不存在。检查空白、缩进和换行符。
- **"old_string found N times"**：找到多个匹配。可以：
  - 添加更多上下文使其唯一
  - 设置 `replace_all: true` 如果你想替换所有出现
- **"old_string and new_string are identical"**：无需更改。

### 尾随换行符容错

工具会自动处理轻微的尾随换行符差异：

- 如果 `old_string` 有最后的 `\n` 但匹配项没有（反之亦然），并且匹配是唯一的，编辑会继续。
- 这减少了因换行符不匹配导致的重试。

---

## 建议工作流

两个编辑工具都不要求预先 `read`：它们在执行时都会读取当前磁盘内容。为了可靠编辑，仍建议遵循以下做法：

1. **在尚未确认精确文本、路径或 hunk 锚点时，先检查目标区域**。可以使用 `read`、`grep` 或 `lsp`。
2. **使用最小的唯一块**（2-4 行）。大的上下文块更容易过时。
3. **失败后重新读取**。如果块或字符串匹配失败，文件可能已更改——在重试之前再次读取。

---

## 特定任务指南

### 局部更改

两个工具都很适用。根据模型训练选择：

- **Patch**：当你需要位置控制时更好（例如，"更改此函数中的第一个出现"）。
- **Edit**：对于具有清晰边界的简单查找替换更好。

### 重命名/重构

- **Edit 配合 `replace_all: true`**：在一个文件中重命名变量。
- **LSP 工具**：用于跨多个文件的符号感知重命名。

### 大规模更改

这两个工具都不适合：

- 创建新文件 → 使用 **Write**
- 删除文件 → 使用 **Delete**
- 跨多个文件的批量文本替换 → 使用 **Shell** 配合 `sd` 或 `sed`
- 跨文件的符号重命名 → 使用 **LSP**

---

## 权限

两个工具共享**文件权限族**（基于路径的授权）。对路径的单次批准适用于两个编辑工具。

### 权限配置

配置任一编辑工具名即可；一个编辑器的规则会作用到另一个编辑器，除非另一个编辑器也有自己的显式规则：

**统一配置**（推荐）：

```yaml
permission:
  edit: allow  # patch 和 edit 工具都允许
```

**禁用某一种格式**（高级）：

```yaml
permission:
  edit: allow
  patch: deny  # GPT/o 系列模型会退回使用 edit
```

**权限回退规则**：

- 如果仅配置了 `edit`，`patch` 继承相同的权限
- 如果仅配置了 `patch`，`edit` 继承相同的权限
- 这也包括 `deny`：`edit: deny` 也会禁用 `patch`，除非 `patch` 同时有自己的显式规则
- 如果两者都配置了，各自使用自己的显式规则
- 单独的 `edit` 或 `patch` 规则会同时作用于两个工具，并覆盖通配符规则

**示例**：

- `edit: allow` → 两个工具都允许；GPT/o 系列模型通常看到 `patch`，其他模型通常看到 `edit`
- `edit: allow, patch: deny` → patch 拒绝，edit 允许；GPT/o 系列模型退回使用 `edit`
- `patch: allow, edit: deny` → patch 允许，edit 拒绝；非 GPT 模型退回使用 `patch`
- `*: deny, patch: allow` → 两个工具都允许（edit 继承 patch 规则）
- `*: allow, patch: deny` → 两个工具都拒绝（edit 继承 patch 拒绝）

---

## 技术说明

### 为什么是两个工具？

模型根据其训练数据表现出强烈的偏好：

- GPT 模型在训练中见过大量的 `@@`-风格补丁（OpenAI 的 `apply_patch`）
- Claude、Qwen 和类似模型在直观的查找替换格式上表现更好

实证测试（Aider 的 edit-bench、内部 chord 指标）显示：

- **GPT 模型**：补丁格式成功率 91-96%，替换格式约 70%
- **非 GPT 模型**：替换格式成功率 81-96%，补丁格式 44-79%

### Token 效率

- **Replace**：对于小编辑通常减少 20-40% 的 token（无需上下文行）
- **Patch**：由于上下文需要更多 token，但对复杂编辑具有更好的精度

### 实现

- 两个工具在写入前都验证块/字符串
- 两个都支持 LSP 集成（工作区通知）
- 两个都参与相同的并发编辑控制（基于路径的锁定）
- 两个都生成统一差异用于显示（无论输入格式如何）

---

## 从单工具系统迁移

如果你正在从只有一个编辑工具的系统升级：

1. **无需操作**：Chord 自动为每个模型选择正确的工具
2. **SessionImport 兼容性**：历史编辑调用会被映射：
   - `codex` 提供者 → `patch` 工具
   - 其他提供者 → `edit` 工具
3. **权限连续性**：两个工具共享文件权限族

---

## 常见问题

**Q：我可以强制使用特定工具吗？**
A：工具选择是自动的且特定于模型。覆盖它可能会降低成功率。

**Q：如果我的模型未被识别怎么办？**
A：默认情况下，未识别的模型使用 `edit`（替换）工具。GPT/o 系列模型使用 `patch`。

**Q：两个工具支持相同的文件类型吗？**
A：是的。两者都适用于任何文本文件（检测到的编码：UTF-8、UTF-16、GB18030 等）。二进制文件会被拒绝。

**Q：我可以在同一对话中使用两个工具吗？**
A：一次只有一个工具可见，基于活动模型。你不会同时看到两者。

**Q：`hashline`（内容寻址锚点）怎么样？**
A：目前未启用。这是在生产工作负载中验证后的潜在未来增强功能。

---

## 另请参阅

- [工具参考](./tools_CN.md) – 所有可用工具
- [权限系统](./permissions-and-safety_CN.md) – 文件访问控制的工作原理
- [LSP 集成](./tools_CN.md) – 符号感知操作
