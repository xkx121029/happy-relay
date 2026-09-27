# Happy Relay · 模型中转站

把手上零散的 AI 上游聚合成一套标准接口的自用中转站。

一个 Go 单文件二进制（内嵌网页面板）+ 一个 SQLite 文件，没有任何外部依赖。渠道支持 OpenAI 兼容协议、智谱 GLM、Gemini 与 Anthropic 原生协议，以及一个「模板驱动」适配器用于接入任意奇形怪状的 HTTP 上游；对外统一输出标准的 `/v1/chat/completions`、`/v1/images/generations`、`/v1/embeddings`、任务式 `/v1/videos`，以及语音合成的 `/v1/audio/speech` 与语音识别的 `/v1/audio/transcriptions`。

为 arm64 部署而生：交叉编译、多阶段 Docker 构建、纯净的静态二进制。

---

## 特性

**渠道与调度**

- 一个渠道可填**多个入口地址**与**多个密钥**，实际调用是两者的笛卡尔积
- 渠道探测到的模型先进入**模型库**（仅作配置候选，不对外开放），配置对外模型时从中挑选
- 一个对外模型可配置**多个上游**（每个上游是「渠道 + 上游模型名」），按顺序失败自动切换
- 对外提供内置 **`auto` 自动模型**：按任务难度（输入长度、是否含图、工具调用、复杂关键词）自动挑选合适档位的模型
- 渠道内三种调度策略：`round_robin` 轮询、`random` 随机、`failover` 主备
- 跨渠道调度同样支持 `failover`（默认，按绑定顺序）、`round_robin`、`random`
- 组合失败自动切换下一个，流式响应已下发后不再重试，避免客户端收到半截内容
- 添加渠道时可自动拉取上游模型列表，并按元数据与命名特征推断每个模型的能力，支持勾选一键导入为对外模型
- 内置连通性探测，逐个入口地址返回状态码、耗时与探测到的模型列表
- **同渠道多上游**：一个对外模型可以在同一个渠道下挂多个上游模型，按输入模态自动择一（带图请求自动走视觉模型），某个上游报错时自动切到下一个
- **auto 逐模型故障转移**：`auto` 会把候选模型按档位与容量排序后依次尝试，前一个的所有上游都失败就换下一个，全部试完才报错；客户端只会感觉响应慢了一点
- **自动模态识别**：探测上游模型列表时按名称推断输入 / 输出模态（读图、读视频、读文档、出图、出视频），无需手工配置
- **开箱即用的免费模型**：首次启动自动创建「智谱 GLM」渠道并写入 6 个免费模型（GLM-4.7-Flash、GLM-4.6V-Flash、GLM-4V-Flash、GLM-4.1V-Thinking-Flash、CogView-3-Flash、CogVideoX-Flash）；首次打开面板会引导填入该平台的 API Key，并可顺手生成一个访问秘钥

**五类上游协议**

| 类型 | 说明 |
| --- | --- |
| `openai` | OpenAI 兼容，覆盖绝大多数中转商与自部署服务（vLLM、Ollama、one-api…） |
| `zhipu` | 智谱开放平台 GLM，端点 `https://open.bigmodel.cn/api/paas/v4`，协议与 OpenAI 一致 |
| `gemini` | Google Gemini 原生 `generateContent` |
| `anthropic` | Anthropic 原生 Messages |
| `template` | 自定义模板：手填请求体与响应字段路径，可接任意 HTTP 上游 |

**统一对外接口**

