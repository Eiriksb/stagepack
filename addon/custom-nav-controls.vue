<script setup lang="ts">
// The "Export as .sptx" button in Slidev's nav bar. It shows only on `slidev` (the dev server), where
// setup/vite-plugins.ts answers; in a built site and in Stagepack's viewer there is no endpoint, so no button.
import { onMounted, ref } from 'vue'

const base = import.meta.env.BASE_URL || '/'
const shown = ref(false)
const busy = ref(false)
const message = ref('')
const isError = ref(false)

onMounted(async () => {
  if (!import.meta.env.DEV)
    return
  try {
    const r = await fetch(`${base}__stagepack/status`, { cache: 'no-store' })
    shown.value = r.ok && (r.headers.get('content-type') || '').includes('json')
  }
  catch {}
})

function tell(text: string, error = false) {
  message.value = text
  isError.value = error
}

async function exportTalk() {
  if (busy.value)
    return
  busy.value = true
  tell('Packing the talk…')
  try {
    const r = await fetch(`${base}__stagepack/export`, { method: 'POST' })
    if (!r.ok) {
      let error = `HTTP ${r.status}`
      try {
        error = (await r.json()).error || error
      }
      catch {}
      tell(error, true)
      return
    }
    const name = fileName(r.headers.get('content-disposition')) || 'talk.sptx'
    const notes = decodeURIComponent(r.headers.get('x-stagepack-notes') || '')
    const url = URL.createObjectURL(await r.blob())
    const a = document.createElement('a')
    a.href = url
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    setTimeout(() => URL.revokeObjectURL(url), 60_000)
    tell(notes ? `Saved ${name}.\n\n${notes}` : `Saved ${name}.`)
  }
  catch (e) {
    tell(String((e as Error)?.message || e), true)
  }
  finally {
    busy.value = false
  }
}

// fileName reads the name from a Content-Disposition header, preferring filename*.
function fileName(cd: string | null): string {
  if (!cd)
    return ''
  const star = /filename\*=UTF-8''([^;]+)/i.exec(cd)
  if (star) {
    try {
      return decodeURIComponent(star[1])
    }
    catch {}
  }
  return /filename="([^"]*)"/i.exec(cd)?.[1] || ''
}
</script>

<template>
  <button
    v-if="shown"
    class="slidev-icon-btn"
    :class="{ disabled: busy }"
    :title="busy ? 'Packing…' : 'Export as .sptx (Stagepack)'"
    :disabled="busy"
    @click="exportTalk"
  >
    <span class="sr-only">Export as .sptx (Stagepack)</span>
    <svg viewBox="0 0 32 32" width="1em" height="1em" fill="currentColor" aria-hidden="true">
      <path d="M26.5 9.1 16.5 3.2a1 1 0 0 0-1 0l-10 5.9a1 1 0 0 0-.5.9v12a1 1 0 0 0 .5.9l10 5.9a1 1 0 0 0 1 0l10-5.9a1 1 0 0 0 .5-.9V10a1 1 0 0 0-.5-.9ZM16 5.2l8 4.7-8 4.8-8-4.8Zm-9 6.5 8 4.8v9.4l-8-4.7Zm10 14.2v-9.4l8-4.8v9.5Z" />
    </svg>
  </button>
  <!-- in <body>: the nav bar fades out when the mouse leaves it, and the message must stay -->
  <Teleport to="body">
    <div v-if="message" class="stagepack-export-message" :class="{ error: isError }" role="status">
      <pre>{{ message }}</pre>
      <button title="Close" @click="message = ''">
        ×
      </button>
    </div>
  </Teleport>
</template>

<style scoped>
.stagepack-export-message {
  position: fixed;
  right: 1rem;
  bottom: 4rem;
  z-index: 100;
  display: flex;
  gap: 0.5rem;
  align-items: flex-start;
  max-width: min(36rem, calc(100vw - 2rem));
  max-height: 50vh;
  overflow: auto;
  padding: 0.75rem 1rem;
  border-radius: 0.5rem;
  background: #1f2937;
  color: #f9fafb;
  font-size: 0.85rem;
  box-shadow: 0 4px 16px rgb(0 0 0 / 30%);
}
.stagepack-export-message.error {
  background: #7f1d1d;
}
.stagepack-export-message pre {
  margin: 0;
  white-space: pre-wrap;
  font-family: inherit;
}
.stagepack-export-message button {
  background: none;
  border: 0;
  color: inherit;
  font-size: 1.1rem;
  line-height: 1;
  cursor: pointer;
}
</style>
