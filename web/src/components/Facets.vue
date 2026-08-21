<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { Facet } from '../types'

const props = defineProps<{
  facets: Facet[]
  loading: boolean
  columns: string[]
  // Header labels by field name, from the tool's config. Shown ALONGSIDE the
  // real name here rather than instead of it: this panel is where somebody
  // comes to find out what a column actually is, and the short name that suits
  // a header would hide exactly that.
  labels: Record<string, string>
  tailing: boolean
  open: boolean
  // Whether this tool's columns have been changed from the configured set. The
  // reset is offered only then: a button that does nothing is worse than no
  // button, because it invites the question of what it did.
  columnsChanged: boolean
}>()

const emit = defineEmits<{
  filter: [{ field: string; value: string; negate: boolean }]
  'toggle-column': [string]
  'toggle-panel': []
  'reset-columns': []
}>()

// Per-field collapse, keyed by name rather than by index, so the open/closed
// state survives a refresh that reorders or drops fields. Separate from the
// panel's own open/closed, which the parent owns because it decides whether the
// facets are worth fetching at all.
const collapsed = ref<Set<string>>(new Set())

function toggle(field: string) {
  const next = new Set(collapsed.value)
  if (!next.delete(field)) next.add(field)
  collapsed.value = next
}

function isColumn(field: string): boolean {
  return props.columns.includes(field)
}

/* Searching the panel.
 *
 * A log line in a real deployment carries dozens of fields — every tag the
 * producer set, every label the collector added — so the panel is a long
 * scroll, and finding `kubernetes_pod_name` in it is the sort of thing people
 * give up on.
 *
 * The match is on the FIELD NAME only. Searching the values as well was
 * tempting and is the wrong thing: the panel lists the top few values per
 * field, so a value search would answer from a sample and quietly miss the
 * value that is there but not in the top ten. The query line searches values,
 * against all of them, which is where that belongs. */
const search = ref('')

const shown = computed(() => {
  const needle = search.value.trim().toLowerCase()
  if (!needle) return props.facets
  return props.facets.filter((f) => f.field_name.toLowerCase().includes(needle))
})

// While searching, everything matching is expanded: a hit that is still
// collapsed looks like the search failed.
const searching = computed(() => search.value.trim() !== '')

function isCollapsed(field: string): boolean {
  return !searching.value && collapsed.value.has(field)
}

// Clearing the box leaves the panel as the search found it, not as it was
// before — the reader has been looking at these fields and expanding one they
// had collapsed is not a surprise worth avoiding.
watch(
  () => props.facets,
  () => {
    if (!props.facets.length) search.value = ''
  },
)
</script>

<template>
  <aside class="facets" :class="{ closed: !open }">
    <!-- Closed, the panel is a rail rather than nothing at all: a sidebar that
         vanished entirely would leave no way back to it. -->
    <button v-if="!open" type="button" class="rail" title="Show fields" @click="emit('toggle-panel')">
      <span class="rail-text">▸ Fields</span>
    </button>

    <template v-else>
      <header>
        <button type="button" class="ghost collapse" title="Hide fields" @click="emit('toggle-panel')">◂</button>
        <span>Fields</span>
        <span v-if="loading" class="muted">…</span>

        <!-- Here rather than on the table: this panel is where columns are
             added and removed, so it is where undoing that belongs. -->
        <button
          v-if="columnsChanged"
          type="button"
          class="ghost reset"
          title="Forget the columns chosen here and show the ones this tool is configured with"
          @click="emit('reset-columns')"
        >
          reset columns
        </button>
      </header>

      <div v-if="facets.length" class="find">
        <input
          v-model="search"
          type="text"
          class="mono"
          placeholder="find a field…"
          aria-label="Find a field"
          spellcheck="false"
          autocomplete="off"
          @keydown.esc.prevent="search = ''"
        />
        <button v-if="search" type="button" class="ghost clear" title="Clear" @click="search = ''">×</button>
      </div>

      <p v-if="tailing" class="empty muted">
        Facets describe a fixed window, so they pause while you are following.
      </p>
      <p v-else-if="!loading && !facets.length" class="empty muted">
        Run a query to see which fields the matching logs carry.
      </p>

      <p v-if="searching && !shown.length" class="empty muted">
        No field matching “{{ search }}”.
      </p>

      <section v-for="f in shown" :key="f.field_name">
        <h3 @click="toggle(f.field_name)">
          <span class="caret">{{ isCollapsed(f.field_name) ? '▸' : '▾' }}</span>
          <span class="name mono">{{ f.field_name }}</span>
          <span v-if="labels[f.field_name]" class="label" :title="`shown in the table as ${labels[f.field_name]}`">{{ labels[f.field_name] }}</span>
          <button
            type="button"
            class="ghost col"
            :class="{ on: isColumn(f.field_name) }"
            :title="isColumn(f.field_name) ? 'Remove this column' : 'Show as a column'"
            @click.stop="emit('toggle-column', f.field_name)"
          >
            ▦
          </button>
        </h3>

        <ul v-if="!isCollapsed(f.field_name)">
          <li v-for="v in f.values" :key="v.field_value">
            <button
              type="button"
              class="ghost value mono"
              :title="`Filter to ${f.field_name}:${v.field_value}`"
              @click="emit('filter', { field: f.field_name, value: v.field_value, negate: false })"
            >
              {{ v.field_value === '' ? '(empty)' : v.field_value }}
            </button>
            <span class="muted count">{{ v.hits.toLocaleString() }}</span>
            <button
              type="button"
              class="ghost negate"
              title="Exclude this value"
              @click="emit('filter', { field: f.field_name, value: v.field_value, negate: true })"
            >
              −
            </button>
          </li>
        </ul>
      </section>
    </template>
  </aside>
