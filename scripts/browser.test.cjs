// Real-browser regression tests. Playwright is a test-only dependency installed
// outside the repository; the application still has no browser build step.
const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const { mkdtempSync, rmSync, mkdirSync } = require('node:fs');
const { tmpdir } = require('node:os');
const path = require('node:path');
const { spawn, execFileSync } = require('node:child_process');
const { chromium } = require('playwright');
const root = path.resolve(__dirname, '..');
const snapshot = path.basename(root) === 'snapshot-buddy';
let browser, demo, address, temporary;
before(async () => {
  temporary = mkdtempSync(path.join(tmpdir(), 'buddy-browser-'));
  const binary = path.join(temporary, 'demo');
  execFileSync('go', ['build', '-o', binary, './scripts/demo'], { cwd: root, stdio: 'pipe' });
  demo = spawn(binary, [], { cwd: root, env: { ...process.env, BUDDY_DEMO_PORT: '0' }, stdio: ['ignore', 'pipe', 'pipe'] });
  address = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('Demo failed to start')), 15000);
    const output = data => { const match = data.toString().match(/http:\/\/127\.0\.0\.1:\d+/); if (match) { clearTimeout(timer); resolve(match[0]); } };
    demo.stdout.on('data', output); demo.stderr.on('data', output);
    demo.once('exit', code => { clearTimeout(timer); reject(new Error(`Demo exited: ${code}`)); });
  });
  browser = await chromium.launch({ headless: true });
});
after(async () => {
  await browser?.close();
  if (demo && demo.exitCode === null) { const exited = new Promise(resolve => demo.once('exit', resolve)); demo.kill('SIGTERM'); await exited; }
  if (temporary) rmSync(temporary, { recursive: true, force: true });
});
async function pageFor(t, { mobile = false, fallback = false } = {}) {
  const context = await browser.newContext({ viewport: mobile ? { width: 390, height: 844 } : { width: 1280, height: 900 } });
  t.after(() => context.close());
  const page = await context.newPage();
  const errors = []; page.on('pageerror', error => errors.push(error.message));
  t.after(() => assert.deepEqual(errors, []));
  if (fallback) await page.route('**/api/events', route => route.abort());
  await page.goto(address);
  await page.waitForFunction(() => /Live|Updating every 3/.test(document.getElementById('connection-state').textContent));
  return page;
}
async function marker(page, value) {
  const response = await page.request.post(`${address}/demo/marker`, { form: { marker: value } });
  assert.equal(response.status(), 204);
}
async function session(page) { await page.locator('.session-row').first().click(); await page.waitForFunction(() => document.getElementById('connection-state').textContent === 'Live'); }
async function editSpool(page, index = 0) {
  const form = page.locator('.spool-form').nth(index);
  if (!(await form.getByRole('combobox').isVisible())) await form.locator('.spool-edit').click();
}
async function choose(page, index, query, id) {
  await editSpool(page, index);
  const form = page.locator('.spool-form').nth(index);
  await form.getByRole('combobox').fill(query);
  await form.locator(`[role=option][data-value="${id}"]`).click();
  assert.equal(await form.locator('select').inputValue(), String(id));
}
async function save(page, index = 0) {
  const form = page.locator('.spool-form').nth(index);
  await form.getByRole('button', { name: 'Save spool', exact: true }).click();
  await page.waitForFunction(i => !document.querySelectorAll('.spool-form')[i].buddyPicker.editing, index);
}
async function screenshot(page, name, fullPage = false) {
  if (!process.env.BUDDY_SCREENSHOT_DIR) return;
  mkdirSync(process.env.BUDDY_SCREENSHOT_DIR, { recursive: true });
  await page.screenshot({ fullPage, path: path.join(process.env.BUDDY_SCREENSHOT_DIR, `${snapshot ? 'snapshot' : 'filament'}-${name}.png`) });
}

test('shared page layouts fit desktop and mobile widths', { timeout: 45000 }, async t => {
  const page = await pageFor(t);
  const active = await page.locator('.session-row').nth(0).getAttribute('href');
  const closed = await page.locator('.session-row').nth(1).getAttribute('href');
  const pages = [['library', '/'], ['setup', '/setup'], ['session-active', active], ['session-closed', closed], ['error', '/sessions/missing']];
  if (!snapshot) pages.push(['archived', '/?archived=true']);
  for (const [size, viewport] of [['desktop', { width: 1280, height: 900 }], ['mobile', { width: 390, height: 844 }]]) {
    await page.setViewportSize(viewport);
    for (const [name, pathname] of pages) {
      await page.goto(address + pathname);
      if (!['setup', 'error'].includes(name)) await page.waitForFunction(() => document.getElementById('connection-state').textContent === 'Live');
      await page.locator('h1').waitFor();
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `${name} fits ${size}`);
      await screenshot(page, `review-${name}-${size}`);
      await screenshot(page, `review-${name}-${size}-full`, true);
    }
  }
});

test('setup exposes complete Start, End, and third blocks with exact copying', { timeout: 30000 }, async t => {
  const page = await pageFor(t);
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async text => { window.testClipboard = text; } } });
  });
  const plain = await browser.newContext({ javaScriptEnabled: false });
  t.after(() => plain.close());
  const fallback = await plain.newPage(); await fallback.goto(address + '/setup');
  assert.equal(await fallback.locator('.printer-choice').isVisible(), false);
  const original = await fallback.locator('.setup-printer').evaluateAll(panels => panels.map(panel => ({
    id: panel.id, codes: [...panel.querySelectorAll('pre')].map(pre => ({ id: pre.id, text: pre.textContent })),
  })));
  for (const panel of await fallback.locator('.setup-printer').all()) assert.equal(await panel.isVisible(), true);
  for (const code of await fallback.locator('pre').all()) assert.equal(await code.isVisible(), true);
  const titles = ['Start G-code', 'End G-code', snapshot ? 'After layer change G-code' : 'Color change G-code'];
  for (const printer of original) {
    await page.goto(address + '/setup#' + printer.id);
    assert.equal(await page.locator('#setup-printer').inputValue(), printer.id);
    const panel = page.locator('.setup-printer:visible');
    assert.equal(await panel.count(), 1);
    assert.deepEqual(await panel.locator('h3').allTextContents(), titles);
    assert.deepEqual(await panel.locator('.step-number').allTextContents(), ['01', '02', '03']);
    assert.equal(await page.getByText('What happens next', { exact: true }).count(), 0);
    for (const [i, code] of printer.codes.entries()) {
      const pre = panel.locator('pre').nth(i);
      assert.equal(await pre.isVisible(), true);
      assert.equal(await pre.textContent(), code.text);
      await panel.locator('[data-copy]').nth(i).click();
      await page.waitForFunction(text => window.testClipboard === text, code.text);
    }
    assert.ok(await panel.locator('.command').count());
    assert.ok(await panel.locator('.comment').count());
    for (const width of [1440, 768, 390, 320]) {
      await page.setViewportSize({ width, height: 1000 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
      assert.equal(await panel.locator('pre').evaluateAll(nodes => nodes.every(node => node.scrollHeight === node.clientHeight && node.scrollWidth === node.clientWidth)), true, 'complete code is visible without internal scrolling');
      await screenshot(page, 'setup-' + printer.id + '-' + width, true);
    }
  }
  if (original.length > 1) {
    await page.goto(address + '/setup');
    await page.evaluate(() => { window.setupDocument = true; });
    await page.locator('#setup-printer').selectOption(original[1].id);
    assert.equal(await page.locator('.setup-printer:visible').getAttribute('id'), original[1].id);
    assert.equal(new URL(page.url()).hash, '#' + original[1].id);
    assert.equal(await page.evaluate(() => window.setupDocument), true);
    await page.goBack();
    assert.equal(await page.locator('#setup-printer').inputValue(), original[0].id);
    await page.goForward();
    assert.equal(await page.locator('#setup-printer').inputValue(), original[1].id);
    await page.reload();
    assert.equal(await page.locator('.setup-printer:visible').getAttribute('id'), original[1].id);
  }
  await page.evaluate(() => { navigator.clipboard.writeText = async () => { throw new Error('Unavailable'); }; });
  const panel = page.locator('.setup-printer:visible');
  const expected = await panel.locator('pre').first().textContent();
  await panel.locator('[data-copy]').first().click();
  await page.waitForFunction(text => window.getSelection().toString() === text, expected);
  assert.equal(await panel.locator('[data-copy]').first().textContent(), 'Copy manually');
});

