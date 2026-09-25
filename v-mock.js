// 临时 mock：复刻 Agnes Video 2.5 Flash 的关键约束，用于验证视频中转改造。验证完会删除。
//   1) 查询端点只在根路径 /agnesapi（不在 /v1 下）—— 专门用来验证 base_root
//   2) size 必须是字符串 "720P"
//   3) seconds 必须是字符串
//   4) text 模式不允许出现 images / audios / first_frame / last_frame
//   5) images 最多 5 张、audios 最多 3 段、videos 不支持
//   6) 错误统一返回 {"detail": "..."}
const http = require('http')

const tasks = {}

const server = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://127.0.0.1:9006')
  const json = (code, obj) => {
    res.writeHead(code, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify(obj))
  }

  // ---------- 查询：只挂在根路径，不带 /v1 前缀 ----------
  if (req.method === 'GET' && u.pathname === '/agnesapi') {
    if (u.searchParams.get('model_name') !== 'agnes-video-2.5-flash') {
      return json(400, { detail: 'model_name is required for non-text mode' })
    }
    const id = u.searchParams.get('video_id')
    const t = tasks[id]
    if (!t) return json(404, { detail: 'video not found: ' + id })
    t.polls++
    // 第二次查询就返回完成，便于快速验证全链路
    if (t.polls >= 2) {
      return json(200, {
        id: id,
        object: 'video',
        status: 'completed',
        progress: 100,
        url: 'https://example.com/agnes-video.mp4',
        size: '720P',
        seconds: t.seconds,
        internal_status: 'pending',
        internal_progress: 0,
        error: null,
      })
    }
    return json(200, {
      id: id,
      object: 'video',
      status: 'processing',
      progress: 40,
      url: null,
      internal_status: 'pending',
      internal_progress: 0,
      error: null,
    })
  }

  // ---------- 创建任务：挂在 /v1 下 ----------
  if (req.method === 'POST' && u.pathname === '/v1/videos') {
    let body = ''
    req.on('data', (d) => (body += d))
    req.on('end', () => {
      let raw = {}
      try {
        raw = JSON.parse(body || '{}')
      } catch {
        /* ignore */
      }
      // 复刻官方的校验顺序：size → images → audios → videos
      if (raw.size !== '720P') return json(400, { detail: 'size must be 720P' })
      if (!raw.mode) return json(400, { detail: 'mode is required' })
      if (Array.isArray(raw.images) && raw.images.length > 5) {
        return json(400, { detail: 'images length must not exceed 5' })
      }
      if (Array.isArray(raw.audios) && raw.audios.length > 3) {
        return json(400, { detail: 'audios length must not exceed 3' })
      }
      if (raw.videos) return json(400, { detail: 'videos is not supported' })
      if (raw.seconds !== undefined && typeof raw.seconds !== 'string') {
        return json(400, { detail: 'seconds must be a string, got ' + typeof raw.seconds })
      }
      if (raw.mode === 'text') {
        for (const k of ['images', 'audios', 'first_frame', 'last_frame', 'videos']) {
          if (raw[k] !== undefined) return json(400, { detail: k + ' is not allowed in text mode' })
        }
      }
      if (raw.mode === 'keyframe' && !raw.first_frame && !raw.last_frame) {
        return json(400, { detail: 'first_frame or last_frame is required' })
      }
      const id = 'video_' + Math.random().toString(36).slice(2, 10)
      tasks[id] = { polls: 0, seconds: raw.seconds || '5', mode: raw.mode }
      console.log('create', raw.mode, 'seconds=' + JSON.stringify(raw.seconds), 'fields=' + Object.keys(raw).join(','))
      return json(200, { id: id, video_id: id, object: 'video', status: 'queued', progress: 0 })
    })
    return
  }

  json(404, { detail: 'not found: ' + u.pathname })
})

server.listen(9006)
