// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// k-demo.js — the UI recordings (figure.k-demo, docs/assets/demos).
// Each <picture> lists its sources in a fixed order: reduced-motion stills first (dark, then
// light), then the animated clips (dark, then light), then the <img>. This script:
//   - follows the site's theme (Material's data-md-color-scheme on <body>, "slate" is dark)
//     rather than only the OS's prefers-color-scheme, so a reader who picked a palette sees
//     the matching clip;
//   - under reduced motion, adds a Play control that swaps in the animated clip, and Stop
//     to go back to the still.
// It rebuilds each picture's sources from the original list, so the order never drifts.
// Without JavaScript the <picture> media queries apply, which is the safe default.
(function () {
  var reduce = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)')
  var DARK = /\s*(and\s*)?\(prefers-color-scheme:\s*dark\)\s*(and\s*)?/
  var figures = []

  // The theme the site shows: 'dark', 'light', or '' when the page does not say.
  function scheme() {
    var s = document.body && document.body.getAttribute('data-md-color-scheme')
    if (!s) return ''
    return s === 'slate' ? 'dark' : 'light'
  }

  // A source's media for the site theme: a dark source applies only when the site is dark.
  function mediaFor(original, theme) {
    if (!DARK.test(original) || !theme) return original
    if (theme === 'light') return 'not all'
    var rest = original.replace(DARK, ' ').trim()
    return rest || 'all'
  }

  function render(f) {
    var theme = scheme()
    f.sources.forEach(function (s) { if (s.el.parentNode) s.el.remove() })
    f.sources.forEach(function (s) {
      if (f.playing && s.still) return
      s.el.media = mediaFor(s.media, theme)
      f.picture.insertBefore(s.el, f.img)
    })
    if (f.button) {
      f.button.textContent = f.playing ? 'Stop' : 'Play'
      f.button.setAttribute('aria-pressed', String(f.playing))
      f.button.setAttribute('aria-label', (f.playing ? 'Stop' : 'Play') + ' the recording: ' + (f.img.alt || ''))
    }
  }

  function enhance(figure) {
    if (figure.getAttribute('data-k-demo')) return
    var picture = figure.querySelector('picture')
    var img = picture && picture.querySelector('img')
    if (!img) return
    figure.setAttribute('data-k-demo', 'on')
    var f = {
      picture: picture,
      img: img,
      playing: false,
      sources: Array.prototype.map.call(picture.querySelectorAll('source'), function (el) {
        return { el: el, media: el.media, still: /prefers-reduced-motion/.test(el.media) }
      }),
    }
    figures.push(f)
    if (reduce && reduce.matches && f.sources.some(function (s) { return s.still })) {
      var button = document.createElement('button')
      button.type = 'button'
      button.className = 'k-demo__play'
      button.addEventListener('click', function () { f.playing = !f.playing; render(f) })
      f.button = button
      // The control sits on the picture (not the caption): wrap the picture to place it.
      var frame = document.createElement('div')
      frame.className = 'k-demo__frame'
      picture.parentNode.insertBefore(frame, picture)
      frame.appendChild(picture)
      frame.appendChild(button)
    }
    render(f)
  }

  function run() {
    figures = figures.filter(function (f) { return document.contains(f.img) })
    document.querySelectorAll('figure.k-demo').forEach(enhance)
  }

  // Follow the palette toggle.
  if (window.MutationObserver && document.body) {
    new MutationObserver(function () { figures.forEach(render) })
      .observe(document.body, { attributes: true, attributeFilter: ['data-md-color-scheme'] })
  }

  // Material's instant navigation swaps pages without a load event: follow it when it is on.
  if (window.document$ && window.document$.subscribe) window.document$.subscribe(run)
  else if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', run)
  else run()
})()
