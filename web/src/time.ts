import { activeTZ } from './settings'
import type { TimeRange } from './types'

// A relative range is stored as a duration rather than as two timestamps, so
// "last 15 minutes" still means the last 15 minutes when it is re-run an hour
// later. It is resolved to instants at the moment a query is sent.
export interface RelativeRange {
  kind: 'relative'
  seconds: number
}

export interface AbsoluteRange {
  kind: 'absolute'
  startMs: number
  endMs: number
}

export type RangeSelection = RelativeRange | AbsoluteRange

export const QUICK_RANGES: { label: string; seconds: number }[] = [
  { label: '5m', seconds: 5 * 60 },
  { label: '15m', seconds: 15 * 60 },
  { label: '1h', seconds: 60 * 60 },
  { label: '3h', seconds: 3 * 60 * 60 },
  { label: '12h', seconds: 12 * 60 * 60 },
  { label: '24h', seconds: 24 * 60 * 60 },
  { label: '7d', seconds: 7 * 24 * 60 * 60 },
]

export function resolveRange(sel: RangeSelection): TimeRange {
  if (sel.kind === 'absolute') {
    return { startMs: sel.startMs, endMs: sel.endMs }
  }
  const end = Date.now()
  return { startMs: end - sel.seconds * 1000, endMs: end }
}

export function describeRange(sel: RangeSelection): string {
  if (sel.kind === 'relative') return `last ${formatDuration(sel.seconds)}`
  return `${formatStamp(sel.startMs)} → ${formatStamp(sel.endMs)}`
}

export function formatDuration(seconds: number): string {
  if (seconds < 60) return `${Math.round(seconds)}s`
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`
  if (seconds < 86400) return `${round1(seconds / 3600)}h`
  return `${round1(seconds / 86400)}d`
}

function round1(n: number): string {
  return (Math.round(n * 10) / 10).toString()
}

// Timestamps are shown in the selected timezone, with milliseconds: ordering
// two lines a few milliseconds apart is a routine thing to need here.
//
// activeTZ is read here rather than passed in, so every caller that formats
// inside a template or a computed re-renders by itself when the zone changes.
export function formatStamp(ms: number, millis = true): string {
  if (!Number.isFinite(ms)) return ''
  const d = new Date(ms)
  if (Number.isNaN(d.getTime())) return ''

  const p = partsIn(activeTZ.value, d)
  if (!p) return ''

  const stamp = `${p.year}-${p.month}-${p.day} ${p.hour}:${p.minute}:${p.second}`
  if (!millis) return stamp
  // Milliseconds are the same in every zone, so they come straight off the
  // instant; Intl would not give them anyway.
  return `${stamp}.${String(d.getMilliseconds()).padStart(3, '0')}`
}

interface ZonedParts {
  year: string
  month: string
  day: string
  hour: string
  minute: string
  second: string
}

function partsIn(tz: string, d: Date): ZonedParts | null {
  try {
    const parts = new Intl.DateTimeFormat('en-GB', {
      timeZone: tz,
      hourCycle: 'h23', // not hour12:false, which yields "24" for midnight in some engines
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    }).formatToParts(d)

    const at = (type: string) => parts.find((x) => x.type === type)?.value ?? ''
    return {
      year: at('year'),
      month: at('month'),
      day: at('day'),
      hour: at('hour'),
      minute: at('minute'),
      second: at('second'),
    }
  } catch {
    return null
  }
}

// How far the zone is from UTC at that instant, in milliseconds. Derived by
// formatting the instant in the zone and reading those wall-clock digits back
// as if they were UTC — the difference is the offset, DST included, with no
// table of rules to keep up to date.
export function zoneOffsetMs(tz: string, ms: number): number {
  const p = partsIn(tz, new Date(ms))
  if (!p) return 0
  const asUTC = Date.UTC(
    Number(p.year), Number(p.month) - 1, Number(p.day),
    Number(p.hour), Number(p.minute), Number(p.second),
  )
  // Sub-second parts are dropped by the format, so put them back before
  // comparing, or every offset would be out by up to 999ms.
  return asUTC - (ms - (((ms % 1000) + 1000) % 1000))
}

// _time arrives as RFC3339 with nanoseconds, which Date cannot parse past
// milliseconds. Truncating is fine — nothing here needs more — but it has to be
// done deliberately rather than by letting Date return NaN.
export function parseLogTime(value: string | undefined): number {
  if (!value) return NaN
  const ms = Date.parse(value)
  if (!Number.isNaN(ms)) return ms
  const trimmed = value.replace(/(\.\d{3})\d+/, '$1')
  return Date.parse(trimmed)
}

// An ISO-8601 instant: a date, a time, and — the part that matters — a zone,
// either "Z" or an explicit offset.
//
// The zone designator is what makes this safe to reformat. A value like
// "2026-08-19T07:44:44Z" names a moment in time, so showing it in the reader's
// selected zone is a change of presentation. A bare "2026-08-19 10:00:00" does
// not: it is whatever clock the producer was on, nobody here knows which, and
// "converting" it would invent an offset and quietly report the wrong time.
// Those are left exactly as they arrived.
const INSTANT =
  /^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})$/

// formatIfInstant renders a log field in the selected timezone when — and only
// when — the value is unambiguously an instant. Anything else comes back null,
// and the caller shows the raw string.
//
// This exists because _time is not the only timestamp in a log line. Producers
// carry their own: a `timestamp` field, an upstream's `time`, a tag copied off
// a SIP message. Those are the fields somebody is comparing against _time when
// they change the timezone, and leaving them in the producer's zone makes the
// selector look broken.
export function formatIfInstant(raw: string): string | null {
  if (!INSTANT.test(raw)) return null
  const ms = parseLogTime(raw)
  if (Number.isNaN(ms)) return null
  // Milliseconds only if the source had them. Rendering "10:47:01.000" for a
  // value written as "10:47:01Z" would claim the event landed on the second,
  // which the producer never said.
  return formatStamp(ms, raw.includes('.'))
}

// <input type="datetime-local"> has no concept of a zone: it shows and returns
// bare wall-clock digits. These two put the selected zone's wall clock in the
// box and read it back out, so picking "09:00" means 09:00 where the operator
// says they are, not where their laptop happens to be.
export function toPickerValue(ms: number): string {
  const p = partsIn(activeTZ.value, new Date(ms))
  if (!p) return ''
  return `${p.year}-${p.month}-${p.day}T${p.hour}:${p.minute}:${p.second}`
}

export function fromPickerValue(value: string): number {
  // Parsed as UTC first (the "Z"), then moved by the zone's offset. Applied
  // twice because the offset depends on the instant, and the first guess can
  // land on the wrong side of a DST change — the second pass is computed from
  // an instant already within an hour of the answer.
  const asUTC = Date.parse(value + 'Z')
  if (Number.isNaN(asUTC)) return NaN

  const tz = activeTZ.value
  let guess = asUTC - zoneOffsetMs(tz, asUTC)
  guess = asUTC - zoneOffsetMs(tz, guess)
  return guess
}
