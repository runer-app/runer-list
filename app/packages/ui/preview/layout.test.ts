/**
 * What jsdom can't see, measured headless in Chromium and WebKit on the design preview: `npm run test:layout -w @runer/ui`.
 * Needs the Playwright browsers (`npx playwright install chromium webkit`). Sizes and positions only; how it looks is not tested.
 */
import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { chromium, devices, webkit, type Browser, type Page } from 'playwright';
import { createServer, type ViteDevServer } from 'vite';
import { lowContrast } from './contrast.ts';

const LONG_NAME = 'W'.repeat(60);
const MIN_CONTRAST = 4.5; // WCAG AA
const THEMES = ['default', 'moss', 'harbor', 'ember']; // appearance.svelte.ts, which only loads under Svelte
let server: ViteDevServer;
let browsers: Browser[];
let url: string;

before(async () => {
  server = await createServer({ configFile: new URL('./vite.config.ts', import.meta.url).pathname });
  await server.listen();
  url = server.resolvedUrls!.local[0]!;
  browsers = await Promise.all([chromium.launch(), webkit.launch()]);
});

after(async () => {
  await Promise.all(browsers.map((b) => b.close()));
  await server.close();
});

async function open(engine: number, width: number, query = '', ready = 'nav'): Promise<Page> {
  const page = await browsers[engine]!.newPage({ viewport: { width, height: 800 } });
  await page.goto(url + query);
  await page.locator(ready).first().waitFor();
  return page;
}

/** Nothing sticks out sideways: the page doesn't scroll, and no element ends past the window. */
async function assertNoSideways(page: Page, where: string) {
  const over = await page.evaluate(() => {
    const width = document.documentElement.clientWidth;
    const out = [...document.querySelectorAll<HTMLElement>('body *')].filter((el) => {
      const r = el.getBoundingClientRect();
      return r.width > 0 && r.right > width + 1 && getComputedStyle(el).position !== 'fixed' && el.closest('[aria-hidden="true"]') === null;
    });
    return { scrolls: document.documentElement.scrollWidth > width, out: out.slice(0, 3).map((e) => e.tagName + '.' + e.className) };
  });
  assert.deepEqual(over, { scrolls: false, out: [] }, where);
}

const openList = async (page: Page, name: string) => {
  await page.getByRole('button', { name }).first().click();
  await page.getByRole('heading', { name }).waitFor();
};

