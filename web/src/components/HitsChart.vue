<script setup lang="ts">
import { computed, ref } from 'vue'
import { formatStamp } from '../time'
import type { HitsResponse, TimeRange } from '../types'

const props = defineProps<{
  hits: HitsResponse | null
  range: TimeRange | null
  loading: boolean
}>()

const emit = defineEmits<{ zoom: [TimeRange] }>()

interface Bar {
  startMs: number
  endMs: number
  count: number
  x: number
  w: number
  h: number
}

// The chart is drawn in a 1000x100 user-space box and stretched to whatever
// width it is given, so a bucket's position is a fraction of the range rather
// than a pixel count that would have to be recomputed on every resize.
const VIEW_W = 1000
const VIEW_H = 100

const bars = computed<Bar[]>(() => {
  const series = props.hits?.hits ?? []
  const range = props.range
  if (!series.length || !range) return []

  const stepMs = (props.hits?.step_seconds ?? 0) * 1000
  if (stepMs <= 0) return []

  // Several series appear only when hits are grouped by a field; summing them
  // keeps this a histogram of "how many logs", which is what it is for.
  const totals = new Map<number, number>()
  for (const s of series) {
    s.timestamps.forEach((ts, i) => {
      const t = Date.parse(ts)
      if (Number.isNaN(t)) return
      totals.set(t, (totals.get(t) ?? 0) + (s.values[i] ?? 0))
    })
  }

  const span = range.endMs - range.startMs
  if (span <= 0) return []

  const peak = Math.max(...totals.values(), 1)
  const width = Math.max((stepMs / span) * VIEW_W, 1)

  return [...totals.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([t, count]) => ({
      startMs: t,
      endMs: t + stepMs,
      count,
      x: ((t - range.startMs) / span) * VIEW_W,
      w: width,
      // Square root, not linear: log volume is spiky, and a linear scale
      // flattens everything that is not the single busiest minute into nothing.
      h: count > 0 ? Math.max((Math.sqrt(count) / Math.sqrt(peak)) * VIEW_H, 1) : 0,
    }))
})

const total = computed(() => (props.hits?.hits ?? []).reduce((sum, s) => sum + s.total, 0))

/* --- drag to zoom --------------------------------------------------------- */

const svg = ref<SVGSVGElement | null>(null)
const dragFrom = ref<number | null>(null)
const dragTo = ref<number | null>(null)

function fraction(e: MouseEvent): number {
  const el = svg.value
  if (!el) return 0
  const box = el.getBoundingClientRect()
  return Math.min(Math.max((e.clientX - box.left) / box.width, 0), 1)
}

function onDown(e: MouseEvent) {
  if (!props.range) return
  dragFrom.value = fraction(e)
  dragTo.value = dragFrom.value
}

function onMove(e: MouseEvent) {
  if (dragFrom.value === null) return
  dragTo.value = fraction(e)
}

function onUp() {
  const range = props.range
  const from = dragFrom.value
  const to = dragTo.value
  dragFrom.value = null
  dragTo.value = null

  if (!range || from === null || to === null) return
  // A click, not a drag. Zooming to a zero-width window would just empty the
  // table, so treat it as a miss.
  if (Math.abs(to - from) < 0.005) return

  const span = range.endMs - range.startMs
  const lo = Math.min(from, to)
  const hi = Math.max(from, to)
  emit('zoom', {
    startMs: Math.round(range.startMs + lo * span),
    endMs: Math.round(range.startMs + hi * span),
  })
}

const selection = computed(() => {
  if (dragFrom.value === null || dragTo.value === null) return null
  const lo = Math.min(dragFrom.value, dragTo.value)
  const hi = Math.max(dragFrom.value, dragTo.value)
  return { x: lo * VIEW_W, w: (hi - lo) * VIEW_W }
})
</script>

<template>
  <div class="chart" :class="{ loading }">
    <svg
      ref="svg"
      :viewBox="`0 0 ${VIEW_W} ${VIEW_H}`"
      preserveAspectRatio="none"
      @mousedown="onDown"
      @mousemove="onMove"
      @mouseup="onUp"
      @mouseleave="onUp"
    >
      <rect
        v-for="b in bars"
        :key="b.startMs"
        class="bar"
        :x="b.x"
        :y="VIEW_H - b.h"
        :width="b.w"
        :height="b.h"
      >
        <title>{{ formatStamp(b.startMs) }} — {{ b.count.toLocaleString() }}</title>
      </rect>

      <rect v-if="selection" class="selection" :x="selection.x" y="0" :width="selection.w" :height="VIEW_H" />
    </svg>

    <div class="axis mono muted">
      <span>{{ range ? formatStamp(range.startMs) : '' }}</span>
      <span class="total">
        <template v-if="loading">counting…</template>
        <template v-else-if="hits">{{ total.toLocaleString() }} matching logs · drag to zoom</template>
      </span>
      <span>{{ range ? formatStamp(range.endMs) : '' }}</span>
    </div>
  </div>
</template>

<style scoped>
.chart {
  border-bottom: 1px solid var(--border);
  background: var(--bg-sunken);
  padding: 6px 10px 2px;
}

.chart.loading { opacity: 0.6; }

svg {
  display: block;
  width: 100%;
  height: 72px;
  cursor: crosshair;
  user-select: none;
}

.bar { fill: var(--accent); opacity: 0.75; }
.bar:hover { opacity: 1; }

.selection { fill: var(--accent); opacity: 0.18; }

.axis {
  display: flex;
  justify-content: space-between;
  font-size: 11px;
  padding: 2px 0 4px;
}

.total { text-align: center; }
</style>
