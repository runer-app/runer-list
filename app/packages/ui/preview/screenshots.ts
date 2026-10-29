/**
 * The marketing screenshots (`npm run screenshots -w @runer/ui`): the design preview on the marketing data,
 * dark theme, saved to app/img/marketing/. Needs a Playwright browser (`npx playwright install chromium`).
 */
import { mkdirSync } from 'node:fs';
import { chromium } from 'playwright';
import { createServer } from 'vite';

const out = new URL('../../../img/marketing/', import.meta.url).pathname;
mkdirSync(out, { recursive: true });

const server = await createServer({ configFile: new URL('./vite.config.ts', import.meta.url).pathname });
await server.listen();
const url = `${server.resolvedUrls!.local[0]}?seed=marketing`;
const browser = await chromium.launch();
try {
  const shoot = async (file: string, width: number, height: number, steps: (p: import('playwright').Page) => Promise<void>) => {
    const page = await browser.newPage({ viewport: { width, height }, deviceScaleFactor: 2 });
    await page.addInitScript(() => localStorage.setItem('runer.appearance', JSON.stringify({ theme: 'default', mode: 'dark' })));
    await page.goto(url);
    await steps(page);
    // The member names don't fit next to the avatars at phone width; the avatars already say who.
    await page.addStyleTag({ content: '.kicker .sep, .kicker .who { display: none }' });
    await page.mouse.move(0, 0);
    await page.evaluate(async () => {
      (document.activeElement as HTMLElement | null)?.blur();
      await document.fonts.ready;
    });
    await page.screenshot({ path: out + file });
    await page.close();
  };
  await shoot('desktop-today.png', 1440, 900, async (p) => {
    await p.getByRole('button', { name: 'Today' }).first().waitFor();
  });
  await shoot('mobile-list.png', 390, 844, async (p) => {
    await p.locator('.lists-tab').click();
    await p.getByRole('button', { name: /Lisbon in May/ }).click();
    await p.getByRole('heading', { name: 'Lisbon in May' }).waitFor();
  });
} finally {
  await browser.close();
  await server.close();
}