test('printer filters update in place and retain the library view', { timeout: 30000 }, async t => {
  const page = await pageFor(t);
  for (const archived of snapshot ? [false] : [false, true]) {
    await page.goto(address + (archived ? '/?archived=true' : '/'));
    await page.evaluate(() => { window.filterDocument = {}; window.filterVideo = document.querySelector('video'); });
    const identity = await page.evaluateHandle(() => window.filterDocument);
    const allSessions = await page.locator('.session-row').evaluateAll(rows => rows.map(row => row.getAttribute('href')));
    const printers = await page.locator('.filter option').evaluateAll(options => options.filter(option => option.value).map(option => ({ id: option.value, name: option.textContent })));
    assert.equal(await page.getByRole('button', { name: 'Filter', exact: true }).count(), 0);
    for (const width of [1440, 390, 320]) {
      await page.setViewportSize({ width, height: 1000 });
      const [filter, select] = await page.locator('.library-controls').evaluate(node => [...node.querySelectorAll('select,[data-selection-toggle]')].map(item => {
        const rect = item.getBoundingClientRect(); return { top: rect.top, right: rect.right, left: rect.left, height: rect.height, fontSize: getComputedStyle(item).fontSize, fontWeight: getComputedStyle(item).fontWeight };
      }));
      assert.equal(filter.height, select.height);
      assert.equal(filter.top, select.top);
      assert.equal(filter.fontSize, select.fontSize);
      assert.equal(filter.fontWeight, select.fontWeight);
      assert.equal(select.left - filter.right, 8);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    }
    for (const printer of printers) {
      await page.locator('.filter select').selectOption(printer.id);
      await page.waitForFunction(() => !window.buddyUpdates.filtering);
      assert.equal(new URL(page.url()).searchParams.get('printer'), printer.id);
      assert.equal(await page.locator('.filter select').inputValue(), printer.id);
      assert.equal(new URL(page.url()).searchParams.get('archived') === 'true', archived);
      assert.ok((await page.locator('.session-main p').allTextContents()).every(text => text.includes(printer.name)));
      assert.equal(await page.locator('#bulk-form input[name=printer]').inputValue(), printer.id);
      assert.equal(new URL(await page.locator('[data-selection-toggle]').getAttribute('href'), address).searchParams.get('printer'), printer.id);
      await page.locator('.filter select').selectOption('');
      await page.waitForFunction(() => !window.buddyUpdates.filtering);
      assert.equal(new URL(page.url()).searchParams.get('archived') === 'true', archived);
      assert.deepEqual(await page.locator('.session-row').evaluateAll(rows => rows.map(row => row.getAttribute('href'))), allSessions);
      await page.goBack();
      await page.waitForFunction(id => !window.buddyUpdates.filtering && document.querySelector('.filter select').value === id, printer.id);
      assert.ok((await page.locator('.session-main p').allTextContents()).every(text => text.includes(printer.name)));
      await page.goForward();
      await page.waitForFunction(() => !window.buddyUpdates.filtering && document.querySelector('.filter select').value === '');
      assert.deepEqual(await page.locator('.session-row').evaluateAll(rows => rows.map(row => row.getAttribute('href'))), allSessions);
    }
    assert.equal(await page.evaluate(value => value === window.filterDocument, identity), true, 'filtering and history retain the document');
    assert.equal(await page.evaluate(() => window.filterVideo === document.querySelector('video')), true, 'filtering retains the live camera');
  }
  const plain = await browser.newContext({ javaScriptEnabled: false });
  t.after(() => plain.close());
  const fallback = await plain.newPage(); await fallback.goto(address);
  await fallback.locator('.filter select').selectOption(snapshot ? 'core-one' : 'c1');
  await fallback.getByRole('button', { name: 'Filter', exact: true }).click();
  assert.equal(new URL(fallback.url()).searchParams.get('printer'), snapshot ? 'core-one' : 'c1');
});

test('rapid filter changes and reconnect discard stale session lists', { timeout: 30000 }, async t => {
  const page = await pageFor(t);
  await page.waitForFunction(() => !window.buddyUpdates.fetching);
  const allSessions = await page.locator('.session-row').evaluateAll(rows => rows.map(row => row.getAttribute('href')));
  const printer = snapshot ? 'core-one' : 'c1';
  let release, received;
  const blocked = new Promise(resolve => { received = resolve; });
  const hold = new Promise(resolve => { release = resolve; });
  t.after(() => release());
  let delay = true;
  await page.route('**/api/live?**', async route => {
    if (delay && new URL(route.request().url()).searchParams.get('printer') === printer) {
      delay = false;
      const response = await route.fetch(); received();
      await hold;
      await route.fulfill({ response });
    } else await route.continue();
  });
  await page.locator('.filter select').selectOption(printer);
  await blocked;
  assert.equal(await page.locator('#library-list').getAttribute('aria-busy'), 'true');
  assert.equal(await page.getByRole('button', { name: 'Select', exact: true }).getAttribute('aria-disabled'), 'true');
  await page.locator('.filter select').selectOption('');
  await page.waitForFunction(() => !window.buddyUpdates.filtering);
  release();
  await page.waitForFunction(() => !window.buddyUpdates.fetching);
  assert.deepEqual(await page.locator('.session-row').evaluateAll(rows => rows.map(row => row.getAttribute('href'))), allSessions);
  assert.equal(await page.locator('.filter select').inputValue(), '');
  await page.unroute('**/api/live?**');
  await page.route('**/api/live?**', route => route.abort());
  await page.evaluate(() => { window.filterDocument = true; });
  await page.locator('.filter select').selectOption(printer);
  await page.waitForFunction(() => document.getElementById('connection-state').textContent.includes('Disconnected'));
  assert.equal(await page.locator('#library-list').getAttribute('aria-busy'), 'true');
  assert.equal(await page.evaluate(() => window.filterDocument), true);
  await page.unroute('**/api/live?**');
  await page.waitForFunction(() => !window.buddyUpdates.filtering);
  assert.equal(await page.locator('#library-list').getAttribute('aria-busy'), null);
  const label = await page.locator('.filter select option:checked').textContent();
  assert.ok((await page.locator('.session-main p').allTextContents()).every(text => text.includes(label)));
});

