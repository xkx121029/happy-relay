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
  chat?: TemplateSpec | null
  image?: TemplateSpec | null
  video?: TemplateSpec | null
  video_query?: TemplateSpec | null
}

export interface Model {
  id: number
  provider_id: number
  /** 绑定的渠道列表（有序，首个为默认），失败时按策略自动切换 */
  provider_ids: number[]
  provider_name?: string
  provider_kind?: string
  name: string
  upstream_name: string
  capability: string
  config: ModelConfig
  enabled: boolean
  remark: string
  created_at: string
  updated_at: string
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
  calls?: number
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

export interface Meta {
  kinds: OptionItem[]
  capabilities: OptionItem[]
  strategies: OptionItem[]
  video_presets: VideoPreset[]
  settings: Record<string, string>
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
  /** 面板调试台专用：本次调用最终命中的上游渠道 */
  onMeta?: (meta: { model?: string; provider?: string }) => void
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
  testProvider: (payload: { id?: number; kind?: string; base_url?: string; api_key?: string }) =>
    post<{ results: ProbeResult[] }>('/api/providers/test', payload),

  // 模型
  listModels: () => request<Model[]>('/api/models'),
  saveModel: (m: Partial<Model>) =>
    m.id ? put<Model>(`/api/models/${m.id}`, m) : post<Model>('/api/models', m),
  deleteModel: (id: number) => del<{ ok: boolean }>(`/api/models/${id}`),

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
  listTasks: (limit = 50) => request<VideoTask[]>(`/api/tasks?limit=${limit}`),
  deleteTask: (id: string) => del<{ ok: boolean }>(`/api/tasks/${encodeURIComponent(id)}`),
  listLogs: (params: { limit?: number; capability?: string; q?: string } = {}) => {
    const q = new URLSearchParams()
    if (params.limit) q.set('limit', String(params.limit))
    if (params.capability) q.set('capability', params.capability)
    if (params.q) q.set('q', params.q)
    return request<UsageLog[]>(`/api/logs?${q.toString()}`)
  },
  clearLogs: () => del<{ ok: boolean }>('/api/logs'),

  // 设置
  getSettings: () => request<Record<string, string>>('/api/settings'),
  saveSettings: (payload: Record<string, string>) =>
    post<Record<string, string>>('/api/settings', payload),

  // 调试台
  playgroundImage: (body: any) => post<any>('/api/playground/image', body),
  playgroundVideo: (body: any) => post<VideoTask>('/api/playground/video', body),

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
            handlers.onMeta?.({ model: chunk.model, provider: chunk.provider })
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
