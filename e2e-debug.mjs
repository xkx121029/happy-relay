import { chromium } from 'file:///C:/Users/linch/.workbuddy/binaries/node/workspace/node_modules/playwright-core/index.mjs'
const exe = 'C:/Users/linch/AppData/Local/ms-playwright/chromium-1244/chrome-win64/chrome.exe'
const browser = await chromium.launch({ executablePath: exe, headless: true })
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
page.on('console', m => { if (m.type() === 'error' || m.type() === 'warning') console.log('[console]', m.type(), m.text().slice(0, 200)) })
page.on('pageerror', e => console.log('[pageerror]', e.message))
page.on('response', async r => {
  if (r.url().includes('feishu/status')) console.log('[resp]', r.status(), r.url())
})
await page.goto('http://127.0.0.1:8799/settings', { waitUntil: 'domcontentloaded' })
await page.waitForTimeout(2500)
// 点击「通知与告警」
const items = page.locator('.rail-item')
console.log('rail items:', await items.count())
const labels = await page.locator('.rail-label').allTextContents()
console.log('labels:', JSON.stringify(labels))
await page.locator('.rail-item', { hasText: '通知与告警' }).click()
await page.waitForTimeout(1500)
const info = await page.evaluate(() => {
  const wiz = document.querySelector('.feishu-wizard')
  return {
    wizardInDom: !!wiz,
    wizardVisible: wiz ? !!(wiz.offsetWidth || wiz.offsetHeight) : false,
    sections: [...document.querySelectorAll('section.card')].map(s => getComputedStyle(s).display),
  }
})
console.log(JSON.stringify(info))
await browser.close()