test('compact session layout keeps primary content in the first viewport', { timeout: 30000 }, async t => {
  const page = await pageFor(t); await session(page);
  await page.setViewportSize({ width: 1280, height: 800 });
  await screenshot(page, 'session-compact-desktop');
  const firstBottom = await page.locator(snapshot ? '.frame-card' : '.section-card').first().evaluate(n => n.getBoundingClientRect().bottom);
  t.diagnostic(`First ${snapshot ? 'snapshot' : 'section'} ends at ${Math.round(firstBottom)}px`);
  if (!snapshot) assert.ok(firstBottom <= 720, `first section ends at ${firstBottom}px`);
  else assert.ok(firstBottom <= 800, `first snapshot ends at ${firstBottom}px`);
  await page.setViewportSize({ width: 1280, height: 900 });
  if (!snapshot) {
    const secondBottom = await page.locator('.section-card').nth(1).evaluate(n => n.getBoundingClientRect().bottom);
    t.diagnostic(`Second section ends at ${Math.round(secondBottom)}px`);
    assert.ok(secondBottom <= 900, `second section ends at ${secondBottom}px`);
    const form = page.locator('.spool-form').first();
    assert.equal(await form.getByRole('combobox').isVisible(), false);
    assert.equal(await form.getByRole('button', { name: 'Save spool', exact: true }).isVisible(), false);
    await editSpool(page);
    const picker = await form.locator('.spool-picker').evaluate(n => n.getBoundingClientRect().toJSON());
    const save = await form.getByRole('button', { name: 'Save spool', exact: true }).evaluate(n => n.getBoundingClientRect().toJSON());
    assert.ok(save.top >= picker.bottom, 'save sits below the inline picker');
    await form.getByRole('button', { name: 'Cancel', exact: true }).click();
  }
  await page.locator('.name-edit summary').click();
  const editor = await page.locator('.name-form').evaluate(n => n.getBoundingClientRect().toJSON());
  const metadata = await page.locator('#session-heading .lede').evaluate(n => n.getBoundingClientRect().toJSON());
  assert.ok(editor.bottom <= metadata.top, 'inline name editor replaces the title above metadata');
  await page.getByRole('link', { name: 'Cancel', exact: true }).click();
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  await screenshot(page, 'session-compact-mobile');
  if (!snapshot) {
    const plain = await browser.newContext({ javaScriptEnabled: false, viewport: { width: 390, height: 844 } });
    t.after(() => plain.close());
    const fallback = await plain.newPage(); await fallback.goto(page.url());
    assert.equal(await fallback.locator('.spool-form select').first().isVisible(), true);
    assert.equal(await fallback.getByRole('button', { name: 'Save spool', exact: true }).first().isEnabled(), true);
    assert.equal(await fallback.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  }
});

test('new prints update automatically while filters, focus, and camera nodes survive', { timeout: 30000 }, async t => {
  const page = await pageFor(t);
  await page.evaluate(() => { window.testIdentity = 'same document'; window.testVideo = document.querySelector('video'); });
  const row = page.locator('.session-row').first(); await row.focus();
  const title = 'Browser live library';
  await marker(page, `START ${snapshot ? 'core-one' : 'c1'} ${title}`);
  await page.waitForTimeout(200);
  assert.equal(await page.locator('.session-row').filter({ hasText: title }).count(), 0, 'focused list must defer insertion');
  await page.locator('.filter select').focus();
  await page.waitForFunction(text => [...document.querySelectorAll('.session-row')].some(r => r.textContent.includes(text)), title);
  assert.equal(await page.evaluate(() => window.testIdentity), 'same document');
  assert.equal(await page.evaluate(() => window.testVideo === document.querySelector('video')), true);
  assert.equal(await page.locator('.filter select').evaluate(n => n === document.activeElement), true);
});

test('polling fallback continues live updates and reconnects without losing the page', { timeout: 30000 }, async t => {
  const page = await pageFor(t, { fallback: true });
  await page.evaluate(() => { window.testIdentity = 'same document'; });
  const title = 'Browser fallback library';
  await marker(page, `START ${snapshot ? 'core-one' : 'c1'} ${title}`);
  await page.waitForFunction(text => [...document.querySelectorAll('.session-row')].some(r => r.textContent.includes(text)), title, { timeout: 10000 });
  await page.unroute('**/api/events');
  await page.waitForFunction(() => document.getElementById('connection-state').textContent === 'Live', null, { timeout: 15000 });
  assert.equal(await page.evaluate(() => window.testIdentity), 'same document');
});

test('offline reconnect and application restart preserve drafts and renew form credentials', { timeout: 30000 }, async t => {
  const page = await pageFor(t); await session(page);
  if (snapshot) await page.locator('#duration').fill('23');
  else await choose(page, 0, 'white', 12);
  const before = await page.locator('input[name=csrf]').first().inputValue();
  await page.evaluate(() => { window.testIdentity = 'same document'; });
  await page.context().setOffline(true);
  const response = await fetch(`${address}/demo/restart`, { method: 'POST' });
  assert.equal(response.status, 204);
  await page.waitForTimeout(150);
  await page.context().setOffline(false);
  await page.waitForFunction(token => document.querySelector('input[name=csrf]').value !== token && document.getElementById('connection-state').textContent === 'Live', before);
  assert.equal(await page.locator(snapshot ? '#duration' : '.spool-form select').first().inputValue(), snapshot ? '23' : '12');
  assert.equal(await page.evaluate(() => window.testIdentity), 'same document');
});

if (snapshot) {
  test('capture updates preserve duration, image nodes, and exactly-once download intent', { timeout: 45000 }, async t => {
    const page = await pageFor(t); await session(page);
    await marker(page, 'LAYER core-one 1');
    await page.waitForSelector('[data-capture-state="saved"]');
    await page.locator('#duration').fill('20');
    await page.evaluate(() => { window.testImage = document.querySelector('[data-capture] img'); window.testIdentity = 'same document'; });
    await marker(page, 'LAYER core-one 2');
    await page.waitForFunction(() => document.querySelectorAll('[data-capture-state="saved"]').length === 2);
    assert.equal(await page.locator('#duration').inputValue(), '20');
    assert.equal(await page.evaluate(() => window.testImage === document.querySelector('[data-capture] img')), true);
    let downloads = 0; page.on('download', () => downloads++);
    await page.getByRole('button', { name: 'Create video' }).click();
    await page.waitForFunction(() => document.querySelector('[data-job][data-state="ready"]') !== null);
    await page.waitForTimeout(300);
    assert.equal(downloads, 1);
    await marker(page, 'LAYER core-one 3');
    await page.waitForFunction(() => document.querySelectorAll('[data-capture-state="saved"]').length === 3);
    assert.equal(downloads, 1);
    assert.equal(await page.locator("#duration").inputValue(), "20");
    assert.equal(await page.evaluate(() => window.testIdentity), 'same document');
    const jobID = await page.locator('[data-job][data-state="ready"]').first().getAttribute('data-job');
    const other = await page.context().newPage(); let otherDownloads = 0;
    other.on('download', () => otherDownloads++);
    await other.goto(`${page.url().split('?')[0].split('#')[0]}?download=${jobID}`);
    await other.waitForFunction(() => document.getElementById('connection-state').textContent === 'Live');
    assert.equal(otherDownloads, 0, 'a copied URL cannot authorize an automatic download');
    await other.close();
    await screenshot(page, 'desktop');
    await page.setViewportSize({ width: 390, height: 844 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    await screenshot(page, 'mobile');
  });
} else {
  test('drafts survive unrelated changes and same-field conflicts can be resolved explicitly', { timeout: 45000 }, async t => {
    const page = await pageFor(t); await session(page);
    const other = await page.context().newPage(); await other.goto(page.url()); await other.waitForSelector('[role=combobox]', { state: 'attached' });
    const searchOnly = page.locator('.spool-form').first().getByRole('combobox');
    await editSpool(page);
    await searchOnly.fill('galaxy');
    await choose(other, 0, 'purple', 7); await save(other);
    await page.waitForFunction(() => document.querySelector('.spool-form select').value === '7');
    assert.equal(await searchOnly.inputValue(), 'galaxy');
    assert.equal(await page.getByRole('button', { name: 'Use saved value' }).count(), 0, 'search alone is not an assignment draft');
    await choose(page, 0, 'white', 12);
    await other.getByRole('button', { name: '+ Prepare next section' }).click();
    await page.waitForFunction(() => document.querySelectorAll('.section-card').length === 2);
    assert.equal(await page.locator('.spool-form select').first().inputValue(), '12');
    await choose(other, 0, 'blue', 19); await save(other);
    const form = page.locator('.spool-form').first();
    await form.getByRole('button', { name: 'Use saved value' }).waitFor();
    assert.equal(await form.locator('select').inputValue(), '12');
    await form.getByRole('button', { name: 'Use saved value' }).focus();
    await other.getByRole('button', { name: '+ Prepare next section' }).click();
    await page.waitForFunction(() => document.querySelectorAll('.section-card').length === 3);
    assert.equal(await form.getByRole('button', { name: 'Use saved value' }).evaluate(n => n === document.activeElement), true);
    await form.getByRole('button', { name: 'Use saved value' }).click();
    assert.equal(await form.locator('select').inputValue(), '19');
    await choose(page, 0, 'purple', 7);
    await choose(other, 0, 'white', 12); await save(other);
    await form.getByRole('button', { name: 'Save my value' }).click();
    await page.waitForFunction(() => document.querySelector('.spool-label').textContent.includes('Purple'));
    const response = await page.request.get(`${address}/api/sessions/${page.url().split('/').pop()}`);
    assert.equal((await response.json()).Sections[0].SpoolID, 7);
    await other.close();
  });

  test('search, keyboard selection, archived spools, and catalog updates work on mobile', { timeout: 30000 }, async t => {
    const page = await pageFor(t, { mobile: true }); await session(page);
    const response = await page.request.post(`${address}/demo/catalog`, { data: [{ ID: 77, Label: '#77 · Retired Purple PLA', Archived: true, Color: '8844aa' }, { ID: 88, Label: '#88 · Fresh Yellow PETG', Color: 'ffee00', RemainingMG: 123000 }] });
    assert.equal(response.status(), 204);
    await page.waitForFunction(() => window.BuddyPicker.spools.some(s => s.ID === 88));
    const form = page.locator('.spool-form').first(); const input = form.getByRole('combobox');
    await editSpool(page);
    await input.fill('retired');
    assert.equal(await form.locator('[role=option][data-value="77"]').count(), 0);
    await form.getByLabel('Include archived').check();
    assert.equal(await form.locator('[role=option][data-value="77"]').count(), 1);
    await input.fill('88'); await input.press('ArrowDown'); await input.press('ArrowDown'); await input.press('Enter');
    assert.equal(await form.locator('select').inputValue(), '88');
    assert.equal(await input.getAttribute('aria-expanded'), 'false');
    await input.fill('fresh yellow');
    await input.press('ArrowDown'); await input.press('ArrowDown');
    const highlight = await input.getAttribute('aria-activedescendant');
    await page.request.post(`${address}/demo/catalog`, { data: [{ ID: 88, Label: '#88 · Fresh Yellow PETG', RemainingMG: 120000 }] });
    await page.waitForFunction(() => window.BuddyPicker.spools.find(s => s.ID === 88).RemainingMG === 120000);
    assert.equal(await input.inputValue(), 'fresh yellow');
    assert.equal(await input.getAttribute('aria-activedescendant'), highlight);
    assert.equal(await form.locator('select').inputValue(), '88');
    assert.equal(await input.evaluate(n => n === document.activeElement), true);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    const pickerTop = await form.locator('.spool-picker').evaluate(n => n.getBoundingClientRect().top);
    const saveTop = await form.getByRole('button', { name: 'Save spool', exact: true }).evaluate(n => n.getBoundingClientRect().top);
    assert.ok(pickerTop < saveTop);
    await screenshot(page, 'mobile-picker');
    await page.setViewportSize({ width: 1280, height: 900 }); await screenshot(page, 'desktop-picker');
  });

  test('a removed planned section retains its unfinished draft', { timeout: 30000 }, async t => {
    const page = await pageFor(t); await session(page);
    const count = await page.locator('.section-card').count();
    await page.getByRole('button', { name: '+ Prepare next section' }).click();
    await page.waitForFunction(n => document.querySelectorAll('.section-card').length === n + 1, count);
    const other = await page.context().newPage(); await other.goto(page.url());
    await choose(page, count, 'white', 12);
    await other.locator('.section-card').last().getByRole('button', { name: 'Remove', exact: true }).click();
    await page.locator('[data-removed-draft]').waitFor();
    assert.equal(await page.locator('[data-removed-draft] select').inputValue(), '12');
    await page.getByRole('button', { name: 'Discard draft' }).click();
    assert.equal(await page.locator('.section-card').count(), count);
    await other.close();
  });
}

if (!snapshot) test('uncertain saves keep the draft and wait for resynchronization without replay', { timeout: 30000 }, async t => {
  const page = await pageFor(t); await session(page);
  await choose(page, 0, 'white', 12);
  let writes = 0;
  await page.route('**/sessions/*/spool', async route => {
    writes++;
    await page.context().setOffline(true);
    await route.abort('failed');
  });
  const form = page.locator('.spool-form').first();
  const button = form.getByRole('button', { name: 'Save spool', exact: true });
  await button.click();
  await form.getByText('Could not confirm whether this was saved.', { exact: false }).waitFor();
  assert.equal(await button.isDisabled(), true);
  assert.equal(await form.locator('select').inputValue(), '12');
  await page.context().setOffline(false);
  await page.waitForFunction(() => !document.querySelector('.spool-form').buddyState.unconfirmed);
  assert.equal(await button.isDisabled(), false);
  assert.equal(writes, 1);
  assert.equal(await form.locator('select').inputValue(), '12');
});

test('library insertion and removal keep a surviving visible row anchored', { timeout: 30000 }, async t => {
  const page = await pageFor(t, { mobile: true });
  await page.setViewportSize({ width: 390, height: 550 });
  await page.locator('.session-row').nth(2).evaluate(n => window.scrollTo(0, window.scrollY + n.getBoundingClientRect().top - 20));
  const key = await page.locator('.session-row').nth(3).getAttribute('data-key');
  const anchor = page.locator(`[data-key="${key}"]`);
  const top = await anchor.evaluate(n => n.getBoundingClientRect().top);
  const title = 'Browser scroll anchor';
  await marker(page, `START ${snapshot ? 'core-one' : 'c1'} ${title}`);
  await page.waitForFunction(text => [...document.querySelectorAll('.session-row')].some(r => r.textContent.includes(text)), title);
  assert.ok(Math.abs(await anchor.evaluate(n => n.getBoundingClientRect().top) - top) < 2);
  const visible = await page.locator('.session-row').evaluateAll(rows => rows.filter(n => n.getBoundingClientRect().bottom > 0 && n.getBoundingClientRect().top < innerHeight).map(n => ({ key: n.dataset.key, href: n.getAttribute('href'), top: n.getBoundingClientRect().top })));
  const removeID = visible[0].href.split('/').pop();
  const survivor = page.locator(`[data-key="${visible[1].key}"]`);
  const current = await (await page.request.get(`${address}/api/live?view=session&id=${removeID}`)).json();
  const response = await page.request.post(`${address}/sessions/${removeID}/${snapshot ? 'delete' : 'archive'}`, {
    headers: { Accept: 'application/json' }, form: { csrf: current.csrf, revision: String(current.revision ?? '') },
  });
  assert.equal(response.status(), 200);
  await page.waitForFunction(id => !document.querySelector(`.session-row[href="/sessions/${id}"]`), removeID);
  assert.ok(Math.abs(await survivor.evaluate(n => n.getBoundingClientRect().top) - visible[1].top) < 2);
});

test('opening and cancelling the title editor preserves vertical layout', { timeout: 45000 }, async t => {
  const page = await pageFor(t);
  const printer = snapshot ? 'core-one' : 'c1';
  const positions = target => target.evaluate(() => ['#session-heading .eyebrow', '.session-name', '#session-heading .lede', '#summary', '#manage', 'footer'].map(selector => {
    const rect = document.querySelector(selector).getBoundingClientRect();
    return { selector, top: rect.top + scrollY, height: rect.height };
  }));
  for (const title of ['House', 'Workshop storage trays and camera accessories with a very long session name that wraps onto multiple lines on smaller screens']) {
    await page.goto(address);
    const original = title.length > 20 ? 'Long title layout' : title;
    await marker(page, `START ${printer} ${original}`);
    await page.locator('.session-row').filter({ hasText: original }).click();
    await page.waitForFunction(() => document.getElementById('connection-state').textContent === 'Live');
    if (title !== original) {
      await page.getByRole('button', { name: 'Edit session name', exact: true }).click();
      await page.getByLabel('Display name', { exact: true }).fill(title);
      await page.getByRole('button', { name: 'Save name', exact: true }).click();
      await page.waitForFunction(value => document.querySelector('h1').textContent === value && !document.querySelector('.name-edit').open, title);
    }
    for (const width of [1440, 768, 680, 390, 320]) {
      await page.setViewportSize({ width, height: 1000 });
      const before = await positions(page);
      await page.getByRole('button', { name: 'Edit session name', exact: true }).click();
      const input = page.getByLabel('Display name', { exact: true });
      assert.deepEqual(await positions(page), before, `${width}px editor preserves layout for ${title}`);
      await input.fill('An unsaved draft');
      assert.deepEqual(await positions(page), before, 'typing preserves layout');
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
      if (title === 'House' && [1440, 390].includes(width)) await screenshot(page, `name-edit-stable-${width}`);
      await input.press('Escape');
      assert.deepEqual(await positions(page), before, 'Escape restores the title without movement');
      await page.getByRole('button', { name: 'Edit session name', exact: true }).click();
      await page.getByRole('link', { name: 'Cancel', exact: true }).click();
      assert.deepEqual(await positions(page), before, 'Cancel restores the title without movement');
    }
    const plain = await browser.newContext({ javaScriptEnabled: false, viewport: { width: 390, height: 1000 } });
    try {
      const native = await plain.newPage(); await native.goto(page.url());
      const before = await positions(native);
      await native.getByRole('button', { name: 'Edit session name', exact: true }).click();
      assert.deepEqual(await positions(native), before, 'native editor preserves layout');
    } finally { await plain.close(); }
  }
});

test('inline name editing supports keyboard, cancellation, failed saves, and native forms', { timeout: 30000 }, async t => {
  const page = await pageFor(t);
  const original = 'Inline name editing';
  await marker(page, `START ${snapshot ? 'core-one' : 'c1'} ${original}`);
  await page.locator('.session-row').filter({ hasText: original }).click();
  await page.waitForFunction(() => document.getElementById('connection-state').textContent === 'Live');
  const pencil = page.getByRole('button', { name: 'Edit session name', exact: true });
  const input = page.getByLabel('Display name', { exact: true });
  const heading = await page.locator('h1').boundingBox();
  const icon = await pencil.boundingBox();
  assert.ok(icon.x >= heading.x + heading.width && icon.x - heading.x - heading.width <= 12, 'pencil sits beside the name');
  await pencil.focus(); await pencil.press('Enter');
  assert.equal(await input.inputValue(), '');
  assert.equal(await input.getAttribute('placeholder'), original);
  assert.equal(await input.evaluate(n => n.matches(':placeholder-shown')), true);
  assert.equal(await input.evaluate(n => n === document.activeElement && n.selectionStart === 0 && n.selectionEnd === n.value.length), true);
  await input.fill('Discard with Escape'); await input.press('Escape');
  assert.equal(await page.locator('.name-edit').evaluate(n => n.open), false);
  assert.equal(await pencil.evaluate(n => n === document.activeElement), true);
  await pencil.click(); await input.fill('Discard with Cancel');
  await page.getByRole('link', { name: 'Cancel', exact: true }).click();
  assert.equal(await page.locator('h1').textContent(), original);
  assert.equal(await page.locator('.name-edit').evaluate(n => n.open), false);

  await pencil.click(); await input.fill('Saved with Enter'); await input.press('Enter');
  await page.waitForFunction(() => document.querySelector('h1').textContent === 'Saved with Enter' && !document.querySelector('.name-edit').open);
  assert.equal(await pencil.evaluate(n => n === document.activeElement), true);
  await pencil.click();
  assert.equal(await input.inputValue(), 'Saved with Enter');
  await input.fill('');
  assert.equal(await input.getAttribute('placeholder'), original);
  assert.equal(await input.evaluate(n => n.matches(':placeholder-shown')), true);
  await input.press('Enter');
  await page.waitForFunction(name => document.querySelector('h1').textContent === name && !document.querySelector('.name-edit').open, original);
  const stored = await (await page.request.get(`${address}/api/sessions/${page.url().split('/').pop()}`)).json();
  assert.equal((snapshot ? stored.display_name : stored.DisplayName) ?? '', '');
  await pencil.click();
  assert.equal(await input.inputValue(), '', 'opening the default name must not create a custom override');
  await input.fill('Keep after failure');
  await page.route('**/sessions/*/name', route => route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'Save failed for test' }) }));
  await input.press('Enter');
  await page.getByText('Save failed for test', { exact: true }).waitFor();
  assert.equal(await input.inputValue(), 'Keep after failure');
  assert.equal(await page.locator('.name-edit').evaluate(n => n.open), true);
  await page.unroute('**/sessions/*/name');

  let release;
  const held = new Promise(resolve => { release = resolve; });
  await page.route('**/sessions/*/name', async route => { await held; await route.continue(); });
  await input.fill('First submitted draft'); await input.press('Enter');
  await page.waitForFunction(() => document.querySelector('.name-form').buddyState.saving);
  await input.fill('Continue typing during save');
  release();
  await page.waitForFunction(() => document.querySelector('h1').textContent === 'First submitted draft' && !document.querySelector('.name-form').buddyState.saving);
  assert.equal(await input.inputValue(), 'Continue typing during save');
  assert.equal(await page.locator('.name-edit').evaluate(n => n.open), true);
  await input.press('Escape');
  assert.equal(await page.locator('h1').textContent(), 'First submitted draft');
  await page.unroute('**/sessions/*/name');

  const plain = await browser.newContext({ javaScriptEnabled: false, viewport: { width: 390, height: 844 } });
  t.after(() => plain.close());
  const fallback = await plain.newPage(); await fallback.goto(page.url());
  await fallback.getByRole('button', { name: 'Edit session name', exact: true }).click();
  await fallback.getByLabel('Display name', { exact: true }).fill('Saved without JavaScript');
  await fallback.getByRole('button', { name: 'Save name', exact: true }).click();
  assert.equal(await fallback.locator('h1').textContent(), 'Saved without JavaScript');
  await fallback.getByRole('button', { name: 'Edit session name', exact: true }).click();
  const plainInput = fallback.getByLabel('Display name', { exact: true });
  await plainInput.fill('');
  assert.equal(await plainInput.getAttribute('placeholder'), original);
  assert.equal(await plainInput.evaluate(n => n.matches(':placeholder-shown')), true);
  await fallback.getByRole('button', { name: 'Save name', exact: true }).click();
  assert.equal(await fallback.locator('h1').textContent(), original);
  assert.equal(await fallback.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
});

