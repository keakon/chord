# 图片生成与编辑

你可以让 Chord 生成图片，也可以指定参考图和编辑要求。对话模型配置了获准的服务端生图工具时优先使用它；否则使用内置 `generate_image`，图片模型和对话模型分别选择。

内置生图默认关闭。模型通过 `image_generation: {}` 标记为图片模型，可用 `type` 显式声明图片协议，顶层 `image_generation.model_pool` 引用专用图片池，复用所属 provider 在 `auth.yaml` 中的 API key：

```yaml
providers:
  image_service:
    api_url: https://images.example.invalid/v1/images/generations
    models:
      gpt-image-1.5:
        image_generation: {}
  aistudio:
    api_url: https://generativelanguage.googleapis.com/v1beta/
    models:
      gemini-3.1-flash-image-preview:
        image_generation: {}

model_pools:
  image-generation:
    - image_service/gpt-image-1.5
    - aistudio/gemini-3.1-flash-image-preview

image_generation:
  enabled: true
  model_pool: image-generation
  timeout_seconds: 300
```

图片模型不用于对话、压缩或翻译，也不需要加入 Agent 的对话模型池。启用的图片池由 `generate_image` 工具权限控制。各模型沿用所属 provider 的密钥选择、代理、User-Agent 和响应头超时配置。ChatGPT/Codex OAuth 凭据不能用于独立图片 API。Chord 不发送付费能力探测请求。

## 选择图片类型

| 类型 | 接受的型号 | 操作与选项 |
| --- | --- | --- |
| `openai` | `gpt-image-1`、`gpt-image-1-mini`、`gpt-image-1.5`, `gpt-image-2`, `gpt-image-2.5-sunburst`, `gpt-image-2.5-flare` | 文生图、显式参考图编辑；尺寸、质量、背景及 PNG/JPEG/WebP 输出 |
| `gemini` | `gemini-2.5-flash-image`、`gemini-3-pro-image-preview`、`gemini-3.1-flash-image-preview` | 文生图、参考图编辑；宽高比；两个 preview 型号可选 1K/2K/4K |
| `seedream` | `doubao-seedream-4-0-250828`、`doubao-seedream-4-5-251128` | 文生图；2K/4K 尺寸 |
| `xai` | `grok-imagine-image`、`grok-imagine-image-pro`、`grok-imagine-image-2.0` | 文生图；宽高比、1k/2k 分辨率 |
| `qwen` | `qwen-image-3.0-pro`、`qwen-image-3.0`、`qwen-image-2.1-pro`、`qwen-image-2.1-turbo` | 文生图；`auto`、1024×1024、1024×1536、1536×1024 或 2048×2048 |
| `openai-compatible` | 手动填写接口实际接受的型号 | 基础文生图、常用尺寸；解析 URL 或 base64 返回 |

表中列出 Chord 实现的请求契约，目前已通过本地模拟接口测试，尚未逐预设完成真实接口验收；服务和账号也会影响型号能否使用。参考图编辑仅在 OpenAI 和 Gemini 预设中开放。不支持的参数会在生成前报错，透明背景、质量或参考图要求不会被悄悄忽略。

OpenAI 等 Images 接口可在 provider 的 `api_url` 配置完整生图地址，如 `https://images.example.invalid/v1/images/generations`，也可以配置 `/v1/images/` 资源根。Chord 保留配置的服务地址和代理路径前缀；文生图请求使用 `generations`，支持编辑的模型使用同一资源根下的 `edits`。Gemini 可使用 `/v1beta/` 等版本根，由 Chord 追加 `models/{model}:generateContent`；独立图片 provider 也可以直接配置完整的 `.../v1beta/models/{model}:generateContent` 地址，URL 中的型号必须与配置的型号一致。共用 Gemini 对话和图片模型时使用版本根。模型无需重复配置 `base_url`，只有图片接口与 provider 使用不同地址时才用它指定图片资源根。无法识别的显式 `api_url` 会报配置错误，不会改用官方默认地址；共用对话 provider 时，应通过 `base_url` 指定独立图片接口。Images 的显式 `base_url` 也可指定自定义资源根：Chord 在其后追加 `/generations` 或 `/edits`，不会补上 `/images`。

