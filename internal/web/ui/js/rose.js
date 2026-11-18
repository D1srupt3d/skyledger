// The compass rose: rings, ticks, the map, the reception outlines and the aircraft in range.
import { deg3, ft, nm, svgEl, showTip, hideTip } from "./fmt.js";

const R = 280, C = 320, MAXNM = 300;
// ponytail: fixed 300 nm scale (as the demo); anything further is pinned to the edge. Scale to the data if receivers often exceed it.
const xy = (brg, d) => { const a = (brg - 90) * Math.PI / 180, r = Math.min(d, MAXNM) / MAXNM * R; return [C + r * Math.cos(a), C + r * Math.sin(a)]; };
const pt = (brg, d) => xy(brg, d).map(v => v.toFixed(1)).join(",");
const fromEN = (e, n) => [(Math.atan2(e, n) * 180 / Math.PI + 360) % 360, Math.hypot(e, n)];

// Points of an outline (360 values, null where nothing was heard), through the degrees that have a value.
const outlinePoints = arr => (arr || []).flatMap((d, deg) => d == null ? [] : [pt(deg, d)]);
const furthest = arr => (arr || []).reduce((best, d, deg) => d != null && (best == null || d > best.nm) ? { nm: d, deg } : best, null);

export function drawRose(svg, { map, allTime, today, furthestToday } = {}) {
  svg.replaceChildren();
  const id = svg.id;
  const defs = svgEl("defs"), pat = svgEl("pattern", { id: id + "-hatch", width: 7, height: 7, patternUnits: "userSpaceOnUse", patternTransform: "rotate(45)" });
  pat.append(svgEl("line", { x1: 0, y1: 0, x2: 0, y2: 7, style: "stroke: var(--airway)", "stroke-width": 1.2, "stroke-opacity": 0.35 }));
  const clip = svgEl("clipPath", { id: id + "-clip" }); clip.append(svgEl("circle", { cx: C, cy: C, r: R }));
  defs.append(pat, clip); svg.append(defs);
  if (map) drawMap(svg, map, id + "-clip");
  for (let d = 50; d <= 250; d += 50) {
    svg.append(svgEl("circle", { class: "ring", cx: C, cy: C, r: d / MAXNM * R }));
    const [x, y] = xy(0, d); svg.append(svgEl("text", { x: x + 4, y: y + 13, "font-size": 12 }, d));
  }
  svg.append(svgEl("circle", { class: "ring", cx: C, cy: C, r: R }));
  for (let d = 0; d < 360; d += 5) {
    const len = d % 30 === 0 ? 14 : d % 10 === 0 ? 9 : 5, a = (d - 90) * Math.PI / 180;
    const [x1, y1] = xy(d, MAXNM);
    svg.append(svgEl("line", { class: "tick", x1, y1, x2: C + (R - len) * Math.cos(a), y2: C + (R - len) * Math.sin(a), "stroke-width": d % 30 === 0 ? 1.6 : 1 }));
    if (d % 30 === 0) {
      const label = { 0: "N", 90: "E", 180: "S", 270: "W" }[d] ?? String(d).padStart(3, "0");
      svg.append(svgEl("text", { x: C + (R + 18) * Math.cos(a), y: C + (R + 18) * Math.sin(a) + 5, "text-anchor": "middle",
        "font-size": label.length === 1 ? 19 : 14, class: label.length === 1 ? "card-pt" : null }, label));
    }
  }
  const ever = outlinePoints(allTime), now = outlinePoints(today);
  if (ever.length >= 3) svg.append(svgEl("polygon", { class: "ever", fill: `url(#${id}-hatch)`, points: ever.join(" ") }));
  if (now.length >= 3) svg.append(svgEl("polygon", { class: "today", points: now.join(" ") }));
  // Name each outline where it reaches furthest, in its own color, instead of a separate key.
  // All-time sits just outside its line and reads outward; Today sits just inside its line and reads inward.
  const label = (text, cls, at, outside) => {
    const [x, y] = xy(at.deg, outside ? Math.min(at.nm + 10, MAXNM - 6) : at.nm - 10);
    svg.append(svgEl("text", { class: "line-label " + cls, x, y: y + 5, "text-anchor": (at.deg > 180) === outside ? "end" : "start" }, text));
  };
  // The All-time label goes where the drawn outline peaks: furthest_ever can be today's record, which
  // the all-time outline (built from history imports) does not reach yet.
  const everTop = furthest(allTime);
  if (ever.length >= 3 && everTop) label("All-time", "all-time", everTop, true);
  if (now.length >= 3 && furthestToday) label("Today", "today-l", furthestToday, false);
  const g = svgEl("g"); svg.append(g);
  svg.append(svgEl("circle", { class: "rx", cx: C, cy: C, r: 5 }));
  return g;
}

