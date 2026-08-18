// Theme: light, dark, or follow the OS.
//
// The chosen mode is what is persisted; the *resolved* theme (only ever light
// or dark) is what the stylesheet keys off, so the CSS never has to reason
// about "auto".
import { computed, ref } from 'vue'

export type Mode = 'light' | 'dark' | 'auto'
export type Theme = 'light' | 'dark'

const KEY = 'vlui.theme'

function stored(): Mode {
  try {
    const v = window.localStorage.getItem(KEY)
    return v === 'light' || v === 'dark' || v === 'auto' ? v : 'auto'
  } catch {
    // Storage is unavailable in a private window with cookies blocked, which is
    // a reason to fall back to the OS, not to fail to start.
    return 'auto'
  }
}

export const mode = ref<Mode>(stored())

const mql = window.matchMedia('(prefers-color-scheme: dark)')
const systemDark = ref(mql.matches)

export const resolved = computed<Theme>(() =>
  mode.value === 'auto' ? (systemDark.value ? 'dark' : 'light') : mode.value,
)

export function setMode(m: Mode) {
  mode.value = m
  try {
    window.localStorage.setItem(KEY, m)
  } catch {
    // As above: the theme still changes, it just will not be remembered.
  }
  apply()
}

// The attribute the stylesheet keys off, plus color-scheme so the browser's own
// widgets — the datetime-local picker, the number spinner, the scrollbars —
// match the page rather than staying stubbornly light on a dark background.
export function apply() {
  document.documentElement.dataset.theme = resolved.value
  document.documentElement.style.colorScheme = resolved.value
}

// Called once at startup, before the app mounts, so the first paint is already
// in the right theme.
export function initTheme() {
  apply()
  mql.addEventListener('change', (e) => {
    systemDark.value = e.matches
    apply() // only changes anything while the mode is "auto"
  })
}
