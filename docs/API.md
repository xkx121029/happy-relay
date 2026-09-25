# 接口文档

中转站有两套接口，彼此完全隔离：

| 前缀 | 用途 | 鉴权 |
| --- | --- | --- |
| `/v1/*` | 对外统一接口，给第三方客户端（ChatBox、NextChat、Cherry Studio、自写脚本等）调用 | 必须携带 `Authorization: Bearer sk-xxx` |
| `/api/*` | 面板管理接口，仅供自带的网页控制台使用 | 无（自用场景，服务不应直接暴露到公网） |

所有请求与响应均为 JSON（UTF-8）。流式对话使用 SSE。

---

## 一、对外接口 `/v1`

### 鉴权

三种写法任选其一，值都必须是面板里创建过的秘钥：

```
Authorization: Bearer sk-xxxxxxxx
x-api-key: sk-xxxxxxxx
?key=sk-xxxxxxxx
```

秘钥可以被限定「只能调用哪些模型」；调用未授权的模型返回 `403`。

错误统一返回：

```json
{
  "error": {
    "message": "人类可读的说明",
    "type": "invalid_request_error",
    "code": 401
  }
}
```

常见状态码：`400` 参数错误、`401` 秘钥无效、`403` 秘钥被停用/过期/无模型权限、`404` 模型不存在或未启用、`502` 上游出错。

---

### 1. 列出模型

```
GET /v1/models
```

响应：

```json
{
  "object": "list",
  "data": [
    { "id": "gpt-4o-mini", "object": "model", "created": 1758800000, "owned_by": "relay" }
  ]
}
```

只返回已启用、且当前秘钥有权调用的模型。

---

### 2. 文本对话

```
POST /v1/chat/completions
```

请求体沿用 OpenAI 规范，未知字段会被原样透传给上游：

```json
{
  "model": "gpt-4o-mini",
  "messages": [
    { "role": "system", "content": "你是一个简洁的助手" },
    { "role": "user", "content": "你好" }
  ],
  "temperature": 0.7,
  "stream": false
}
```

消息内容既可以是字符串，也可以是分段数组（用于图片理解）：

```json
{
  "role": "user",
  "content": [
    { "type": "text", "text": "这张图里是什么？" },
    { "type": "image_url", "image_url": { "url": "https://example.com/a.png" } }
  ]
}
```

非流式响应：

```json
{
  "id": "chatcmpl-xxx",
  "object": "chat.completion",
  "created": 1758800000,
  "model": "gpt-4o-mini",
  "choices": [
    {
      "index": 0,
      "message": { "role": "assistant", "content": "你好，有什么可以帮你？" },
      "finish_reason": "stop"
    }
  ],
  "usage": { "prompt_tokens": 12, "completion_tokens": 9, "total_tokens": 21 }
}
```

流式（`"stream": true`）返回 `text/event-stream`，每个分片：

