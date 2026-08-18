<script setup lang="ts">
// The signed-in user, at the foot of the rail. Rendered only when
// authentication is on — an avatar for "anonymous" would say nothing.
import { computed, ref, watch } from 'vue'
import type { User } from '../types'

const props = defineProps<{ user: User }>()
const emit = defineEmits<{ 'sign-out': [] }>()

const open = ref(false)
const root = ref<HTMLElement | null>(null)

const label = computed(() => props.user.name || props.user.email || props.user.sub)

const initials = computed(() =>
  label.value
    .split(/[\s@._-]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((w) => w[0]?.toUpperCase() ?? '')
    .join(''),
)

function onDocClick(e: MouseEvent) {
  if (root.value && !root.value.contains(e.target as Node)) open.value = false
}

watch(open, (isOpen) => {
  if (isOpen) document.addEventListener('mousedown', onDocClick)
  else document.removeEventListener('mousedown', onDocClick)
})
</script>

<template>
  <div ref="root" class="user">
    <button type="button" class="rail-btn" :aria-label="label" :aria-expanded="open" @click="open = !open">
      <span class="ini">{{ initials }}</span>
      <span class="tip">{{ label }}</span>
    </button>

    <div v-if="open" class="pop">
      <div class="who">
        <strong>{{ user.name || user.email || user.sub }}</strong>
        <span v-if="user.email && user.name" class="muted">{{ user.email }}</span>
      </div>
      <button type="button" class="out" @click="emit('sign-out')">Sign out</button>
    </div>
  </div>
</template>

<style scoped>
.user { position: relative; }

.ini {
  display: grid;
  place-items: center;
  width: 26px;
  height: 26px;
  border-radius: 50%;
  background: var(--accent);
  color: #fff;
  font-size: 11px;
  font-weight: 700;
}

.pop {
  position: absolute;
  left: calc(100% + 6px);
  bottom: 0;
  z-index: 40;
  width: 220px;
  padding: 8px;
  background: var(--bg-raised);
  border: 1px solid var(--border);
  border-radius: 8px;
  box-shadow: 0 8px 24px rgb(0 0 0 / 25%);
}

.who {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding-bottom: 8px;
  font-size: 12px;
  word-break: break-word;
}

.out { width: 100%; }
</style>
