// storage.bd backup client UI. Vanilla JS, no build step, embedded in
// the storagebd binary. All dynamic text goes through textContent (via
// h()): file names, error messages and labels are never parsed as HTML.
"use strict";

// ---------- tiny DOM helpers ----------
function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "html") throw new Error("no raw HTML");
    else if (v === true) el.setAttribute(k, "");
    else el.setAttribute(k, v);
  }
  append(el, children);
  return el;
}
function append(el, children) {
  for (const c of children.flat(Infinity)) {
    if (c == null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
}
function clear(el) { while (el.firstChild) el.firstChild.remove(); return el; }

// Icons: static path data only (never user input).
const ICONS = {
  home: "M3 10.5 12 3l9 7.5V20a1 1 0 0 1-1 1h-5v-6H9v6H4a1 1 0 0 1-1-1z",
  folder: "M3 6.5A1.5 1.5 0 0 1 4.5 5h4.6l2 2.2h8.4A1.5 1.5 0 0 1 21 8.7v9.8a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 18.5z",
  db: "M12 3c4.4 0 8 1.3 8 3s-3.6 3-8 3-8-1.3-8-3 3.6-3 8-3zM4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3",
  file: "M6 3h8l4 4v14H6zM14 3v4h4",
  clock: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 7v5l3 2",
  history: "M3 12a9 9 0 1 0 3-6.7M3 4v5h5M12 8v4l3 2",
  restore: "M4 12a8 8 0 1 0 2.3-5.7M4 4v4h4M12 9v6M9 12l3 3 3-3",
  gear: "M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z",
  play: "M7 5v14l11-7z",
  stop: "M7 7h10v10H7z",
  plus: "M12 5v14M5 12h14",
  trash: "M4 7h16M9 7V4h6v3M6 7l1 13h10l1-13",
  edit: "M4 20h4L19 9l-4-4L4 16zM14 6l4 4",
  alert: "M12 3 2 20h20zM12 9v5M12 17.5v.5",
  info: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 11v6M12 7.5v.5",
  user: "M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM4 21a8 8 0 0 1 16 0",
  server: "M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01",
  check: "M5 12l5 5 9-10",
  chevron: "M9 6l6 6-6 6",
  shield: "M12 3l8 3v6c0 4.5-3.4 8.3-8 9-4.6-.7-8-4.5-8-9V6z",
};
function icon(name) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("width", "16");
  svg.setAttribute("height", "16");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "1.8");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  svg.setAttribute("aria-hidden", "true");
  const p = document.createElementNS("http://www.w3.org/2000/svg", "path");
  p.setAttribute("d", ICONS[name] || ICONS.info);
  svg.append(p);
  return svg;
}

// ---------- formatting ----------
function bytes(n) {
  if (n == null) return "–";
  const u = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i === 0 ? n : n.toFixed(n < 10 ? 1 : 0)) + " " + u[i];
}
function ago(ts) {
  if (!ts) return "never";
  const s = (Date.now() - new Date(ts).getTime()) / 1000;
  if (s < 60) return "just now";
  if (s < 3600) return Math.round(s / 60) + " min ago";
  if (s < 86400) return Math.round(s / 3600) + " h ago";
  return Math.round(s / 86400) + " days ago";
}
function when(ts) {
  if (!ts) return "–";
  return new Date(ts).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}
function until(ts) {
  if (!ts) return "–";
  const s = (new Date(ts).getTime() - Date.now()) / 1000;
  if (s <= 60) return "any moment";
  if (s < 3600) return "in " + Math.round(s / 60) + " min";
  if (s < 86400) return "in " + Math.round(s / 3600) + " h";
  return when(ts);
}
function duration(sec) {
  sec = Math.round(sec || 0);
  if (sec < 60) return sec + "s";
  if (sec < 3600) return Math.floor(sec / 60) + "m " + (sec % 60) + "s";
  return Math.floor(sec / 3600) + "h " + Math.floor((sec % 3600) / 60) + "m";
}
const STATUS = {
  succeeded: ["ok", "Succeeded"], partial: ["warn", "Some files skipped"], failed: ["bad", "Failed"],
  interrupted: ["warn", "Interrupted"], running: ["run", "Running"],
};
function statusBadge(status) {
  const [cls, label] = STATUS[status] || ["", status];
  return h("span", { class: "badge " + cls }, label);
}
const KIND_LABEL = { backup: "Backup", restore: "File restore", "restore-db": "Database restore", cleanup: "Cleanup" };

// ---------- API ----------
class ApiError extends Error {
  constructor(status, body) { super(body.error || "Request failed (" + status + ")"); this.status = status; this.body = body; }
}
async function api(method, path, body) {
  const opts = { method, headers: { "X-Storagebd": "1" }, credentials: "same-origin" };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  let data = null;
  const text = await res.text();
  if (text) { try { data = JSON.parse(text); } catch { data = { error: text }; } }
  if (res.status === 401 && data && /ui-url/.test(data.error || "")) { location.reload(); }
  if (!res.ok) throw new ApiError(res.status, data || {});
  return data;
}
const enc = encodeURIComponent;
const credPath = (name) => "/api/credentials/" + enc(name);

// ---------- toasts & dialogs ----------
function toast(msg, kind) {
  const t = h("div", { class: "toast " + (kind || ""), role: kind === "bad" ? "alert" : "status" }, msg);
  document.getElementById("toasts").append(t);
  setTimeout(() => t.remove(), kind === "bad" ? 9000 : 4500);
}
function fail(err) { toast(err.message || String(err), "bad"); }

// dialog({title, body: Node, actions: [{label, kind, onClick: async () => boolean|void}]})
function dialog({ title, intro, body, actions, wide, onDismiss }) {
  const dlg = h("dialog", {});
  if (wide) dlg.style.width = "min(720px, calc(100vw - 32px))";
  const foot = h("div", { class: "dlg-foot" });
  let acted = false;
  const close = () => {
    if (!dlg.isConnected) return;
    dlg.close(); dlg.remove();
    if (!acted && onDismiss) onDismiss();
  };
  foot.append(h("button", { class: "btn ghost", type: "button", onclick: close }, "Cancel"));
  for (const a of actions || []) {
    const b = h("button", { class: "btn " + (a.kind || "primary"), type: "button" }, a.label);
    b.addEventListener("click", async () => {
      b.disabled = true;
      try { if ((await a.onClick()) !== false) { acted = true; close(); } }
      catch (e) { fail(e); }
      finally { b.disabled = false; }
    });
    foot.append(b);
  }
  dlg.append(
    h("div", { class: "dlg-head" }, h("h2", {}, title), intro ? h("p", { class: "soft mt-4" }, intro) : null),
    h("div", { class: "dlg-body" }, body || ""),
    foot,
  );
  dlg.addEventListener("cancel", (e) => { e.preventDefault(); close(); });
  document.body.append(dlg);
  dlg.showModal();
  const first = dlg.querySelector("input, select, textarea");
  if (first) first.focus();
  return { close };
}
function confirmDialog(title, text, label, danger) {
  return new Promise((resolve) => {
    dialog({
      title, body: h("p", {}, text), onDismiss: () => resolve(false),
      actions: [{ label, kind: danger ? "danger solid" : "primary", onClick: () => resolve(true) }],
    });
  });
}
function closeDialogs() { document.querySelectorAll("dialog").forEach((d) => { d.close(); d.remove(); }); }

function field(label, input, hint) {
  const id = "f" + Math.random().toString(36).slice(2);
  input.id = id;
  return h("div", { class: "field" }, h("label", { for: id }, label), input, hint ? h("span", { class: "hint" }, hint) : null);
}
function input(attrs) { return h("input", Object.assign({ type: "text", autocomplete: "off", spellcheck: "false" }, attrs)); }
function sw(checked, onchange, label) {
  const i = h("input", { type: "checkbox", role: "switch", "aria-label": label });
  i.checked = !!checked;
  i.addEventListener("change", () => onchange(i.checked, i));
  return h("label", { class: "switch" }, i, h("span", {}));
}
function emptyState(iconName, title, text, action) {
  return h("div", { class: "empty" }, icon(iconName), h("h3", {}, title), h("p", {}, text), action ? h("div", { class: "mt-14" }, action) : null);
}
function loading(text) { return h("div", { class: "loading" }, h("span", { class: "spinner" }), text || "Loading…"); }