```
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1758800000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant"}}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"你好"}}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

无论上游是 OpenAI、Gemini 还是 Anthropic，出去的格式都统一成这样，客户端只需实现一套解析。

工具调用（function calling）同样按 OpenAI 格式透传与回传；流式下 `tool_calls` 的 `arguments` 已做分片拼接。

---

### 3. 旧版补全

```
POST /v1/completions
```

`prompt` 会被包成一条 user 消息转给对话接口，便于老客户端接入：

```json
{ "model": "gpt-4o-mini", "prompt": "写一句诗", "max_tokens": 64 }
```

---

### 4. 文本向量

```
POST /v1/embeddings
```

```json
{ "model": "text-embedding-3-small", "input": "要向量化的文本" }
```

`input` 支持字符串或字符串数组。响应：

```json
{
  "object": "list",
  "model": "text-embedding-3-small",
  "data": [{ "object": "embedding", "index": 0, "embedding": [0.01, -0.02] }],
  "usage": { "prompt_tokens": 6, "total_tokens": 6 }
}
```

---

### 5. 图片生成

```
POST /v1/images/generations
```

```json
{
  "model": "flux-image",
  "prompt": "一只戴墨镜的柴犬，赛博朋克风格",
  "size": "1024x1024",
  "n": 1,
  "response_format": "url"
}
```

响应：

```json
{
  "created": 1758800000,
  "data": [
    { "url": "https://cdn.example.com/a.png" },
    { "b64_json": "iVBORw0KGgo..." }
  ]
}
```

上游直接给地址就返回 `url`，给 base64 就返回 `b64_json`。

---

#### 输入参考图（图生图 / 多图合成）

`/v1/images/generations` 支持传入参考图，字段名兼容 `image` / `images` / `image_url` / `input_image` / `reference_images`，也可以直接写 `extra_body.image`：

```json
{
  "model": "agnes-img",
  "prompt": "把物体改成橙色，保持原有构图",
  "size": "1024x768",
  "image": ["https://example.com/input.png"]
}
```

参考图可以是公共可访问的 URL，也可以是 `data:image/png;base64,...` 形式的 data URI。传一张即为图生图，传多张即为多图合成。

**接入 Agnes Image 2.1 Flash**

该模型有几条非标准约束，按下面配置即可开箱兼容：

| 约束 | 中转站里的做法 |
| --- | --- |
| `size` 必填，推荐 `1K` / `2K` / `3K` / `4K` 档位 | 在模型「默认参数」里填 `{"size": "2K", "ratio": "16:9"}` |
| `response_format` 禁止出现在请求体顶层 | 模型编辑里把「图片请求模式」设为 `extra_body` |
| 图生图必须放在 `extra_body.image` | 同上，设为 `extra_body` 后参考图会自动放进 `extra_body.image` |

`extra_body` 模式下，`response_format` 与参考图都会被合并进 `extra_body`；客户端自己传入的 `extra_body` 字段优先，不会被覆盖。
模板类渠道也能用参考图：`{{images}}` 是完整数组，`{{image}}` / `{{image_url}}` 取第一张。

### 6. 视频生成

视频是异步任务，分两步：先提交，再轮询。

**提交**

```
POST /v1/videos
```

```json
{
  "model": "veo-demo",
  "prompt": "海边日落，镜头缓慢推进",
  "image": "https://example.com/first-frame.png",
  "duration": 5,
  "size": "1280x720"
}
```

`prompt` 与 `image` 至少填一个：只填 `prompt` 是文生视频，带 `image` 是图生视频。其余未知字段原样透传。

需要更精细控制的上游（首尾帧、参考素材等），可以改用下面这组字段：

| 字段 | 说明 |
| --- | --- |
| `mode` | 生成方式，如 Agnes 的 `text` / `keyframe` / `reference`，留空按 `text` 处理 |
| `first_frame` / `last_frame` | 首帧、尾帧图片 URL（`image` 等同于 `first_frame`） |
| `images` | 参考图 URL 数组 |
| `audios` | 参考音频 URL 数组 |
| `aspect_ratio` | 画幅比例，如 `16:9` |
| `size` | 分辨率档位，如 `720P` |
| `duration` / `seconds` | 时长，数字与字符串两种写法都接受 |

这些字段最终如何下发由模型的「视频模板」决定，模板可用占位符见后文。

**接入 Agnes Video 2.5 Flash**

该模型有三处非标准约定，模型编辑页「视频预设」一键套用即可开箱兼容：

| 约定 | 中转站里的做法 |
| --- | --- |
| 查询端点是根路径 `/agnesapi`，不在 `/v1` 下 | 模板路径写 `{{base_root}}/agnesapi`，或直接写绝对 URL |
| `seconds` 必须是字符串 `"4"`–`"12"` | 模板里用 `{{duration_text}}`（`{{seconds}}` 同义） |
| 不同模式只接受对应参数，传空值会报错 | 模板开启 `omit_empty`，空串 / 空数组 / `null` 一律不下发 |

`size` 固定 `720P`、`reference` 模式参考图最多 5 张 / 音频最多 3 段，预设里都已按此配置。轮询频率可在「设置 → 视频轮询间隔」调整，官方建议 1–2 秒。

响应：

```json
{
  "id": "task_f39690e2b6f36f40e055b77f",
  "object": "video.generation",
  "model": "veo-demo",
  "status": "queued",
  "progress": 0,
  "created_at": "2026-09-25T20:31:43+08:00",
  "updated_at": "2026-09-25T20:31:43+08:00",
  "video_url": "",
  "error": ""
}
```

**轮询**

```
GET /v1/videos/{id}
```

```json
{
  "id": "task_f39690e2b6f36f40e055b77f",
  "object": "video.generation",
  "model": "veo-demo",
  "status": "succeeded",
  "progress": 100,
  "video_url": "https://cdn.example.com/out.mp4",
  "created_at": "2026-09-25T20:31:43+08:00",
  "updated_at": "2026-09-25T20:31:48+08:00",
  "error": ""
}
```

`status` 只会是四个值之一：`queued`（排队中）、`running`（生成中）、`succeeded`（成功）、`failed`（失败，`error` 里带原因）。

### 7. 视频任务管理

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/v1/videos` | 列出该秘钥可见的视频任务（最多 50 条） |
| GET | `/v1/videos/{id}` | 查询单个任务 |
| DELETE | `/v1/videos/{id}` | 删除（取消）本地任务记录 |
| GET | `/v1/videos/{id}/stream` | SSE 持续推送任务状态，直到结束或超时 30 分钟 |

