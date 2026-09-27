// 与后端 internal/api 面板接口一一对应的类型与调用封装。

export interface Provider {
  id: number
  name: string
  kind: string
  base_urls: string[]
  api_keys: string[]
  headers: Record<string, string>
  strategy: string
  timeout_sec: number
  enabled: boolean
  remark: string
  /** 上游接口直接挂在根路径、没有 /v1 版本前缀（如 GitHub Models） */
  no_version_path?: boolean
  created_at: string
  updated_at: string
  model_count?: number
}

export interface ResponseMap {
  task_id?: string
  status?: string
  progress?: string
  video_url?: string
  image_url?: string
  content?: string
  error?: string
  status_map?: Record<string, string>
  success_value?: string
  pending_value?: string
  failed_value?: string
}

export interface TemplateSpec {
  method?: string
  path?: string
  headers?: Record<string, string>
  body?: Record<string, any>
  query?: Record<string, string>
  response?: ResponseMap
}

export interface ModelConfig {
  system_prompt?: string
  defaults?: Record<string, any>
  /** 绑定多个渠道时的调度方式：failover（默认）| round_robin | random */
  route_strategy?: string
  /** 模型档位，供 auto 自动按任务难度挑选：light | standard | pro */
  tier?: string
  /** 智力分：主模型测评给出的 0-100 综合分，0/缺省表示未测评；auto 同档位内优先选高分 */
  intelligence?: number
  /** 最近一次智力测评时间（RFC3339） */
  benchmarked_at?: string
  /** 图片请求体组装模式：standard（默认）| extra_body（Agnes 等要求放进 extra_body） */
  image_mode?: string
  /** 上下文窗口（token）。留空时回退到模型库探测值，再按上游模型名推断 */
  context_length?: number
  /** 限速（仅对对外模型生效，0 表示不限制）。 */
  rpm?: number
  /** 每分钟 token 上限（按实际消耗累加），0 表示不限制。 */
  tpm?: number
  /** 单价（计费用，单位自行统一；0 表示不计费）。 */
  price_in?: number
  /** 每 1K 输入 token 价格。 */
  price_out?: number
  /** 每 1K 输出 token 价格。 */
  price_image?: number
  /** 每张生成图片价格。 */
  chat?: TemplateSpec | null
  image?: TemplateSpec | null
  video?: TemplateSpec | null
  video_query?: TemplateSpec | null
}

/** 一个上游输入：某渠道下的某个具体上游模型 */
export interface ModelInput {
  provider_id: number
  upstream_name: string
  /** 支持的输入模态，如 ["text","image"]；留空自动推断 */
  input_modalities?: string[]
  /** 产出的输出模态，如 ["text"]；留空自动推断 */
  output_modalities?: string[]
}

/** 内置模型预设：选好渠道即开箱可用 */
export interface ModelPreset {
  id: string
  name: string
  /** 需要绑定哪种渠道类型 */
  provider_kind: string
  provider_name: string
  upstream_name: string
  capability: string
  context_length?: number
  input_modalities: string[]
  output_modalities: string[]
  free: boolean
  description: string
  default_params?: Record<string, any>
  video?: boolean
  /** 该平台的内置入口地址（OpenAI 兼容类平台自带，无需手填） */
  provider_base_url?: string
  /** 申请该平台 API Key 的地址 */
  signup_url?: string
  /** 开通前置条件提示（实名认证、绑阿里云账号等） */
  provider_note?: string
  /** 上游接口无 /v1 版本前缀（如 GitHub Models），创建渠道时需要带上 */
  no_version_path?: boolean
  /** 开通该平台是否需要实名认证（大陆平台普遍要求，海外平台基本免实名） */
  realname_required?: boolean
}

export interface Model {
  id: number
  provider_id: number
  /** 绑定的渠道列表（有序，首个为默认），失败时按策略自动切换 */
  provider_ids: number[]
  /** 上游输入列表：每个输入是一个「渠道 + 上游模型」组合 */
  inputs: ModelInput[]
  provider_name?: string
  provider_kind?: string
  name: string
  upstream_name: string
  capability: string
  config: ModelConfig
  /** 模型库中探测到的上下文长度，只读回退值 */
  catalog_context_length?: number
  enabled: boolean
  /** 当前是否可用（至少绑定一个可调通的渠道）；?all=1 时附带 */
  usable?: boolean
  remark: string
  created_at: string
  updated_at: string
}

