<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, shallowRef } from 'vue'
import { ApiError, fetchConfig, fetchFacets, fetchHits, openTail, streamQuery } from './api'
import { parseLogTime, resolveRange, type RangeSelection } from './time'
import type { AppConfig, Facet, HitsResponse, LogRow, TimeRange, Tool } from './types'
import Facets from './components/Facets.vue'
import HitsChart from './components/HitsChart.vue'
import QueryBar from './components/QueryBar.vue'
import ResultsTable from './components/ResultsTable.vue'
import RowDetail from './components/RowDetail.vue'
import SideRail from './components/SideRail.vue'

const cfg = ref<AppConfig | null>(null)
const bootError = ref('')

const query = ref('*')

/* The selected tool. Its filter is applied by the SERVER, from this id, on
   every request — the browser only says which tool, never what it filters on.
   That is the whole point: /api/query is reachable with curl by anyone holding
   a session, so a filter composed here would be a suggestion. */
const activeTool = ref('')
const tool = computed<Tool | null>(() => {
  const tools = cfg.value?.tools ?? []
  return tools.find((t) => t.id === activeTool.value) ?? tools[0] ?? null
})
const range = ref<RangeSelection>({ kind: 'relative', seconds: 3600 })
const limit = ref(500)

// shallowRef: the rows are replaced wholesale and never mutated field by field,
// so deep reactivity would cost a proxy per row for nothing.
const rows = shallowRef<LogRow[]>([])
const columns = ref<string[]>(['_time', '_msg'])
const selectedIndex = ref(-1)

const running = ref(false)
const error = ref('')
const elapsedMs = ref(0)
// The window the displayed rows actually came from, which is not the picker's
// value once someone changes it without re-running.
const shownRange = ref<TimeRange | null>(null)

const hits = ref<HitsResponse | null>(null)
const hitsLoading = ref(false)
const facets = ref<Facet[]>([])
const facetsLoading = ref(false)

// The field panel starts closed. It is the third thing on the screen after the
// query line and the table, and every open panel is also an extra facets query
// against VictoriaLogs on every run — so it is opt-in, and the choice is
// remembered for whoever opts in.
const FACETS_KEY = 'vlui.fields-open'
const facetsOpen = ref(readFacetsOpen())

function readFacetsOpen(): boolean {
  try {
    return window.localStorage.getItem(FACETS_KEY) === '1'
  } catch {
    // Storage is unavailable in a private window with cookies blocked, which
    // is a reason to forget the preference, not to fail to start.
    return false
  }
}

function toggleFacets() {
  facetsOpen.value = !facetsOpen.value
  try {
    window.localStorage.setItem(FACETS_KEY, facetsOpen.value ? '1' : '0')
  } catch {
    // As above: the panel still opens, it just will not be remembered.
  }

  // Opened after a query has already run, so the facets it should be showing
  // were never fetched.
  if (facetsOpen.value && !tailing.value && shownRange.value && !facets.value.length) {
    loadFacets(shownRange.value)
  }
}

const tailing = ref(false)

let queryAbort: AbortController | undefined
let hitsAbort: AbortController | undefined
let facetsAbort: AbortController | undefined
let tailSource: EventSource | undefined

const truncated = computed(() => !tailing.value && rows.value.length >= limit.value)
const selectedRow = computed(() => rows.value[selectedIndex.value] ?? null)

/* --- running a query ------------------------------------------------------ */

async function run() {
  if (tailing.value) stopTail()

  queryAbort?.abort()
  queryAbort = new AbortController()
  const signal = queryAbort.signal

  const window_ = resolveRange(range.value)
  shownRange.value = window_
  writeHash()

  running.value = true
  error.value = ''
  rows.value = []
  selectedIndex.value = -1
  const started = performance.now()

  loadSidecars(window_)

  const collected: LogRow[] = []
  try {
    for await (const batch of streamQuery({ query: query.value, range: window_, limit: limit.value, tool: activeTool.value }, signal)) {
      collected.push(...batch)
      // A new array each time, because shallowRef only notices identity.
      rows.value = collected.slice()
      elapsedMs.value = performance.now() - started
    }

    // Sorted once, at the end. VictoriaLogs streams rows as it finds them, and
    // re-sorting on every batch would make the table jump under the reader.
    collected.sort((a, b) => (parseLogTime(b['_time']) || 0) - (parseLogTime(a['_time']) || 0))
    rows.value = collected
  } catch (e) {
    if (!signal.aborted) error.value = message(e)
    // Whatever arrived before the failure is still worth showing.
    rows.value = collected.slice()
  } finally {
    elapsedMs.value = performance.now() - started
    running.value = false
  }
}

