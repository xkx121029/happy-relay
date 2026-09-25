// 临时 mock：模拟 Agnes Image 2.1 Flash 的几条关键约束，用于验证兼容性改造。验证完会删除。
//   1) 顶层出现 response_format -> 400（Agnes 明确要求放进 extra_body）
//   2) 缺少 size -> 400（Agnes 的 size 必填）
//   3) 参考图只认 extra_body.image
const http = require('http')

http
  .createServer((req, res) => {
    let body = ''
    req.on('data', (d) => (body += d))
    req.on('end', () => {
      let raw = {}
      try {
        raw = JSON.parse(body || '{}')
      } catch {
        /* ignore */
      }
      const fail = (code, message) => {
        res.writeHead(code, { 'Content-Type': 'application/json' })
        res.end(JSON.stringify({ error: { message, type: 'invalid_request_error' } }))
      }
      if (raw.response_format !== undefined) {
        return fail(400, '`response_format` must be placed inside extra_body')
      }
      if (!raw.size) {
        return fail(400, '`size` is required')
      }
      const extra = raw.extra_body && typeof raw.extra_body === 'object' ? raw.extra_body : {}
      const refs = Array.isArray(extra.image) ? extra.image.length : 0
      const wantB64 = extra.response_format === 'b64_json'
      const item = wantB64 ? { b64_json: 'aGVsbG8taW1hZ2U=' } : { url: 'https://example.com/agnes-out.png' }
      item.revised_prompt =
        raw.model + ' | size=' + raw.size + ' | ratio=' + (raw.ratio || '-') + ' | refs=' + refs
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end(JSON.stringify({ created: 1790000000, data: [item] }))
    })
  })
  .listen(9004)
