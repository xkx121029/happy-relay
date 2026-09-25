// 临时 mock 上游：支持模型列表、普通补全与 SSE 流式（含一大块文本）。验证完会删除。
const http = require('http')

const MODELS = [
  { id: 'mock-chat', object: 'model' },
  { id: 'flux-image', object: 'model' },
]

const LONG_TEXT =
  '这是用于验证均匀流式输出的一段较长文本，逐字下发时应当平滑而连续，不应出现整段瞬间跳出的情况。'.repeat(6)

http
  .createServer((req, res) => {
    let body = ''
    req.on('data', (d) => (body += d))
    req.on('end', () => {
      if (req.method === 'GET') {
        res.writeHead(200, { 'Content-Type': 'application/json' })
        res.end(JSON.stringify({ object: 'list', data: MODELS }))
        return
      }
      let stream = false
      try {
        stream = !!JSON.parse(body || '{}').stream
      } catch {
        /* ignore */
      }
      if (stream) {
        res.writeHead(200, {
          'Content-Type': 'text/event-stream; charset=utf-8',
          'Cache-Control': 'no-cache',
        })
        res.write('data: ' + JSON.stringify({ choices: [{ delta: { role: 'assistant' } }] }) + '\n\n')
        // 一次性给出一大块正文，服务端应当拆成小片匀速下发。
        res.write('data: ' + JSON.stringify({ choices: [{ delta: { content: LONG_TEXT } }] }) + '\n\n')
        res.write(
          'data: ' + JSON.stringify({ choices: [{ delta: {}, finish_reason: 'stop' }] }) + '\n\n',
        )
        res.write('data: [DONE]\n\n')
        res.end()
        return
      }
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end(
        JSON.stringify({
          id: 'chatcmpl-mock',
          object: 'chat.completion',
          created: 1,
          model: 'mock-chat',
          choices: [{ index: 0, message: { role: 'assistant', content: 'OK' }, finish_reason: 'stop' }],
          usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
        }),
      )
    })
  })
  .listen(9003)