`DELETE` 只删除本地记录；上游任务是否随之取消取决于该上游是否提供取消接口。

```bash
# 进度流：状态或进度变化时才推送一条 data，结束时推送 data: [DONE]
curl -N http://127.0.0.1:8787/v1/videos/task_xxxx/stream \
  -H "Authorization: Bearer sk-xxxxxxxx"
```

> 任务提交后由服务端后台定时向上游查询状态，客户端不必自己带上游凭据。

### 8. 技能列表与技能调用

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/v1/skills` | 返回启用的站内技能及其说明与参数定义 |

```json
{
  "object": "list",
  "data": [
    {
      "slug": "polish",
      "name": "文案润色",
      "description": "把口语化或生硬的表达改写成通顺、专业的书面中文。",
      "category": "文本",
      "capability": "chat",
      "params": [{ "key": "input", "label": "原文", "type": "textarea" }]
    }
  ],
  "usage": "在 /v1/chat/completions、/v1/images/generations、/v1/videos 里带上 skill=<slug> 与 skill_params={...} 即可套用"
}
```

调用时把 `skill` 与 `skill_params` 一并带上，服务端会先渲染模板再转发上游：

```bash
curl http://127.0.0.1:8787/v1/chat/completions \
  -H "Authorization: Bearer sk-xxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "skill": "translate",
    "skill_params": { "input": "Hello world", "target": "中文" }
  }'
```

- `skill_params` 里未填写的参数会回落到技能定义的默认值。
- 对话类请求中技能会生成一条 `user` 消息；图片与视频请求中会生成 `prompt`。
- `capability` 为 `image` / `video` 的技能请配合对应能力的模型使用。

### 9. 健康检查

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz` | 返回 `{"status":"ok"}`，不需要鉴权，供部署探活 |

### 10. 自动模型 `auto`

把 `model` 填成 `auto`，服务端会先评估任务难度，再挑选档位匹配的文本模型。

```bash
curl http://127.0.0.1:8787/v1/chat/completions \
  -H "Authorization: Bearer sk-xxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{"model": "auto", "messages": [{"role": "user", "content": "你好"}]}'
```

难度打分维度：

| 维度 | 加分 |
| --- | --- |
| 输入长度 ≥ 100 / 400 / 1200 字 | +1 / +2 / +3 |
| 含图片 | +2 |
| 带 `tools`（函数调用） | +1 |
| 系统提示 ≥ 500 字 | +1 |
| 命中复杂关键词（重构、算法、证明、架构…） | +1，命中 3 个以上 +2 |

| 总分 | 档位 |
| --- | --- |
| 0 | `light` 轻量 |
| 1–2 | `standard` 标准 |
| ≥ 3 | `pro` 强力 |

模型档位在「模型」编辑里配置，留空时按上游模型名推断（`mini` / `flash` → 轻量，`pro` / `max` / `opus` → 强力）。
目标档位没有可用模型时会向相邻档位降级，最终回退到任意一个可用的文本模型。