/** 模型库条目：渠道探测到或手工登记的上游模型，仅作配置候选，不直接对外 */
export interface CatalogModel {
  id: number
  provider_id: number
  provider_name?: string
  provider_kind?: string
  model_id: string
  capability: string
  source: string
  enabled: boolean
  remark: string
  created_at: string
  updated_at: string
  /** 探测到的上下文窗口（token），0 或缺失表示未知 */
  context_length?: number
  /** 被多少个对外模型引用 */
  used_by?: number
}

export interface APIKey {
  id: number
  name: string
  key: string
  models: string[]
  enabled: boolean
  expires_at: string
  remark: string
  created_at: string
  last_used_at: string
  /** 配额，0 表示不限制 */
  rpm: number
  daily_quota: number
  daily_tokens: number
  calls?: number
}

/** 渠道健康状态（引擎熔断） */
export interface ProviderHealth {
  provider_id: number
  open: boolean
  fails: number
  remain_sec: number
  last_error?: string
}

export interface ModelStat {
  model: string
  calls: number
  failed: number
  tokens: number
  avg_latency_ms: number
}

export interface ProviderStat {
  provider: string
  calls: number
  failed: number
  avg_latency_ms: number
}

export interface StatsReport {
  days: number
  by_model: ModelStat[]
  by_provider: ProviderStat[]
  by_day: { date: string; total: number; failed: number }[]
}

export interface SkillParam {
  key: string
  label: string
  type: string
  default?: string
  options?: string[]
  placeholder?: string
}

export interface Skill {
  id: number
  slug: string
  name: string
  description: string
  category: string
  icon: string
  capability: string
  prompt: string
  params: SkillParam[]
  builtin: boolean
  enabled: boolean
  sort_order: number
  created_at: string
  updated_at: string
}

/** 自动聚合计划：给已有上游补模态声明 */
export interface AggDeclareItem {
  key: string
  model_id: number
  model_name: string
  provider_id: number
  provider_name: string
  upstream_name: string
  modalities: string[]
}

/** 自动聚合计划：新建聚合模型（视觉主上游 + 纯文本故障转移） */
export interface AggCreateItem {
  key: string
  name: string
  provider_id: number
  provider_name: string
  upstream_name: string
  modalities: string[]
  failovers: string[]
}

/** 上游健康画像：渠道 × 上游 的累计调用统计（持久化 + 内存合并） */
export interface UpstreamStat {
  provider_id: number
  provider_name?: string
  upstream_name: string
  ok_count: number
  fail_count: number
  consec_fails: number
  avg_latency_ms: number
  success_rate?: number
  last_ok_at?: string
  last_fail_at?: string
  last_error?: string
  updated_at?: string
}

/** AI 建模：主 AI 产出的模型草案（store.Model 形状，可直接保存） */
export interface AIModelDraft {
  name: string
  upstream_name: string
  capability: string
  remark: string
  enabled: boolean
  inputs: ModelInput[]
  config: { route_strategy?: string; [k: string]: any }
}

/** AI 建模响应 */
export interface AICreateResponse {
  reply: string
  models: AIModelDraft[]
  warnings: string[]
  system_model: string
  latency_ms: number
  usage?: { prompt_tokens: number; completion_tokens: number }
}

export interface UsageLog {
  id: number
  created_at: string
  capability: string
  model: string
  provider_name: string
  api_key_name: string
  status: number
  stream: boolean
  latency_ms: number
  prompt_tokens: number
  completion_tokens: number
  cost?: number
  error: string
  request_body?: string
  response_preview?: string
}

export interface VideoTask {
  id: string
  object: string
  model: string
  /** 实际命中的上游渠道（面板调试台返回） */
  provider?: string
  status: string
  progress: number
  created_at: string
  updated_at: string
  video_url?: string
  image_url?: string
  error?: { message: string; type: string }
}

export interface Stats {
  providers: number
  models: number
  keys: number
  skills: number
  today_calls: number
  total_calls: number
  success_rate: number
  avg_latency_ms: number
  prompt_tokens: number
  completion_tokens: number
  failed_calls: number
}

