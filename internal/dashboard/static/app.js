// Queue Console — client. Wires SSE stream + stats cards + live jobs table.
(() => {
  const $ = (id) => document.getElementById(id);

  const TOKEN_KEY = "queue.token";
  let token = localStorage.getItem(TOKEN_KEY) || "";
  let stream = null;
  let stateFilter = "";

  // Rolling throughput window (60 samples, one per ~1s of SSE stats events).
  const MAX = 60;
  let lastStats = null;
  const subSeries = new Array(MAX).fill(0);
  const ackSeries = new Array(MAX).fill(0);

  // ---------- Gate ----------
  const gate = $("gate");
  const mainEls = [document.querySelector(".topbar"), document.querySelector(".wrap")];

  function showApp() {
    gate.hidden = true;
    mainEls.forEach((e) => (e.hidden = false));
    connect();
    refreshJobs();
  }
  function showGate() {
    gate.hidden = false;
    mainEls.forEach((e) => (e.hidden = true));
    if (stream) { stream.close(); stream = null; }
  }

  $("gate-form").addEventListener("submit", (e) => {
    e.preventDefault();
    token = $("token-input").value.trim();
    if (!token) return;
    localStorage.setItem(TOKEN_KEY, token);
    showApp();
  });
  $("logout").addEventListener("click", () => {
    localStorage.removeItem(TOKEN_KEY);
    token = "";
    showGate();
  });

  // ---------- SSE ----------
  const indicator = $("stream-indicator");
  const indLabel = $("stream-label");
  function setStream(state) {
    indicator.classList.remove("pill-dim", "pill-ok", "pill-err");
    if (state === "live")     { indicator.classList.add("pill-ok");  indLabel.textContent = "live"; }
    else if (state === "err") { indicator.classList.add("pill-err"); indLabel.textContent = "disconnected"; }
    else                      { indicator.classList.add("pill-dim"); indLabel.textContent = "connecting…"; }
  }

  function connect() {
    if (stream) stream.close();
    setStream("connecting");
    stream = new EventSource(`/events?token=${encodeURIComponent(token)}`);
    stream.addEventListener("stats", (e) => {
      setStream("live");
      const s = JSON.parse(e.data);
      paintStats(s);
      pushThroughput(s);
    });
    stream.addEventListener("job", (e) => {
      // Job state changed — refresh the table. Debounced below.
      scheduleJobsRefresh();
    });
    stream.onerror = () => setStream("err");
  }

  // ---------- Stats cards ----------
  function paintStats(s) {
    $("s-total").textContent     = s.total;
    $("s-runnable").textContent  = s.runnable;
    $("s-running").textContent   = s.by_state?.RUNNING ?? 0;
    $("s-scheduled").textContent = s.scheduled;
    $("s-dead").textContent      = s.by_state?.DEAD ?? 0;

    $("c-submits").textContent = s.total_submits;
    $("c-acks").textContent    = s.total_acks;
    $("c-retries").textContent = s.total_retries;
    $("c-dead").textContent    = s.total_dead;
  }

  function pushThroughput(s) {
    if (lastStats) {
      const dt = Math.max(1, (new Date(s.snapshot_at) - new Date(lastStats.snapshot_at)) / 1000);
      const dSub = Math.max(0, (s.total_submits - lastStats.total_submits) / dt);
      const dAck = Math.max(0, (s.total_acks    - lastStats.total_acks)    / dt);
      subSeries.shift(); subSeries.push(dSub);
      ackSeries.shift(); ackSeries.push(dAck);
      drawSpark();
    }
    lastStats = s;
  }

  // ---------- Sparkline (no-dep canvas) ----------
  const canvas = $("spark");
  const ctx = canvas.getContext("2d");

  function resizeCanvas() {
    const dpr = window.devicePixelRatio || 1;
    const w = canvas.clientWidth, h = canvas.clientHeight;
    canvas.width = w * dpr; canvas.height = h * dpr;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  }
  window.addEventListener("resize", () => { resizeCanvas(); drawSpark(); });

  function drawSeries(series, color) {
    const w = canvas.clientWidth, h = canvas.clientHeight;
    const max = Math.max(1, ...subSeries, ...ackSeries);
    ctx.beginPath();
    series.forEach((v, i) => {
      const x = (i / (MAX - 1)) * w;
      const y = h - (v / max) * (h - 12) - 6;
      if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    });
    ctx.strokeStyle = color; ctx.lineWidth = 2; ctx.lineJoin = "round"; ctx.stroke();

    // fill
    ctx.lineTo(w, h); ctx.lineTo(0, h); ctx.closePath();
    const g = ctx.createLinearGradient(0, 0, 0, h);
    g.addColorStop(0, color + "55"); g.addColorStop(1, color + "00");
    ctx.fillStyle = g; ctx.fill();
  }

  function drawSpark() {
    const w = canvas.clientWidth, h = canvas.clientHeight;
    ctx.clearRect(0, 0, w, h);
    drawSeries(ackSeries, "#86efac");
    drawSeries(subSeries, "#7dd3fc");
  }

  // ---------- Jobs table ----------
  let refreshHandle = null;
  function scheduleJobsRefresh() {
    if (refreshHandle) return;
    refreshHandle = setTimeout(() => { refreshHandle = null; refreshJobs(); }, 250);
  }

  async function refreshJobs() {
    const url = new URL("/jobs", window.location.origin);
    if (stateFilter) url.searchParams.set("state", stateFilter);
    url.searchParams.set("limit", "50");
    try {
      const res = await fetch(url, { headers: { Authorization: `Bearer ${token}` } });
      if (res.status === 401) { showGate(); return; }
      const jobs = await res.json();
      renderJobs(jobs || []);
    } catch (e) {
      // swallow — SSE will retry
    }
  }

  const knownIDs = new Set();
  function renderJobs(jobs) {
    const body = $("jobs-body");
    if (!jobs.length) {
      body.innerHTML = `<tr><td colspan="6" class="muted center">no jobs</td></tr>`;
      return;
    }
    body.innerHTML = jobs.map(j => {
      const isNew = !knownIDs.has(j.job_id);
      knownIDs.add(j.job_id);
      return `
      <tr class="${isNew ? "flash-new" : ""}">
        <td title="${j.job_id}">${j.job_id.slice(0, 8)}</td>
        <td><span class="state-pill state-${j.state}">${j.state}</span></td>
        <td>${j.priority}</td>
        <td>${j.attempts}/${j.max_retries}</td>
        <td>${fmtTime(j.run_at)}</td>
        <td>${fmtRel(j.updated_at)}</td>
      </tr>`;
    }).join("");
  }

  function fmtTime(iso) {
    if (!iso) return "—";
    const d = new Date(iso);
    return d.toLocaleTimeString([], { hour12: false });
  }
  function fmtRel(iso) {
    if (!iso) return "—";
    const d = new Date(iso);
    const s = Math.max(0, (Date.now() - d.getTime()) / 1000);
    if (s < 60)   return `${Math.round(s)}s ago`;
    if (s < 3600) return `${Math.round(s/60)}m ago`;
    return `${Math.round(s/3600)}h ago`;
  }

  $("state-filter").addEventListener("change", (e) => {
    stateFilter = e.target.value;
    refreshJobs();
  });

  // ---------- Submit form ----------
  $("submit-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const f = e.target;
    const body = {
      payload:     f.payload.value,
      priority:    Number(f.priority.value) || 0,
      max_retries: Number(f.max_retries.value) || 0,
      delay_ms:    Number(f.delay_ms.value) || 0,
    };
    const res = await fetch("/jobs", {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
      body: JSON.stringify(body),
    });
    const out = $("submit-status");
    if (res.ok) {
      const { job_id } = await res.json();
      out.textContent = `enqueued ${job_id.slice(0,8)}…`;
      out.style.color = "var(--ok)";
    } else {
      const err = await res.text();
      out.textContent = `error: ${err}`;
      out.style.color = "var(--err)";
    }
    setTimeout(() => { out.textContent = ""; }, 4000);
  });

  // ---------- Boot ----------
  resizeCanvas();
  if (token) showApp(); else showGate();

  // Periodically refresh "updated X ago" strings and poll jobs as a fallback.
  setInterval(() => { if (!gate.hidden) return; refreshJobs(); }, 5000);
})();