function cancel() {
  queryAbort?.abort()
  hitsAbort?.abort()
  facetsAbort?.abort()
  running.value = false
}

// The histogram and the facets are separate queries against the same window.
// They are fired alongside the rows rather than after them, and a failure in
// either is silent: neither is the answer the user asked for, and an error
// banner over a working result table would be noise.
//
// The facets are only asked for when their panel is open. A closed panel would
// otherwise cost an extra VictoriaLogs query per run to fill something nobody
// is looking at.
function loadSidecars(window_: TimeRange) {
  loadHits(window_)
  if (facetsOpen.value) loadFacets(window_)
}

function loadHits(window_: TimeRange) {
  hitsAbort?.abort()
  hitsAbort = new AbortController()
  const signal = hitsAbort.signal

  hitsLoading.value = true
  fetchHits(query.value, window_, activeTool.value, signal)
    .then((h) => (hits.value = h))
    .catch(() => {
      if (!signal.aborted) hits.value = null
    })
    .finally(() => {
      if (!signal.aborted) hitsLoading.value = false
    })
}

function loadFacets(window_: TimeRange) {
  facetsAbort?.abort()
  facetsAbort = new AbortController()
  const signal = facetsAbort.signal

  facetsLoading.value = true
  fetchFacets(query.value, window_, 10, activeTool.value, signal)
    .then((f) => (facets.value = f.facets ?? []))
    .catch(() => {
      if (!signal.aborted) facets.value = []
    })
    .finally(() => {
      if (!signal.aborted) facetsLoading.value = false
    })
}

/* --- live tail ------------------------------------------------------------ */

// Incoming lines are buffered and flushed on a timer. A busy tail can deliver
// hundreds of lines a second, and one render per line would spend the whole
// frame budget on Vue rather than on drawing.
let tailBuffer: LogRow[] = []
let tailTimer: number | undefined

function toggleTail() {
  if (tailing.value) {
    stopTail()
    return
  }

  cancel()
  error.value = ''
  rows.value = []
  selectedIndex.value = -1
  hits.value = null
  facets.value = []
  shownRange.value = null
  tailing.value = true
  writeHash()

  tailSource = openTail(query.value, activeTool.value, {
    onRow: (row) => {
      tailBuffer.push(row)
    },
    onError: (m) => {
      error.value = m
      stopTail()
    },
  })

  tailTimer = window.setInterval(flushTail, 250)
}

function flushTail() {
  if (!tailBuffer.length) return

  const cap = cfg.value?.max_rows ?? 5000
  // Newest first, matching a finished query, and bounded the same way: a tail
  // left running for a day must not grow the tab until it dies.
  const next = [...tailBuffer.reverse(), ...rows.value].slice(0, cap)
  tailBuffer = []

  // Keep whatever the reader had open selected, by identity rather than index.
  const keep = selectedRow.value
  rows.value = next
  if (keep) {
    const at = next.indexOf(keep)
    selectedIndex.value = at
  }
}

function stopTail() {
  tailing.value = false
  tailSource?.close()
  tailSource = undefined
  window.clearInterval(tailTimer)
  tailTimer = undefined
  flushTail()
}

/* --- query manipulation --------------------------------------------------- */

function addFilter(f: { field: string; value: string; negate: boolean }) {
  const term = `${f.negate ? '-' : ''}${f.field}:${quote(f.value)}`
  query.value = query.value.trim() === '*' || query.value.trim() === ''
    ? term
    : `${query.value.trim()} ${term}`
  run()
}

function quote(v: string): string {
  return /^[A-Za-z0-9_.\-/]+$/.test(v) && v !== '' ? v : JSON.stringify(v)
}

function toggleColumn(field: string) {
  const at = columns.value.indexOf(field)
  if (at >= 0) {
    // _time is the spine of the table; removing it would leave rows with no
    // way to tell which came first.
    if (field === '_time') return
    columns.value = columns.value.filter((c) => c !== field)
    return
  }
  columns.value = [...columns.value, field]
}