test('display names update every view, retain drafts, and resolve concurrent edits', { timeout: 45000 }, async t => {
  const page = await pageFor(t);
  const original = 'Browser display name';
  const printer = snapshot ? 'core-one' : 'c1';
  await marker(page, `START ${printer} ${original}`);
  const row = page.locator('.session-row').filter({ hasText: original });
  await row.waitFor(); await row.click();
  await page.waitForFunction(() => document.getElementById('connection-state').textContent === 'Live');
  await page.locator('.name-edit summary').click();
  await page.getByLabel('Display name', { exact: true }).fill('My draft');
  const other = await page.context().newPage(); await other.goto(page.url());
  await other.locator('.name-edit summary').click();
  const library = await page.context().newPage(); await library.goto(address);
  const id = page.url().split('/').pop();
  await marker(page, snapshot ? `LAYER ${printer} 101` : `CHANGE ${printer} 1 12000`);
  await page.waitForFunction(isSnapshot => isSnapshot ? document.querySelector('[data-capture-state="saved"]') : document.querySelectorAll('.section-card').length === 2, snapshot);
  assert.equal(await page.getByLabel('Display name', { exact: true }).inputValue(), 'My draft');
  assert.equal(await page.getByLabel('Display name', { exact: true }).evaluate(n => n === document.activeElement), true);
  const saveName = async (target, value) => {
    if (!(await target.locator('.name-edit').evaluate(n => n.open))) await target.locator('.name-edit summary').click();
    await target.getByLabel('Display name', { exact: true }).fill(value);
    await target.getByRole('button', { name: 'Save name', exact: true }).click();
    await target.waitForFunction(name => document.querySelector('h1').textContent === name && !document.querySelector('.name-edit').open, value.trim() || original);
  };
  const saved = 'Garden <vase> & café 🌿';
  await saveName(other, saved);
  assert.equal(await other.getByRole('button', { name: 'Edit session name', exact: true }).evaluate(n => n === document.activeElement), true);
  await page.getByRole('button', { name: 'Use saved value', exact: true }).waitFor();
  assert.equal(await page.getByLabel('Display name', { exact: true }).inputValue(), 'My draft');
  assert.ok((await page.locator('.name-form .form-message').textContent()).includes(saved));
  await library.waitForFunction(({ id, saved }) => document.querySelector(`.session-row[href="/sessions/${id}"] h3`).textContent === saved && document.querySelector('[data-role="name"]').textContent === saved, { id, saved });
  assert.equal(await page.title(), `${saved} · ${snapshot ? 'Snapshot' : 'Filament'} Buddy`);
  await page.getByRole('button', { name: 'Use saved value', exact: true }).click();
  assert.equal(await page.getByLabel('Display name', { exact: true }).inputValue(), saved);
  const mine = 'Plant pot with a matching tray — a gift for the kitchen windowsill';
  await page.getByLabel('Display name', { exact: true }).fill(mine);
  await saveName(other, 'Other edit');
  await page.getByRole('button', { name: 'Save my value', exact: true }).click();
  await page.waitForFunction(name => document.querySelector('h1').textContent === name, mine);
  await other.waitForFunction(name => document.querySelector('h1').textContent === name, mine);
  await page.waitForFunction(() => !document.querySelector('.name-edit').open);
  await screenshot(page, 'display-name-desktop');
  await page.locator('.name-edit summary').click();
  await screenshot(page, 'display-name-edit-desktop');
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  await screenshot(page, 'display-name-edit-mobile');
  await page.getByLabel('Display name', { exact: true }).press('Escape');
  await screenshot(page, 'display-name-mobile');
  await saveName(page, '');
  await other.waitForFunction(name => document.querySelector('h1').textContent === name, original);
  assert.equal(await page.title(), `${original} · ${snapshot ? 'Snapshot' : 'Filament'} Buddy`);
  await marker(page, snapshot ? `STOP ${printer} 102` : `STOP ${printer} 2 5000`);
  await page.waitForFunction(() => document.querySelector('#summary').textContent.includes('Closed'));
  await saveName(page, '  Finished vase  ');
  await page.reload();
  assert.equal(await page.locator('h1').textContent(), 'Finished vase');
  const stored = await (await page.request.get(`${address}/api/sessions/${id}`)).json();
  const record = stored;
  assert.equal(snapshot ? record.name : record.Name, original);
  assert.equal(snapshot ? record.display_name : record.DisplayName, 'Finished vase');
  await other.close(); await library.close();
});

