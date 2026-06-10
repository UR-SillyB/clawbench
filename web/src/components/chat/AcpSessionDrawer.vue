<template>
  <BottomSheet :open="open" auto :title="t('chat.acpSession.title')" @close="$emit('close')">
    <template #header>
      <RotateCwIcon v-if="acpSessionsLoading" :size="16" class="bs-header-icon spin" />
      <HistoryIcon v-else :size="16" class="bs-header-icon" />
      <span class="bs-header-title">{{ t('chat.acpSession.title') }}</span>
    </template>
    <div class="acp-session-list">
      <div v-if="acpSessionsLoading && acpSessions.length === 0" class="acp-session-empty">
        {{ t('chat.acpSession.loading') }}
      </div>
      <div v-else-if="acpSessionsNotSupported" class="acp-session-empty">
        {{ t('chat.acpSession.notSupported') }}
      </div>
      <div v-else-if="acpSessions.length === 0" class="acp-session-empty">
        {{ t('chat.acpSession.empty') }}
      </div>
      <div
        v-for="session in acpSessions"
        :key="session.sessionId"
        class="acp-session-item"
        :class="{ 'acp-session-item--loading': acpResuming }"
        @click="handleSelect(session)"
      >
        <div class="acp-session-item-title">{{ session.title || t('chat.acpSession.untitled') }}</div>
        <div class="acp-session-item-meta">
          <span v-if="session.updatedAt">{{ formatTime(session.updatedAt) }}</span>
          <span class="acp-session-item-id">{{ session.sessionId.slice(0, 8) }}</span>
        </div>
      </div>
      <button
        v-if="nextCursor && !acpSessionsLoading"
        class="acp-session-more"
        @click="loadMore"
      >
        {{ t('chat.acpSession.loadMore') }}
      </button>
    </div>
  </BottomSheet>
</template>

<script setup lang="ts">
import { watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { History as HistoryIcon, RotateCw as RotateCwIcon } from 'lucide-vue-next'
import BottomSheet from '@/components/common/BottomSheet.vue'
import { useAcpSession, type AcpSessionInfo } from '@/composables/useAcpSession'
import { currentAgentId } from '@/composables/useSessionIdentity'

const props = defineProps<{
  open: boolean
  agentId: string
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'select', sessionId: string): void
}>()

const { t } = useI18n()

const {
  acpSessions,
  acpSessionsLoading,
  acpResuming,
  acpSessionsNotSupported,
  nextCursor,
  loadAcpSessions,
  acpLoadSession,
} = useAcpSession({ currentAgentId })

// Load sessions when drawer opens
watch(() => props.open, (val) => {
  if (val && props.agentId) {
    loadAcpSessions(props.agentId)
  }
})

async function handleSelect(session: AcpSessionInfo) {
  if (acpResuming.value) return
  const sessionId = await acpLoadSession(session.sessionId)
  if (sessionId) {
    emit('select', sessionId)
    emit('close')
  }
}

function loadMore() {
  loadAcpSessions(props.agentId, true)
}

function formatTime(iso: string): string {
  try {
    const d = new Date(iso)
    const now = new Date()
    const diffMs = now.getTime() - d.getTime()
    const diffMin = Math.floor(diffMs / 60000)
    if (diffMin < 1) return t('chat.acpSession.justNow')
    if (diffMin < 60) return t('chat.acpSession.minutesAgo', { n: diffMin })
    const diffH = Math.floor(diffMin / 60)
    if (diffH < 24) return t('chat.acpSession.hoursAgo', { n: diffH })
    const diffD = Math.floor(diffH / 24)
    if (diffD < 30) return t('chat.acpSession.daysAgo', { n: diffD })
    return d.toLocaleDateString()
  } catch {
    return iso
  }
}
</script>

<style scoped>
.acp-session-list {
  padding: 0 8px 16px;
  max-height: 60vh;
  overflow-y: auto;
}
.acp-session-empty {
  padding: 24px 16px;
  text-align: center;
  color: var(--color-text-secondary, #888);
  font-size: 14px;
}
.acp-session-item {
  padding: 12px 16px;
  border-radius: 8px;
  cursor: pointer;
  transition: background 0.15s;
}
.acp-session-item:hover {
  background: var(--color-bg-hover, rgba(0,0,0,0.04));
}
.acp-session-item--loading {
  opacity: 0.5;
  pointer-events: none;
}
.acp-session-item-title {
  font-size: 14px;
  font-weight: 500;
  line-height: 1.4;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.acp-session-item-meta {
  display: flex;
  gap: 8px;
  margin-top: 4px;
  font-size: 12px;
  color: var(--color-text-secondary, #888);
}
.acp-session-item-id {
  font-family: monospace;
  opacity: 0.6;
}
.acp-session-more {
  display: block;
  width: 100%;
  padding: 10px;
  margin-top: 8px;
  border: none;
  border-radius: 8px;
  background: var(--color-bg-hover, rgba(0,0,0,0.04));
  cursor: pointer;
  font-size: 13px;
  color: var(--color-text-secondary, #888);
}
.acp-session-more:hover {
  background: var(--color-bg-active, rgba(0,0,0,0.08));
}
.spin {
  animation: spin 1s linear infinite;
}
@keyframes spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}
</style>
