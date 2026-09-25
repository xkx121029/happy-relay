// 临时 mock：模型列表里返回带 context_length 的模型，用于验证上下文自动获取与容量规划。验证完会删除。
const http = require('http')

http
  .createServer((req, res) => {
    const u = new URL(req.url, 'http://127.0.0.1:9007')
    const json = (code, obj) => {
      res.writeHead(code, { 'Content-Type': 'application/json' })
      res.end(JSON.stringify(obj))
    }

    if (req.method === 'GET' && u.pathname === '/v1/models') {
      return json(200, {
        object: 'list',
        data: [
          // 小窗口模型：用来触发上下文超限
          { id: 'mock-small', object: 'model', context_length: 8192 },
          // 大窗口模型：auto 路由在大请求下应优先选中它
          { id: 'mock-large', object: 'model', context_length: 200000 },
        ],
      })
    }

    if (req.method === 'POST' && u.pathname === '/v1/chat/completions') {
      let body = ''
      req.on('data', (d) => (body += d))
      req.on('end', () => {
        json(200, {
          id: 'chatcmpl-mock',
          object: 'chat.completion',
          created: 1790000000,
          model: 'mock',
          choices: [
            { index: 0, message: { role: 'assistant', content: 'ok' }, finish_reason: 'stop' },
          ],
          usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
        })
      })
      return
    }

    json(404, { error: { message: 'not found: ' + u.pathname } })
  })
  .listen(9007)