if (!snapshot) test('unassigned spool badges track saved assignments across tabs', { timeout: 30000 }, async t => {
  const library = await pageFor(t);
  const catalog = await library.request.post(`${address}/demo/catalog`, { data: [{ ID: 7, Label: '#7 · Purple PLA', Color: '8844aa' }] });
  assert.equal(catalog.status(), 204);
  const title = 'Unassigned spool indicators';
  await marker(library, `START c1 ${title}`);
  const row = library.locator('.session-row').filter({ hasText: title });
  await row.locator('.unassigned-spools').waitFor();
  assert.equal(await row.locator('.unassigned-spools').textContent(), '1 unassigned');
  const page = await library.context().newPage();
  await page.goto(address + await row.getAttribute('href'));
  await page.waitForSelector('[role=combobox]', { state: 'attached' });
  await choose(page, 0, 'purple', 7);
  assert.equal(await page.locator('#summary .unassigned-spools').textContent(), '1 unassigned', 'drafts do not clear the saved warning');
  await save(page);
  await page.locator('#summary .unassigned-spools').waitFor({ state: 'detached' });
  await row.locator('.unassigned-spools').waitFor({ state: 'detached' });
  await page.getByRole('button', { name: '+ Prepare next section' }).click();
  await page.locator('#summary .unassigned-spools').waitFor();
  await choose(page, 0, '', 0); await save(page);
  await library.waitForFunction(name => [...document.querySelectorAll('.session-row')].find(n => n.textContent.includes(name))?.querySelector('.unassigned-spools')?.textContent === '2 unassigned', title);
  assert.equal(await page.locator('#summary .unassigned-spools').textContent(), '2 unassigned');
  await marker(page, 'STOP c1 1 12000');
  await page.getByRole('button', { name: 'Archive session', exact: true }).waitFor();
  await page.getByRole('button', { name: 'Archive session', exact: true }).click();
  await library.goto(address + '/?archived=true');
  await row.locator('.unassigned-spools').waitFor();
  assert.equal(await row.locator('.unassigned-spools').textContent(), '2 unassigned');
  await screenshot(library, 'unassigned-archived-desktop');
  await library.setViewportSize({ width: 390, height: 844 });
  assert.equal(await library.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  await screenshot(library, 'unassigned-archived-mobile');
  await page.close();
});

let bulkSequence = 0;
async function bulkSessions(page, count = 2) {
  const ids = [];
  const printer = snapshot ? 'core-one' : 'c1';
  for (let i = 0; i < count; i++) {
    const title = `Bulk browser ${++bulkSequence}`;
    await marker(page, `START ${printer} ${title}`);
    await marker(page, snapshot ? `STOP ${printer} 1` : `STOP ${printer} 1 ${1000 + bulkSequence}`);
    const row = page.locator('.session-row').filter({ hasText: title });
    await row.waitFor();
    ids.push((await row.getAttribute('href')).split('/').pop());
  }
  return ids;
}
function bulkBox(page, id) { return page.locator(`[data-session-select][value="${id}"]`); }
async function singleBulkAction(page, id, action) {
  const state = await (await page.request.get(`${address}/api/live?view=session&id=${id}`)).json();
  const response = await page.request.post(`${address}/sessions/${id}/${action}`, {
    headers: { Accept: 'application/json' }, form: { csrf: state.csrf, revision: String(state.revision ?? '') },
  });
  assert.equal(response.status(), 200);
}

test('bulk selection mode reveals checkboxes and cancellation clears them without writes', { timeout: 45000 }, async t => {
  const page = await pageFor(t);
  assert.equal(await page.locator('[data-session-select]').first().isVisible(), false);
  assert.equal(await page.locator('[data-select-all]').isVisible(), false);
  assert.equal(await page.locator('[data-bulk-submit]').isVisible(), false);
  const ids = await bulkSessions(page);
  assert.equal(await bulkBox(page, ids[0]).isVisible(), false, 'live arrivals remain outside selection mode');
  let writes = 0;
  page.on('request', request => { if (request.method() === 'POST' && request.url().includes('/sessions/bulk/')) writes++; });
  const toggle = page.locator('[data-selection-toggle]');
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 1000 });
    await screenshot(page, `bulk-browse-${width}`);
    assert.equal(await toggle.textContent(), 'Select');
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false');
    assert.equal(await bulkBox(page, ids[0]).evaluate(n => n.closest('.session-select').getBoundingClientRect().width), 0);
    await toggle.focus(); await toggle.press('Space');
    assert.equal(await toggle.textContent(), 'Cancel');
    assert.equal(await toggle.getAttribute('aria-expanded'), 'true');
    assert.equal(await bulkBox(page, ids[0]).isVisible(), true);
    const centers = await page.evaluate(() => ['[data-select-all]', '[data-session-select]'].map(selector => {
      const rect = document.querySelector(selector).getBoundingClientRect(); return rect.x + rect.width / 2;
    }));
    assert.ok(Math.abs(centers[0] - centers[1]) < 1);
    await bulkBox(page, ids[0]).check();
    await screenshot(page, `bulk-selection-${width}`);
    assert.equal(await page.locator('[data-selection-count]').textContent(), '1 selected');
    await toggle.click();
    assert.equal(await bulkBox(page, ids[0]).isVisible(), false);
    assert.equal(await page.locator('[data-session-select]:checked').count(), 0);
    assert.equal(await page.locator('[data-bulk-submit]').isVisible(), false);
    await toggle.press('Enter');
    await bulkBox(page, ids[0]).check();
    await bulkBox(page, ids[0]).press('Escape');
    assert.equal(await toggle.textContent(), 'Select');
    assert.equal(await toggle.evaluate(n => n === document.activeElement), true);
    assert.equal(await page.locator('[data-session-select]:checked').count(), 0);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  }
  assert.equal(writes, 0);
});

