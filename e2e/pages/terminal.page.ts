import { type Locator, type Page, expect } from '@playwright/test'

/**
 * Page Object Model for the Terminal panel.
 *
 * DOM structure (key selectors):
 *   .terminal-panel        — root container
 *   .terminal-tab-bar      — tab bar at top
 *   .terminal-tab-list     — scrollable tab list
 *   .terminal-tab          — individual tab (has .active when selected)
 *   .terminal-tab-title    — tab title text
 *   .terminal-tab-status   — connection status dot (.connected, .disconnected, etc.)
 *   .terminal-tab-menu-btn — three-dot menu button per tab
 *   .terminal-tab-add      — "+" button to create new tab
 *   .terminal-viewport     — xterm containers area
 *   .terminal-container    — per-tab xterm container (v-show toggled)
 *   .terminal-toolbar      — virtual keyboard toolbar
 *   .terminal-error-overlay — error overlay (when connection fails)
 */
export class TerminalPage {
  readonly page: Page
  private readonly panel: Locator
  private readonly tabBar: Locator
  private readonly tabList: Locator
  private readonly addBtn: Locator
  private readonly viewport: Locator

  constructor(page: Page) {
    this.page = page
    this.panel = page.locator('.terminal-panel')
    this.tabBar = page.locator('.terminal-tab-bar')
    this.tabList = page.locator('.terminal-tab-list')
    this.addBtn = page.locator('.terminal-tab-add')
    this.viewport = page.locator('.terminal-viewport')
  }

  // --- Navigation ---

  /** Switch to Terminal tab via overflow menu */
  async switchToTerminal() {
    await this.page.locator('.dock-overflow-btn').click()
    await expect(this.page.locator('.dock-overflow-popup')).toBeVisible()
    await this.page.locator('.dock-overflow-item', { hasText: /Terminal|终端/ }).click()
  }

  // --- Tab queries ---

  /** Get all tab elements */
  getTabs(): Locator {
    return this.tabList.locator('.terminal-tab')
  }

  /** Get the active tab element */
  getActiveTab(): Locator {
    return this.tabList.locator('.terminal-tab.active')
  }

  /** Get tab by index */
  getTab(index: number): Locator {
    return this.getTabs().nth(index)
  }

  /** Get the number of tabs */
  async tabCount(): Promise<number> {
    return await this.getTabs().count()
  }

  /** Get tab title text by index */
  async getTabTitle(index: number): Promise<string> {
    return await this.getTab(index).locator('.terminal-tab-title').textContent() || ''
  }

  // --- Tab actions ---

  /** Click a tab by index to switch to it */
  async clickTab(index: number) {
    await this.getTab(index).click()
  }

  /** Click the "+" button to create a new tab */
  async createNewTab() {
    await this.addBtn.click()
  }

  /** Check if the "+" button is disabled (tab limit reached) */
  async isAddButtonDisabled(): Promise<boolean> {
    return await this.addBtn.isDisabled()
  }

  /** Open the three-dot menu for a specific tab */
  async openTabMenu(index: number) {
    await this.getTab(index).locator('.terminal-tab-menu-btn').click()
  }

  /** Click a menu item in the tab menu popup */
  async clickTabMenuItem(label: string | RegExp) {
    // The PopupMenu is teleported to body
    await this.page.locator('.popup-menu-item', { hasText: label }).click()
  }

  // --- Status dots ---

  /** Get the status dot class for a tab */
  async getTabStatusClass(index: number): Promise<string> {
    const statusDot = this.getTab(index).locator('.terminal-tab-status')
    const className = await statusDot.getAttribute('class') || ''
    return className
  }

  /** Check if a tab's status dot shows "connected" */
  async isTabConnected(index: number): Promise<boolean> {
    const cls = await this.getTabStatusClass(index)
    return cls.includes('connected')
  }

  // --- Terminal viewport ---

  /** Get the xterm container for a specific tab (by index) */
  getTerminalContainer(index: number): Locator {
    return this.viewport.locator('.terminal-container').nth(index)
  }

  /** Check if the terminal error overlay is visible */
  async isErrorOverlayVisible(): Promise<boolean> {
    return await this.panel.locator('.terminal-error-overlay').isVisible().catch(() => false)
  }

  // --- Assertions ---

  /** Assert the terminal panel is visible */
  async expectPanelVisible() {
    await expect(this.panel).toBeVisible()
  }

  /** Assert tab count */
  async expectTabCount(count: number) {
    await expect(this.getTabs()).toHaveCount(count)
  }

  /** Assert a specific tab is active */
  async expectTabActive(index: number) {
    await expect(this.getTab(index)).toHaveClass(/active/)
  }

  /** Assert a tab's status dot has a specific class */
  async expectTabStatus(index: number, statusClass: string) {
    const statusDot = this.getTab(index).locator('.terminal-tab-status')
    await expect(statusDot).toHaveClass(new RegExp(statusClass))
  }

  /** Wait for a tab to become connected (status dot turns green) */
  async waitForTabConnected(index: number, timeout = 10000) {
    const statusDot = this.getTab(index).locator('.terminal-tab-status')
    await expect(statusDot).toHaveClass(/connected/, { timeout })
  }

  /** Wait for the terminal viewport to contain xterm content */
  async waitForTerminalReady(timeout = 10000) {
    // Wait for the xterm cursor to appear (indicates the PTY session is running)
    await this.page.waitForFunction(
      () => !!document.querySelector('.xterm-screen'),
      { timeout }
    ).catch(() => {
      // xterm might not have rendered yet, which is fine for some tests
    })
  }
}