export interface OptionItem {
  value: string
  label: string
  hint?: string
  endpoint?: string
}

export interface VideoPreset {
  name: string
  description: string
  capability: string
  video?: TemplateSpec
  video_query?: TemplateSpec
}

/** 服务端运行参数：面板里改完即刻生效，无需重启。 */
export interface RuntimeOptions {
  max_log_rows: number
  log_body: boolean
  /** 单条日志请求/响应体的记录上限（KB），超出截断 */
  log_body_limit_kb: number
  stream_smooth: boolean
  stream_rate: number
  heartbeat_sec: number
  video_poll_sec: number
  cache_ttl: number
  alert_enabled: boolean
  alert_min_fails: number

  /** 全局上游请求兜底超时（秒），渠道未单独配置时生效 */
  request_timeout_sec: number
  /** 调用日志按天保留天数，0 表示不按天清理 */
  log_retention_days: number

  // ---- 无人值守：健康巡检 ----
  health_enabled: boolean
  health_interval: number
  health_recover: boolean
  health_keep: number
  health_timeout: number

  // ---- 无人值守：日报与预警 ----
  report_enabled: boolean
  report_hour: number
  report_fail_rate: number
  report_key_days: number

  // ---- 无人值守：自动备份 ----
  backup_auto_enabled: boolean
  backup_auto_hour: number
  backup_auto_keep: number

  // ---- 无人值守：负载再平衡 ----
  /** off | prefer_fast | prefer_healthy */
  balance_mode: string
  balance_bad: number

  // ---- 通知：邮件 ----
  email_enabled: boolean
  email_host: string
  email_port: number
  email_user: string
  email_from: string
  email_to: string
  email_insecure: boolean
  /** 库里是否已存 SMTP 密码（密码本身不回传） */
  has_email_pass: boolean

  /** 面板对外地址，用于通知里回链 */
  public_url: string
}

/** 自检项的严重程度。 */
export type CheckLevel = 'ok' | 'warn' | 'error'

export interface CheckItem {
  id: string
  level: CheckLevel
  title: string
  detail?: string
  hint?: string
  related?: string[]
}

export interface SelfCheck {
  checked_at: string
  ok_count: number
  warn_count: number
  error_count: number
  items: CheckItem[]
}

/** 一条渠道巡检记录。 */
export interface HealthCheck {
  id: number
  provider_id: number
  ok: boolean
  status: number
  latency_ms: number
  error?: string
  checked_at: string
}

/** 某渠道的可用率摘要。 */
export interface ProviderUptime {
  provider_id: number
  total: number
  ok_count: number
  /** 可用率百分比；无样本时为 -1 */
  uptime: number
  avg_latency_ms: number
  last_ok: boolean
  last_at?: string
  last_error?: string
  consecutive_fails: number
}

export interface RuntimeInfo {
  /** 当前生效值 */
  values: RuntimeOptions
  /** 环境变量等启动配置提供的默认值 */
  defaults: RuntimeOptions
}

/** 首次进入面板时的引导状态：内置免费渠道、模型与 API Key 情况。 */
export interface SetupStatus {
  seeded: boolean
  provider_id: number
  provider_name: string
  provider_kind: string
  models: string[]
  /** 内置渠道还没有 API Key，需要用户填写 */
  needs_key: boolean
  signup_url: string
  /** 已启用的访问秘钥数量，为 0 时引导顺手创建一个 */
  access_keys: number
}

export interface Meta {
  kinds: OptionItem[]
  capabilities: OptionItem[]
  strategies: OptionItem[]
  video_presets: VideoPreset[]
  settings: Record<string, string>
  runtime?: RuntimeInfo
  /** 内置的免费模型预设 */
  model_presets?: ModelPreset[]
}

export interface Overview {
  stats: Stats
  daily: { date: string; total: number; failed: number }[]
  recent_logs: UsageLog[]
  recent_tasks: VideoTask[]
}

export interface ProbeModel {
  id: string
  /** 推断出的能力：chat | image | video | embedding，空表示未识别 */
  capability: string
  hint?: string
  /** 从上游模型列表里读到的上下文窗口，读不到时为 0 */
  context_length?: number
}