// ---------- app state ----------
const app = {
  state: null,      // /api/state
  cred: null,       // selected credential name
  view: "overview",
  live: {},         // credential -> latest running info from events
};
const root = () => document.getElementById("app");

function currentCred() {
  return app.state && app.state.credentials.find((c) => c.name === app.cred);
}
function sessionFor(apiURL) {
  return app.state && app.state.sessions.find((s) => s.api_url === apiURL);
}

async function refresh() {
  app.state = await api("GET", "/api/state");
  const names = app.state.credentials.map((c) => c.name);
  if (!names.includes(app.cred)) {
    const def = app.state.credentials.find((c) => c.default);
    app.cred = def ? def.name : names[0] || null;
  }
  for (const c of app.state.credentials) {
    if (c.running && !app.live[c.name]) app.live[c.name] = Object.assign({}, c.running);
    if (!c.running) delete app.live[c.name];
  }
  renderChrome();
}

function renderChrome() {
  const host = document.getElementById("host-chip");
  host.hidden = false;
  clear(host).append(icon("server"), app.state.hostname || "this machine");
  const acct = document.getElementById("account-chip");
  const sess = app.state.sessions[0];
  acct.hidden = false;
  clear(acct).append(icon("user"), sess ? sess.email : "Not signed in");
}

// ---------- routing ----------
const VIEWS = [
  ["overview", "home", "Overview"],
  ["sources", "folder", "What to back up"],
  ["restore", "restore", "Snapshots & restore"],
  ["schedule", "clock", "Schedule & cleanup"],
  ["activity", "history", "Activity"],
  ["settings", "gear", "Settings"],
];

function go(view) {
  app.view = view;
  try { history.replaceState(null, "", "#" + view); } catch {}
  render();
}

function render() {
  const el = clear(root());
  if (!app.state.credentials.length) {
    el.append(onboarding());
    return;
  }
  const nav = h("nav", { class: "nav", "aria-label": "Sections" },
    VIEWS.map(([id, ic, label]) => h("button", { type: "button", "aria-current": app.view === id ? "page" : null, onclick: () => go(id) },
      icon(ic), h("span", {}, label))));
  const side = h("aside", { class: "sidebar" });
  if (app.state.credentials.length > 1) {
    const sel = h("select", { "aria-label": "Backup repo" },
      app.state.credentials.map((c) => { const o = h("option", { value: c.name }, c.name + (c.default ? " (default)" : "")); o.selected = c.name === app.cred; return o; }));
    sel.addEventListener("change", () => { app.cred = sel.value; render(); });
    side.append(h("div", { class: "repo-switch" }, h("label", {}, "Backup space"), sel));
  }
  side.append(nav);
  const main = h("main", { class: "main", id: "main" });
  el.append(h("div", { class: "shell" }, side, main));
  const views = { overview, sources, restore: restoreView, schedule, activity, settings };
  (views[app.view] || overview)(main);
}

function pageHead(title, sub, actions) {
  return h("div", { class: "page-head" },
    h("div", {}, h("h1", {}, title), sub ? h("p", { class: "soft" }, sub) : null),
    actions ? h("div", { class: "btn-row" }, actions) : null);
}

// ---------- onboarding: sign in, then create this machine's repo ----------
function onboarding() {
  const wrap = h("div", { class: "onboard" });
  const sess = app.state.sessions.find((s) => s.api_url === app.state.default_api_url) || app.state.sessions[0];
  if (app.onboardExisting) {
    wrap.append(addExistingCard(sess),
      h("p", { class: "small soft mt-14" }, h("button", { class: "linkish", type: "button", onclick: () => { app.onboardExisting = false; render(); } }, "Back")));
    return wrap;
  }
  if (!sess) wrap.append(loginCard(1));
  else wrap.append(connectCard(sess, 2));
  wrap.append(h("p", { class: "small soft mt-14" }, "Already added storage in your storage.bd dashboard? ",
    h("button", { class: "linkish", type: "button", onclick: () => { app.onboardExisting = true; render(); } }, "Use it on this machine")));
  return wrap;
}

function stepBar(n) {
  return h("div", { class: "steps", "aria-label": "Step " + n + " of 2" }, h("span", { class: n >= 1 ? "on" : "" }), h("span", { class: n >= 2 ? "on" : "" }));
}

function loginCard(step) {
  const email = input({ type: "email", autocomplete: "username", required: true });
  const pass = input({ type: "password", autocomplete: "current-password", required: true });
  const remember = h("input", { type: "checkbox" }); remember.checked = true;
  const apiUrl = input({ placeholder: app.state.default_api_url });
  const btn = h("button", { class: "btn primary lg", type: "submit" }, "Sign in");
  const form = h("form", {},
    field("Email", email),
    field("Password", pass),
    h("label", { class: "check mt-14" }, remember, "Keep me signed in for 30 days"),
    h("details", { class: "mt-14" }, h("summary", { class: "soft small" }, "Advanced"),
      h("div", { class: "mt-8" }, field("storage.bd API", apiUrl, "Leave empty for " + app.state.default_api_url))),
    h("div", { class: "btn-row mt-20" }, btn,
      h("a", { href: "https://storage.bd", target: "_blank", rel: "noopener noreferrer", class: "small" }, "No account? Sign up at storage.bd")));
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    btn.disabled = true;
    try {
      await api("POST", "/api/account/login", { email: email.value, password: pass.value, remember: remember.checked, api_url: apiUrl.value.trim() });
      pass.value = "";
      await refresh();
      render();
    } catch (err) { fail(err); } finally { btn.disabled = false; }
  });
  return h("div", { class: "card" }, step ? stepBar(step) : null,
    h("h1", {}, "Connect to storage.bd"),
    h("p", { class: "soft mt-4" }, "Sign in to your storage.bd account to set up backups for this machine."),
    h("div", { class: "mt-20" }, form));
}

function connectCard(sess, step) {
  const name = input({ value: app.state.default_name, required: true, pattern: "[a-z0-9][a-z0-9._\\-]{0,62}" });
  const label = input({ value: app.state.hostname || "", required: true });
  const gb = input({ type: "number", min: "1", step: "1", value: "50", required: true });
  const activate = h("input", { type: "checkbox" }); activate.checked = true;
  const price = h("div", { class: "price" }, h("span", { class: "soft" }, "Monthly cost"), h("strong", {}, "…"));
  let timer;
  const quote = () => {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      const n = parseInt(gb.value, 10);
      if (!(n >= 1)) return;
      try {
        const q = await api("GET", "/api/quote?gb=" + n + "&api_url=" + enc(sess.api_url));
        clear(price).append(h("span", { class: "soft" }, n + " GB × " + q.PricePerGB + " " + q.Currency + "/GB"), h("strong", {}, q.Monthly + " " + q.Currency + " / month"));
      } catch (err) { clear(price).append(h("span", { class: "soft" }, err.message)); }
    }, 250);
  };
  gb.addEventListener("input", quote);
  quote();

  const btn = h("button", { class: "btn primary lg", type: "submit" }, "Create backup space");
  const form = h("form", {},
    field("Name on this machine", name, "Used in commands, for example: storagebd backup " + app.state.default_name),
    field("Label in your storage.bd dashboard", label),
    field("Storage size (GB)", gb, "You can change this later in your storage.bd dashboard."),
    price,
    h("label", { class: "check mt-14" }, activate, "Activate now (charges the first month from your balance)"),
    h("div", { class: "btn-row mt-20" }, btn));
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    btn.disabled = true;
    try {
      const res = await api("POST", "/api/connect", { api_url: sess.api_url, name: name.value.trim(), label: label.value.trim(), storage_gb: parseInt(gb.value, 10), activate: activate.checked });
      if (res.activation_error) toast("Created, but not activated yet: " + res.activation_error, "bad");
      else toast(res.activated ? "Backup space created and activated." : "Backup space created.", "ok");
      closeDialogs();
      app.cred = res.name;
      app.view = "sources";
      await refresh();
      render();
    } catch (err) {
      if (err.body && err.body.install_secret) {
        dialog({ title: "Save this access key now", intro: "storage.bd created the backup space, but this machine couldn't save it. The secret key below is shown only once.",
          body: h("pre", { class: "muted-box mono break" }, "Space ID:       " + err.body.tenant_id + "\nAccess key ID:  " + err.body.install_id + "\nSecret key:     " + err.body.install_secret) });
      } else fail(err);
    } finally { btn.disabled = false; }
  });
  return h("div", { class: "card" }, step ? stepBar(step) : null,
    h("h1", {}, "Create a backup space for this machine"),
    h("p", { class: "soft mt-4" }, "Signed in as " + sess.email + ". This machine gets its own encrypted backup space on storage.bd."),
    h("div", { class: "mt-20" }, form));
}

