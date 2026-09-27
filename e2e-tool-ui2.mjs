// 复验：设置卡片 20 行内可见 secret 打码 + summary 不再带裸 JSON
import { chromium } from 'file:///C:/Users/linch/.workbuddy/binaries/node/workspace/node_modules/playwright-core/index.mjs'
const exe = 'C:/Users/linch/AppData/Local/ms-playwright/chromium-1244/chrome-win64/chrome.exe'
const results = []
const check = (name, cond) => {
  results.push(`${cond ? 'PASS' : 'FAIL'} ${name}`)
  return cond
}
const browser = await chromium.launch({ executablePath: exe, headless: true })
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
try {
  await page.goto('http://127.0.0.1:8799/agent', { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.ac-textarea', { timeout: 15000 })
  await page.locator('.ac-textarea').fill('读取面板设置')
  await page.locator('.ac-send').click()
  await page.waitForFunction(() => document.querySelectorAll('.toolcard').length >= 1, { timeout: 120000 })
  await page.waitForTimeout(1500)
  const card = page.locator('.toolcard').last()
  const txt = await card.innerText()
  check('设置卡片渲染', txt.includes('配置项'))
  check('secret 行可见且打码', /bridge_feishu_app_secret\s*\n?\s*••••••（已配置）/.test(txt))
  check('其余配置明文（balance_mode=prefer_healthy）', txt.includes('prefer_healthy'))
  // summary 不含裸 JSON
  const stepTxt = await page.locator('.ai-step-txt').last().innerText()
  check('summary 无裸 JSON（' + stepTxt.slice(0, 30) + '…）', !/\[\{|"/.test(stepTxt))
  await card.scrollIntoViewIfNeeded()
  await page.waitForTimeout(300)
  await page.screenshot({ path: 'D:/xkx/xkx_appproj/happy_中转站/e2e-tool-ui.png' })
} catch (e) {
  results.push('FAIL 异常: ' + e.message.split('\n')[0])
} finally {
  await browser.close()
}
console.log(results.join('\n'))
