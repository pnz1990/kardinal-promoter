// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// director.mjs — paces a scripted flow so a person can follow it: a visible cursor
// that glides to each target, a ripple on every click, a keycap caption for
// shortcuts, and pauses long enough to read. Headless Chromium draws no pointer, so
// the cursor is a page overlay (CURSOR_SCRIPT), injected before the app loads.

/** Runs in the page (context.addInitScript): the cursor, the click ripple and the keycap. */
export function CURSOR_SCRIPT() {
  const install = () => {
    if (document.getElementById('k-demo-cursor')) return
    const style = document.createElement('style')
    style.textContent = `
      #k-demo-cursor { position: fixed; left: 0; top: 0; z-index: 2147483647; pointer-events: none;
        width: 22px; height: 22px; transform: translate(-100px, -100px); will-change: transform;
        filter: drop-shadow(0 1px 1.5px rgba(0,0,0,.45)); }
      .k-demo-ripple { position: fixed; z-index: 2147483646; pointer-events: none; width: 34px; height: 34px;
        margin: -17px 0 0 -17px; border-radius: 50%; border: 2px solid #C0151B; opacity: .9;
        animation: k-demo-ripple 520ms ease-out forwards; }
      @keyframes k-demo-ripple { from { transform: scale(.35); } to { transform: scale(1.4); opacity: 0; } }
      #k-demo-keys { position: fixed; left: 50%; bottom: 36px; z-index: 2147483647; pointer-events: none;
        transform: translateX(-50%); display: flex; gap: 6px; padding: 8px 12px; border-radius: 10px;
        background: rgba(20,24,23,.88); color: #fff; font: 600 15px/1 ui-monospace, monospace;
        opacity: 0; transition: opacity 160ms; }
      #k-demo-keys.on { opacity: 1; }
      #k-demo-keys kbd { padding: 5px 9px; border: 1px solid rgba(255,255,255,.35); border-radius: 6px; }`
    document.head.appendChild(style)
    const cursor = document.createElement('div')
    cursor.id = 'k-demo-cursor'
    cursor.innerHTML = '<svg viewBox="0 0 22 22" width="22" height="22"><path d="M3 2 L3 18 L7.5 14 L10.5 20.5 L13.4 19.2 L10.5 12.8 L16.5 12.8 Z" fill="#141817" stroke="#fff" stroke-width="1.4" stroke-linejoin="round"/></svg>'
    document.body.appendChild(cursor)
    const keys = document.createElement('div')
    keys.id = 'k-demo-keys'
    document.body.appendChild(keys)
    let at = { x: -100, y: -100 }
    window.__kDemo = {
      at: () => at,
      move(x, y, ms) {
        cursor.style.transition = ms ? `transform ${ms}ms cubic-bezier(.45,.05,.25,1)` : 'none'
        cursor.style.transform = `translate(${x - 3}px, ${y - 2}px)`
        at = { x, y }
      },
      ripple(x, y) {
        const r = document.createElement('div')
        r.className = 'k-demo-ripple'
        r.style.left = `${x}px`
        r.style.top = `${y}px`
        document.body.appendChild(r)
        setTimeout(() => r.remove(), 600)
      },
      keys(labels) {
        keys.replaceChildren(...labels.map((l) => { const k = document.createElement('kbd'); k.textContent = l; return k }))
        keys.classList.add('on')
        clearTimeout(window.__kDemoKeysTimer)
        window.__kDemoKeysTimer = setTimeout(() => keys.classList.remove('on'), 1100)
      },
    }
  }
  if (document.body) install()
  else document.addEventListener('DOMContentLoaded', install)
}

export class Director {
  /** @param {import('playwright').Page} page */
  constructor(page, { speed = 1 } = {}) {
    this.page = page
    this.speed = speed
    this.posterAt = undefined
  }

  /** Waits ms (scaled by speed), letting the page animate. */
  async pause(ms = 900) {
    await this.page.waitForTimeout(Math.round(ms / this.speed))
  }

  /** Marks the current moment as the clip's poster (the still for reduced motion). */
  poster() {
    this.posterAt = Date.now()
  }

  /** The cursor's current position, after the page has (re)loaded the overlay. */
  async #at() {
    await this.page.waitForFunction(() => !!window.__kDemo)
    return this.page.evaluate(() => window.__kDemo.at())
  }

  /** Glides the cursor to the middle of target (a locator) or to {x, y}, and hovers there. */
  async move(target, { offset } = {}) {
    let x, y
    if ('x' in target) ({ x, y } = target)
    else {
      await target.scrollIntoViewIfNeeded()
      const box = await target.boundingBox()
      if (!box) throw new Error(`record-demos: ${target} has no box to move to`)
      x = box.x + (offset?.x ?? box.width / 2)
      y = box.y + (offset?.y ?? box.height / 2)
    }
    const from = await this.#at()
    const dist = from.x < 0 ? 400 : Math.hypot(x - from.x, y - from.y)
    const ms = Math.round(Math.min(950, Math.max(320, dist * 0.9)) / this.speed)
    await this.page.evaluate(([x, y, ms]) => window.__kDemo.move(x, y, ms), [x, y, ms])
    await this.page.waitForTimeout(ms)
    await this.page.mouse.move(x, y)
    return { x, y }
  }

  /** Moves to target and clicks it with a ripple. modifiers: ['Shift'], for example. */
  async click(target, { modifiers = [], offset, after = 700 } = {}) {
    const { x, y } = await this.move(target, { offset })
    await this.pause(180)
    for (const m of modifiers) await this.page.keyboard.down(m)
    await this.page.evaluate(([x, y]) => window.__kDemo.ripple(x, y), [x, y])
    await this.page.mouse.click(x, y)
    for (const m of modifiers.toReversed()) await this.page.keyboard.up(m)
    await this.pause(after)
  }

  /** Types text into the focused control at a readable pace. */
  async type(text, { delay = 85 } = {}) {
    await this.page.keyboard.type(text, { delay: Math.round(delay / this.speed) })
  }

  /** Presses a shortcut and shows its keycaps, e.g. press('Control+K', ['Ctrl', 'K']). */
  async press(key, caption) {
    if (caption) await this.page.evaluate((c) => window.__kDemo.keys(c), caption)
    await this.pause(250)
    await this.page.keyboard.press(key)
    await this.pause(500)
  }

  /** Scrolls the page under the cursor by dy pixels, in readable steps. */
  async scroll(dy, { steps = 8 } = {}) {
    for (let i = 0; i < steps; i++) {
      await this.page.mouse.wheel(0, dy / steps)
      await this.page.waitForTimeout(Math.round(90 / this.speed))
    }
    await this.pause(400)
  }
}
