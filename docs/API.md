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

`GET /v1/videos/generations/{id}` 是等价写法，方便直连某些客户端的路径约定。

> 任务提交后由服务端后台定时向上游查询状态，客户端不必自己带上游凭据。

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
      "detail": "连通正常，发现 1 个可用模型",
      "models": ["gpt-4o-mini"]
    }
  ]
}
```

---

### 模型 models

一个模型 = 对外的模型名，指向某个渠道下的真实上游模型名。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/models` | 列出全部模型（含 `provider_name`、`provider_kind`） |
| POST | `/api/models` | 新建 |
| PUT | `/api/models/{id}` | 更新 |
| DELETE | `/api/models/{id}` | 删除 |

```json
{
  "provider_id": 1,
  "name": "gpt-4o-mini",
  "upstream_name": "gpt-4o-mini",
  "capability": "chat",
  "config": {
    "system_prompt": "",
    "defaults": { "temperature": 0.7 }
  },
  "enabled": true
}
```

- `name` 是客户端填的模型名，`upstream_name` 是发给上游的真实名字。两者解耦，因此同一个上游模型可以暴露成多个不同默认参数的对外模型。
- `capability`：`chat` 文本 / `image` 图片 / `video` 视频 / `embedding` 向量。
- `defaults` 里的键值会合并进上游请求体，可用来固定温度、`max_tokens`、`top_p` 等；客户端显式传入的同名参数优先。

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
| `{{size}}` / `{{n}}` / `{{duration}}` | 尺寸、张数、时长 |
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

---

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

### 视频任务、日志与设置

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/tasks?limit=50` | 视频任务列表，字段与 `/v1/videos/{id}` 一致 |
| GET | `/api/logs?limit=200&capability=chat&q=关键词` | 调用日志，按时间倒序 |
| DELETE | `/api/logs` | 清空日志 |
| GET | `/api/settings` | 读取全部设置 |
| POST | `/api/settings` | 写入设置（传 `{"key": "value"}` 形式的对象，只覆盖传入的键） |

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