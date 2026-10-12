<script setup lang="ts">
/* VictoriaLogs' storage: how much disk it uses, how much it has left, and how
 * well it compresses.
 *
 * Read from VictoriaLogs' own /metrics, which is the only place it is
 * published, and which is instance-wide: per storage tier and per data path,
 * never per stream or per tool. Single-node only — a cluster's vlselect does not
 * export these, and the server says so instead of showing zeros.
 *
 * Like the icons sheet, a page of this app rather than a page of the logs:
 * App.vue hands the window over once the server has said who this is, and
 * nothing here runs a query.
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { fetchStatus } from '../api'
import type { StorageStatus } from '../types'

const status = ref<StorageStatus | null>(null)
const error = ref('')
const loading = ref(false)
let abort: AbortController | null = null

async function load() {
  abort?.abort()
  abort = new AbortController()
  loading.value = true
  error.value = ''
  try {
    status.value = await fetchStatus(abort.signal)
  } catch (e) {
    if ((e as Error).name === 'AbortError') return
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

onMounted(load)
onUnmounted(() => abort?.abort())

const UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']

function fmtBytes(n: number): string {
  let i = 0
  while (Math.abs(n) >= 1024 && i < UNITS.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(i === 0 || n >= 100 ? 0 : 1)} ${UNITS[i]}`
}

const countFmt = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 })
const fmtCount = (n: number) => countFmt.format(n)

// "12.3×", or a dash where there is nothing compressed to divide by: an empty
// in-memory tier is normal, and Infinity× is not a ratio.
function ratio(uncompressed: number, compressed: number): string {
  return compressed > 0 ? `${(uncompressed / compressed).toFixed(1)}×` : '—'
}

const onDisk = computed(() => (status.value ? status.value.data_bytes + status.value.indexdb_bytes : 0))
const readOnly = computed(() => status.value?.paths.filter((p) => p.read_only) ?? [])

// Shares of one filesystem, for the bar under it. Used rather than "ours":
// the filesystem may hold other things, and what fills it is what matters.
function usedPct(p: { free_bytes: number; total_bytes: number }): number {
  return p.total_bytes > 0 ? ((p.total_bytes - p.free_bytes) / p.total_bytes) * 100 : 0
}
function capPct(p: { total_bytes: number; max_usage_bytes?: number }): number | null {
  return p.max_usage_bytes && p.total_bytes > 0 ? Math.min(100, (p.max_usage_bytes / p.total_bytes) * 100) : null
}
function level(pct: number): string {
  return pct >= 90 ? 'error' : pct >= 75 ? 'warn' : 'ok'
}
</script>

<template>
  <!-- The workspace's own shape: a sunken bar on top, then what scrolls. -->
  <div class="storage workspace">
    <div class="bar">
      <strong>Storage</strong>
      <span class="muted">VictoriaLogs' own figures for the whole instance — every tenant and every stream</span>
      <span class="spacer"></span>
      <button type="button" class="primary" :disabled="loading" @click="load">
        {{ loading ? 'Loading…' : 'Refresh' }}
      </button>
    </div>

    <p v-if="error" class="banner">{{ error }}</p>
    <p v-for="p in readOnly" :key="p.path" class="banner">
      {{ p.path }} is read-only: free space fell under the configured minimum and VictoriaLogs has
      stopped accepting logs.
    </p>

    <div v-if="status" class="content">
      <section class="kpis">
        <div class="kpi">
          <span class="label">On disk</span>
          <span class="value">{{ fmtBytes(onDisk) }}</span>
          <span class="muted">data {{ fmtBytes(status.data_bytes) }} · index {{ fmtBytes(status.indexdb_bytes) }}</span>
        </div>
        <div class="kpi">
          <span class="label">Compression</span>
          <span class="value">{{ ratio(status.uncompressed_bytes, status.compressed_bytes) }}</span>
          <span class="muted">{{ fmtBytes(status.uncompressed_bytes) }} raw → {{ fmtBytes(status.compressed_bytes) }}</span>
        </div>
        <div class="kpi">
          <span class="label">Log entries</span>
          <span class="value">{{ fmtCount(status.rows) }}</span>
          <span class="muted">{{ status.rows.toLocaleString() }} in {{ status.parts.toLocaleString() }} parts</span>
        </div>
        <div class="kpi">
          <span class="label">Days kept</span>
          <span class="value">{{ status.partitions }}</span>
          <span class="muted">{{ status.retention ? `retention ${status.retention}` : 'daily partitions' }}</span>
        </div>
      </section>

      <h2>Disk</h2>
      <section v-for="p in status.paths" :key="p.path" class="disk">
        <div class="disk-head">
          <code>{{ p.path || 'storage' }}</code>
          <span>
            {{ fmtBytes(p.total_bytes - p.free_bytes) }} used of {{ fmtBytes(p.total_bytes) }} ·
            <strong>{{ fmtBytes(p.free_bytes) }} free</strong>
          </span>
        </div>
        <div class="meter" role="meter" :aria-valuenow="Math.round(usedPct(p))" aria-valuemin="0"
             aria-valuemax="100" :aria-label="`${p.path} used`">
          <span class="fill" :class="level(usedPct(p))" :style="{ width: `${usedPct(p)}%` }"></span>
          <span v-if="capPct(p) !== null" class="cap" :style="{ left: `${capPct(p)}%` }"
                :title="`Retention cap: ${fmtBytes(p.max_usage_bytes ?? 0)}`"></span>
        </div>
        <p v-if="p.max_usage_bytes" class="muted note">
          Past {{ fmtBytes(p.max_usage_bytes) }} the oldest days are deleted.
        </p>
      </section>

      <h2>Tiers</h2>
      <table v-if="status.tiers.length">
        <thead>
          <tr>
            <th>tier</th>
            <th>entries</th>
            <th>parts</th>
            <th>raw</th>
            <th>compressed</th>
            <th>ratio</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="t in status.tiers" :key="t.name">
            <td>{{ t.name }}</td>
            <td>{{ t.rows.toLocaleString() }}</td>
            <td>{{ t.parts.toLocaleString() }}</td>
            <td>{{ fmtBytes(t.uncompressed_bytes) }}</td>
            <td>{{ fmtBytes(t.compressed_bytes) }}</td>
            <td>{{ ratio(t.uncompressed_bytes, t.compressed_bytes) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
/* Not .status: App.vue has a scoped .status of its own, and a child's root
   element carries the parent's scope too, so it would match here. The layout
   itself is the shared .workspace column. */

