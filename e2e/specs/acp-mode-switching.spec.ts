import { test, expect } from '../fixtures'
import { ChatPage } from '../pages/chat.page'

/**
 * E2E tests for ACP mode switching feature.
 *
 * Uses acp-mock agent (real ACP stdio protocol) which provides:
 * - 3 modes (Code, Plan, Bypass Permissions)
 *
 * Key ACP behaviors tested:
 * 1. Mode menu opens when clicking mode chip
 * 2. Mode can be switched (e.g., Code → Plan) and chip text updates
 * 3. Selected mode persists after page reload
 * 4. Mode switch is included in POST /api/ai/chat body with correct modeId
 *
 * IMPORTANT: ACP connections are lazy — established on first message.
 * The first test must send a message to warm up the ACP connection pool.
 * After that, subsequent tests can rely on cached ACP state.
 *
 * SERIAL: Tests must run serially because the ACP mock agent is a single
 * subprocess. Concurrent Prompt requests on the same agent process can
 * cause the JSON-RPC stream to become corrupted.
 */
test.describe.serial('ACP Mode Switching', () => {
  let chat: ChatPage

  test.beforeEach(async ({ page }) => {
    chat = new ChatPage(page)
  })

  test('should open mode tab in modal when clicking mode chip', async ({ page }) => {
    // Establish ACP connection first (default agent is acp-mock)
    await chat.sendAndAwaitACPReply('hi')

    // Mode chip should be visible for ACP-backed agents
    const modeChip = page.locator('.mode-chip')
    await expect(modeChip).toBeVisible({ timeout: 15000 })

    // Click the mode chip to open the ModelModal on the mode tab
    await modeChip.click()

    // Mode tab should be active and mode items visible
    const modeTab = page.locator('.model-tab.active').filter({ hasText: /mode|模式/i })
    await expect(modeTab).toBeVisible({ timeout: 5000 })

    // Mode items use .thinking-item class in the modal
    const modeItems = page.locator('.model-tab-content .thinking-item')
    await expect(modeItems.first()).toBeVisible({ timeout: 5000 })

    // acp-mock provides at least 2 modes
    const count = await modeItems.count()
    expect(count).toBeGreaterThanOrEqual(2)
  })

  test('should switch mode from Code to Plan', async ({ page }) => {
    // Previous test already established ACP connection
    // Mode chip should already be visible with cached ACP state
    const modeChip = page.locator('.mode-chip')
    await expect(modeChip).toBeVisible({ timeout: 15000 })

    // Open mode tab in modal using ChatPage helper
    await chat.openModeMenu()

    // Select "Plan" mode
    await chat.selectMode('Plan')

    // Modal should close after selection
    const modalTabs = page.locator('.model-tab')
    await expect(modalTabs.first()).not.toBeVisible({ timeout: 3000 })

    // Mode chip text should update to show "Plan"
    await expect(modeChip).toContainText('Plan', { timeout: 5000 })
  })

  test('should persist mode after page reload', async ({ page }) => {
    // Previous test switched mode to "Plan" — verify it persists after reload
    await page.reload()
    await page.waitForLoadState('networkidle')
    await page.waitForTimeout(500)

    // Wait for the UI to be ready
    await expect(chat.textarea).toBeVisible({ timeout: 5000 })

    // Mode chip should still show "Plan" after reload
    const modeChip = page.locator('.mode-chip')
    await expect(modeChip).toBeVisible({ timeout: 15000 })
    await expect(modeChip).toContainText('Plan', { timeout: 5000 })
  })

  test('mode switch should be included in chat request body', async ({ page }) => {
    // Warm up ACP connection (reload in previous test may have reset state)
    await chat.sendAndAwaitACPReply('hi')

    // Wait for mode chip to be visible
    const modeChip = page.locator('.mode-chip')
    await expect(modeChip).toBeVisible({ timeout: 15000 })

    // Switch mode first (local ref update only — no API call)
    await chat.openModeMenu()
    await chat.selectMode('Code')

    // Intercept the next chat API call
    const chatRequestPromise = page.waitForRequest(
      req => req.url().includes('/api/ai/chat') && req.method() === 'POST'
    )

    // Send a message — modeId should be included in the request body
    await chat.sendMessage('test mode in body')

    // Wait for the intercepted request
    const chatRequest = await chatRequestPromise

    // Verify request body contains modeId
    const body = chatRequest.postDataJSON()
    expect(body.modeId).toBeTruthy()
  })
})
