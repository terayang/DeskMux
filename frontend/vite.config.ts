import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'

/**
 * Dev-only CSP relaxation: @vitejs/plugin-react injects its refresh preamble
 * as an inline <script>, which the strict script-src 'self' in index.html
 * blocks (the page would stay blank under both `vite dev` and `wails dev`).
 * Production keeps the strict CSP — every script there is an external asset.
 */
function devCsp(): Plugin {
  return {
    name: 'deskmux-dev-csp',
    apply: 'serve',
    transformIndexHtml(html) {
      return html.replace("script-src 'self';", "script-src 'self' 'unsafe-inline';")
    }
  }
}

// Wails embeds frontend/dist into the binary (see //go:embed in main.go) and
// proxies this dev server in `wails dev` (wails.json sets frontend:dir here).
export default defineConfig({
  plugins: [react(), devCsp()],
  build: {
    outDir: 'dist'
  }
})
