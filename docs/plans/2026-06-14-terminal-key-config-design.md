# Terminal Key Configuration Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a settings button next to the quick commands button in the terminal toolbar that opens a full-screen drawer with two tabs (Keys / Symbols), allowing users to customize which keys and symbols appear in the toolbar via click-to-select and drag-to-reorder.

**Architecture:** Backend adds a `terminal_key_config` table with per-item records (type, key_id, sort_order), served via a custom CRUD API (not the generic crudHelpers since columns differ). Frontend adds a composable `useKeyConfig` wrapping the API, a shared `KeyConfigDrawer` component with two identical tabs rendered by a shared `KeyConfigTab` sub-component, and refactors the toolbar rendering to be driven by the config data instead of hardcoded HTML.

**Tech Stack:** Go (net/http handler), SQLite, Vue 3, vuedraggable, lucide-vue-next, i18n

---

## Design Decisions (confirmed)

| # | Decision | Choice |
|---|----------|--------|
| 1 | Settings button position | Right of quick commands (ZapIcon) |
| 2 | Drawer type | Full-screen BottomSheet with two tabs |
| 3 | Selected area layout | Multi-line flow wrap |
| 4 | Selected area ordering | Free drag (vuedraggable) |
| 5 | Remove from selected | Long-press delete + click to remove from selected area (NOT drag back — simplified per user) |
| 6 | Available area interaction | Click to toggle select/deselect |
| 7 | Persistence | Backend API |
| 8 | Data model | Single API, `type` field distinguishes key/symbol, each item is an independent record |
| 9 | Apply mode | Confirm (save button) then生效 |
| 10 | Config scope | Global (all tabs shared) |
| 11 | Default selected keys | Current toolbar keys (Esc/Tab/Ctrl/Alt/Shift/⌃C/⌃Z/⌃S/Home/End/PgUp/PgDn/↑↓←→) |
| 12 | Default selected symbols | Fewer: `./-/$&;|=>` (8 symbols) |
| 13 | Empty selected area | Show placeholder guidance text |
| 14 | Key groups (Termius-style) | Modifiers / Function / Navigation / Arrows / Shortcuts / Editing |
| 15 | Symbol groups (by char type) | Punctuation / Math / Brackets / Quotes / Shell special |
| 16 | Cross-group selection | Allowed |
| 17 | Frequency sorting | Removed (replaced by this config) |
| 18 | Tab code reuse | Identical logic, shared component |

---

## Key Definitions (front-end catalog, hardcoded)

### Keys catalog (all available keys, grouped)

**Modifiers:** Esc, Tab, Ctrl, Alt, Shift, Shift+Tab
**Function:** F1-F12
**Navigation:** Home, End, PgUp, PgDn, Insert
**Arrows:** ↑, ↓, ←, →
**Shortcuts:** ⌃C (Ctrl+C), ⌃Z (Ctrl+Z), ⌃S (Ctrl+S), ⌃D (Ctrl+D), ⌃L (Ctrl+L), ⌃R (Ctrl+R)
**Editing:** Enter, Backspace, Delete

### Symbols catalog (all available symbols, grouped)

**Punctuation:** `.`, `,`, `;`, `:`, `!`, `?`
**Math:** `+`, `-`, `*`, `/`, `=`, `%`, `<`, `>`
**Brackets:** `(`, `)`, `[`, `]`, `{`, `}`
**Quotes:** `"`, `'`, `` ` ``
**Shell special:** `|`, `&`, `$`, `~`, `#`, `@`, `\`, `_`, `^`

### Key data model (front-end)

```typescript
interface KeyDef {
  id: string          // unique identifier: 'esc', 'f1', 'ctrl_c', etc.
  label: string       // display text: 'Esc', 'F1', '⌃C', etc.
  group: string       // group key: 'modifier', 'function', 'navigation', 'arrow', 'shortcut', 'editing'
  sendFn?: string     // function name on useTerminalKeys: 'sendEscape', 'sendF1', 'sendCtrlC', etc.
  char?: string       // character to send directly (for symbols)
  isModifier?: boolean // true for ctrl/alt/shift (toggle behavior, not a send action)
}
```

For symbols, `sendFn` is not used — clicking a symbol sends the character directly via `sendInput()`.

---

## Backend Schema

```sql
CREATE TABLE IF NOT EXISTS terminal_key_config (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    type TEXT NOT NULL,       -- 'key' or 'symbol'
    key_id TEXT NOT NULL,     -- matches KeyDef.id or symbol character
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(type, key_id)     -- one config entry per key/symbol
);
```

### API endpoints

| Method | Path | Body | Response |
|--------|------|------|----------|
| GET | `/api/terminal/key-config?type=key` | - | `KeyConfigItem[]` |
| GET | `/api/terminal/key-config?type=symbol` | - | `KeyConfigItem[]` |
| PUT | `/api/terminal/key-config` | `{type: string, items: string[]}` | `{success: true}` |

The PUT endpoint replaces all items of a given type (full replace, not incremental). This simplifies the "confirm to apply" flow — the frontend sends the complete ordered list on save.

**Model:**
```go
type KeyConfigItem struct {
    ID        int64  `json:"id"`
    Type      string `json:"type"`       // "key" or "symbol"
    KeyID     string `json:"key_id"`     // matches frontend KeyDef.id or symbol char
    SortOrder int    `json:"sort_order"`
}
```

---

## Frontend Composable

```typescript
// useKeyConfig.ts
// Wraps GET/PUT /api/terminal/key-config
// Provides: keyItems, symbolItems, loading, fetchConfig, saveConfig(type, items)
// On first load, if no items returned, seeds defaults
```

