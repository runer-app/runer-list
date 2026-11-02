import { JSDOM } from 'jsdom';
import { chromium } from 'playwright';
import { describe, expect, test } from 'vitest';
import { lowContrast } from '../packages/ui/preview/contrast.js';
import { server } from './device.js';

// The real server's /join page, with its script run: an invite link's secret is after the #, so only the page sees it.
async function open(hash: string): Promise<Document> {
  const html = await (await fetch(`${server.url}/join`)).text();
  return new JSDOM(html, { url: `${server.url}/join${hash}`, runScripts: 'dangerously' }).window.document;
}

describe('the /join page', () => {
  test('an invite link offers to open the app, with the server and the secret', async () => {
    const page = await open('#secret');
    expect(page.querySelector('h1')!.textContent).toBe("You're invited to a list");
    expect(page.querySelector('#open')!.textContent).toBe('Open the invite');
    expect(page.querySelector('#open')!.getAttribute('href')).toBe(`runer://join?host=${encodeURIComponent(server.url)}#secret`);
    expect(page.querySelector<HTMLElement>('#incomplete')!.hidden).toBe(true);
  });

  test('with nothing after /join it says the link is incomplete and shows no button', async () => {
    const page = await open('');
    expect(page.querySelector<HTMLElement>('#invite')!.hidden).toBe(true);
    expect(page.querySelector<HTMLElement>('#incomplete')!.hidden).toBe(false);
    expect(page.querySelector('#incomplete')!.textContent).toMatch(/incomplete/);
  });

  test.each(['light', 'dark'] as const)('reads well in %s mode, with an invite and without', async (colorScheme) => {
    const browser = await chromium.launch();
    try {
      const page = await browser.newPage({ colorScheme });
      await page.goto(`${server.url}/join`);
      expect(await page.evaluate(lowContrast, 4.5)).toEqual([]);
      // Without its script: with one, an invite leaves for runer://, which a browser here can't open.
      const html = await (await fetch(`${server.url}/join`)).text();
      await page.setContent(html.replace(/<script>[\s\S]*<\/script>/, ''));
      expect(await page.getByRole('link', { name: 'Open the invite' }).isVisible()).toBe(true);
      expect(await page.evaluate(lowContrast, 4.5)).toEqual([]);
    } finally {
      await browser.close();
    }
  });
});
