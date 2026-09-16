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
        <path d="M4 5h16M4 12h10" />
        <!-- Blue over yellow: the flag of Ukraine. -->
        <path d="M4 19h13" stroke="#ffd700" />
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

      <!-- Last of all, the version. The rail is only 48px wide, so only the
           number fits; its hover label carries the flag and the line under it —
           this is a Ukrainian project. Blue over yellow in the light theme; in
           the dark theme it turns red over black (the colours are in the CSS
           below, where the theme is). The commit, when the build has one, is
           the line after that: it is where you want it when you are diffing a
           deployment against a build. -->
      <span class="build" aria-label="Made in Ukraine">
        {{ version }}
        <span class="tip">
          <span class="made">
            Made in Ukraine
            <svg class="flag" viewBox="0 0 24 16" width="18" height="12" aria-hidden="true">
              <rect class="upper" width="24" height="8" />
              <rect class="lower" y="8" width="24" height="8" />
            </svg>
          </span>
          <span v-if="commit" class="tip-commit">{{ commit }}</span>
        </span>
      </span>
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
   select?" without a trip to the config file. The commit under the version is
   set the same way. */
.tip-query,
.tip-commit {
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
  position: relative;
  max-width: 46px;
  padding: 4px 0;
  overflow: visible;
  color: var(--text-dim);
  font-size: 10px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  cursor: default;
}

/* The same label the tools get. Theirs is styled in styles.css against
   .rail-btn, which this span is not, so the placement is repeated here. */
.build .tip {
  position: absolute;
  left: calc(100% + 8px);
  top: 50%;
  transform: translateY(-50%);
  z-index: 40;
  padding: 4px 8px;
  border-radius: 4px;
  background: var(--tooltip);
  color: var(--tooltip-fg);
  font-size: 12px;
  white-space: nowrap;
  opacity: 0;
  pointer-events: none;
  transition: opacity 0.1s;
}
.build:hover .tip {
  opacity: 1;
}

/* The label's first line: the words, then the flag. */
.made {
  display: inline-flex;
  align-items: center;
  gap: 7px;
}
.flag {
  flex: none;
  display: block;
  border-radius: 2px;
  /* A hairline, so the lower band is seen against the label in either theme. */
  outline: 1px solid rgb(128 128 128 / 40%);
}
.flag .upper {
  fill: #0057b7;
}
.flag .lower {
  fill: #ffd700;
}
/* theme.ts stamps the resolved theme on <html>, so this is the dark theme and
   only the dark theme. */
:root[data-theme='dark'] .flag .upper {
  fill: #d0021b;
}
:root[data-theme='dark'] .flag .lower {
  fill: #0b0b0b;
}
</style>