/** 智力测评结果：单模型三道题的综合评分与档位。 */
export interface BenchResult {
  model: string
  score: number
  tier: string
  items: { question: string; score: number; reason: string; strict_ok: boolean }[]
}

export interface ProbeResult {
  url: string
  ok: boolean
  status: number
  latency_ms: number
  detail: string
  models?: ProbeModel[]
}

export interface ConnectorDraft {
  name?: string
  upstream_name?: string
  capability?: string
  remark?: string
  parse_error?: string
  video?: TemplateSpec
  video_query?: TemplateSpec
  [key: string]: any
}

/** postBinary 发送 JSON 请求并期望二进制响应（语音合成返回的是音频流）。 */
async function postBinary(path: string, body: any): Promise<Blob> {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    // 失败时上游返回的是 JSON 错误，读出来给用户看。
    throw new Error(errorTextOf(await res.text(), res.status))
  }
  return await res.blob()
}

/** postForm 发送 multipart 表单，boundary 由浏览器自动带上。 */
async function postForm<T>(path: string, form: FormData): Promise<T> {
  const res = await fetch(path, { method: 'POST', body: form })
  const text = await res.text()
  if (!res.ok) {
    throw new Error(errorTextOf(text, res.status))
  }
  return text ? (JSON.parse(text) as T) : (null as T)
}

/** errorTextOf 从错误响应里提取可读信息，兼容 JSON 与纯文本两种返回。 */
function errorTextOf(text: string, status: number): string {
  let data: any = text
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  return (
    data?.error?.message ||
    (typeof data === 'string' && data ? data : '') ||
    `请求失败（HTTP ${status}）`
  )
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const { headers, ...rest } = options
  const res = await fetch(path, {
    ...rest,
    headers: { 'Content-Type': 'application/json', ...(headers as Record<string, string>) },
  })
  const text = await res.text()
  let data: any = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (!res.ok) {
    const msg =
      data?.error?.message ||
      (typeof data === 'string' && data ? data : null) ||
      `请求失败（HTTP ${res.status}）`
    throw new Error(msg)
  }
  return data as T
}

const post = <T>(path: string, body?: any) =>
  request<T>(path, { method: 'POST', body: body === undefined ? undefined : JSON.stringify(body) })
const put = <T>(path: string, body?: any) =>
  request<T>(path, { method: 'PUT', body: body === undefined ? undefined : JSON.stringify(body) })
const del = <T>(path: string) => request<T>(path, { method: 'DELETE' })

export interface StreamHandlers {
  onDelta?: (content: string, reasoning: string) => void
  onToolCalls?: (calls: any[]) => void
  onError?: (message: string) => void
  onDone?: () => void
  /** 面板调试台专用：本次调用最终命中的上游渠道与（auto 时的）难度评估 */
  onMeta?: (meta: {
    model?: string
    provider?: string
    requested?: string
    difficulty?: { score: number; tier: string; reason: string }
  }) => void
}

