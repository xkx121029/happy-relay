# 飞书 / 企业微信 告警接入教程

> 适用版本：本仓库已实现「渠道失败 → 群机器人告警」功能（见 `internal/api/alert.go`）。
> 你只需提供一个群机器人 Webhook 地址，中继在对话请求失败时会自动往群里推一条文本告警。

---

## 一、它能做什么

当某个上游渠道挂掉（5xx、超时、鉴权失败、熔断等）导致对话请求失败时，
中继会向配置好的 **飞书群 / 企业微信群机器人** 推送一条文本消息：

```
【Happy Relay 告警】对话请求失败
模型：gpt-4o
渠道：OpenAI 主渠道
错误：upstream returned 500 ...
```

让你第一时间知道「哪个模型、哪个渠道、什么错」，不用盯日志。

---

## 二、先建一个群机器人（两种平台任选）

### A. 飞书（Feishu）

1. 打开目标飞书群 → 右上角 **···** → **群机器人** → **添加机器人** → **自定义机器人**。
2. 机器人名称填 `Happy Relay 告警`。
3. **安全设置** 建议选 **自定义关键词**，加一个关键词：`Happy Relay`
   （因为代码推送的文本固定以「【Happy Relay 告警】」开头，加了关键词才不会被判为非法消息）。
4. 创建后复制 **Webhook 地址**，形如：
   ```
   https://open.feishu.cn/open-apis/bot/v2/hook/xxxx-xxxx-xxxx
   ```

### B. 企业微信（WeCom）

1. 群聊右上角 **···** → **群机器人** → **添加** → **新建一个机器人**。
2. 复制 **Webhook 地址**，形如：
   ```
   https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxx
   ```

> 代码会按 Webhook 域名 **自动识别** 飞书还是企微并选用对应消息格式，你不用管格式，贴对地址即可。

---

## 三、把地址交给中继（二选一）

### 方式 1：环境变量（推荐新手，改一次重启生效）

在启动中继的环境里加一行：

```bash
# Linux / macOS
export RELAY_ALERT_WEBHOOK="https://open.feishu.cn/open-apis/bot/v2/hook/你的地址"

# Windows PowerShell
$env:RELAY_ALERT_WEBHOOK="https://open.feishu.cn/open-apis/bot/v2/hook/你的地址"
```

Docker 部署则在 `docker-compose.yml` 的 `environment:` 下加这一行，然后 `docker compose up -d`。

只要这个变量非空，告警即视为「已开启」。

### 方式 2：管理接口热更新（不改环境、不重启）

中继提供内部管理接口 `POST /api/settings`，提交即生效：

```bash
curl -X POST "http://你的relay地址/api/settings" \
  -H "Content-Type: application/json" \
  -d '{"alert_webhook":"https://open.feishu.cn/open-apis/bot/v2/hook/你的地址"}'
```

（面板「设置」页若已对接该接口，直接在页面里填 `alert_webhook` 即可。）

### 可选开关

| 设置键 | 说明 | 默认 |
| --- | --- | --- |
| `alert_webhook` | 机器人 Webhook 地址（同上） | 空 |
| `alert_enabled` | `on` / `off`：即使没配环境变量也强制开/关 | `off`（配了 webhook 自动开） |
| `alert_min_fails` | 同一模型近 5 分钟内连续失败多少次才推送，避免抖动刷屏 | `1` |

例如设为「连续失败 3 次才告警」：

```bash
curl -X POST "http://你的relay地址/api/settings" \
  -H "Content-Type: application/json" \
  -d '{"alert_min_fails":"3"}'
```

---

## 四、验证是否生效

1. 看中继日志，推送成功/失败都会打 `[alert]` 前缀日志。
2. 故意让某渠道不可用（比如填错 API Key 或临时把上游 BaseURL 改错），打一个请求，群里应收到上面的告警文本。

---

## 五、实现要点（给想改代码的你）

- 入口：`internal/api/v1.go` 的 `runChat`，在流式 / 非流式两条失败分支都调用 `s.notifyAlert(...)`。
- 核心：`internal/api/alert.go`
  - `alertEnabled()`：环境变量或 `alert_enabled=on` 即开。
  - `alertWebhook()`：面板设置优先，其次环境变量。
  - `notifyAlert(scope, title, detail)`：先按 `scope`（模型名）做 5 分钟滑动窗口去抖，失败次数 ≥ `alert_min_fails` 才推。
  - `buildAlertPayload()`：按域名自动选飞书 `msg_type:text` 或企微 `msgtype:text`。
- 推送失败 **只打日志，绝不阻塞主请求**。
- 数据库迁移：首次启动会自动给 `usage_logs` 加 `cost` 列、给 `providers` 加 `weight` 列（见 `internal/store/store.go` 的 `migrate()`）。

---

## 六、边界与诚实说明

- **当前只在「对话请求失败」路径触发告警**（流式/非流式都覆盖）。图片生成、embedding 等其他能力的失败暂未接入，需要可补一两行 `s.notifyAlert(...)`。
- 群机器人有频率限制（飞书约 20 条/分钟），上游大规模故障时可能触发限流，属正常现象，不会拖垮主服务。
- 这只是一个「失败通知」，不是自动恢复。自动切换渠道/熔断已由 `internal/relay/engine.go` 的 `failover` 策略 + 熔断器负责。