// A space created with "New backup space" in the storage.bd dashboard,
// which shows its Space ID, Access key ID and Secret key once. Same as
// `storagebd credential add`; no sign-in needed.
function addExistingCard(sess) {
  const taken = app.state.credentials.some((c) => c.name === app.state.default_name);
  const name = input({ value: taken ? "" : app.state.default_name, required: true, pattern: "[a-z0-9][a-z0-9._\\-]{0,62}" });
  const tenant = input({ required: true, placeholder: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx", class: "mono" });
  const install = input({ required: true, placeholder: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx", class: "mono" });
  const secret = input({ type: "password", autocomplete: "off", required: true, class: "mono" });
  const used = h("input", { type: "checkbox" });
  const pass = input({ type: "password", autocomplete: "off", class: "mono" });
  const passField = field("Its encryption password", pass, "On the machine that used it: storagebd credential show NAME --reveal (the encryption password).");
  passField.hidden = true;
  used.addEventListener("change", () => { passField.hidden = !used.checked; pass.required = used.checked; });
  const apiUrl = input({ placeholder: sess ? sess.api_url : app.state.default_api_url });

  const btn = h("button", { class: "btn primary lg", type: "submit" }, "Add backup space");
  const form = h("form", {},
    field("Name on this machine", name, "Used in commands, for example: storagebd backup " + (name.value || "NAME")),
    field("Space ID", tenant),
    field("Access key ID", install),
    field("Secret key", secret, "Shown once in the dashboard. If you've lost it, issue a new one there."),
    h("label", { class: "check mt-14" }, used, "This space already has backups from another machine"),
    passField,
    h("details", { class: "mt-14" }, h("summary", { class: "soft small" }, "Advanced"),
      h("div", { class: "mt-8" }, field("storage.bd API", apiUrl, "Leave empty for " + (sess ? sess.api_url : app.state.default_api_url)))),
    h("div", { class: "btn-row mt-20" }, btn));
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    btn.disabled = true;
    try {
      const res = await api("POST", "/api/credentials", {
        api_url: apiUrl.value.trim() || (sess ? sess.api_url : ""), name: name.value.trim(),
        tenant_id: tenant.value.trim(), install_id: install.value.trim(), install_secret: secret.value.trim(),
        restic_password: used.checked ? pass.value : "",
      });
      secret.value = ""; pass.value = "";
      toast("Backup space added.", "ok");
      closeDialogs();
      app.onboardExisting = false;
      app.cred = res.name;
      app.view = "sources";
      await refresh();
      render();
    } catch (err) { fail(err); } finally { btn.disabled = false; }
  });
  return h("div", { class: "card" },
    h("h1", {}, "Use a backup space from your dashboard"),
    h("p", { class: "soft mt-4" }, "Paste the details shown after \u201cNew backup space\u201d in your storage.bd dashboard. They're checked with storage.bd before anything is saved."),
    h("div", { class: "mt-20" }, form));
}

// ---------- overview ----------
function overview(main) {
  const c = currentCred();
  const live = app.live[c.name];
  main.append(pageHead("Overview", c.name + " · " + c.tenant_id.slice(0, 8)));

  if (!c.password_escrowed_at && c.repo_initialized_at) {
    main.append(h("div", { class: "banner warn" }, icon("alert"), h("div", { class: "grow" },
      h("strong", {}, "Recovery copy pending. "),
      "This space's encryption password hasn't been saved with storage.bd yet. Until it is, this machine holds the only copy. It will be retried on the next backup.")));
  }
  if (!c.retention && c.last_success) {
    main.append(h("div", { class: "banner info" }, icon("info"), h("div", { class: "grow" },
      h("strong", {}, "Old backups are kept forever. "), "They keep counting against your storage. Set how many to keep."),
      h("button", { class: "btn sm", type: "button", onclick: () => go("schedule") }, "Set up cleanup")));
  }
  if (c.last_job && c.last_job.status === "failed" && !live) {
    main.append(h("div", { class: "banner bad" }, icon("alert"), h("div", { class: "grow" },
      h("strong", {}, "Last " + (KIND_LABEL[c.last_job.kind] || c.last_job.kind).toLowerCase() + " failed: "), c.last_job.error || "see Activity for details")));
  }

  // status hero
  let cls = "status-idle", headline = "No backups yet", sub = "Add something to back up, then run your first backup.";
  if (live) {
    cls = "status-run"; headline = (KIND_LABEL[live.kind] || "Job") + " in progress"; sub = "Started " + ago(live.started_at);
  } else if (c.last_success) {
    const hrs = (Date.now() - new Date(c.last_success)) / 3.6e6;
    cls = hrs < 48 ? "status-ok" : "status-warn";
    headline = "Last backup " + ago(c.last_success);
    sub = when(c.last_success);
  }
  const action = live
    ? h("button", { class: "btn danger", type: "button", onclick: () => cancelJob(c) }, icon("stop"), "Cancel")
    : h("button", { class: "btn primary lg", type: "button", disabled: c.source_count === 0, onclick: () => startBackup(c) }, icon("play"), "Back up now");
  const hero = h("div", { class: "card hero " + cls },
    h("div", {}, h("div", { class: "status-line" }, h("span", { class: "dot" }), h("span", { class: "big" }, headline)), h("p", { class: "soft mt-4" }, sub)),
    action);
  main.append(hero);

  if (live) main.append(progressCard(live));

  const sched = c.schedule && c.schedule.enabled
    ? (c.schedule.mode === "daily" ? "Daily at " + c.schedule.daily_time : "Every " + c.schedule.interval_hours + " h")
    : "Off";
  main.append(h("div", { class: "stats mt-14" },
    statCard("Sources", c.source_count ? String(c.source_count) : "None yet", () => go("sources")),
    statCard("Schedule", sched, () => go("schedule"), c.schedule && c.schedule.enabled ? "Next " + until(c.schedule.next_run_at) : "Runs only while storagebd serve is running"),
    statCard("Last job", c.last_job ? (KIND_LABEL[c.last_job.kind] || c.last_job.kind) : "–", () => go("activity"), c.last_job ? h("span", {}, statusBadge(c.last_job.status), " ", ago(c.last_job.finished_at || c.last_job.started_at)) : null)));

  if (c.source_count === 0) {
    main.append(h("div", { class: "card mt-14" }, emptyState("folder", "Nothing to back up yet", "Choose folders and databases on this machine to protect.",
      h("button", { class: "btn primary", type: "button", onclick: () => go("sources") }, icon("plus"), "Add a source"))));
  }
  main.append(accountCard(c));
}

function statCard(label, value, onClick, extra) {
  return h("button", { class: "card stat", type: "button", onclick: onClick },
    h("div", { class: "label" }, label), h("div", { class: "value" }, value), extra ? h("div", { class: "small soft mt-4" }, extra) : null);
}

function progressCard(live) {
  const p = live.progress;
  const pct = p ? Math.min(100, p.percent_done * 100) : 0;
  const bar = h("div", { class: "progress" + (p ? "" : " indeterminate") }, h("span", {}));
  bar.firstChild.style.width = pct.toFixed(1) + "%";
  const meta = h("div", { class: "progress-meta" },
    h("span", {}, live.source ? (live.progress ? "Source: " + live.source : live.source) : "Preparing…"),
    p ? h("span", {}, pct.toFixed(1) + "% · " + bytes(p.bytes_done) + " of " + bytes(p.total_bytes) + " · " + p.files_done + "/" + p.total_files + " files" + (p.seconds_remaining ? " · " + duration(p.seconds_remaining) + " left" : "")) : null);
  const cur = p && p.current_files && p.current_files.length ? h("p", { class: "small soft mono mt-8 break" }, p.current_files[0]) : null;
  return h("div", { class: "card", id: "progress-card" }, bar, meta, cur);
}

async function accountCardBody(c, body) {
  const sess = sessionFor(c.api_url);
  if (!sess) {
    clear(body).append(h("p", { class: "soft" }, "Not signed in. Backups keep working without it; sign in to see your balance and plan."),
      h("div", { class: "mt-14" }, h("button", { class: "btn", type: "button", onclick: () => go("settings") }, "Sign in")));
    return;
  }
  try {
    const acct = await api("GET", "/api/account?api_url=" + enc(c.api_url));
    const mine = acct.tenants.find((t) => t.tenant_id === c.tenant_id);
    clear(body).append(h("div", { class: "grid-2" },
      h("div", {}, h("div", { class: "soft small" }, "Balance"), h("div", { class: "big" }, acct.balance + " " + acct.currency)),
      mine ? h("div", {}, h("div", { class: "soft small" }, "This backup space"),
        h("div", { class: "big" }, mine.storage_gb + " GB "), h("div", { class: "mt-4" }, planBadge(mine), mine.state === "active" ? h("span", { class: "small soft" }, " renews " + new Date(mine.next_billing_at).toLocaleDateString()) : null)) : null));
    if (mine && mine.state === "suspended" && !mine.suspend_reason) {
      body.append(h("div", { class: "banner warn mt-14" }, icon("alert"), h("div", { class: "grow" }, "This backup space isn't active yet, so backups will be refused."),
        h("button", { class: "btn sm", type: "button", onclick: async () => {
          if (!(await confirmDialog("Activate " + c.name + "?", "This charges the first month from your storage.bd balance.", "Activate"))) return;
          try { await api("POST", credPath(c.name) + "/activate"); toast("Activated.", "ok"); accountCardBody(c, body); } catch (e) { fail(e); }
        } }, "Activate")));
    }
  } catch (err) {
    clear(body).append(h("p", { class: "soft" }, err.message));
  }
}
// Same state names as the storage.bd dashboard.
const STATE_LABEL = { active: "Active", grace: "Over limit", suspended: "Suspended", cancelled: "Cancelled", purged: "Deleted" };
function planBadge(t) {
  const map = { active: "ok", grace: "warn", suspended: "bad", cancelled: "bad" };
  const reason = !t.suspend_reason ? "" : t.suspend_reason === "balance_exhausted" ? " (balance ran out)" : " (" + t.suspend_reason + ")";
  return h("span", { class: "badge " + (map[t.state] || "") }, (STATE_LABEL[t.state] || t.state) + reason);
}
function accountCard(c) {
  const body = h("div", {}, loading("Checking your storage.bd account…"));
  accountCardBody(c, body);
  return h("div", { class: "card mt-14" }, h("div", { class: "card-head" }, h("h2", {}, "storage.bd account")), body);
}

async function startBackup(c, sources) {
  try {
    await api("POST", credPath(c.name) + "/backup", { sources: sources || [] });
    app.live[c.name] = { kind: "backup", started_at: new Date().toISOString() };
    toast("Backup started.");
    render();
  } catch (e) { fail(e); }
}
async function cancelJob(c) {
  if (!(await confirmDialog("Cancel the running job?", "restic stops cleanly and releases its lock. Sources already finished keep their snapshots.", "Cancel job", true))) return;
  try { await api("POST", credPath(c.name) + "/cancel"); toast("Cancelling…"); } catch (e) { fail(e); }
}

// ---------- sources ----------
async function sources(main) {
  const c = currentCred();
  main.append(pageHead("What to back up", "Each source gets its own snapshot on every backup run.", [
    h("button", { class: "btn", type: "button", onclick: () => sourceDialog(c, "db") }, icon("db"), "Add database"),
    h("button", { class: "btn primary", type: "button", onclick: () => sourceDialog(c, "dir") }, icon("plus"), "Add folder"),
  ]));
  const box = h("div", {}, loading());
  main.append(box);
  try {
    const list = await api("GET", credPath(c.name) + "/sources");
    clear(box);
    if (!list.length) {
      box.append(h("div", { class: "card" }, emptyState("folder", "No sources yet", "Add a folder (for example /var/www or /home) or a MySQL, MariaDB or Postgres database.")));
      return;
    }
    box.append(h("div", { class: "list" }, list.map((s) => sourceRow(c, s))));
    box.append(h("div", { class: "btn-row mt-14" },
      h("button", { class: "btn", type: "button", disabled: !!app.live[c.name], onclick: () => startBackup(c) }, icon("play"), "Back up all enabled now")));
  } catch (e) { clear(box).append(h("div", { class: "banner bad" }, icon("alert"), e.message)); }
}

function describe(s) {
  if (s.kind === "dir") return s.path + (s.excludes && s.excludes.length ? "  ·  excludes " + s.excludes.join(", ") : "");
  const where = s.db_host ? s.db_host + (s.db_port ? ":" + s.db_port : "") : (s.db_socket || "local socket");
  return (s.kind === "mysql" ? "MySQL/MariaDB" : "PostgreSQL") + " · " + s.db_name + " as " + (s.db_user || "OS user") + " @ " + where;
}

function sourceRow(c, s) {
  const isDB = s.kind !== "dir";
  return h("div", { class: "row" },
    h("div", { class: "icon-tile" + (isDB ? " db" : "") }, icon(isDB ? "db" : "folder")),
    h("div", { class: "grow" }, h("div", { class: "title" }, s.name), h("div", { class: "sub mono" }, describe(s))),
    sw(s.enabled, async (on, el) => {
      try { await api("PUT", credPath(c.name) + "/sources/" + enc(s.name), Object.assign(sourceBodyFrom(s), { enabled: on })); toast(on ? "Enabled." : "Disabled: skipped by scheduled and 'all' backups."); }
      catch (e) { el.checked = !on; fail(e); }
    }, "Include " + s.name + " in backups"),
    h("button", { class: "btn ghost sm", type: "button", title: "Back up only this source", "aria-label": "Back up " + s.name, disabled: !!app.live[c.name], onclick: () => startBackup(c, [s.name]) }, icon("play")),
    h("button", { class: "btn ghost sm", type: "button", "aria-label": "Edit " + s.name, onclick: () => sourceDialog(c, s.kind === "dir" ? "dir" : "db", s) }, icon("edit")),
    h("button", { class: "btn ghost sm danger", type: "button", "aria-label": "Remove " + s.name, onclick: async () => {
      if (!(await confirmDialog("Remove " + s.name + "?", "It won't be backed up any more. Existing snapshots stay in the backup space and can still be restored."
, "Remove", true))) return;
      try { await api("DELETE", credPath(c.name) + "/sources/" + enc(s.name)); toast("Removed."); go("sources"); await refresh(); } catch (e) { fail(e); }
    } }, icon("trash")));
}

function sourceBodyFrom(s) {
  return { name: s.name, kind: s.kind, enabled: s.enabled, path: s.path || "", excludes: s.excludes || [], db_name: s.db_name || "",
    db_host: s.db_host || "", db_port: s.db_port || 0, db_socket: s.db_socket || "", db_user: s.db_user || "" };
}

function sourceDialog(c, type, existing) {
  const editing = !!existing;
  const name = input({ value: existing ? existing.name : "", placeholder: type === "dir" ? "www" : "shop-db", pattern: "[a-z0-9][a-z0-9._\\-]{0,62}" });
  let body, collect;
  if (type === "dir") {
    const path = input({ value: existing ? existing.path : "", placeholder: "/var/www" });
    const excl = h("textarea", { placeholder: "*.log\n/var/www/cache" });
    excl.value = existing && existing.excludes ? existing.excludes.join("\n") : "";
    body = h("div", {}, field("Name", name, "Lowercase letters, digits, '.', '_' or '-'."), field("Folder", path, "Absolute path on this machine."), field("Exclude patterns (one per line, optional)", excl, "restic patterns, for example *.log or /var/www/cache"));
    collect = () => ({ name: name.value.trim(), kind: "dir", path: path.value.trim(), excludes: excl.value.split("\n").map((x) => x.trim()).filter(Boolean) });
  } else {
    const engine = h("select", {}, h("option", { value: "mysql" }, "MySQL / MariaDB"), h("option", { value: "postgres" }, "PostgreSQL"));
    if (existing) { engine.value = existing.kind; engine.disabled = true; }
    const db = input({ value: existing ? existing.db_name : "" });
    const host = input({ value: existing ? existing.db_host : "", placeholder: "local socket" });
    const port = input({ type: "number", min: "0", max: "65535", value: existing && existing.db_port ? existing.db_port : "" , placeholder: "default" });
    const socket = input({ value: existing ? existing.db_socket : "", placeholder: "optional" });
    const user = input({ value: existing ? existing.db_user : "" });
    const pass = input({ type: "password", autocomplete: "new-password", placeholder: existing && existing.has_password ? "unchanged" : "" });
    body = h("div", {},
      field("Name", name), field("Engine", engine), field("Database", db),
      h("div", { class: "field-row mt-14" }, field("Host", host), field("Port", port)),
      field("Socket path", socket, "Only if the server listens on a non-default socket."),
      h("div", { class: "field-row mt-14" }, field("User", user), field("Password", pass)),
      h("p", { class: "small soft mt-14" }, "The password is stored only on this machine and passed to the dump tool through a private file, never on the command line. A read-only backup user is enough for backups."));
    collect = () => {
      const out = { name: name.value.trim(), kind: engine.value, db_name: db.value.trim(), db_host: host.value.trim(), db_port: parseInt(port.value, 10) || 0,
        db_socket: socket.value.trim(), db_user: user.value.trim() };
      if (pass.value || !editing) out.db_password = pass.value;
      return out;
    };
  }
  dialog({
    title: (editing ? "Edit " : "Add ") + (type === "dir" ? "folder" : "database"), body,
    actions: [{ label: editing ? "Save" : "Add", onClick: async () => {
      const b = collect();
      if (editing) { b.enabled = existing.enabled; await api("PUT", credPath(c.name) + "/sources/" + enc(existing.name), b); }
      else await api("POST", credPath(c.name) + "/sources", b);
      toast(editing ? "Saved." : "Added.", "ok");
      await refresh();
      go("sources");
    } }],
  });
}

// ---------- snapshots & restore ----------
async function restoreView(main) {
  const c = currentCred();
  main.append(pageHead("Snapshots & restore", "Browse what's been backed up and bring it back."));
  const filter = h("select", { "aria-label": "Filter by source" }, h("option", { value: "" }, "All sources"));
  const listBox = h("div", {}, loading("Reading snapshots from storage.bd…"));
  const detail = h("div", {});
  main.append(h("div", { class: "card" }, h("div", { class: "card-head" }, h("h2", {}, "Snapshots"), filter), listBox), detail);

  let snaps = [], srcMap = {};
  try {
    const [list, srcs] = await Promise.all([api("GET", credPath(c.name) + "/snapshots"), api("GET", credPath(c.name) + "/sources")]);
    snaps = list;
    for (const s of srcs) srcMap[s.name] = s;
    const names = [...new Set(snaps.map((s) => s.source).filter(Boolean))].sort();
    for (const n of names) filter.append(h("option", { value: n }, n));
  } catch (e) { clear(listBox).append(h("div", { class: "banner bad" }, icon("alert"), e.message)); return; }

  const draw = () => {
    clear(listBox);
    const shown = snaps.filter((s) => !filter.value || s.source === filter.value);
    if (!shown.length) { listBox.append(emptyState("history", "No snapshots yet", "Run a backup and its snapshots will appear here.")); return; }
    const list = h("div", { class: "list" });
    for (const s of shown.slice(0, 200)) {
      const src = srcMap[s.source];
      const isDB = src ? src.kind !== "dir" : (s.paths || []).some((p) => /\.sql$/.test(p));
      const row = h("div", { class: "row clickable", tabindex: "0", role: "button" },
        h("div", { class: "icon-tile" + (isDB ? " db" : "") }, icon(isDB ? "db" : "folder")),
        h("div", { class: "grow" }, h("div", { class: "title" }, (s.source || "untagged") + " · " + when(s.time)), h("div", { class: "sub mono" }, s.short_id + "  ·  " + (s.paths || []).join(" "))),
        icon("chevron"));
      const open = () => { list.querySelectorAll(".selected").forEach((x) => x.classList.remove("selected")); row.classList.add("selected"); showSnapshot(c, s, src, isDB, detail); };
      row.addEventListener("click", open);
      row.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); } });
      list.append(row);
    }
    listBox.append(list);
    if (shown.length > 200) listBox.append(h("p", { class: "small soft mt-8" }, "Showing the newest 200 of " + shown.length + "."));
  };
  filter.addEventListener("change", draw);
  draw();
}

