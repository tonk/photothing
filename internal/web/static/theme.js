// Theme: "light", "dark", or unset (follow the system). Applied before first paint.
(function () {
  var KEY = "theme", order = [null, "light", "dark"];
  function get() { try { return localStorage.getItem(KEY); } catch (e) { return null; } }
  function set(v) { try { v ? localStorage.setItem(KEY, v) : localStorage.removeItem(KEY); } catch (e) {} }
  function apply(v) {
    if (v) document.documentElement.setAttribute("data-theme", v);
    else document.documentElement.removeAttribute("data-theme");
    var l = document.querySelector("[data-theme-label]"), b = document.querySelector("[data-theme-toggle]");
    if (l) l.textContent = (b && b.dataset["l" + (v ? v[0].toUpperCase() + v.slice(1) : "System")]) || v || "system";
  }
  var cur = get();
  if (cur !== "light" && cur !== "dark") cur = null;
  apply(cur);
  document.addEventListener("DOMContentLoaded", function () {
    apply(cur);
    var b = document.querySelector("[data-theme-toggle]");
    if (b) b.addEventListener("click", function () {
      cur = order[(order.indexOf(cur) + 1) % order.length];
      set(cur); apply(cur);
    });
  });
})();
