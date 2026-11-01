document.getElementById("select-all")?.addEventListener("change", function (e) {
  document.querySelectorAll('input[name="id"][form="bulk"]').forEach(function (c) {
    c.checked = e.target.checked;
  });
});
