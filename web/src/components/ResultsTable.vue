<script setup lang="ts">
import { computed, ref } from 'vue'
import { formatIfInstant, formatStamp, parseLogTime } from '../time'
import { valueMatcher } from '../valuestyle'
import type { LogRow, StyleRule, ValueStyle } from '../types'

const props = defineProps<{
  rows: LogRow[]
  columns: string[]
  // Header labels by field name. A field name is often far wider than its
  // values — "payload.response.status_code" over "200" — and the header is what
  // sizes the column, so a deployment can name it something shorter.
  labels: Record<string, string>
  // How each column's values are drawn, by field name, for the columns a
  // deployment has said something about. Everything else is plain text.
  styles: Record<string, ValueStyle>
  selectedIndex: number
  running: boolean
}>()

const emit = defineEmits<{
  select: [number]
  'remove-column': [string]
}>()

/* Virtualised: a result set is up to max_rows lines, and a DOM node per line
   makes scrolling stutter long before that. Only the visible window plus a
   little overscan is rendered, which is what keeps every row the same height —
   an expanded row would break the arithmetic, so details open in the drawer
   beside the table instead of inline. */

const ROW_HEIGHT = 24
const OVERSCAN = 12

const scroller = ref<HTMLElement | null>(null)
const scrollTop = ref(0)
const viewportHeight = ref(600)

function onScroll() {
  scrollTop.value = scroller.value?.scrollTop ?? 0
}

function measure() {
  viewportHeight.value = scroller.value?.clientHeight ?? 600
}

const start = computed(() => Math.max(0, Math.floor(scrollTop.value / ROW_HEIGHT) - OVERSCAN))
const count = computed(() => Math.ceil(viewportHeight.value / ROW_HEIGHT) + OVERSCAN * 2)
const visible = computed(() => props.rows.slice(start.value, start.value + count.value))

/* Column widths, measured from the data rather than divided out of the window.
 *
 * The rows are separate grids sharing one template — that is what keeps a
 * virtualised table aligned — so a column cannot size itself to its own
 * content the way a real <table> would. It has to be computed.
 *
 * The previous template handed every extra column `minmax(90px, 1fr)`, which
 * fails in both directions: in a wide window the columns stretch to fill and
 * the row never overflows, so there is no horizontal scrollbar however long the
 * values are; in a narrow one they collapse to 90px and ellipsize a timestamp
 * that needs 149. Either way the reader cannot see the value, and scrolling
 * does not help because the text is cut off INSIDE the column.
 *
 * The cell font is monospace, so a character count converts exactly to pixels
 * once one character has been measured. */
// A floor, not a target. Low enough that a three-letter field — "pop" holding
// "fra" — is a three-letter column: it needs 22px of text and CELL_PADDING
// around it, and anything wider is space taken from _msg for nothing. High
// enough that a one-character column still reads as a column.
const MIN_COLUMN = 32
const MAX_COLUMN = 400
// _msg is the log line and deserves more room, but not an unbounded amount: one
// stack trace should not push every other column off a 4000px scroll.
const MAX_MSG = 720

/* The width a cell needs on top of its text: .cell's padding either side, plus
 * a little air so a rounded-down character does not ellipsize a value that
 * fits.
 *
 * It is also, exactly, the whitespace between the last character of one column
 * and the first character of the next — the padding on the right of one cell
 * and the left of the next add up to it — so it is the only number that decides
 * how far apart two values sit when both columns are sized to their content. At
 * 8px either side that was 18px, near three characters of nothing at this font
 * size, on every row and between every pair of columns. 4px either side reads
 * as a column boundary just as clearly and gives back 8px per column.
 *
 * MUST agree with .cell and .hcell in the stylesheet below: the widths here are
 * computed from it, and a cell whose real padding is wider than this ellipsizes
 * text that was measured to fit. */
const CELL_PADDING = 10

// A tagged value is drawn inside a pill, which is wider than the text by its
// own padding either side. Only the columns that carry tags pay for it.
//
// MUST agree with .tag in the stylesheet below, for the same reason
// CELL_PADDING must agree with .cell: these widths are computed, not measured,
// and a pill wider than the arithmetic says would ellipsize a value that fits.
const TAG_PADDING = 10

// Rows are sampled rather than scanned: 5000 rows times a dozen columns on
// every streamed batch would be real work, and the widest value in the first
// few hundred is what the reader is looking at anyway.
const WIDTH_SAMPLE = 300

const charWidth = ref(7.2) // replaced by a real measurement on mount