- `/v1/chat/completions`：支持 SSE 流式、多模态消息、function calling；无论上游是哪个协议，出去的格式都统一成 OpenAI 分片
- `/v1/images/generations`：支持图生图与多图合成（参考图可传 URL 或 data URI），并可按上游要求把 `response_format` / 参考图放进 `extra_body`
- `/v1/embeddings`、`/v1/videos`（异步任务 + 后台自动轮询 + `/{id}/stream` 进度流；支持首尾帧、参考图与参考音频）
- `/v1/audio/speech` 语音合成（直接返回音频流）、`/v1/audio/transcriptions` 语音识别（multipart 上传音频文件）
- `GET /v1/models` 在已知时附带 `context_length`；内置 `auto` 自动路由会结合任务难度与上下文容量挑选模型
- **流式输出平滑**：上游一次吐出的长文本会拆成小段匀速下发，客户端观感均匀；空闲时自动发心跳保活
- **技能可对外调用**：`GET /v1/skills` 拿到技能清单与说明，调用时带 `skill` + `skill_params` 即可套用提示词模板
- 对外模型名与上游模型名解耦，同一个上游模型可以暴露成多个不同默认参数的对外模型
- 跨协议状态归一化：上游五花八门的 `processing` / `done` / `finished` 统一成 `queued` / `running` / `succeeded` / `failed`
- `/healthz` 免鉴权探活

**网页面板**

- **内置模型**：智谱 GLM 的 6 个免费模型一键添加（文本 / 视觉 / 图片 / 视频），填好渠道 API Key 即用
- 渠道、模型、秘钥、技能、日志、设置，全程可视化配置
- **调试台**：不建秘钥就能直接在网页里跟模型对话（流式逐字输出）、出图、出视频；还可以**指定上游**——钉住某个渠道或某条上游模型单独验证，失败不再切换，拿到的就是它真实的报错
- **站内技能**：带参数的提示词模板，一键套用，预置 10 个常用动作
- **连接器生成器**：用系统级 AI 把一段接口描述翻译成可用的上游适配模板，并支持当场验证
- **AI 生成技能**：描述一个任务，系统级 AI 自动产出带参数的提示词模板
- **任务面板**：视频任务的进度、结果与取消
- 秘钥级模型白名单与过期时间；调用日志含 token、耗时、请求体与响应预览

**可观测与运维**

- **渠道熔断**：某个渠道连续失败达到阈值后自动跳过，冷却结束放行一次试探；面板显示健康状态并可手动恢复
- **秘钥配额**：每把秘钥可设每分钟请求上限、每日请求上限与每日 token 上限，超限返回 429
- **用量报表**：按模型 / 渠道 / 日期聚合，概览页直接查看
- **Prometheus 指标**：`/metrics` 导出调用统计与运行时指标
- **配置备份**：一键导出 / 导入全部配置（渠道、模型、秘钥、技能、模型库、设置），导入为合并方式

**部署**

- `CGO_ENABLED=0` 纯静态二进制，SQLite 走 `modernc.org/sqlite`，无需 CGO
- 前端产物内嵌进二进制，运行时只有一个文件 + 一个 `data` 目录
- 多阶段 Dockerfile 与 buildx arm64 交叉编译脚本

---

## 快速开始

### 方式一：源码运行

需要 Go 1.26+ 与 Node 22+。

```bash
# 构建网页面板，产物会输出到 internal/api/webdist
cd web && npm install && npm run build && cd ..

# 编译并启动
go build -o relay ./cmd/relay
./relay
```

打开 `http://127.0.0.1:8787` 即是控制台。

> 若只想用接口、不要面板，可以跳过前端构建——但 `//go:embed all:webdist` 要求该目录非空，
> `internal/api/webdist/` 里已保留一个占位 `index.html`，直接 `go build` 也能通过。

### 方式二：Docker（x86 主机上交叉编译 arm64）

```bash
# 直接在本机跑
docker compose -f deploy/docker-compose.yml up -d

# 或交叉编译出 arm64 镜像包，拷到 NAS 上加载
./deploy/build-arm64.sh
# 产物：happy-relay-arm64.tar

# 目标机器上
docker load -i happy-relay-arm64.tar
docker run -d --name happy-relay -p 8787:8787 -v /volume1/docker/happy-relay:/data happy-relay:arm64
```

想直接推到镜像仓库用 `./deploy/build-arm64.sh --push`，用 `IMAGE=` 与 `TAG=` 覆盖名字。

---

## 上手流程

三步就能用起来。

**1. 建渠道** —— 填上游地址和密钥。

例如接一个 OpenAI 兼容的中转商：

| 字段 | 值 |
| --- | --- |
| 名称 | 主力中转 |
| 类型 | OpenAI 兼容 |
| 入口地址 | `https://api.example.com/v1`、`https://backup.example.com/v1` |
| 密钥 | `sk-aaa`、`sk-bbb` |
| 策略 | 轮询 |

