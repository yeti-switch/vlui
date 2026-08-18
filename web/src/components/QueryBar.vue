<script setup lang="ts">
import { nextTick, onMounted, ref, watch } from 'vue'
import { fetchFieldNames, fetchFieldValues } from '../api'
import { resolveRange, type RangeSelection } from '../time'
import type { Preset } from '../types'
import TimeRangePicker from './TimeRangePicker.vue'

const props = defineProps<{
  query: string
  // The active tool's filter, shown before the input because it is part of
  // every query this session sends. Read-only on purpose: it is applied by the
  // server from the tool id, and an editable copy here would imply otherwise.
  prefix: string
  // The id that filter is applied from. Autocomplete carries it too, or it
  // would offer field values drawn from logs the tool excludes.
  toolId: string
  range: RangeSelection
  limit: number
  maxRows: number
  running: boolean
  tailing: boolean
  presets: Preset[]
}>()

const emit = defineEmits<{
  'update:query': [string]
  'update:range': [RangeSelection]
  'update:limit': [number]
  run: []
  cancel: []
  'toggle-tail': []
}>()

const input = ref<HTMLTextAreaElement | null>(null)

/* The box is one control-height at rest, which is what keeps it level with the
   buttons beside it, and grows to fit a query written over several lines
   (Shift+Enter). Without this the second line is simply invisible: a textarea
   does not size itself to its content, so it scrolls instead.

   The resize handle is gone with it — two things setting the same height fight,
   and this one is always right. */
function autosize() {
  const el = input.value
  if (!el) return
  // Measured from a collapsed box, or it could only ever grow.
  el.style.height = 'auto'
  // scrollHeight is the content box; the borders are ours to add back, and CSS
  // min-height holds the floor at --control-h.
  el.style.height = `${el.scrollHeight + el.offsetHeight - el.clientHeight}px`
}

onMounted(autosize)

/* --- autocomplete ---------------------------------------------------------
   The word under the caret decides what is offered: a bare word asks for field
   names, and anything after a colon asks for that field's values. Both come
   from the same VictoriaLogs that will answer the query, so the suggestions
   describe the logs actually stored rather than a schema someone wrote down. */

interface Suggestion {
  value: string
  hits: number
}

const suggestions = ref<Suggestion[]>([])
const active = ref(-1)
const suggestOpen = ref(false)

let debounce: number | undefined
let inflight: AbortController | undefined

interface Token {
  start: number
  end: number
  text: string
}

function tokenAtCaret(): Token | null {
  const el = input.value
  if (!el) return null

  const caret = el.selectionStart ?? 0
  const text = el.value
  let start = caret
  // A LogsQL token ends at whitespace or at one of the grouping characters;
  // colons and dots are part of it, since field names contain them.
  while (start > 0 && !/[\s()|,]/.test(text[start - 1]!)) start--
  return { start, end: caret, text: text.slice(start, caret) }
}

function requestSuggestions() {
  window.clearTimeout(debounce)
  debounce = window.setTimeout(loadSuggestions, 180)
}

async function loadSuggestions() {
  const token = tokenAtCaret()
  if (!token || token.text.length === 0) {
    close()
    return
  }

  inflight?.abort()
  inflight = new AbortController()
  const range = resolveRange(props.range)

  // Everything typed so far, minus the token being completed: suggestions
  // should describe the logs the rest of the query selects.
  const context = (props.query.slice(0, token.start) + props.query.slice(token.end)).trim() || '*'

  try {
    const colon = token.text.indexOf(':')
    const res =
      colon >= 0
        ? await fetchFieldValues(context, token.text.slice(0, colon), range, token.text.slice(colon + 1), props.toolId, inflight.signal)
        : await fetchFieldNames(context, range, token.text, props.toolId, inflight.signal)

    suggestions.value = res.values.slice(0, 12).map((v) => ({
      value: colon >= 0 ? `${token.text.slice(0, colon)}:${quote(v.value)}` : v.value,
      hits: v.hits,
    }))
    suggestOpen.value = suggestions.value.length > 0
    active.value = -1
  } catch {
    // Autocomplete is a convenience. A failure here must never interrupt
    // typing, and the query itself will report anything that matters.
    close()
  }
}

