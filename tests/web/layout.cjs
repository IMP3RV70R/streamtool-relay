// Run with Playwright available through NODE_PATH. Requires a local test account.
const {chromium} = require('playwright');
const assert = require('node:assert/strict');

(async () => {
  const browser = await chromium.launch({channel: 'chrome', headless: true});
  try {
    const page = await browser.newPage({viewport: {width: 1440, height: 900}});
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/v1/**',async route=>{
      const path=new URL(route.request().url()).pathname;
      const bodies={
        '/v1/me':{account_id:'owner-test-account'},
        '/v1/auth/setup':{required:false},
        '/v1/me/source':{source_id:'test',enabled:true,media_configured:true},
        '/v1/me/source/fallback':null,
        '/v1/me/source/media':{width:1280,height:720,fps_num:30,fps_den:1,video_kbps:3000,audio_kbps:160,generation:1},
        '/v1/me/source/slate':{on_source_loss:true,forced:false,generation:1},
        '/v1/me/source/outputs':[],
        '/v1/me/source/status':{status:'LIVE',can_stop:false,stop_block_reason:'SOURCE_CONNECTED',session:{input_live:true,destinations:[]}},
        '/v1/auth/logout':{}
      };
      assert(Object.hasOwn(bodies,path),'Unexpected API '+path);
      await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(bodies[path])});
    });
    await page.goto((process.env.CABINET_TEST_URL || 'http://127.0.0.1:18082') + '/dashboard');
    await page.locator('nav [data-route="dashboard"]').waitFor();
    assert.equal(await page.locator('nav a').count(), 1);
    for (const removed of ['Аналитика', 'Модерация', 'Telegram', 'Название эфира', 'Подключить канал Twitch']) {
      assert(!(await page.locator('body').innerText()).includes(removed), 'Removed section remains: ' + removed);
    }
    const geometry = () => page.evaluate(() => ({
      overflow: document.querySelector('#page-scroll').scrollWidth > document.querySelector('#page-scroll').clientWidth,
      bodyOverflow: document.documentElement.scrollWidth > innerWidth,
      bodyScroll: scrollY
    }));
    for (const width of [1920, 1024, 768, 760, 390, 320]) {
      await page.setViewportSize({width, height: 844});
      const state = await geometry();
      assert(!state.overflow && !state.bodyOverflow, 'Horizontal overflow at ' + width);
      assert.equal(state.bodyScroll, 0);
    }
    await page.screenshot({path:'/tmp/streamtool-pivot-desktop.png',fullPage:true});
    await page.setViewportSize({width: 390, height: 844});
    await page.locator('#menu-toggle').click();
    await page.locator('#sidebar').waitFor({state: 'visible'});
    await page.locator('nav [data-route="dashboard"]').click();
    assert.equal(await page.locator('#menu-toggle').getAttribute('aria-expanded'), 'false');
    assert.equal(await page.locator('#sidebar').isVisible(), false);
    await page.getByRole('heading', {name: 'Подключение и защита эфира'}).waitFor();
    await page.locator('#menu-toggle').click();
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('#sidebar').isVisible(), false);
    await page.locator('#menu-toggle').click();
    await page.locator('#logout').click();
    await page.locator('#auth').waitFor();
    assert.equal(await page.locator('#menu-toggle').isVisible(), false);
    assert.deepEqual(errors, []);
    console.log('PASS: single-function cabinet, responsive layout, mobile menu, navigation, Escape, logout');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exit(1); });
