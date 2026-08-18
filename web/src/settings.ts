// Display preferences that are not the query: which timezone timestamps are
// shown in.
//
// Purely a display concern here. Every request to the server carries instants
// (unix milliseconds), and VictoriaLogs stores and compares in UTC — so the
// zone changes what a reader sees, never what is asked for or what comes back.
import { computed, ref } from 'vue'

const TZ_KEY = 'vlui.timezone'

// The machine's own zone, which is what an empty preference means.
export const browserTZ: string = safeBrowserTZ()

function safeBrowserTZ(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

// Empty means "follow the browser", stored as an empty string rather than as
// the resolved zone so somebody who travels keeps following their machine.
export const timezone = ref<string>(stored())

function stored(): string {
  try {
    return window.localStorage.getItem(TZ_KEY) ?? ''
  } catch {
    return ''
  }
}

export const activeTZ = computed(() => timezone.value || browserTZ)

export function setTimezone(tz: string) {
  timezone.value = tz
  try {
    if (tz) window.localStorage.setItem(TZ_KEY, tz)
    else window.localStorage.removeItem(TZ_KEY)
  } catch {
    // The choice still applies; it just will not survive a reload.
  }
}

// The zones the picker offers. Straight from the browser: it is the only thing
// that has to be able to format in them, since the zone never leaves the page.
export const timezones: string[] = supportedZones()

function supportedZones(): string[] {
  try {
    const supported = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] })
      .supportedValuesOf
    if (supported) return supported('timeZone')
  } catch {
    // Fall through to the short list.
  }
  // Safari before 15.4 and anything older has no supportedValuesOf. A handful
  // of zones beats an empty picker.
  return ['UTC', 'Europe/London', 'Europe/Berlin', 'Europe/Kyiv', 'Europe/Moscow',
    'America/New_York', 'America/Chicago', 'America/Los_Angeles', 'Asia/Dubai',
    'Asia/Kolkata', 'Asia/Shanghai', 'Asia/Tokyo', 'Australia/Sydney']
}

function tzName(tz: string, style: 'short' | 'long'): string {
  try {
    const parts = new Intl.DateTimeFormat('en-US', { timeZone: tz, timeZoneName: style }).formatToParts()
    return parts.find((p) => p.type === 'timeZoneName')?.value ?? ''
  } catch {
    return ''
  }
}

// What the rail button shows. Intl's short style carries real abbreviations
// only for a few zones (EDT, PDT); everywhere else it answers "GMT+3", which is
// still the most useful thing a 48px button can say. Where even that is
// missing, the initials of the long name ("Central European Summer Time" ->
// "CEST") beat a truncated zone id.
export const tzAbbrev = computed(() => {
  const tz = activeTZ.value
  const short = tzName(tz, 'short')
  if (short && !/^GMT[+-]?\d*$/.test(short)) return short

  const long = tzName(tz, 'long')
  if (long) {
    const initials = long
      .split(/\s+/)
      .filter(Boolean)
      .map((w) => w[0]?.toUpperCase() ?? '')
      .join('')
    if (initials.length >= 3) return initials
  }
  return short || tz
})
