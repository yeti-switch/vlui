/* Which rule a value matches, if any — the matching half of a value style; the
 * drawing half is the type on the style itself.
 *
 * The RULE rather than just its colour, because a rule carries a description
 * too, and the cell that draws the colour is the cell that puts the description
 * in its tooltip: one lookup, one answer.
 *
 * A module rather than a function inside the table, because the table is not
 * the only place a value appears: the log entry shows every field of a row, and
 * a 500 that is red in one and plain in the other would make the reader check
 * twice. One matcher, one answer, wherever the value is drawn.
 *
 * The rules come from the config by way of /api/config, already validated —
 * exactly one matcher per rule, a colour the stylesheet has, and any range
 * parsed into numbers by the server. So this only has to try them in order and
 * take the first that matches.
 */
import type { StyleRule } from './types'

// Longer than this and a tag is a coloured wall rather than a label: a status
// is short, a message is not, and nothing stops a config from pointing a set at
// the wrong field.
const MAX_TAGGED = 24

export function valueRule(value: string, rules: StyleRule[] | undefined): StyleRule | null {
  if (!rules?.length || !value || value.length > MAX_TAGGED) return null

  for (const rule of rules) {
    if (rule.default) return rule
    if (rule.value?.includes(value)) return rule
    if (rule.prefix && value.startsWith(rule.prefix)) return rule
    if (rule.range) {
      // A log value is a string until proven otherwise. One that is not a
      // number does not match a range — it is not "below the low bound", it is
      // not on that scale at all.
      const n = Number(value)
      if (value.trim() === '' || !Number.isFinite(n)) continue

      // An absent bound is an open one: {lo: 1000} matches everything from a
      // thousand up. The server guarantees at least one of the two.
      const { lo, hi } = rule.range
      if ((lo === undefined || n >= lo) && (hi === undefined || n <= hi)) return rule
    }
  }
  return null
}

/* The same answer, memoised per column.
 *
 * Log values repeat hard — two hundred rows of a status column hold three
 * distinct values — and the table re-renders the visible window on every
 * scroll, so the cache turns a rule walk per cell into a map lookup per cell.
 * One cache per column set of rules, discarded whenever they change.
 */
export function valueMatcher(rules: StyleRule[] | undefined): (value: string) => StyleRule | null {
  const seen = new Map<string, StyleRule | null>()

  return (value: string) => {
    const hit = seen.get(value)
    if (hit !== undefined) return hit

    const rule = valueRule(value, rules)
    seen.set(value, rule)
    return rule
  }
}
