import {
  test,
  expect,
  type Page,
  type APIRequestContext,
} from '@playwright/test';
import { writeFile } from 'node:fs/promises';

const failures = new WeakMap<Page, string[]>();
test.beforeEach(async ({ page, request }, info) => {
  const messages: string[] = [];
  failures.set(page, messages);
  page.on('pageerror', (error) => messages.push(error.message));
  const response = await request.post('/api/v1/labs', {
    data: { name: 'UI · ' + info.title.slice(0, 85), count: 8 },
  });
  expect(response.status()).toBe(201);
  await page.goto('/');
  await expect(
    page.getByRole('button', { name: 'Run lab', exact: true })
  ).toBeVisible();
  await expect(page.locator('.subscriber-table tbody tr')).toHaveCount(8);
});
test.afterEach(async ({ page }) => {
  expect(failures.get(page)).toEqual([]);
});
async function current(page: Page, request: APIRequestContext) {
  const id = await page.getByLabel('Select lab').inputValue();
  return (await request.get('/api/v1/labs/' + id)).json();
}
async function runtime(page: Page, request: APIRequestContext) {
  const lab = await current(page, request);
  return (await request.get('/api/v1/labs/' + lab.id + '/runtime')).json();
}

test('fits the complete network and reports stopped sessions', async ({
  page,
  request,
}) => {
  await expect(page.locator('.svelte-flow__node')).toHaveCount(14);
  const allInside = () =>
    page.locator('.svelte-flow__node').evaluateAll((nodes) => {
      const canvas = document
        .querySelector('.svelte-flow')!
        .getBoundingClientRect();
      return (
        nodes.length === 14 &&
        nodes.every((node) => {
          const bounds = node.getBoundingClientRect();
          return (
            bounds.left >= canvas.left + 8 &&
            bounds.right <= canvas.right - 8 &&
            bounds.top >= canvas.top + 8 &&
            bounds.bottom <= canvas.bottom - 8
          );
        })
      );
    });
  await expect.poll(allInside).toBe(true);
  const view = await runtime(page, request);
  expect(view.phase).toBe('stopped');
  expect(view.metrics.activeSessions).toBe(0);
  expect(
    view.sessions.every((s: any) => s.status === 'stopped' && !s.address)
  ).toBe(true);
  await page.reload();
  await expect.poll(allInside).toBe(true);
  await page.setViewportSize({ width: 1280, height: 980 });
  await expect.poll(allInside).toBe(true);
  await page.setViewportSize({ width: 1536, height: 980 });
  await expect.poll(allInside).toBe(true);
  await page.screenshot({ path: '../artifacts/workspace.png' });
});

test('adds, edits, undoes and persists a device', async ({ page, request }) => {
  await page.getByTitle('Add Switch', { exact: true }).click();
  await page.getByRole('button', { name: 'Configure', exact: true }).click();
  await page
    .getByLabel('Display name', { exact: true })
    .fill('Distribution switch');
  await page.getByLabel('Display name', { exact: true }).press('Tab');
  await expect
    .poll(
      async () =>
        (await current(page, request)).nodes.find(
          (n: any) => n.kind === 'switch'
        )?.label
    )
    .toBe('Distribution switch');
  await page.getByTitle('Undo · Ctrl Z', { exact: true }).click();
  await expect
    .poll(
      async () =>
        (await current(page, request)).nodes.find(
          (n: any) => n.kind === 'switch'
        )?.label
    )
    .not.toBe('Distribution switch');
  await page.getByTitle('Undo · Ctrl Z', { exact: true }).click();
  await expect
    .poll(async () => (await current(page, request)).nodes.length)
    .toBe(14);
  await page.getByTitle('Redo · Ctrl Shift Z', { exact: true }).click();
  await expect
    .poll(async () => (await current(page, request)).nodes.length)
    .toBe(15);
  await page.reload();
  await expect(page.locator('.fleet-section h3')).toContainText('15');
});

