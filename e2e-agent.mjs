// 验证独立 Agent 页：渲染、导航、悬浮球直达、真实一轮工具调用
import { chromium } from 'file:///C:/Users/linch/.workbuddy/binaries/node/workspace/node_modules/playwright-core/index.mjs'

const exe = 'C:/Users/linch/AppData/Local/ms-playwright/chromium-1244/chrome-win64/chrome.exe'
const results = []
const check = (name, cond) => {
  results.push(`${cond ? 'PASS' : 'FAIL'} ${name}`)
  return cond
}

const browser = await chromium.launch({ executablePath: exe, headless: true })
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
page.on('pageerror', (e) => results.push('FAIL pageerror: ' + e.message))

try {
  // 1. 直接访问 /agent
  await page.goto('http://127.0.0.1:8799/agent', { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.agent-chat', { timeout: 15000 })
  check('Agent 页渲染', true)
  check('空态建议 chips', (await page.locator('.ai-chip').count()) >= 4)
  const sub = await page.locator('.agent-sub').innerText()
  check('页头显示主模型（' + sub.replace(/\n/g, ' ').slice(0, 50) + '…）', /主模型|去设置/.test(sub))

  // 2. 侧边栏导航项存在且高亮
  const navAgent = page.locator('.nav-item', { hasText: 'Agent' })
  check('侧边栏有 Agent 项', (await navAgent.count()) === 1)
  check('Agent 项处于激活态', (await navAgent.getAttribute('class')).includes('active'))

  // 3. 发送一轮真实调用（查模型），验证工具循环
  await page.locator('.ac-textarea').fill('列出所有启用的模型，一句话概括')
  await page.locator('.ac-send').click()
  // 等待出现用户气泡
  await page.waitForSelector('.ai-msg.user', { timeout: 5000 })
  // 等最终回复（最多 90s）
  await page.waitForFunction(
    () => {
      const bubbles = document.querySelectorAll('.ai-msg.assistant .ai-bubble')
      const last = bubbles[bubbles.length - 1]
      return last && last.innerText.trim().length > 0 && !last.innerText.includes('思考 / 执行中')
    },
    { timeout: 90000 },
  )
  const reply = await page.locator('.ai-msg.assistant .ai-bubble').last().innerText()
  check('Agent 回复非空（' + reply.replace(/\n/g, ' ').slice(0, 40) + '…）', reply.trim().length > 5)
  const steps = await page.locator('.ai-step').count()
  check('出现工具步骤（' + steps + ' 步）', steps >= 1)
  const foot = await page.locator('.ac-foot').last().innerText()
  check('回复标注模型与耗时', /ms/.test(foot))

  // 截图：带对话的 Agent 页
  await page.screenshot({ path: 'D:/xkx/xkx_appproj/happy_中转站/e2e-agent.png' })

  // 4. 换个页面，悬浮球应直达 Agent 页（共享会话还在）
  await page.goto('http://127.0.0.1:8799/overview', { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.ai-fab', { timeout: 10000 })
  await page.locator('.ai-fab').click()
  await page.waitForURL('**/agent', { timeout: 5000 })
  await page.waitForSelector('.ai-msg.assistant', { timeout: 10000 })
  check('悬浮球直达 Agent 页', page.url().endsWith('/agent'))
  check('对话记录跨页共享', (await page.locator('.ai-msg.user').count()) >= 1)

  // 5. 悬浮球简化后不再有拖拽/面板（只有单个按钮）
  check('悬浮球为单按钮入口（无面板残留）', (await page.locator('.ai-fab').count()) === 1 && (await page.locator('.ai-panel').count()) === 0)
} catch (e) {
  results.push('FAIL 异常: ' + e.message.split('\n')[0])
} finally {
  await browser.close()
}
console.log(results.join('\n'))
