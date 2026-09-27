// 验证悬浮球已删除、Agent 页不受影响
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
  // 概览页：悬浮球应不存在
  await page.goto('http://127.0.0.1:8799/overview', { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.nav-item', { timeout: 15000 })
  await page.waitForTimeout(800)
  check('概览页无悬浮球', (await page.locator('.ai-fab').count()) === 0)
  await page.screenshot({ path: 'D:/xkx/xkx_appproj/happy_中转站/e2e-no-fab.png' })

  // Agent 页正常
  await page.goto('http://127.0.0.1:8799/agent', { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.agent-chat', { timeout: 15000 })
  check('Agent 页正常渲染', true)
  check('历史对话还在（localStorage 共享保留）', (await page.locator('.ai-msg').count()) >= 1)
} catch (e) {
  results.push('FAIL 异常: ' + e.message.split('\n')[0])
} finally {
  await browser.close()
}
console.log(results.join('\n'))
