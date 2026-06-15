import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const terminalComponentPaths = [
  '../terminal/TerminalPanelContent.vue',
]

const readTerminalComponent = (path: string) => readFileSync(resolve(__dirname, path), 'utf8')
const readToolbarStyleBlock = (source: string) => {
  const start = source.indexOf('.terminal-toolbar {')
  const end = source.indexOf('</style>', start)

  return source.slice(start, end)
}

describe('TerminalPanel xterm selection defaults', () => {
  it('does not force xterm selection to line mode', () => {
    const source = readTerminalComponent('../terminal/TerminalPanelContent.vue')

    expect(source).not.toContain("selectionStyle: 'line'")
  })

  it('renders config-driven toolbar with gesture-aware visibility', () => {
    const source = readTerminalComponent('../terminal/TerminalPanelContent.vue')

    // Toolbar is now config-driven: keys rendered via v-for over visibleKeys
    expect(source).toContain('v-for="def in visibleKeys"')
    // Modifier keys still use toggle behavior with active/locked classes
    expect(source).toContain('toolbarBtnClass(def)')
    // Click handler dispatches via terminalKeys.send() or toggleModifier()
    expect(source).toContain('handleToolbarKeyClick(def)')
  })

  it('keeps terminal virtual keys in a borderless, transparent overlay system', () => {
    for (const path of terminalComponentPaths) {
      const source = readTerminalComponent(path)
      const toolbarStyle = readToolbarStyleBlock(source)

      // Borderless: no border on buttons
      expect(toolbarStyle).toContain('border: none')
      // Transparent default background
      expect(toolbarStyle).toContain('background: transparent')
      // Hover/active use semi-transparent overlays
      expect(toolbarStyle).toContain('--toolbar-key-hover')
      expect(toolbarStyle).toContain('--toolbar-key-active')
      // Scroll fade instead of scrollbar
      expect(toolbarStyle).toContain('scrollbar-width: none')
      expect(toolbarStyle).toContain('scroll-fade')
      // No decorative masks or accent colors
      expect(toolbarStyle).not.toContain('var(--color-green)')
      expect(toolbarStyle).not.toContain('var(--color-yellow)')
      expect(toolbarStyle).not.toContain('var(--color-purple)')
    }
  })
})