面板调试台选中 `auto` 后，回复下方会显示实际选用的模型与打分理由。`GET /v1/models` 会把 `auto` 一并列出（带 `"auto": true`）。

挑选时还会检查上下文容量：同一档位内优先选择窗口装得下本次请求的模型；若都装不下，则退回窗口最大的那个，并在理由里说明。

### 上下文长度与容量规划

**自动获取**：探测渠道时，服务端会从上游 `/v1/models` 的返回里读取上下文窗口并写入模型库，识别 `context_length`、`context_window`、`max_input_tokens`、`inputTokenLimit`、`max_tokens` 等字段，以及 `top_provider` / `model_info` 这类嵌套结构。模型库页面可以直接看到并手工修正。

**生效顺序**：模型编辑里的「上下文窗口」→ 模型库探测值 → 按上游模型名推断（已覆盖 Gemini、Claude、GPT、Qwen、DeepSeek、Llama 等常见系列）。

**调用前校验**：`/v1/chat/completions` 会先估算输入 token（CJK 按 1 字 1 token、其余按 4 字符 1 token，图片、工具调用与工具定义同样计入），再与窗口对比。处理方式由「设置 → 上下文超限策略」决定：

| 策略 | 行为 |
| --- | --- |
| `reject`（默认） | 超出时直接返回 `400 context_length_exceeded`，错误信息写明估算用量与可用预算 |
| `truncate` | 自动从最早的历史消息开始裁剪（保留系统提示与最近对话，并连带清理孤立的工具结果），裁剪条数会记入日志预览 |
| `off` | 不做校验，完全交给上游判断 |

可用输入预算为「窗口 × 安全系数」（默认 90%），其余留给输出与估算误差，安全系数可在设置里调整。窗口未知（探测不到且无法推断）时不作拦截，仅记录估算结果。

`GET /v1/models` 在已知时会附带 `context_length`，便于客户端自行规划请求长度。

### 流式输出说明

- `/v1/chat/completions` 的 `stream=true` 与 `/v1/videos/{id}/stream` 都是 SSE。
- 服务端默认把上游的长文本**拆成小段匀速下发**，避免整段突然跳出；速率由 `RELAY_STREAM_RATE`（字符/秒，默认 120）控制，`RELAY_STREAM_SMOOTH=0` 可关闭。
- 长时间无输出时会定期发送 `: keep-alive` 注释行，间隔由 `RELAY_SSE_HEARTBEAT`（秒，默认 15）控制。

---

## 二、面板接口 `/api`

供网页控制台使用，无鉴权。

### 元信息与概览

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/meta` | 返回渠道类型、能力类型、调度策略、视频模板预设与全部设置项 |
| GET | `/api/overview` | 概览统计、近 14 天调用趋势、最近 8 条日志、最近 5 个视频任务 |

`/api/overview` 响应形状：

```json
{
  "stats": {
    "providers": 4, "models": 8, "keys": 2, "skills": 10,
    "today_calls": 14, "total_calls": 14, "success_rate": 100,
    "avg_latency_ms": 1.14, "prompt_tokens": 75, "completion_tokens": 49, "failed_calls": 0
  },
  "daily": [{ "date": "2026-09-25", "total": 14, "failed": 0 }],
  "recent_logs": [],
  "recent_tasks": []
}
```

`/api/meta` 中的 `video_presets` 是可直接套用的视频模板预设，每项形如：

```json
{
  "name": "通用任务式（提交返回 task_id）",
  "description": "适用于「POST 提交返回任务 ID，再 GET 查询」的绝大多数视频接口。",
  "capability": "video",
  "video": { "method": "POST", "path": "...", "body": {}, "response": {} },
  "video_query": { "method": "GET", "path": "...", "response": {} }
}
```

---

### 渠道 providers

一个渠道 = 一个上游供应商，可以填多个入口地址与多个密钥。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/providers` | 列出全部渠道 |
| POST | `/api/providers` | 新建 |
| PUT | `/api/providers/{id}` | 更新 |
| DELETE | `/api/providers/{id}` | 删除（其下模型一并删除） |
| POST | `/api/providers/test` | 连通性探测 |

