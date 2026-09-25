// 临时 mock：返回最小可用的 OpenAI 对话响应，用于验证设置项是否真实生效。验证完会删除。
const http = require('http')

http
  .createServer((req, res) => {
    let body = ''
    req.on('data', (d) => (body += d))
    req.on('end', () => {
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end(
        JSON.stringify({
          id: 'chatcmpl-mock',
          object: 'chat.completion',
          created: 1790000000,
          model: 'mock',
          choices: [
            { index: 0, message: { role: 'assistant', content: 'pong' }, finish_reason: 'stop' },
          ],
          usage: { prompt_tokens: 3, completion_tokens: 1, total_tokens: 4 },
        }),
      )
    })
  })
  .listen(9005)
