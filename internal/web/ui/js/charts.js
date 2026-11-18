// Charts: uPlot line charts (global uPlot, loaded before the modules) and SVG stacked bars.
import { fmtTime, svgEl, showTip, hideTip } from "./fmt.js";

const css = n => getComputedStyle(document.documentElement).getPropertyValue(n).trim();
const plots = new WeakMap(); // host element: its uPlot

// lineChart draws series over t (Unix seconds) into host, replacing any chart already there.
// series: [{ label, color: "--airspace", values: [n or null], unit: " kt" }]. Nulls are gaps.
export function lineChart(host, t, series, opts = {}) {
  plots.get(host)?.destroy();
  const axis = { stroke: css("--ink-3"), grid: { stroke: css("--rule"), width: 1 }, ticks: { stroke: css("--rule"), width: 1 }, font: "13px Barlow, sans-serif" };
  const value = unit => (u, v) => v == null ? "no data" : (Math.round(v * 10) / 10).toLocaleString() + unit;
  const u = new uPlot({
    width: host.clientWidth || 500, height: opts.height || 220, legend: { show: false },
    cursor: { points: { size: 8 }, drag: { x: false, y: false } },
    scales: { x: { time: true }, y: opts.y || { range: (u, mn, mx) => [0, Math.max(5, mx * 1.1)] } },
    axes: [{ ...axis, values: (u, sp) => sp.map(v => fmtTime(v)) }, { ...axis, size: 52 }],
    series: [{}, ...series.map(s => ({ label: s.label, stroke: css(s.color), width: 2, spanGaps: false, points: { show: false }, value: value(s.unit || "") }))],
    hooks: { setCursor: [u => {
      const i = u.cursor.idx; if (i == null || u.cursor.left < 0) return hideTip();
      const r = u.over.getBoundingClientRect();
      const rows = u.series.slice(1).map((s, k) => `${u.series.length > 2 ? s.label + ": " : ""}${s.value(u, u.data[k + 1][i])}`);
      showTip(fmtTime(u.data[0][i]), rows, r.left + u.cursor.left, r.top + u.cursor.top);
    }] },
  }, [t, ...series.map(s => s.values)], host);
  u.over.addEventListener("mouseleave", hideTip);
  if (!plots.has(host)) new ResizeObserver(() => { const p = plots.get(host); if (p && host.clientWidth) p.setSize({ width: host.clientWidth, height: p.height }); }).observe(host);
  plots.set(host, u);
  return u;
}

// Stacked bars: aircraft per clock hour, by altitude band (the /api/traffic altitude block).
// Colors are CSS variables in style, so they follow the theme without a redraw.
const BANDS = [["ground", "On the ground", "--alt-1"], ["below_10k", "Under 10,000 ft", "--alt-2"],
  ["10k_30k", "10,000 to 30,000 ft", "--alt-3"], ["30k_up", "Above 30,000 ft", "--alt-4"]];
export function stackedBars(svg, altitude) {
  const W = svg.clientWidth, H = 220, L = 34, B = 24, T = 8;
  if (!W || !altitude) return;
  svg.setAttribute("viewBox", `0 0 ${W} ${H}`); svg.replaceChildren();
  const hours = altitude.t.map((t, i) => ({ t, v: BANDS.map(([k]) => altitude[k]?.[i] ?? 0) }));
  const top = Math.max(0, ...hours.map(h => h.v.reduce((a, b) => a + b, 0)));
  const step = [1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000].find(s => top / s <= 5) || 10000;
  const max = Math.max(step, Math.ceil(top / step) * step);
  const y = v => H - B - (v / max) * (H - B - T), bw = (W - L) / Math.max(1, hours.length);
  for (let g = 0; g <= max; g += step) { svg.append(svgEl("line", { class: "gl", x1: L, x2: W, y1: y(g), y2: y(g) })); svg.append(svgEl("text", { x: L - 6, y: y(g) + 4, "text-anchor": "end" }, g.toLocaleString())); }
  hours.forEach((h, k) => {
    const x = L + k * bw + 2, w = Math.max(2, bw - 4), g = svgEl("g"); let base = 0;
    h.v.forEach((v, b) => { if (v <= 0) return; const t = y(base + v), bot = y(base);
      g.append(svgEl("rect", { x, y: t, width: w, height: Math.max(0, bot - t - 2), style: `fill: var(${BANDS[b][2]})`, rx: 1.5 })); base += v; });
    g.append(svgEl("rect", { x: L + k * bw, y: T, width: bw, height: H - B - T, fill: "transparent" }));
    const end = h.t + (altitude.step_s || 3600);
    g.addEventListener("mousemove", e => showTip(`${fmtTime(h.t)} to ${fmtTime(end)}`, h.v.map((v, b) => `${BANDS[b][1]}: ${v.toLocaleString()}`).reverse(), e.clientX, e.clientY));
    g.addEventListener("mouseleave", hideTip); svg.append(g);
    if (new Date(h.t * 1000).getHours() % 4 === 0) svg.append(svgEl("text", { x: x + w / 2, y: H - 6, "text-anchor": "middle" }, fmtTime(h.t)));
  });
}