// Switching tool changes what every panel is looking at, so it re-runs rather
// than leaving the previous tool's rows on screen under the new icon.
function selectTool(id: string) {
  if (id === activeTool.value) return
  activeTool.value = id
  writeHash()
  run()
}

function zoom(to: TimeRange) {
  range.value = { kind: 'absolute', startMs: to.startMs, endMs: to.endMs }
  run()
}

function download() {
  const body = rows.value.map((r) => JSON.stringify(r)).join('\n')
  const url = URL.createObjectURL(new Blob([body], { type: 'application/x-ndjson' }))
  const a = document.createElement('a')
  a.href = url
  a.download = `logs-${new Date().toISOString().replace(/[:.]/g, '-')}.ndjson`
  a.click()
  URL.revokeObjectURL(url)
}

/* --- shareable state ------------------------------------------------------ */

// The query, the window and the row cap live in the URL, so a link to what you
// are looking at is the address bar rather than a screenshot.
function writeHash() {
  const p = new URLSearchParams()
  p.set('q', query.value)
  p.set('limit', String(limit.value))
  if (activeTool.value) p.set('tool', activeTool.value)
  if (range.value.kind === 'relative') {
    p.set('range', String(range.value.seconds))
  } else {
    p.set('start', String(range.value.startMs))
    p.set('end', String(range.value.endMs))
  }
  if (columns.value.join(',') !== '_time,_msg') p.set('cols', columns.value.join(','))
  history.replaceState(null, '', `#${p.toString()}`)
}

