// UI-only verification; SQLite tests verify authoritative stop admission.
const {chromium} = require('playwright');
const assert = require('node:assert/strict');

(async () => {
  const browser = await chromium.launch({channel: 'chrome', headless: true});
  try {
    const page = await browser.newPage();
    const errors = [];
    let state = {status: 'LIVE', can_stop: false, stop_block_reason: 'SOURCE_CONNECTED', session: {input_live: true, destinations: []}};
    let statusError = false, stops = 0;
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/v1/**', async route => {
      const path = new URL(route.request().url()).pathname;
      let body = {}, status = 200;
      switch (path) {
        case '/v1/me': body = {account_id:'owner-test-account'}; break;
        case '/v1/auth/setup': body={required:false}; break;
        case '/v1/me/source': body = {enabled: true, source_id: 'ui-source', media_configured: true}; break;
        case '/v1/me/source/fallback': body=null; break;
        case '/v1/me/source/media': body={width:1280,height:720,fps_num:30,fps_den:1,video_kbps:3000,audio_kbps:160,generation:1}; break;
        case '/v1/me/source/outputs': body=[]; break;
        case '/v1/me/source/slate': body = {on_source_loss: true, forced: false, generation: 1}; break;
        case '/v1/me/source/status': body = state; if (statusError) {status = 503; body = {error: 'source state unavailable'};} break;
        case '/v1/me/source/stop': stops++; status = 202; state = {...state, can_stop: false, stop_block_reason: 'SESSION_INACTIVE', status: 'STOPPED'}; break;
        default: throw new Error('Unexpected API: ' + path);
      }
      await route.fulfill({status, contentType: 'application/json', body: JSON.stringify(body)});
    });
    await page.goto((process.env.CABINET_TEST_URL || 'http://127.0.0.1:18080') + '/dashboard');
    await page.locator('#controls').waitFor();
    const stop = page.locator('#stop');
    const refresh = () => page.evaluate(() => window.refresh());
    assert(await stop.isDisabled());
    assert.match(await page.locator('#stop-hint').innerText(), /остановите передачу/);
    state = {...state, status: 'FALLBACK', session: {...state.session, fallback_active: true, fallback_forced: true}};
    await refresh();
    assert(await stop.isDisabled(), 'Forced slate must not unlock stop for a connected source');
    state = {...state, stop_block_reason: 'SOURCE_STATE_UNKNOWN'};
    await refresh();
    assert(await stop.isDisabled());
    state = {...state, can_stop: true, stop_block_reason: ''};
    await refresh();
    assert(await stop.isEnabled());
    statusError = true;
    await refresh();
    assert(await stop.isDisabled(), 'Failed observation must revoke stop availability');
    statusError = false;
    await refresh();
    page.on('dialog', dialog => dialog.accept());
    await stop.click();
    await page.waitForFunction(() => document.getElementById('stop').disabled);
    assert.equal(stops, 1);
    assert.deepEqual(errors, []);
    console.log('PASS: live/forced/unknown source blocks stop; confirmed disconnect permits it; observation errors disable it');
  } finally {await browser.close();}
})().catch(error => {console.error(error); process.exit(1);});
