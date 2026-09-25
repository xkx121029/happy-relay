// 临时 mock 上游：返回一份混合能力的模型列表，用于验证能力推断。验证完会删除。
const http = require('http')

const MODELS = [
  { id: 'gpt-4o-mini', object: 'model', owned_by: 'mock' },
  { id: 'text-embedding-3-small', object: 'model', owned_by: 'mock' },
  { id: 'dall-e-3', object: 'model', owned_by: 'mock' },
  { id: 'veo-3', object: 'model', owned_by: 'mock' },
  { id: 'whisper-1', object: 'model', owned_by: 'mock' },
  { id: 'my-custom-model', object: 'model', owned_by: 'mock' },
  { id: 'gemini-2.5-flash', object: 'model', supportedGenerationMethods: ['generateContent'] },
  { id: 'gemini-embedding-001', object: 'model', supportedGenerationMethods: ['embedContent'] },
  { id: 'kling-v2', object: 'model', type: 'video' },
]

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
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end(
        JSON.stringify({
          id: 'chatcmpl-mock',
          object: 'chat.completion',
          created: 1,
          model: 'mock',
          choices: [{ index: 0, message: { role: 'assistant', content: 'OK' }, finish_reason: 'stop' }],
          usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
        }),
      )
    })
  })
  .listen(9002)
