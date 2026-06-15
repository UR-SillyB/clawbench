import { ref, computed } from 'vue'
import { apiGet, apiPut } from '@/utils/api'
import { DEFAULT_KEY_IDS, DEFAULT_SYMBOL_IDS, getDef, type ConfigType, type KeyDef } from '@/utils/terminalKeyDefs'

export interface KeyConfigItem {
  id: number
  type: ConfigType
  key_id: string
  sort_order: number
}

// Module-level singleton state
const keyItems = ref<KeyConfigItem[]>([])
const symbolItems = ref<KeyConfigItem[]>([])
const loaded = ref(false)
const loading = ref(false)

export function useKeyConfig() {
  async function fetchConfig(force = false) {
    if (loaded.value && !force) return
    if (loading.value) return
    loading.value = true
    try {
      const [keys, symbols] = await Promise.all([
        apiGet<KeyConfigItem[]>('/api/terminal/key-config?type=key'),
        apiGet<KeyConfigItem[]>('/api/terminal/key-config?type=symbol'),
      ])
      keyItems.value = keys || []
      symbolItems.value = symbols || []
      // Seed defaults if empty (first use)
      if (keyItems.value.length === 0) {
        await saveConfig('key', DEFAULT_KEY_IDS)
        keyItems.value = DEFAULT_KEY_IDS.map((id, i) => ({
          id: -(i + 1), type: 'key' as ConfigType, key_id: id, sort_order: i,
        }))
      }
      if (symbolItems.value.length === 0) {
        await saveConfig('symbol', DEFAULT_SYMBOL_IDS)
        symbolItems.value = DEFAULT_SYMBOL_IDS.map((id, i) => ({
          id: -(i + 1), type: 'symbol' as ConfigType, key_id: id, sort_order: i,
        }))
      }
      loaded.value = true
    } finally {
      loading.value = false
    }
  }

  async function saveConfig(type: ConfigType, items: string[]) {
    await apiPut('/api/terminal/key-config', { type, items })
  }

  const selectedKeyIds = computed(() => keyItems.value.map(i => i.key_id))
  const selectedSymbolIds = computed(() => symbolItems.value.map(i => i.key_id))

  /** Get ordered KeyDef list for rendering the toolbar */
  const selectedKeys = computed(() =>
    selectedKeyIds.value.map(id => getDef('key', id)).filter((d): d is KeyDef => d !== undefined)
  )
  const selectedSymbols = computed(() =>
    selectedSymbolIds.value.map(id => getDef('symbol', id)).filter((d): d is KeyDef => d !== undefined)
  )

  return {
    keyItems,
    symbolItems,
    loading,
    fetchConfig,
    saveConfig,
    selectedKeyIds,
    selectedSymbolIds,
    selectedKeys,
    selectedSymbols,
  }
}