test('bulk checkboxes support keyboard, select-all, and live selection reconciliation on mobile', { timeout: 45000 }, async t => {
  const page = await pageFor(t, { mobile: true });
  await page.getByRole('button', { name: 'Select', exact: true }).click();
  const ids = await bulkSessions(page);
  await marker(page, `START ${snapshot ? 'core-one' : 'c1'} Bulk active`);
  const activeRow = page.locator('.session-entry').filter({ hasText: 'Bulk active' });
  await activeRow.waitFor();
  assert.equal(await activeRow.locator('input[type=checkbox]').isDisabled(), true);
  const box = bulkBox(page, ids[0]);
  await box.focus(); await page.keyboard.press('Space');
  assert.equal(new URL(page.url()).pathname, '/');
  assert.equal(await box.isChecked(), true);
  assert.equal(await page.locator('[data-select-all]').evaluate(n => n.indeterminate), true);
  await page.locator('[data-select-all]').check();
  const eligible = await page.locator('[data-session-select][data-eligible=true]').count();
  assert.equal(await page.locator('[data-session-select]:checked').count(), eligible);
  await page.getByRole('button', { name: 'Clear selection' }).click();
  assert.equal(await page.locator('[data-session-select]:checked').count(), 0);
  await bulkBox(page, ids[0]).check(); await bulkBox(page, ids[1]).check();
  await page.locator('.filter select').focus();
  const added = await bulkSessions(page, 1);
  assert.equal(await bulkBox(page, ids[0]).isChecked(), true);
  assert.equal(await bulkBox(page, ids[1]).isChecked(), true);
  assert.equal(await bulkBox(page, added[0]).isChecked(), false);
  await screenshot(page, 'bulk-selected-mobile');
  await singleBulkAction(page, ids[0], snapshot ? 'delete' : 'archive');
  await page.waitForFunction(id => !document.querySelector(`[data-session-select][value="${id}"]`), ids[0]);
  assert.equal(await bulkBox(page, ids[1]).isChecked(), true);
  assert.match(await page.locator('[data-selection-notice]').textContent(), /deselected/);
  assert.equal(await page.locator('[data-selection-count]').textContent(), '1 selected');
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
});

