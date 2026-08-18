<script setup lang="ts">
// Timezone picker. Several hundred IANA zones, so it searches.
//
// Display only: every request carries instants, so changing this re-renders
// what is on screen and never re-queries.
import { computed, nextTick, ref, watch } from 'vue'
import { activeTZ, browserTZ, setTimezone, timezone, timezones, tzAbbrev } from '../settings'

const open = ref(false)
const query = ref('')
const input = ref<HTMLInputElement | null>(null)
const root = ref<HTMLElement | null>(null)

const matches = computed(() => {
  const q = query.value.trim().toLowerCase().replace(/\s+/g, '_')
  const list = q ? timezones.filter((z) => z.toLowerCase().includes(q)) : timezones
  // Capped: the list is a few hundred long and nobody scrolls it — the search
  // box is how you get to a zone.
  return list.slice(0, 200)
})

const tip = computed(() => `Timezone: ${activeTZ.value}`)

async function toggle() {
  open.value = !open.value
  if (!open.value) return
  query.value = ''
  await nextTick()
  input.value?.focus()
}

function choose(tz: string) {
  setTimezone(tz)
  open.value = false
}

function onDocClick(e: MouseEvent) {
  if (root.value && !root.value.contains(e.target as Node)) open.value = false
}

watch(open, (isOpen) => {
  if (isOpen) document.addEventListener('mousedown', onDocClick)
  else document.removeEventListener('mousedown', onDocClick)
})
</script>

<template>
  <div ref="root" class="tz">
    <button type="button" class="rail-btn" :aria-label="tip" :aria-expanded="open" @click="toggle">
      <span class="abbr">{{ tzAbbrev }}</span>
      <span class="tip">{{ tip }}</span>
    </button>

    <div v-if="open" class="pop">
      <input
        ref="input"
        v-model="query"
        type="text"
        placeholder="Search timezone…"
        aria-label="Search timezone"
        @keydown.esc.prevent="open = false"
      />

      <ul role="listbox">
        <!-- The two an operator actually wants, pinned. "Browser" follows the
             machine, so someone who travels is not left on a stale zone. -->
        <li v-if="!query" :class="{ on: timezone === '' }" @click="choose('')">
          Browser <span class="muted">({{ browserTZ }})</span>
        </li>
        <li v-if="!query" :class="{ on: timezone === 'UTC' }" @click="choose('UTC')">UTC</li>

        <li v-for="z in matches" :key="z" :class="{ on: timezone === z }" @click="choose(z)">
          {{ z.replace(/_/g, ' ') }}
        </li>

        <li v-if="!matches.length" class="muted">No match</li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.tz { position: relative; }

/* Abbreviations run from "UTC" to "GMT+13", so they get the rail's full width
   and smaller type rather than an ellipsis — "GMT…" tells an operator nothing. */
.abbr {
  max-width: 44px;
  overflow: hidden;
  font-size: 11px;
  font-weight: 600;
  white-space: nowrap;
}

.pop {
  position: absolute;
  left: calc(100% + 6px);
  bottom: 0;
  z-index: 40;
  width: 260px;
  padding: 6px;
  background: var(--bg-raised);
  border: 1px solid var(--border);
  border-radius: 8px;
  box-shadow: 0 8px 24px rgb(0 0 0 / 25%);
}

.pop input { width: 100%; }

ul {
  max-height: 320px;
  margin: 6px 0 0;
  padding: 0;
  overflow-y: auto;
  list-style: none;
}

li {
  padding: 3px 6px;
  border-radius: 4px;
  font-size: 12px;
  cursor: pointer;
}

li:hover { background: var(--hover); }
li.on { background: var(--accent-soft); color: var(--accent); }
</style>
