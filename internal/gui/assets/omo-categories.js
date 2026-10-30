const OMO_REASONING_PRESETS = ["off", "minimal", "low", "medium", "high", "xhigh", "max", "auto"];
const OMO_NAMED_AGENTS = ["explore", "librarian", "plan-consultant", "plan-reviewer"];
window.PREFS_GLYPH = "M2.75 4.75h10.5M2.75 11.25h10.5M5.75 3.25v3M10.25 9.75v3";
const localOmo = (a) => !gatewayMode && a?.id === "omo";
window.omoCategoriesMenu = () => [];
window.omoCategoriesEntry = () => {};
const omoCategoryEditors = new Map();
window.omoCategoriesDetails = (a, kv) => {
  if (!localOmo(a)) return;
  if (!omoCategoryEditors.has(a.id)) omoCategoryEditors.set(a.id, createOmoCategoriesEditor(a));
  omoCategoryEditors.get(a.id).mount(kv(t("OmO settings")));
};

// Ask the local OmO categories API, keeping the status and payload of a non-2xx reply (missing, conflict)
async function requestOmoCategories(body) {
  const res = await fetch("/api/agents/omo/categories", {
    method: body ? "POST" : "GET",
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
  return { ok: res.ok, httpStatus: res.status, data };
}

function cloneOmoCategoriesDraft(categories) {
  return (categories || []).map((c) => {
    const r = typeof c.reasoning === "string" ? c.reasoning : "";
    return {
      name: c.name,
      ...(c.scope === "agents" ? { scope: "agents" } : {}),
      status: c.status || "ready",
      error: c.error || "",
      reasoning: r,
      customReasoning: !!r && !OMO_REASONING_PRESETS.includes(r),
      models: (c.models || []).map((m) => ({
        model: String(m.model || ""),
        index: typeof m.index === "number" ? m.index : undefined,
        object: !!m.object,
      })),
    };
  });
}

function diffOmoCategories(baseCats, draftCats) {
  const changes = [];
  let invalid = false;
  for (let i = 0; i < draftCats.length; i++) {
    const orig = baseCats[i], cur = draftCats[i];
    if (!orig || !cur || orig.status === "error") continue;
    const change = { name: cur.name, ...(orig.scope === "agents" ? { scope: "agents" } : {}) };
    if (orig.status === "ready") {
      const origModels = orig.models || [];
      const curModels = cur.models || [];
      const modelsDiff = origModels.length !== curModels.length || curModels.some((m, j) => {
        const om = origModels[j];
        return !om || om.index !== m.index || om.model !== m.model.trim();
      });
      if (modelsDiff) {
        if (!curModels.length || curModels.some((m) => !m.model.trim())) invalid = true;
        change.models = curModels.map((m) => {
          const item = { model: m.model.trim() };
          if (typeof m.index === "number") item.index = m.index;
          return item;
        });
      }
    }
    const origR = (orig.reasoning || "").trim();
    const curR = (cur.reasoning || "").trim();
    if (cur.customReasoning && !curR) invalid = true;
    if (origR !== curR || (cur.customReasoning && !curR)) {
      change.reasoning = { value: curR === "" ? null : curR };
    }
    if (change.models || change.reasoning) changes.push(change);
  }
  return { changes, invalid };
}

// Drafts and request state live outside the DOM; an agent-list redraw only re-binds the current container
function createOmoCategoriesEditor(a) {
  let base = { path: "~/.omo/omo.jsonc", revision: "", status: "ready", error: "", categories: [], agents: [] };
  let draft = [];
  let saveError = "";
  let loading = true;
  let saving = false;
  let started = false;
  const expanded = new Set();
  let body, reloadHeadBtn, cancelBtn, saveBtn;

  // A missing subagent gets an editable empty row: no model is picked for the user and no default is written.
  const baseRows = () => [...base.categories, ...(base.status === "ready" ? OMO_NAMED_AGENTS.map((name) => ({
    name, status: "ready", models: [],
    ...base.agents.find((row) => row.name === name), scope: "agents",
  })) : [])];

  const currentAgent = () => state.agents?.find((x) => x.id === "omo") || a;
  const modelCatalogOptions = () => {
    const ag = currentAgent();
    const mf = ag?.fields?.find((f) => f.key === "model" || f.label === "model");
    return (mf?.options || []).filter((o) => o.value && !o.reset && !o.run);
  };
  const modelDisplayLabel = (modelId) => {
    const opt = modelCatalogOptions().find((o) => o.value === modelId);
    return opt?.label || modelId;
  };

  const syncButtons = () => {
    const { changes, invalid } = diffOmoCategories(baseRows(), draft);
    const editable = base.status === "ready" && baseRows().some((c) => c.status === "ready" || c.status === "legacy");
    saveBtn.disabled = loading || saving || !editable || invalid || changes.length === 0;
    reloadHeadBtn.disabled = loading || saving;
    cancelBtn.disabled = saving;
    if (loading || saving) {
      for (const ctrl of body.querySelectorAll("button, select, input")) {
        ctrl.disabled = true;
      }
    }
  };

  const isDirty = () => {
    const { changes } = diffOmoCategories(baseRows(), draft);
    return changes.length > 0;
  };

  const loadFromServer = async () => {
    loading = true;
    saveError = "";
    renderBody();
    try {
      const { ok, data } = await requestOmoCategories();
      base = {
        path: data?.path || "~/.omo/omo.jsonc",
        revision: data?.revision || "",
        status: data?.status || (ok ? "ready" : "error"),
        error: data?.error || "",
        categories: Array.isArray(data?.categories) ? data.categories : [],
        agents: Array.isArray(data?.agents) ? data.agents : [],
      };
      draft = cloneOmoCategoriesDraft(baseRows());
    } catch (e) {
      base = { path: "~/.omo/omo.jsonc", revision: "", status: "error", error: e.message || String(e), categories: [], agents: [] };
      draft = [];
    } finally {
      loading = false;
      if (body.isConnected) renderBody();
    }
  };

  const triggerReload = async () => {
    if (loading || saving) return;
    closePicker();
    if (isDirty()) {
      const ok = await confirmAction(t("Reload"), t("Discard unsaved OmO changes?"), t("Reload"));
      if (!ok) return;
    }
    await loadFromServer();
  };

  const cancel = () => {
    if (saving) return;
    closePicker();
    draft = cloneOmoCategoriesDraft(baseRows());
    saveError = "";
    renderBody();
  };

  const save = async () => {
    const { changes, invalid } = diffOmoCategories(baseRows(), draft);
    if (loading || saving || invalid || !changes.length) return;
    closePicker();
    saving = true;
    saveError = "";
    syncButtons();
    try {
      const { ok, data } = await requestOmoCategories({ revision: base.revision, changes });
      if (!ok) {
        saveError = data?.error || String(data?.status || "error");
        saving = false;
        if (body.isConnected) renderBody();
        return;
      }
      base = {
        path: data?.path || base.path,
        revision: data?.revision || "",
        status: data?.status || "ready",
        error: data?.error || "",
        categories: Array.isArray(data?.categories) ? data.categories : [],
        agents: Array.isArray(data?.agents) ? data.agents : [],
      };
      draft = cloneOmoCategoriesDraft(baseRows());
      saving = false;
      status(t("OmO settings saved — restart OmO to apply"));
      if (body.isConnected) renderBody();
    } catch (e) {
      saveError = e.message || String(e);
      saving = false;
      if (body.isConnected) renderBody();
    }
  };

  function renderBody() {
    body.replaceChildren();
    const meta = el("div", "omo-cat-meta");
    const pathEl = el("div", "omo-cat-path", base.path || "~/.omo/omo.jsonc");
    const noteEl = el("div", "omo-cat-scope", t("Edits {path}. Restart OmO after saving; project or profile configs may override global settings.", { path: base.path || "~/.omo/omo.jsonc" }));
    meta.append(pathEl, noteEl);
    const bar = el("div", "omo-cat-actions ag-vl");
    reloadHeadBtn = el("button", "ag-quiet omo-cat-reload-head", t("Reload"));
    cancelBtn = el("button", "ag-quiet omo-cat-cancel", t("Cancel"));
    saveBtn = el("button", "ag-quiet accent omo-cat-save", t("Save"));
    for (const b of [reloadHeadBtn, cancelBtn, saveBtn]) b.type = "button";
    reloadHeadBtn.onclick = () => triggerReload();
    cancelBtn.onclick = cancel;
    saveBtn.onclick = save;
    bar.append(reloadHeadBtn, el("span", "grow"), cancelBtn, saveBtn);
    body.append(meta, bar);

    if (saveError || (base.status === "error" && base.error)) {
      const banner = el("div", "omo-cat-banner");
      banner.setAttribute("role", "alert");
      const msg = el("span", "", `${t(saveError ? "Save" : "OmO settings")} · ${t("Error")}: ${saveError || base.error}`);
      const reloadBtn = el("button", "text omo-cat-reload", t("Reload"));
      reloadBtn.type = "button";
      reloadBtn.onclick = () => triggerReload();
      banner.append(msg, reloadBtn);
      body.append(banner);
    }

    if (loading) {
      body.append(el("div", "omo-cat-empty", t("Loading…")));
      syncButtons();
      return;
    }

    if (base.status === "missing") {
      const empty = el("div", "omo-cat-missing");
      empty.append(el("div", "omo-cat-note", t("Configuration file not found at {path}.", { path: base.path || "~/.omo/omo.jsonc" })));
      body.append(empty);
      syncButtons();
      return;
    }

    if (!draft.length) {
      if (base.status !== "error") {
        const empty = el("div", "omo-cat-empty-card");
        empty.append(el("div", "omo-cat-empty", t("No categories in this configuration.")));
        body.append(empty);
      }
      syncButtons();
      return;
    }

    const list = el("div", "omo-cat-list");
    const sections = new Map();
    for (const [scope, label] of [["agents", "Subagents"], ["categories", "Categories"]]) {
      const section = el("section", "omo-cat-section");
      section.dataset.scope = scope;
      section.setAttribute("aria-label", t(label));
      section.append(el("div", "omo-cat-section-label", t(label)));
      if (scope === "categories" && !base.categories.length) section.append(el("div", "omo-cat-empty", t("No categories in this configuration.")));
      sections.set(scope, section);
      list.append(section);
    }
    for (const cat of draft) {
      const card = el("div", "omo-cat-category");
      const scope = cat.scope || "categories";
      const rowKey = `${scope}:${cat.name}`;
      card.dataset.scope = scope;
      if (scope === "agents") card.dataset.agent = cat.name;
      else card.dataset.category = cat.name;
      card.dataset.status = cat.status;

      const cHead = el("div", "ag-vl omo-cat-head");
      const cTitle = el("span", "ag-tier", cat.name);
      cTitle.title = cat.name;
      const tools = cHead;

      const reasonDisabled = saving || cat.status === "error";
      const reasonWrap = el("div", "omo-cat-reason");
      const reasonPick = el("button", "field ag-eff omo-cat-reason-pick", cat.customReasoning ? t("Custom…") : t(cat.reasoning || "Not set"));
      reasonPick.type = "button";
      reasonPick.setAttribute("aria-label", `${cat.name} ${t("Reasoning")}`);
      reasonPick.disabled = reasonDisabled;
      reasonPick.prepend(effortIcon({ value: cat.reasoning, options: OMO_REASONING_PRESETS.map((value) => ({ value })) }));
      reasonPick.append(svg(CHEV, 10, 1.6));

      const customInput = el("input", "omo-cat-reason-custom");
      customInput.type = "text";
      customInput.placeholder = t("Custom reasoning effort");
      customInput.setAttribute("aria-label", `${cat.name} ${t("Custom reasoning effort")}`);
      customInput.value = cat.customReasoning ? cat.reasoning : "";
      customInput.hidden = !cat.customReasoning;
      customInput.disabled = reasonDisabled;

      reasonPick.onclick = (ev) => {
        if (saving || loading || cat.status === "error") return;
        openPicker(currentAgent(), {
          key: "omo-category-reasoning", label: "Reasoning", menu: true,
          value: cat.customReasoning ? "__custom__" : cat.reasoning,
          options: [{ value: "", label: t("Not set") }, ...OMO_REASONING_PRESETS.map((value) => ({ value, label: t(value) })), { value: "__custom__", label: t("Custom…") }],
          onPick: (val) => {
            if (saving || loading || cat.status === "error") return;
            cat.customReasoning = val === "__custom__";
            if (!cat.customReasoning) cat.reasoning = val;
            else if (OMO_REASONING_PRESETS.includes(cat.reasoning)) cat.reasoning = "";
            renderBody();
            if (cat.customReasoning) body.querySelectorAll(".omo-cat-reason-custom")[draft.indexOf(cat)]?.focus();
          },
        }, reasonPick, ev);
      };
      customInput.oninput = () => {
        if (saving || loading || cat.status === "error") return;
        cat.reasoning = customInput.value;
        syncButtons();
      };

      reasonWrap.append(reasonPick, customInput);
      cHead.append(cTitle, reasonWrap);
      card.append(cHead);

      if (cat.status === "legacy") {
        card.append(el("div", "omo-cat-note", t("Legacy model field: reasoning can be edited, models list is read-only")));
      } else if (cat.status === "error") {
        card.append(el("div", "omo-cat-note err", `${t("Read-only: unsupported models format")}${cat.error ? `: ${cat.error}` : ""}`));
      }

      const modelsBox = el("div", "omo-cat-models");
      const modelsEditable = !saving && cat.status === "ready";
      if (!cat.models.length) {
        const emptyPick = el("button", "field ag-pick omo-cat-model-pick", t(cat.status === "ready" ? "Not set" : "Read-only: unsupported models format"));
        emptyPick.type = "button";
        emptyPick.disabled = !modelsEditable || scope !== "agents";
        emptyPick.setAttribute("aria-label", `${cat.name} ${t("model")}`);
        emptyPick.onclick = (ev) => {
          if (saving || loading || cat.status !== "ready" || scope !== "agents") return;
          openPicker(currentAgent(), {
            key: "omo-agent-model", label: "model", value: "",
            options: modelCatalogOptions().map((o) => ({ ...o })),
            onPick: (val) => {
              if (saving || loading || cat.status !== "ready") return;
              const v = (val || "").trim();
              if (!v) return;
              cat.models.push({ model: v, object: false });
              renderBody();
            },
          }, emptyPick, ev);
        };
        tools.insertBefore(emptyPick, reasonWrap);
        modelsBox.append(el("div", "omo-cat-empty", t("No models")));
      } else {
        cat.models.forEach((m, idx) => {
          const row = el("div", "omo-cat-model-row");
          row.dataset.index = String(idx);
          const pickBtn = el("button", "field ag-pick omo-cat-model-pick");
          pickBtn.type = "button";
          pickBtn.disabled = !modelsEditable;
          pickBtn.title = m.model;
          pickBtn.setAttribute("aria-label", `${cat.name} ${m.model}`);
          const option = modelCatalogOptions().find((o) => o.value === m.model);
          if (option) pickBtn.append(optionIcon(option));
          pickBtn.append(el("span", "v", modelDisplayLabel(m.model)));
          if (modelsEditable) {
            const c = el("span", "chev");
            c.append(svg(CHEV, 10, 1.6));
            pickBtn.append(c);
            pickBtn.onclick = (ev) => {
              if (saving || loading || cat.status !== "ready") return;
              const ag = currentAgent();
              openPicker(ag, {
                key: "omo-category-model",
                label: "model",
                value: m.model,
                options: modelCatalogOptions().map((o) => ({ ...o })),
                onPick: (val) => {
                  if (saving || loading || cat.status !== "ready") return;
                  const v = (val || "").trim();
                  if (!v) return;
                  m.model = v;
                  renderBody();
                },
              }, pickBtn, ev);
            };
          }
          if (idx === 0) {
            tools.insertBefore(pickBtn, reasonWrap);
            const identity = el("span", "omo-cat-model-primary", modelDisplayLabel(m.model));
            identity.title = m.model;
            row.append(identity);
          } else row.append(pickBtn);
          if (m.object) {
            const badge = el("span", "badge omo-cat-obj-badge", t("Object entry"));
            row.append(badge);
          }
          if (modelsEditable) {
            const acts = el("span", "omo-cat-model-acts");
            const upBtn = el("button", "icon omo-cat-up", "↑");
            upBtn.type = "button";
            upBtn.title = t("Move up");
            upBtn.setAttribute("aria-label", `${cat.name} ${m.model} ${t("Move up")}`);
            upBtn.disabled = idx === 0;
            upBtn.onclick = () => {
              if (saving || loading || cat.status !== "ready" || idx <= 0) return;
              const [item] = cat.models.splice(idx, 1);
              cat.models.splice(idx - 1, 0, item);
              renderBody();
            };

            const downBtn = el("button", "icon omo-cat-down", "↓");
            downBtn.type = "button";
            downBtn.title = t("Move down");
            downBtn.setAttribute("aria-label", `${cat.name} ${m.model} ${t("Move down")}`);
            downBtn.disabled = idx >= cat.models.length - 1;
            downBtn.onclick = () => {
              if (saving || loading || cat.status !== "ready" || idx >= cat.models.length - 1) return;
              const [item] = cat.models.splice(idx, 1);
              cat.models.splice(idx + 1, 0, item);
              renderBody();
            };

            const rmBtn = el("button", "icon omo-cat-remove", "×");
            rmBtn.type = "button";
            rmBtn.title = t("Remove");
            rmBtn.setAttribute("aria-label", `${cat.name} ${m.model} ${t("Remove")}`);
            rmBtn.disabled = cat.models.length <= 1;
            rmBtn.onclick = () => {
              if (saving || loading || cat.status !== "ready" || cat.models.length <= 1) return;
              cat.models.splice(idx, 1);
              renderBody();
            };

            acts.append(upBtn, downBtn, rmBtn);
            row.append(acts);
          }
          modelsBox.append(row);
        });
      }
      const disclosure = el("button", "text omo-cat-disclosure", t("{n} more models", { n: Math.max(0, cat.models.length - 1) }));
      disclosure.type = "button";
      disclosure.setAttribute("aria-label", `${cat.name}: ${disclosure.textContent}`);
      disclosure.setAttribute("aria-expanded", String(expanded.has(rowKey)));
      modelsBox.hidden = !expanded.has(rowKey);
      disclosure.onclick = () => {
        if (saving || loading) return;
        if (expanded.has(rowKey)) expanded.delete(rowKey);
        else expanded.add(rowKey);
        modelsBox.hidden = !expanded.has(rowKey);
        disclosure.setAttribute("aria-expanded", String(expanded.has(rowKey)));
      };
      card.append(disclosure, modelsBox);

      if (modelsEditable) {
        const foot = el("div", "omo-cat-foot");
        const addBtn = el("button", "text omo-cat-add", `+ ${t("Add model")}`);
        addBtn.type = "button";
        addBtn.onclick = (ev) => {
          if (saving || loading || cat.status !== "ready") return;
          const ag = currentAgent();
          openPicker(ag, {
            key: "omo-category-add-model",
            label: "model",
            value: "",
            options: modelCatalogOptions().map((o) => ({ ...o })),
            onPick: (val) => {
              if (saving || loading || cat.status !== "ready") return;
              const v = (val || "").trim();
              if (!v) return;
              cat.models.push({ model: v, object: false });
              renderBody();
            },
          }, addBtn, ev);
        };
        foot.append(addBtn);
        modelsBox.append(foot);
      }

      sections.get(scope).append(card);
    }
    body.prepend(list);
    syncButtons();
  }

  return { mount(container) {
    body = container;
    body.classList.add("omo-categories");
    renderBody();
    if (!started) {
      started = true;
      loadFromServer();
    }
  } };
}