export const api = {
  // 元信息 / 概览
  getMeta: () => request<Meta>('/api/meta'),
  getOverview: () => request<Overview>('/api/overview'),

  // 渠道
  listProviders: () => request<Provider[]>('/api/providers'),
  saveProvider: (p: Partial<Provider>) =>
    p.id ? put<Provider>(`/api/providers/${p.id}`, p) : post<Provider>('/api/providers', p),
  deleteProvider: (id: number) => del<{ ok: boolean }>(`/api/providers/${id}`),
  testProvider: (payload: { id?: number; kind?: string; base_url?: string; api_key?: string; no_version_path?: boolean }) =>
    post<{ results: ProbeResult[] }>('/api/providers/test', payload),

  // 渠道健康（熔断状态）
  providerHealth: () => request<ProviderHealth[]>('/api/providers/health'),
  resetProviderHealth: (id: number) => post<{ ok: boolean }>(`/api/providers/${id}/reset`),
  resetAllProviderHealth: () => post<{ ok: boolean }>('/api/providers/reset'),

  // 用量报表与备份
  getStats: (days = 7) => request<StatsReport>(`/api/stats?days=${days}`),
  exportBackup: () => request<any>('/api/backup'),
  importBackup: (payload: any) => post<Record<string, number>>('/api/backup', payload),

  // 模型
  /** all=true 返回全量并附带 usable 标记；默认只返回可用模型（渠道至少可调通） */
  listModels: (params: { all?: boolean } = {}) =>
    request<Model[]>(`/api/models${params.all ? '?all=1' : ''}`),
  saveModel: (m: Partial<Model>) =>
    m.id ? put<Model>(`/api/models/${m.id}`, m) : post<Model>('/api/models', m),
  /** 用内置预设一键创建模型，只需给出预设 ID 与绑定的渠道 */
  createModelFromPreset: (body: { preset_id: string; provider_id: number; name?: string }) =>
    post<Model | { ok: true; name: string; existed: true }>('/api/models/preset', body),
  /** 主模型智力测评：三道固定题 + 主模型评分，结果写回模型档位 */
  benchmarkModel: (model: string) =>
    post<BenchResult>('/api/models/benchmark', { model }),
  deleteModel: (id: number) => del<{ ok: boolean }>(`/api/models/${id}`),

  // 模型库
  listCatalog: (params: { provider_id?: number; capability?: string } = {}) => {
    const q = new URLSearchParams()
    if (params.provider_id) q.set('provider_id', String(params.provider_id))
    if (params.capability) q.set('capability', params.capability)
    return request<CatalogModel[]>(`/api/catalog?${q.toString()}`)
  },
  saveCatalog: (m: Partial<CatalogModel>) =>
    m.id ? put<CatalogModel>(`/api/catalog/${m.id}`, m) : post<CatalogModel>('/api/catalog', m),
  deleteCatalog: (id: number) => del<{ ok: boolean }>(`/api/catalog/${id}`),
  importCatalog: (body: { provider_id: number; models: Partial<CatalogModel>[] }) =>
    post<{ created: number; updated: number }>('/api/catalog/import', body),

  // 秘钥
  listKeys: () => request<APIKey[]>('/api/keys'),
  saveKey: (k: Partial<APIKey>) =>
    k.id ? put<APIKey>(`/api/keys/${k.id}`, k) : post<APIKey>('/api/keys', k),
  deleteKey: (id: number) => del<{ ok: boolean }>(`/api/keys/${id}`),

  // 技能
  listSkills: () => request<Skill[]>('/api/skills'),
  saveSkill: (s: Partial<Skill>) =>
    s.id ? put<Skill>(`/api/skills/${s.id}`, s) : post<Skill>('/api/skills', s),
  deleteSkill: (id: number) => del<{ ok: boolean }>(`/api/skills/${id}`),
  generateSkill: (body: { description: string; capability?: string }) =>
    post<{ draft: any; raw_reply: string; system_model: string }>('/api/skills/generate', body),

  // 任务 / 日志
  // 多模态自动聚合：apply=false 只返回计划，apply=true 按勾选的 key 应用
  autoAggregate: (body: { apply?: boolean; declares?: string[]; creates?: string[] } = {}) =>
    post<{ declares: AggDeclareItem[]; creates: AggCreateItem[]; applied?: number }>(
      '/api/models/auto-aggregate',
      body,
    ),
  // AI 建模：主 AI 把自然语言转成模型配置草案（不落库，预览后走 saveModel）
  aiCreateModel: (prompt: string) => post<AICreateResponse>('/api/models/ai-create', { prompt }),
  // 站内 AI 智能体：主 AI 带工具循环，与用户同权限执行管理操作
  agentChat: (messages: { role: string; content: string }[]) =>
    post<{
      reply: string
      steps: { action: string; params?: Record<string, any>; ok: boolean; summary?: string; error?: string }[]
      system_model: string
      latency_ms: number
    }>('/api/agent/chat', { messages }),
  // 流式版：每步工具执行实时推送，最终回复单独推送（SSE，`data:` 行内带 type 字段）
  agentChatStream: (messages: { role: string; content: string }[], handlers: AgentStreamHandlers, signal?: AbortSignal) =>
    streamAgentChat(messages, handlers, signal),
  // 上游健康画像：每次真实调用的滚动累计，供展示与排障
  upstreamStats: () => request<UpstreamStat[]>('/api/stats/upstreams'),
  listTasks: (limit = 50) => request<VideoTask[]>(`/api/tasks?limit=${limit}`),
  deleteTask: (id: string) => del<{ ok: boolean }>(`/api/tasks/${encodeURIComponent(id)}`),
  listLogs: (params: { limit?: number; before?: number; capability?: string; q?: string } = {}) => {
    const q = new URLSearchParams()
    if (params.limit) q.set('limit', String(params.limit))
    if (params.before) q.set('before', String(params.before))
    if (params.capability) q.set('capability', params.capability)
    if (params.q) q.set('q', params.q)
    return request<UsageLog[]>(`/api/logs?${q.toString()}`)
  },
  clearLogs: () => del<{ ok: boolean }>('/api/logs'),

  // 首次引导
  getSetup: () => request<SetupStatus>('/api/setup'),
  saveSetup: (body: { api_key?: string; create_access_key?: boolean; access_key_name?: string }) =>
    post<{ saved_provider?: string; access_key?: string; status: SetupStatus }>('/api/setup', body),

  // 设置
  getSettings: () => request<Record<string, string>>('/api/settings'),
  saveSettings: (payload: Record<string, string>) =>
    post<Record<string, string>>('/api/settings', payload),

  // 无人值守：自检 / 巡检历史 / 通知测试
  selfCheck: () => request<SelfCheck>('/api/selfcheck'),
  healthHistory: (params: { provider_id?: number; limit?: number; hours?: number } = {}) => {
    const q = new URLSearchParams()
    if (params.provider_id) q.set('provider_id', String(params.provider_id))
    if (params.limit) q.set('limit', String(params.limit))
    if (params.hours) q.set('hours', String(params.hours))
    const qs = q.toString()
    return request<{ checks: HealthCheck[]; uptime: Record<string, ProviderUptime> }>(
      '/api/health/history' + (qs ? `?${qs}` : ''),
    )
  },
  testNotify: () =>
    post<{ ok: boolean; sent: number; channels: string[] }>('/api/notify/test', {}),

  /** 飞书整合测试：校验应用凭据；带 chat_id 时顺便发一条测试消息 */
  bridgeFeishuTest: (chatId?: string) =>
    post<{ token_ok: boolean; send_ok?: boolean; error?: string }>('/api/bridge/feishu/test', {
      chat_id: chatId || '',
    }),

  bridgeFeishuStatus: () =>
    request<{
      enabled: boolean
      transport: string
      credentials_ok: boolean
      ws_connected: boolean
      ws_last_error: string
      mode: string
      agent_ready: boolean
      chat_ready: boolean
      notify_chat_id: string
      alert_enabled: boolean
      report_enabled: boolean
      report_weekday: string
      report_hour: string
    }>('/api/bridge/feishu/status'),

  // 调试台
  playgroundChat: (body: any) => post<any>('/api/playground/chat', body),
  playgroundImage: (body: any) => post<any>('/api/playground/image', body),
  playgroundVideo: (body: any) => post<VideoTask>('/api/playground/video', body),
  /** 语音合成：返回音频二进制，页面可直接播放或下载 */
  playgroundSpeech: (body: any) => postBinary('/api/playground/speech', body),
  /** 语音识别：以 multipart 上传音频文件 */
  playgroundTranscribe: (form: FormData) =>
    postForm<{ text?: string; [key: string]: any }>('/api/playground/transcriptions', form),

  // 连接器生成器
  connectorGenerate: (body: {
    description?: string
    base_url?: string
    sample_request?: string
    sample_response?: string
    capability?: string
  }) =>
    post<{ draft: ConnectorDraft; raw_reply: string; system_model: string }>(
      '/api/connectors/generate',
      body,
    ),
  connectorTry: (body: {
    provider_id: number
    upstream_name?: string
    prompt?: string
    duration?: number
    size?: string
    spec: TemplateSpec
  }) => post<{ ok: boolean; response?: any; error?: string }>('/api/connectors/try', body),
}

