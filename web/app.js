(function () {
  "use strict";

  const state = {
    board: null,
    config: null,
    search: "",
    selected: null,
    dragging: null,
    sse: null,
    sseFailures: 0,
    reconnectTimer: null,
    pollTimer: null,
    logTimer: null,
    renderQueued: false,
  };

  const $ = (id) => document.getElementById(id);
  const els = {
    board: $("board"),
    name: $("board-name"),
    repo: $("board-repo"),
    search: $("search"),
    server: $("server-state"),
    serverLabel: $("server-label"),
    toasts: $("toasts"),
    providerNotice: $("provider-notice"),
    themeBtn: $("theme-btn"),
    syncBtn: $("sync-btn"),
    syncDetail: $("sync-detail"),
    syncSubmit: $("sync-submit"),
    syncProvider: $("sync-provider"),
    newModal: $("new-modal"),
    newForm: $("new-form"),
    newColumn: $("new-column"),
    syncModal: $("sync-modal"),
    syncForm: $("sync-form"),
    syncResult: $("sync-result"),
    drawer: $("drawer"),
    dTitle: $("d-title"),
    dBody: $("d-body"),
    dColumn: $("d-column"),
    dPriority: $("d-priority"),
    dLabels: $("d-labels"),
    dAssignee: $("d-assignee"),
    dMeta: $("d-meta"),
    dLog: $("d-log"),
    dLogStatus: $("d-log-status"),
    dSave: $("d-save"),
    dRun: $("d-run"),
    dRetry: $("d-retry"),
    dCancel: $("d-cancel"),
    dDelete: $("d-delete"),
  };

  const THEME_KEY = "pegg-kanban-theme";
  const themeQuery = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)") : null;

  function storedTheme() {
    try {
      const value = localStorage.getItem(THEME_KEY);
      return value === "light" || value === "dark" ? value : null;
    } catch (err) {
      return null;
    }
  }

  function preferredTheme() {
    return themeQuery && themeQuery.matches ? "dark" : "light";
  }

  function applyTheme(theme) {
    const next = theme === "dark" ? "dark" : "light";
    document.documentElement.setAttribute("data-theme", next);
    document.documentElement.classList.toggle("dark", next === "dark");
    const meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.setAttribute("content", next === "dark" ? "#070707" : "#ffffff");
    if (els.themeBtn) {
      const label = next === "dark" ? "Switch to light theme" : "Switch to dark theme";
      els.themeBtn.setAttribute("aria-label", label);
      els.themeBtn.setAttribute("title", label);
      els.themeBtn.setAttribute("aria-pressed", next === "dark" ? "true" : "false");
    }
  }

  function toggleTheme() {
    const next = document.documentElement.getAttribute("data-theme") === "dark" ? "light" : "dark";
    applyTheme(next);
    try {
      localStorage.setItem(THEME_KEY, next);
    } catch (err) {
      /* storage may be unavailable */
    }
  }

  applyTheme(storedTheme() || preferredTheme());
  if (themeQuery) {
    const onSchemeChange = function (event) {
      if (!storedTheme()) applyTheme(event.matches ? "dark" : "light");
    };
    if (themeQuery.addEventListener) themeQuery.addEventListener("change", onSchemeChange);
    else if (themeQuery.addListener) themeQuery.addListener(onSchemeChange);
  }

  const query = new URLSearchParams(window.location.search);
  if (query.get("token")) {
    sessionStorage.setItem("pegg-kanban-token", query.get("token"));
  }

  function token() {
    return sessionStorage.getItem("pegg-kanban-token") || "";
  }

  async function api(path, options) {
    const opts = Object.assign({}, options || {});
    const headers = Object.assign({}, opts.headers || {});
    if (opts.body !== undefined && typeof opts.body !== "string") {
      opts.body = JSON.stringify(opts.body);
    }
    if (opts.body !== undefined) {
      headers["Content-Type"] = "application/json";
    }
    const auth = token();
    if (auth) headers["X-PegG-Token"] = auth;
    opts.headers = headers;
    const res = await fetch(path, opts);
    const text = await res.text();
    const contentType = res.headers.get("content-type") || "";
    if (!res.ok) {
      let message = text;
      try {
        const parsed = JSON.parse(text);
        if (parsed && parsed.error) message = parsed.error;
      } catch (err) {
        /* keep raw text */
      }
      throw new Error(message || res.statusText);
    }
    if (contentType.indexOf("application/json") >= 0) {
      return text ? JSON.parse(text) : null;
    }
    return text;
  }

  function toast(message, kind) {
    const el = document.createElement("div");
    el.className = "toast" + (kind ? " " + kind : "");
    el.textContent = message;
    els.toasts.appendChild(el);
    window.setTimeout(function () {
      el.remove();
    }, 4200);
  }

  function setServer(ok, label) {
    els.server.classList.toggle("ok", !!ok);
    els.server.classList.toggle("bad", !ok);
    els.serverLabel.textContent = label || (ok ? "connected" : "offline");
  }

  function splitLabels(value) {
    return value
      .split(",")
      .map(function (part) { return part.trim(); })
      .filter(Boolean);
  }

  async function loadBoard() {
    try {
      state.board = await api("/api/board");
      render();
      setServer(true, "connected");
    } catch (err) {
      setServer(false, "offline");
      toast("Cannot load board: " + err.message, "error");
    }
  }

  async function loadConfig() {
    try {
      state.config = await api("/api/config");
    } catch (err) {
      state.config = null;
    }
    applyProviderAvailability();
  }

  function providers() {
    const list = state.config && Array.isArray(state.config.providers) ? state.config.providers : [];
    return list.filter(function (info) { return info && info.id; });
  }

  function providerInfo(provider) {
    const list = providers();
    for (let i = 0; i < list.length; i += 1) {
      if (list[i].id === provider) return list[i];
    }
    return null;
  }

  function providerLabel(provider) {
    const info = providerInfo(provider);
    if (info && info.label) return info.label;
    return provider;
  }

  function applyProviderAvailability() {
    let anyAvailable = false;
    let known = 0;
    providers().forEach(function (info) {
      known += 1;
      if (info.available === false) {
        if (els.providerNotice) {
          els.providerNotice.textContent = info.detail || info.label + " sync is unavailable. Manual cards and agent runs still work.";
          els.providerNotice.hidden = false;
        }
      } else {
        anyAvailable = true;
      }
    });
    if (els.providerNotice && (anyAvailable || known === 0)) {
      els.providerNotice.hidden = true;
      els.providerNotice.textContent = "";
    }
    els.syncBtn.disabled = known > 0 && !anyAvailable;
    els.syncBtn.title = els.syncBtn.disabled
      ? "No issue provider is available"
      : "Sync integration issues";
  }

  function sortedCards(columnID) {
    if (!state.board) return [];
    return state.board.cards
      .filter(function (card) { return card.column === columnID; })
      .sort(function (a, b) {
        if (a.position !== b.position) return a.position - b.position;
        return String(a.created_at).localeCompare(String(b.created_at));
      });
  }

  function matchesSearch(card) {
    if (!state.search) return true;
    const haystack = [
      card.id,
      card.title,
      card.body,
      card.assignee,
      (card.labels || []).join(" "),
    ]
      .join(" ")
      .toLowerCase();
    return haystack.indexOf(state.search) >= 0;
  }

  function scheduleRender() {
    if (state.renderQueued || state.dragging) return;
    state.renderQueued = true;
    window.requestAnimationFrame(function () {
      state.renderQueued = false;
      render();
    });
  }

  function render() {
    if (!state.board) return;
    els.name.textContent = "Pegg Kanban";
    const boardName = state.board.name || "";
    if (boardName && boardName !== "Kanban" && boardName !== "Pegg Kanban") {
      document.title = boardName + " · Pegg Kanban";
    } else {
      document.title = "Pegg Kanban";
    }
    els.repo.textContent = state.board.repo || (state.config && state.config.workdir) || "";
    const scrolls = {};
    Array.prototype.forEach.call(els.board.querySelectorAll(".column"), function (col) {
      const list = col.querySelector(".cards");
      if (list) scrolls[col.dataset.column] = list.scrollTop;
    });

    const fragment = document.createDocumentFragment();
    state.board.columns.forEach(function (column) {
      const cards = sortedCards(column.id).filter(matchesSearch);
      const total = sortedCards(column.id).length;
      const section = document.createElement("section");
      section.className = "column";
      section.dataset.column = column.id;
      section.setAttribute("aria-label", column.name);

      const head = document.createElement("div");
      head.className = "column-head";
      const title = document.createElement("h2");
      title.textContent = column.name;
      head.appendChild(title);
      const count = document.createElement("span");
      count.className = "count";
      if (column.wip && total > column.wip) count.classList.add("over");
      count.textContent = column.wip ? total + "/" + column.wip : String(total);
      head.appendChild(count);
      section.appendChild(head);

      const list = document.createElement("div");
      list.className = "cards";
      list.dataset.column = column.id;

      if (cards.length === 0) {
        const empty = document.createElement("div");
        empty.className = "empty";
        empty.textContent = total === 0 ? "No cards" : "No matches";
        list.appendChild(empty);
      }
      cards.forEach(function (card) {
        list.appendChild(cardElement(card));
      });
      section.appendChild(list);

      section.addEventListener("dragover", function (event) {
        event.preventDefault();
        section.classList.add("drop-target");
      });
      section.addEventListener("dragleave", function (event) {
        if (!section.contains(event.relatedTarget)) {
          section.classList.remove("drop-target");
        }
      });
      section.addEventListener("drop", function (event) {
        event.preventDefault();
        section.classList.remove("drop-target");
        handleDrop(event, column.id, list);
      });

      fragment.appendChild(section);
      if (scrolls[column.id]) {
        list.scrollTop = scrolls[column.id];
      }
    });

    els.board.replaceChildren(fragment);
    populateColumnSelects();
  }

  function cardElement(card) {
    const el = document.createElement("article");
    el.className = "card";
    el.draggable = true;
    el.tabIndex = 0;
    el.dataset.id = card.id;
    el.dataset.source = card.source || "manual";
    el.setAttribute("role", "button");
    el.setAttribute("aria-label", card.title + " — open card details");

    const title = document.createElement("div");
    title.className = "card-title";
    title.textContent = card.title;
    el.appendChild(title);

    const meta = document.createElement("div");
    meta.className = "card-meta";

    if (card.priority) {
      const pill = document.createElement("span");
      pill.className = "pill " + card.priority;
      pill.textContent = card.priority;
      meta.appendChild(pill);
    }
    if (card.source && card.source !== "manual") {
      const badge = document.createElement("span");
      badge.className = "badge source-" + String(card.source).replace(/[^a-z0-9_-]/gi, "");
      badge.dataset.source = card.source;
      badge.textContent = card.issue && card.issue.provider ? card.issue.provider : card.source;
      meta.appendChild(badge);
    }
    if (card.issue) {
      let label = "";
      if (card.issue.key) {
        label = card.issue.key;
      } else if (card.issue.number) {
        label = (card.issue.repo || "issue") + "#" + card.issue.number;
      }
      if (label && card.issue.provider) {
        label = card.issue.provider + " " + label;
      }
      if (label) {
        const link = document.createElement("a");
        link.className = "badge issue";
        link.href = card.issue.url || "#";
        link.target = "_blank";
        link.rel = "noopener noreferrer";
        link.textContent = label;
        link.addEventListener("click", function (event) { event.stopPropagation(); });
        meta.appendChild(link);
      }
    }
    if (card.context && (card.context.branch || card.context.label || card.context.kind)) {
      const branch = document.createElement("span");
      branch.className = "badge branch";
      branch.textContent = card.context.branch || card.context.label || card.context.kind;
      meta.appendChild(branch);
    }
    if (card.run && card.run.status && card.run.status !== "idle") {
      const run = document.createElement("span");
      run.className = "run-badge " + card.run.status;
      run.textContent = card.run.status;
      meta.appendChild(run);
    }
    (card.labels || []).slice(0, 4).forEach(function (label) {
      const badge = document.createElement("span");
      badge.className = "badge";
      badge.textContent = label;
      meta.appendChild(badge);
    });

    el.appendChild(meta);

    el.addEventListener("click", function () {
      openDrawer(card.id);
    });
    el.addEventListener("keydown", function (event) {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        openDrawer(card.id);
      }
    });
    el.addEventListener("dragstart", function (event) {
      state.dragging = card.id;
      el.classList.add("dragging");
      if (event.dataTransfer) {
        event.dataTransfer.setData("text/plain", card.id);
        event.dataTransfer.effectAllowed = "move";
      }
    });
    el.addEventListener("dragend", function () {
      state.dragging = null;
      el.classList.remove("dragging");
      Array.prototype.forEach.call(document.querySelectorAll(".drop-target"), function (col) {
        col.classList.remove("drop-target");
      });
      scheduleRender();
    });
    return el;
  }

  function handleDrop(event, columnID, list) {
    const id = state.dragging || (event.dataTransfer && event.dataTransfer.getData("text/plain"));
    if (!id) return;
    const cards = Array.prototype.slice.call(list.querySelectorAll(".card"));
    let position = cards.length;
    for (let i = 0; i < cards.length; i += 1) {
      const rect = cards[i].getBoundingClientRect();
      if (event.clientY < rect.top + rect.height / 2) {
        position = i;
        break;
      }
    }
    state.dragging = null;

    const card = (state.board.cards || []).find(function (item) { return item.id === id; });
    if (card) {
      card.column = columnID;
      card.position = position;
      render();
    }
    api("/api/cards/" + encodeURIComponent(id) + "/move", {
      method: "POST",
      body: { column: columnID, position: position },
    })
      .then(function () {
        return loadBoard();
      })
      .catch(function (err) {
        toast("Move failed: " + err.message, "error");
        loadBoard();
      });
  }

  function populateColumnSelects() {
    if (!state.board) return;
    const selects = [els.newColumn, els.dColumn];
    selects.forEach(function (select) {
      const current = select.value;
      select.replaceChildren();
      state.board.columns.forEach(function (column) {
        const option = document.createElement("option");
        option.value = column.id;
        option.textContent = column.name;
        select.appendChild(option);
      });
      if (current) select.value = current;
    });
  }

  function openModal(el, focusSelector) {
    el.hidden = false;
    window.setTimeout(function () {
      const target = focusSelector ? el.querySelector(focusSelector) : null;
      if (target) target.focus();
    }, 30);
  }

  function closeModal(el) {
    el.hidden = true;
  }

  function openNewCard() {
    els.newForm.reset();
    populateColumnSelects();
    els.newColumn.value = "backlog";
    openModal(els.newModal, "input[name=title]");
  }

  function populateSyncProviders() {
    const select = els.syncProvider;
    const current = select.value;
    select.innerHTML = "";
    const all = document.createElement("option");
    all.value = "all";
    all.textContent = "All providers";
    select.appendChild(all);
    providers().forEach(function (info) {
      const option = document.createElement("option");
      option.value = info.id;
      option.textContent = info.label || info.id;
      select.appendChild(option);
    });
    if (current && Array.prototype.some.call(select.options, function (option) { return option.value === current; })) {
      select.value = current;
    }
  }

  function updateSyncProviderPanel() {
    const provider = els.syncProvider.value || "all";
    const info = provider === "all" ? null : providerInfo(provider);
    const unavailable = !!(info && info.available === false);
    els.syncDetail.textContent = unavailable
      ? info.detail || providerLabel(provider) + " sync is unavailable in this environment."
      : "";
    els.syncDetail.hidden = !unavailable;
    els.syncSubmit.disabled = unavailable;
  }

  function openSyncModal() {
    populateSyncProviders();
    if (state.config) {
      const form = els.syncForm;
      if (!form.labels.value) form.labels.value = (state.config.sync_labels || []).join(", ");
      if (!form.state.value) form.state.value = state.config.sync_state || "";
      if (!form.limit.value && state.config.sync_limit) form.limit.value = state.config.sync_limit;
      if (!form.query.value) form.query.value = "";
    }
    const currentInfo = providerInfo(els.syncProvider.value);
    if (currentInfo && currentInfo.available === false && els.syncProvider.value !== "all") {
      const list = providers();
      for (let i = 0; i < list.length; i += 1) {
        if (list[i].available !== false) {
          els.syncProvider.value = list[i].id;
          break;
        }
      }
    }
    updateSyncProviderPanel();
    els.syncResult.textContent = "";
    openModal(els.syncModal, els.syncSubmit.disabled ? ".icon" : "#sync-form input");
  }

  function openDrawer(id) {
    api("/api/cards/" + encodeURIComponent(id))
      .then(function (card) {
        state.selected = card;
        els.dTitle.value = card.title || "";
        els.dBody.value = card.body || "";
        els.dColumn.value = card.column || "";
        els.dPriority.value = card.priority || "";
        els.dLabels.value = (card.labels || []).join(", ");
        els.dAssignee.value = card.assignee || "";
        renderMeta(card);
        els.dLog.textContent = "(loading log)";
        els.drawer.hidden = false;
        startLogPolling(card.id);
        window.setTimeout(function () { els.dTitle.focus(); }, 30);
      })
      .catch(function (err) {
        toast("Cannot open card: " + err.message, "error");
      });
  }

  function closeDrawer() {
    state.selected = null;
    stopLogPolling();
    els.drawer.hidden = true;
  }

  function addMeta(term, value) {
    if (!value) return;
    const dt = document.createElement("dt");
    dt.textContent = term;
    const dd = document.createElement("dd");
    dd.textContent = value;
    els.dMeta.appendChild(dt);
    els.dMeta.appendChild(dd);
  }

  function renderMeta(card) {
    els.dMeta.replaceChildren();
    addMeta("ID", card.id);
    addMeta("Source", card.source);
    addMeta("Created", card.created_at);
    addMeta("Updated", card.updated_at);
    if (card.issue) {
      if (card.issue.provider) addMeta("Provider", card.issue.provider);
      if (card.issue.key) addMeta("Key", card.issue.key);
      if (card.issue.number) addMeta("Issue", (card.issue.repo || "") + "#" + card.issue.number);
      addMeta("Issue URL", card.issue.url);
    }
    if (card.context) {
      addMeta("Environment", card.context.kind || "repo");
      addMeta("Path", card.context.path);
      addMeta("Branch", card.context.branch);
    }
    if (card.run) {
      addMeta("Run", card.run.status);
      if (card.run.pid) addMeta("PID", String(card.run.pid));
      if (card.run.started_at) addMeta("Started", card.run.started_at);
      if (card.run.finished_at) addMeta("Finished", card.run.finished_at);
      if (typeof card.run.exit_code === "number" && card.run.status !== "idle" && card.run.status !== "running") {
        addMeta("Exit code", String(card.run.exit_code));
      }
      if (card.run.error) addMeta("Error", card.run.error);
    }
  }

  function startLogPolling(id) {
    stopLogPolling();
    pollLog(id);
    state.logTimer = window.setInterval(function () { pollLog(id); }, 2000);
  }

  function stopLogPolling() {
    if (state.logTimer) {
      window.clearInterval(state.logTimer);
      state.logTimer = null;
    }
  }

  function pollLog(id) {
    if (!state.selected || state.selected.id !== id) return;
    fetch("/api/cards/" + encodeURIComponent(id) + "/log?tail=200", {
      headers: token() ? { "X-PegG-Token": token() } : {},
    })
      .then(function (res) {
        if (!res.ok) throw new Error(res.statusText);
        return res.text();
      })
      .then(function (text) {
        if (!state.selected || state.selected.id !== id) return;
        const atBottom = els.dLog.scrollTop + els.dLog.clientHeight >= els.dLog.scrollHeight - 24;
        els.dLog.textContent = text || "(no log yet)";
        if (atBottom) els.dLog.scrollTop = els.dLog.scrollHeight;
        els.dLogStatus.textContent = state.selected.run ? state.selected.run.status : "";
      })
      .catch(function () {
        els.dLogStatus.textContent = "log unavailable";
      });
  }

  async function saveCard() {
    if (!state.selected) return;
    const patch = {
      title: els.dTitle.value,
      body: els.dBody.value,
      column: els.dColumn.value,
      priority: els.dPriority.value,
      labels: splitLabels(els.dLabels.value),
      assignee: els.dAssignee.value,
    };
    try {
      const card = await api("/api/cards/" + encodeURIComponent(state.selected.id), {
        method: "PATCH",
        body: patch,
      });
      state.selected = card;
      renderMeta(card);
      toast("Card saved", "success");
      await loadBoard();
    } catch (err) {
      toast("Save failed: " + err.message, "error");
    }
  }

  async function cardAction(action) {
    if (!state.selected) return;
    const id = state.selected.id;
    try {
      const card = await api("/api/cards/" + encodeURIComponent(id) + "/" + action, {
        method: "POST",
        body: action === "run" || action === "retry" || action === "cancel" ? {} : undefined,
      });
      if (card && card.id) {
        state.selected = card;
        renderMeta(card);
      }
      toast(action + " requested", "success");
      await loadBoard();
    } catch (err) {
      toast(action + " failed: " + err.message, "error");
    }
  }

  async function deleteCard() {
    if (!state.selected) return;
    if (!window.confirm("Delete this card?")) return;
    try {
      await api("/api/cards/" + encodeURIComponent(state.selected.id), { method: "DELETE" });
      closeDrawer();
      toast("Card deleted", "success");
      await loadBoard();
    } catch (err) {
      toast("Delete failed: " + err.message, "error");
    }
  }

  function connectSSE() {
    if (state.reconnectTimer) {
      window.clearTimeout(state.reconnectTimer);
      state.reconnectTimer = null;
    }
    if (state.sse) {
      state.sse.close();
      state.sse = null;
    }
    const suffix = token() ? "?token=" + encodeURIComponent(token()) : "";
    const source = new EventSource("/api/events" + suffix);
    state.sse = source;

    source.addEventListener("open", function () {
      state.sseFailures = 0;
      setServer(true, "connected");
      stopPolling();
    });
    source.addEventListener("board", function (event) {
      state.sseFailures = 0;
      setServer(true, "connected");
      try {
        state.board = JSON.parse(event.data);
        if (state.selected) {
          const current = (state.board.cards || []).find(function (card) {
            return card.id === state.selected.id;
          });
          if (current) {
            state.selected = current;
            renderMeta(current);
          }
        }
        scheduleRender();
      } catch (err) {
        /* ignore malformed event */
      }
    });
    source.addEventListener("card", function (event) {
      try {
        const card = JSON.parse(event.data);
        updateLocalCard(card);
        if (state.selected && state.selected.id === card.id) {
          state.selected = card;
          renderMeta(card);
        }
        scheduleRender();
      } catch (err) {
        /* ignore malformed event */
      }
    });
    source.addEventListener("error", function () {
      setServer(false, "reconnecting");
      state.sseFailures += 1;
      source.close();
      if (state.sse === source) state.sse = null;
      if (state.sseFailures >= 3) {
        startPolling();
      }
      const delay = Math.min(30000, 1000 * Math.pow(2, Math.min(state.sseFailures, 5)));
      state.reconnectTimer = window.setTimeout(connectSSE, delay);
    });
  }

  function updateLocalCard(card) {
    if (!state.board || !state.board.cards) return;
    for (let i = 0; i < state.board.cards.length; i += 1) {
      if (state.board.cards[i].id === card.id) {
        state.board.cards[i] = card;
        return;
      }
    }
  }

  function startPolling() {
    if (state.pollTimer) return;
    state.pollTimer = window.setInterval(loadBoard, 10000);
  }

  function stopPolling() {
    if (state.pollTimer) {
      window.clearInterval(state.pollTimer);
      state.pollTimer = null;
    }
  }

  function isTyping(target) {
    if (!target) return false;
    const tag = target.tagName;
    return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || target.isContentEditable;
  }

  function modalOpen() {
    return !els.newModal.hidden || !els.syncModal.hidden || !els.drawer.hidden;
  }

  function closeTopmost() {
    if (!els.drawer.hidden) {
      closeDrawer();
      return true;
    }
    if (!els.newModal.hidden) {
      closeModal(els.newModal);
      return true;
    }
    if (!els.syncModal.hidden) {
      closeModal(els.syncModal);
      return true;
    }
    return false;
  }

  document.addEventListener("click", function (event) {
    const closer = event.target.closest("[data-close]");
    if (closer) {
      const id = closer.getAttribute("data-close");
      const el = document.getElementById(id);
      if (el) {
        if (id === "drawer") closeDrawer();
        else closeModal(el);
      }
      return;
    }
    if (event.target === els.newModal) closeModal(els.newModal);
    if (event.target === els.syncModal) closeModal(els.syncModal);
    if (event.target === els.drawer) closeDrawer();
  });

  document.addEventListener("keydown", function (event) {
    if (event.key === "Escape") {
      if (closeTopmost()) event.preventDefault();
      return;
    }
    if ((event.key === "n" || event.key === "N") && !isTyping(event.target) && !modalOpen()) {
      event.preventDefault();
      openNewCard();
    }
  });

  els.search.addEventListener("input", function () {
    state.search = els.search.value.trim().toLowerCase();
    render();
  });

  $("new-btn").addEventListener("click", openNewCard);
  els.syncBtn.addEventListener("click", openSyncModal);
  els.syncProvider.addEventListener("change", updateSyncProviderPanel);
  els.themeBtn.addEventListener("click", toggleTheme);
  els.dSave.addEventListener("click", saveCard);
  els.dRun.addEventListener("click", function () { cardAction("run"); });
  els.dRetry.addEventListener("click", function () { cardAction("retry"); });
  els.dCancel.addEventListener("click", function () { cardAction("cancel"); });
  els.dDelete.addEventListener("click", deleteCard);

  els.newForm.addEventListener("submit", async function (event) {
    event.preventDefault();
    const form = new FormData(els.newForm);
    const number = parseInt(String(form.get("issue_number") || "0"), 10);
    const repo = String(form.get("issue_repo") || "").trim();
    const payload = {
      title: String(form.get("title") || "").trim(),
      body: String(form.get("body") || ""),
      column: String(form.get("column") || ""),
      priority: String(form.get("priority") || ""),
      labels: splitLabels(String(form.get("labels") || "")),
    };
    if (number > 0 || repo) {
      payload.issue = { repo: repo, number: number > 0 ? number : 0, url: "" };
    }
    try {
      await api("/api/cards", { method: "POST", body: payload });
      closeModal(els.newModal);
      toast("Card added", "success");
      await loadBoard();
    } catch (err) {
      toast("Add failed: " + err.message, "error");
    }
  });

  els.syncForm.addEventListener("submit", async function (event) {
    event.preventDefault();
    if (els.syncSubmit.disabled) return;
    const form = new FormData(els.syncForm);
    const provider = String(form.get("provider") || "all").trim() || "all";
    const payload = {
      provider: provider,
      labels: splitLabels(String(form.get("labels") || "")),
      state: String(form.get("state") || "").trim(),
      limit: parseInt(String(form.get("limit") || "50"), 10) || 50,
      query: String(form.get("query") || "").trim(),
    };
    els.syncResult.textContent = "Syncing…";
    try {
      const result = await api("/api/sync", { method: "POST", body: payload });
      if (provider === "all") {
        const results = result.results || {};
        const errors = result.errors || {};
        const parts = [];
        Object.keys(results).forEach(function (id) {
          const stats = results[id];
          parts.push(id + ": imported " + stats.imported + ", updated " + stats.updated + " of " + stats.total);
        });
        Object.keys(errors).forEach(function (id) {
          parts.push(id + ": " + errors[id]);
        });
        els.syncResult.textContent = parts.length ? parts.join("\\n") : "No sync providers are enabled and available.";
      } else {
        els.syncResult.textContent =
          "Imported " + result.imported + ", updated " + result.updated + " of " + result.total + " issues.";
      }
      toast("Sync complete", "success");
      await loadBoard();
    } catch (err) {
      els.syncResult.textContent = "Sync failed: " + err.message;
      toast("Sync failed: " + err.message, "error");
    }
  });

  async function boot() {
    await loadConfig();
    await loadBoard();
    populateColumnSelects();
    connectSSE();
  }

  boot();
})();
