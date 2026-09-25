// 临时验证脚本：检查上下文自动获取、容量校验与 auto 路由。验证完会删除。
const RELAY = 'http://127.0.0.1:8910'
const KEY = 'sk-ctx'

async function api(path, body, method = 'POST', key) {
  const headers = { 'Content-Type': 'application/json' }
  if (key) headers['Authorization'] = 'Bearer ' + key
  const res = await fetch(RELAY + path, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  })
  let data = null
  try {
    data = await res.json()
  } catch {
    /* ignore */
  }
  return { status: res.status, data }
}

const line = (t) => console.log('\n=== ' + t + ' ===')

async function main() {
  line('1. 探测渠道：自动读取上游模型列表里的上下文长度')
  const probe = await api('/api/providers/test', {
    kind: 'openai',
    base_url: 'http://127.0.0.1:9007',
    api_key: 'sk-mock',
  })
  console.log(JSON.stringify(probe.data?.results?.[0]?.models))

  line('2. 创建渠道')
  const ch = await api('/api/providers', {
    name: 'ctxMock',
    kind: 'openai',
    base_urls: ['http://127.0.0.1:9007'],
    api_keys: ['sk-mock'],
    enabled: true,
  })
  const providerID = ch.data?.id
  console.log('provider id =', providerID)

  line('3. 把探测结果（含上下文长度）导入模型库')
  const models = (probe.data.results[0].models || []).map((m) => ({
    model_id: m.id,
    capability: m.capability,
    context_length: m.context_length,
  }))
  console.log(JSON.stringify((await api('/api/catalog/import', { provider_id: providerID, models })).data))
  const cat = await api('/api/catalog?provider_id=' + providerID, null, 'GET')
  console.log('模型库：', cat.data.map((c) => `${c.model_id}=${c.context_length}`).join(', '))

  line('4. 创建对外模型（不声明窗口，检查是否回退到探测值）')
  const mk = (name, upstream) =>
    api('/api/models', {
      name,
      capability: 'chat',
      inputs: [{ provider_id: providerID, upstream_name: upstream }],
      config: {},
      enabled: true,
    })
  // 故意先建大窗口、后建小窗口：列表按 id 倒序，无容量过滤时会优先挑到小窗口那个。
  await mk('ctx-b', 'mock-large')
  await mk('ctx-a', 'mock-small')
  const list = await api('/api/models', null, 'GET')
  for (const m of list.data) console.log(`${m.name}: 上游=${m.upstream_name} 探测窗口=${m.catalog_context_length || 0}`)

  await api('/api/keys', { name: 'ctx', key: KEY, enabled: true })

  line('5. /v1/models 是否对外暴露上下文长度')
  const vm = await api('/v1/models', null, 'GET', KEY)
  console.log(JSON.stringify(vm.data.data.map((x) => ({ id: x.id, context_length: x.context_length }))))

  line('6. 短请求（应正常放行）')
  const short = await api('/v1/chat/completions', {
    model: 'ctx-a',
    messages: [{ role: 'user', content: '你好' }],
  }, 'POST', KEY)
  console.log(short.status, JSON.stringify(short.data).slice(0, 110))

  line('7. 超长请求（默认 reject，应给出可读错误而不是上游报错）')
  const huge = '上下文压力测试内容。'.repeat(1500)
  const over = await api('/v1/chat/completions', {
    model: 'ctx-a',
    messages: [{ role: 'user', content: huge }],
  }, 'POST', KEY)
  console.log(over.status, JSON.stringify(over.data))

  line('8. 切换为自动裁剪策略，多轮历史应被裁掉后成功')
  await api('/api/settings', { context_policy: 'truncate' })
  const msgs = [{ role: 'system', content: '你是助手' }]
  for (let i = 0; i < 30; i++) {
    msgs.push({ role: 'user', content: `第 ${i} 段历史，` + '填充内容。'.repeat(60) })
    msgs.push({ role: 'assistant', content: `收到第 ${i} 段。` })
  }
  msgs.push({ role: 'user', content: '总结一下' })
  const trim = await api('/v1/chat/completions', { model: 'ctx-a', messages: msgs }, 'POST', KEY)
  console.log('调用结果：', trim.status, JSON.stringify(trim.data).slice(0, 120))
  const logs = await api('/api/logs?limit=1', null, 'GET')
  console.log('日志预览：', JSON.stringify(logs.data[0]?.response_preview))

  line('9. auto 路由：大请求应跳过装不下的小窗口模型')
  await api('/api/settings', { context_policy: 'off' })
  const auto = await api('/v1/chat/completions', {
    model: 'auto',
    messages: [{ role: 'user', content: huge }],
  }, 'POST', KEY)
  console.log('调用结果：', auto.status)
  const logs2 = await api('/api/logs?limit=1', null, 'GET')
  console.log('命中的模型：', logs2.data[0]?.model, '（期望 ctx-b）')
}

main().catch((e) => console.error('FAILED', e))
