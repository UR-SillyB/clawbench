import { ref } from 'vue'

export type ModifierKey = 'ctrl' | 'alt' | 'shift'
export type ModifierState = 'inactive' | 'once' | 'locked'

export function useTerminalKeys(sendInput: (data: string) => void) {
  const modifiers: Record<ModifierKey, ModifierState> = {
    ctrl: 'inactive',
    alt: 'inactive',
    shift: 'inactive',
  }
  const activeModifiers = ref<Record<ModifierKey, ModifierState>>({ ...modifiers })

  function toggleModifier(key: ModifierKey, isLongPress: boolean) {
    const current = modifiers[key]
    if (current === 'inactive') {
      modifiers[key] = isLongPress ? 'locked' : 'once'
    } else if (current === 'once') {
      if (isLongPress) {
        modifiers[key] = 'locked'
      } else {
        modifiers[key] = 'inactive'
      }
    } else {
      // locked -> deactivate
      modifiers[key] = 'inactive'
    }
    activeModifiers.value = { ...modifiers }
  }

  function clearOnceModifiers() {
    let changed = false
    for (const key of ['ctrl', 'alt', 'shift'] as ModifierKey[]) {
      if (modifiers[key] === 'once') {
        modifiers[key] = 'inactive'
        changed = true
      }
    }
    if (changed) {
      activeModifiers.value = { ...modifiers }
    }
  }

  // Transform a character input based on active modifiers
  function processInput(data: string): string {
    // If no modifiers active, pass through
    if (modifiers.ctrl === 'inactive' && modifiers.alt === 'inactive' && modifiers.shift === 'inactive') {
      return data
    }

    // Handle Ctrl combinations
    if (modifiers.ctrl !== 'inactive') {
      const char = data.toLowerCase()
      if (char.length === 1 && char >= 'a' && char <= 'z') {
        const code = char.charCodeAt(0) - 96 // Ctrl+A = \x01, Ctrl+Z = \x1a
        clearOnceModifiers()
        return String.fromCharCode(code)
      }
      // Special cases
      if (char === '[') { clearOnceModifiers(); return '\x1b' }
      if (char === '\\') { clearOnceModifiers(); return '\x1c' }
      if (char === ']') { clearOnceModifiers(); return '\x1d' }
      if (char === '@') { clearOnceModifiers(); return '\x00' }
      if (char === '^') { clearOnceModifiers(); return '\x1e' }
      if (char === '_') { clearOnceModifiers(); return '\x1f' }
    }

    // Handle Alt combinations
    if (modifiers.alt !== 'inactive') {
      if (data.length === 1) {
        clearOnceModifiers()
        return `\x1b${data}`
      }
    }

    // Handle Shift combinations
    if (modifiers.shift !== 'inactive') {
      if (data === '\t') { // Shift+Tab
        clearOnceModifiers()
        return '\x1b[Z'
      }
    }

    clearOnceModifiers()
    return data
  }

  // Special key sends
  function sendCtrlC() {
    sendInput('\x03')
  }

  function sendCtrlZ() {
    sendInput('\x1a')
  }

  function sendCtrlS() {
    sendInput('\x13')
  }

  function sendEscape() {
    sendInput('\x1b')
  }

  function sendTab() {
    sendInput('\t')
  }

  function sendShiftTab() {
    sendInput('\x1b[Z')
  }

  function sendArrowUp() {
    sendInput('\x1b[A')
  }

  function sendArrowDown() {
    sendInput('\x1b[B')
  }

  function sendArrowRight() {
    sendInput('\x1b[C')
  }

  function sendArrowLeft() {
    sendInput('\x1b[D')
  }

  function sendHome() {
    sendInput('\x1b[H')
  }

  function sendEnd() {
    sendInput('\x1b[F')
  }

  function sendPageUp() {
    sendInput('\x1b[5~')
  }

  function sendPageDown() {
    sendInput('\x1b[6~')
  }

  function sendEnter() {
    sendInput('\r')
  }

  function sendBackspace() {
    sendInput('\x7f')
  }

  function sendDelete() {
    sendInput('\x1b[3~')
  }

  // Function keys
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

  function reset() {
    modifiers.ctrl = 'inactive'
    modifiers.alt = 'inactive'
    modifiers.shift = 'inactive'
    activeModifiers.value = { ...modifiers }
  }

  return {
    activeModifiers,
    toggleModifier,
    processInput,
    clearOnceModifiers,
    sendCtrlC,
    sendCtrlZ,
    sendCtrlS,
    sendCtrlD,
    sendCtrlL,
    sendCtrlR,
    sendEscape,
    sendTab,
    sendShiftTab,
    sendArrowUp,
    sendArrowDown,
    sendArrowRight,
    sendArrowLeft,
    sendHome,
    sendEnd,
    sendPageUp,
    sendPageDown,
    sendEnter,
    sendBackspace,
    sendDelete,
    sendF1,
    sendF2,
    sendF3,
    sendF4,
    sendF5,
    sendF6,
    sendF7,
    sendF8,
    sendF9,
    sendF10,
    sendF11,
    sendF12,
    sendInsert,
    send,
    isModifierKey,
    reset,
  }
}
