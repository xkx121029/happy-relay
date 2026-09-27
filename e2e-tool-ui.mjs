// 验证工具调用富 UI：list_models 渲染表格卡片、get_settings 渲染键值卡片
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

async function ask(text) {
  await page.locator('.ac-textarea').fill(text)
  await page.locator('.ac-send').click()
  await page.waitForFunction(
    () => {
      const bubbles = document.querySelectorAll('.ai-msg.assistant .ai-bubble')
      const last = bubbles[bubbles.length - 1]
      return last && last.innerText.trim().length > 0 && !last.innerText.includes('思考 / 执行中')
    },
    { timeout: 120000 },
  )
}

try {
  await page.goto('http://127.0.0.1:8799/agent', { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.agent-chat', { timeout: 15000 })

  // 第 1 轮：查模型 → 应出现表格卡片
  await ask('列出所有模型')
  const t1 = page.locator('.toolcard').first()
  await t1.waitFor({ timeout: 5000 })
  check('list_models 渲染表格卡片', true)
  const headers1 = await t1.locator('th').allTextContents()
  check('表头正确（' + headers1.join('/') + '）', headers1.join(',').includes('模型') && headers1.join(',').includes('状态'))
  const rowCount = await t1.locator('tbody tr').count()
  check('渲染数据行（' + rowCount + ' 行）', rowCount >= 3)
  const okCells = await t1.locator('td.ok').count()
  check('状态列有启用/停用着色（' + okCells + ' 个启用）', okCells >= 1)

  // 第 2 轮：读设置 → 键值卡片，敏感值应打码
  await ask('看一下面板当前的配置项')
  await page.waitForFunction(() => document.querySelectorAll('.toolcard').length >= 2, { timeout: 120000 })
  const t2 = page.locator('.toolcard').last()
  const kvText = await t2.innerText()
  check('get_settings 渲染键值卡片', kvText.includes('配置项'))
  check('敏感值打码（••••）', kvText.includes('••••••'))

  // 原始 JSON 详情开关仍在
  const toggles = await page.locator('.ai-step-toggle').count()
  check('保留原始 JSON 详情开关（' + toggles + ' 个）', toggles >= 1)

  await page.locator('.toolcard').first().scrollIntoViewIfNeeded()
  await page.waitForTimeout(300)
  await page.screenshot({ path: 'D:/xkx/xkx_appproj/happy_中转站/e2e-tool-ui.png' })
} catch (e) {
  results.push('FAIL 异常: ' + e.message.split('\n')[0])
} finally {
  await browser.close()
}
console.log(results.join('\n'))