// The map: land, borders and cities, already projected to nm east and north of the receiver
// (azimuthal equidistant), so distance and bearing match the rings exactly.
const mapPath = rings => rings.map(r => "M" + r.map(([e, n]) => pt(...fromEN(e, n))).join("L")).join("");
function drawMap(svg, map, clipId) {
  const g = svgEl("g", { "clip-path": `url(#${clipId})`, "aria-hidden": "true" });
  g.append(svgEl("circle", { class: "water", cx: C, cy: C, r: R }));
  g.append(svgEl("path", { class: "land", d: mapPath((map.land || []).map(r => r.concat([r[0]]))) }));
  g.append(svgEl("path", { class: "border", d: mapPath(map.borders || []) }));
  const placed = [];
  for (const c of map.cities || []) {
    const [brg, dist] = fromEN(c.e, c.n);
    if (dist > MAXNM - 12 || placed.some(([e, n]) => Math.hypot(e - c.e, n - c.n) < 28)) continue; // keep labels apart
    placed.push([c.e, c.n]);
    const [x, y] = xy(brg, dist);
    g.append(svgEl("rect", { class: "city", x: x - 2.5, y: y - 2.5, width: 5, height: 5 }));
    g.append(svgEl("text", { class: "city-label", x: x + 6, y: y + 4 }, c.name));
  }
  svg.append(g);
}

const EMERGENCY_SQUAWKS = new Set(["7500", "7600", "7700"]);
export const isEmergency = a => EMERGENCY_SQUAWKS.has(a.squawk) || a.emergency != null;
export const callsignOf = a => a.callsign || a.hex;

function planeTip(a) {
  return [
    [a.type, a.military ? "military" : null].filter(Boolean).join(", "),
    [a.on_ground ? "on the ground" : ft(a.alt), a.gs != null ? Math.round(a.gs) + " kt" : ""].filter(Boolean).join(", "),
    `${nm(a.distance_nm)} at ${deg3(a.bearing)}`,
    a.squawk ? `Squawking ${a.squawk}${EMERGENCY_SQUAWKS.has(a.squawk) ? " (emergency)" : ""}` : "",
  ];
}

// drawAircraft replaces the overlay's contents with each aircraft's 5-minute trail and symbol.
// Aircraft with no distance or bearing (no receiver location, or no position) are not drawn.
export function drawAircraft(g, aircraft) {
  g.replaceChildren();
  const here = (aircraft || []).filter(a => a.distance_nm != null && a.bearing != null);
  for (const a of here) {
    const trail = (a.trail || []).map(([b, d]) => pt(b, d));
    trail.push(pt(a.bearing, a.distance_nm));
    if (trail.length > 1) g.append(svgEl("polyline", { class: "trail", points: trail.join(" ") }));
  }
  for (const a of here) {
    const [x, y] = xy(a.bearing, a.distance_nm), cs = callsignOf(a);
    const p = svgEl("g", { class: "plane" + (isEmergency(a) ? " alert" : a.military ? " mil" : ""), tabindex: 0, role: "img",
      transform: `translate(${x.toFixed(1)},${y.toFixed(1)})`, "aria-label": [cs, ft(a.alt), `${nm(a.distance_nm)} at ${deg3(a.bearing)}`].filter(Boolean).join(", ") });
    p.append(svgEl("path", { d: "M0,-8 L5.5,6 L0,3 L-5.5,6 Z", transform: a.track != null ? `rotate(${Math.round(a.track)})` : null }));
    p.append(svgEl("text", { x: 9, y: 4 }, cs));
    p.addEventListener("mousemove", e => showTip(cs, planeTip(a), e.clientX, e.clientY));
    p.addEventListener("mouseleave", hideTip);
    p.addEventListener("focus", () => { const r = p.getBoundingClientRect(); showTip(cs, planeTip(a), r.right, r.top); });
    p.addEventListener("blur", hideTip);
    g.append(p);
  }
}
