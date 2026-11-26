"use strict";
// Anyserve web UI. Talks to the API through `anyserve proxy`, which needs
// the per-run token from the address it printed (#token=...).
(function () {
  const params = new URLSearchParams(location.hash.slice(1));
  if (params.get("token")) {
    try { sessionStorage.setItem("anyserve-token", params.get("token")); } catch (e) {}
    history.replaceState(null, "", location.pathname);
  }
  let token = params.get("token");
  try { token = token || sessionStorage.getItem("anyserve-token"); } catch (e) {}

  const $ = (s) => document.querySelector(s);
  const el = (tag, props, ...kids) => {
    const n = document.createElement(tag);
    Object.assign(n, props || {});
    for (const k of kids) n.append(k);
    return n;
  };
  let current = null, cwd = "", instances = [], logTimer = null, node = "";
  // Calls about servers go to the chosen machine; the manager forwards them.
  const onNode = (path) => node && /^\/v1\/(instances|catalog|jobs|reservations)(\/|\?|$)/.test(path) ? "/v1/nodes/" + encodeURIComponent(node) + path : path;

  async function api(method, path, body, raw) {
    const opts = { method, headers: { "X-Anyserve-Token": token || "" } };
    if (body !== undefined) {
      if (body instanceof Blob || body instanceof File) opts.body = body;
      else { opts.body = JSON.stringify(body); opts.headers["Content-Type"] = "application/json"; }
    }
    const r = await fetch("/api" + onNode(path), opts);
    if (raw) {
      if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || r.statusText);
      return r;
    }
    const ct = r.headers.get("Content-Type") || "";
    const data = ct.includes("json") ? await r.json() : await r.text();
    if (!r.ok) { const e = new Error(data.error || data || r.statusText); e.log = data.log; throw e; }
    return data;
  }

  function toast(msg, bad) {
    const t = $("#toast");
    t.textContent = msg; t.className = "toast" + (bad ? " bad" : ""); t.hidden = false;
    clearTimeout(toast.t); toast.t = setTimeout(() => (t.hidden = true), bad ? 8000 : 3000);
  }
  const stateClass = (s) => "s-" + (s || "unknown").split(" ")[0];
  const size = (n) => n < 1024 ? n + " B" : n < 1048576 ? (n / 1024).toFixed(1) + " KB" : n < 1073741824 ? (n / 1048576).toFixed(1) + " MB" : (n / 1073741824).toFixed(2) + " GB";
  const when = (s) => s && !s.startsWith("0001") ? new Date(s).toLocaleString() : "";
  const enc = encodeURIComponent;

  async function loadList() {
    try {
      instances = await api("GET", "/v1/instances");
    } catch (e) { toast(e.message, true); return; }
    const box = $("#instances");
    box.replaceChildren();
    for (const i of instances) {
      const b = el("button", { className: "card", role: "listitem" },
        el("div", {}, el("span", { className: "name", textContent: i.name }),
          el("span", { className: "badge " + stateClass(i.state), textContent: i.state })),
        el("div", { className: "svc", textContent: i.service + (i.upgrade ? " · update available" : "") }));
      b.setAttribute("aria-current", current === i.name ? "true" : "false");
      b.onclick = () => select(i.name);
      box.append(b);
    }
    $("#count").textContent = instances.length ? String(instances.length) : "";
    $("#empty").hidden = instances.length > 0;
    if (current) {
      const i = instances.find((x) => x.name === current);
      if (i) renderDetail(i); else { current = null; $("#detail").hidden = true; $("#welcome").hidden = false; }
    }
  }

  function renderDetail(i) {
    $("#d-name").textContent = i.name;
    $("#d-sub").textContent = i.service + " · definition " + i.definition + " · " + i.target;
    const st = $("#d-state");
    st.replaceChildren(el("span", { className: "badge " + stateClass(i.state), textContent: i.state }), " " + (i.detail || ""));
    document.querySelectorAll(".iname").forEach((n) => (n.textContent = i.name));
    const ov = $("#d-overview");
    ov.replaceChildren();
    const rows = [["Service", i.service + " (" + i.registry + "/" + i.service_id + ")"], ["Definition", i.definition + (i.upgrade ? " → " + i.upgrade + " available" : "")],
      ["Auto-update", i.auto_update], ["Installed", when(i.installed)], ["Updated", when(i.updated)], ["Console", i.console || "none"]];
    for (const [k, v] of rows) ov.append(el("tr", {}, el("td", { textContent: k }), el("td", { textContent: v })));
    const urls = (i.ports || []).filter((p) => /^https?:/.test(p.url || ""));
    if (urls.length) {
      const cell = el("td", {});
      urls.forEach((p, n) => {
        if (n) cell.append(el("br"));
        cell.append(el("a", { href: p.url, target: "_blank", rel: "noopener", textContent: p.url }), urls.length > 1 ? " (" + p.name + ")" : "");
      });
      ov.prepend(el("tr", {}, el("td", { textContent: "Open at" }), cell));
    }
    const pt = $("#d-ports");
    pt.replaceChildren();
    for (const p of i.ports || []) {
      pt.append(el("tr", {}, el("td", { textContent: p.name }), el("td", { textContent: p.port }), el("td", { textContent: p.protocol }),
        el("td", { textContent: p.public ? "public" : p.exposed ? "your network" : "host only" }), el("td", { textContent: p.description || "" })));
    }
    document.querySelector('[data-act="backup"]').hidden = !i.backups;
    $("#console-note").textContent = i.console === "stdin" ? "This server's console doesn't reply directly; what it prints in the next moment is shown." : i.console ? "" : "This server has no console.";
    $("#console-in").disabled = !i.console;
  }

  function select(name) {
    if (current !== name) { cwd = ""; $("#console-out").textContent = ""; $("#d-log").hidden = true; editing = null; $("#editor").hidden = true; }
    current = name;
    $("#welcome").hidden = true; $("#detail").hidden = false;
    document.querySelectorAll(".card").forEach((c) => c.setAttribute("aria-current", c.querySelector(".name").textContent === name ? "true" : "false"));
    const i = instances.find((x) => x.name === name);
    if (i) renderDetail(i);
    const tab = document.querySelector('.tabs [aria-selected="true"]').dataset.tab;
    showTab(tab);
  }

  function showTab(tab) {
    document.querySelectorAll(".tabs button").forEach((b) => b.setAttribute("aria-selected", b.dataset.tab === tab ? "true" : "false"));
    document.querySelectorAll(".panel").forEach((p) => (p.hidden = p.dataset.panel !== tab));
    clearInterval(logTimer); logTimer = null;
    stopStats();
    if (tab === "overview") startStats();
    if (tab === "logs") { loadLogs(); if ($("#logs-follow").checked) logTimer = setInterval(loadLogs, 3000); }
    if (tab === "files") loadFiles();
    if (tab === "backups") loadBackups();
  }
  document.querySelectorAll(".tabs button").forEach((b) => (b.onclick = () => showTab(b.dataset.tab)));

  document.querySelectorAll("[data-act]").forEach((b) => {
    b.onclick = async () => {
      const act = b.dataset.act, name = current;
      if ((act === "stop" || act === "restart" || act === "update") && !confirm(act[0].toUpperCase() + act.slice(1) + " " + name + "?")) return;
      document.querySelectorAll("[data-act]").forEach((x) => (x.disabled = true));
      toast(act + " " + name + "…");
      try {
        const r = await api("POST", "/v1/instances/" + enc(name) + "/" + act);
        toast(name + ": " + act + " done");
        showLog(r.log);
      } catch (e) { toast(name + ": " + e.message, true); showLog(e.log); }
      finally { document.querySelectorAll("[data-act]").forEach((x) => (x.disabled = false)); loadList(); }
    };
  });
  // ---- Live stats (Overview): polled every 2 s while shown, like a panel's
  // live graphs. The host reports running totals; rates come from the
  // difference between two readings. Two minutes of history, kept here only.
  const STATS_EVERY = 2000, STATS_KEEP = 60;
  let statsTimer = null, statsKey = "", statsHist = [], statsPrev = null, statsHover = null;
  function stopStats() { clearInterval(statsTimer); statsTimer = null; }
  function startStats() {
    const key = node + "/" + current;
    if (key !== statsKey) { statsKey = key; statsHist = []; statsPrev = null; $("#stats").hidden = true; }
    loadStats();
    statsTimer = setInterval(() => { if (!document.hidden) loadStats(); }, STATS_EVERY);
  }
  const rate = (n) => size(Math.max(0, n)) + "/s";
  function uptime(since) {
    let s = Math.max(0, Math.floor((Date.now() - new Date(since)) / 1000));
    const d = Math.floor(s / 86400); s %= 86400;
    const h = Math.floor(s / 3600); s %= 3600;
    const m = Math.floor(s / 60);
    return d ? d + "d " + h + "h" : h ? h + "h " + m + "m" : m + "m " + (s % 60) + "s";
  }
  async function loadStats() {
    const key = statsKey;
    let st;
    try { st = await api("GET", "/v1/instances/" + enc(current) + "/stats"); }
    catch (e) { if (key === statsKey) { $("#stats").hidden = true; if (/doesn't report/.test(e.message)) stopStats(); } return; }
    if (key !== statsKey) return;
    const at = new Date(st.at).getTime();
    let sample = { t: at, mem: st.memory, cpu: null, rx: null, tx: null };
    if (statsPrev && st.running && statsPrev.running && at > statsPrev.at && st.cpu_ns >= statsPrev.cpu) {
      const dt = (at - statsPrev.at) / 1000;
      sample.cpu = (st.cpu_ns - statsPrev.cpu) / 1e9 / dt * 100;
      if (st.net) { sample.rx = Math.max(0, (st.net_rx - statsPrev.rx) / dt); sample.tx = Math.max(0, (st.net_tx - statsPrev.tx) / dt); }
    }
    statsPrev = { at, running: st.running, cpu: st.cpu_ns, rx: st.net_rx, tx: st.net_tx };
    if (!st.running) sample = { t: at, mem: 0, cpu: 0, rx: st.net ? 0 : null, tx: st.net ? 0 : null };
    statsHist.push(sample);
    if (statsHist.length > STATS_KEEP) statsHist.shift();
    renderStats(st);
  }
  function renderStats(st) {
    const box = $("#stats"), last = statsHist[statsHist.length - 1] || {};
    const tile = (k, v, sub, chart) => el("div", { className: "tile" }, el("div", { className: "k", textContent: k }), el("div", { className: "v", textContent: v }), sub, chart || "");
    const subEl = (text) => el("div", { className: "sub", textContent: text });
    const tiles = [];
    tiles.push(tile("Uptime", st.running && st.started ? uptime(st.started) : st.running ? "Running" : "Stopped",
      subEl(st.running && st.started ? "since " + new Date(st.started).toLocaleString() : st.running ? "" : "not running")));
    const cpuSub = subEl("of " + st.cores + " cores (100% is one core)");
    tiles.push(tile("CPU", last.cpu == null ? (st.running ? "…" : "0%") : last.cpu.toFixed(1) + "%", cpuSub,
      spark([{ key: "cpu", color: "var(--s1)", name: "CPU" }], cpuSub, (v) => v.toFixed(1) + "%", 100)));
    const memSub = subEl("of " + size(st.memory_max) + (st.processes ? " · " + st.processes + " process" + (st.processes === 1 ? "" : "es") : ""));
    tiles.push(tile("Memory", size(st.memory), memSub, spark([{ key: "mem", color: "var(--s1)", name: "Memory" }], memSub, size, 64 * 1048576)));
    tiles.push(tile("Disk", size(st.disk), subEl("the server's folder")));
    if (st.net) {
      const legend = () => [el("span", { className: "sw", style: "background:var(--s1)" }), "In " + size(st.net_rx), el("span", { className: "sw", style: "background:var(--s2)" }), "Out " + size(st.net_tx)];
      const netSub = el("div", { className: "sub" }, ...legend());
      netSub.reset = () => netSub.replaceChildren(...legend());
      tiles.push(tile("Network", last.rx == null ? (st.running ? "…" : "idle") : "↓ " + rate(last.rx) + "  ↑ " + rate(last.tx), netSub,
        spark([{ key: "rx", color: "var(--s1)", name: "In" }, { key: "tx", color: "var(--s2)", name: "Out" }], netSub, rate, 1024)));
    } else {
      tiles.push(tile("Network", "Not shown", subEl("Native servers share the machine's network. Isolate this one (anyserve config " + current + " --isolate) to see its traffic.")));
    }
    box.replaceChildren(...tiles);
    box.hidden = false;
  }
  // spark draws a small line chart of the history for one or two series,
  // with a crosshair readout on hover. floor keeps an idle line low
  // instead of filling the chart.
  function spark(series, sub, fmt, floor) {
    const W = 120, H = 40, n = STATS_KEEP;
    const ns = "http://www.w3.org/2000/svg";
    const svg = document.createElementNS(ns, "svg");
    svg.setAttribute("class", "spark"); svg.setAttribute("viewBox", "0 0 " + W + " " + H); svg.setAttribute("preserveAspectRatio", "none");
    svg.setAttribute("role", "img");
    let max = floor;
    for (const p of statsHist) for (const s of series) if (p[s.key] != null) max = Math.max(max, p[s.key]);
    const x = (i) => W - (statsHist.length - 1 - i) * (W / (n - 1));
    const y = (v) => H - 1 - (v / max) * (H - 4);
    const base = document.createElementNS(ns, "line");
    base.setAttribute("class", "grid"); base.setAttribute("x1", 0); base.setAttribute("x2", W); base.setAttribute("y1", H - 0.5); base.setAttribute("y2", H - 0.5);
    base.setAttribute("vector-effect", "non-scaling-stroke");
    svg.append(base);
    for (const s of series) {
      const pts = statsHist.map((p, i) => p[s.key] == null ? null : x(i).toFixed(2) + "," + y(p[s.key]).toFixed(2)).filter(Boolean);
      if (pts.length < 2) continue;
      const pl = document.createElementNS(ns, "polyline");
      pl.setAttribute("points", pts.join(" ")); pl.setAttribute("stroke", s.color); pl.setAttribute("vector-effect", "non-scaling-stroke");
      svg.append(pl);
    }
    const label = series.map((s) => s.name).join(" and ") + " over the last two minutes";
    svg.setAttribute("aria-label", label);
    const cross = document.createElementNS(ns, "line");
    cross.setAttribute("class", "cross"); cross.setAttribute("y1", 0); cross.setAttribute("y2", H); cross.setAttribute("vector-effect", "non-scaling-stroke"); cross.style.display = "none";
    svg.append(cross);
    const original = sub.reset || ((t) => () => (sub.textContent = t))(sub.textContent);
    svg.onmousemove = (ev) => {
      if (!statsHist.length) return;
      const r = svg.getBoundingClientRect();
      const fx = (ev.clientX - r.left) / r.width * W;
      let i = Math.round(statsHist.length - 1 - (W - fx) / (W / (n - 1)));
      i = Math.max(0, Math.min(statsHist.length - 1, i));
      const p = statsHist[i];
      cross.setAttribute("x1", x(i)); cross.setAttribute("x2", x(i)); cross.style.display = "";
      const vals = series.map((s) => (series.length > 1 ? s.name + " " : "") + (p[s.key] == null ? "–" : fmt(p[s.key])));
      sub.textContent = new Date(p.t).toLocaleTimeString() + " · " + vals.join(" · ");
      statsHover = { label, clientX: ev.clientX };
    };
    svg.onmouseleave = () => { cross.style.display = "none"; original(); statsHover = null; };
    // Keep the readout where the pointer is when the chart is redrawn.
    if (statsHover && statsHover.label === label) requestAnimationFrame(() => svg.isConnected && svg.onmousemove(statsHover));
    return svg;
  }

  function showLog(text) { const d = $("#d-log"); d.textContent = text || ""; d.hidden = !text; }

  async function loadLogs() {
    if (!current) return;
    try {
      const out = $("#logs");
      const atEnd = out.scrollTop + out.clientHeight >= out.scrollHeight - 20;
      out.textContent = await api("GET", "/v1/instances/" + enc(current) + "/logs?lines=400");
      if (atEnd) out.scrollTop = out.scrollHeight;
    } catch (e) { toast(e.message, true); }
  }
  $("#logs-refresh").onclick = loadLogs;
  $("#logs-follow").onchange = () => showTab("logs");

  $("#console-form").onsubmit = async (ev) => {
    ev.preventDefault();
    const inp = $("#console-in"), cmd = inp.value.trim();
    if (!cmd) return;
    const out = $("#console-out");
    out.textContent += "> " + cmd + "\n";
    inp.value = "";
    try {
      const r = await api("POST", "/v1/instances/" + enc(current) + "/console", { command: cmd });
      out.textContent += (r.reply || "(no reply)").replace(/\s+$/, "") + "\n";
    } catch (e) { out.textContent += "! " + e.message + "\n"; }
    out.scrollTop = out.scrollHeight;
  };

  const selected = new Set();
  const isArchive = (n) => /\.(zip|tar|tar\.gz|tgz)$/i.test(n);
  const fpath = (n) => (cwd ? cwd + "/" : "") + n;
  const fapi = (rest) => "/v1/instances/" + enc(current) + "/files" + rest;
  function updateSel() {
    $("#f-selbar").hidden = selected.size === 0;
    $("#f-selcount").textContent = selected.size + " selected";
  }

  async function loadFiles() {
    if (!current) return;
    selected.clear(); updateSel(); $("#f-all").checked = false;
    const crumbs = $("#crumbs");
    crumbs.replaceChildren();
    const parts = cwd ? cwd.split("/") : [];
    const crumb = (label, path) => { const b = el("button", { className: "link", textContent: label }); b.onclick = () => { cwd = path; loadFiles(); }; return b; };
    crumbs.append(crumb(current, ""));
    parts.forEach((p, idx) => crumbs.append(" / ", crumb(p, parts.slice(0, idx + 1).join("/"))));
    const tb = $("#f-list");
    try {
      const list = await api("GET", fapi("?path=" + enc(cwd)));
      tb.replaceChildren();
      if (cwd) {
        const up = el("button", { className: "link", textContent: ".." });
        up.onclick = () => { cwd = parts.slice(0, -1).join("/"); loadFiles(); };
        tb.append(el("tr", {}, el("td"), el("td", {}, up), el("td"), el("td"), el("td")));
      }
      for (const f of list) {
        const path = fpath(f.name);
        const box = el("input", { type: "checkbox", className: "fsel" });
        box.setAttribute("aria-label", "Select " + f.name);
        box.onchange = () => { box.checked ? selected.add(f.name) : selected.delete(f.name); updateSel(); };
        const nameCell = el("td");
        if (f.dir) { const b = el("button", { className: "link", textContent: f.name + "/" }); b.onclick = () => { cwd = path; loadFiles(); }; nameCell.append(b); }
        else if (f.link) nameCell.append(el("span", { className: "muted", textContent: f.name + " (link)" }));
        else { const b = el("button", { className: "link", textContent: f.name }); b.onclick = () => (f.size <= 2 << 20 && !isArchive(f.name) ? edit(path) : download(path, f.name)); nameCell.append(b); }
        const acts = el("td");
        if (!f.dir && !f.link) {
          if (isArchive(f.name)) {
            const un = el("button", { textContent: "Unpack" });
            un.onclick = async () => {
              toast("Unpacking " + f.name + "…");
              try { const r = await api("POST", fapi("/extract"), { path }); toast("Unpacked " + r.files + " files"); loadFiles(); } catch (e) { toast(e.message, true); }
            };
            acts.append(un, " ");
          } else if (f.size <= 2 << 20) {
            const ed = el("button", { textContent: "Edit" }); ed.onclick = () => edit(path); acts.append(ed, " ");
          }
          const dl = el("button", { textContent: "Download" }); dl.onclick = () => download(path, f.name); acts.append(dl, " ");
        }
        const ren = el("button", { textContent: "Rename" });
        ren.onclick = async () => {
          const to = prompt("New name for " + f.name, f.name);
          if (!to || to === f.name || to.includes("/")) return;
          try { await api("POST", fapi("/rename?from=" + enc(path) + "&to=" + enc(fpath(to)))); loadFiles(); } catch (e) { toast(e.message, true); }
        };
        const del = el("button", { textContent: "Delete", className: "danger" });
        del.onclick = async () => {
          if (!confirm("Delete " + path + (f.dir ? " and everything in it" : "") + "? This can't be undone.")) return;
          try { await api("DELETE", fapi("?path=" + enc(path))); loadFiles(); } catch (e) { toast(e.message, true); }
        };
        acts.append(ren, " ", del);
        tb.append(el("tr", {}, el("td", {}, box), nameCell, el("td", { textContent: f.dir ? "" : size(f.size) }), el("td", { textContent: when(f.modified) }), acts));
      }
      if (!list.length) tb.append(el("tr", {}, el("td"), el("td", { className: "muted", textContent: "Empty folder", colSpan: 4 })));
    } catch (e) { tb.replaceChildren(el("tr", {}, el("td", { className: "muted", textContent: e.message, colSpan: 5 }))); }
  }
  $("#f-all").onchange = (ev) => {
    document.querySelectorAll(".fsel").forEach((b) => { if (b.checked !== ev.target.checked) { b.checked = ev.target.checked; b.onchange(); } });
  };
  $("#f-clearsel").onclick = loadFiles;
  $("#f-archive").onclick = async () => {
    const def = (selected.size === 1 ? [...selected][0] : (cwd.split("/").pop() || current)) + ".zip";
    const name = prompt("Archive name (.zip or .tar.gz)", def);
    if (!name) return;
    toast("Archiving…");
    try { await api("POST", fapi("/archive"), { dir: cwd, paths: [...selected], name }); toast("Created " + name); loadFiles(); } catch (e) { toast(e.message, true); }
  };
  $("#f-delsel").onclick = async () => {
    if (!confirm("Delete " + selected.size + " items? This can't be undone.")) return;
    for (const n of [...selected]) {
      try { await api("DELETE", fapi("?path=" + enc(fpath(n)))); } catch (e) { toast(n + ": " + e.message, true); }
    }
    loadFiles();
  };
  $("#f-newfile").onclick = async () => {
    const n = prompt("File name");
    if (!n || n.includes("/")) return;
    try { await api("PUT", fapi("?path=" + enc(fpath(n))), new Blob([""])); await loadFiles(); edit(fpath(n)); } catch (e) { toast(e.message, true); }
  };

  let editing = null;
  async function edit(path) {
    try {
      const r = await api("GET", fapi("?path=" + enc(path)), undefined, true);
      const buf = new Uint8Array(await r.arrayBuffer());
      if (buf.includes(0)) { toast("That looks like a binary file; download it instead.", true); return; }
      editing = path;
      $("#ed-path").textContent = path;
      $("#ed-text").value = new TextDecoder().decode(buf);
      $("#ed-state").textContent = "";
      $("#editor").hidden = false;
      $("#ed-text").focus();
    } catch (e) { toast(e.message, true); }
  }
  $("#ed-text").oninput = () => ($("#ed-state").textContent = "Unsaved changes");
  $("#ed-save").onclick = async () => {
    if (!editing) return;
    try {
      await api("PUT", fapi("?path=" + enc(editing)), new Blob([$("#ed-text").value]));
      $("#ed-state").textContent = "Saved " + new Date().toLocaleTimeString();
      loadFiles();
    } catch (e) { toast(e.message, true); }
  };
  $("#ed-text").onkeydown = (ev) => { if ((ev.ctrlKey || ev.metaKey) && ev.key === "s") { ev.preventDefault(); $("#ed-save").onclick(); } };
  $("#ed-close").onclick = () => {
    if ($("#ed-state").textContent === "Unsaved changes" && !confirm("Close without saving?")) return;
    editing = null; $("#editor").hidden = true;
  };

  async function download(path, name) {
    try {
      const r = await api("GET", "/v1/instances/" + enc(current) + "/files?path=" + enc(path), undefined, true);
      const url = URL.createObjectURL(await r.blob());
      const a = el("a", { href: url, download: name });
      document.body.append(a); a.click(); a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 10000);
    } catch (e) { toast(e.message, true); }
  }

  $("#f-mkdir").onclick = async () => {
    const n = prompt("Folder name");
    if (!n || n.includes("/")) return;
    try { await api("POST", "/v1/instances/" + enc(current) + "/files/mkdir?path=" + enc((cwd ? cwd + "/" : "") + n)); loadFiles(); } catch (e) { toast(e.message, true); }
  };
  $("#f-upload").onchange = async (ev) => {
    for (const f of ev.target.files) {
      toast("Uploading " + f.name + "…");
      try { await api("PUT", "/v1/instances/" + enc(current) + "/files?path=" + enc((cwd ? cwd + "/" : "") + f.name), f); }
      catch (e) { toast(f.name + ": " + e.message, true); }
    }
    ev.target.value = "";
    toast("Upload finished");
    loadFiles();
  };

  async function loadBackups() {
    const tb = $("#b-list");
    try {
      const list = await api("GET", "/v1/instances/" + enc(current) + "/backups");
      tb.replaceChildren();
      for (const b of list) tb.append(el("tr", {}, el("td", { textContent: b.file }), el("td", { textContent: when(b.created) }), el("td", { textContent: size(b.size) })));
      if (!list.length) tb.append(el("tr", {}, el("td", { className: "muted", textContent: "No backups yet", colSpan: 3 })));
    } catch (e) { tb.replaceChildren(el("tr", {}, el("td", { className: "muted", textContent: e.message, colSpan: 3 }))); }
  }

  // ---- Admin: views, users, new servers, removal ----
  let isAdmin = false, catalog = null, def = null, jobTimer = null;
  function showView(v) {
    for (const n of ["servers", "users", "machines", "ports", "new"]) $("#view-" + n).hidden = n !== v;
    document.querySelectorAll("#adminnav button").forEach((b) => b.setAttribute("aria-current", b.dataset.view === v ? "true" : "false"));
    if (v === "users") loadUsers();
    if (v === "machines") loadMachines();
    if (v === "ports") loadReserved();
    if (v === "new") openNew();
  }
  document.querySelectorAll("#adminnav button").forEach((b) => (b.onclick = () => showView(b.dataset.view)));

  $("#d-remove").onclick = async () => {
    const name = current;
    const typed = prompt("This deletes " + name + " and its files (backups are kept). Type " + name + " to confirm.");
    if (typed === null) return;
    if (typed !== name) { toast("The name didn't match; nothing was removed.", true); return; }
    try { await api("DELETE", "/v1/instances/" + enc(name), { confirm: typed }); toast("Removed " + name); current = null; $("#detail").hidden = true; $("#welcome").hidden = false; loadList(); }
    catch (e) { toast(e.message, true); showLog(e.log); }
  };

  // ---- Machines (the fleet) ----
  async function loadNodes() {
    let nodes = [];
    try { nodes = await api("GET", "/v1/nodes"); } catch (e) { return []; }
    const sel = $("#node");
    const want = node;
    sel.replaceChildren(el("option", { value: "", textContent: "This machine" }),
      ...nodes.map((n) => el("option", { value: n.name, textContent: n.name + (n.connected ? "" : " (offline)") })));
    sel.value = nodes.some((n) => n.name === want) ? want : "";
    sel.hidden = nodes.length === 0;
    if (sel.value !== node) chooseNode(sel.value);
    return nodes;
  }
  function chooseNode(n) {
    node = n; current = null; catalog = null; instances = [];
    clearInterval(logTimer); stopStats();
    $("#detail").hidden = true; $("#welcome").hidden = false;
    $("#instances").replaceChildren();
    if (!$("#view-new").hidden) openNew();
    if (!$("#view-ports").hidden) loadReserved();
    loadList();
  }
  $("#node").onchange = (ev) => chooseNode(ev.target.value);

  async function loadMachines() {
    const tb = $("#m-list");
    const nodes = await loadNodes();
    if (!nodes.length) { tb.replaceChildren(el("tr", {}, el("td", { colSpan: 5, className: "muted", textContent: "No other machines yet." }))); return; }
    tb.replaceChildren(...nodes.map((n) => {
      const rm = el("button", { className: "danger", textContent: "Remove" });
      rm.onclick = async () => {
        if (!confirm("Remove " + n.name + " from this fleet? Its servers keep running there, but you can't manage them from here until it joins again.")) return;
        try { await api("DELETE", "/v1/nodes/" + enc(n.name)); toast("Removed " + n.name); loadMachines(); } catch (e) { toast(e.message, true); }
      };
      return el("tr", {},
        el("td", {}, el("strong", { textContent: n.name }), n.addr ? el("div", { className: "muted small", textContent: n.addr }) : ""),
        el("td", {}, el("span", { className: "badge " + (n.connected ? "s-running" : "s-stopped"), textContent: n.connected ? "connected" : "offline" })),
        el("td", { textContent: n.version || "" }),
        el("td", { className: "nowrap", textContent: when(n.joined) }),
        el("td", {}, rm));
    }));
  }
  $("#m-add").onclick = async () => {
    const box = $("#m-join");
    try {
      const j = await api("POST", "/v1/fleet/token");
      const code = el("code", { textContent: j.command });
      const copy = el("button", { textContent: "Copy" });
      copy.onclick = () => navigator.clipboard.writeText(j.command).then(() => toast("Copied"), () => toast("Select the command and copy it", true));
      box.className = "notice";
      box.replaceChildren("Run this on the new machine. It works once, until " + new Date(j.expires).toLocaleTimeString() + ":\n", code, "\n", copy);
    } catch (e) { box.className = "notice bad"; box.textContent = e.message; }
    box.hidden = false;
  };

  // ---- Reserved ports (per machine) ----
  async function loadReserved() {
    $("#p-where").textContent = node ? "on " + node : "on this machine";
    const tb = $("#p-list");
    let rs;
    try { rs = await api("GET", "/v1/reservations"); } catch (e) { tb.replaceChildren(el("tr", {}, el("td", { colSpan: 4, className: "muted", textContent: e.message }))); return; }
    if (!rs.length) { tb.replaceChildren(el("tr", {}, el("td", { colSpan: 4, className: "muted", textContent: "No ports are reserved." }))); return; }
    tb.replaceChildren(...rs.map((r) => {
      const range = r.from === r.to ? String(r.from) : r.from + "-" + r.to;
      const rm = el("button", { className: "danger", textContent: "Remove" });
      rm.onclick = async () => {
        try { await api("DELETE", "/v1/reservations", { from: r.from, to: r.to }); toast("Removed " + range); loadReserved(); } catch (e) { toast(e.message, true); }
      };
      return el("tr", {}, el("td", { textContent: range }), el("td", { textContent: r.protocol }), el("td", { textContent: r.note || "" }), el("td", {}, rm));
    }));
  }
  $("#p-form").onsubmit = async (ev) => {
    ev.preventDefault();
    const m = $("#p-range").value.trim().match(/^(\d+)(?:\s*-\s*(\d+))?$/);
    if (!m) { toast("Give a port like 25565 or a range like 27015-27030", true); return; }
    const body = { from: Number(m[1]), to: Number(m[2] || m[1]), protocol: $("#p-proto").value, note: $("#p-note").value.trim() };
    try {
      const r = await api("POST", "/v1/reservations", body);
      toast("Reserved " + $("#p-range").value.trim() + (r.in_use && r.in_use.length ? "\nAlready used by your servers, which keep them: " + r.in_use.join(", ") : ""));
      ev.target.reset();
      loadReserved();
    } catch (e) { toast(e.message, true); }
  };

  async function loadUsers() {
    const tb = $("#u-list");
    let users;
    try { users = await api("GET", "/v1/users"); } catch (e) { tb.replaceChildren(el("tr", {}, el("td", { colSpan: 5, className: "muted", textContent: e.message }))); return; }
    const names = instances.map((i) => i.name);
    tb.replaceChildren();
    for (const u of users) {
      const patch = async (body, msg) => { try { await api("PATCH", "/v1/users/" + enc(u.name), body); if (msg) toast(msg); loadUsers(); } catch (e) { toast(e.message, true); } };
      const role = el("select", {}, el("option", { value: "user", textContent: "User" }), el("option", { value: "admin", textContent: "Admin" }));
      role.value = u.admin ? "admin" : "user";
      role.setAttribute("aria-label", "Role of " + u.name);
      role.onchange = () => patch({ admin: role.value === "admin" }, u.name + " is now " + (role.value === "admin" ? "an admin" : "a user"));
      const chips = el("div", { className: "chips" });
      for (const n of u.instances) {
        const x = el("button", { textContent: "×" }); x.setAttribute("aria-label", "Take " + n + " from " + u.name);
        x.onclick = () => patch({ revoke: [n] });
        chips.append(el("span", { className: "chip" }, n, x));
      }
      const avail = names.filter((n) => !u.instances.includes(n));
      if (!u.admin && avail.length) {
        const add = el("select", {}, el("option", { value: "", textContent: "+ give a server" }), ...avail.map((n) => el("option", { value: n, textContent: n })));
        add.setAttribute("aria-label", "Give a server to " + u.name);
        add.onchange = () => add.value && patch({ grant: [add.value] });
        chips.append(add);
      }
      if (u.admin) chips.append(el("span", { className: "muted small", textContent: "all servers" }));
      const keys = el("div", { className: "small" });
      u.keys.forEach((k, idx) => {
        const x = el("button", { textContent: "×", className: "link" }); x.setAttribute("aria-label", "Remove key " + k);
        x.onclick = () => confirm("Remove the key " + k + " from " + u.name + "?") && patch({ drop_key: idx });
        keys.append(el("div", {}, el("code", { textContent: k }), " ", u.keys.length > 1 ? x : ""));
      });
      const addKey = el("button", { textContent: "Add key" });
      addKey.onclick = () => { const k = prompt("SSH public key for " + u.name); if (k) patch({ add_key: k }, "Key added"); };
      const acts = el("td", { className: "nowrap" });
      if (u.admin) {
        const cert = el("button", { textContent: "Issue certificate" });
        cert.onclick = () => issueCert(u.name);
        acts.append(cert, " ");
      }
      const rm = el("button", { textContent: "Remove", className: "danger" });
      rm.onclick = async () => {
        if (!confirm("Remove " + u.name + "? They lose access at once.")) return;
        try { await api("DELETE", "/v1/users/" + enc(u.name)); toast("Removed " + u.name); loadUsers(); } catch (e) { toast(e.message, true); }
      };
      acts.append(addKey, " ", rm);
      tb.append(el("tr", {}, el("td", { textContent: u.name }), el("td", {}, role, u.admin ? el("div", { className: "muted small", textContent: u.certs + " certificate" + (u.certs === 1 ? "" : "s") }) : ""), el("td", {}, chips), el("td", {}, keys), acts));
    }
    if (!users.length) tb.append(el("tr", {}, el("td", { colSpan: 5, className: "muted", textContent: "No users yet. Only this host's owner can sign in." })));
  }

  async function issueCert(name) {
    if (!confirm("Issue a new browser certificate for " + name + "? Their old ones keep working until revoked on the host.")) return;
    try {
      const c = await api("POST", "/v1/users/" + enc(name) + "/cert");
      const bytes = Uint8Array.from(atob(c.p12), (ch) => ch.charCodeAt(0));
      const url = URL.createObjectURL(new Blob([bytes], { type: "application/x-pkcs12" }));
      const a = el("a", { href: url, download: name + ".p12" });
      document.body.append(a); a.click(); a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 10000);
      const box = $("#u-cert");
      box.replaceChildren("Downloaded " + name + ".p12 (valid until " + c.not_after + "). Import password, shown only now: ", el("code", { textContent: c.password }),
        "\nSend the file and the password to " + name + " by different routes, then delete your copy.");
      box.hidden = false;
      loadUsers();
    } catch (e) { toast(e.message, true); }
  }

  $("#u-form").onsubmit = async (ev) => {
    ev.preventDefault();
    const body = { name: $("#u-name").value.trim(), key: $("#u-key").value.trim(), admin: $("#u-admin").checked };
    try {
      await api("POST", "/v1/users", body);
      toast("Added " + body.name);
      ev.target.reset();
      const box = $("#u-cert");
      box.replaceChildren(body.name + " can now connect from their computer:\n", el("code", { textContent: "anyserve remote add " + location.hostname.replace(/^127\.0\.0\.1$|^localhost$/, "<this host>") + " " + body.name + "@<host address>:7022" }),
        body.admin ? "\nAs an admin, they also need a browser certificate: use Issue certificate." : "\nGive them servers in the table above.");
      box.hidden = false;
      loadUsers();
    } catch (e) { toast(e.message, true); }
  };

  async function openNew() {
    $("#n-form").hidden = true; $("#n-pick").hidden = false;
    if (!catalog) {
      try { catalog = await api("GET", "/v1/catalog"); } catch (e) { $("#n-catalog").replaceChildren(el("p", { className: "muted", textContent: e.message })); return; }
    }
    renderCatalog();
    $("#n-search").focus();
  }
  function renderCatalog() {
    const q = $("#n-search").value.trim().toLowerCase();
    const box = $("#n-catalog");
    box.replaceChildren();
    for (const c of catalog.filter((c) => !q || (c.name + " " + c.id + " " + c.description).toLowerCase().includes(q))) {
      const b = el("button", { className: "card" }, el("div", { className: "name", textContent: c.name }),
        el("div", { className: "kind", textContent: (c.kind === "stack" ? "App" : "Game server") + (c.registry !== "main" ? " · " + c.registry : "") }),
        el("div", { className: "svc", textContent: c.description }));
      b.onclick = () => chooseService(c);
      box.append(b);
    }
  }
  $("#n-search").oninput = renderCatalog;
  $("#n-back").onclick = openNew;

  async function chooseService(c) {
    try { def = await api("GET", "/v1/catalog/" + enc(c.registry) + "/" + enc(c.id)); } catch (e) { toast(e.message, true); return; }
    def.ref = c.ref;
    $("#n-pick").hidden = true; $("#n-form").hidden = false;
    $("#n-title").textContent = def.name; $("#n-desc").textContent = def.description;
    let name = def.id, k = 2;
    while (instances.some((i) => i.name === name)) name = def.id + "-" + k++;
    $("#n-name").value = name;
    $("#n-log").hidden = true; $("#n-notes").hidden = true; $("#n-install").disabled = false;
    const sb = $("#n-settings");
    sb.replaceChildren();
    for (const st of def.settings || []) {
      let input;
      // An address is filled in from the machine's address and port.
      const dflt = st.address || st.default === undefined || st.default === null ? "" : String(st.default);
      if (st.type === "bool") {
        input = el("input", { type: "checkbox", checked: dflt === "true" && !st.must_accept });
        const lab = el("label", { className: "check" }, input, " " + st.prompt);
        input.dataset.key = st.key; input.dataset.type = "bool";
        if (st.must_accept) input.required = true;
        sb.append(lab);
        continue;
      }
      if (st.type === "choice") {
        input = el("select", {}, ...(st.choices || []).map((ch) => el("option", { value: ch.value, textContent: ch.label || ch.value })));
        input.value = dflt;
      } else {
        input = el("input", { type: st.type === "int" ? "number" : st.secret ? "password" : "text", value: dflt, placeholder: st.generate ? "leave empty to generate one" : st.address ? "leave empty to use this machine's address" : st.optional ? "optional" : "" });
        if (st.type === "int") { if (st.min != null) input.min = st.min; if (st.max != null) input.max = st.max; }
        if (!st.optional && !st.generate && !st.address && st.type !== "folder") input.required = dflt === "";
        if (st.secret) input.autocomplete = "new-password";
      }
      input.dataset.key = st.key;
      sb.append(el("label", {}, st.prompt + (st.description ? " — " + st.description : ""), input));
    }
    const pb = $("#n-ports");
    pb.replaceChildren();
    for (const p of def.ports || []) {
      const input = el("input", { type: "number", min: 1, max: 65535, value: p.port });
      input.dataset.port = p.name;
      const cell = el("div", { className: "port" }, el("label", {}, p.name + " (" + p.protocol + (p.public ? ", public" : "") + ")", input));
      // A stack's private ports listen on this machine only unless opened.
      if (def.kind === "stack" && !p.public) {
        const open = el("input", { type: "checkbox", checked: true });
        open.dataset.expose = p.name;
        cell.append(el("label", { className: "check" }, open, "Open to your network (otherwise only this machine can reach it)"));
      }
      pb.append(cell);
    }
    plan(true);
  }

  function formBody(dry) {
    const settings = {}, ports = {};
    $("#n-settings").querySelectorAll("[data-key]").forEach((i) => {
      const v = i.dataset.type === "bool" ? String(i.checked) : i.value;
      const st = (def.settings || []).find((s) => s.key === i.dataset.key);
      const dflt = st && st.default != null && !st.address ? String(st.default) : "";
      if (v !== dflt || i.dataset.type === "bool") settings[i.dataset.key] = v;
    });
    $("#n-ports").querySelectorAll("[data-port]").forEach((i) => {
      if (i.dataset.touched) ports[i.dataset.port] = Number(i.value);
    });
    const expose = [...$("#n-ports").querySelectorAll("[data-expose]:checked")].map((i) => i.dataset.expose);
    return { service: def.ref, name: $("#n-name").value.trim(), settings, ports, expose, domain: $("#n-domain").value.trim(), start: $("#n-start").checked, dry_run: dry };
  }
  $("#n-ports").oninput = (ev) => { if (ev.target.dataset.port) ev.target.dataset.touched = "1"; };
  // Re-check a port as soon as it's changed, so warnings (in use, reserved)
  // show before Install.
  $("#n-ports").onchange = () => plan(true);

  // plan asks the host for the ports it would use: taken defaults are
  // moved to free ones before the user sees them.
  async function plan(quiet) {
    const notes = $("#n-notes");
    try {
      const body = formBody(true);
      if (quiet) body.settings = {};
      const p = await api("POST", "/v1/instances", body);
      $("#n-ports").querySelectorAll("[data-port]").forEach((i) => { if (!i.dataset.touched && p.ports[i.dataset.port]) i.value = p.ports[i.dataset.port]; });
      const lines = [...(p.moved || []), ...(p.warnings || []).map((w) => "Warning: " + w)];
      notes.textContent = lines.join("\n"); notes.className = "notice"; notes.hidden = !lines.length;
      return true;
    } catch (e) {
      if (quiet) { notes.hidden = true; return false; }
      notes.textContent = e.message; notes.className = "notice bad"; notes.hidden = false;
      return false;
    }
  }

  $("#n-form").onsubmit = async (ev) => {
    ev.preventDefault();
    if (!(await plan(false))) return;
    $("#n-install").disabled = true;
    const log = $("#n-log");
    log.hidden = false; log.textContent = "Starting…\n";
    try {
      const job = await api("POST", "/v1/instances", formBody(false));
      clearInterval(jobTimer);
      jobTimer = setInterval(async () => {
        try {
          const j = await api("GET", "/v1/jobs/" + enc(job.id));
          log.textContent = j.log || "Working…";
          log.scrollTop = log.scrollHeight;
          if (j.done) {
            clearInterval(jobTimer);
            await loadList();
            if (j.error) { log.textContent += "\nFailed: " + j.error; $("#n-install").disabled = false; toast("Install failed", true); }
            else { toast("Installed " + job.instance); showView("servers"); select(job.instance); }
          }
        } catch (e) { clearInterval(jobTimer); toast(e.message, true); $("#n-install").disabled = false; }
      }, 1500);
    } catch (e) { log.textContent = e.message + (e.log ? "\n" + e.log : ""); $("#n-install").disabled = false; }
  };

  async function start() {
    if (!token) { $("#who").textContent = "Open the address `anyserve proxy` printed (it includes a token)."; return; }
    try {
      const me = await api("GET", "/v1/whoami");
      $("#host").textContent = "· " + me.host;
      $("#who").textContent = me.name + (me.local ? " (this host's owner)" : me.admin ? " (admin)" : "");
      isAdmin = me.admin;
      $("#adminnav").hidden = !isAdmin;
      if (isAdmin) { loadNodes(); setInterval(loadNodes, 15000); }
      $("#d-remove").hidden = !isAdmin;
      document.title = "Anyserve · " + me.host;
    } catch (e) { $("#who").textContent = e.message; return; }
    await loadList();
    setInterval(loadList, 5000);
  }
  start();
})();
