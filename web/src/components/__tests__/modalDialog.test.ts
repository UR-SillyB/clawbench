import { describe, expect, it, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import ModalDialog from '@/components/common/ModalDialog.vue'

describe('ModalDialog', () => {
  const TeleportStub = { template: '<div><slot /></div>' }

  afterEach(() => {
    vi.useRealTimers()
  })

  function mountDialog(props = {}, slots = {}) {
    return mount(ModalDialog, {
      props: { open: true, ...props },
      slots,
      global: {
        stubs: { Teleport: TeleportStub },
      },
    })
  }

  it('renders content when open', async () => {
    const wrapper = mountDialog({}, { default: '<div class="content">Hello</div>' })

    expect(wrapper.find('.modal-overlay').exists()).toBe(true)
    expect(wrapper.find('.content').text()).toBe('Hello')
  })

  it('shows title in header', async () => {
    const wrapper = mountDialog({ title: 'Test Dialog' })

    expect(wrapper.find('.modal-title').text()).toBe('Test Dialog')
  })

  it('shows close button', async () => {
    const wrapper = mountDialog({ title: 'Test' })

    expect(wrapper.find('.modal-close-btn').exists()).toBe(true)
  })

  it('emits close when overlay is clicked (after animation)', async () => {
    vi.useFakeTimers()
    const wrapper = mountDialog()

    await wrapper.find('.modal-overlay').trigger('click')
    expect(wrapper.find('.modal-dialog').classes()).toContain('modal-leaving')

    vi.advanceTimersByTime(250)
    await nextTick()

    expect(wrapper.emitted('close')).toBeTruthy()
  })

  it('does not emit close when dialog body is clicked', async () => {
    const wrapper = mountDialog()

    // Click the dialog itself (has @click.stop)
    await wrapper.find('.modal-dialog').trigger('click')

    // Should NOT close because @click.stop prevents bubbling
    expect(wrapper.emitted('close')).toBeFalsy()
  })

  it('emits close when close button is clicked (after animation)', async () => {
    vi.useFakeTimers()
    const wrapper = mountDialog({ title: 'Test' })

    await wrapper.find('.modal-close-btn').trigger('click')
    expect(wrapper.find('.modal-dialog').classes()).toContain('modal-leaving')

    vi.advanceTimersByTime(250)
    await nextTick()

    expect(wrapper.emitted('close')).toBeTruthy()
  })

  it('uses custom zIndex', async () => {
    const wrapper = mountDialog({ zIndex: 3000 })

    const overlay = wrapper.find('.modal-overlay')
    expect(overlay.attributes('style')).toContain('3000')
  })

  it('applies fullHeight class when fullHeight is true', async () => {
    const wrapper = mountDialog({ fullHeight: true })

    expect(wrapper.find('.modal-dialog').classes()).toContain('modal-full-height')
  })

  it('uses header slot when provided', async () => {
    const wrapper = mountDialog({}, { header: '<span class="custom-hdr">Custom</span>' })

    expect(wrapper.find('.custom-hdr').text()).toBe('Custom')
  })

  it('renders footer slot when provided', async () => {
    const wrapper = mountDialog({}, { footer: '<button class="footer-btn">OK</button>' })

    expect(wrapper.find('.modal-footer').exists()).toBe(true)
    expect(wrapper.find('.footer-btn').text()).toBe('OK')
  })

  it('renders after slot when provided', async () => {
    const wrapper = mountDialog({}, { after: '<div class="after-content">After</div>' })

    expect(wrapper.find('.after-content').text()).toBe('After')
  })

  it('keeps content in DOM after closing (everOpened)', async () => {
    const wrapper = mountDialog({}, { default: '<div class="content">Hello</div>' })

    // Close via prop
    await wrapper.setProps({ open: false })
    await nextTick()

    // everOpened stays true, v-show hides it
    expect(wrapper.find('.modal-overlay').exists()).toBe(true)
    expect(wrapper.find('.modal-overlay').isVisible()).toBe(false)
  })

  it('enters leaving state before closing (animation)', async () => {
    vi.useFakeTimers()
    const wrapper = mountDialog()

    // Click overlay to close
    await wrapper.find('.modal-overlay').trigger('click')
    await nextTick()

    // Should have leaving class
    expect(wrapper.find('.modal-dialog').classes()).toContain('modal-leaving')

    // After 250ms, close event fires
    vi.advanceTimersByTime(250)
    await nextTick()

    expect(wrapper.emitted('close')).toBeTruthy()
  })

  it('does not emit close twice if already leaving', async () => {
    vi.useFakeTimers()
    const wrapper = mountDialog()

    // Click overlay to start close animation
    await wrapper.find('.modal-overlay').trigger('click')
    await nextTick()

    // Click again while leaving
    await wrapper.find('.modal-overlay').trigger('click')
    await nextTick()

    // Advance past timer — should only emit once
    vi.advanceTimersByTime(250)
    await nextTick()

    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('exposes close method', async () => {
    const wrapper = mountDialog()

    expect(typeof wrapper.vm.close).toBe('function')
  })

  it('does not render when never opened', () => {
    const wrapper = mount(ModalDialog, {
      props: { open: false },
      global: { stubs: { Teleport: TeleportStub } },
    })

    expect(wrapper.find('.modal-overlay').exists()).toBe(false)
  })

  it('cancels leaving animation when re-opened', async () => {
    vi.useFakeTimers()
    const wrapper = mountDialog()

    // Start closing
    await wrapper.find('.modal-overlay').trigger('click')
    await nextTick()
    expect(wrapper.find('.modal-dialog').classes()).toContain('modal-leaving')

    // Wait for close event
    vi.advanceTimersByTime(250)
    await nextTick()
    expect(wrapper.emitted('close')).toBeTruthy()

    // Parent sets open=false
    await wrapper.setProps({ open: false })
    await nextTick()

    // Then parent re-opens
    await wrapper.setProps({ open: true })
    await nextTick()

    // leaving should be reset
    expect(wrapper.find('.modal-dialog').classes()).not.toContain('modal-leaving')

    vi.useRealTimers()
  })
})
