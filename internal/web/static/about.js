// The About box: a native <dialog>, opened from the status line. Esc and the Close button close it, and so
// does a click on the dimmed area around it.
(function () {
  var dlg = document.getElementById("about");
  var open = document.querySelector("[data-about-open]");
  if (!dlg || !open || typeof dlg.showModal !== "function") {
    if (open) open.hidden = true; // a browser without <dialog>: do not offer a button that does nothing
    return;
  }
  open.addEventListener("click", function () { dlg.showModal(); });
  dlg.addEventListener("click", function (e) { if (e.target === dlg) dlg.close(); });
})();
