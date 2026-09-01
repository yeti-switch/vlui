<script setup lang="ts">
/* Every icon a tool may name, drawn the way the rail draws it.
 *
 * A reference sheet rather than a feature: `icon:` takes one of a dozen names,
 * the config refuses anything else at startup, and the alternative to this page
 * is reading icons.ts or guessing and restarting. It is not linked from
 * anywhere — whoever needs it is editing a config file and has been told the
 * address, or has hit the startup error, which names it.
 *
 * Behind the same login as the rest of the app — App.vue hands the window over
 * only once the server has said who this is — but in front of nothing else: no
 * query, and no VictoriaLogs. Somebody editing tool config is often doing it
 * because the deployment is not otherwise happy, and a reference sheet that
 * needed the logs to be up would be missing exactly then.
 */
import { ref } from 'vue'
import { ICONS } from '../icons'

const names = Object.keys(ICONS)

// Back to the app, wherever it is mounted. The <base href> the server injects
// already knows, so a relative link needs nothing else.
const home = new URL('.', document.baseURI).pathname

const copied = ref('')

// The whole YAML line rather than the bare name: what somebody wants from this
// page is something to paste into a tool.
async function copy(name: string) {
  try {
    await navigator.clipboard.writeText(`icon: ${name}`)
    copied.value = name
    window.setTimeout(() => (copied.value = ''), 1200)
  } catch {
    // Refused on an insecure origin. The name is on screen either way, which is
    // what the page is for.
  }
}
</script>

<template>
  <div class="icons">
    <header>
      <h1>Tool icons</h1>
      <a :href="home">← back to logs</a>
    </header>

    <p class="lead muted">
      The {{ names.length }} names <code>icon:</code> accepts, drawn at the size and weight the rail
      draws them. Click one to copy its line. A tool carries either an
      <code>icon:</code> or up to three <code>letters:</code> — never both.
    </p>

    <ul>
      <li v-for="name in names" :key="name">
        <button type="button" :title="`Copy \`icon: ${name}\``" @click="copy(name)">
          <!-- Same viewBox, size, stroke and currentColor as SideRail: a sheet
               that flattered the shapes would be a sheet that lies. -->
          <span class="tile">
            <svg
              viewBox="0 0 24 24"
              width="19"
              height="19"
              fill="none"
              stroke="currentColor"
              stroke-width="1.7"
              aria-hidden="true"
              v-html="ICONS[name]"
            ></svg>
          </span>
          <code>{{ name }}</code>
          <span class="note">{{ copied === name ? 'copied' : '' }}</span>
        </button>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.icons {
  height: 100%;
  overflow: auto;
  padding: 24px 28px 48px;
  background: var(--bg);
  color: var(--text);
}

header {
  display: flex;
  align-items: baseline;
  gap: 12px;
}

h1 {
  margin: 0;
  font-size: 18px;
}

a {
  color: var(--accent);
  text-decoration: none;
}

a:hover {
  text-decoration: underline;
}

.lead {
  max-width: 62ch;
  margin: 8px 0 20px;
  line-height: 1.5;
}

code {
  font-family: var(--mono);
  font-size: 12px;
}

ul {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

button {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 6px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg-raised);
  color: inherit;
  cursor: pointer;
  text-align: left;
}

button:hover {
  border-color: var(--border-strong);
  background: var(--hover);
}

/* The rail's own button: 40px, rounded, dim until it is the active one. Same
   metrics, so what is on this page is what lands in the rail. */
.tile {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 40px;
  height: 40px;
  flex: none;
  border-radius: 8px;
  background: var(--rail);
  color: var(--text-dim);
}

button:hover .tile {
  color: var(--text);
}

.note {
  margin-left: auto;
  font-size: 11px;
  color: var(--ok);
}
</style>