function showSnapshot(c, snap, src, isDB, box) {
  clear(box);
  if (!snap.source) {
    box.append(h("div", { class: "card mt-14" }, h("p", { class: "soft" }, "This snapshot isn't tagged with a source, so it can't be restored from here. Use restic directly.")));
    return;
  }
  if (isDB) {
    const into = input({ value: src ? src.db_name : "", placeholder: "database name" });
    box.append(h("div", { class: "card mt-14" },
      h("div", { class: "card-head" }, h("h2", {}, "Restore database"), h("span", { class: "badge" }, snap.short_id)),
      h("p", { class: "soft" }, "Loads this dump into the database server configured for source " + snap.source + ". Tables in the dump replace existing tables of the same name."),
      src && src.kind === "mysql" ? h("div", { class: "banner warn mt-14" }, icon("alert"), "MySQL/MariaDB can't undo a restore that fails part-way. To be safe, restore into a new, empty database first and check it.") : null,
      src && src.kind === "postgres" ? h("div", { class: "banner info mt-14" }, icon("shield"), "PostgreSQL restores run in one transaction: if anything fails, nothing is changed.") : null,
      h("div", { class: "mt-14" }, field("Restore into database", into, "Must already exist. Default: the source's own database.")),
      h("div", { class: "btn-row mt-14" }, h("button", { class: "btn primary", type: "button", disabled: !src, onclick: async () => {
        if (!(await confirmDialog("Restore into " + into.value + "?", "Tables in the dump from " + when(snap.time) + " will replace existing tables in " + into.value + ".", "Restore", true))) return;
        try { await api("POST", credPath(c.name) + "/restore-db", { source: snap.source, snapshot: snap.id, into: into.value.trim() }); app.live[c.name] = { kind: "restore-db", started_at: new Date().toISOString() }; toast("Database restore started. Progress is shown under Activity."); } catch (e) { fail(e); }
      } }, icon("restore"), "Restore database")),
      !src ? h("p", { class: "small soft mt-8" }, "The source " + snap.source + " was removed, so its connection settings are gone. Re-add it to restore.") : null));
    return;
  }

  // file browser
  const selected = new Set();
  const crumbs = h("div", { class: "crumbs" });
  const listing = h("div", { class: "list browser" });
  const selInfo = h("span", { class: "small soft" }, "Nothing selected: everything in the snapshot will be restored.");
  const target = input({ value: "/tmp/storagebd-restore", placeholder: "/tmp/restore" });
  const inPlace = h("input", { type: "checkbox" });
  inPlace.addEventListener("change", () => { target.disabled = inPlace.checked; });

  const updateSel = () => {
    clear(selInfo).append(selected.size ? selected.size + " item(s) selected: " + [...selected].slice(0, 3).join(", ") + (selected.size > 3 ? "…" : "") : "Nothing selected: everything in the snapshot will be restored.");
  };
  const startPath = (snap.paths && snap.paths.length === 1) ? snap.paths[0] : "/";

  async function open(path) {
    clear(crumbs);
    const parts = path.split("/").filter(Boolean);
    const rootBtn = h("button", { type: "button", onclick: () => open("/") }, "/");
    crumbs.append(rootBtn);
    parts.forEach((p, i) => {
      const sub = "/" + parts.slice(0, i + 1).join("/");
      crumbs.append(h("span", { class: "sep" }, "›"), h("button", { type: "button", onclick: () => open(sub) }, p));
    });
    clear(listing).append(loading());
    try {
      const res = await api("GET", credPath(c.name) + "/ls?snapshot=" + enc(snap.id) + "&path=" + enc(path));
      clear(listing);
      if (!res.nodes.length) { listing.append(h("div", { class: "empty" }, "Empty folder")); return; }
      for (const n of res.nodes) {
        const cb = h("input", { type: "checkbox", "aria-label": "Select " + n.name });
        cb.checked = selected.has(n.path);
        cb.addEventListener("click", (e) => e.stopPropagation());
        cb.addEventListener("change", () => { cb.checked ? selected.add(n.path) : selected.delete(n.path); updateSel(); });
        const isDir = n.type === "dir";
        const row = h("div", { class: "row" + (isDir ? " dir clickable" : "") }, cb, icon(isDir ? "folder" : "file"),
          h("span", { class: "grow mono break" }, n.name), isDir ? null : h("span", { class: "size" }, bytes(n.size)));
        if (isDir) row.addEventListener("click", () => open(n.path));
        listing.append(row);
      }
      if (res.truncated) listing.append(h("div", { class: "row soft small" }, "Showing the first 5000 entries."));
    } catch (e) { clear(listing).append(h("div", { class: "banner bad" }, icon("alert"), e.message)); }
  }

  const doRestore = async (confirmOverwrite) => {
    const tgt = inPlace.checked ? "/" : target.value.trim();
    const body = { source: snap.source, snapshot: snap.id, target: tgt, includes: [...selected], confirm_overwrite: confirmOverwrite };
    try {
      await api("POST", credPath(c.name) + "/restore", body);
      app.live[c.name] = { kind: "restore", started_at: new Date().toISOString() };
      toast("Restore started. Progress is shown under Activity.");
    } catch (e) {
      if (e.status === 409 && e.body.needs_confirm) {
        if (await confirmDialog("Overwrite files?", e.message + ".", "Restore and overwrite", true)) doRestore(true);
      } else fail(e);
    }
  };

  box.append(h("div", { class: "card mt-14" },
    h("div", { class: "card-head" }, h("h2", {}, "Browse " + snap.source), h("span", { class: "badge" }, snap.short_id + " · " + when(snap.time))),
    crumbs, listing,
    h("div", { class: "mt-14" }, selInfo)),
  h("div", { class: "card mt-14" },
    h("h2", {}, "Restore"),
    h("div", { class: "mt-14" }, field("Restore into folder", target, "Files keep their full original path inside this folder, for example " + startPath + "/… → " + "/tmp/storagebd-restore" + startPath + "/…")),
    h("label", { class: "check mt-14" }, inPlace, "Restore to the original location (overwrites current files)"),
    h("div", { class: "btn-row mt-14" }, h("button", { class: "btn primary", type: "button", onclick: async () => {
      if (inPlace.checked && !(await confirmDialog("Restore in place?", "Files from " + when(snap.time) + " will overwrite the current files at their original paths.", "Restore in place", true))) return;
      doRestore(inPlace.checked);
    } }, icon("restore"), "Restore"))));
  open(startPath);
}