// 调试台流式对话：解析 OpenAI 风格 SSE 分片。
export async function streamChat(
  body: any,
  handlers: StreamHandlers,
  signal?: AbortSignal,
): Promise<void> {
  let res: Response
  try {
    res = await fetch('/api/playground/chat', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
      signal,
    })
  } catch (e: any) {
    if (e?.name === 'AbortError') return
    handlers.onError?.(e?.message || '网络请求失败')
    handlers.onDone?.()
    return
  }
  if (!res.ok || !res.body) {
    let msg = `请求失败（HTTP ${res.status}）`
    try {
      const text = await res.text()
      const parsed = JSON.parse(text)
      msg = parsed?.error?.message || msg
    } catch {
      /* ignore */
    }
    handlers.onError?.(msg)
    handlers.onDone?.()
    return
  }
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const events = buffer.split('\n\n')
      buffer = events.pop() || ''
      for (const evt of events) {
        const line = evt.trim()
        if (!line.startsWith('data:')) continue
        const payload = line.slice(5).trim()
        if (payload === '[DONE]') {
          handlers.onDone?.()
          return
        }
        try {
          const chunk = JSON.parse(payload)
          if (chunk.object === 'relay.meta') {
            handlers.onMeta?.({
              model: chunk.model,
              provider: chunk.provider,
              requested: chunk.requested,
              difficulty: chunk.difficulty,
            })
            continue
          }
          if (chunk.error) {
            handlers.onError?.(chunk.error.message || '上游返回错误')
            continue
          }
          const delta = chunk.choices?.[0]?.delta
          if (delta) {
            if (delta.content || delta.reasoning_content) {
              handlers.onDelta?.(delta.content || '', delta.reasoning_content || '')
            }
            if (delta.tool_calls) handlers.onToolCalls?.(delta.tool_calls)
          }
        } catch {
          /* 忽略无法解析的分片 */
        }
      }
    }
  } catch (e: any) {
    if (e?.name !== 'AbortError') handlers.onError?.(e?.message || '流式读取中断')
  }
  handlers.onDone?.()
}