渠道对象：

```json
{
  "id": 1,
  "name": "主力中转",
  "kind": "openai",
  "base_urls": ["https://api.example.com/v1", "https://backup.example.com/v1"],
  "api_keys": ["sk-aaa", "sk-bbb"],
  "headers": { "X-Custom": "1" },
  "strategy": "round_robin",
  "timeout_sec": 600,
  "enabled": true,
  "remark": ""
}
```

- `kind`

  | 值 | 含义 |
  | --- | --- |
  | `openai` | OpenAI 兼容协议（`/v1/chat/completions` 等），覆盖绝大多数中转商 |
  | `gemini` | Google Gemini 原生 `generateContent`，密钥走 `x-goog-api-key` |
  | `anthropic` | Anthropic 原生 Messages，密钥走 `x-api-key` |
  | `template` | 自定义模板，手填请求体与响应取值路径，可接任意 HTTP 上游 |

- `strategy`：`round_robin` 轮询 / `random` 随机 / `failover` 主备。
  实际调用组合是「入口地址 × 密钥」的笛卡尔积，按策略排序后依次尝试，某个组合失败自动换下一个。

- `base_urls` 只填域名时，OpenAI 类型会自动补 `/v1`。

**探测**

```json
// 请求（id 与 base_url 二选一即可）
{ "id": 1 }
{ "kind": "openai", "base_url": "https://api.example.com/v1", "api_key": "sk-aaa" }
```

```json
{
  "results": [
    {
      "url": "https://api.example.com/v1",
      "ok": true,
      "status": 200,
      "latency_ms": 240,
      "detail": "连通正常，发现 3 个可用模型",
      "models": [
        { "id": "dall-e-3", "capability": "image", "hint": "名称含图片关键词" },
        { "id": "gpt-4o-mini", "capability": "chat", "hint": "未识别到特征，默认按对话处理" },
        { "id": "text-embedding-3-small", "capability": "embedding", "hint": "名称含向量化关键词" }
      ]
    }
  ]
}
```

`models` 是探测到的模型清单，每个条目含推断出的 `capability`（`chat` / `image` / `video` / `embedding`，空表示未识别）与推断依据 `hint`：

- 优先采信上游声明的字段：`capability` / `type` / `model_type`，或 Gemini 的 `supportedGenerationMethods`
- 其次按模型名特征判断：`embed` → 向量；`veo` / `kling` / `video` → 视频；`dall-e` / `flux` / `image` → 图片
- 音频、审核、重排类模型（`whisper` / `tts` / `moderation` / `rerank`）标记为空能力，导入时默认不勾选
- 其余默认按 `chat` 处理

面板可在探测结果里直接勾选并批量导入为对外模型（同名模型会自动跳过）。

---

### 模型库 catalog

模型库保存从渠道探测到（或手工登记）的上游模型，**仅作为配置候选，不直接对外开放**。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/catalog?provider_id=1&capability=chat` | 列出条目（含 `used_by` 引用计数） |
| POST | `/api/catalog` | 手动登记（`provider_id` + `model_id` + `capability`） |
| PUT | `/api/catalog/{id}` | 更新 |
| DELETE | `/api/catalog/{id}` | 移除 |
| POST | `/api/catalog/import` | 批量导入探测结果 |

```json
// POST /api/catalog/import
{ "provider_id": 1, "models": [{ "model_id": "gpt-4o-mini", "capability": "chat" }] }
// -> { "created": 1, "updated": 0 }
```

导入按「渠道 + 模型名」去重，重复导入只更新能力与时间；导入的条目默认可用。

### 模型 models

一个模型 = 对外的模型名，可以指向一个或多个渠道下的真实上游模型名。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/models` | 列出全部模型（含 `provider_name`、`provider_kind`、`provider_ids`） |
| POST | `/api/models` | 新建 |
| PUT | `/api/models/{id}` | 更新 |
| DELETE | `/api/models/{id}` | 删除 |