填完点「测试并获取模型」，会自动列出上游可用模型并推断各自能力，勾选后即可一键导入为对外模型，然后再保存。

**2. 建模型** —— 决定对外叫什么名字。

| 字段 | 值 |
| --- | --- |
| 对外模型名 | `gpt-4o-mini` |
| 上游 | 主力中转 · `gpt-4o-mini`（可再加一个备用上游，失败时自动切换） |
| 能力 | 文本对话 |
| 档位 | 标准（供 `auto` 自动模型挑选） |
| 默认参数 | `{"temperature": 0.7}` |

`默认参数` 会合并进上游请求体，客户端显式传入的同名参数优先。所以你可以用同一个上游模型，配出 `fast`（低温度、短输出）和 `creative`（高温度）两个对外模型。

**3. 建秘钥** —— 给不同客户端各发一把。

秘钥可以限定只能调用哪些模型。比如给手机上的 ChatBox 发一把只管 `gpt-4o-mini` 的，给脚本发一把全放开的。

---

## 客户端接入

任何兼容 OpenAI 的客户端都能接，只需填两处：

```
接口地址 / Base URL：http://你的地址:8787/v1
API Key：            sk-xxxxxxxx
```

命令行验证：

```bash
curl http://127.0.0.1:8787/v1/chat/completions \
  -H "Authorization: Bearer sk-xxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

图片：

```bash
curl http://127.0.0.1:8787/v1/images/generations \
  -H "Authorization: Bearer sk-xxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{"model": "flux-image", "prompt": "一只戴墨镜的柴犬", "size": "1024x1024"}'
```

视频（异步任务）：

```bash
# 提交
curl http://127.0.0.1:8787/v1/videos \
  -H "Authorization: Bearer sk-xxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{"model": "veo-demo", "prompt": "海边日落", "duration": 5}'

# 轮询（把返回的 id 填进去）
curl http://127.0.0.1:8787/v1/videos/task_xxxxxxxx \
  -H "Authorization: Bearer sk-xxxxxxxx"
