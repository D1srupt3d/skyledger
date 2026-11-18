// Formatting and DOM helpers. Times are Unix seconds or RFC 3339 strings and show in the
// browser's time zone. Every helper returns "" for a missing (null) value.

const toDate = t => new Date(typeof t === "number" ? t * 1000 : t);
const known = v => v != null && !Number.isNaN(v);

export const fmtTime = t => known(t) ? toDate(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : "";
export const fmtDate = t => known(t) ? toDate(t).toLocaleDateString([], { day: "numeric", month: "long" }) : "";
export const deg3 = d => known(d) ? String(Math.round(d) % 360).padStart(3, "0") + "°" : "";
export const ft = a => known(a) ? Math.round(a).toLocaleString() + " ft" : "";
export const nm = d => known(d) ? Math.round(d).toLocaleString() + " nm" : "";

// "3 days ago", "5 hours ago", "12 min ago".
export function ago(t) {
  if (!known(t)) return "";
  const s = Math.max(0, (Date.now() - toDate(t).getTime()) / 1000);
  const n = (v, one, many) => `${v} ${v === 1 ? one : many} ago`;
  if (s >= 86400) return n(Math.floor(s / 86400), "day", "days");
  if (s >= 3600) return n(Math.floor(s / 3600), "hour", "hours");
  return Math.floor(s / 60) + " min ago";
}

// el("td", { class: "num" }, "3,000 ft", otherNode): attributes go through setAttribute and
// text children become text nodes, so nothing passed here is ever parsed as HTML.
export function el(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const k in attrs) if (attrs[k] != null) e.setAttribute(k, attrs[k]);
  e.append(...children.filter(c => c != null && c !== false));
  return e;
}

export function svgEl(tag, attrs = {}, text) {
  const e = document.createElementNS("http://www.w3.org/2000/svg", tag);
  for (const k in attrs) if (attrs[k] != null) e.setAttribute(k, attrs[k]);
  if (text != null) e.textContent = text;
  return e;
}

// The floating tooltip (#tip): a bold title, then one line per entry.
export function showTip(title, lines, x, y) {
  const tip = document.getElementById("tip");
  tip.replaceChildren(el("b", {}, title));
  for (const l of lines) if (l) tip.append(el("br"), l);
  tip.style.display = "block";
  const r = tip.getBoundingClientRect();
  tip.style.left = Math.min(x + 14, innerWidth - r.width - 8) + "px";
  tip.style.top = Math.min(y + 14, innerHeight - r.height - 8) + "px";
}
export const hideTip = () => { document.getElementById("tip").style.display = "none"; };
