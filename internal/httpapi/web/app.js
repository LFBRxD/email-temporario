(() => {
  const $ = (id) => document.getElementById(id);
  const KEY = "tempmail.boxes";
  const NAME_RE = /^[a-z0-9][a-z0-9._-]{2,31}$/;

  // The URL is the source of truth: /<name> is a mailbox. Each tab shows one.
  let boxes = loadBoxes();   // [{address, deadline(ms), total(s)}] active boxes of this browser
  let cur = null;            // full address being shown
  let seen = [];
  let selected = null;
  let polling = 0;           // generation id to cancel stale poll loops

  const api = (path, opt) => fetch("/api/inbox" + path, opt);
  const enc = encodeURIComponent;
  const local = (addr) => addr.split("@")[0];

  // ---- persistence (per browser; active boxes only) ----
  function loadBoxes() {
    try {
      const l = JSON.parse(localStorage.getItem(KEY) || "[]");
      return l.filter((b) => b && typeof b.address === "string" && NAME_RE.test(local(b.address)) && b.deadline > Date.now());
    } catch (e) { return []; }
  }
  function saveBoxes() { try { localStorage.setItem(KEY, JSON.stringify(boxes)); } catch (e) {} }
  function upsert(address, expiresIn) {
    const deadline = Date.now() + expiresIn * 1000;
    const b = boxes.find((x) => x.address === address);
    if (b) { b.deadline = deadline; return; }
    boxes.push({ address, deadline, total: Math.max(expiresIn, 1) });
  }

  // ---- helpers ----
  function fmt(s) {
    s = Math.max(0, s);
    return String(Math.floor(s / 60)).padStart(2, "0") + ":" + String(s % 60).padStart(2, "0");
  }
  const left = (b) => Math.max(0, Math.round((b.deadline - Date.now()) / 1000));
  function notice(text, canReopen) {
    $("notice-text").textContent = text || "";
    $("reopen").hidden = !canReopen;
    $("notice").hidden = !text;
  }
  function pathName() {
    let p = "";
    try { p = decodeURIComponent(location.pathname.slice(1)); } catch (e) {}
    return NAME_RE.test(p.toLowerCase()) ? p.toLowerCase() : "";
  }

  // ---- chips + countdown ----
  function renderBoxes() {
    const nav = $("boxes");
    nav.replaceChildren();
    for (const b of boxes) {
      const chip = document.createElement("span");
      chip.className = "chip" + (b.address === cur ? " cur" : "");
      const go = document.createElement("a");
      go.className = "chip-go";
      go.href = "/" + enc(local(b.address));
      go.title = b.address;
      go.textContent = local(b.address).slice(0, 14) + (local(b.address).length > 14 ? "…" : "") + "  " + fmt(left(b));
      const x = document.createElement("button");
      x.type = "button";
      x.className = "chip-x";
      x.title = "Remover da lista (a caixa continua existindo até expirar)";
      x.textContent = "×";
      x.onclick = () => { boxes = boxes.filter((o) => o.address !== b.address); saveBoxes(); renderBoxes(); };
      chip.append(go, x);
      nav.append(chip);
    }
    nav.hidden = boxes.length === 0;
  }

  function tick() {
    const expiredNow = cur && boxes.some((b) => b.address === cur && left(b) === 0);
    const n = boxes.length;
    boxes = boxes.filter((b) => left(b) > 0 || b.address === cur);
    const b = boxes.find((x) => x.address === cur);
    if (b) {
      const l = left(b);
      $("timer").textContent = fmt(l);
      $("timer").classList.toggle("low", l <= 60);
      $("bar-fill").style.width = (100 * l / b.total) + "%";
    }
    if (expiredNow) {
      polling++;
      boxes = boxes.filter((o) => o.address !== cur);
      $("status").textContent = "Caixa expirada";
      notice("A caixa " + cur + " expirou e as mensagens foram apagadas.", true);
    }
    if (boxes.length !== n) saveBoxes();
    renderBoxes();
  }
  setInterval(tick, 1000);

  // ---- open / create ----
  // How the page gets its inbox: PUT /api/inbox/<name> (open or create) for a
  // named URL, POST /api/inbox for a random one.
  async function enter(method, path, body) {
    $("status").textContent = "Abrindo caixa…";
    let r;
    try {
      r = await api(path, { method, headers: body ? { "Content-Type": "application/json" } : {}, body });
    } catch (e) {
      $("addr").textContent = "sem conexão";
      return notice("Não foi possível falar com o servidor.", false);
    }
    const v = await r.json().catch(() => ({}));
    if (!r.ok) {
      $("addr").textContent = "indisponível";
      $("status").textContent = "";
      return notice(v.error || "Falha ao abrir a caixa.", false);
    }
    cur = v.address;
    upsert(v.address, v.expires_in);
    saveBoxes();
    if (pathName() !== local(v.address)) {
      try { history.replaceState(null, "", "/" + enc(local(v.address))); } catch (e) {}
    }
    document.title = local(v.address) + " · Mail Temporário";
    $("addr").textContent = v.address;
    $("domain").textContent = "@" + v.address.split("@")[1];
    $("status").textContent = "Aguardando mensagens…";
    notice("");
    seen = []; selected = null;
    renderList(); renderEmpty(); renderApiDoc(); tick();
    merge(v.messages);
    poll();
  }

  // ---- messages ----
  function merge(msgs) {
    if (!msgs.length) return;
    const ids = new Set(seen.map((m) => m.id));
    for (const m of msgs) if (!ids.has(m.id)) seen.push(m);
    renderList();
    $("status").textContent = seen.length + " mensagem(ns)";
  }

  async function poll() {
    const gen = ++polling;
    while (gen === polling && cur) {
      const after = seen.reduce((a, m) => Math.max(a, +m.id), 0);
      try {
        const r = await api("/" + enc(cur) + "/messages?wait=25&after=" + after);
        if (gen !== polling) return;
        if (r.status === 404) return; // tick() reports the expiry
        if (r.ok) merge((await r.json()).messages);
      } catch (e) {
        await new Promise((res) => setTimeout(res, 3000));
      }
    }
  }

  function renderList() {
    const ul = $("list");
    ul.replaceChildren();
    if (!seen.length) {
      const li = document.createElement("li");
      li.className = "empty";
      li.textContent = "Nenhuma mensagem ainda.";
      ul.append(li);
      return;
    }
    for (const m of [...seen].reverse()) {
      const li = document.createElement("li");
      if (m.id === selected) li.className = "sel";
      const f = document.createElement("div"); f.className = "from"; f.textContent = m.from || "(desconhecido)";
      const s = document.createElement("div"); s.className = "subj"; s.textContent = m.subject || "(sem assunto)";
      const t = document.createElement("div"); t.className = "meta";
      t.textContent = new Date(m.received_at).toLocaleTimeString() + (m.attachments ? " · 📎 " + m.attachments : "");
      li.append(f, s, t);
      li.onclick = () => open(m.id);
      ul.append(li);
    }
  }

  function renderEmpty() {
    const v = $("view");
    v.replaceChildren();
    const p = document.createElement("p");
    p.className = "muted center";
    p.textContent = "Selecione uma mensagem.";
    v.append(p);
  }

  async function open(id) {
    if (!cur) return;
    selected = id; renderList();
    const base = "/" + enc(cur) + "/messages/" + id;
    const r = await api(base);
    if (!r.ok) return;
    const m = await r.json();
    const v = $("view");
    v.replaceChildren();
    const h = document.createElement("h2"); h.textContent = m.subject || "(sem assunto)";
    const hd = document.createElement("div"); hd.className = "hdr";
    hd.textContent = "De: " + m.from + " · Para: " + m.to + " · " + new Date(m.received_at).toLocaleString();
    v.append(h, hd);

    if (m.html) {
      const f = document.createElement("iframe");
      // No scripts, no same-origin; only popups so links open in a new tab.
      f.setAttribute("sandbox", "allow-popups allow-popups-to-escape-sandbox");
      f.setAttribute("referrerpolicy", "no-referrer");
      f.srcdoc = '<base target="_blank"><meta http-equiv="Content-Security-Policy" content="default-src \'none\'; style-src \'unsafe-inline\'; img-src data:">' + m.html;
      v.append(f);
    } else {
      const pre = document.createElement("pre"); pre.className = "text"; pre.textContent = m.text || "(mensagem vazia)";
      v.append(pre);
    }

    if (m.attachments.length) {
      const ul = document.createElement("ul"); ul.className = "atts";
      for (const a of m.attachments) {
        const li = document.createElement("li");
        const link = document.createElement("a");
        link.href = "/api/inbox" + base + "/attachments/" + a.id;
        link.textContent = "📎 " + (a.filename || "anexo-" + a.id) + " (" + Math.ceil(a.size / 1024) + " KB)";
        li.append(link); ul.append(li);
      }
      v.append(ul);
    }
    const act = document.createElement("div"); act.className = "actions";
    const raw = document.createElement("a");
    raw.href = "/api/inbox" + base + "/raw"; raw.textContent = "Baixar .eml original";
    act.append(raw); v.append(act);
  }

  function renderApiDoc() {
    const o = location.origin, n = cur ? local(cur) : "meunome";
    $("api-doc").textContent =
`# abrir (ou criar) a caixa pelo nome - idempotente
curl -X PUT ${o}/api/inbox/${n}

# criar com nome aleatório
curl -X POST ${o}/api/inbox

# listar mensagens (long-poll: espera até 30s por novas)
curl "${o}/api/inbox/${n}/messages?wait=25&after=0"

# ler mensagem (texto, html, lista de anexos)
curl ${o}/api/inbox/${n}/messages/1

# baixar anexo / e-mail original (.eml)
curl -OJ ${o}/api/inbox/${n}/messages/1/attachments/0
curl -OJ ${o}/api/inbox/${n}/messages/1/raw

# apagar a caixa antes de expirar
curl -X DELETE ${o}/api/inbox/${n}`;
  }

  // ---- controls ----
  function flash(btn, text, orig) {
    btn.textContent = text;
    setTimeout(() => (btn.textContent = orig), 1500);
  }
  $("copy").onclick = async () => {
    if (!cur) return;
    try { await navigator.clipboard.writeText(cur); flash($("copy"), "Copiado!", "Copiar endereço"); }
    catch (e) { flash($("copy"), "Selecione e copie", "Copiar endereço"); }
  };
  $("copylink").onclick = async () => {
    if (!cur) return;
    try { await navigator.clipboard.writeText(location.href); flash($("copylink"), "Link copiado!", "Copiar link"); }
    catch (e) { flash($("copylink"), "Copie da barra de endereço", "Copiar link"); }
  };
  $("reopen").onclick = () => location.reload(); // same /<name> URL: PUT recreates it
  // Both "open by name" forms just navigate: the server validates the name and,
  // when it is bad, redirects to the welcome page with the reason.
  for (const form of document.querySelectorAll(".name-form")) {
    form.onsubmit = (e) => {
      e.preventDefault();
      const n = form.querySelector("input").value.trim().toLowerCase();
      if (n) location.href = "/" + enc(n);
    };
  }

  // ---- start ----
  const ERRORS = {
    curto: "Nome da caixa de correio muito curto. Use pelo menos 3 caracteres.",
    longo: "Nome da caixa de correio muito longo. Use no máximo 32 caracteres.",
    reservado: "Este nome é reservado. Escolha outro.",
    invalido: "Nome de caixa inválido. Use letras, números, ponto, hífen ou sublinhado, começando por letra ou número.",
  };

  function showWelcome() {
    $("welcome").hidden = false;
    $("example").textContent = location.origin + "/nome-da-caixa";
    document.title = "Mail Temporário";
    const code = new URLSearchParams(location.search).get("erro");
    if (code) {
      // Only known codes map to text; anything else in the URL is ignored.
      notice(Object.prototype.hasOwnProperty.call(ERRORS, code) ? ERRORS[code] : "", false);
      $("new2").hidden = true;
      try { history.replaceState(null, "", "/"); } catch (e) {} // a refresh must not repeat the error
    }
    renderApiDoc(); renderBoxes();
  }

  function showBox() {
    $("box").hidden = false;
    renderList(); renderEmpty(); renderApiDoc(); renderBoxes();
  }

  const name = pathName();
  if (name) {
    showBox();
    enter("PUT", "/" + enc(name));
  } else if (new URLSearchParams(location.search).has("new")) {
    showBox();
    enter("POST", "");
  } else {
    showWelcome();
  }
})();
