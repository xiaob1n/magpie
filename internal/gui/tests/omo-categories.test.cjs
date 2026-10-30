// Isolated HTTP fixtures check the local OmO category editor; the real user config is never read.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const endpoint = "/api/agents/omo/categories";
const namedAgents = ["explore", "librarian", "plan-consultant", "plan-reviewer"];
const options = ["vendor/first", "vendor/second", "vendor/third"].map((value) => ({ value, label: value, ref: value }));
const agent = (id) => ({ id, name: "OmO", icon: "opencode", path: "/fixture/.omo/omo.jsonc", wired: true, fields: [{ key: "model", label: "model", value: options[0].value, options }, { key: "effort", label: "effort", value: "high", options: ["low", "medium", "high"].map((value) => ({ value })) }] });
const fresh = () => ({
  path: "/fixture/.omo/omo.jsonc", revision: "revision-1", status: "ready",
  agents: namedAgents.map((name) => ({ name, status: "ready", models: [] })),
  categories: [
    { name: "quick", status: "ready", models: [{ model: "vendor/first", index: 0, object: false }, { model: "vendor/second", index: 1, object: true }], reasoning: "low" },
    { name: "legacy", status: "legacy", models: [{ model: "vendor/first", index: 0, object: false }], reasoning: "vendor-effort" },
    { name: "broken", status: "error", error: "unsupported models format", models: [] },
  ],
});
const editor = (page) => page.locator('.row.agent[data-id="omo"] .omo-categories');
const card = (page, name = "quick") => editor(page).locator(`[data-category="${name}"]`);
const namedCard = (page, name = "explore") => editor(page).locator(`[data-scope="agents"][data-agent="${name}"]`);
const save = (page) => editor(page).locator(".omo-cat-save");
const cancel = (page) => editor(page).locator(".omo-cat-cancel");
const modelValues = (page) => card(page).locator(".omo-cat-model-pick > .v").allTextContents();
const press = (locator) => locator.press("Enter");
const reasoning = (page, name = "quick") => card(page, name).locator(".omo-cat-reason-pick");
const translated = (page, value) => page.evaluate((value) => t(value), value);
async function reason(page, value, name = "quick", scope = "categories") {
  await press(scope === "agents" ? namedCard(page, name).locator(".omo-cat-reason-pick") : reasoning(page, name));
  const index = await page.evaluate((value) => pick.options.findIndex((o) => o.value === value), value);
  assert(index >= 0, `reasoning option present: ${value}`);
  await page.locator(`#pop #list li[data-i="${index}"]`).click();
  await page.locator("#pop").waitFor({ state: "hidden" });
}

function fixture(lang, theme = "light") {
  const state = { current: fresh(), posts: [], gets: 0, conflict: false, readStatus: 200 };
  state.route = async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ json: data, status });
    if (url.pathname === endpoint) {
      assert.equal(url.search, "", "the categories API takes no client path or WSL parameter");
      if (req.method() === "GET") {
        state.gets++;
        return json(state.current, state.readStatus);
      }
      assert.equal(req.method(), "POST");
      const body = req.postDataJSON();
      state.posts.push(body);
      if (state.pendingPost) await state.pendingPost;
      if (state.conflict) return json({ ...state.current, error: "Omo categories changed; reload before saving" }, 409);
      if (state.saveFailure) return json({ error: "fixture save failed" }, 500);
      // Mimic the server reading again: original indexes are renumbered and the object flag follows the original index.
      for (const change of body.changes) {
        const category = state.current[change.scope === "agents" ? "agents" : "categories"].find((c) => c.name === change.name);
        if (change.models) {
          const old = category.models;
          category.models = change.models.map((m, index) => ({ model: m.model, index, object: m.index === undefined ? false : old.find((o) => o.index === m.index).object }));
        }
        if (change.reasoning) {
          if (change.reasoning.value === null) delete category.reasoning;
          else category.reasoning = change.reasoning.value;
        }
      }
      if (state.canonicalize) state.canonicalize(state.current);
      state.current.revision = `revision-${state.posts.length + 1}`;
      return json(state.current);
    }
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:${JSON.stringify(theme)},web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [agent("omo"), agent("omo@wsl:Ubuntu"), { ...agent("claude"), name: "Claude Code", icon: "claude", path: "/fixture/.claude/settings.json" }], profiles: [], settings: { lang, theme } });
    if (url.pathname === "/api/settings") return json({ lang, theme });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], gateway: { running: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try {
      return await route.fulfill({ body: await fs.readFile(file), contentType });
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
      return route.fulfill({ status: 404, body: "" });
    }
  };
  return state;
}

