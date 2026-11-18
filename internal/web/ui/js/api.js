// The JSON API. Paths are relative ("api/live") so the UI also works under a proxy subpath.

export async function getJSON(path) {
  const r = await fetch(path, { cache: "no-store" });
  if (!r.ok) throw new Error(`${path}: HTTP ${r.status}`);
  return r.json();
}

// poll fetches path now and every everyMs, skipping while the tab is hidden and catching up
// as soon as it is shown again. Returns a function that stops it.
export function poll(path, everyMs, onData, onError = () => {}) {
  let timer = null, busy = false, stopped = false;
  const tick = async () => {
    if (busy || stopped) return; // one request at a time, one timer at a time
    clearTimeout(timer);
    busy = true;
    if (!document.hidden) {
      try { onData(await getJSON(path)); } catch (e) { onError(e); }
    }
    busy = false;
    if (!stopped) timer = setTimeout(tick, everyMs);
  };
  const onShow = () => { if (!document.hidden && !stopped) tick(); };
  document.addEventListener("visibilitychange", onShow);
  tick();
  return () => { stopped = true; clearTimeout(timer); document.removeEventListener("visibilitychange", onShow); };
}