```json
{
  "provider_id": 1,
  "provider_ids": [1, 2],
  "inputs": [
    { "provider_id": 1, "upstream_name": "gpt-4o-mini" },
    { "provider_id": 2, "upstream_name": "gpt-4o-mini" }
  ],
  "name": "gpt-4o-mini",
  "upstream_name": "gpt-4o-mini",
  "capability": "chat",
  "config": {
    "system_prompt": "",
    "route_strategy": "failover",
    "tier": "standard",
    "defaults": { "temperature": 0.7 }
  },
  "enabled": true
}
```

- `name` 是客户端填的模型名，`upstream_name` 是发给上游的真实名字。两者解耦，因此同一个上游模型可以暴露成多个不同默认参数的对外模型。
- `inputs` 是上游列表（渠道 + 该渠道下的上游模型名），按顺序尝试、失败自动切换；`provider_ids` / `provider_id` 由它推导，兼容旧数据。
- `config.tier` 是模型档位（`light` / `standard` / `pro`），供 `auto` 自动模型挑选，留空按上游模型名推断。
- `capability`：`chat` 文本 / `image` 图片 / `video` 视频 / `embedding` 向量。
- `defaults` 里的键值会合并进上游请求体，可用来固定温度、`max_tokens`、`top_p` 等；客户端显式传入的同名参数优先。
- `route_strategy`：绑定多个渠道时的调度方式，`failover`（默认，按 `provider_ids` 顺序逐个尝试）/ `round_robin` / `random`。每次调用先按该策略排列渠道，再在渠道内按渠道自身的策略排列「入口 × 密钥」；全部失败才返回错误，流式响应一旦开始下发就不再切换。

**模板类模型的额外配置**

当渠道是 `template`，或能力是 `video` 时，需要在 `config` 里补上模板：

```json
{
  "config": {
    "chat": {
      "method": "POST",
      "path": "/chat/completions",
      "headers": { "Authorization": "Bearer {{key}}" },
      "query": {},
      "body": { "model": "{{upstream_model}}", "messages": "{{messages}}" },
      "response": { "content": "choices[0].message.content", "error": "error.message" }
    },
    "video": { "method": "POST", "path": "/videos", "body": {}, "response": {} },
    "video_query": { "method": "GET", "path": "/videos/{{task_id}}", "response": {} }
  }
}
```

占位符（写在整个字段位置时保留原始类型，写在字符串中间时做文本替换）：

| 占位符 | 含义 |
| --- | --- |
| `{{prompt}}` / `{{input}}` | 用户提示词 |
| `{{messages}}` | 完整的消息数组（仅对话） |
| `{{system}}` | system 提示词 |
| `{{image}}` / `{{image_url}}` | 图片地址或 data URL |
| `{{model}}` / `{{upstream_model}}` | 上游模型名 |
| `{{key}}` | 当前渠道密钥 |
| `{{size}}` / `{{n}}` / `{{duration}}` | 尺寸、张数、时长（数字形式） |
| `{{duration_text}}` / `{{seconds}}` | 时长的字符串形式，供要求字符串秒数的上游使用 |
| `{{base_root}}` | 入口地址去掉 `/v1` 等版本后缀后的根地址 |
| `{{mode}}` | 生成方式（如 `text` / `keyframe` / `reference`） |
| `{{first_frame}}` / `{{last_frame}}` | 首帧、尾帧图片地址 |
| `{{images}}` / `{{audios}}` | 参考图、参考音频的数组 |
| `{{aspect_ratio}}` | 画幅比例 |
| `{{task_id}}` | 上游视频任务 ID（仅查询模板） |

`response` 是从上游响应里取值的 JSON 路径，支持 `data.outputs[0].url` 这样的写法：

| 字段 | 用途 |
| --- | --- |
| `task_id` | 上游任务 ID |
| `status` | 任务状态 |
| `progress` | 进度百分比 |
| `video_url` | 视频地址 |
| `image_url` | 图片地址 |
| `content` | 文本内容（仅对话） |
| `error` | 错误信息 |
| `status_map` | 状态值映射，如 `{"processing": "running"}` |
| `success_value` / `pending_value` / `failed_value` | 未命中映射表时的兜底判定 |