</template>

<style scoped>
.facets {
  width: 260px;
  flex: none;
  overflow-y: auto;
  border-right: 1px solid var(--border);
  background: var(--bg-sunken);
  padding-bottom: 20px;
}

/* Closed, the panel is a 26px rail carrying its own way back. No width
   transition: the table beside it re-measures on resize, and animating the
   width would make it do that on every frame of the animation. */
.facets.closed {
  width: 26px;
  overflow: hidden;
  padding-bottom: 0;
}

.rail {
  display: flex;
  /* Pinned to the top, where the panel's own header would be: centred in the
     full height it reads as a stray label rather than as the way back. */
  align-items: flex-start;
  justify-content: center;
  width: 100%;
  height: 100%;
  padding: 8px 0;
  border: none;
  border-radius: 0;
  background: transparent;
  color: var(--text-dim);
}

.rail:hover { background: var(--bg-raised); color: var(--text); }

.rail-text {
  /* Vertical, so the label stays readable in a rail narrower than the word. */
  writing-mode: vertical-rl;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.04em;
  white-space: nowrap;
}

.collapse { padding: 0 4px; font-size: 12px; }

header {
  position: sticky;
  top: 0;
  display: flex;
  align-items: center;
  gap: 6px;
  /* The collapse control, then the title. Not space-between: with the loading
     indicator as a third child that pushes the title to the far side, away
     from the button it belongs to. */
  padding: 6px 10px 6px 4px;
  background: var(--bg-sunken);
  border-bottom: 1px solid var(--border);
  font-weight: 600;
}

header .muted { margin-left: auto; }

/* Pushed to the right, and quiet: it is an escape hatch, not a thing to reach
   for. */
.reset {
  margin-left: auto;
  font-size: 11px;
  font-weight: 400;
  color: var(--text-dim);
}

.reset:hover { color: var(--accent); }

.empty { padding: 10px; font-size: 12px; }

/* Sticky under the header, so it stays reachable in a long list — which is the
   situation it exists for. */
.find {
  position: sticky;
  top: 29px;
  z-index: 1;
  display: flex;
  align-items: center;
  gap: 2px;
  padding: 6px 8px;
  background: var(--bg-sunken);
}

.find input {
  flex: 1;
  min-width: 0;
  padding: 3px 6px;
  font-size: 12px;
}

.clear { padding: 0 4px; }

h3 {
  display: flex;
  align-items: center;
  gap: 4px;
  margin: 0;
  padding: 6px 8px 4px;
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
}

.caret { width: 10px; color: var(--text-dim); }
.name { flex: none; overflow: hidden; text-overflow: ellipsis; }

/* The header's short name, beside the real one — so the panel explains the
   abbreviation the table shows rather than repeating it. */
.label {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  color: var(--text-dim);
  font-family: var(--mono);
  font-size: 11px;
  font-weight: 400;
  text-overflow: ellipsis;
}

.label::before { content: "· "; }

.col { opacity: 0.4; }
.col.on { opacity: 1; color: var(--accent); }
h3:hover .col { opacity: 0.8; }
h3:hover .col.on { opacity: 1; }

ul { list-style: none; margin: 0; padding: 0 6px; }

li {
  display: flex;
  align-items: center;
  gap: 4px;
}

.value {
  flex: 1;
  min-width: 0;
  text-align: left;
  font-size: 12px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.count { font-size: 11px; font-variant-numeric: tabular-nums; }
.negate { visibility: hidden; }
li:hover .negate { visibility: visible; }
</style>
