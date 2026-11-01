// SPDX-License-Identifier: Apache-2.0
// Live preview for the problem editor: posts the form to /problems/preview
// and swaps in the server-rendered fragment. The page works without it.
(function () {
  "use strict";
  var form = document.getElementById("problem-form");
  var target = document.getElementById("preview");
  if (!form || !target || !window.fetch) return;
  var timer = null;
  var seq = 0;

  function refresh() {
    var mine = ++seq;
    var token = form.elements["csrf_token"].value;
    fetch("/problems/preview", {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/x-www-form-urlencoded",
        "X-CSRF-Token": token
      },
      body: new URLSearchParams(new FormData(form)).toString()
    })
      .then(function (r) { return r.ok ? r.text() : null; })
      .then(function (html) {
        if (html !== null && mine === seq) target.innerHTML = html;
      })
      .catch(function () { /* keep the last good preview */ });
  }

  form.addEventListener("input", function () {
    clearTimeout(timer);
    timer = setTimeout(refresh, 250);
  });
})();
