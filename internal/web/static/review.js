// Keyboard shortcuts and answer-latency measurement for the Review page.
// Everything works without this script; it only adds speed.
//
// Elements declare their shortcut with data-key: "1".."9", "Enter" or
// "Space". The script clicks the element that matches the key pressed.
(function () {
  "use strict";

  // Latency: time from the problem being shown to the answer being sent.
  var shownAt = performance.now();
  window.addEventListener("pageshow", function (e) {
    if (e.persisted) shownAt = performance.now();
  });
  var form = document.querySelector("form[data-latency]");
  if (form) {
    form.addEventListener("submit", function () {
      var field = form.elements.namedItem("latency_ms");
      if (field) field.value = String(Math.max(0, Math.round(performance.now() - shownAt)));
    });
  }

  document.addEventListener("keydown", function (e) {
    if (e.defaultPrevented || e.repeat || e.ctrlKey || e.metaKey || e.altKey) return;
    var key = e.key === " " ? "Space" : e.key;
    var t = e.target;
    var tag = t && t.tagName;
    var type = tag === "INPUT" ? t.type : "";
    var checkable = type === "checkbox" || type === "radio";

    if (tag === "TEXTAREA") {
      // Enter submits; Shift+Enter adds a line. Other keys type.
      if (!(key === "Enter" && !e.shiftKey && t.hasAttribute("data-submit-enter"))) return;
    } else if (tag === "INPUT" && !checkable) {
      return; // typing into a text box (Enter submits natively)
    } else if (tag === "BUTTON" || tag === "A" || tag === "SELECT") {
      if (key === "Enter" || key === "Space") return; // native activation
    } else if (checkable && key === "Space") {
      return; // native toggle
    }

    var el = document.querySelector('[data-key="' + key + '"]');
    if (!el) return;
    e.preventDefault();
    el.click();
  });
})();
