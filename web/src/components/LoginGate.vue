<script setup lang="ts">
// Shown when authentication is on and nobody is signed in.
//
// The app renders nothing else until then — not because the SPA is a security
// boundary (it is not; the server is), but because every panel would 401 and
// the screen would fill with errors instead of saying the one useful thing.
import { begin } from '../session'
</script>

<template>
  <div class="gate">
    <div class="card">
      <svg viewBox="0 0 24 24" width="34" height="34" aria-hidden="true" fill="none"
           stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
        <path d="M4 5h16M4 12h10M4 19h13" />
        <circle cx="18.5" cy="12" r="2.2" />
      </svg>

      <h1>vlui</h1>
      <p class="muted">VictoriaLogs</p>

      <button type="button" class="primary" autofocus @click="begin">Log in</button>

      <p class="muted note">You will be sent to your identity provider.</p>
    </div>
  </div>
</template>

<style scoped>
.gate {
  display: grid;
  place-items: center;
  width: 100%;
  height: 100%;
  background: var(--bg-sunken);
}

.card {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  padding: 34px 44px;
  background: var(--bg-raised);
  border: 1px solid var(--border);
  border-radius: 10px;
  box-shadow: 0 10px 30px rgb(0 0 0 / 18%);
  color: var(--accent);
}

h1 {
  margin: 8px 0 0;
  font-size: 19px;
  color: var(--text);
}

p { margin: 0; font-size: 12px; }

button {
  margin-top: 18px;
  padding: 8px 26px;
  font-size: 14px;
  font-weight: 600;
}

.note { margin-top: 12px; font-size: 11px; }
</style>
