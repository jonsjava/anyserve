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
  let current = null, cwd = "", instances = [], logTimer = null;

  async function api(method, path, body, raw) {
    const opts = { method, headers: { "X-Anyserve-Token": token || "" } };
    if (body !== undefined) {
      if (body instanceof Blob || body instanceof File) opts.body = body;
      else { opts.body = JSON.stringify(body); opts.headers["Content-Type"] = "application/json"; }
    }
    const r = await fetch("/api" + path, opts);
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
    if (current !== name) { cwd = ""; $("#console-out").textContent = ""; $("#d-log").hidden = true; }
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

  async function loadFiles() {
    if (!current) return;
    const crumbs = $("#crumbs");
    crumbs.replaceChildren();
    const parts = cwd ? cwd.split("/") : [];
    const crumb = (label, path) => { const b = el("button", { className: "link", textContent: label }); b.onclick = () => { cwd = path; loadFiles(); }; return b; };
    crumbs.append(crumb(current, ""));
    parts.forEach((p, idx) => crumbs.append(" / ", crumb(p, parts.slice(0, idx + 1).join("/"))));
    const tb = $("#f-list");
    try {
      const list = await api("GET", "/v1/instances/" + enc(current) + "/files?path=" + enc(cwd));
      tb.replaceChildren();
      if (cwd) {
        const up = el("button", { className: "link", textContent: ".." });
        up.onclick = () => { cwd = parts.slice(0, -1).join("/"); loadFiles(); };
        tb.append(el("tr", {}, el("td", {}, up), el("td"), el("td"), el("td")));
      }
      for (const f of list) {
        const path = (cwd ? cwd + "/" : "") + f.name;
        const nameCell = el("td");
        if (f.dir) { const b = el("button", { className: "link", textContent: f.name + "/" }); b.onclick = () => { cwd = path; loadFiles(); }; nameCell.append(b); }
        else if (f.link) nameCell.append(el("span", { className: "muted", textContent: f.name + " (link)" }));
        else { const b = el("button", { className: "link", textContent: f.name }); b.onclick = () => download(path, f.name); nameCell.append(b); }
        const ren = el("button", { textContent: "Rename" });
        ren.onclick = async () => {
          const to = prompt("New name for " + f.name, f.name);
          if (!to || to === f.name || to.includes("/")) return;
          try { await api("POST", "/v1/instances/" + enc(current) + "/files/rename?from=" + enc(path) + "&to=" + enc((cwd ? cwd + "/" : "") + to)); loadFiles(); } catch (e) { toast(e.message, true); }
        };
        const del = el("button", { textContent: "Delete" });
        del.onclick = async () => {
          if (!confirm("Delete " + path + (f.dir ? " and everything in it" : "") + "? This can't be undone.")) return;
          try { await api("DELETE", "/v1/instances/" + enc(current) + "/files?path=" + enc(path)); loadFiles(); } catch (e) { toast(e.message, true); }
        };
        tb.append(el("tr", {}, nameCell, el("td", { textContent: f.dir ? "" : size(f.size) }), el("td", { textContent: when(f.modified) }), el("td", {}, ren, " ", del)));
      }
      if (!list.length) tb.append(el("tr", {}, el("td", { className: "muted", textContent: "Empty folder", colSpan: 4 })));
    } catch (e) { tb.replaceChildren(el("tr", {}, el("td", { className: "muted", textContent: e.message, colSpan: 4 }))); }
  }

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

  async function start() {
    if (!token) { $("#who").textContent = "Open the address `anyserve proxy` printed (it includes a token)."; return; }
    try {
      const me = await api("GET", "/v1/whoami");
      $("#host").textContent = "· " + me.host;
      $("#who").textContent = me.name + (me.local ? " (this host's owner)" : me.admin ? " (admin)" : "");
      document.title = "Anyserve · " + me.host;
    } catch (e) { $("#who").textContent = e.message; return; }
    await loadList();
    setInterval(loadList, 5000);
  }
  start();
})();