```

完整接口说明见 [docs/API.md](docs/API.md)。

---

## 配置项

全部通过环境变量覆盖。

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `RELAY_ADDR` | `:8787` | HTTP 监听地址 |
| `RELAY_DATA_DIR` | `./data` | 数据目录 |
| `RELAY_DB` | `<数据目录>/relay.db` | SQLite 文件路径 |
| `RELAY_PUBLIC_URL` | 空 | 对外可访问地址，用于拼接生成结果链接 |
| `RELAY_TIMEOUT` | `600` | 单次上游请求兜底超时（秒） |
| `RELAY_VIDEO_POLL` | `10` | 视频任务后台轮询间隔（秒） |
| `RELAY_MAX_LOG_ROWS` | `20000` | 调用日志保留条数上限，超出自动裁剪 |
| `RELAY_DISABLE_LOG_BODY` | `0` | 设为 `1` 则不落库请求体与响应预览 |
| `RELAY_STREAM_SMOOTH` | `1` | 设为 `0` 关闭流式输出平滑（原样透传上游分片） |
| `RELAY_STREAM_RATE` | `120` | 平滑输出的速率上限（字符/秒） |
| `RELAY_SSE_HEARTBEAT` | `15` | SSE 心跳间隔（秒），`0` 表示关闭 |
| `RELAY_BREAKER_FAILS` | `5` | 渠道连续失败多少次后熔断，`0` 关闭熔断 |
| `RELAY_BREAKER_COOLDOWN` | `60` | 熔断冷却时间（秒），到期后放行一次试探 |

### 面板设置

以下 6 项可以直接在面板「设置 → 服务端运行参数」里调整，保存后**立即生效、无需重启服务**；面板里留空则回落到上表的环境变量默认值：

`RELAY_MAX_LOG_ROWS`、`RELAY_DISABLE_LOG_BODY`、`RELAY_STREAM_SMOOTH`、`RELAY_STREAM_RATE`、`RELAY_SSE_HEARTBEAT`、`RELAY_VIDEO_POLL`

> `RELAY_ADDR`、`RELAY_DATA_DIR`、`RELAY_DB` 属于启动期参数，仍需通过环境变量设置。

「通用」与「外观与体验」还提供了一批面板偏好，同样是保存后即时生效：

| 设置项 | 说明 |
| --- | --- |
| 面板标题 | 显示在侧边栏与浏览器标题上 |
| 系统级 AI 模型 | 连接器生成器与技能生成所调用的对话模型 |
| 默认落地页 | 打开面板时自动进入的页面 |
| 主题 | 深色 / 浅色 / 跟随系统，也可点右上角按钮快速切换 |
| 紧凑模式 | 收紧卡片与列表间距，一屏显示更多内容 |
| 危险操作二次确认 | 删除渠道、模型、秘钥等操作前先弹窗确认 |
| 概览自动刷新间隔 | 单位秒，`0` 表示关闭，仅「概览」页生效 |
| 上下文超限策略 | `reject` 直接报错 / `truncate` 自动裁剪最早的历史 / `off` 不校验 |
| 上下文安全系数 | 只把窗口的该比例用于输入，默认 90%，其余留给输出与估算误差 |

### 移动端

面板已按手机屏幕适配，用手机浏览器直接打开即可，也可以「添加到主屏幕」当成 App 使用：

- 桌面端的侧边栏在窄屏下换成**底部标签栏**（概览 / 调试台 / 渠道 / 模型 / 更多），其余页面收进「更多」面板
- 适配刘海屏与底部横条的安全区，内容不会被遮挡
- 表单控件字号不低于 16px，避免 iOS 聚焦输入框时自动放大整页
- 表格在窄屏按内容展开并支持横向滑动；弹窗改为从底部升起的抽屉
- 调试台撑满可用高度，用户消息靠右显示，接近原生聊天观感
- 去掉点击灰块、消除双击缩放延迟，按钮与标签统一加了按压缩放反馈
- 超窄机型（≤380px）与横屏另有一套更紧凑的间距

> 适配层集中在 `web/src/mobile.css`，只在 ≤768px 生效，桌面端布局不受影响。

---

## 关于安全

这是**自用**工具。`/api/*` 面板接口没有做鉴权，因为设计前提是只跑在内网或只由自己访问。

如果要暴露到公网，请在前面加一层反向代理并自行做好访问控制，例如：

```nginx
location /v1/ {
    proxy_pass http://127.0.0.1:8787;
    proxy_buffering off;          # SSE 流式必须关掉缓冲
    proxy_read_timeout 3600s;
    proxy_set_header X-Accel-Buffering no;
}

# 面板只允许内网访问
location / {
    allow 192.168.0.0/16;
    deny all;
    proxy_pass http://127.0.0.1:8787;
}
```

上游密钥在数据库里是明文存储的（SQLite 文件），请自行保管好 `data` 目录的访问权限。

---

## 项目结构

```
cmd/relay/            程序入口
internal/config/      环境变量配置
internal/store/       SQLite 数据层（渠道 / 模型 / 秘钥 / 技能 / 任务 / 日志）
internal/relay/       上游适配器与调度引擎
  ├── engine.go         模型解析、候选排序、故障转移
  ├── openai.go         OpenAI 兼容协议
  ├── gemini.go         Gemini 原生协议
  ├── anthropic.go      Anthropic 原生协议
  ├── template.go       模板驱动适配器
  └── video.go          视频任务模板与状态归一化
internal/api/         HTTP 层
  ├── server.go         路由与鉴权中间件
  ├── v1.go             对外 /v1 接口
  ├── admin.go          面板管理接口
  ├── playground.go     调试台、技能渲染、连接器生成器
  └── worker.go         后台视频轮询与日志裁剪
web/                  Vue 3 + Vite 网页面板
docs/API.md           完整接口文档
deploy/               Dockerfile、compose、arm64 构建脚本
```

---

## 技术栈

Go 1.26 · SQLite（`modernc.org/sqlite`，纯 Go 无 CGO）· Vue 3 · Vite · TypeScript

---

## 许可

[MIT](LICENSE)