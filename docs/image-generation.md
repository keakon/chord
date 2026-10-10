# Image generation and editing

Ask Chord to generate an image or edit named references. A selected conversation model with an authorized server image tool uses that tool first; otherwise it uses the local `generate_image` tool with a separately configured image pool.

Local image generation is disabled by default. Mark each image model with `image_generation: {}`; use `type` to declare its protocol explicitly if needed; top-level `image_generation.model_pool` selects a dedicated pool using provider API keys from `auth.yaml`:

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

Image-only models cannot serve conversation, compaction or translation requests and need not join an agent's chat model pools. Access to the configured image pool follows `generate_image` tool permissions. Each model reuses its provider's key selection, proxy, User-Agent and response-header timeout. ChatGPT/Codex OAuth credentials cannot serve the independent image API. Chord sends no paid capability probes.

## Choose an image type

| Type | Accepted models | Operations and options |
| --- | --- | --- |
| `openai` | `gpt-image-1`, `gpt-image-1-mini`, `gpt-image-1.5`, `gpt-image-2`, `gpt-image-2.5-sunburst`, `gpt-image-2.5-flare` | Generation and explicit reference editing; size, quality, background and PNG/JPEG/WebP output |
| `gemini` | `gemini-2.5-flash-image`, `gemini-3-pro-image-preview`, `gemini-3.1-flash-image-preview` | Generation and reference editing; aspect ratio; 1K/2K/4K on the two preview models |
| `seedream` | `doubao-seedream-4-0-250828`, `doubao-seedream-4-5-251128` | Generation; 2K/4K size |
| `xai` | `grok-imagine-image`, `grok-imagine-image-pro`, `grok-imagine-image-2.0` | Generation; aspect ratio and 1k/2k resolution |
| `qwen` | `qwen-image-3.0-pro`, `qwen-image-3.0`, `qwen-image-2.1-pro`, `qwen-image-2.1-turbo` | Generation; `auto`, 1024×1024, 1024×1536, 1536×1024 or 2048×2048 |
| `openai-compatible` | Explicit wire model ID | Basic generation; standard sizes; URL or base64 results |

These are the request contracts Chord implements. They have passed local simulated-interface tests; live-service acceptance has not been completed for each preset. Model access and availability also depend on the service and your account. Reference editing is enabled only for the OpenAI and Gemini presets. Unsupported parameters cause an error before generation; Chord does not silently discard a transparency, quality or reference-image requirement.

For OpenAI-style Images APIs, set the provider's `api_url` to a full generation endpoint such as `https://images.example.invalid/v1/images/generations`, or to the `/v1/images/` resource root. Chord preserves the configured service and proxy path prefix; generation uses `generations`, while models that support editing use `edits` under the same resource root. Gemini accepts a version root such as `/v1beta/`, appending `models/{model}:generateContent`. A dedicated image provider can also use the full `.../v1beta/models/{model}:generateContent` endpoint; the model in the URL must match the configured model. Use a version root when sharing Gemini chat and image models. Models need no repeated `base_url`; use it to specify the image resource root only when the image API uses a different address. An unrecognized explicit `api_url` fails configuration validation rather than selecting an official default. When sharing a conversation provider, set `base_url` to the separate image API root. An explicit Images `base_url` can name a custom resource root: Chord appends `/generations` or `/edits` without adding `/images`.

Automatic detection uses known model IDs and standard endpoint paths. A proxy URL ending only in `/v1` cannot distinguish Gemini from OpenAI by itself. Gemini needs a known Gemini image model or `type: gemini`; Images APIs use `/images` or `/images/generations`. Custom aliases do not inherit the listed models' editing or parameter capabilities. Local generation consumes one complete response and does not accept Gemini's streaming `:streamGenerateContent` endpoint. Addresses cannot contain query parameters, fragments or embedded credentials; API keys come from `auth.yaml`.

`type` is optional: a clear Gemini root selects the Gemini protocol, and known image model IDs select their contract from the table. Unknown models at `/images/` or `/images/generations` receive only basic `openai-compatible` generation capabilities. Conflicting URL/model hints, unresolved types and unsupported model IDs fail configuration validation. An explicit `type` takes priority; Chord sends no paid capability probes.

The image pool traverses one round from its successful cursor and selects only models supporting the complete operation and options. Switching models never drops transparency, quality or reference requirements. GPT Image 2/2.5 expose common 2K/4K sizes; 2.5 also supports `xhigh` and `max` quality. The `openai-compatible` type exposes basic generation and standard sizes.

Omit optional size, quality, background and format parameters unless you need them, so the pool keeps all eligible targets. Size defaults are applied after selecting a target: OpenAI uses `auto`, basic `openai-compatible` uses `1024x1024`, and other types use service defaults. Explicit user options are never replaced by defaults; one target must support the complete option set.

`timeout_seconds` covers local queueing, credentials, generation, downloading and saving. It defaults to 300 seconds and is capped at 1800. A server-to-local fallback uses the remaining budget.

