<script setup lang="ts">
import { computed, ref } from 'vue'
import { formatIfInstant, formatStamp, parseLogTime } from '../time'
import type { LogRow } from '../types'

const props = defineProps<{
  rows: LogRow[]
  columns: string[]
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

const gridTemplate = computed(() =>
  props.columns
    .map((c) => {
      if (c === '_time') return '188px'
      if (c === '_msg') return 'minmax(320px, 4fr)'
      return 'minmax(90px, 1fr)'
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
  }
}
</script>

<template>
  <div class="results" :ref="(el) => mounted(el as Element | null)" @scroll.passive="onScroll">
    <div class="head" :style="{ gridTemplateColumns: gridTemplate }">
      <div v-for="c in columns" :key="c" class="hcell mono">
        <span class="hname">{{ c }}</span>
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
