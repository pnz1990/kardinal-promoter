// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// A table that scrolls sideways must be reachable with the keyboard
// (WCAG 2.1.1; axe scrollable-region-focusable). Material wraps each table in
// .md-typeset__scrollwrap: when one overflows, make it a focusable, labelled
// region; when it fits again, take that back. Runs on every page load and
// instant navigation (document$) and on resize.
(function () {
  function label(wrap) {
    var caption = wrap.querySelector("caption");
    if (caption && caption.textContent.trim()) return caption.textContent.trim();
    var node = wrap;
    while (node) {
      var prev = node.previousElementSibling;
      while (prev) {
        if (/^H[1-6]$/.test(prev.tagName)) return prev.textContent.replace(/¶/g, "").trim();
        prev = prev.previousElementSibling;
      }
      node = node.parentElement;
      if (!node || node.classList.contains("md-content")) break;
    }
    return "Table";
  }
  function update() {
    document.querySelectorAll(".md-typeset__scrollwrap").forEach(function (wrap) {
      if (wrap.scrollWidth > wrap.clientWidth + 1) {
        wrap.setAttribute("tabindex", "0");
        wrap.setAttribute("role", "region");
        wrap.setAttribute("aria-label", label(wrap) + " (table, scrolls sideways)");
      } else if (wrap.getAttribute("role") === "region") {
        wrap.removeAttribute("tabindex");
        wrap.removeAttribute("role");
        wrap.removeAttribute("aria-label");
      }
    });
  }
  var timer;
  window.addEventListener("resize", function () {
    clearTimeout(timer);
    timer = setTimeout(update, 150);
  });
  if (typeof document$ !== "undefined") {
    document$.subscribe(update);
  } else {
    document.addEventListener("DOMContentLoaded", update);
  }
})();