自动识别依据已知型号和标准接口路径。只有 `/v1` 的代理地址无法单独区分 Gemini 与 OpenAI；Gemini 需结合已知 Gemini 图片型号或 `type: gemini`，Images 接口应配置 `/images` 或 `/images/generations`。自定义别名不会自动获得表中型号的编辑或参数能力。内置生图使用一次完整响应，不接受 Gemini 的 `:streamGenerateContent` 流式地址；地址不支持查询参数、片段或内嵌凭据，API key 从 `auth.yaml` 读取。

`type` 可省略：明确的 Gemini 根地址选择 Gemini 协议，已知图片模型名选择表中的对应契约；`/images/` 或 `/images/generations` 地址上的未知模型仅使用 `openai-compatible` 的基础生图能力。地址与模型名冲突、无法推断类型或型号不受支持时，配置校验会报错。显式 `type` 优先，不会发送付费请求探测能力。

图片池按成功游标遍历一轮，只选择能满足完整操作和参数的模型。透明背景、质量和参考图要求不会为切换模型而降低。GPT Image 2/2.5 开放常用 2K/4K 尺寸；2.5 还支持 `xhigh`、`max` 质量。自定义 `openai-compatible` 类型只开放基础文生图和常用尺寸。

没有尺寸、质量、背景或格式要求时，省略相应工具参数，使图片池保留所有可用候选。尺寸默认值在选定目标后补入请求：OpenAI 使用 `auto`，基础 `openai-compatible` 接口使用 `1024x1024`，其余类型使用服务默认值。用户明确指定的参数不会被默认值覆盖；可选参数组合必须由同一个目标支持。

`timeout_seconds` 覆盖内置调用的排队、密钥尝试、生成、下载和保存，默认 300 秒，最多 1800 秒。服务端切换到内置时沿用剩余预算。

## 优先使用服务端工具

在实际调用的对话模型上配置 `native_image_generation`，Chord 会优先声明服务端生图工具，并隐藏同义的内置工具：

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      conversation-model:
        native_image_generation:
          contract: openai.responses.image_generation
          api_url: https://api.openai.com/v1/responses
          preauthorized: true
          model: gpt-image-1.5
          max_uses: 1