// ---------- schedule ----------
async function schedule(main) {
  const c = currentCred();
  main.append(pageHead("Schedule & cleanup", "When to back up, and how many old backups to keep."));
  const sc = c.schedule || { enabled: false, mode: "daily", daily_time: "02:00", interval_hours: 24 };
  let mode = sc.mode;
  const enabled = h("input", { type: "checkbox" }); enabled.checked = sc.enabled;
  const time = input({ type: "time", value: sc.daily_time });
  const hours = input({ type: "number", min: "1", max: "168", value: String(sc.interval_hours || 24) });
  const dailyBox = field("Time of day", time, "Local time on this machine.");
  const intBox = field("Every (hours)", hours);
  const seg = h("div", { class: "segmented", role: "group", "aria-label": "Schedule type" });
  const setMode = (m) => {
    mode = m;
    for (const b of seg.children) b.setAttribute("aria-pressed", String(b.dataset.m === m));
    dailyBox.classList.toggle("hidden", m !== "daily");
    intBox.classList.toggle("hidden", m !== "interval");
  };
  for (const [m, label] of [["daily", "Once a day"], ["interval", "Every few hours"]]) {
    const b = h("button", { type: "button" }, label); b.dataset.m = m; b.addEventListener("click", () => setMode(m)); seg.append(b);
  }
  setMode(mode);
  const save = h("button", { class: "btn primary", type: "button" }, "Save schedule");
  save.addEventListener("click", async () => {
    save.disabled = true;
    try {
      await api("PUT", credPath(c.name) + "/schedule", { enabled: enabled.checked, mode, daily_time: time.value || "02:00", interval_hours: parseInt(hours.value, 10) || 24 });
      toast("Schedule saved.", "ok");
      await refresh();
      go("schedule");
    } catch (e) { fail(e); } finally { save.disabled = false; }
  });
  main.append(h("div", { class: "card" },
    h("label", { class: "check" }, enabled, h("strong", {}, "Back up automatically")),
    h("div", { class: "mt-14" }, seg),
    h("div", { class: "mt-14" }, dailyBox, intBox),
    sc.next_run_at && sc.enabled ? h("p", { class: "soft mt-14" }, "Next run: " + when(sc.next_run_at) + (sc.last_run_at ? " · last run " + when(sc.last_run_at) : "")) : null,
    h("div", { class: "btn-row mt-20" }, save)),
  h("div", { class: "banner info mt-14" }, icon("info"), h("div", { class: "grow" },
    "Schedules run while ", h("code", {}, "storagebd serve"), " is running. Install it as a system service so backups continue after a reboot. A run missed while the machine was off happens once at the next start.")));
  main.append(retentionCard(c));
}

