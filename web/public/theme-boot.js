// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// theme-boot.js — runs before the first paint (a blocking <script> in index.html; the
// UI's Content-Security-Policy allows no inline script). Applies the theme and density
// the user saved, so the page does not flash the other theme while the app loads.
// Without a saved theme, the CSS follows the OS setting. ThemeContext takes over once
// React runs.
(function () {
  try {
    var root = document.documentElement
    var theme = localStorage.getItem('kardinal-theme')
    if (theme === 'light' || theme === 'dark') root.setAttribute('data-theme', theme)
    if (localStorage.getItem('kardinal-density') === 'compact') root.setAttribute('data-density', 'compact')
  } catch (e) {
    // Storage blocked: the OS setting applies.
  }
})()