/* The query bar's strip: same sunken background, same control height, so moving
   between the logs and this page does not move anything that stays. */
.bar {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: calc(var(--control-h) + 14px);
  padding: 7px 8px;
  border-bottom: 1px solid var(--border);
  background: var(--bg-sunken);
}

.bar .muted {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.bar button {
  height: var(--control-h);
}

.content {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding: 12px;
}

h2 {
  margin: 16px 0 6px;
  color: var(--text-dim);
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.kpis {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 8px;
  max-width: 960px;
}

.kpi {
  display: flex;
  flex-direction: column;
  padding: 8px 10px;
  border: 1px solid var(--border);
  border-radius: 5px;
  background: var(--bg-raised);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

.kpi .label {
  color: var(--text-dim);
  font-size: 11px;
  font-weight: 600;
}

.kpi .value {
  font-size: 20px;
  font-weight: 600;
  line-height: 1.3;
}

.disk {
  max-width: 960px;
  margin-bottom: 8px;
}

.disk-head {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 4px;
  font-variant-numeric: tabular-nums;
}

.meter {
  position: relative;
  height: 8px;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg-sunken);
}

.fill {
  position: absolute;
  inset: 0 auto 0 0;
  border-radius: 4px;
}

.fill.ok { background: var(--ok); }
.fill.warn { background: var(--warn); }
.fill.error { background: var(--danger); }

/* The retention cap: where VictoriaLogs starts deleting the oldest days. */
.cap {
  position: absolute;
  top: -4px;
  bottom: -4px;
  width: 2px;
  margin-left: -1px;
  background: var(--text);
}

.note {
  margin: 4px 0 0;
  font-size: 12px;
}

/* The results table's look: a sunken header of small dim labels, one row
   height, monospace figures. */
table {
  border-collapse: collapse;
  border: 1px solid var(--border);
  font-family: var(--mono);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

th {
  height: var(--row-height);
  padding: 0 12px;
  border-bottom: 1px solid var(--border-strong);
  background: var(--bg-sunken);
  color: var(--text-dim);
  font-family: var(--sans);
  font-size: 11px;
  font-weight: 600;
  text-align: right;
}

td {
  height: var(--row-height);
  padding: 0 12px;
  border-bottom: 1px solid var(--border);
  text-align: right;
}

tbody tr:hover {
  background: var(--hover);
}

th:first-child,
td:first-child {
  text-align: left;
}
</style>