for (const [engine, name] of ['chromium', 'webkit'].entries()) {
  for (const width of [1100, 390]) {
    test(`${name} at ${width}: the chip in a new task row leaves the text alone and keeps the row's height`, async () => {
      const page = await open(engine, width);
      if (width < 700) await page.locator('.lists-tab').click();
      await openList(page, 'Weekend trip');
      const row = page.getByRole('textbox', { name: 'New task' }).last();
      await row.click();
      const height = (await row.boundingBox())!.height;
      await row.fill('Dentist 12/10');
      const chip = page.getByRole('button', { name: /^Date:/ });
      await chip.waitFor();
      assert.equal((await row.boundingBox())!.height, height);

      const { textEnd, chipStart } = await row.evaluate((el: HTMLTextAreaElement, c) => {
        const s = getComputedStyle(el);
        const ctx = document.createElement('canvas').getContext('2d')!;
        ctx.font = s.font;
        const left = el.getBoundingClientRect().left + parseFloat(s.paddingLeft);
        return { textEnd: left + ctx.measureText(el.value).width, chipStart: c!.getBoundingClientRect().left };
      }, await chip.elementHandle());
      assert.ok(chipStart >= textEnd, `chip at ${chipStart}, text ends at ${textEnd}`);
      await page.close();
    });
  }

  test(`${name}: Help in a narrow window fits, and its back button returns to Settings`, async () => {
    const page = await open(engine, 390);
    await page.locator('.settings-tab').click();
    await page.getByRole('button', { name: 'Open Help' }).click();
    await page.getByRole('heading', { name: 'Help' }).waitFor();
    await assertNoSideways(page, 'Help');
    await page.getByRole('main').getByRole('button', { name: 'Settings' }).click();
    await page.getByRole('heading', { name: 'Settings' }).waitFor();
    await page.close();
  });

  for (const width of [1100, 390]) {
    test(`${name} at ${width}: a 60-character name without spaces doesn't break the layout`, async () => {
      const page = await open(engine, width, `?member=${LONG_NAME}`);
      if (width < 700) await page.locator('.lists-tab').click();
      await openList(page, 'Weekend trip');
      await assertNoSideways(page, 'the list');
      await page.getByRole('button', { name: 'Share' }).click();
      await page.getByRole('dialog').waitFor();
      await assertNoSideways(page, 'the share dialog');
      await page.close();
    });

    test(`${name} at ${width}: a 60-character name fits the join notice and the Join screen`, async () => {
      const page = await open(engine, width, `?member=${LONG_NAME}`);
      await page.getByRole('status').filter({ hasText: 'joined' }).waitFor();
      await assertNoSideways(page, 'the join notice');
      await page.close();
      const join = await open(engine, width, `?join&member=${LONG_NAME}`, 'h1');
      await join.getByRole('button', { name: 'Join' }).waitFor();
      await assertNoSideways(join, 'the Join screen');
      await join.close();
    });

    test(`${name} at ${width}: Settings has two columns on a wide window and one on a narrow one`, async () => {
      const page = await open(engine, width);
      await page.locator('.settings-tab').click();
      const columns = await page.locator('main section').first().evaluate((el) => getComputedStyle(el).gridTemplateColumns.split(' ').length);
      assert.equal(columns, width < 700 ? 1 : 2);
      await page.close();
    });
  }

  test(`${name}: the sidebar has its rings, add and search, and the join hint isn't cut off`, async () => {
    const page = await open(engine, 1100);
    const nav = page.getByRole('navigation');
    assert.ok((await nav.locator('.mine svg').count()) > 0);
    assert.ok(await nav.getByRole('button', { name: 'Search' }).isVisible());
    await nav.getByRole('button', { name: 'Add a list' }).click();
    const hint = nav.getByText('Or paste an invite link to join a list.');
    await hint.waitFor();
    const fits = await hint.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const side = el.closest('nav')!.getBoundingClientRect();
      return el.scrollWidth <= el.clientWidth && el.scrollHeight <= el.clientHeight && r.left >= side.left && r.right <= side.right;
    });
    assert.equal(fits, true);
    await page.close();
  });
}

for (const theme of THEMES) {
  for (const mode of ['light', 'dark']) {
    test(`${theme} ${mode}: text stands out from its background`, async () => {
      const page = await browsers[0]!.newPage({ viewport: { width: 1100, height: 800 } });
      await page.addInitScript((a) => localStorage.setItem('runer.appearance', a), JSON.stringify({ theme, mode }));
      await page.goto(`${url}?member=Anna`);
      await page.locator('nav').first().waitFor();
      const faint: Record<string, string[]> = {};
      faint.today = await page.evaluate(lowContrast, MIN_CONTRAST);
      await openList(page, 'Weekend trip');
      faint.list = await page.evaluate(lowContrast, MIN_CONTRAST);
      await page.getByRole('navigation').getByRole('button', { name: 'Settings' }).click();
      await page.getByRole('heading', { name: 'Settings' }).waitFor();
      faint.settings = await page.evaluate(lowContrast, MIN_CONTRAST);
      await page.getByRole('navigation').getByRole('button', { name: 'Help' }).click();
      await page.getByRole('heading', { name: 'Help' }).waitFor();
      faint.help = await page.evaluate(lowContrast, MIN_CONTRAST);
      assert.deepEqual(faint, { today: [], list: [], settings: [], help: [] });
      await page.close();
    });
  }
}

test('on an iPhone: the drag handles and the calendar stay on screen', async () => {
  const context = await browsers[1]!.newContext(devices['iPhone 15']);
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  await page.goto(url);
  await page.locator('.lists-tab').click();
  await openList(page, 'Weekend trip');
  const handle = (await page.locator('.grip').first().boundingBox())!;
  assert.ok(handle.x >= 0, `handle at ${handle.x}`);
  await assertNoSideways(page, 'the list');
  await page.locator('.row.task textarea').first().click();
  await page.getByRole('button', { name: /^Date:/ }).first().click();
  await assertNoSideways(page, 'the calendar');
  await context.close();
});