test('bulk actions confirm only deletion and restore archived sessions', { timeout: 45000 }, async t => {
  const page = await pageFor(t);
  await page.getByRole('button', { name: 'Select', exact: true }).click();
  const ids = await bulkSessions(page);
  for (const id of ids) await bulkBox(page, id).check();
  let writes = 0;
  page.on('request', request => { if (request.method() === 'POST' && request.url().includes('/sessions/bulk/')) writes++; });
  const action = page.locator('[data-bulk-submit]');
  if (snapshot) {
    page.once('dialog', dialog => { assert.match(dialog.message(), /2 selected sessions.*snapshots and videos/); dialog.dismiss(); });
    await action.click();
    assert.equal(writes, 0);
    assert.equal(await page.locator('[data-session-select]:checked').count(), 2);
    page.once('dialog', dialog => dialog.accept());
  } else page.on('dialog', () => assert.fail('Archive and restore must not prompt'));
  await action.click();
  await page.waitForFunction(() => document.querySelector('[data-bulk-results]').textContent.includes('2 sessions'), null, { timeout: 5000 }).catch(async error => { t.diagnostic(await page.locator('[data-bulk-results]').textContent()); throw error; });
  for (const id of ids) await page.waitForFunction(value => !document.querySelector(`[data-session-select][value="${value}"]`), id);
  assert.equal(writes, 1);
  assert.equal(await page.locator('[data-selection-count]').textContent(), '0 selected');
  await screenshot(page, 'bulk-complete-desktop');
  if (!snapshot) {
    await page.getByRole('link', { name: 'View archived sessions' }).click();
    await page.getByRole('button', { name: 'Select', exact: true }).click();
    for (const id of ids) await bulkBox(page, id).check();
    await page.getByRole('button', { name: 'Restore selected', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('[data-bulk-results]').textContent.includes('2 sessions restored.'));
    for (const id of ids) await page.waitForFunction(value => !document.querySelector(`[data-session-select][value="${value}"]`), id);
    assert.equal(writes, 2);
  }
});

test('bulk partial results retain only failed selections and explain the failure', { timeout: 30000 }, async t => {
  const page = await pageFor(t);
  await page.getByRole('button', { name: 'Select', exact: true }).click();
  const ids = await bulkSessions(page);
  for (const id of ids) await bulkBox(page, id).check();
  await page.route('**/sessions/bulk/*', async route => {
    await singleBulkAction(page, ids[0], snapshot ? 'delete' : 'archive');
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({
      summary: `1 session ${snapshot ? 'deleted' : 'archived'}. 1 could not be processed.`, location: '/', results: [
        { id: ids[0], name: 'Completed session', status: 'success' },
        { id: ids[1], name: 'Busy session', status: 'conflict', error: 'Wait for active work to finish.' },
      ],
    }) });
  });
  if (snapshot) page.once('dialog', dialog => dialog.accept());
  await page.locator('[data-bulk-submit]').click();
  await page.getByText('Busy session: Wait for active work to finish.', { exact: true }).waitFor();
  await page.waitForFunction(id => !document.querySelector(`[data-session-select][value="${id}"]`), ids[0]);
  assert.equal(await bulkBox(page, ids[1]).isChecked(), true);
  assert.equal(await page.locator('[data-selection-count]').textContent(), '1 selected');
  assert.equal(await page.locator('[data-bulk-submit]').isEnabled(), true);
});

