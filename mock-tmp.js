// 临时 mock 上游：验证「多上游聚合 + 报错自动切换」用，验证完会删除。
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
          created: 1,
          model: 'gpt-test',
          choices: [
            {
              index: 0,
              message: { role: 'assistant', content: 'HELLO_FROM_BACKUP_UPSTREAM' },
              finish_reason: 'stop',
            },
          ],
          usage: { prompt_tokens: 1, completion_tokens: 2, total_tokens: 3 },
        }),
      )
    })
  })
  .listen(9001)
