<script setup lang="ts">
// One button that cycles Light -> Dark -> System.
//
// The icon shows the *mode*, not the resolved theme: with three states you need
// to know which one you are in, and a sun would be ambiguous between "Light"
// and "System, which happens to be light". The label names the current mode
// and what a click will do, so the cycle is never a guess.
import { computed } from 'vue'
import { mode, resolved, setMode, type Mode } from '../theme'

const ORDER: Mode[] = ['light', 'dark', 'auto']
const LABEL: Record<Mode, string> = { light: 'Light', dark: 'Dark', auto: 'System' }

const next = computed<Mode>(() => ORDER[(ORDER.indexOf(mode.value) + 1) % ORDER.length]!)

const current = computed(() =>
  mode.value === 'auto' ? `System (${LABEL[resolved.value].toLowerCase()})` : LABEL[mode.value],
)

const tip = computed(() => `Theme: ${current.value} — click for ${LABEL[next.value]}`)
</script>

<template>
  <!-- No native title: it would fire a second, duplicate tooltip alongside the
       styled one below. -->
  <button type="button" class="rail-btn theme" :aria-label="tip" @click="setMode(next)">
    <svg v-if="mode === 'light'" viewBox="0 0 24 24" width="19" height="19" fill="none"
         stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
    </svg>

    <svg v-else-if="mode === 'dark'" viewBox="0 0 24 24" width="19" height="19" fill="none"
         stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
      <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8Z" />
    </svg>

    <!-- System: a monitor, so it reads as "whatever the machine says" rather
         than as a third colour choice. -->
    <svg v-else viewBox="0 0 24 24" width="19" height="19" fill="none"
         stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
      <rect x="2.5" y="4" width="19" height="12.5" rx="1.6" />
      <path d="M8.5 20.5h7M12 16.5v4" />
    </svg>

    <span class="tip">{{ tip }}</span>
  </button>
</template>
