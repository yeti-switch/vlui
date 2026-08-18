<script setup lang="ts">
import { computed, ref } from 'vue'
import {
  QUICK_RANGES,
  describeRange,
  fromPickerValue,
  resolveRange,
  toPickerValue,
  type RangeSelection,
} from '../time'
import { activeTZ } from '../settings'

const props = defineProps<{ modelValue: RangeSelection }>()
const emit = defineEmits<{ 'update:modelValue': [RangeSelection] }>()

const open = ref(false)

// Seeded from whatever is selected now, so switching to absolute starts from
// the window you were already looking at instead of from today at midnight.
const resolved = computed(() => resolveRange(props.modelValue))
const fromInput = ref('')
const toInput = ref('')

function toggle() {
  if (!open.value) {
    fromInput.value = toPickerValue(resolved.value.startMs)
    toInput.value = toPickerValue(resolved.value.endMs)
  }
  open.value = !open.value
}

function pickQuick(seconds: number) {
  emit('update:modelValue', { kind: 'relative', seconds })
  open.value = false
}

const absoluteError = ref('')

function applyAbsolute() {
  const startMs = fromPickerValue(fromInput.value)
  const endMs = fromPickerValue(toInput.value)

  if (Number.isNaN(startMs) || Number.isNaN(endMs)) {
    absoluteError.value = 'both ends must be valid times'
    return
  }
  if (startMs >= endMs) {
    absoluteError.value = 'the start must come before the end'
    return
  }
  absoluteError.value = ''
  emit('update:modelValue', { kind: 'absolute', startMs, endMs })
  open.value = false
}

const label = computed(() => describeRange(props.modelValue))
</script>

<template>
  <div class="picker">
    <button type="button" @click="toggle" :title="label">
      <span class="clock">🕒</span>
      <span class="label">{{ label }}</span>
    </button>

    <div v-if="open" class="pop">
      <div class="quick">
        <button
          v-for="q in QUICK_RANGES"
          :key="q.seconds"
          type="button"
          class="ghost quick-btn"
          @click="pickQuick(q.seconds)"
        >
          {{ q.label }}
        </button>
      </div>

      <div class="absolute">
        <label>
          <span class="muted">from</span>
          <input v-model="fromInput" type="datetime-local" step="1" />
        </label>
        <label>
          <span class="muted">to</span>
          <input v-model="toInput" type="datetime-local" step="1" />
        </label>
        <div v-if="absoluteError" class="err">{{ absoluteError }}</div>
        <!-- Which clock these two boxes are on. The browser's own picker shows
             bare digits with no zone, so without this the operator has to
             remember what the rail is set to. -->
        <div class="muted zone">times in {{ activeTZ }}</div>
        <button type="button" class="primary" @click="applyAbsolute">Apply</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.picker { position: relative; }

/* Matches the rest of the query line; see --control-h. */
.picker > button { height: var(--control-h); }

.label { font-variant-numeric: tabular-nums; }
.clock { margin-right: 6px; }

.pop {
  position: absolute;
  top: calc(100% + 4px);
  right: 0;
  z-index: 20;
  width: 280px;
  padding: 10px;
  background: var(--bg-raised);
  border: 1px solid var(--border);
  border-radius: 8px;
  box-shadow: 0 8px 24px rgb(0 0 0 / 25%);
}

.quick {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--border);
}

.quick-btn { border: 1px solid var(--border); min-width: 44px; }

.absolute {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding-top: 10px;
}

.absolute label { display: flex; flex-direction: column; gap: 3px; }
.absolute input { width: 100%; }

.err { color: var(--danger); font-size: 12px; }
.zone { font-size: 11px; }
</style>