---

## Implementation Tasks

### Task 1: Backend — Database table & model

**Files:**
- Modify: `internal/service/database.go`

**Step 1: Add table DDL in InitDB()**

Add after the existing `terminal_quick_commands` table creation:

```go
// Terminal key configuration
_, _ = DB.Exec(`CREATE TABLE IF NOT EXISTS terminal_key_config (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    type TEXT NOT NULL,
    key_id TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(type, key_id)
)`)
```

**Step 2: Add model struct**

```go
type KeyConfigItem struct {
    ID        int64  `json:"id"`
    Type      string `json:"type"`
    KeyID     string `json:"key_id"`
    SortOrder int    `json:"sort_order"`
}
```

**Step 3: Add service functions**

```go
func GetKeyConfig(typeFilter string) ([]KeyConfigItem, error) {
    rows, err := DBRead.Query("SELECT id, type, key_id, sort_order FROM terminal_key_config WHERE type = ? ORDER BY sort_order", typeFilter)
    if err != nil {
        return nil, err
    }
    defer func() { _ = rows.Close() }()
    var items []KeyConfigItem
    for rows.Next() {
        var item KeyConfigItem
        if err := rows.Scan(&item.ID, &item.Type, &item.KeyID, &item.SortOrder); err != nil {
            return nil, err
        }
        items = append(items, item)
    }
    return items, nil
}

func ReplaceKeyConfig(typeVal string, keyIDs []string) error {
    tx, err := DB.Begin()
    if err != nil {
        return err
    }
    if _, err := tx.Exec("DELETE FROM terminal_key_config WHERE type = ?", typeVal); err != nil {
        _ = tx.Rollback()
        return err
    }
    for i, keyID := range keyIDs {
        if _, err := tx.Exec("INSERT INTO terminal_key_config (type, key_id, sort_order) VALUES (?, ?, ?)", typeVal, keyID, i); err != nil {
            _ = tx.Rollback()
            return err
        }
    }
    return tx.Commit()
}
```

**Step 4: Commit**

```bash
git add internal/service/database.go
git commit -m "feat: add terminal_key_config table, model, and service functions"
```

---

### Task 2: Backend — HTTP handler & routes

**Files:**
- Modify: `internal/handler/terminal.go`
- Modify: `internal/handler/handler.go`

**Step 1: Add handler in terminal.go**

```go
func ServeKeyConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        typeFilter := r.URL.Query().Get("type")
        if typeFilter != "key" && typeFilter != "symbol" {
            writeLocalizedErrorf(w, r, http.StatusBadRequest, "InvalidType")
            return
        }
        items, err := service.GetKeyConfig(typeFilter)
        if err != nil {
            slog.Error("failed to get key config", "error", err)
            writeLocalizedErrorf(w, r, http.StatusInternalServerError, "InternalError")
            return
        }
        if items == nil {
            items = []service.KeyConfigItem{}
        }
        writeJSON(w, http.StatusOK, items)

    case http.MethodPut:
        var req struct {
            Type  string   `json:"type"`
            Items []string `json:"items"`
        }
        if !decodeJSON(w, r, &req) {
            return
        }
        if req.Type != "key" && req.Type != "symbol" {
            writeLocalizedErrorf(w, r, http.StatusBadRequest, "InvalidType")
            return
        }
        if err := service.ReplaceKeyConfig(req.Type, req.Items); err != nil {
            slog.Error("failed to replace key config", "error", err)
            writeLocalizedErrorf(w, r, http.StatusInternalServerError, "InternalError")
            return
        }
        writeJSON(w, http.StatusOK, map[string]any{"success": true})

    default:
        writeLocalizedErrorf(w, r, http.StatusMethodNotAllowed, "MethodNotAllowed")
    }
}
```

**Step 2: Register route in handler.go**

Add in `RegisterRoutes()` after the quick-commands routes:

```go
register("/api/terminal/key-config", middleware.Auth(ServeKeyConfig))
```

**Step 3: Commit**

```bash
git add internal/handler/terminal.go internal/handler/handler.go
git commit -m "feat: add /api/terminal/key-config GET/PUT handler and route"
```

---

### Task 3: Backend — Handler test

**Files:**
- Create: `internal/handler/terminal_key_config_test.go`

**Step 1: Write test file**