test('bulk lost responses reconcile submitted sessions without replay', { timeout: 45000 }, async t => {
  const page = await pageFor(t);
  await page.getByRole('button', { name: 'Select', exact: true }).click();
  const ids = await bulkSessions(page);
  for (const id of ids) await bulkBox(page, id).check();
  let writes = 0;
  await page.route('**/sessions/bulk/*', async route => {
    writes++;
    const response = await route.fetch();
    assert.equal(response.status(), 200);
    await page.context().setOffline(true);
    await route.abort('failed');
  });
  if (snapshot) page.once('dialog', dialog => dialog.accept());
  await page.locator('[data-bulk-submit]').click();
  await page.waitForFunction(() => Boolean(window.buddyUpdates.bulk.unconfirmed));
  assert.equal(await page.locator('[data-bulk-submit]').isDisabled(), true);
  assert.equal(await page.locator('[data-selection-toggle]').getAttribute('aria-disabled'), 'true');
  assert.equal(await page.locator('.filter select').isDisabled(), true);
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('#bulk-form').evaluate(n => n.hasAttribute('data-selecting')), true);
  await page.context().setOffline(false);
  await page.waitForFunction(() => !window.buddyUpdates.bulk.unconfirmed);
  assert.equal(await page.locator('.filter select').isEnabled(), true);
  assert.equal(writes, 1);
  assert.equal(await page.locator('[data-selection-count]').textContent(), '0 selected');
  assert.match(await page.locator('[data-bulk-results]').textContent(), /Review the remaining selection/);
});

test('bulk selection survives credential renewal and clears across pages and filters', { timeout: 60000 }, async t => {
  const page = await pageFor(t);
  await page.getByRole('button', { name: 'Select', exact: true }).click();
  const ids = await bulkSessions(page, snapshot ? 25 : 51);
  await bulkBox(page, ids.at(-1)).check();
  const token = await page.locator('#bulk-form [name=csrf]').inputValue();
  await page.request.post(`${address}/demo/restart`);
  await page.waitForFunction(old => document.querySelector('#bulk-form [name=csrf]').value !== old, token);
  assert.equal(await bulkBox(page, ids.at(-1)).isChecked(), true);
  await page.getByRole('link', { name: 'Older prints →' }).click();
  assert.equal(await page.locator('[data-session-select]:checked').count(), 0);
  assert.equal(await page.locator('[data-session-select]').first().isVisible(), false);
  await page.getByRole('button', { name: 'Select', exact: true }).click();
  await page.locator('[data-session-select][data-eligible=true]').first().check();
  await Promise.all([
    page.waitForURL(url => url.searchParams.get('printer') === (snapshot ? 'core-one' : 'c1') && !url.searchParams.has('page')),
    page.locator('.filter select').selectOption(snapshot ? 'core-one' : 'c1'),
  ]);
  assert.equal(await page.locator('[data-session-select]:checked').count(), 0);
  assert.equal(Number(new URL(page.url()).searchParams.get('page')), 0);
});

test('bulk native forms work without JavaScript and retain deletion confirmation', { timeout: 45000 }, async t => {
  const setup = await pageFor(t);
  const ids = await bulkSessions(setup);
  const context = await browser.newContext({ javaScriptEnabled: false });
  t.after(() => context.close());
  const page = await context.newPage(); await page.goto(address);
  assert.equal(await bulkBox(page, ids[0]).isVisible(), false);
  await page.getByRole('button', { name: 'Select', exact: true }).click();
  assert.equal(new URL(page.url()).searchParams.has('csrf'), false);
  await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  assert.equal(await bulkBox(page, ids[0]).isVisible(), false);
  await page.getByRole('button', { name: 'Select', exact: true }).click();
  for (const id of ids) await bulkBox(page, id).check();
  await page.locator('[data-bulk-submit]').click();
  if (snapshot) {
    await page.getByRole('heading', { name: 'Delete 2 selected sessions?' }).waitFor();
    for (const id of ids) assert.equal((await setup.request.get(`${address}/api/sessions/${id}`)).status(), 200);
    await page.getByRole('button', { name: 'Permanently delete selected sessions' }).click();
  }
  await page.getByText(`2 sessions ${snapshot ? 'deleted' : 'archived'}.`, { exact: true }).waitFor();
  assert.equal(await page.locator('[data-session-select]:checked').count(), 0);
});