## Prefer a server image tool

Configure `native_image_generation` on the selected conversation model to declare the server image tool and hide its local equivalent:

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

The inner `model` is the image model; the conversation request still uses the outer model. Server tools require matching endpoint, protocol and explicit request authorization, and follow `generate_image` permissions. Parameter rules, per-call approval or synchronous hooks that cannot be enforced natively use the ordinary local tool path. Codex OAuth endpoints are not public Responses image endpoints.

Fallback requires a structured rejection proving unsupported capability, credentials or quota, a saved rejection receipt, and an enabled, permitted local tool. Subsequent requests in the same user turn keep local mode; a new user message selects the tool surface again. Disconnects, 5xx, cancellation, permission/content refusals, unknown outcomes and persistence failures never replay generation. Text-only server responses also do not trigger generation. When native search is enabled too, the total server-call cap is the smaller of both `max_uses` values.

## Generate or edit

For example, ask “Generate a square illustration of a tree with a transparent background, and save it as assets/tree.png.” Use an existing output directory. Chord saves the original in the session even when no workspace output path was requested. Existing output files are never overwritten. Reference paths and output directories cannot traverse symlinks within the workspace.

To edit, name the image explicitly: “Use assets/tree.png as the reference and change the leaves to blue.” The tool takes `operation: edit` with `reference_images`; it does not guess which recent image you meant. You can also reuse an `artifact:images/...` reference returned by an earlier generation in the same session.

Each call requests one final image. Up to five references are allowed, with a 20 MiB combined upload limit. Original outputs are limited to 32 MiB each and 36 million pixels. Chord validates and preserves the original bytes, including transparency; the conversation model receives at most one preview per result, using the existing bounded image-input normalization. The preview enters the next model request automatically; the model does not need to call `view_image` to inspect it. A text-only conversation model still receives the file reference. More images returned by the service are saved and reported, up to the five-image response limit.

The usual tool permissions and hooks apply to `generate_image`, reference reads and workspace writes. `artifact:` references are checked against their actual session file paths, which are shown when read approval is required. Enabling image generation does not grant a worker access to otherwise denied files.

When a service returns a download URL, its address must also be allowed by the `web_fetch` network permissions. A blocked address stops image delivery with an explicit permission error.

In terminals with image support, the tool card displays a thumbnail of every returned image in order. Click a thumbnail or use a view shortcut to open the full-screen viewer; generation never opens the viewer automatically.

## Saved images and interrupted requests

The result includes the operation ID, session image references, MIME type, dimensions, bytes, hash, service request ID when available and reported usage. Image billing has its own dimensions; missing prices are reported as unknown, and image charges are not included in the chat token cost estimate.

Results identify originals with session-relative `artifact:images/...` references. Session restore keeps the same saved originals, and a fork copies them into its own session so it remains usable after the source session is removed. Use the image viewer or `view_image` to inspect a saved file. Text and JSON exports retain the image references, rather than embedding image bytes; keep the session images directory when transferring originals.

An interrupted paid request can have an unknown outcome. Chord does not retry it, change services or regenerate during restore. Only structured credential/limit rejections known to be unexecuted may traverse all keys on the target and other eligible targets in one pool round. If generation finished but downloading failed, the private operation receipt retains the download URL; Chord retries that download while the URL remains valid and the call has time remaining. If the workspace copy failed, or its directory was moved or replaced while waiting, the result reports a warning; use the saved session original named in the result.

Local failures distinguish credentials, quota or billing, temporary rate limits, and uncertain service outcomes. Explicit credential or hard-quota rejections stop selecting that image key for the current runtime and continue with other eligible keys and targets; conversation key health is unchanged. Temporary limits prefer server retry advice and reuse provider retry pacing and caps. If all keys are cooling, the tool reports the remaining wait; wait before requesting again rather than repeatedly generating or using shell sleep. Fix account configuration for quota or credential failures. Failed image requests are recorded in the runtime log. Terminal tool failures also appear in the error panel with the HTTP status, image provider and model. Diagnostics retain bounded protocol facts, excluding credentials, raw API error bodies and signed download URLs.

Chord completes generation, downloading and saving within one tool call. Temporary download failures and temporary file-write errors receive bounded automatic retries under the call's overall timeout; cancellation stops retries. Every download attempt follows network permissions. Permission failures, expired download URLs, full disks and invalid images require their cause to be resolved. If delivery cannot finish after generation, the result explicitly says that the image was generated and identifies the unfinished stage; it does not ask the model to perform recovery or generate again. Successful results contain verified session image references and image attachments for supported conversation models.

ACP clients receive the original image in the tool result. Headless clients receive image references and bounded original-data chunks; see [Headless image delivery](headless.md#image-delivery). The companion chord-gateway forwards all returned images to WeChat and Feishu; Feishu uses a reduced preview for oversized images, keeping the originals in Chord.
