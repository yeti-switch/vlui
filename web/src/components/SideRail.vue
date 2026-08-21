<script setup lang="ts">
// The left rail: everything that is about the session rather than about the
// query. Icon-width by design — the query line and the table are the page, and
// these are the controls you touch once a day.
import { iconBody } from '../icons'
import type { Tool, User } from '../types'
import ThemeToggle from './ThemeToggle.vue'
import TimezoneSelect from './TimezoneSelect.vue'
import UserMenu from './UserMenu.vue'

defineProps<{
  version: string
  commit: string
  authEnabled: boolean
  user: User | null
  tools: Tool[]
  activeTool: string
}>()

const emit = defineEmits<{
  'sign-out': []
  'select-tool': [string]
}>()
</script>

<template>
  <nav class="rail" aria-label="Preferences">
    <span class="logo" title="vlui — VictoriaLogs">
      <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true" fill="none"
           stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
        <path d="M4 5h16M4 12h10M4 19h13" />
        <circle cx="18.5" cy="12" r="2.2" />
      </svg>
    </span>

    <!-- One icon per configured tool. A deployment that configures none gets
         the rail exactly as it was. -->
    <button
      v-for="t in tools"
      :key="t.id"
      type="button"
      class="rail-btn tool"
      :class="{ active: t.id === activeTool }"
      :aria-label="t.tooltip"
      :aria-current="t.id === activeTool ? 'true' : undefined"
      @click="emit('select-tool', t.id)"
    >
      <!-- Letters where a shape would not say enough: past three or four tools
           the icons stop being distinguishable, while "API" needs no legend. -->
      <span v-if="t.letters" class="letters" :class="`len-${[...t.letters].length}`">{{ t.letters }}</span>
      <svg v-else viewBox="0 0 24 24" width="19" height="19" fill="none" stroke="currentColor"
           stroke-width="1.7" aria-hidden="true" v-html="iconBody(t.icon)"></svg>
      <span class="tip">
        {{ t.tooltip }}
        <span v-if="t.query" class="tip-query">{{ t.query }}</span>
      </span>
    </button>

    <div class="prefs">
      <TimezoneSelect />
      <ThemeToggle />
      <UserMenu v-if="authEnabled && user" :user="user" @sign-out="emit('sign-out')" />

      <!-- The rail is 48px, so only the version fits; the commit is in the
           hover label, which is where you want it when you are diffing a
           deployment against a build. -->
      <span class="build" :title="commit ? `${version} (${commit})` : version">{{ version }}</span>
    </div>
  </nav>
</template>

<style scoped>
.rail {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;

  /* 48px: the narrowest this can be without shrinking what is in it. The
     controls are 40px hit targets and the timezone abbreviation runs to
     "GMT+13" at 44px, so those two set the floor. */
  flex: 0 0 48px;
  width: 48px;
  padding: 10px 0;
  background: var(--rail);
  border-right: 1px solid var(--border);
}

.logo {
  display: grid;
  place-items: center;
  width: 40px;
  height: 40px;
  margin-bottom: 4px;
  color: var(--accent);
}

/* The selected tool is filled rather than tinted: it says what every query on
   the screen is scoped to, which is worth more than subtlety. */
.tool.active,
.tool.active:hover {
  background: var(--accent);
  color: #fff;
}

/* Letters in place of an icon. Sized by how many there are: three characters at
   the size of one would spill out of a 40px button, and shrinking all of them
   to fit the worst case would make "DB" needlessly small. */
.letters {
  font-weight: 700;
  letter-spacing: -0.02em;
  font-variant-numeric: tabular-nums;
}

.len-1 { font-size: 17px; }
.len-2 { font-size: 14px; }
.len-3 { font-size: 11px; letter-spacing: -0.04em; }

/* The filter under the name, so hovering answers "what does this actually
   select?" without a trip to the config file. */
.tip-query {
  display: block;
  margin-top: 2px;
  font-family: var(--mono);
  font-size: 11px;
  opacity: 0.8;
}

/* Pinned to the foot, out of the way of the query line. */
.prefs {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
  margin-top: auto;
}

.build {
  max-width: 46px;
  padding-top: 4px;
  overflow: hidden;
  color: var(--text-dim);
  font-size: 10px;
  font-variant-numeric: tabular-nums;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: default;
}
</style>
