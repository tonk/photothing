// Keyboard navigation on the photo page: ← → between photos, Esc back to the album.
(function () {
  var v = document.querySelector(".viewer");
  if (!v) return;
  document.addEventListener("keydown", function (e) {
    var tag = e.target && e.target.tagName;
    if (tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA" || tag === "VIDEO") return;
    if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
    var t = e.key === "ArrowLeft" ? v.dataset.prev : e.key === "ArrowRight" ? v.dataset.next : e.key === "Escape" ? v.dataset.up : null;
    if (t) window.location.href = t;
  });
})();