test('drags a device and connects actual output/input handles', async ({
  page,
  request,
}) => {
  await page.getByRole('button', { name: 'New lab', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Lab name').fill('UI · cable editing');
  await dialog.getByRole('button', { name: /Blank canvas/ }).click();
  await dialog.getByRole('button', { name: 'Create lab', exact: true }).click();
  await expect(
    page.getByText('Your network starts here.', { exact: true })
  ).toBeVisible();
  await page.getByTitle('Add MikroTik', { exact: true }).click();
  await expect
    .poll(async () => (await current(page, request)).nodes.length)
    .toBe(1);
  await page.getByTitle('Add HSGQ OLT', { exact: true }).click();
  await expect
    .poll(async () => (await current(page, request)).nodes.length)
    .toBe(2);
  let lab = await current(page, request);
  const olt = lab.nodes.find((n: any) => n.kind === 'olt');
  const router = lab.nodes.find((n: any) => n.kind === 'router');
  const box = await page
    .locator('.svelte-flow__node[data-id="' + olt.id + '"] .device-node-main')
    .boundingBox();
  expect(box).toBeTruthy();
  await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2);
  await page.mouse.down();
  await page.mouse.move(
    box!.x + box!.width / 2 + 170,
    box!.y + box!.height / 2 + 170,
    { steps: 12 }
  );
  await page.mouse.up();
  await expect
    .poll(
      async () =>
        (await current(page, request)).nodes.find((n: any) => n.id === olt.id)
          .position.y
    )
    .not.toBe(olt.position.y);
  const source = page.locator(
    '.svelte-flow__node[data-id="' + router.id + '"] [data-handleid="lan1"]'
  );
  const target = page.locator(
    '.svelte-flow__node[data-id="' + olt.id + '"] [data-handleid="uplink"]'
  );
  const from = await source.boundingBox(),
    to = await target.boundingBox();
  expect(from).toBeTruthy();
  expect(to).toBeTruthy();
  await page.mouse.move(from!.x + from!.width / 2, from!.y + from!.height / 2);
  await page.mouse.down();
  await page.mouse.move(to!.x + to!.width / 2, to!.y + to!.height / 2, {
    steps: 15,
  });
  await page.mouse.up();
  await expect
    .poll(async () => (await current(page, request)).links.length)
    .toBe(1);
  lab = await current(page, request);
  expect(lab.links[0]).toMatchObject({
    source: router.id,
    target: olt.id,
    sourcePort: 'lan1',
    targetPort: 'uplink',
    medium: 'ethernet',
  });
});

test('PON faults affect one branch and repair through the inspector', async ({
  page,
  request,
}) => {
  await page
    .locator('.fleet-device')
    .filter({ hasText: 'HSGQ · access' })
    .click();
  await page.getByTitle('Disable pon1', { exact: true }).click();
  await expect
    .poll(
      async () =>
        Object.values((await runtime(page, request)).nodes).filter(
          (n: any) => n.status === 'los'
        ).length
    )
    .toBe(6);
  let view = await runtime(page, request);
  expect(view.nodes['onu-0001'].opticalUp).toBe(false);
  expect(view.nodes['onu-0005'].opticalUp).toBe(true);
  await page.getByTitle('Restore pon1', { exact: true }).click();
  await expect
    .poll(async () => (await current(page, request)).faults.length)
    .toBe(0);
  view = await runtime(page, request);
  expect(view.nodes['onu-0001'].opticalUp).toBe(true);
});

test('billing suspension preserves optical service', async ({
  page,
  request,
}) => {
  await page.getByTitle('Suspend subscriber', { exact: true }).first().click();
  await expect
    .poll(async () => (await current(page, request)).subscribers[0].enabled)
    .toBe(false);
  expect((await runtime(page, request)).nodes['onu-0001'].opticalUp).toBe(true);
  await page.getByTitle('Restore subscriber', { exact: true }).first().click();
  await expect
    .poll(async () => (await current(page, request)).subscribers[0].enabled)
    .toBe(true);
});

test('native connection details and honest runtime setup', async ({
  page,
  request,
  context,
}) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.getByRole('button', { name: 'Integration', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toContainText('10.203.0.10');
  await expect(dialog).toContainText('10.203.0.64');
  await expect(dialog).toContainText('8728');
  await expect(dialog).toContainText('10.203.0.10:8291');
  await expect(dialog).toContainText('Endpoints available when lab runs');
  const lab = await current(page, request);
  await dialog
    .getByRole('button', {
      name: 'Copy Winbox address for MikroTik · core',
      exact: true,
    })
    .click();
  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toBe('10.203.0.10:8291');
  await dialog
    .getByRole('button', {
      name: 'Copy password for MikroTik · core',
      exact: true,
    })
    .click();
  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toBe(lab.nodes[0].config.password);
  await expect(
    dialog.getByText(lab.nodes[0].config.password, { exact: true })
  ).toHaveCount(0);
  await dialog.getByRole('button', { name: 'Show credentials' }).click();
  await expect(
    dialog.getByText(lab.nodes[0].config.password, { exact: true })
  ).toBeVisible();
  await page.keyboard.press('Escape');
  await page.getByRole('button', { name: 'Run lab', exact: true }).click();
  await expect(page.getByRole('dialog')).toContainText('Network helper');
  expect((await runtime(page, request)).phase).toBe('stopped');
  await page.keyboard.press('Escape');
  await page.keyboard.press('/');
  await expect(page.getByLabel('Search devices')).toBeFocused();
});

test('helper authorization can be cancelled and retried without a terminal', async ({
  page,
  request,
}) => {
  const original = await (await request.get('/api/v1/system')).json();
  let phase: 'idle' | 'starting' | 'cancelled' | 'connected' = 'idle';
  let starts = 0;
  await page.route('**/api/v1/system', async (route) => {
    await route.fulfill({
      json: {
        ...original,
        helperOnline: phase === 'connected',
        helperLaunch: {
          available: true,
          starting: phase === 'starting',
          error:
            phase === 'cancelled'
              ? 'System authorization was cancelled. Start the helper again when ready.'
              : '',
        },
      },
    });
  });
  await page.route('**/api/v1/system/helper/start', async (route) => {
    starts++;
    phase = starts === 1 ? 'starting' : 'connected';
    await route.fulfill({ status: 202, json: { starting: true } });
  });
  await page.getByTitle('Runtime settings', { exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog
    .getByRole('button', { name: 'Start network helper', exact: true })
    .click();
  await expect(
    dialog.getByRole('button', { name: 'Starting helper…', exact: true })
  ).toBeDisabled();
  await expect(dialog).toContainText(
    'Enter your Linux account password in the system dialog.'
  );
  phase = 'cancelled';
  await expect(dialog.getByRole('alert')).toContainText(
    'authorization was cancelled'
  );
  await dialog
    .getByRole('button', { name: 'Start network helper', exact: true })
    .click();
  await expect(dialog.locator('.status-pill')).toHaveText('Connected');
  expect(starts).toBe(2);
  expect((await runtime(page, request)).phase).toBe('stopped');
});

test('connection details warn and switch when another lab owns the router IP', async ({
  page,
  request,
}) => {
  const selected = await current(page, request);
  const active = await (
    await request.post('/api/v1/labs', {
      data: { name: 'Active network', count: 8 },
    })
  ).json();
  const details = await (
    await request.get(`/api/v1/labs/${selected.id}/connections`)
  ).json();
  await page.route(
    `**/api/v1/labs/${selected.id}/connections`,
    async (route) => {
      await route.fulfill({
        json: {
          ...details,
          activeLab: { id: active.id, name: active.name, phase: 'running' },
        },
      });
    }
  );
  await page.getByRole('button', { name: 'Integration', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.locator('.connection-warning')).toContainText(
    'Active network'
  );
  await expect(dialog.locator('.connection-warning')).toContainText(
    'passwords differ'
  );
  await dialog
    .getByRole('button', { name: 'Switch to active lab', exact: true })
    .click();
  await expect(page.getByLabel('Select lab')).toHaveValue(active.id);
  await dialog
    .getByRole('button', { name: 'Show credentials', exact: true })
    .click();
  await expect(
    dialog.getByText(active.nodes[0].config.password, { exact: true })
  ).toBeVisible();
  await expect(
    dialog.getByText(selected.nodes[0].config.password, { exact: true })
  ).toHaveCount(0);
  await page.reload();
  await expect(page.getByLabel('Select lab')).toHaveValue(active.id);
});

test('invalid RADIUS settings cannot report saved', async ({
  page,
  request,
}) => {
  await page.getByTitle('Runtime settings', { exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog
    .getByRole('button', { name: 'RADIUS & events', exact: true })
    .click();
  await dialog.getByRole('button', { name: /Your RADIUS server/ }).click();
  await dialog.getByLabel('Server IPv4').fill('127.0.0.1');
  await dialog
    .getByRole('button', { name: 'Save service settings', exact: true })
    .click();
  await expect(page.getByRole('alert')).toBeVisible();
  expect((await current(page, request)).radius.mode).toBe('builtin');
  await page.getByLabel('Server IPv4').focus();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog')).toHaveCount(0);
});

test('exports and imports a reproducible snapshot', async ({
  page,
  request,
}) => {
  const original = await current(page, request);
  const downloaded = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Export', exact: true }).click();
  const file = await downloaded;
  const path = await file.path();
  expect(path).toBeTruthy();
  await page.locator('input[type=file]').setInputFiles(path!);
  await expect
    .poll(async () => (await current(page, request)).id)
    .not.toBe(original.id);
  const imported = await current(page, request);
  expect(imported.nodes).toEqual(original.nodes);
  expect(imported.subscribers).toEqual(original.subscribers);
});

test('configures GenieACS and an ONU override without inventing registration', async ({
  page,
  request,
}) => {
  await page.getByTitle('Runtime settings', { exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('button', { name: 'ONU ACS', exact: true }).click();
  await dialog.getByRole('button', { name: /Connect to ACS/ }).click();
  await dialog
    .getByLabel('ACS URL', { exact: true })
    .fill('http://192.0.2.50:7547');
  await dialog.getByLabel('ACS username', { exact: true }).fill('cpe-user');
  await dialog.getByLabel('ACS password', { exact: true }).fill('cpe-password');
  await dialog
    .getByLabel('Connection-request public URL', { exact: true })
    .fill('http://192.0.2.51:7548');
  await dialog
    .getByRole('button', { name: 'Save ACS settings', exact: true })
    .click();
  await expect
    .poll(async () => (await current(page, request)).acs?.enabled)
    .toBe(true);
  expect((await current(page, request)).acs.url).toBe('http://192.0.2.50:7547');
  await page.screenshot({ path: '../artifacts/acs-settings.png' });
  await page.keyboard.press('Escape');
  await page.locator('.subscriber-table tbody tr').first().click();
  await expect(page.locator('.inspector')).toContainText('Waiting for runtime');
  await expect(
    page.getByRole('button', { name: 'Send Inform now', exact: true })
  ).toBeDisabled();
  await page.getByRole('button', { name: 'Configure', exact: true }).click();
  await page
    .getByLabel('ACS participation', { exact: true })
    .selectOption('disabled');
  await expect
    .poll(
      async () =>
        (await current(page, request)).nodes.find(
          (n: any) => n.id === 'onu-0001'
        ).config.acs?.disabled
    )
    .toBe(true);
  const persistedID = (await current(page, request)).id;
  await page.reload();
  await expect(page.getByLabel('Select lab')).toHaveValue(persistedID);
  expect((await current(page, request)).acs.password).toBe('cpe-password');
  expect((await runtime(page, request)).acs || {}).toEqual({});
});

test('rejects an invalid ACS URL without reporting saved', async ({
  page,
  request,
}) => {
  await page.getByTitle('Runtime settings', { exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('button', { name: 'ONU ACS', exact: true }).click();
  await dialog.getByRole('button', { name: /Connect to ACS/ }).click();
  await dialog
    .getByLabel('ACS URL', { exact: true })
    .fill('file:///etc/passwd');
  await dialog
    .getByRole('button', { name: 'Save ACS settings', exact: true })
    .click();
  await expect(page.getByRole('alert')).toBeVisible();
  expect((await current(page, request)).acs?.enabled).not.toBe(true);
});

test('500 ONU workspace stays editable with real counts', async ({
  page,
  request,
}) => {
  await page.getByRole('button', { name: 'New lab', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Lab name').fill('UI · 500 ONU performance');
  await dialog.getByRole('button', { name: /Load test/ }).click();
  const start = Date.now();
  await dialog.getByRole('button', { name: 'Create lab', exact: true }).click();
  await expect(page.locator('.subscriber-table tbody tr')).toHaveCount(500);
  await page.getByLabel('Filter subscribers').fill('pelanggan0500');
  await expect(page.locator('.subscriber-table tbody tr')).toHaveCount(1);
  await page.locator('.subscriber-table tbody tr').click();
  await expect(page.locator('.inspector h2')).toHaveText('Rumah 500');
  const view = await runtime(page, request);
  expect(view.metrics).toMatchObject({
    configuredSessions: 500,
    activeSessions: 0,
  });
  const elapsed = Date.now() - start;
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('HeapProfiler.collectGarbage');
  const heap = await cdp.send('Runtime.getHeapUsage');
  await writeFile(
    '../artifacts/ui-performance.json',
    JSON.stringify(
      {
        configuredONUs: 500,
        activeSessions: 0,
        createFilterSelectMs: elapsed,
        jsHeapBytes: heap.usedSize,
      },
      null,
      2
    )
  );
  await page.screenshot({ path: '../artifacts/workspace-500.png' });
});