// Measured by laying out a real cell rather than through canvas font parsing:
// the cells are monospace 12px while the table around them is the sans body
// font, and measuring the wrong one is how every column ends up subtly wrong.
function measureCharWidth(scroller: HTMLElement): number {
  const probe = document.createElement('div')
  probe.className = 'cell mono'
  probe.style.cssText = 'position:absolute;visibility:hidden;white-space:pre;padding:0;width:auto'
  probe.textContent = '0'.repeat(100)

  scroller.appendChild(probe)
  const w = probe.getBoundingClientRect().width / 100
  probe.remove()

  return w > 0 ? w : 7.2
}

const columnWidths = computed<number[]>(() => {
  const sample = props.rows.length > WIDTH_SAMPLE ? props.rows.slice(0, WIDTH_SAMPLE) : props.rows

  return props.columns.map((column) => {
    // The widest value in the sample, in full. Not a percentile of it: a column
    // that ellipsizes an address or a path to save width has taken away the one
    // thing the reader put it on screen for. Whatever room the values need is
    // what they get; the gap between two columns is closed by the padding
    // around them, not by cutting into them.
    //
    let widest = 0
    for (const row of sample) {
      // A row with more lines carries a "+N" after its first one, and the
      // column has to hold both or the badge is what gets ellipsized.
      const extra = extraLines(row, column)
      const len = cell(row, column).length + (extra ? String(extra).length + 3 : 0)
      if (len > widest) widest = len
    }
    // Only a pill is wider than its text; colouring the text costs nothing.
    const content =
      widest * charWidth.value + CELL_PADDING + (props.styles[column] && pill(column) ? TAG_PADDING : 0)

    // The header is its own constraint. Same font as the cells — .mono applies
    // to the header cell too, and wins over the smaller size .head inherits —
    // so it measures with the same character width.
    //
    // No allowance for the remove button. It is invisible until the header is
    // hovered, and reserving 28px in every column for a control nobody is
    // looking at cost more width than the values did — on a short column it was
    // most of the column. It overlays the name on hover instead.
    const header = headerLabel(column).length * charWidth.value + CELL_PADDING

    const max = column === '_msg' ? MAX_MSG : MAX_COLUMN
    return Math.round(Math.min(Math.max(content, header, MIN_COLUMN), max))
  })
})

/* Every column is exactly as wide as its content needs — which is what makes
 * the row overflow, and the horizontal scrollbar appear, when they do not all
 * fit — with one exception and one addition.
 *
 * The exception is _msg where _msg is last: the log line is what the reader
 * came for, so it takes any width the window has spare. Only where it is last.
 * A stretched column in the MIDDLE of a row does not absorb the slack, it moves
 * it inside the table, and puts a hand's width of nothing between that column's
 * values and the next one's.
 *
 * The addition, for every other arrangement, is an empty track on the end that
 * soaks up the same slack. It has no cells in it — grid leaves a track without
 * children empty — so the width goes somewhere harmless instead of stretching
 * whichever column happens to be last, and a table narrower than the window
 * still does not leave a stripe of unpainted grid down its right-hand side. */
const gridTemplate = computed(() => {
  const last = props.columns.length - 1

  const tracks = props.columns.map((c, i) => {
    const w = columnWidths.value[i] ?? MIN_COLUMN
    return c === '_msg' && i === last ? `minmax(${w}px, 1fr)` : `${w}px`
  })

  if (props.columns[last] !== '_msg') tracks.push('1fr')
  return tracks.join(' ')
})

/* One memoised matcher per tagged column, rebuilt only when the rules change.
 *
 * Built here rather than per cell so the cache survives scrolling: the table
 * re-renders its window on every scroll event, and a fresh matcher each time
 * would be a cache that never hits. */
const matchers = computed(() => {
  const out: Record<string, (value: string) => StyleRule | null> = {}
  for (const [field, style] of Object.entries(props.styles)) out[field] = valueMatcher(style.rules)
  return out
})

// A pill or bare colour, for the template to pick a class with.
function pill(column: string): boolean {
  return props.styles[column]?.type !== 'text'
}

function rule(row: LogRow, column: string): StyleRule | null {
  return matchers.value[column]?.(cell(row, column)) ?? null
}