状态值会被统一归一化成 `queued` / `running` / `succeeded` / `failed`，常见别名（`processing`、`completed`、`done`、`error`…）已内置识别。

**关于模板路径**：`path` 默认拼在渠道入口地址之后；若渲染结果以 `http://` 或 `https://` 开头，则视为绝对地址直接使用。配合 `{{base_root}}`，即可对接「创建与查询不在同一路径前缀」的上游。

**关于 `omit_empty`**：模板设置 `"omit_empty": true` 后，渲染出的请求体会剔除值为空串、空数组与 `null` 的字段，适合「不同模式只接受对应参数、传空值会被判错」的上游。

### 对外秘钥 keys

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/keys` | 列出全部秘钥 |
| POST | `/api/keys` | 新建（`key` 留空则自动生成 `sk-` + 48 位十六进制） |
| PUT | `/api/keys/{id}` | 更新（不会改动已有的 `key` 值） |
| DELETE | `/api/keys/{id}` | 删除 |

```json
{
  "name": "给手机上的 ChatBox 用",
  "models": ["gpt-4o-mini", "claude-sonnet"],
  "enabled": true,
  "expires_at": "2027-01-01",
  "remark": ""
}
```

- `models` 为空数组表示不限制，可以用 `["*"]` 显式表示全部。
- `expires_at` 支持 `2027-01-01`、`2027-01-01 08:00:00` 或 RFC3339；留空表示永不过期。
- 响应中的 `last_used_at` 由服务端自动维护。

---

### 站内技能 skills

技能是一段带参数的提示词模板，可在调试台里一键套用。软件预置 10 个，可自行增删改。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/skills` | 列出全部技能 |
| POST | `/api/skills` | 新建 |
| PUT | `/api/skills/{id}` | 更新 |
| DELETE | `/api/skills/{id}` | 删除（预置技能不可删除） |

```json
{
  "slug": "polish",
  "name": "文案润色",
  "description": "把口语化表达改写成更专业的书面语",
  "category": "写作",
  "icon": "wand",
  "capability": "chat",
  "prompt": "请把下面的内容改写成{{tone}}的书面表达：\n\n{{text}}",
  "params": [
    { "key": "text", "label": "原始内容", "type": "textarea" },
    { "key": "tone", "label": "语气", "type": "select", "default": "专业", "options": ["专业", "亲切", "简洁"] }
  ],
  "enabled": true,
  "sort_order": 0
}
```

`prompt` 里的 `{{参数名}}` 会被替换成实际取值；参数未填写时回落到 `default`。
`capability` 决定套用后写进哪个字段：`image` / `video` 写入 `prompt`，`chat` 写入 `messages`。

---

### 调试台 playground

在网页里直接试用模型，不需要创建秘钥。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/playground/chat` | 对话，请求体与 `/v1/chat/completions` 一致，可加 `stream` |
| POST | `/api/playground/image` | 图片，请求体与 `/v1/images/generations` 一致 |
| POST | `/api/playground/video` | 视频，请求体与 `/v1/videos` 一致 |

可选加两个字段套用技能：

```json
{
  "model": "gpt-4o-mini",
  "skill": "polish",
  "skill_params": { "text": "把这个句子改好一点", "tone": "简洁" },
  "messages": [{ "role": "user", "content": "占位" }]
}
```

---

### 连接器生成 connectors

用系统级 AI 把一段模糊的接口说明翻译成可用的模板配置，省去手写 JSON 路径。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/connectors/generate` | 生成模板草案 |
| POST | `/api/connectors/try` | 用草案真实打一次上游，验证是否配对 |

生成：

```json
{
  "description": "提交返回 task_id，查询接口用 GET /v1/video/status?id=xxx，返回的 data.video 是地址",
  "base_url": "https://api.example.com/v1",
  "sample_request": "curl -X POST ... ",
  "sample_response": "{\"task_id\":\"abc\"}",
  "capability": "video"
}
```

```json
{
  "draft": { "spec": {}, "video_query": {}, "upstream_name": "", "notes": "" },
  "raw_reply": "AI 的原始回复，解析失败时供人工修正",
  "system_model": "gpt-4o-mini"
}
```

