// 验证「飞书配置向导」：渲染、步骤状态、跳转高亮
import { chromium } from 'file:///C:/Users/linch/.workbuddy/binaries/node/workspace/node_modules/playwright-core/index.mjs'

const exe = 'C:/Users/linch/AppData/Local/ms-playwright/chromium-1244/chrome-win/chrome.exe'
const results = []
const check = (name, cond) => {
  results.push(`${cond ? 'PASS' : 'FAIL'} ${name}`)
  return cond
}

const browser = await chromium.launch({ executablePath: exe, headless: true })
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })

try {
  await page.goto('http://127.0.0.1:8799/#/settings', { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.feishu-wizard', { timeout: 15000 })
  check('向导卡片渲染', true)

  const steps = page.locator('.fw-step')
  const n = await steps.count()
  check(`共 5 个步骤（实际 ${n}）`, n === 5)

  const doneCount = await page.locator('.fw-step.done').count()
  const warnCount = await page.locator('.fw-step.warn').count()
  const optionalCount = await page.locator('.fw-step.optional').count()
  check(`4 步就绪（done=${doneCount}）`, doneCount === 4)
  check(`0 步失败（warn=${warnCount}）`, warnCount === 0)
  check(`1 步可选提醒（optional=${optionalCount}）`, optionalCount === 1)

  // 各步骤名称
  const names = await page.locator('.fw-name').allTextContents()
  check('步骤含「事件接入」', names.some(t => t.includes('事件接入')))
  check('步骤含「告警 / 日报推送」', names.some(t => t.includes('告警')))

  // 长连接描述应为「已建立」
  const descs = await page.locator('.fw-desc').allTextContents()
  check('事件接入显示「长连接已建立」', descs.some(t => t.includes('长连接已建立')))

  // 「去配置」只出现在未完成步骤（可选步骤 1 个）
  const jumps = await page.locator('.fw-jump').count()
  check(`未完成步骤有去配置链接（${jumps} 个）`, jumps === 1)

  // 点击跳转：应滚动到 chat_id 字段并加 flash-target
  await page.locator('.fw-jump').first().click()
  await page.waitForTimeout(400)
  const flashed = await page.evaluate(() => {
    const el = document.getElementById('f-feishu-notify')
    if (!el) return 'missing'
    return el.classList.contains('flash-target') ? 'flashed' : 'no-flash'
  })
  check('跳转后目标字段高亮', flashed === 'flashed')

  // 刷新按钮可用
  await page.locator('.fw-head button').click()
  await page.waitForTimeout(600)
  check('刷新按钮不报错', true)

  // 截图留档
  const wiz = page.locator('.feishu-wizard')
  await wiz.scrollIntoViewIfNeeded()
  await page.waitForTimeout(300)
  await page.screenshot({ path: 'D:/xkx/xkx_appproj/happy_中转站/e2e-wizard.png', clip: null })
} catch (e) {
  results.push('FAIL 异常: ' + e.message)
} finally {
  await browser.close()
}
console.log(results.join('\n'))
