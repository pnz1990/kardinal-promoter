// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// capture.mjs — records a page as frames (Chromium's screencast sends one whenever the
// page paints, with its timestamp) and encodes them as an animated WebP that plays
// at the speed it was recorded, plus a still PNG poster. No ffmpeg: sharp (libvips +
// libwebp) joins the frames.
import sharp from 'sharp'

/** Starts recording page; await the returned stop() for the frames [{ data: Buffer, t: seconds }]. */
export async function startCapture(page, { width, height }) {
  const cdp = await page.context().newCDPSession(page)
  const frames = []
  cdp.on('Page.screencastFrame', ({ data, metadata, sessionId }) => {
    frames.push({ data: Buffer.from(data, 'base64'), t: metadata.timestamp ?? Date.now() / 1000 })
    cdp.send('Page.screencastFrameAck', { sessionId }).catch(() => {})
  })
  await cdp.send('Page.startScreencast', { format: 'png', maxWidth: width, maxHeight: height, everyNthFrame: 1 })
  return async function stop() {
    // One last paint, so the final state is in the clip even if nothing moved since.
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))))
    await page.waitForTimeout(150)
    await cdp.send('Page.stopScreencast')
    await cdp.detach()
    return frames
  }
}

/**
 * Picks the frames to keep: at most fps per second (a later frame replaces one that
 * comes too soon, so the clip ends on the final state), each shown until the next.
 * The last one is held for holdMs.
 */
export function timeline(frames, { fps, holdMs }) {
  const kept = []
  for (const f of frames) {
    const last = kept[kept.length - 1]
    if (last && f.t - last.t < 1 / fps) last.data = f.data
    else kept.push({ ...f })
  }
  return kept.map((f, i) => ({
    data: f.data,
    t: f.t,
    delay: i + 1 < kept.length ? Math.max(20, Math.round((kept[i + 1].t - f.t) * 1000)) : holdMs,
  }))
}

/**
 * Encodes frames as an animated WebP under budget bytes: it lowers the quality,
 * then the frame rate, until the clip fits (or reports that it does not).
 * Returns { webp, quality, fps, frames, durationMs }.
 */
export async function encodeWebp(frames, { budget, width, holdMs = 1800 }) {
  const tries = [[15, 88], [15, 80], [12, 70], [12, 60], [10, 50], [8, 40], [6, 32]]
  let best
  for (const [fps, quality] of tries) {
    const kept = timeline(frames, { fps, holdMs })
    const inputs = await Promise.all(kept.map((f) => sharp(f.data).resize({ width, withoutEnlargement: true }).png().toBuffer()))
    const webp = await sharp(inputs, { join: { animated: true } })
      .webp({ quality, effort: 6, smartSubsample: true, mixed: true, loop: 0, delay: kept.map((f) => f.delay) })
      .toBuffer()
    best = { webp, quality, fps, frames: kept.length, durationMs: kept.reduce((s, f) => s + f.delay, 0) }
    if (webp.length <= budget) return best
  }
  return best
}

/** A still poster PNG, palette-quantised to stay small. */
export async function encodePoster(data, { width }) {
  return sharp(data).resize({ width, withoutEnlargement: true }).png({ palette: true, quality: 90, compressionLevel: 9, effort: 8 }).toBuffer()
}
