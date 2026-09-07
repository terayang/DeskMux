import type { DeskMuxApi } from './index'

declare global {
  interface Window {
    deskmux: DeskMuxApi
  }
}

export {}
