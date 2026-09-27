// 通用格式化与轻量 Markdown 渲染工具。

export function fmtTime(ts: string): string {
  if (!ts) return '—'
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return ts
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(
    d.getMinutes(),
  )}:${pad(d.getSeconds())}`
}

export function fmtDay(ts: string): string {
  if (!ts) return '—'
  return ts.slice(5, 10)
}

export function relative(ts: string): string {
  if (!ts) return '—'
  const t = new Date(ts).getTime()
  if (Number.isNaN(t)) return ts
  const diff = Date.now() - t
  if (diff < 0) return fmtTime(ts)
  const sec = Math.floor(diff / 1000)
  if (sec < 60) return '刚刚'
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟前`
  if (sec < 86400) return `${Math.floor(sec / 3600)} 小时前`
  if (sec < 86400 * 30) return `${Math.floor(sec / 86400)} 天前`
  return fmtTime(ts)
}

export function fmtNumber(n: number | undefined | null): string {
  if (n === undefined || n === null) return '0'
  if (Math.abs(n) >= 1e9) return (n / 1e9).toFixed(2) + 'B'
  if (Math.abs(n) >= 1e6) return (n / 1e6).toFixed(2) + 'M'
  if (Math.abs(n) >= 1e4) return (n / 1e3).toFixed(1) + 'K'
  return String(n)
}

export function fmtTokens(n: number | undefined | null): string {
  return fmtNumber(n || 0)
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    try {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
      return true
    } catch {
      return false
    }
  }
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

// renderMarkdown 是一个刻意保持简单的渲染器：只处理代码块、标题、列表、
// 粗体、行内代码与链接，输出前统一转义，避免 XSS。
export function renderMarkdown(src: string): string {
  const blocks: string[] = []
  let text = src.replace(/\r\n/g, '\n')

  text = text.replace(/```([\w-]*)\n?([\s\S]*?)```/g, (_m, lang, code) => {
    const idx = blocks.length
    blocks.push(
      `<pre class="md-code"><code data-lang="${escapeHtml(lang || '')}">${escapeHtml(
        code.replace(/\n$/, ''),
      )}</code></pre>`,
    )
    return `\u0000BLOCK${idx}\u0000`
  })

  const lines = text.split('\n')
  const out: string[] = []
  let inList = false
  let paragraph: string[] = []

  const flushParagraph = () => {
    if (paragraph.length) {
      out.push(`<p>${paragraph.map(inline).join('<br/>')}</p>`)
      paragraph = []
    }
  }
  const closeList = () => {
    if (inList) {
      out.push('</ul>')
      inList = false
    }
  }

  for (const raw of lines) {
    const line = raw
    if (/^\u0000BLOCK\d+\u0000$/.test(line.trim())) {
      flushParagraph()
      closeList()
      out.push(line.trim())
      continue
    }
    const heading = line.match(/^(#{1,4})\s+(.*)$/)
    if (heading) {
      flushParagraph()
      closeList()
      const level = heading[1].length
      out.push(`<h${level + 2}>${inline(heading[2])}</h${level + 2}>`)
      continue
    }
    const item = line.match(/^\s*[-*+]\s+(.*)$/)
    if (item) {
      flushParagraph()
      if (!inList) {
        out.push('<ul>')
        inList = true
      }
      out.push(`<li>${inline(item[1])}</li>`)
      continue
    }
    if (line.trim() === '') {
      flushParagraph()
      closeList()
      continue
    }
    closeList()
    paragraph.push(line)
  }
  flushParagraph()
  closeList()

  let html = out.join('\n')
  html = html.replace(/\u0000BLOCK(\d+)\u0000/g, (_m, i) => blocks[Number(i)] || '')
  return html
}

function inline(s: string): string {
  let out = escapeHtml(s)
  out = out.replace(/`([^`]+)`/g, '<code>$1</code>')
  out = out.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
  out = out.replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noreferrer">$1</a>')
  return out
}

const CAP_LABELS: Record<string, string> = {
  chat: '文本对话',
  image: '图片生成',
  video: '视频生成',
  embedding: '文本向量',
  speech: '语音合成',
  transcription: '语音识别',
  connector: '连接器',
}

export function capLabel(cap: string): string {
  return CAP_LABELS[cap] || cap || '—'
}

const STRATEGY_LABELS: Record<string, string> = {
  round_robin: '轮询',
  random: '随机',
  failover: '主备',
}

export function strategyLabel(s: string): string {
  return STRATEGY_LABELS[s] || s || '轮询'
}

const KIND_LABELS: Record<string, string> = {
  openai: 'OpenAI 兼容',
  zhipu: '智谱 GLM',
  gemini: 'Google Gemini',
  anthropic: 'Anthropic Claude',
  template: '自定义模板',
}

export function kindLabel(kind: string): string {
  return KIND_LABELS[kind] || kind || '—'
}

export function prettyJSON(v: any): string {
  if (v === undefined || v === null) return ''
  if (typeof v === 'string') {
    const trimmed = v.trim()
    if (!trimmed) return ''
    try {
      return JSON.stringify(JSON.parse(trimmed), null, 2)
    } catch {
      return v
    }
  }
  try {
    return JSON.stringify(v, null, 2)
  } catch {
    return String(v)
  }
}