// ---------- retention (old-snapshot cleanup) ----------
function describeKeep(p) {
  const parts = [];
  if (p.keep_last) parts.push(p.keep_last + " most recent");
  if (p.keep_daily) parts.push(p.keep_daily + " daily");
  if (p.keep_weekly) parts.push(p.keep_weekly + " weekly");
  if (p.keep_monthly) parts.push(p.keep_monthly + " monthly");
  if (p.keep_yearly) parts.push(p.keep_yearly + " yearly");
  return parts.join(", ");
}

function retentionCard(c) {
  const card = h("div", { class: "card mt-14" }, h("div", { class: "card-head" }, h("h2", {}, "Keep old backups")), loading());
  (async () => {
    let data;
    try { data = await api("GET", credPath(c.name) + "/retention"); }
    catch (e) { clear(card).append(h("div", { class: "banner bad" }, icon("alert"), e.message)); return; }
    const pol = data.policy, start = pol || data.suggested;
    const num = (v, label, hint) => {
      const i = input({ type: "number", min: "0", max: "10000", value: String(v || 0) });
      return { i, el: field(label, i, hint) };
    };
    const f = {
      last: num(start.keep_last, "Most recent"), daily: num(start.keep_daily, "Daily"), weekly: num(start.keep_weekly, "Weekly"),
      monthly: num(start.keep_monthly, "Monthly"), yearly: num(start.keep_yearly, "Yearly"),
      prune: num(start.prune_interval_days, "Free space every (days)", "Freeing space may read some old data back, so it runs at most this often."),
    };
    const body = () => ({ keep_last: +f.last.i.value || 0, keep_daily: +f.daily.i.value || 0, keep_weekly: +f.weekly.i.value || 0,
      keep_monthly: +f.monthly.i.value || 0, keep_yearly: +f.yearly.i.value || 0, prune_interval_days: +f.prune.i.value || 7 });
    const previewBox = h("div", {});

    const status = pol
      ? h("div", { class: "banner info" }, icon("check"), h("div", { class: "grow" },
          h("strong", {}, "On: "), "keeping " + describeKeep(pol) + " of each source. ",
          h("span", { class: "soft" }, "Last cleanup " + (pol.last_forget_at ? ago(pol.last_forget_at) : "not yet") + ", space last freed " + (pol.last_prune_at ? ago(pol.last_prune_at) : "not yet") + ".")))
      : h("div", { class: "banner warn" }, icon("alert"), h("div", { class: "grow" },
          h("strong", {}, "Off: "), "every backup is kept forever and counts against your storage. The suggested settings below keep a week of daily, a month of weekly and a year of monthly backups."));

    const previewBtn = h("button", { class: "btn", type: "button" }, "Preview");
    previewBtn.addEventListener("click", async () => {
      previewBtn.disabled = true;
      clear(previewBox).append(loading("Checking which backups the rules would remove…"));
      try { clear(previewBox).append(previewView(await api("POST", credPath(c.name) + "/retention/preview", body()))); }
      catch (e) { clear(previewBox).append(h("div", { class: "banner bad mt-14" }, icon("alert"), e.message)); }
      finally { previewBtn.disabled = false; }
    });
    const saveBtn = h("button", { class: "btn primary", type: "button" }, pol ? "Save" : "Turn on cleanup");
    saveBtn.addEventListener("click", async () => {
      saveBtn.disabled = true;
      try {
        await api("PUT", credPath(c.name) + "/retention", body());
        toast("Cleanup policy saved. It applies after each successful backup.", "ok");
        await refresh(); go("schedule");
      } catch (e) { fail(e); } finally { saveBtn.disabled = false; }
    });
    const actions = [previewBtn, saveBtn];
    if (pol) {
      const pruneNow = h("input", { type: "checkbox" });
      actions.push(h("button", { class: "btn", type: "button", disabled: !!app.live[c.name], onclick: async () => {
        try {
          await api("POST", credPath(c.name) + "/cleanup", { prune: pruneNow.checked });
          app.live[c.name] = { kind: "cleanup", started_at: new Date().toISOString() };
          toast("Cleanup started.");
        } catch (e) { fail(e); }
      } }, "Clean up now"), h("label", { class: "check small" }, pruneNow, "also free space now"));
      actions.push(h("button", { class: "btn danger ghost ml-auto", type: "button", onclick: async () => {
        if (!(await confirmDialog("Turn off cleanup?", "Backups will be kept forever from now on. Nothing already removed comes back.", "Turn off", true))) return;
        try { await api("DELETE", credPath(c.name) + "/retention"); toast("Cleanup turned off."); await refresh(); go("schedule"); } catch (e) { fail(e); }
      } }, "Turn off"));
    }
    clear(card).append(
      h("div", { class: "card-head" }, h("h2", {}, "Keep old backups")),
      status,
      h("p", { class: "soft small" }, "For each source, keep:"),
      h("div", { class: "field-row mt-8" }, f.last.el, f.daily.el, f.weekly.el, f.monthly.el, f.yearly.el),
      h("div", { class: "mt-14" }, f.prune.el),
      h("p", { class: "small soft mt-14" }, "The newest backup of every source is always kept, and backups made by other tools are never touched."),
      h("div", { class: "btn-row mt-14" }, actions),
      previewBox);
  })();
  return card;
}

