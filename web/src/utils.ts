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

// renderMarkdown 是一个刻意保持简单的渲染器：代码块、标题、无序/有序列表、
// 表格、引用、分隔线、粗体/斜体/删除线/行内代码与链接，输出前统一转义，避免 XSS。
export function renderMarkdown(src: string): string {
  const blocks: string[] = []
  let text = (src || '').replace(/\r\n/g, '\n')

  text = text.replace(/```([\w+-]*)\n?([\s\S]*?)```/g, (_m, lang, code) => {
    const idx = blocks.length
    blocks.push(
      `<pre class="md-pre"><code data-lang="${escapeHtml(lang || '')}">${escapeHtml(
        code.replace(/\n$/, ''),
      )}</code></pre>`,
    )
    return `\u0000BLOCK${idx}\u0000`
  })

  const lines = text.split('\n')
  const out: string[] = []
  let para: string[] = []
  let listType: '' | 'ul' | 'ol' = ''
  let quote: string[] = []

  const flushPara = () => {
    if (para.length) {
      out.push(`<p>${para.map(inline).join('<br/>')}</p>`)
      para = []
    }
  }
  const closeList = () => {
    if (listType) {
      out.push(`</${listType}>`)
      listType = ''
    }
  }
  const flushQuote = () => {
    if (quote.length) {
      out.push(`<blockquote class="md-quote">${quote.map((q) => `<p>${inline(q)}</p>`).join('')}</blockquote>`)
      quote = []
    }
  }
  const flushAll = () => {
    flushPara()
    closeList()
    flushQuote()
  }

  // 表格分隔行：| --- | :---: | 之类
  const isTableSep = (l: string) => /^\|?[\s:|-]+\|[\s:|-]*$/.test(l.trim()) && l.includes('-')
  const splitRow = (l: string) =>
    l
      .trim()
      .replace(/^\|/, '')
      .replace(/\|$/, '')
      .split('|')
      .map((c) => inline(c.trim()))

  for (let i = 0; i < lines.length; i++) {
    const raw = lines[i]
    const t = raw.trim()

    if (/^\u0000BLOCK\d+\u0000$/.test(t)) {
      flushAll()
      out.push(t)
      continue
    }

    // 表格：本行以 | 开头且下一行是分隔行
    if (t.startsWith('|') && i + 1 < lines.length && isTableSep(lines[i + 1])) {
      flushAll()
      const rows = [t]
      let j = i + 2
      while (j < lines.length && lines[j].trim().startsWith('|')) {
        rows.push(lines[j].trim())
        j++
      }
      i = j - 1
      const head = splitRow(rows[0])
      const body = rows.slice(1).map(splitRow)
      out.push(
        '<table class="md-table"><thead><tr>' +
          head.map((h) => `<th>${h}</th>`).join('') +
          '</tr></thead><tbody>' +
          body.map((r) => '<tr>' + r.map((c) => `<td>${c}</td>`).join('') + '</tr>').join('') +
          '</tbody></table>',
      )
      continue
    }

    const heading = t.match(/^(#{1,4})\s+(.*)$/)
    if (heading) {
      flushAll()
      const level = heading[1].length + 2
      out.push(`<h${level}>${inline(heading[2])}</h${level}>`)
      continue
    }

    if (/^(-{3,}|\*{3,})$/.test(t)) {
      flushAll()
      out.push('<hr class="md-hr"/>')
      continue
    }

    const q = t.match(/^>\s?(.*)$/)
    if (q) {
      flushPara()
      closeList()
      quote.push(q[1])
      continue
    }
    flushQuote()

    const ul = raw.match(/^\s*[-*+]\s+(.*)$/)
    if (ul) {
      flushPara()
      if (listType !== 'ul') {
        closeList()
        out.push('<ul class="md-ul">')
        listType = 'ul'
      }
      out.push(`<li>${inline(ul[1])}</li>`)
      continue
    }

    const ol = raw.match(/^\s*\d+[.)]\s+(.*)$/)
    if (ol) {
      flushPara()
      if (listType !== 'ol') {
        closeList()
        out.push('<ol class="md-ol">')
        listType = 'ol'
      }
      out.push(`<li>${inline(ol[1])}</li>`)
      continue
    }

    if (t === '') {
      flushAll()
      continue
    }

    closeList()
    para.push(raw)
  }
  flushAll()

  let html = out.join('\n')
  html = html.replace(/\u0000BLOCK(\d+)\u0000/g, (_m, i) => blocks[Number(i)] || '')
  return html
}

function inline(s: string): string {
  let out = escapeHtml(s)
  out = out.replace(/`([^`]+)`/g, '<code>$1</code>')
  out = out.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
  out = out.replace(/~~([^~]+)~~/g, '<del>$1</del>')
  out = out.replace(/(^|[^*\w])\*([^*\n]+)\*(?!\w)/g, '$1<em>$2</em>')
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
