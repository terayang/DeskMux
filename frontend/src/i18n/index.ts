import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import zhCN from './zh-CN.json'
import enUS from './en-US.json'

/** Languages the UI ships with; the default is Simplified Chinese. */
export type AppLanguage = 'zh-CN' | 'en-US'

const STORAGE_KEY = 'deskmux.language'

/** Restores the language chosen last time (localStorage); zh-CN by default. */
function initialLanguage(): AppLanguage {
  try {
    return localStorage.getItem(STORAGE_KEY) === 'en-US' ? 'en-US' : 'zh-CN'
  } catch {
    return 'zh-CN' // storage unavailable (privacy mode etc.): use the default
  }
}

void i18n.use(initReactI18next).init({
  resources: {
    'zh-CN': { translation: zhCN },
    'en-US': { translation: enUS }
  },
  lng: initialLanguage(),
  fallbackLng: 'en-US',
  interpolation: {
    // React already escapes values.
    escapeValue: false
  }
})

/** Switches the UI language and persists the choice for the next launch. */
export function setLanguage(lng: AppLanguage): void {
  try {
    localStorage.setItem(STORAGE_KEY, lng)
  } catch {
    // Storage unavailable: the switch still applies for this run.
  }
  void i18n.changeLanguage(lng)
}

export default i18n