```go
package handler

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "clawbench/internal/service"
)

func TestServeKeyConfig_GetEmpty(t *testing.T) {
    teardown := setupTestEnv(t)
    defer teardown()

    req := newRequest(t, http.MethodGet, "/api/terminal/key-config?type=key", nil)
    w := httptest.NewRecorder()
    ServeKeyConfig(w, req)
    assertStatus(t, w, http.StatusOK)

    var items []service.KeyConfigItem
    decodeRespJSON(t, w.Body, &items)
    if len(items) != 0 {
        t.Fatalf("expected empty, got %d items", len(items))
    }
}

func TestServeKeyConfig_PutAndGet(t *testing.T) {
    teardown := setupTestEnv(t)
    defer teardown()

    // Put key config
    body := `{"type":"key","items":["esc","tab","ctrl"]}`
    req := newRequest(t, http.MethodPut, "/api/terminal/key-config", strings.NewReader(body))
    w := httptest.NewRecorder()
    ServeKeyConfig(w, req)
    assertStatus(t, w, http.StatusOK)

    // Get key config
    req = newRequest(t, http.MethodGet, "/api/terminal/key-config?type=key", nil)
    w = httptest.NewRecorder()
    ServeKeyConfig(w, req)
    assertStatus(t, w, http.StatusOK)

    var items []service.KeyConfigItem
    decodeRespJSON(t, w.Body, &items)
    if len(items) != 3 {
        t.Fatalf("expected 3 items, got %d", len(items))
    }
    if items[0].KeyID != "esc" || items[1].KeyID != "tab" || items[2].KeyID != "ctrl" {
        t.Fatalf("unexpected order: %+v", items)
    }
}

func TestServeKeyConfig_InvalidType(t *testing.T) {
    teardown := setupTestEnv(t)
    defer teardown()

    req := newRequest(t, http.MethodGet, "/api/terminal/key-config?type=invalid", nil)
    w := httptest.NewRecorder()
    ServeKeyConfig(w, req)
    assertStatus(t, w, http.StatusBadRequest)
}

func TestServeKeyConfig_Replace(t *testing.T) {
    teardown := setupTestEnv(t)
    defer teardown()

    // Initial config
    body := `{"type":"symbol","items":[".","/","-"]}`
    req := newRequest(t, http.MethodPut, "/api/terminal/key-config", strings.NewReader(body))
    w := httptest.NewRecorder()
    ServeKeyConfig(w, req)

    // Replace with different config
    body = `{"type":"symbol","items":["$","&"]}`
    req = newRequest(t, http.MethodPut, "/api/terminal/key-config", strings.NewReader(body))
    w = httptest.NewRecorder()
    ServeKeyConfig(w, req)

    // Get should show only new config
    req = newRequest(t, http.MethodGet, "/api/terminal/key-config?type=symbol", nil)
    w = httptest.NewRecorder()
    ServeKeyConfig(w, req)

    var items []service.KeyConfigItem
    decodeRespJSON(t, w.Body, &items)
    if len(items) != 2 {
        t.Fatalf("expected 2 items after replace, got %d", len(items))
    }
}
```

**Step 2: Run tests**

```bash
cd /home/xulongzhe/projects/clawbench && go test ./internal/handler/ -run TestServeKeyConfig -v
```

**Step 3: Commit**

```bash
git add internal/handler/terminal_key_config_test.go
git commit -m "test: add terminal key config handler tests"
```

---

### Task 4: Frontend — Key definitions catalog

**Files:**
- Create: `web/src/utils/terminalKeyDefs.ts`

**Step 1: Create the key definitions file**