function previewView(d) {
  const box = h("div", { class: "mt-14" });
  if (!d.removed) {
    box.append(h("div", { class: "banner info" }, icon("check"), "With these rules nothing would be removed right now."));
    return box;
  }
  box.append(h("p", {}, h("strong", {}, d.removed + " backup(s) would be removed:")));
  const list = h("div", { class: "list mt-8" });
  for (const g of d.groups) {
    list.append(h("div", { class: "row" }, h("div", { class: "grow" },
      h("div", { class: "title" }, g.source || "(untagged)"),
      h("div", { class: "sub" }, "keeps " + g.kept + ", removes " + g.removed.length),
      g.removed.length ? h("details", { class: "mt-4" }, h("summary", { class: "small" }, "Show the backups that would be removed"),
        h("ul", { class: "small mono" }, g.removed.map((sn) => h("li", {}, sn.short_id + "  " + when(sn.time))))) : null)));
  }
  box.append(list);
  return box;
}

// ---------- activity ----------
async function activity(main) {
  const c = currentCred();
  main.append(pageHead("Activity", "Backups and restores on this machine."));
  const live = app.live[c.name];
  if (live) main.append(h("div", { class: "card" }, h("div", { class: "card-head" }, h("h2", {}, (KIND_LABEL[live.kind] || "Job") + " running"), h("button", { class: "btn danger sm", type: "button", onclick: () => cancelJob(c) }, "Cancel")), progressCard(live)));
  const box = h("div", { class: "mt-14" }, loading());
  main.append(box);
  try {
    const list = await api("GET", credPath(c.name) + "/jobs?limit=100");
    clear(box);
    if (!list.length) { box.append(h("div", { class: "card" }, emptyState("history", "No activity yet", "Backups and restores will be listed here."))); return; }
    const tbody = h("tbody", {});
    for (const j of list) {
      const tr = h("tr", { class: "expandable", tabindex: "0" },
        h("td", {}, KIND_LABEL[j.kind] || j.kind, j.trigger === "schedule" ? h("span", { class: "small soft" }, " · scheduled") : null),
        h("td", {}, statusBadge(j.status)),
        h("td", { class: "nowrap" }, when(j.started_at)),
        h("td", { class: "nowrap" }, j.finished_at ? duration((new Date(j.finished_at) - new Date(j.started_at)) / 1000) : "–"),
        h("td", { class: "soft small" }, j.error || ""));
      let open = null;
      const toggle = () => {
        if (open) { open.remove(); open = null; return; }
        open = h("tr", {}, h("td", { colspan: "5", class: "detail-cell" }, jobDetail(j)));
        tr.after(open);
      };
      tr.addEventListener("click", toggle);
      tr.addEventListener("keydown", (e) => { if (e.key === "Enter") toggle(); });
      tbody.append(tr);
    }
    box.append(h("div", { class: "table-wrap" }, h("table", {},
      h("thead", {}, h("tr", {}, ["What", "Result", "Started", "Took", "Note"].map((x) => h("th", {}, x)))), tbody)));
  } catch (e) { clear(box).append(h("div", { class: "banner bad" }, icon("alert"), e.message)); }
}

function jobDetail(j) {
  const d = j.detail || {};
  const out = h("div", {});
  if (j.kind === "cleanup") {
    for (const w of d.warnings || []) out.append(h("div", { class: "banner warn" }, icon("alert"), w));
    out.append(h("p", { class: "small" }, "Removed " + (d.removed || 0) + " old backup(s)" + (d.pruned ? " and freed unused space." : ". Space is freed on the next scheduled prune.")));
    for (const g of d.groups || []) out.append(h("p", { class: "small soft" }, (g.source || "(untagged)") + ": kept " + g.kept + ", removed " + (g.removed || []).length));
    if (d.prune_report) out.append(h("details", { class: "mt-4" }, h("summary", { class: "small" }, "restic prune report"), h("pre", { class: "small mono muted-box break" }, d.prune_report)));
    return out;
  }
  for (const w of d.warnings || []) out.append(h("div", { class: "banner warn" }, icon("alert"), w));
  if (d.sources && d.sources.length) {
    for (const s of d.sources) {
      out.append(h("div", { class: "mt-8" },
        h("strong", {}, s.source), " ", statusBadge(s.status), " ",
        s.snapshot_id ? h("span", { class: "small soft mono" }, s.snapshot_id.slice(0, 8)) : null,
        s.status !== "failed" ? h("span", { class: "small soft" }, " · " + bytes(s.bytes_processed) + " scanned, " + bytes(s.data_added) + " new, " + duration(s.duration_seconds)) : null,
        s.error ? h("p", { class: "small mt-4 break" }, s.error) : null,
        s.skipped && s.skipped.length ? h("details", { class: "mt-4" }, h("summary", { class: "small" }, s.skipped_total + " file(s) could not be read"),
          h("ul", { class: "small mono" }, s.skipped.map((f) => h("li", { class: "break" }, f.item + " (" + f.message + ")")))) : null));
    }
  } else if (d.source) {
    out.append(h("p", { class: "small" }, "Source " + d.source + (d.snapshot_id ? " · snapshot " + d.snapshot_id.slice(0, 8) : "")));
  } else out.append(h("p", { class: "small soft" }, "No details recorded."));
  return out;
}