function cell(row: LogRow, column: string): string {
  const raw = row[column]
  if (raw === undefined) return ''
  if (column === '_time') {
    const ms = parseLogTime(raw)
    return Number.isNaN(ms) ? raw : formatStamp(ms)
  }
  // Any other field that carries an instant is shown in the same zone: two
  // timestamps side by side on one row must be on one clock, or comparing them
  // is a trap.
  const shown = formatIfInstant(raw) ?? raw

  // The FIRST line, where a value has more than one — a job's captured stdout,
  // a backtrace. A row is one line tall and every row is the same height, so
  // the rest cannot be drawn here; what is drawn is what the column is measured
  // from, and the count of what was left out is beside it.
  const br = shown.indexOf('\n')
  return br < 0 ? shown : shown.slice(0, br).replace(/\r$/, '')
}

/* How many lines a value has beyond the first, for the "+N" beside it.
 *
 * Trailing blank lines are not lines anybody wants counted: a message that ends
 * in a newline is one line, not two. */
function extraLines(row: LogRow, column: string): number {
  const raw = row[column]
  if (!raw || raw.indexOf('\n') < 0) return 0

  const lines = raw.split('\n')
  while (lines.length && lines[lines.length - 1].trim() === '') lines.pop()
  return Math.max(0, lines.length - 1)
}

/* What each header shows: the configured label, or — failing that — the field
 * name shortened to its last dotted segment. "payload.response.status_code"
 * reads as "status_code".
 *
 * The header is a constraint on the column's width, and a dotted name is
 * routinely several times wider than the values under it: 28 characters of
 * header over three of data leaves the reader looking at 200px of empty grid on
 * every row. The leaf is the part that identifies the field anyway — the prefix
 * says where in a JSON document it came from, which is a question for the row
 * drawer, not for a column heading.
 *
 * Only where the leaf is unambiguous among the columns on screen: with
 * payload.status and status both up, two headers reading "status" would be
 * worse than two long ones. The full name is in the header's tooltip, and
 * untouched in the field panel and the log entry, which is where somebody goes
 * to find out what a column actually is.
 *
 * A label from the config still wins: it can shorten what has no dots in it at
 * all — "location" to "pop" — which no rule here could guess. */
const headerNames = computed<Record<string, string>>(() => {
  const leaves = new Map<string, number>()
  for (const c of props.columns) {
    const leaf = c.slice(c.lastIndexOf('.') + 1)
    leaves.set(leaf, (leaves.get(leaf) ?? 0) + 1)
  }

  const out: Record<string, string> = {}
  for (const c of props.columns) {
    const configured = props.labels[c]
    const leaf = c.slice(c.lastIndexOf('.') + 1)
    out[c] = configured || (leaf && leaves.get(leaf) === 1 ? leaf : c)
  }
  return out
})

function headerLabel(column: string): string {
  return headerNames.value[column] ?? column
}

// What the header says on hover: the real field name, and what it was shortened
// to, so nothing on screen is unfindable.
function headerTitle(column: string): string {
  const shown = headerLabel(column)
  return shown === column ? column : `${column} (shown as ${shown})`
}

// The cell's hover title, which is where the untouched value lives once a cell
// has been reformatted or ellipsized — nothing is hidden, it is one hover away.
//
// A matched rule's description follows on its own line. The colour says a value
// is worth attention; the line under it says why, which is the half a colour
// cannot carry.
function cellTitle(row: LogRow, column: string): string {
  const raw = row[column]
  if (raw === undefined) return ''

  const shown = cell(row, column)
  // A multi-line value is shown whole rather than as "first line (whole
  // thing)", which would print it twice — capped, because a sixty-line tooltip
  // is a screen of text the pointer is holding hostage. The drawer has all of
  // it, and that is what the count is pointing at.
  const title = raw.includes('\n') ? capLines(raw) : shown === raw ? raw : `${shown}  (${raw})`

  const description = rule(row, column)?.description
  return description ? `${title}\n${description}` : title
}

const TOOLTIP_LINES = 12

function capLines(raw: string): string {
  const lines = raw.split('\n')
  if (lines.length <= TOOLTIP_LINES) return raw
  return `${lines.slice(0, TOOLTIP_LINES).join('\n')}\n… ${lines.length - TOOLTIP_LINES} more lines — open the row`
}

// The scroller's height is not known until it is laid out, and it changes with
// the window; both are handled by the same measurement.
const observer = new ResizeObserver(measure)

function mounted(el: Element | null) {
  if (el instanceof HTMLElement) {
    scroller.value = el
    observer.observe(el)
    measure()
    charWidth.value = measureCharWidth(el)
  }
}
</script>