```typescript
/**
 * Terminal key definitions catalog.
 * All available keys and symbols with their groups, display labels, and send functions.
 * This is the source of truth for what keys/symbols exist in the system.
 */

export interface KeyDef {
  id: string
  label: string
  group: KeyGroup
  sendFn?: string       // function name on useTerminalKeys (for keys)
  char?: string         // character to send (for symbols)
  isModifier?: boolean  // true for ctrl/alt/shift (toggle behavior)
}

export type KeyGroup = 'modifier' | 'function' | 'navigation' | 'arrow' | 'shortcut' | 'editing'
export type SymbolGroup = 'punctuation' | 'math' | 'bracket' | 'quote' | 'shell'

export type ConfigType = 'key' | 'symbol'

export interface KeyGroupInfo {
  key: KeyGroup | SymbolGroup
  label: string  // i18n key
}

// Key groups (Termius-style)
export const KEY_GROUPS: KeyGroupInfo[] = [
  { key: 'modifier', label: 'terminal.keyGroupModifier' },
  { key: 'function', label: 'terminal.keyGroupFunction' },
  { key: 'navigation', label: 'terminal.keyGroupNavigation' },
  { key: 'arrow', label: 'terminal.keyGroupArrow' },
  { key: 'shortcut', label: 'terminal.keyGroupShortcut' },
  { key: 'editing', label: 'terminal.keyGroupEditing' },
]

// Symbol groups (by char type)
export const SYMBOL_GROUPS: KeyGroupInfo[] = [
  { key: 'punctuation', label: 'terminal.symbolGroupPunctuation' },
  { key: 'math', label: 'terminal.symbolGroupMath' },
  { key: 'bracket', label: 'terminal.symbolGroupBracket' },
  { key: 'quote', label: 'terminal.symbolGroupQuote' },
  { key: 'shell', label: 'terminal.symbolGroupShell' },
]

// All available keys
export const ALL_KEYS: KeyDef[] = [
  // Modifiers
  { id: 'esc', label: 'Esc', group: 'modifier', sendFn: 'sendEscape' },
  { id: 'tab', label: 'Tab', group: 'modifier', sendFn: 'sendTab' },
  { id: 'ctrl', label: 'Ctrl', group: 'modifier', isModifier: true },
  { id: 'alt', label: 'Alt', group: 'modifier', isModifier: true },
  { id: 'shift', label: 'Shift', group: 'modifier', isModifier: true },
  { id: 'shift_tab', label: '⇧Tab', group: 'modifier', sendFn: 'sendShiftTab' },
  // Function keys
  { id: 'f1', label: 'F1', group: 'function', sendFn: 'sendF1' },
  { id: 'f2', label: 'F2', group: 'function', sendFn: 'sendF2' },
  { id: 'f3', label: 'F3', group: 'function', sendFn: 'sendF3' },
  { id: 'f4', label: 'F4', group: 'function', sendFn: 'sendF4' },
  { id: 'f5', label: 'F5', group: 'function', sendFn: 'sendF5' },
  { id: 'f6', label: 'F6', group: 'function', sendFn: 'sendF6' },
  { id: 'f7', label: 'F7', group: 'function', sendFn: 'sendF7' },
  { id: 'f8', label: 'F8', group: 'function', sendFn: 'sendF8' },
  { id: 'f9', label: 'F9', group: 'function', sendFn: 'sendF9' },
  { id: 'f10', label: 'F10', group: 'function', sendFn: 'sendF10' },
  { id: 'f11', label: 'F11', group: 'function', sendFn: 'sendF11' },
  { id: 'f12', label: 'F12', group: 'function', sendFn: 'sendF12' },
  // Navigation
  { id: 'home', label: 'Home', group: 'navigation', sendFn: 'sendHome' },
  { id: 'end', label: 'End', group: 'navigation', sendFn: 'sendEnd' },
  { id: 'pgup', label: 'PgUp', group: 'navigation', sendFn: 'sendPageUp' },
  { id: 'pgdn', label: 'PgDn', group: 'navigation', sendFn: 'sendPageDown' },
  { id: 'insert', label: 'Ins', group: 'navigation', sendFn: 'sendInsert' },
  // Arrows
  { id: 'arrow_up', label: '↑', group: 'arrow', sendFn: 'sendArrowUp' },
  { id: 'arrow_down', label: '↓', group: 'arrow', sendFn: 'sendArrowDown' },
  { id: 'arrow_left', label: '←', group: 'arrow', sendFn: 'sendArrowLeft' },
  { id: 'arrow_right', label: '→', group: 'arrow', sendFn: 'sendArrowRight' },
  // Shortcuts
  { id: 'ctrl_c', label: '⌃C', group: 'shortcut', sendFn: 'sendCtrlC' },
  { id: 'ctrl_z', label: '⌃Z', group: 'shortcut', sendFn: 'sendCtrlZ' },
  { id: 'ctrl_s', label: '⌃S', group: 'shortcut', sendFn: 'sendCtrlS' },
  { id: 'ctrl_d', label: '⌃D', group: 'shortcut', sendFn: 'sendCtrlD' },
  { id: 'ctrl_l', label: '⌃L', group: 'shortcut', sendFn: 'sendCtrlL' },
  { id: 'ctrl_r', label: '⌃R', group: 'shortcut', sendFn: 'sendCtrlR' },
  // Editing
  { id: 'enter', label: 'Enter', group: 'editing', sendFn: 'sendEnter' },
  { id: 'backspace', label: '⌫', group: 'editing', sendFn: 'sendBackspace' },
  { id: 'delete', label: 'Del', group: 'editing', sendFn: 'sendDelete' },
]

// All available symbols
export const ALL_SYMBOLS: KeyDef[] = [
  // Punctuation
  { id: '.', label: '.', group: 'punctuation', char: '.' },
  { id: ',', label: ',', group: 'punctuation', char: ',' },
  { id: ';', label: ';', group: 'punctuation', char: ';' },
  { id: ':', label: ':', group: 'punctuation', char: ':' },
  { id: '!', label: '!', group: 'punctuation', char: '!' },
  { id: '?', label: '?', group: 'punctuation', char: '?' },
  // Math
  { id: '+', label: '+', group: 'math', char: '+' },
  { id: '-', label: '-', group: 'math', char: '-' },
  { id: '*', label: '*', group: 'math', char: '*' },
  { id: '/', label: '/', group: 'math', char: '/' },
  { id: '=', label: '=', group: 'math', char: '=' },
  { id: '%', label: '%', group: 'math', char: '%' },
  { id: '<', label: '<', group: 'math', char: '<' },
  { id: '>', label: '>', group: 'math', char: '>' },
  // Brackets
  { id: '(', label: '(', group: 'bracket', char: '(' },
  { id: ')', label: ')', group: 'bracket', char: ')' },
  { id: '[', label: '[', group: 'bracket', char: '[' },
  { id: ']', label: ']', group: 'bracket', char: ']' },
  { id: '{', label: '{', group: 'bracket', char: '{' },
  { id: '}', label: '}', group: 'bracket', char: '}' },
  // Quotes
  { id: '"', label: '"', group: 'quote', char: '"' },
  { id: "'", label: "'", group: 'quote', char: "'" },
  { id: '`', label: '`', group: 'quote', char: '`' },
  // Shell special
  { id: '|', label: '|', group: 'shell', char: '|' },
  { id: '&', label: '&', group: 'shell', char: '&' },
  { id: '$', label: '$', group: 'shell', char: '$' },
  { id: '~', label: '~', group: 'shell', char: '~' },
  { id: '#', label: '#', group: 'shell', char: '#' },
  { id: '@', label: '@', group: 'shell', char: '@' },
  { id: '\\', label: '\\', group: 'shell', char: '\\' },
  { id: '_', label: '_', group: 'shell', char: '_' },
  { id: '^', label: '^', group: 'shell', char: '^' },
]

// Default selected key IDs (current toolbar keys)
export const DEFAULT_KEY_IDS = [
  'esc', 'tab', 'ctrl', 'alt', 'shift', 'shift_tab',
  'ctrl_c', 'ctrl_z', 'ctrl_s',
  'home', 'end', 'pgup', 'pgdn',
  'arrow_up', 'arrow_down', 'arrow_left', 'arrow_right',
]

// Default selected symbol IDs (fewer)
export const DEFAULT_SYMBOL_IDS = ['.', '/', '-', '$', '&', ';', '|', '=', '>']

// Lookup map for quick access
export const KEY_MAP = new Map(ALL_KEYS.map(k => [k.id, k]))
export const SYMBOL_MAP = new Map(ALL_SYMBOLS.map(s => [s.id, s]))

