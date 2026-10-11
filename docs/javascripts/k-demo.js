// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// k-demo.js — the UI recordings (figure.k-demo, docs/assets/demos) under reduced motion.
// The <picture> already shows the still poster when the reader asks for reduced motion;
// this adds a Play control that swaps in the animated clip, and Stop to go back.
// Without JavaScript the poster stays, which is the safe default.
(function () {
  var reduce = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)')

  function enhance(figure) {
    if (figure.querySelector('.k-demo__play')) return
    var picture = figure.querySelector('picture')
    if (!picture) return
    var stills = Array.prototype.filter.call(picture.querySelectorAll('source'), function (s) {
      return /prefers-reduced-motion/.test(s.media)
    })
    if (!stills.length) return
    var button = document.createElement('button')
    button.type = 'button'
    button.className = 'k-demo__play'
    var playing = false
    function render() {
      button.textContent = playing ? 'Stop' : 'Play'
      button.setAttribute('aria-pressed', String(playing))
      button.setAttribute('aria-label', (playing ? 'Stop' : 'Play') + ' the recording: ' + (figure.querySelector('img') || {}).alt)
    }
    button.addEventListener('click', function () {
      playing = !playing
      stills.forEach(function (s) {
        if (playing) s.remove()
        else picture.insertBefore(s, picture.firstChild)
      })
      render()
    })
    render()
    // The control sits on the picture (not the caption): wrap the picture to place it.
    var frame = document.createElement('div')
    frame.className = 'k-demo__frame'
    picture.parentNode.insertBefore(frame, picture)
    frame.appendChild(picture)
    frame.appendChild(button)
  }

  function run() {
    if (!reduce || !reduce.matches) return
    document.querySelectorAll('figure.k-demo').forEach(enhance)
  }

  // Material's instant navigation swaps pages without a load event: follow it when it is on.
  if (window.document$ && window.document$.subscribe) window.document$.subscribe(run)
  else if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', run)
  else run()
})()
