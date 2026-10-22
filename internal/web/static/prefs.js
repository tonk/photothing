// Submit the per-page selector as soon as it changes (the Apply button covers browsers without scripting).
document.addEventListener("change", function (e) {
  var el = e.target;
  if (el && el.matches && el.matches("select[data-autosubmit]") && el.form) el.form.submit();
});
