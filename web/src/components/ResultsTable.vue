<script setup lang="ts">
import { computed, ref } from 'vue'
import { formatIfInstant, formatStamp, parseLogTime } from '../time'
import type { LogRow } from '../types'

const props = defineProps<{
  rows: LogRow[]
  columns: string[]
  // Header labels by field name. A field name is often far wider than its
  // values — "payload.response.status_code" over "200" — and the header is what
  // sizes the column, so a deployment can name it something shorter.
  labels: Record<string, string>
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
const MIN_COLUMN = 70
const MAX_COLUMN = 400
// _msg is the log line and deserves more room, but not an unbounded amount: one
// stack trace should not push every other column off a 4000px scroll.
const MAX_MSG = 720
const CELL_PADDING = 18 // .cell's 8px either side, plus a little air

// Rows are sampled rather than scanned: 5000 rows times a dozen columns on
// every streamed batch would be real work, and the widest value in the first
// few hundred is what the reader is looking at anyway.
const WIDTH_SAMPLE = 300

// The header's remove button sits beside the name and takes its width even
// while it is invisible, so the name has to be given room for it or a short
// column like "level" shows as "lev…" over values that fit perfectly.
//
// The button, the flex gap beside it, and a couple of pixels of slack: the name
// is set in the sans body font while these widths are computed from the
// monospace cell font, so the estimate has to err wide. It was one pixel short
// before, which is all an ellipsis needs.
const DROP_BUTTON = 28

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
    let widest = 0
    for (const row of sample) {
      const len = cell(row, column).length
      if (len > widest) widest = len
    }
    const content = widest * charWidth.value + CELL_PADDING

    // The header is its own constraint, and a different font: smaller, and not
    // monospace, so its name is measured generously rather than exactly.
    const removable = column !== '_time' && column !== '_msg'
    const header = headerLabel(column).length * charWidth.value + CELL_PADDING + (removable ? DROP_BUTTON : 0)

    const max = column === '_msg' ? MAX_MSG : MAX_COLUMN
    return Math.round(Math.min(Math.max(content, header, MIN_COLUMN), max))
  })
})

const gridTemplate = computed(() =>
  props.columns
    .map((c, i) => {
      const w = columnWidths.value[i] ?? MIN_COLUMN
      // _msg takes any slack going, so a table narrower than the window does
      // not leave a stripe of empty grid down the right-hand side. Every other
      // column is exactly as wide as its content needs, which is what makes the
      // row overflow — and the scrollbar appear — when they do not all fit.
      return c === '_msg' ? `minmax(${w}px, 1fr)` : `${w}px`
    })
    .join(' '),
)

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
  return formatIfInstant(raw) ?? raw
}

// The hover title, which is where the untouched value lives once a cell has
// been reformatted — nothing is hidden, it is one hover away.
// What the header shows, and what it says on hover: the label when there is
// one, with the real field name a mouse away so nothing is unfindable.
function headerLabel(column: string): string {
  return props.labels[column] ?? column
}

function headerTitle(column: string): string {
  const label = props.labels[column]
  return label ? `${column} (shown as ${label})` : column
}

function cellTitle(row: LogRow, column: string): string {
  const raw = row[column]
  if (raw === undefined) return ''
  const shown = cell(row, column)
  return shown === raw ? raw : `${shown}  (${raw})`
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
            {{ cell(row, c) }}
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
  align-items: center;
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
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 0 8px;
  overflow: hidden;
}

.hname { overflow: hidden; text-overflow: ellipsis; }
.drop { visibility: hidden; }
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
  padding: 0 8px;
  white-space: pre;
  overflow: hidden;
  text-overflow: ellipsis;
  line-height: var(--row-height);
}

.empty { padding: 16px; }
</style>
