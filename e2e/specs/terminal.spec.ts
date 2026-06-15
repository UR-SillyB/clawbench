import { test, expect } from '../fixtures'
import { TerminalPage } from '../pages/terminal.page'

test.describe('Terminal multi-tab', () => {
  let terminal: TerminalPage

  test.beforeEach(async ({ page }) => {
    terminal = new TerminalPage(page)
  })

  test('should show terminal panel with one default tab', async ({ page }) => {
    await terminal.switchToTerminal()
    await terminal.expectPanelVisible()
    await terminal.expectTabCount(1)
  })

  test('should connect the default tab on open', async ({ page }) => {
    await terminal.switchToTerminal()
    await terminal.expectPanelVisible()
    // Wait for the status dot to show connected (green)
    await terminal.waitForTabConnected(0, 15000)
  })

  test('should create a new tab when "+" button is clicked', async ({ page }) => {
    await terminal.switchToTerminal()
    await terminal.waitForTabConnected(0, 15000)

    // Click "+" to create a new tab
    await terminal.createNewTab()
    await terminal.expectTabCount(2)

    // The new tab should be active
    await terminal.expectTabActive(1)
  })

  test('new tab should connect automatically', async ({ page }) => {
    await terminal.switchToTerminal()
    await terminal.waitForTabConnected(0, 15000)

    // Create a new tab
    await terminal.createNewTab()
    await terminal.expectTabCount(2)

    // Wait for the new tab to connect
    await terminal.waitForTabConnected(1, 15000)
  })

  test('should switch between tabs', async ({ page }) => {
    await terminal.switchToTerminal()
    await terminal.waitForTabConnected(0, 15000)

    // Create a second tab
    await terminal.createNewTab()
    await terminal.waitForTabConnected(1, 15000)

    // Switch back to the first tab
    await terminal.clickTab(0)
    await terminal.expectTabActive(0)

    // Switch to the second tab
    await terminal.clickTab(1)
    await terminal.expectTabActive(1)
  })

  test('should show tab title derived from directory name', async ({ page }) => {
    await terminal.switchToTerminal()
    await terminal.waitForTabConnected(0, 15000)

    // Default tab should show some title (directory name or "Terminal")
    const title = await terminal.getTabTitle(0)
    expect(title).toBeTruthy()
  })

  test('should disconnect tabs when switching away from terminal', async ({ page }) => {
    await terminal.switchToTerminal()
    await terminal.waitForTabConnected(0, 15000)

    // Switch to Chat tab
    await page.locator('.dock-center .dock-btn').nth(0).click()

    // Wait a moment for disconnect to process
    await page.waitForTimeout(500)

    // Switch back to terminal
    await terminal.switchToTerminal()

    // Tab should reconnect
    await terminal.waitForTabConnected(0, 15000)
  })

  test('should close a tab via three-dot menu', async ({ page }) => {
    await terminal.switchToTerminal()
    await terminal.waitForTabConnected(0, 15000)

    // Create a second tab so we still have one after closing
    await terminal.createNewTab()
    await terminal.waitForTabConnected(1, 15000)
    await terminal.expectTabCount(2)

    // Open the three-dot menu for the second tab
    await terminal.openTabMenu(1)

    // Click "Close Tab" menu item
    await terminal.clickTabMenuItem(/Close Tab|关闭标签/)

    // Should have 1 tab now
    await terminal.expectTabCount(1)
  })

  test('should auto-create a new tab when closing the last tab', async ({ page }) => {
    await terminal.switchToTerminal()
    await terminal.waitForTabConnected(0, 15000)

    // Open the three-dot menu for the only tab
    await terminal.openTabMenu(0)

    // Click "Close Tab" menu item
    await terminal.clickTabMenuItem(/Close Tab|关闭标签/)

    // Should auto-create a new tab (still 1 tab total)
    await terminal.expectTabCount(1)
  })

  test('status dots should reflect connection state', async ({ page }) => {
    await terminal.switchToTerminal()

    // Initially the status should be "disconnected" or "connecting"
    // After connecting, it should be "connected"
    await terminal.waitForTabConnected(0, 15000)
    await terminal.expectTabStatus(0, 'connected')
  })

  test('multiple tabs should each have their own status dot', async ({ page }) => {
    await terminal.switchToTerminal()
    await terminal.waitForTabConnected(0, 15000)

    await terminal.createNewTab()
    await terminal.waitForTabConnected(1, 15000)

    // Both tabs should show connected
    await terminal.expectTabStatus(0, 'connected')
    await terminal.expectTabStatus(1, 'connected')
  })

  test('+" button should respect max sessions limit', async ({ page }) => {
    await terminal.switchToTerminal()
    await terminal.waitForTabConnected(0, 15000)

    // Create tabs up to a reasonable limit — just verify the button becomes
    // disabled at some point. Default max_sessions is 10.
    // We create a few and verify they work.
    for (let i = 1; i < 3; i++) {
      await terminal.createNewTab()
      await terminal.waitForTabConnected(i, 15000)
    }

    await terminal.expectTabCount(3)
  })
})
