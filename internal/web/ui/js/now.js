// The Now page: the rose, facts, "Worth a look", the in-range table and the traffic charts,
// kept current from the API. Anything from the air reaches the page as text only.
import { ago, deg3, el, fmtDate, ft, nm, svgEl } from "./fmt.js";
import { getJSON, poll } from "./api.js";
import { drawRose, drawAircraft, isEmergency, callsignOf } from "./rose.js";
import { lineChart, stackedBars } from "./charts.js";

const $ = id => document.getElementById(id);
const state = { live: null, outlines: null, traffic: null, map: null, overlay: null };

// Status and list icons, built as SVG elements.
const ICONS = {
  good: [["circle", { cx: 8, cy: 8, r: 7, fill: "var(--good)" }], ["path", { d: "M4.5 8.2l2.2 2.2 4.8-4.8", stroke: "#fff", "stroke-width": 2, fill: "none" }]],
  critical: [["path", { d: "M8 1l7 13H1z", fill: "var(--critical)" }], ["path", { d: "M8 6v4M8 11.6v.6", stroke: "#fff", "stroke-width": 1.8 }]],
  warning: [["path", { d: "M8 1l7 13H1z", fill: "var(--warning)" }], ["path", { d: "M8 6v4M8 11.6v.6", stroke: "#17222d", "stroke-width": 1.8 }]],
  info: [["circle", { cx: 8, cy: 8, r: 7, fill: "none", stroke: "var(--ink-2)", "stroke-width": 1.5 }], ["path", { d: "M8 7v4.5M8 4.6v.6", stroke: "var(--ink-2)", "stroke-width": 1.6 }]],
};
function icon(name) {
  const s = svgEl("svg", { width: 16, height: 16, viewBox: "0 0 16 16", "aria-hidden": "true" });
  for (const [tag, attrs] of ICONS[name]) s.append(svgEl(tag, attrs));
  return s;
}

// Header status from how old the newest position is (level from the server: live, stale, silent).
function setStatus(live) {
  let name, text;
  if (!live) [name, text] = ["critical", "Can't reach skyledger's database"];
  else {
    const age = live.status.age_s, min = Math.round((age ?? 0) / 60);
    [name, text] = live.status.level === "live" ? ["good", `Receiver live, last position ${Math.round(age)} s ago`]
      : live.status.level === "stale" ? ["warning", `No new positions for ${min} min`]
      : age == null ? ["critical", "No positions in the last hour"]
      : ["critical", `Receiver silent for ${min} min`];
  }
  $("status").replaceChildren(icon(name), el("span", {}, text));
}

// The rose is rebuilt when the outlines or map change (or the theme); aircraft redraw on each live poll.
function drawMainRose() {
  const o = state.outlines || {}, map = state.map?.center ? state.map : null;
  state.overlay = drawRose($("rose"), { map, allTime: o.all_time, today: o.today, furthestToday: o.furthest_today, furthestEver: o.furthest_ever });
  if (state.live) drawAircraft(state.overlay, state.live.aircraft);
}

const fact = (term, value, small) => [el("dt", {}, term), el("dd", {}, value, small ? el("small", {}, small) : null)];
const WAIT = "…";
function renderFacts() {
  const { live, outlines: o, traffic } = state, rows = [];
  if (live) { const c = live.counts; rows.push(fact("Aircraft now", (c.with_position + (c.without_position ?? 0)).toLocaleString(), `${c.with_position.toLocaleString()} with a position`)); }
  else rows.push(fact("Aircraft now", WAIT));
  if (!o) rows.push(fact("Furthest today", WAIT), fact("Furthest ever", WAIT));
  else {
    rows.push(o.furthest_today ? fact("Furthest today", nm(o.furthest_today.nm), `at ${deg3(o.furthest_today.deg)}`) : fact("Furthest today", "None yet"));
    const imported = (o.all_time || []).some(v => v != null), e = o.furthest_ever;
    rows.push(!imported || !e ? fact("Furthest ever", "", "after the first history import")
      : fact("Furthest ever", nm(e.nm), `at ${deg3(e.deg)}, ${fmtDate(e.time)}`));
  }
  if (traffic) rows.push(fact("Aircraft today", traffic.aircraft_today.toLocaleString(),
    traffic.aircraft_yesterday == null ? "so far" : `so far; ${traffic.aircraft_yesterday.toLocaleString()} yesterday`));
  else rows.push(fact("Aircraft today", WAIT));
  $("facts").replaceChildren(...rows.flat());
}

const SQUAWKS = { 7500: "Hijack", 7600: "Radio failure", 7700: "General emergency" };
const EMERGENCIES = { general: "General emergency", lifeguard: "Medical emergency", minfuel: "Minimum fuel",
  nordo: "Radio failure", unlawful: "Unlawful interference", downed: "Aircraft downed", reserved: "Emergency" };