// A value with spaces or LogsQL punctuation has to be quoted or the query it is
// pasted into no longer parses.
function quote(v: string): string {
  return /^[A-Za-z0-9_.\-/]+$/.test(v) ? v : JSON.stringify(v)
}

function accept(s: Suggestion) {
  const token = tokenAtCaret()
  if (!token) return

  const before = props.query.slice(0, token.start)
  const after = props.query.slice(token.end)
  const next = `${before}${s.value}${after}`
  emit('update:query', next)
  close()

  nextTick(() => {
    const el = input.value
    if (!el) return
    const caret = before.length + s.value.length
    el.focus()
    el.setSelectionRange(caret, caret)
  })
}

function close() {
  suggestOpen.value = false
  suggestions.value = []
  active.value = -1
}

function onKeydown(e: KeyboardEvent) {
  if (suggestOpen.value) {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      active.value = (active.value + 1) % suggestions.value.length
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      active.value = active.value <= 0 ? suggestions.value.length - 1 : active.value - 1
      return
    }
    if (e.key === 'Escape') {
      e.preventDefault()
      close()
      return
    }
    if ((e.key === 'Enter' || e.key === 'Tab') && active.value >= 0) {
      e.preventDefault()
      accept(suggestions.value[active.value]!)
      return
    }
  }

  // Enter runs; Shift+Enter is a newline, because a long query reads better
  // over several lines.
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    close()
    emit('run')
  }
}

watch(
  () => props.query,
  async () => {
    if (document.activeElement === input.value) requestSuggestions()
    // Also fires for a query set from elsewhere — a preset, or a facet click —
    // which is exactly when the box has to resize without anyone typing in it.
    await nextTick()
    autosize()
  },
)

const presetOpen = ref(false)

function usePreset(p: Preset) {
  emit('update:query', p.query)
  presetOpen.value = false
  emit('run')
}
</script>

<template>
  <div class="bar">
    <div class="line">
      <div class="field">
        <span v-if="prefix" class="prefix mono" :title="`Applied by the selected tool: ${prefix}`">{{ prefix }}</span>

        <textarea
          ref="input"
          class="mono query"
          rows="1"
          spellcheck="false"
          autocapitalize="off"
          autocomplete="off"
          placeholder="LogsQL — e.g.  _stream:{app=&quot;sems&quot;} error | fields _time, _msg"
          :value="query"
          @input="emit('update:query', ($event.target as HTMLTextAreaElement).value)"
          @keydown="onKeydown"
          @blur="close"
        ></textarea>

        <ul v-if="suggestOpen" class="suggest">
          <li
            v-for="(s, i) in suggestions"
            :key="s.value"
            :class="{ active: i === active }"
            @mousedown.prevent="accept(s)"
          >
            <span class="mono">{{ s.value }}</span>
            <span class="muted hits">{{ s.hits.toLocaleString() }}</span>
          </li>
        </ul>
      </div>

      <TimeRangePicker
        :model-value="range"
        @update:model-value="emit('update:range', $event)"
      />

      <label class="limit" title="Maximum rows to fetch">
        <input
          type="number"
          min="1"
          :max="maxRows"
          :value="limit"
          @change="emit('update:limit', Number(($event.target as HTMLInputElement).value))"
        />
      </label>

      <button v-if="running" type="button" @click="emit('cancel')">Cancel</button>
      <button v-else type="button" class="primary" @click="emit('run')">Run</button>

      <button
        type="button"
        :class="{ tailing }"
        :title="tailing ? 'Stop following' : 'Follow new logs as they arrive'"
        @click="emit('toggle-tail')"
      >
        {{ tailing ? '■ Live' : '▶ Live' }}
      </button>

      <div v-if="presets.length" class="presets">
        <button type="button" class="ghost" @click="presetOpen = !presetOpen">Presets ▾</button>
        <ul v-if="presetOpen" class="preset-list">
          <li v-for="p in presets" :key="p.name" @mousedown.prevent="usePreset(p)">
            <div>{{ p.name }}</div>
            <div class="mono muted preset-query">{{ p.query }}</div>
          </li>
        </ul>
      </div>
    </div>
  </div>