function readHash(): boolean {
  const raw = window.location.hash.replace(/^#/, '')
  if (!raw) return false

  const p = new URLSearchParams(raw)
  const q = p.get('q')
  if (q) query.value = q

  const l = Number(p.get('limit'))
  if (Number.isFinite(l) && l > 0) limit.value = l

  const cols = p.get('cols')
  if (cols) columns.value = cols.split(',').filter(Boolean)

  // Only a tool this deployment actually offers, and only one this account was
  // offered: a stale or invented id in a shared link falls back to the default
  // rather than erroring on every request.
  const wanted = p.get('tool')
  if (wanted && (cfg.value?.tools ?? []).some((t) => t.id === wanted)) activeTool.value = wanted

  const start = Number(p.get('start'))
  const end = Number(p.get('end'))
  if (Number.isFinite(start) && Number.isFinite(end) && start > 0 && end > start) {
    range.value = { kind: 'absolute', startMs: start, endMs: end }
    return Boolean(q)
  }

  const seconds = Number(p.get('range'))
  if (Number.isFinite(seconds) && seconds > 0) range.value = { kind: 'relative', seconds }
  return Boolean(q)
}

/* --- session -------------------------------------------------------------- */

async function signOut() {
  try {
    const res = await fetch(new URL('api/auth/logout', document.baseURI), {
      method: 'POST',
      credentials: 'same-origin',
    })
    const body = await res.json().catch(() => ({}))
    if (typeof body?.provider_logout_url === 'string') {
      window.location.assign(body.provider_logout_url)
      return
    }
  } catch {
    // Even a failed call has dropped the cookie in every case that matters;
    // reloading is what makes the UI agree.
  }
  window.location.reload()
}

function message(e: unknown): string {
  if (e instanceof ApiError) return e.message
  if (e instanceof Error) return e.message
  return String(e)
}

onMounted(async () => {
  try {
    const c = await fetchConfig()
    cfg.value = c
    limit.value = c.default_limit
    range.value = { kind: 'relative', seconds: c.default_range_seconds }
    // The first tool is the default, matching the server: a request that names
    // no tool gets the first one's filter, so the rail must agree.
    activeTool.value = c.tools[0]?.id ?? ''
  } catch (e) {
    // A 401 has already navigated to the IdP; anything else means the app
    // cannot start, and saying so beats an empty screen.
    if (!(e instanceof ApiError && e.status === 401)) bootError.value = message(e)
    return
  }

  const hadQuery = readHash()
  if (hadQuery) run()
})

onUnmounted(() => {
  queryAbort?.abort()
  hitsAbort?.abort()
  facetsAbort?.abort()
  stopTail()
})
</script>

<template>
  <div class="app">
    <SideRail
      :version="cfg?.version ?? ''"
      :commit="cfg?.commit ?? ''"
      :auth-enabled="cfg?.auth_enabled ?? false"
      :user="cfg?.user ?? null"
      :tools="cfg?.tools ?? []"
      :active-tool="tool?.id ?? ''"
      @sign-out="signOut"
      @select-tool="selectTool"
    />

    <div class="workspace">
      <p v-if="bootError" class="banner">{{ bootError }}</p>

      <template v-if="cfg">
        <QueryBar
          :query="query"
          :prefix="tool?.query ?? ''"
          :tool-id="tool?.id ?? ''"
          :range="range"
          :limit="limit"
          :max-rows="cfg.max_rows"
          :running="running"
          :tailing="tailing"
          :presets="cfg.queries"
          @update:query="query = $event"
          @update:range="range = $event"
          @update:limit="limit = $event"
          @run="run"
          @cancel="cancel"
          @toggle-tail="toggleTail"
        />

        <!-- What came back, directly under what was asked: how many rows, how
             long it took, whether the cap cut it short, and a way to take it
             away with you. Attached to the query line rather than parked in a
             footer, because every word of it is about the query on the line
             above — and a truncation warning at the far bottom of the window is
             a warning nobody reads. -->
        <div class="status muted mono">
          <span>{{ rows.length.toLocaleString() }} rows</span>
          <span v-if="running">· running…</span>
          <span v-else-if="elapsedMs && !tailing">· {{ (elapsedMs / 1000).toFixed(2) }}s</span>
          <span v-if="tailing" class="live">· following</span>
          <span v-if="truncated" class="warn">
            · limited to {{ limit.toLocaleString() }} rows — narrow the query or raise the limit
          </span>

          <span class="spacer"></span>

          <button type="button" class="ghost small" :disabled="!rows.length" @click="download">
            Download
          </button>
        </div>

        <p v-if="error" class="banner">{{ error }}</p>

        <HitsChart
          v-if="!tailing"
          :hits="hits"
          :range="shownRange"
          :loading="hitsLoading"
          @zoom="zoom"
        />

        <div class="body">
          <Facets
            :facets="facets"
            :loading="facetsLoading"
            :columns="columns"
            :tailing="tailing"
            :open="facetsOpen"
            @filter="addFilter"
            @toggle-column="toggleColumn"
            @toggle-panel="toggleFacets"
          />

          <div class="main">
            <ResultsTable
              :rows="rows"
              :columns="columns"
              :selected-index="selectedIndex"
              :running="running"
              @select="selectedIndex = $event === selectedIndex ? -1 : $event"
              @remove-column="toggleColumn"
            />

          </div>

          <RowDetail
            v-if="selectedRow"
            :row="selectedRow"
            @close="selectedIndex = -1"
            @filter="addFilter"
            @toggle-column="toggleColumn"
          />
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
/* Reads as the foot of the query bar, not as a band of its own: same sunken
   background, no rule between them, and the border underneath closes the pair
   off from the chart below. */
.status {
  display: flex;
  align-items: center;
  gap: 6px;
  /* 4px above and below the text. The query bar has no bottom padding of its
     own, so this is the whole gap between the input and this line — otherwise
     the two would add up and the space above would be double the space below. */
  padding: 4px 8px;
  border-bottom: 1px solid var(--border);
  background: var(--bg-sunken);
  font-size: 11px;
  line-height: 1.3;
}

/* Sized to the status line rather than to the query line: it is an occasional
   control and should not set the height of the row it sits in. */
/* Borderless and short enough to fit inside the text's own line box: a taller
   button would set the row height, and the 4px above and below would silently
   become 5. Ghost buttons have a transparent border rather than none, so those
   two pixels have to be taken off explicitly. */
.status button.small {
  padding: 0 5px;
  border: 0;
  font-size: 11px;
  line-height: 1.3;
}

/* .ghost hovers to --bg-sunken, which is this bar's own background and so
   invisible here. */
.status button.small:hover:not(:disabled) {
  background: var(--hover);
}



.live { color: var(--accent); }
.warn { color: var(--warn); }
</style>