async function setup(t, engine, lang) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce" });
  page.setDefaultTimeout(6000);
  const errors = [], api = fixture(lang);
  page.on("pageerror", (error) => errors.push(error.message));
  t.after(() => assert.deepEqual(errors, [], "the page threw no uncaught error"));
  await page.route("**/*", api.route);
  await page.goto("http://magpie.test/?view=agents");
  return { page, api };
}

async function open(page) {
  // Subscribe to the exact reply before triggering the read; a DOM wait only covers that editor's visible state.
  if (!await page.locator('.row.agent[data-id="omo"].expanded').count()) {
    await press(page.locator('.row.agent[data-id="omo"] .ag-link'));
  }
  await card(page).waitFor();
}

async function pick(page, button, value, custom = false) {
  await press(button);
  await page.locator("#pop:not([hidden]) #q").fill(value);
  await press(page.locator(custom ? "#pop #list li.custom" : "#pop #list li:not(.custom)").filter({ hasText: value }).first());
  await page.locator("#pop").waitFor({ state: "hidden" });
}

async function commit(page) {
  const response = page.waitForResponse((r) => new URL(r.url()).pathname === endpoint && r.request().method() === "POST");
  await press(save(page));
  const result = await response;
  await page.waitForFunction(() => document.querySelector('.row.agent[data-id="omo"] .omo-cat-save')?.disabled && !document.querySelector('.row.agent[data-id="omo"] .omo-cat-cancel')?.disabled);
  assert.equal(result.status(), 200);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: named defaults create only explicitly selected agent models`, { timeout: 60000 }, async (t) => {
      const { page, api } = await setup(t, engine, lang);
      await open(page);
      for (const name of namedAgents) {
        assert.equal(await namedCard(page, name).locator('.omo-cat-model-pick').textContent(), await translated(page, 'Not set'));
        assert.equal(await namedCard(page, name).locator('.omo-cat-reason-pick').isDisabled(), false);
      }
      assert.equal(await save(page).isDisabled(), true);
      await pick(page, namedCard(page).locator('.omo-cat-model-pick'), 'vendor/third');
      await reason(page, 'high', 'explore', 'agents');
      await commit(page);
      assert.deepEqual(api.posts, [{ revision: 'revision-1', changes: [{ name: 'explore', scope: 'agents', models: [{ model: 'vendor/third' }], reasoning: { value: 'high' } }] }]);
      assert.deepEqual(api.current.agents.slice(1).map((a) => a.models), [[], [], []]);
      await pick(page, namedCard(page).locator('.omo-cat-model-pick'), 'vendor/second');
      await press(cancel(page));
      assert.equal(await namedCard(page).locator('.omo-cat-model-pick > .v').textContent(), 'vendor/third');
      assert.equal(api.posts.length, 1);
    });

    test(`${engine} ${lang}: all configured named agents edit models and reasoning`, { timeout: 60000 }, async (t) => {
      const { page, api } = await setup(t, engine, lang);
      api.current.agents = namedAgents.map((name, index) => ({ name, status: 'ready', models: [{ model: options[index % 3].value, index: 0, object: index === 2 }], reasoning: ['low', 'medium', 'high', 'max'][index] }));
      await open(page);
      const expected = [];
      for (const [index, name] of namedAgents.entries()) {
        assert.equal(await namedCard(page, name).locator('.omo-cat-model-pick > .v').textContent(), options[index % 3].value);
        assert.equal(await namedCard(page, name).locator('.omo-cat-reason-pick').textContent(), await translated(page, ['low', 'medium', 'high', 'max'][index]));
        await pick(page, namedCard(page, name).locator('.omo-cat-model-pick'), `custom/${name}`, true);
        await reason(page, 'xhigh', name, 'agents');
        expected.push({ name, scope: 'agents', models: [{ model: `custom/${name}`, index: 0 }], reasoning: { value: 'xhigh' } });
      }
      await commit(page);
      assert.deepEqual(api.posts, [{ revision: 'revision-1', changes: expected }]);
      for (const name of namedAgents) assert.equal(await namedCard(page, name).locator('.omo-cat-model-pick > .v').textContent(), `custom/${name}`);
      assert.equal(api.current.agents[2].models[0].object, true);
    });

    test(`${engine} ${lang}: joint category agent save adopts canonical reread revision`, { timeout: 60000 }, async (t) => {
      const { page, api } = await setup(t, engine, lang);
      await open(page);
      await reason(page, 'high');
      await reason(page, 'max', 'plan-reviewer', 'agents');
      api.canonicalize = (current) => { current.agents[3].reasoning = 'medium'; };
      await commit(page);
      assert.deepEqual(api.posts, [{ revision: 'revision-1', changes: [{ name: 'quick', reasoning: { value: 'high' } }, { name: 'plan-reviewer', scope: 'agents', reasoning: { value: 'max' } }] }]);
      assert.equal(await namedCard(page, 'plan-reviewer').locator('.omo-cat-reason-pick').textContent(), await translated(page, 'medium'));
      await reason(page, 'low', 'plan-reviewer', 'agents');
      await press(cancel(page));
      assert.equal(await namedCard(page, 'plan-reviewer').locator('.omo-cat-reason-pick').textContent(), await translated(page, 'medium'));
      api.canonicalize = undefined;
      await reason(page, '', 'plan-reviewer', 'agents');
      await commit(page);
      assert.deepEqual(api.posts.at(-1), { revision: 'revision-2', changes: [{ name: 'plan-reviewer', scope: 'agents', reasoning: { value: null } }] });
    });

    test(`${engine} ${lang}: malformed named rows remain read only beside valid agents`, { timeout: 60000 }, async (t) => {
      const { page, api } = await setup(t, engine, lang);
      api.current.agents[0] = { name: 'explore', status: 'error', error: 'unsupported models format', models: [] };
      api.current.agents[1] = { name: 'librarian', status: 'legacy', models: [{ model: 'vendor/first', index: 0, object: false }], reasoning: 'vendor-effort' };
      await open(page);
      assert.equal(await namedCard(page).locator('.omo-cat-model-pick').isDisabled(), true);
      assert.equal(await namedCard(page).locator('.omo-cat-reason-pick').isDisabled(), true);
      assert.match(await namedCard(page).textContent(), /unsupported models format/);
      assert.equal(await namedCard(page, 'librarian').locator('.omo-cat-model-pick').isDisabled(), true);
      await reason(page, 'high', 'librarian', 'agents');
      await reason(page, 'max', 'plan-consultant', 'agents');
      await commit(page);
      assert.deepEqual(api.posts[0].changes, [{ name: 'librarian', scope: 'agents', reasoning: { value: 'high' } }, { name: 'plan-consultant', scope: 'agents', reasoning: { value: 'max' } }]);
    });

    test(`${engine} ${lang}: local categories stage model edits and reread saved order`, { timeout: 60000 }, async (t) => {
      const { page, api } = await setup(t, engine, lang);
      // The local row has an entry, WSL has none, and no category file is read before it opens.
      assert.equal(api.gets, 0);
      await press(page.locator('.row.agent[data-id="omo@wsl:Ubuntu"] .ag-link'));
      assert.equal(await page.locator('.row.agent[data-id="omo@wsl:Ubuntu"] :is(.omo-categories, .omo-categories-btn, .omo-categories-open)').count(), 0);
      assert.equal(api.gets, 0);
      const initialRead = page.waitForResponse((r) => new URL(r.url()).pathname === endpoint && r.request().method() === 'GET');
      await open(page);
      await initialRead;
      assert.equal(api.gets, 1);
      assert.equal(await page.locator('.row.agent[data-id="omo"] :is(.omo-categories-btn, .omo-categories-open)').count(), 0);
      assert.equal(await page.locator('#modal .omo-categories').count(), 0);
      assert.equal(await editor(page).locator('.omo-cat-list').count(), 1);
      assert.equal(await editor(page).evaluate((el) => {
        const kv = el.closest('.ag-kv');
        return kv.previousElementSibling.querySelector('.ag-k').textContent;
      }), lang === 'zh' ? '新会话默认' : 'New sessions');
      assert.equal(await editor(page).locator(".omo-cat-path").textContent(), fresh().path);
      assert.equal(await save(page).isDisabled(), true);
      assert.equal(await card(page).locator('.omo-cat-disclosure').getAttribute('aria-expanded'), 'false');
      assert.equal(await card(page).locator('.omo-cat-head .omo-cat-model-pick').isVisible(), true);
      assert.equal(await reasoning(page).isVisible(), true);
      assert.equal(await card(page).locator('.omo-cat-models').isVisible(), false);
      assert.match(await card(page).locator('.omo-cat-disclosure').textContent(), /1/);
      await press(card(page).locator('.omo-cat-disclosure'));
      await press(card(page).locator(".omo-cat-up").nth(1));
      assert.deepEqual(await modelValues(page), ["vendor/second", "vendor/first"]);
      await press(card(page).locator(".omo-cat-down").first());
      await press(card(page).locator(".omo-cat-up").nth(1));
      await pick(page, card(page).locator(".omo-cat-model-pick").first(), "custom/replaced", true);
      await reason(page, "high");
      await pick(page, card(page).locator(".omo-cat-add"), "vendor/third");
      await pick(page, card(page).locator(".omo-cat-add"), "custom/new", true);
      assert.match(await card(page).locator('.omo-cat-disclosure').textContent(), /3/);
      assert.equal(await card(page).locator('.omo-cat-disclosure').getAttribute('aria-expanded'), 'true');
      await press(card(page).locator(".omo-cat-remove").nth(1));
      assert.equal(await card(page).locator(".omo-cat-obj-badge").count(), 1);
      assert.equal(api.posts.length, 0);
      await commit(page);
      assert.deepEqual(api.posts, [{ revision: "revision-1", changes: [{ name: "quick", models: [{ model: "custom/replaced", index: 1 }, { model: "vendor/third" }, { model: "custom/new" }], reasoning: { value: "high" } }] }]);
      await open(page);
      if (await card(page).locator('.omo-cat-disclosure').getAttribute('aria-expanded') === 'false') await press(card(page).locator('.omo-cat-disclosure'));
      assert.deepEqual(await modelValues(page), ["custom/replaced", "vendor/third", "custom/new"]);
      assert.equal(await card(page).locator(".omo-cat-obj-badge").count(), 1);
      await reason(page, "max");
      await commit(page);
      assert.deepEqual(api.posts.at(-1), { revision: "revision-2", changes: [{ name: "quick", reasoning: { value: "max" } }] });
    });

    test(`${engine} ${lang}: reasoning presets custom unset and read-only categories`, { timeout: 60000 }, async (t) => {
      const { page, api } = await setup(t, engine, lang);
      await open(page);
      assert.equal(await card(page, "legacy").locator(".omo-cat-model-pick").isDisabled(), true);
      assert.equal(await card(page, "legacy").locator(".omo-cat-add, .omo-cat-remove").count(), 0);
      assert.equal(await card(page, "legacy").locator("input").isVisible(), true);
      assert.equal(await card(page, "legacy").locator("input").inputValue(), "vendor-effort");
      assert.equal(await reasoning(page, "broken").isDisabled(), true);
      assert.equal(await editor(page).locator('select').count(), 0);
      await press(reasoning(page));
      assert.deepEqual(await page.evaluate(() => pick.options.map((o) => o.value).sort()), ["", "off", "minimal", "low", "medium", "high", "xhigh", "max", "auto", "__custom__"].sort());
      await page.keyboard.press('Escape');
      for (const preset of ["off", "minimal", "low", "medium", "high", "xhigh", "max", "auto"]) {
        await reason(page, preset);
        assert.equal(await reasoning(page).textContent(), await translated(page, preset));
      }
      await reason(page, "__custom__");
      assert.equal(await save(page).isDisabled(), true);
      await card(page).locator("input").fill("  experimental  ");
      await reason(page, "max", "legacy");
      await commit(page);
      assert.deepEqual(api.posts.at(-1).changes, [{ name: "quick", reasoning: { value: "experimental" } }, { name: "legacy", reasoning: { value: "max" } }]);
      await open(page);
      assert.equal(await card(page).locator("input").inputValue(), "experimental");
      await reason(page, "");
      await commit(page);
      assert.deepEqual(api.posts.at(-1), { revision: "revision-2", changes: [{ name: "quick", reasoning: { value: null } }] });
      await open(page);
      assert.equal(await card(page).locator("input").isVisible(), false);
      await press(cancel(page));
      api.current.categories[0].models = [api.current.categories[0].models[0]];
      const reread = page.waitForResponse((r) => new URL(r.url()).pathname === endpoint && r.request().method() === 'GET');
      await press(editor(page).locator('.omo-cat-reload-head'));
      await reread;
      await card(page).waitFor();
      assert.match(await card(page).locator('.omo-cat-disclosure').textContent(), /0/);
      await press(card(page).locator('.omo-cat-disclosure'));
      assert.equal(await card(page).locator('.omo-cat-add').isVisible(), true);
      assert.equal(await card(page).locator('.omo-cat-remove').isDisabled(), true);
      await press(cancel(page));
      api.current = { path: fresh().path, status: "error", error: "malformed JSON", categories: [] };
      api.readStatus = 500;
      const response = page.waitForResponse((r) => new URL(r.url()).pathname === endpoint);
      await press(editor(page).locator('.omo-cat-reload-head'));
      await response;
      await editor(page).locator('[role="alert"]').waitFor();
      assert.equal(await save(page).isDisabled(), true);
      assert.equal(await editor(page).locator(".omo-cat-model-pick, select").count(), 0);
    });

    test(`${engine} ${lang}: pending POST freezes every draft control`, { timeout: 60000 }, async (t) => {
      const { page, api } = await setup(t, engine, lang);
      await open(page);
      await reason(page, "__custom__");
      await card(page).locator("input").fill("pending-effort");
      await reason(page, 'max', 'plan-consultant', 'agents');
      const reads = api.gets;
      await page.evaluate(() => renderAgents());
      assert.equal(await card(page).locator('input').inputValue(), 'pending-effort');
      await press(page.locator('.row.agent[data-id="omo"] .ag-link'));
      await editor(page).waitFor({ state: 'hidden' });
      await open(page);
      assert.equal(await card(page).locator('input').inputValue(), 'pending-effort');
      assert.equal(api.gets, reads, 'refresh, collapse and reopen reuse the draft without another read overwriting the edit');
      assert.equal(await namedCard(page, 'plan-consultant').locator('.omo-cat-reason-pick').textContent(), await translated(page, 'max'));
      api.saveFailure = true;
      let release;
      api.pendingPost = new Promise((resolve) => { release = resolve; });
      // Await the POST before holding its reply; the assertion needs the save never to finish early.
      const requested = page.waitForRequest((r) => new URL(r.url()).pathname === endpoint && r.method() === "POST");
      const response = page.waitForResponse((r) => new URL(r.url()).pathname === endpoint && r.request().method() === "POST");
      try {
        await press(save(page));
        await requested;
        const controls = await editor(page).locator("button, input, select").evaluateAll((elements) => elements.map((e) => ({ className: e.className, disabled: e.disabled })));
        assert(controls.length > 10);
        for (const name of namedAgents) assert.equal(await namedCard(page, name).locator('.omo-cat-reason-pick').isDisabled(), true);
        assert.deepEqual(controls.filter((c) => !c.disabled), [], "every draft control is disabled while saving, so a late edit survives the success reply");
        assert.equal(api.posts.length, 1);
        assert.deepEqual(api.posts[0].changes, [{ name: "quick", reasoning: { value: "pending-effort" } }, { name: 'plan-consultant', scope: 'agents', reasoning: { value: 'max' } }]);
      } finally {
        release();
        await response;
      }
      await editor(page).locator('[role="alert"]').waitFor();
      assert.equal(await save(page).isDisabled(), false);
      assert.equal(await cancel(page).isDisabled(), false);
      assert.equal(await card(page).locator("input").inputValue(), "pending-effort");
      assert.equal(await card(page).locator('input').isDisabled(), false);
      api.saveFailure = false;
      api.pendingPost = undefined;
      await commit(page);
      assert.equal(await card(page).locator('input').inputValue(), 'pending-effort');
    });

    test(`${engine} ${lang}: cancel and conflicting reload require explicit discard`, { timeout: 60000 }, async (t) => {
      const { page, api } = await setup(t, engine, lang);
      await open(page);
      await reason(page, "high");
      await press(cancel(page));
      assert.deepEqual(api.posts, []);
      assert.equal(await reasoning(page).textContent(), await translated(page, "low"));
      assert.equal(await editor(page).isVisible(), true);
      const dialog = page.locator("dialog.action-confirm[open]");
      await reason(page, "high");
      api.conflict = true;
      api.current.revision = "external-revision";
      api.current.categories[0].reasoning = "medium";
      const rejected = page.waitForResponse((r) => new URL(r.url()).pathname === endpoint && r.status() === 409);
      await press(save(page));
      await rejected;
      await editor(page).locator('[role="alert"]').waitFor();
      assert.equal(await reasoning(page).textContent(), await translated(page, "high"));
      const gets = api.gets;
      await press(editor(page).locator(".omo-cat-reload"));
      await press(dialog.locator("button:not(.primary)"));
      assert.equal(api.gets, gets);
      assert.equal(await reasoning(page).textContent(), await translated(page, "high"));
      await press(editor(page).locator(".omo-cat-reload-head"));
      const reloaded = page.waitForResponse((r) => new URL(r.url()).pathname === endpoint && r.request().method() === "GET");
      await press(dialog.locator("button.primary"));
      await reloaded;
      await card(page).waitFor();
      assert.equal(await reasoning(page).textContent(), await translated(page, "medium"));
      assert.equal(await save(page).isDisabled(), true);
      assert.equal(api.posts.length, 1);
      api.conflict = false;
      await reason(page, "max");
      await commit(page);
      assert.equal(api.posts.at(-1).revision, "external-revision");
    });
  }
}