```

该配置中的 `model` 是生图型号，对话请求仍使用外层模型。服务端工具要求显式匹配端点、协议和请求级预授权，并遵循 `generate_image` 权限；参数权限、逐调用确认或同步 hook 无法在原生请求中执行时，使用普通内置工具治理。不能将 Codex OAuth 地址当作公开 Responses 生图端点。

只有结构化错误明确证明服务端因不支持、凭据或额度限制而拒绝，且拒绝回执已持久化、内置工具已启用并获准时，才会接续到内置工具。同一用户 turn 后续保持内置模式；新用户消息重新按所选模型配置选择入口。断流、5xx、取消、权限/内容拒绝、结果未知或保存失败都不会自动重放。服务端只返回文本也不会触发再次生图。与原生搜索同时启用时，服务端总调用数使用两者 `max_uses` 的较小值。

## 生成或编辑

例如：“生成一张正方形树木插画，背景透明，保存为 assets/tree.png。”输出目录需要已经存在。不指定工作区路径时，Chord 也会将原图保存在会话目录中。已有输出文件不会被覆盖。参考图路径和输出目录不接受工作区内的符号链接。

编辑时明确指出原图，例如：“以 assets/tree.png 为参考，把树叶改成蓝色。”工具使用 `operation: edit` 和 `reference_images`，不会猜测你指的是最近哪张图片。也可以复用同一会话中之前生成结果返回的 `artifact:images/...` 引用。

每次调用请求一张最终图片，最多接受五张参考图，上传总量不超过 20 MiB。每张原图不超过 32 MiB、3600 万像素。Chord 校验后保存原始字节并保留透明度；主模型默认只接收一张受限预览，沿用现有输入归一化限制。预览会自动进入下一次模型请求，模型无需调用 `view_image` 来查看它。主模型不能看图时仍会收到文件引用。服务多返回图片时，Chord 会保存并报告实际数量，响应上限为五张。

`generate_image`、参考图读取和工作区写入都遵循现有工具权限与 hook。`artifact:` 引用按其实际会话文件路径检查权限，需要确认的读取会显示该路径。启用生图不会让子 Agent 获得原本被禁止的文件访问权限。服务返回下载 URL 时，该地址还需被 `web_fetch` 网络权限允许；受限时会明确报告交付被网络权限阻止。

支持图片显示的终端会在工具卡内按顺序显示全部图片的缩略图。缩略图在后台加载，预览图长边最多为 1024px。点击缩略图或使用查看快捷键可打开全屏查看器查看原图；生成完成不会自动打开查看器。

## 保存结果与中断处理

结果包含操作 ID、会话图片引用、实际 MIME、尺寸、字节数、哈希，以及服务提供的 request ID 和用量。图片计费有自己的维度；缺少价格时标为未知，图片费用不计入对话 token 的费用估算。

结果通过会话相对的 `artifact:images/...` 引用标识原图。恢复会话会保留同一份原图；派生会话会独立保存原图，删除来源会话后也不受影响。你可以通过图片查看器或 `view_image` 查看。文本和 JSON 导出保留图片引用，不内嵌图片字节；转移原图时也需要保留会话的 images 目录。

付费请求中断后，执行结果可能未知。Chord 不会自动重试、更换服务，也不会在恢复会话时重新生图。只有结构化错误明确表示凭据或额度拒绝、请求未执行时，才会遍历该目标的全部密钥及池内其余符合参数要求的目标。生成完成但下载失败时，私有操作回执保留下载 URL；Chord 会在 URL 有效且调用仍有剩余时间时重试下载。工作区副本写入失败，或等待期间输出目录被移动、替换时，结果会提示告警；可使用结果中列出的会话原图。

内置接口失败时，工具会区分凭据错误、额度或账单限制、短期限流及无法确认结果的服务错误。明确被拒绝的凭据或硬配额会停止在当前运行中选择该图片密钥，并继续尝试其他符合条件的密钥和目标；普通对话密钥状态不受影响。短期限流优先采用服务给出的重试时间，并沿用 provider 的重试间隔及上限。所有密钥冷却时会报告剩余时间；应等待提示时间后再请求，不要反复生成或让模型通过 shell 等待绕过。额度或凭据问题需要先处理账户配置。图片接口请求失败会记录到运行日志；最终工具失败也可在错误面板中查看 HTTP 状态、图片服务及型号。诊断只保存限定的协议字段，不保存密钥、原始接口错误正文或签名下载地址。

Chord 在一次工具调用内完成生成、下载和保存。临时下载故障和暂时性文件写入错误会在调用总超时内自动进行有限次数的重试，取消会停止重试；每次下载仍遵循网络权限。权限拒绝、下载地址过期、磁盘空间不足或图片无效，需要处理具体原因。生成成功但交付最终未完成时，结果会明确说明图片已生成以及未完成的环节，不要求模型执行恢复，也不重新生成。成功结果包含经过校验的会话图片引用；支持看图的对话模型还会自动收到图片附件。

ACP 客户端会在工具结果中收到原图。Headless 客户端收到图片引用和有界的原图数据分块，详见[无界面模式的图片交付](headless_CN.md#图片交付)。配套 chord-gateway 会向微信和飞书转发全部图片；飞书超限时发送缩小预览，原图仍保留在 Chord 会话中。
