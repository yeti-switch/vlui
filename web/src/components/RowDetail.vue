<script setup lang="ts">
import { computed, ref } from 'vue'
import { formatIfInstant, formatStamp, parseLogTime } from '../time'
import type { LogRow } from '../types'

const props = defineProps<{ row: LogRow }>()

const emit = defineEmits<{
  close: []
  filter: [{ field: string; value: string; negate: boolean }]
  'toggle-column': [string]
}>()

// _time and _msg first, then everything else alphabetically: those two are what
// a reader looks for, and the rest is a reference list.
const fields = computed(() => {
  const keys = Object.keys(props.row)
  const lead = ['_time', '_msg'].filter((k) => keys.includes(k))
  const rest = keys.filter((k) => !lead.includes(k)).sort()
  return [...lead, ...rest]
})

function display(field: string): string {
  const raw = props.row[field] ?? ''

  if (field === '_time') {
    const ms = parseLogTime(raw)
    return Number.isNaN(ms) ? raw : `${formatStamp(ms)}  (${raw})`
  }

  // Same treatment for any other field carrying an instant, and the same
  // parenthesised original: this pane is where somebody checks what was
  // actually stored, so the raw value is never replaced, only accompanied.
  const zoned = formatIfInstant(raw)
  return zoned ? `${zoned}  (${raw})` : raw
}

const copied = ref(false)

async function copyJSON() {
  try {
    await navigator.clipboard.writeText(JSON.stringify(props.row, null, 2))
    copied.value = true
    window.setTimeout(() => (copied.value = false), 1500)
  } catch {
    // Clipboard access is refused on an insecure origin. Nothing to do about
    // it here, and an error banner would be worse than the missing copy.
  }
}
</script>

<template>
  <aside class="detail">
    <header>
      <span>Log entry</span>
      <div class="spacer"></div>
      <button type="button" class="ghost" @click="copyJSON">{{ copied ? 'Copied' : 'Copy JSON' }}</button>
      <button type="button" class="ghost" title="Close" @click="emit('close')">×</button>
    </header>

    <dl>
      <template v-for="f in fields" :key="f">
        <dt class="mono">
          {{ f }}
          <span class="actions">
            <button type="button" class="ghost" title="Filter to this value" @click="emit('filter', { field: f, value: row[f] ?? '', negate: false })">＝</button>
            <button type="button" class="ghost" title="Exclude this value" @click="emit('filter', { field: f, value: row[f] ?? '', negate: true })">−</button>
            <button type="button" class="ghost" title="Show as a column" @click="emit('toggle-column', f)">▦</button>
          </span>
        </dt>
        <dd class="mono">{{ display(f) }}</dd>
      </template>
    </dl>
  </aside>
</template>

<style scoped>
.detail {
  width: 420px;
  flex: none;
  display: flex;
  flex-direction: column;
  border-left: 1px solid var(--border);
  background: var(--bg-raised);
  overflow: hidden;
}

header {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 6px 8px;
  border-bottom: 1px solid var(--border);
  font-weight: 600;
}

dl { margin: 0; padding: 8px 10px; overflow-y: auto; }

dt {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-top: 10px;
  font-size: 11px;
  color: var(--text-dim);
}

dt:first-child { margin-top: 0; }

.actions { visibility: hidden; display: flex; gap: 2px; }
dt:hover .actions { visibility: visible; }

dd {
  margin: 2px 0 0;
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