<template>
  <div class="results" :ref="(el) => mounted(el as Element | null)" @scroll.passive="onScroll">
    <div class="head" :style="{ gridTemplateColumns: gridTemplate }">
      <div v-for="c in columns" :key="c" class="hcell mono" :title="headerTitle(c)">
        <span class="hname">{{ headerLabel(c) }}</span>
        <button
          v-if="c !== '_time' && c !== '_msg'"
          type="button"
          class="ghost drop"
          title="Remove this column"
          @click="emit('remove-column', c)"
        >
          ×
        </button>
      </div>
    </div>

    <div class="canvas" :style="{ height: `${rows.length * ROW_HEIGHT}px` }">
      <div class="window" :style="{ transform: `translateY(${start * ROW_HEIGHT}px)` }">
        <div
          v-for="(row, i) in visible"
          :key="start + i"
          class="row"
          :class="{ selected: start + i === selectedIndex }"
          :style="{ gridTemplateColumns: gridTemplate }"
          @click="emit('select', start + i)"
        >
          <div v-for="c in columns" :key="c" class="cell mono" :title="cellTitle(row, c)">
            <!-- Coloured only where a rule matched: an unmatched value is
                 drawn as itself, not as a colourless pill. That way the colours
                 in a column mean something by their presence as well as their
                 hue. -->
            <span
              v-if="rule(row, c)"
              :class="[pill(c) ? 'tag' : 'tint', `v-${rule(row, c)?.color}`]"
              >{{ cell(row, c) }}</span
            >
            <template v-else>{{ cell(row, c) }}</template>
            <!-- What the row could not show. Every row is the same height, so a
                 value with more lines in it loses them here rather than growing
                 the row; the count says so, and the row drawer has them. -->
            <span
              v-if="extraLines(row, c)"
              class="more"
              :title="`${extraLines(row, c) + 1} lines — open the row to see them all`"
              >+{{ extraLines(row, c) }}</span
            >
          </div>
        </div>
      </div>
    </div>

    <p v-if="!rows.length && !running" class="empty muted">No logs matched.</p>
    <p v-else-if="!rows.length" class="empty muted">Querying…</p>
  </div>
</template>

<style scoped>
.results {
  flex: 1;
  min-height: 0;
  overflow: auto;
  position: relative;
}

.head, .row {
  display: grid;
  /* stretch, not center: a grid item sized to its own content is what let a
     multi-line value grow taller than its row and paint over the rows above and
     below it. Stretched, every cell is exactly one row high and its own
     overflow:hidden does the clipping. */
  align-items: stretch;
  gap: 0;
}

.head {
  position: sticky;
  top: 0;
  z-index: 5;
  background: var(--bg-sunken);
  border-bottom: 1px solid var(--border-strong);
  font-size: 11px;
  font-weight: 600;
  color: var(--text-dim);
  height: var(--row-height);
  min-width: fit-content;
}

.hcell {
  position: relative;
  display: flex;
  align-items: center;
  padding: 0 4px; /* CELL_PADDING */
  overflow: hidden;
}

.hname { overflow: hidden; text-overflow: ellipsis; }

/* Over the name rather than beside it. Beside it, every column reserved space
   for a button that is hidden until you hover the header — which on a narrow
   column was wider than the values. Its own background so it stays legible
   over whatever it covers. */
.drop {
  position: absolute;
  top: 0;
  right: 1px;
  bottom: 0;
  visibility: hidden;
  padding: 0 4px;
  background: var(--bg-sunken);
}

.hcell:hover .drop { visibility: visible; }

.canvas { position: relative; min-width: fit-content; }
.window { position: absolute; top: 0; left: 0; right: 0; }

.row {
  height: var(--row-height);
  border-bottom: 1px solid color-mix(in srgb, var(--border) 45%, transparent);
  cursor: pointer;
  min-width: fit-content;
}

.row:hover { background: var(--bg-sunken); }
.row.selected { background: var(--accent-soft); }

.cell {
  padding: 0 4px; /* CELL_PADDING */
  white-space: pre;
  overflow: hidden;
  text-overflow: ellipsis;
  line-height: var(--row-height);
}

/* "+65": the lines this row is not showing. Dim and small — it is a footnote
   about the value, not part of it — and it keeps its own width in the column so
   the value beside it is not the thing that gets ellipsized. */
.more {
  margin-left: 6px;
  padding: 0 3px;
  border-radius: 3px;
  font-size: 10px;
  color: var(--text-dim);
  background: var(--hover);
  vertical-align: 1px;
}

.empty { padding: 16px; }
</style>
