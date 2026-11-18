// Entry point: the theme button, then the Now page.
import { startNow } from "./now.js";

// The theme follows the system unless the button chose one; the choice is remembered when
// storage is allowed (it can throw in private windows or with site data blocked).
const KEY = "skyledger-theme";
const root = document.documentElement, btn = document.getElementById("themeBtn");
const dark = matchMedia("(prefers-color-scheme: dark)");
try { const t = localStorage.getItem(KEY); if (t === "light" || t === "dark") root.dataset.theme = t; } catch { /* storage blocked */ }
const isDark = () => root.dataset.theme ? root.dataset.theme === "dark" : dark.matches;
const syncBtn = () => { btn.textContent = isDark() ? "Light" : "Dark"; btn.setAttribute("aria-label", isDark() ? "Switch to light theme" : "Switch to dark theme"); };
syncBtn();

const redraw = startNow();
btn.addEventListener("click", () => {
  root.dataset.theme = isDark() ? "light" : "dark";
  try { localStorage.setItem(KEY, root.dataset.theme); } catch { /* storage blocked */ }
  syncBtn(); redraw();
});
dark.addEventListener("change", () => { if (!root.dataset.theme) { syncBtn(); redraw(); } });
