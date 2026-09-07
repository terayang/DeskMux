// Captures the demo flow (scan -> connect -> desktop/terminal/files) as PNG
// frames for GIF assembly. Requires: vite dev on :5199 and scripts/mockvnc.
// Usage: node scripts/capture-demo.mjs <vncBridgeWsPort>
process.env.PLAYWRIGHT_BROWSERS_PATH = '0'
import { mkdirSync } from 'node:fs'
const { chromium } = await import('playwright')

const port = process.argv[2]
if (!port) {
  console.error('usage: node scripts/capture-demo.mjs <vncBridgeWsPort>')
  process.exit(1)
}

const dir = '/tmp/demo-frames'
mkdirSync(dir, { recursive: true })

const browser = await chromium.launch()
const page = await browser.newPage({ viewport: { width: 1280, height: 800 } })

let n = 0
let stopped = false
const ticker = (async () => {
  while (!stopped) {
    n += 1
    try {
      await page.screenshot({ path: `${dir}/frame-${String(n).padStart(3, '0')}.png` })
    } catch { /* page closed */ }
    await new Promise((r) => setTimeout(r, 200))
  }
})()

const hold = (ms) => new Promise((r) => setTimeout(r, ms))

await page.goto(`http://localhost:5199/?vncbridge=${port}`)
await hold(1500)
await page.locator('#target-address-input').fill('192.168.50.43')
await hold(500)
await page.getByRole('button', { name: /开始扫描/ }).click()
await page.locator('.ant-card', { hasText: 'VNC' }).first().waitFor({ timeout: 15000 })
await hold(1200)
const boxes = page.locator('.ant-checkbox:not(.ant-checkbox-disabled)')
await boxes.first().waitFor()
await boxes.nth(0).click()
await boxes.nth(1).click()
await hold(400)
await page.locator('.newconn-footer button.ant-btn-primary').click()
await page.locator('#cred-username').fill('silicayang')
await page.locator('#cred-password').fill('demo')
await hold(400)
await page.locator('#connect-submit').click()
// Desktop tab with the live mock screen
await page.waitForSelector('.desktop-viewport canvas', { timeout: 15000 })
await hold(4000)
await page.getByRole('tab', { name: '终端' }).click({ force: true })
await hold(2200)
await page.getByRole('tab', { name: '文件管理' }).click({ force: true })
await hold(2200)
await page.getByRole('tab', { name: '远程桌面' }).click({ force: true })
await hold(2500)

stopped = true
await ticker
await browser.close()
console.log(`frames: ${n}`)