// 站内 AI 智能体的流式事件回调。
export interface AgentStreamHandlers {
  // 每完成一步工具（查/改/删）调用一次，step 与历史工具轨迹结构一致。
  onStep?: (step: { action: string; ok: boolean; summary?: string; error?: string }) => void
  // 最终回复（可能附带模型在 settings 字段里已热生效的设置变更）。
  onReply?: (r: { reply: string; system_model: string; latency_ms: number }) => void
  onError?: (message: string) => void
  onDone?: () => void
}

// streamAgentChat 以 SSE 消费 /api/agent/chat（stream=true）：
// 后端每步推 {type:"step"}，结束推 {type:"reply"}，异常推 {type:"error"}。
export async function streamAgentChat(
  messages: { role: string; content: string }[],
  handlers: AgentStreamHandlers,
  signal?: AbortSignal,
): Promise<void> {
  let res: Response
  try {
    res = await fetch('/api/agent/chat', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ messages, stream: true }),
      signal,
    })
  } catch (e: any) {
    if (e?.name === 'AbortError') return
    handlers.onError?.(e?.message || '网络请求失败')
    handlers.onDone?.()
    return
  }
  if (!res.ok || !res.body) {
    let msg = `请求失败（HTTP ${res.status}）`
    try {
      const parsed = JSON.parse(await res.text())
      msg = parsed?.error?.message || msg
    } catch {
      /* ignore */
    }
    handlers.onError?.(msg)
    handlers.onDone?.()
    return
  }
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const events = buffer.split('\n\n')
      buffer = events.pop() || ''
      for (const evt of events) {
        const line = evt.trim()
        if (!line.startsWith('data:')) continue
        const payload = line.slice(5).trim()
        if (payload === '[DONE]') {
          handlers.onDone?.()
          return
        }
        let chunk: any
        try {
          chunk = JSON.parse(payload)
        } catch {
          continue
        }
        if (chunk.type === 'step' && chunk.step) {
          handlers.onStep?.(chunk.step)
        } else if (chunk.type === 'reply') {
          handlers.onReply?.({ reply: chunk.reply, system_model: chunk.system_model, latency_ms: chunk.latency_ms })
        } else if (chunk.type === 'error') {
          handlers.onError?.(chunk.message || '智能体执行失败')
        }
      }
    }
  } catch (e: any) {
    if (e?.name !== 'AbortError') handlers.onError?.(e?.message || '流式读取中断')
  }
  handlers.onDone?.()
}