const where = n => n.distance_nm != null && n.bearing != null ? `${nm(n.distance_nm)} at ${deg3(n.bearing)}` : "";
const csEl = n => el("span", { class: n.military ? "mil" : null }, callsignOf(n));
function notableItem(n) {
  const cs = el("b", {}, callsignOf(n));
  if (n.kind === "emergency") {
    const meaning = SQUAWKS[n.squawk] || EMERGENCIES[n.emergency] || n.emergency || "Emergency";
    cs.append(SQUAWKS[n.squawk] ? ` is squawking ${n.squawk}` : " reports an emergency");
    return ["critical", cs, [meaning, where(n), ft(n.alt)].filter(Boolean).join(", ")];
  }
  if (n.kind === "military") {
    cs.append(`, a military ${n.type || "aircraft"}`);
    return ["info", cs, n.last_heard ? `Last heard here ${ago(n.last_heard)}` : state.live.flights_ready ? "First time heard here" : "No flight history yet"];
  }
  cs.append(" is the lowest airborne aircraft");
  return ["info", cs, [ft(n.alt), where(n)].filter(Boolean).join(", ")];
}
function renderNotable() {
  const items = state.live.notable || [];
  $("notable").replaceChildren(...(items.length ? items.map(n => {
    const [name, title, line] = notableItem(n);
    return el("li", {}, icon(name), el("div", {}, title, el("br"), el("span", {}, line)));
  }) : [el("li", {}, el("span"), el("div", {}, "Nothing unusual in range right now."))]));
}

// In range now: sortable; missing values sort last whichever way.
const COLS = [["callsign", "Callsign"], ["type", "Type"], ["alt", "Altitude"], ["gs", "Speed"], ["distance_nm", "Distance"], ["bearing", "Bearing"], ["squawk", "Squawk"]];
const NUM = new Set(["alt", "gs", "distance_nm", "bearing"]);
let sortKey = "distance_nm", sortDir = 1;
const sortVal = (a, k) => k === "callsign" ? callsignOf(a) : k === "alt" && a.on_ground ? 0 : a[k];
function renderInRange() {
  const rows = [...(state.live.aircraft || [])].sort((a, b) => {
    const x = sortVal(a, sortKey), y = sortVal(b, sortKey);
    if (x == null || y == null) return (x == null) - (y == null);
    return (typeof x === "string" ? x.localeCompare(y) : x - y) * sortDir;
  });
  $("inrange-sub").textContent = rows.length ? `${rows.length.toLocaleString()} aircraft with a position. Select a column to sort.` : "";
  const head = el("tr", {}, ...COLS.map(([k, n]) => el("th", { class: NUM.has(k) ? "num" : null,
    "aria-sort": k === sortKey ? (sortDir > 0 ? "ascending" : "descending") : null }, el("button", { type: "button", "data-k": k }, n))));
  const body = rows.length ? rows.map(a => el("tr", {},
    el("td", {}, csEl(a)),
    el("td", {}, a.type ?? ""),
    el("td", { class: "num" }, a.on_ground ? "ground" : ft(a.alt)),
    el("td", { class: "num" }, a.gs != null ? Math.round(a.gs).toLocaleString() + " kt" : ""),
    el("td", { class: "num" }, nm(a.distance_nm)),
    el("td", { class: "num" }, deg3(a.bearing)),
    el("td", {}, isEmergency(a) && a.squawk ? icon("critical") : null, a.squawk ? (isEmergency(a) ? " " : "") + a.squawk : ""),
  )) : [el("tr", {}, el("td", { colspan: COLS.length }, "Nothing in range right now."))];
  $("inrange").replaceChildren(el("thead", {}, head), el("tbody", {}, ...body));
}

function renderTraffic() {
  const { tracked, altitude } = state.traffic;
  lineChart($("c-tracked"), tracked.t, [{ label: "Aircraft", color: "--airspace", values: tracked.aircraft }]);
  stackedBars($("c-alt"), altitude);
}

// redraw repaints everything from the last data (after a theme change, the colors are re-read).
function redraw() {
  drawMainRose();
  if (state.traffic) renderTraffic();
}

export function startNow() {
  $("inrange").addEventListener("click", e => {
    const b = e.target.closest("button[data-k]"); if (!b || !state.live) return;
    sortDir = sortKey === b.dataset.k ? -sortDir : 1; sortKey = b.dataset.k;
    renderInRange(); $("inrange").querySelector(`button[data-k="${sortKey}"]`).focus();
  });
  new ResizeObserver(() => { if (state.traffic) stackedBars($("c-alt"), state.traffic.altitude); }).observe($("c-alt"));
  drawMainRose(); renderFacts();

  poll("api/live", 10000, live => {
    state.live = live; setStatus(live); renderFacts(); renderNotable(); renderInRange();
    drawAircraft(state.overlay, live.aircraft);
  }, () => setStatus(null)); // keep the last data on screen
  poll("api/outlines", 60000, o => { state.outlines = o; drawMainRose(); renderFacts(); });
  poll("api/traffic", 60000, t => { state.traffic = t; renderTraffic(); renderFacts(); });
  getJSON("api/map").then(m => { state.map = m; $("map-hint").hidden = !!m.center; if (m.center) drawMainRose(); }).catch(() => {});
  return redraw;
}