试用：

```json
{
  "provider_id": 1,
  "upstream_name": "veo-3",
  "prompt": "海边日落",
  "duration": 5,
  "size": "1280x720",
  "spec": { "method": "POST", "path": "/videos", "body": {}, "response": {} }
}
```

```json
{ "ok": true, "response": { "task_id": "abc123" } }
```

`ok` 为 `false` 时 `error` 里带失败原因。

---

### 运维与备份

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/providers/health` | 各渠道的熔断状态（`open` / `fails` / `remain_sec`） |
| POST | `/api/providers/{id}/reset` | 手动清除某个渠道的熔断状态 |
| POST | `/api/providers/reset` | 清除全部渠道的熔断状态 |
| GET | `/api/stats?days=7` | 用量报表：`by_model` / `by_provider` / `by_day` |
| GET | `/api/backup` | 导出全部配置（JSON 下载） |
| POST | `/api/backup` | 合并导入配置：同名渠道 / 模型 / 秘钥 / 技能会被更新，不删除现有数据 |
| GET | `/metrics` | Prometheus 文本格式指标（无需鉴权） |

熔断由环境变量 `RELAY_BREAKER_FAILS`（默认 5）与 `RELAY_BREAKER_COOLDOWN`（默认 60 秒）控制，设为 `0` 可关闭。
熔断期间该渠道会被跳过；若全部渠道都在熔断中，则会忽略熔断照常尝试，避免出现完全不可用的死局。

秘钥新增三个配额字段（均为 `0` 表示不限制）：`rpm`（每分钟请求上限）、`daily_quota`（每日请求上限）、`daily_tokens`（每日 token 上限）。
超限时接口返回 429，`error.type` 为 `rate_limit_error` 或 `quota_exceeded_error`。

`/metrics` 输出示例：

```
relay_requests_total{capability="chat",model="gpt-4o-mini",status="ok"} 12
relay_request_duration_ms_avg{capability="chat",model="gpt-4o-mini"} 812.40
relay_goroutines 11
relay_memory_bytes 945560
```

### 视频任务、日志与设置

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/tasks?limit=50` | 视频任务列表，字段与 `/v1/videos/{id}` 一致 |
| DELETE | `/api/tasks/{id}` | 删除任务记录（面板取消任务） |
| GET | `/api/logs?limit=200&capability=chat&q=关键词` | 调用日志，按时间倒序 |
| DELETE | `/api/logs` | 清空日志 |
| GET | `/api/settings` | 读取全部设置 |
| POST | `/api/settings` | 写入设置（传 `{"key": "value"}` 形式的对象，只覆盖传入的键） |
| POST | `/api/skills/generate` | 用系统级 AI 依据描述生成技能草案 |

`POST /api/skills/generate` 请求：

```json
{ "description": "把会议记录整理成待办清单，按负责人分组", "capability": "chat" }
```

返回 `{"draft": {...}, "raw_reply": "...", "system_model": "..."}`。`draft` 结构与 `POST /api/skills` 的请求体一致（含 `slug` / `name` / `prompt` / `params`），可直接编辑后保存；AI 未返回合法 JSON 时会把原文填入 `prompt` 并带上 `parse_error`。使用前需先在设置里指定 `system_model`。

已知设置项：

| 键 | 含义 |
| --- | --- |
| `system_model` | 系统级 AI 使用的对外模型名，连接器生成器依赖它 |
| `panel_title` | 面板标题 |

日志对象：

```json
{
  "id": 14,
  "created_at": "2026-09-25T20:31:43+08:00",
  "capability": "chat",
  "model": "gpt-4o-mini",
  "provider_name": "主力中转",
  "api_key_name": "给手机上的 ChatBox 用",
  "status": 200,
  "stream": true,
  "latency_ms": 812,
  "prompt_tokens": 12,
  "completion_tokens": 9,
  "error": "",
  "request_body": "…",
  "response_preview": "…"
}
```

对话的日志会逐轮累积 token，一轮会话结束时写入一条汇总记录。设置环境变量 `RELAY_DISABLE_LOG_BODY=1` 可关闭请求体与响应预览落库。