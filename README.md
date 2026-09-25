# Happy Relay · 模型中转站

把手上零散的 AI 上游聚合成一套标准接口的自用中转站。

一个 Go 单文件二进制（内嵌网页面板）+ 一个 SQLite 文件，没有任何外部依赖。渠道支持 OpenAI 兼容协议、Gemini 与 Anthropic 原生协议，以及一个「模板驱动」适配器用于接入任意奇形怪状的 HTTP 上游；对外统一输出标准的 `/v1/chat/completions`、`/v1/images/generations`、`/v1/embeddings` 和任务式 `/v1/videos`。

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

**四类上游协议**

| 类型 | 说明 |
| --- | --- |
| `openai` | OpenAI 兼容，覆盖绝大多数中转商与自部署服务（vLLM、Ollama、one-api…） |
| `gemini` | Google Gemini 原生 `generateContent` |
| `anthropic` | Anthropic 原生 Messages |
| `template` | 自定义模板：手填请求体与响应字段路径，可接任意 HTTP 上游 |

**统一对外接口**

- `/v1/chat/completions`：支持 SSE 流式、多模态消息、function calling；无论上游是哪个协议，出去的格式都统一成 OpenAI 分片
- `/v1/images/generations`、`/v1/embeddings`、`/v1/videos`（异步任务 + 后台自动轮询 + `/{id}/stream` 进度流）
- **流式输出平滑**：上游一次吐出的长文本会拆成小段匀速下发，客户端观感均匀；空闲时自动发心跳保活
- **技能可对外调用**：`GET /v1/skills` 拿到技能清单与说明，调用时带 `skill` + `skill_params` 即可套用提示词模板
- 对外模型名与上游模型名解耦，同一个上游模型可以暴露成多个不同默认参数的对外模型
- 跨协议状态归一化：上游五花八门的 `processing` / `done` / `finished` 统一成 `queued` / `running` / `succeeded` / `failed`
- `/healthz` 免鉴权探活

**网页面板**

- 渠道、模型、秘钥、技能、日志、设置，全程可视化配置
- **调试台**：不建秘钥就能直接在网页里跟模型对话（流式逐字输出）、出图、出视频
- **站内技能**：带参数的提示词模板，一键套用，预置 10 个常用动作
- **连接器生成器**：用系统级 AI 把一段接口描述翻译成可用的上游适配模板，并支持当场验证
- **AI 生成技能**：描述一个任务，系统级 AI 自动产出带参数的提示词模板
- **任务面板**：视频任务的进度、结果与取消
- 秘钥级模型白名单与过期时间；调用日志含 token、耗时、请求体与响应预览

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