// ---------- settings ----------
async function settings(main) {
  main.append(pageHead("Settings"));
  const c = currentCred();

  // account
  const sess = sessionFor(c.api_url);
  main.append(h("div", { class: "card" },
    h("div", { class: "card-head" }, h("h2", {}, "storage.bd account")),
    sess
      ? h("div", { class: "btn-row" }, h("span", {}, "Signed in as ", h("strong", {}, sess.email)), h("button", { class: "btn sm ml-auto", type: "button", onclick: async () => {
          try { await api("POST", "/api/account/logout", { api_url: sess.api_url }); toast("Signed out. Backups keep working."); await refresh(); go("settings"); } catch (e) { fail(e); }
        } }, "Sign out"))
      : loginCard(0),
    h("div", { class: "btn-row mt-14" },
      sess ? h("button", { class: "btn", type: "button", onclick: () => connectDialog(sess) }, icon("plus"), "Create another backup space") : null,
      h("button", { class: "btn", type: "button", onclick: () => dialog({ title: "Add a space from the dashboard", body: addExistingCard(sess), actions: [] }) }, "Use a space from the dashboard"))));

  // repos on this machine
  const repos = h("div", { class: "list" });
  for (const r of app.state.credentials) {
    repos.append(h("div", { class: "row" },
      h("div", { class: "icon-tile" }, icon("shield")),
      h("div", { class: "grow" }, h("div", { class: "title" }, r.name, r.default ? h("span", { class: "badge ok ml-8" }, " default") : null),
        h("div", { class: "sub mono" }, r.tenant_id + " · " + r.api_url),
        h("div", { class: "sub" }, r.password_escrowed_at ? "Recovery copy saved with storage.bd" : "Recovery copy not saved yet")),
      r.default ? null : h("button", { class: "btn sm", type: "button", onclick: async () => { try { await api("POST", credPath(r.name) + "/default"); await refresh(); go("settings"); } catch (e) { fail(e); } } }, "Make default"),
      h("button", { class: "btn sm", type: "button", onclick: async () => { try { await api("POST", credPath(r.name) + "/test"); toast("storage.bd accepted " + r.name + ".", "ok"); } catch (e) { fail(e); } } }, "Test"),
      h("button", { class: "btn sm danger", type: "button", "aria-label": "Remove " + r.name, onclick: () => removeRepoDialog(r) }, icon("trash"))));
  }
  main.append(h("div", { class: "card" }, h("div", { class: "card-head" }, h("h2", {}, "Backup spaces on this machine")), repos,
    h("p", { class: "small soft mt-8" }, "Manage credentials from the terminal too: ", h("code", {}, "storagebd credential --help"))));

  // tools
  const toolsBox = h("div", {}, loading());
  main.append(h("div", { class: "card" }, h("div", { class: "card-head" }, h("h2", {}, "Programs")), toolsBox));
  try {
    const s = await api("GET", "/api/settings");
    append(clear(toolsBox), [
      s.restic.error ? h("div", { class: "banner bad" }, icon("alert"), s.restic.error) : h("p", { class: "soft small" }, "restic " + s.restic.version + " at " + s.restic.path),
      s.settings.map((st) => {
        const i = input({ value: st.value, placeholder: "default" });
        const b = h("button", { class: "btn sm", type: "button", onclick: async () => {
          try { await api("PUT", "/api/settings", { key: st.key, value: i.value.trim() }); toast("Saved " + st.key + ".", "ok"); } catch (e) { fail(e); }
        } }, "Save");
        return h("div", { class: "field mt-14" }, h("label", {}, st.key), h("div", { class: "btn-row" }, h("div", { class: "grow" }, i), b), h("span", { class: "hint" }, st.description));
      })]);
    toolsBox.querySelectorAll(".btn-row .grow").forEach((g) => { g.style.flex = "1"; });
  } catch (e) { clear(toolsBox).append(h("p", { class: "soft" }, e.message)); }

  main.append(h("p", { class: "small soft mt-20" }, "storagebd " + app.state.version + (app.state.running_as_root ? " · running as root" : "") + " · free and open source (Apache-2.0) · ",
    h("a", { href: "https://github.com/EpicLabs23/storage-bd-client", target: "_blank", rel: "noopener noreferrer" }, "source code and user guide")));
}

function connectDialog(sess) {
  const card = connectCard(sess, 0);
  dialog({ title: "New backup space", body: card, actions: [] });
}

function removeRepoDialog(r) {
  const confirmIn = input({ placeholder: r.name });
  dialog({
    title: "Remove " + r.name + " from this machine?",
    body: h("div", {},
      h("p", {}, "This deletes the credential, its sources, schedule and history from this machine. The backups on storage.bd are not deleted."),
      !r.password_escrowed_at ? h("div", { class: "banner bad mt-14" }, icon("alert"), "The encryption password was never saved with storage.bd. Without another copy, these backups can never be restored.") : null,
      h("div", { class: "mt-14" }, field("Type " + r.name + " to confirm", confirmIn))),
    actions: [{ label: "Remove", kind: "danger solid", onClick: async () => {
      await api("DELETE", credPath(r.name), { confirm_name: confirmIn.value.trim() });
      toast("Removed " + r.name + ".");
      await refresh();
      go(app.state.credentials.length ? "settings" : "overview");
    } }],
  });
}

// ---------- live events ----------
function connectEvents() {
  const es = new EventSource("/api/events");
  es.onmessage = (m) => {
    let e;
    try { e = JSON.parse(m.data); } catch { return; }
    const live = app.live[e.credential] || (app.live[e.credential] = { kind: e.kind, started_at: new Date().toISOString() });
    switch (e.type) {
      case "job-start": live.job_id = e.job_id; live.kind = e.kind; break;
      case "source-start": live.source = e.source; live.progress = null; break;
      case "progress": live.progress = e.progress; live.source = e.source; break;
      case "phase": live.source = e.message; live.progress = null; break;
      case "warning": toast(e.message); break;
      case "source-done":
        if (e.result && e.result.status === "failed") toast(e.source + ": " + e.result.error, "bad");
        break;
      case "job-done":
        delete app.live[e.credential];
        if (e.status === "succeeded") toast((KIND_LABEL[e.kind] || "Job") + " finished.", "ok");
        else if (e.status === "partial") toast("Backup finished, but some files could not be read. See Activity.");
        else toast((KIND_LABEL[e.kind] || "Job") + " " + (e.status || "failed") + (e.message ? ": " + e.message : ""), "bad");
        refresh().then(() => { if (["overview", "activity", "restore"].includes(app.view)) render(); });
        return;
    }
    if (e.credential !== app.cred) return;
    // Update just the progress card when it's on screen; re-render otherwise.
    const card = document.getElementById("progress-card");
    if (card && (e.type === "progress" || e.type === "source-start" || e.type === "phase")) card.replaceWith(progressCard(live));
    else if (e.type === "job-start" && app.view === "overview") render();
  };
  es.onerror = () => { /* EventSource reconnects by itself */ };
}

// ---------- boot ----------
(async function boot() {
  const v = (location.hash || "").slice(1);
  if (VIEWS.some(([id]) => id === v)) app.view = v;
  try {
    await refresh();
    render();
    connectEvents();
    setInterval(() => { refresh().then(() => { if (app.view === "overview" && !document.querySelector("dialog")) render(); }).catch(() => {}); }, 60000);
  } catch (e) {
    clear(root()).append(h("div", { class: "onboard" }, h("div", { class: "banner bad" }, icon("alert"), e.message)));
  }
})();