</template>

<style scoped>
.bar {
  /* No bottom border: the status strip sits directly underneath and closes the
     pair off, so a rule here would cut the two halves apart. No bottom padding
     either — the strip's own 4px is the gap, and adding to it here would make
     the space above that text unequal to the space below it. */
  padding: 7px 8px 0;
  background: var(--bg-sunken);
}

.line {
  display: flex;
  align-items: flex-start;
  gap: 6px;
}

/* The box is the field, not the textarea: the tool's filter sits inside the
   same border as what you type, because the two are one query. */
.field {
  position: relative;
  display: flex;
  flex: 1;
  min-width: 0;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 5px;
}

.field:focus-within {
  outline: 2px solid var(--accent);
  outline-offset: -1px;
}

.prefix {
  flex: none;
  max-width: 40%;
  padding: 6px 8px 4px;
  overflow: hidden;
  color: var(--text-dim);
  font-size: 12px;
  line-height: 18px;
  white-space: nowrap;
  text-overflow: ellipsis;
  /* Not editable, and it should not look it. */
  background: var(--bg-sunken);
  border-right: 1px solid var(--border);
  border-radius: 4px 0 0 4px;
  cursor: default;
  user-select: none;
}

.query {
  /* Block, not the default inline-block: an inline textarea sits on a text
     baseline, and the line box reserves descender space under it — 5px of dead
     air below the query line that nothing occupies. */
  display: block;
  width: 100%;
  /* Explicit height rather than rows="1": a textarea sizes itself from its
     font, which is monospace here and taller than the sans-serif in the
     buttons. The padding and line-height below add up to exactly --control-h.
     Dragging the resize handle overrides it, which is the point of the handle. */
  height: var(--control-h);
  min-height: var(--control-h);
  max-height: 40vh;
  flex: 1;
  min-width: 0;
  resize: none;
  /* 6/4 rather than 5/5, and the extra pixel at the top is deliberate. A line
     box centres the font's em square, not the part of it that carries ink: this
     monospace reserves more room above the baseline than below, so evenly split
     padding leaves the text sitting 1px high in the box. Measured — the ink
     band is 10.83px from both edges at 6/4, against 9.83/11.83 at 5/5. The two
     still total 10px, which is what keeps the box at --control-h.

     Note this correction is per font: the row-cap input beside this one is
     13px sans and is already centred at an even 5/5, so it is left alone. */
  padding: 6px 8px 4px;
  line-height: 18px;
  white-space: pre-wrap;
  overflow-y: auto;
  background: transparent;
  border: 0;
}

/* The ring belongs to the field, which is what the eye reads as the input. */
.query:focus { outline: none; }

/* Everything else on the line, to the same height. */
.line > button,
.presets > button,
.limit input {
  height: var(--control-h);
}

.suggest, .preset-list {
  position: absolute;
  z-index: 30;
  margin: 2px 0 0;
  padding: 4px;
  list-style: none;
  background: var(--bg-raised);
  border: 1px solid var(--border);
  border-radius: 6px;
  box-shadow: 0 8px 24px rgb(0 0 0 / 25%);
  max-height: 40vh;
  overflow-y: auto;
}

.suggest { left: 0; right: 0; top: 100%; }

.suggest li {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  padding: 3px 6px;
  border-radius: 4px;
  cursor: pointer;
}

.suggest li.active, .suggest li:hover { background: var(--accent-soft); }
.hits { font-variant-numeric: tabular-nums; }

.limit input { width: 76px; text-align: right; }

button.tailing {
  background: var(--accent-soft);
  border-color: var(--accent);
  color: var(--accent);
}

.presets { position: relative; }
.preset-list { right: 0; top: 100%; width: 320px; }
.preset-list li { padding: 5px 6px; border-radius: 4px; cursor: pointer; }
.preset-list li:hover { background: var(--accent-soft); }
.preset-query { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
</style>