/** Get KeyDef by type and id */
export function getDef(type: ConfigType, id: string): KeyDef | undefined {
  return type === 'key' ? KEY_MAP.get(id) : SYMBOL_MAP.get(id)
}

/** Get all definitions for a type */
export function getAllDefs(type: ConfigType): KeyDef[] {
  return type === 'key' ? ALL_KEYS : ALL_SYMBOLS
}

/** Get group info for a type */
export function getGroups(type: ConfigType): KeyGroupInfo[] {
  return type === 'key' ? KEY_GROUPS : SYMBOL_GROUPS
}
```

**Step 2: Commit**

```bash
git add web/src/utils/terminalKeyDefs.ts
git commit -m "feat: add terminal key/symbol definitions catalog"
```

---

### Task 5: Frontend — useKeyConfig composable

**Files:**
- Create: `web/src/composables/useKeyConfig.ts`

**Step 1: Create the composable**

```typescript
import { ref, computed } from 'vue'
import { apiGet, apiPut } from '@/utils/api'
import { DEFAULT_KEY_IDS, DEFAULT_SYMBOL_IDS, getDef, type ConfigType } from '@/utils/terminalKeyDefs'

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
    selectedKeyIds.value.map(id => getDef('key', id)).filter(Boolean)
  )
  const selectedSymbols = computed(() =>
    selectedSymbolIds.value.map(id => getDef('symbol', id)).filter(Boolean)
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
```

**Step 2: Commit**

```bash
git add web/src/composables/useKeyConfig.ts
git commit -m "feat: add useKeyConfig composable for key/symbol config"
```

---

### Task 6: Frontend — useTerminalKeys extensions (F-keys, new shortcuts)

**Files:**
- Modify: `web/src/composables/useTerminalKeys.ts`

**Step 1: Add new send functions**

Add after the existing `sendDelete()` function:

```typescript
  function sendF1() { sendInput('\x1bOP') }
  function sendF2() { sendInput('\x1bOQ') }
  function sendF3() { sendInput('\x1bOR') }
  function sendF4() { sendInput('\x1bOS') }
  function sendF5() { sendInput('\x1b[15~') }
  function sendF6() { sendInput('\x1b[17~') }
  function sendF7() { sendInput('\x1b[18~') }
  function sendF8() { sendInput('\x1b[19~') }
  function sendF9() { sendInput('\x1b[20~') }
  function sendF10() { sendInput('\x1b[21~') }
  function sendF11() { sendInput('\x1b[23~') }
  function sendF12() { sendInput('\x1b[24~') }
  function sendInsert() { sendInput('\x1b[2~') }
  function sendCtrlD() { sendInput('\x04') }
  function sendCtrlL() { sendInput('\x0c') }
  function sendCtrlR() { sendInput('\x12') }
```

**Step 2: Add unified `send(keyId)` dispatch method**

This method allows the toolbar to dispatch key actions by `keyId` string (matching KeyDef.id) without dynamic property access on the return object:

```typescript
  /** Dispatch a key action by keyId (for config-driven toolbar rendering) */
  function send(keyId: string) {
    const dispatch: Record<string, () => void> = {
      esc: sendEscape, tab: sendTab, shift_tab: sendShiftTab,
      ctrl_c: sendCtrlC, ctrl_z: sendCtrlZ, ctrl_s: sendCtrlS,
      ctrl_d: sendCtrlD, ctrl_l: sendCtrlL, ctrl_r: sendCtrlR,
      home: sendHome, end: sendEnd, pgup: sendPageUp, pgdn: sendPageDown, insert: sendInsert,
      arrow_up: sendArrowUp, arrow_down: sendArrowDown, arrow_left: sendArrowLeft, arrow_right: sendArrowRight,
      enter: sendEnter, backspace: sendBackspace, delete: sendDelete,
      f1: sendF1, f2: sendF2, f3: sendF3, f4: sendF4,
      f5: sendF5, f6: sendF6, f7: sendF7, f8: sendF8,
      f9: sendF9, f10: sendF10, f11: sendF11, f12: sendF12,
    }
    const fn = dispatch[keyId]
    if (fn) fn()
  }

  /** Check if a keyId is a modifier key (toggle behavior, not a send action) */
  function isModifierKey(keyId: string): boolean {
    return keyId === 'ctrl' || keyId === 'alt' || keyId === 'shift'
  }
```

**Step 3: Add to return object**

Add `send`, `isModifierKey`, and all new F-key/shortcut functions to the return statement.

**Step 4: Commit**

```bash
git add web/src/composables/useTerminalKeys.ts
git commit -m "feat: add F1-F12, Insert, Ctrl+D/L/R to useTerminalKeys"
```

---

### Task 7: Frontend — KeyConfigTab shared component

**Files:**
- Create: `web/src/components/terminal/KeyConfigTab.vue`

This is the shared component used by both tabs (keys and symbols). It takes a `type` prop and renders the full selected-area + available-area UI.

**Step 1: Create the component**

Key features:
- Selected area at top: vuedraggable for reorder, long-press to delete, shows placeholder when empty
- Available area below: grouped by key/symbol groups, click to toggle select/deselect
- Selected items shown with a checkmark in available area
- Local state (not committed until parent calls save)
- Emits `save(type, items[])` when save button clicked

The component template structure:

```vue
<template>
  <div class="kcf-content">
    <!-- Selected area -->
    <div class="kcf-selected">
      <div class="kcf-selected-header">
        <span class="kcf-section-title">{{ t('terminal.keyConfigSelected') }}</span>
        <span class="kcf-count">{{ localSelected.length }}</span>
      </div>
      <div v-if="localSelected.length > 0" class="kcf-selected-grid">
        <draggable v-model="localSelected" item-key="id" class="kcf-draggable" @end="onDragEnd">
          <template #item="{ element, index }">
            <button
              class="kcf-chip kcf-chip-selected"
              @click="removeAt(index)"
              @contextmenu.prevent="removeAt(index)"
              @touchstart="startLongPress(index, $event)"
              @touchend="cancelLongPress"
              @touchmove="cancelLongPress"
            >
              <span class="kcf-chip-label">{{ element.label }}</span>
            </button>
          </template>
        </draggable>
      </div>
      <div v-else class="kcf-empty-hint">{{ t('terminal.keyConfigEmpty') }}</div>
    </div>

    <!-- Divider -->
    <div class="kcf-divider" />

    <!-- Available area -->
    <div class="kcf-available">
      <div class="kcf-section-title">{{ t('terminal.keyConfigAvailable') }}</div>
      <div v-for="group in groups" :key="group.key" class="kcf-group">
        <div class="kcf-group-title">{{ t(group.label) }}</div>
        <div class="kcf-group-grid">
          <button
            v-for="def in getGroupDefs(group.key)"
            :key="def.id"
            class="kcf-chip"
            :class="{ 'kcf-chip-active': isSelected(def.id) }"
            @click="toggleSelect(def.id)"
          >
            <span class="kcf-chip-label">{{ def.label }}</span>
            <span v-if="isSelected(def.id)" class="kcf-check">✓</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
```

Script includes:
- `localSelected`: local reactive array of KeyDef (copied from parent on open, mutated locally)
- `toggleSelect(id)`: if selected, remove; if not, append to end
- `removeAt(index)`: remove from localSelected at index
- `startLongPress / cancelLongPress`: 500ms timer, on fire calls removeAt
- `isSelected(id)`: check if id in localSelected
- `getGroupDefs(groupKey)`: filter allDefs by group
- `onDragEnd`: just update local order (no API call yet)
- Expose `getSelectedIds()` for parent to call on save
- Watch `open` prop to reset local state

Style: chip-based grid layout, flowing wrap, consistent with existing toolbar-btn aesthetic.

**Step 2: Commit**

```bash
git add web/src/components/terminal/KeyConfigTab.vue
git commit -m "feat: add KeyConfigTab shared component for key/symbol config"
```

---

### Task 8: Frontend — KeyConfigDrawer component

**Files:**
- Create: `web/src/components/terminal/KeyConfigDrawer.vue`

**Step 1: Create the drawer component**

```vue
<template>
  <BottomSheet :open="open" auto :title="t('terminal.keyConfigTitle')" @close="handleClose">
    <template #header>
      <Settings :size="16" class="bs-header-icon" />
      <span class="bs-header-title">{{ t('terminal.keyConfigTitle') }}</span>
    </template>

    <!-- Tab bar -->
    <div class="kcd-tabs">
      <button class="kcd-tab" :class="{ active: activeTab === 'key' }" @click="activeTab = 'key'">
        {{ t('terminal.keyConfigTabKeys') }}
      </button>
      <button class="kcd-tab" :class="{ active: activeTab === 'symbol' }" @click="activeTab = 'symbol'">
        {{ t('terminal.keyConfigTabSymbols') }}
      </button>
    </div>

    <!-- Tab content -->
    <div class="kcd-body">
      <KeyConfigTab
        v-show="activeTab === 'key'"
        ref="keyTabRef"
        type="key"
        :selected-ids="selectedKeyIds"
      />
      <KeyConfigTab
        v-show="activeTab === 'symbol'"
        ref="symbolTabRef"
        type="symbol"
        :selected-ids="selectedSymbolIds"
      />
    </div>

    <!-- Footer with save button -->
    <template #footer>
      <div class="kcd-footer">
        <button class="kcd-btn kcd-btn-cancel" @click="handleClose">
          {{ t('common.cancel') }}
        </button>
        <button class="kcd-btn kcd-btn-save" @click="handleSave" :disabled="saving">
          {{ saving ? t('common.saving') : t('common.save') }}
        </button>
      </div>
    </template>
  </BottomSheet>
</template>
```

Script:
- Uses `useKeyConfig()` for `selectedKeyIds`, `selectedSymbolIds`, `fetchConfig`, `saveConfig`
- On open: `fetchConfig()`, reset tabs
- `handleSave()`: get selected IDs from both tabs, call `saveConfig('key', keyIds)` and `saveConfig('symbol', symbolIds)`, then close
- `handleClose()`: emit close (discard unsaved changes)

**Step 2: Commit**

```bash
git add web/src/components/terminal/KeyConfigDrawer.vue
git commit -m "feat: add KeyConfigDrawer component with tabs and save"
```

---

### Task 9: Frontend — Integrate into TerminalPanelContent

**Files:**
- Modify: `web/src/components/terminal/TerminalPanelContent.vue`

**Step 1: Add settings button next to quick commands**

In the toolbar Actions group (after the ZapIcon button), add:

```vue
<button class="toolbar-btn btn-action" @click="showKeyConfig = true" :title="t('terminal.keyConfigTitle')">
  <Settings :size="14" />
</button>
```

**Step 2: Add KeyConfigDrawer component**

```vue
<KeyConfigDrawer
  :open="showKeyConfig"
  @close="showKeyConfig = false"
  @saved="onKeyConfigSaved"
/>
```

**Step 3: Replace hardcoded toolbar with config-driven rendering**

Replace the current hardcoded key-group divs in the main-toolbar-row with a dynamic rendering loop based on `selectedKeys` from `useKeyConfig()`.

The toolbar scroll area becomes:

```vue
<div ref="toolbarScrollRef" class="toolbar-scroll" @scroll="updateToolbarScrollFade">
  <button
    v-for="def in selectedKeys"
    :key="def.id"
    class="toolbar-btn"
    :class="toolbarBtnClass(def)"
    @click="handleToolbarKeyClick(def)"
    @contextmenu.prevent
    :title="def.label"
  >
    {{ def.label }}
  </button>
  <!-- Quick commands button (always present) -->
  <button ref="cmdBtnRef" class="toolbar-btn btn-action" @click="showCommands = !showCommands" :title="t('terminal.quickCommands')">
    <ZapIcon :size="14" />
  </button>
  <!-- Settings button (always present) -->
  <button class="toolbar-btn btn-action" @click="showKeyConfig = true" :title="t('terminal.keyConfigTitle')">
    <Settings :size="14" />
  </button>
</div>
```

The `toolbarBtnClass(def)` function maps KeyDef properties to CSS classes:
- `def.isModifier` → `'btn-modifier modifier'` + active/locked classes based on `terminalKeys.activeModifiers`
- `def.group === 'shortcut'` → `'btn-modifier shortcut'`
- `def.group === 'navigation'` → `'btn-nav'`
- `def.group === 'arrow'` → `'btn-arrow'`
- `def.group === 'editing'` → `'btn-nav'`
- `def.group === 'function'` → `'btn-modifier shortcut'` (compact style)

The `handleToolbarKeyClick(def)` function dispatches based on `def.isModifier` and `def.id`:
- If `def.isModifier`: call `terminalKeys.toggleModifier(def.id as ModifierKey, isLongPress)` where `isLongPress` is determined by context (currently always false for toolbar clicks)
- Otherwise: call `terminalKeys.send(def.id); focusTerminal()`

This uses the new `send(keyId)` method from Task 6, avoiding unsafe dynamic property access.

**Step 4: Replace symbol bar with config-driven rendering**

Replace the current `symbolKeys` with `selectedSymbols` from `useKeyConfig()`. The symbol bar buttons render from the config.

**Important**: The HashIcon toggle button (`<button class="toolbar-btn modifier gesture-toggle" :class="{ active: showSymbolBar }" @click="toggleSymbolBar()">`) stays in the main-toolbar-row (OUTSIDE the scroll area), as it is a toolbar toggle not a key. The symbol bar Transition block above the main-toolbar-row also remains — only the content inside changes from `symbolKeys` (frequency-sorted) to `selectedSymbols` (config-sorted).

**Step 5: Add onKeyConfigSaved handler**

```typescript
function onKeyConfigSaved() {
  showKeyConfig.value = false
  // Config is already saved and useKeyConfig state is updated
  // Toolbar will reactively update via computed properties
}
```

**Step 6: Remove old frequency sorting code**

Remove:
- `symbolKeys` ref and `sortSymbolsByFreq()` function
- `handleSymbolClick` frequency tracking (`incrementSymbolFreq`, `saveSymbolFreqs` calls)
- Import of `ALL_SYMBOLS`, `sortSymbolsByFreqUtil`, `loadSymbolFreqs`, `saveSymbolFreqs`, `incrementSymbolFreq` from `terminalSymbolFreq.ts`

Replace `handleSymbolClick(sym)` with simply `terminalKeys.sendInput(sym); focusTerminal()`.

**Step 7: Commit**

```bash
git add web/src/components/terminal/TerminalPanelContent.vue
git commit -m "feat: integrate key config drawer, config-driven toolbar, remove freq sorting"
```

---

### Task 10: Frontend — i18n strings

**Files:**
- Modify: `web/src/i18n/locales/zh.ts`
- Modify: `web/src/i18n/locales/en.ts`

**Step 1: Add Chinese i18n strings**

Add in `terminal` section:

```typescript
keyConfigTitle: '工具栏配置',
keyConfigTabKeys: '按键',
keyConfigTabSymbols: '符号',
keyConfigSelected: '已选',
keyConfigAvailable: '可选',
keyConfigEmpty: '点击下方按键添加到工具栏',
keyConfigSaved: '配置已保存',
keyConfigSaveFailed: '保存失败',
keyGroupModifier: '修饰键',
keyGroupFunction: '功能键',
keyGroupNavigation: '导航键',
keyGroupArrow: '方向键',
keyGroupShortcut: '快捷键',
keyGroupEditing: '编辑键',
symbolGroupPunctuation: '标点',
symbolGroupMath: '数学',
symbolGroupBracket: '括号',
symbolGroupQuote: '引号',
symbolGroupShell: 'Shell 特殊',
```

**Step 2: Add English equivalents**

```typescript
keyConfigTitle: 'Toolbar Config',
keyConfigTabKeys: 'Keys',
keyConfigTabSymbols: 'Symbols',
keyConfigSelected: 'Selected',
keyConfigAvailable: 'Available',
keyConfigEmpty: 'Tap keys below to add to toolbar',
keyConfigSaved: 'Config saved',
keyConfigSaveFailed: 'Save failed',
keyGroupModifier: 'Modifiers',
keyGroupFunction: 'Function',
keyGroupNavigation: 'Navigation',
keyGroupArrow: 'Arrows',
keyGroupShortcut: 'Shortcuts',
keyGroupEditing: 'Editing',
symbolGroupPunctuation: 'Punctuation',
symbolGroupMath: 'Math',
symbolGroupBracket: 'Brackets',
symbolGroupQuote: 'Quotes',
symbolGroupShell: 'Shell Special',
```

**Step 3: Commit**

```bash
git add web/src/i18n/locales/zh.ts web/src/i18n/locales/en.ts
git commit -m "feat: add i18n strings for key config UI"
```

---

### Task 11: Frontend — Styles

**Files:**
- Modify: `web/src/components/terminal/KeyConfigTab.vue` (scoped styles)
- Modify: `web/src/components/terminal/KeyConfigDrawer.vue` (scoped styles)
- Modify: `web/src/components/terminal/TerminalPanelContent.vue` (minor style updates)

This task covers all CSS needed for the new components. Key style patterns to follow:

- Chip buttons: similar size to `toolbar-btn` (36px height), `var(--radius-sm)` border-radius
- Grid flow: `display: flex; flex-wrap: wrap; gap: 6px;`
- Selected chip: accent background, drag handle visible
- Available chip: secondary bg, checkmark overlay when selected
- Group titles: `var(--text-muted)` color, small caps style
- Empty hint: centered, muted text, padding
- Tab bar: two equal buttons, bottom border indicator on active
- Footer: flex row, cancel + save buttons matching BottomSheet footer pattern
- Long-press visual: slight scale-down during press, opacity transition on delete

**Step 1: Add all styles inline in the components**

**Step 2: Commit**

```bash
git add web/src/components/terminal/KeyConfigTab.vue web/src/components/terminal/KeyConfigDrawer.vue web/src/components/terminal/TerminalPanelContent.vue
git commit -m "feat: add styles for key config drawer, tab, and chip components"
```

---

### Task 12: Cleanup — Remove old symbol frequency code

**Files:**
- Consider deleting or marking deprecated: `web/src/utils/terminalSymbolFreq.ts`
- Delete: `web/src/utils/__tests__/terminalSymbolFreq.test.ts`

**Step 1: Remove frequency imports from TerminalPanelContent**

Already done in Task 9. Verify no other files import from `terminalSymbolFreq.ts`.

**Step 2: Delete the old frequency utility**

```bash
rm web/src/utils/terminalSymbolFreq.ts
rm web/src/utils/__tests__/terminalSymbolFreq.test.ts
```

**Step 3: Commit**

```bash
git add -A
git commit -m "chore: remove old symbol frequency sorting (replaced by key config)"
```

---

### Task 13: End-to-end verification

**Step 1: Build the Go backend**

```bash
cd /home/xulongzhe/projects/clawbench && go build ./...
```

**Step 2: Run Go tests**

```bash
cd /home/xulongzhe/projects/clawbench && go test ./internal/handler/ -run TestServeKeyConfig -v
```

**Step 3: Build the Vue frontend**

```bash
cd /home/xulongzhe/projects/clawbench/web && npm run build
```

**Step 4: Run Vue type check**

```bash
cd /home/xulongzhe/projects/clawbench/web && npx vue-tsc --noEmit
```

**Step 5: Manual smoke test checklist**

- [ ] Settings icon appears next to quick commands button
- [ ] Clicking settings opens full-screen drawer
- [ ] Two tabs visible (Keys / Symbols)
- [ ] Keys tab shows selected keys at top (default toolbar keys)
- [ ] Keys tab shows grouped available keys below
- [ ] Clicking an available key adds it to selected
- [ ] Clicking a selected key's chip removes it
- [ ] Long-press on selected chip removes it
- [ ] Dragging selected chips reorders them
- [ ] Symbols tab works identically
- [ ] Save button persists config, toolbar updates
- [ ] Cancel discards changes
- [ ] Toolbar renders dynamically based on config
- [ ] Symbol bar renders dynamically based on config
- [ ] Existing toolbar functionality (modifiers, gestures) still works

**Step 6: Final commit**

```bash
git add -A
git commit -m "feat: complete terminal key/symbol configuration feature"
```

---

## Audit Findings (resolved)

The following issues were found during codebase audit and have been fixed in this document:

| # | Issue | Fix Applied |
|---|-------|-------------|
| 1 | Go module path `github.com/user/clawbench` was wrong | Changed to `clawbench` in test imports |
| 2 | `SettingsIcon` does not exist in lucide-vue-next | Changed all references to `Settings` |
| 3 | `terminalKeys[def.sendFn]()` dynamic property access is type-unsafe | Added `send(keyId)` and `isModifierKey(keyId)` dispatch methods to useTerminalKeys (Task 6), toolbar handler uses these (Task 9) |
| 4 | HashIcon symbol-bar toggle is in main-toolbar-row (outside scroll), not part of symbol bar | Task 9 Step 4 now explicitly preserves this structure |
| 5 | `isToggle` field in KeyDef was dead code | Removed from interface; `sendFn` made optional since modifiers don't use it |

Additional notes for implementer:
- `apiGet<T>(url)` and `apiPut(url, body)` signatures are correct per `web/src/utils/api.ts`
- `BottomSheet` `auto` prop exists and works for full-screen adaptive mode
- vuedraggable import is `import draggable from 'vuedraggable'`
- Only 2 files import from `terminalSymbolFreq.ts` (test + TerminalPanelContent.vue) — cleanup scope is small
- The `terminal` i18n section is flat key-value, consistent with plan's additions
- `setupTestEnv` returns `(*testEnv, func())` — use `teardown := setupTestEnv(t); defer teardown()`
