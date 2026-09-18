const {chromium}=require('playwright');const assert=require('node:assert/strict');
(async()=>{const browser=await chromium.launch({channel:'chrome',headless:true});try{
 const page=await browser.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));let owner=false,attempts=0,confirmations=0;
 await page.route('**/v1/**',async route=>{
  const r=route.request(),p=new URL(r.url()).pathname;let status=200,body={},headers={};
  if(p==='/v1/auth/setup'&&r.method()==='GET')body={required:!owner};
  else if(p==='/v1/me'){if(!owner){status=401;body={error:'unauthorized'};}else body={account_id:'owner-test-account'};}
  else if(p==='/v1/auth/setup'){
   attempts++;assert.equal(r.headers()['x-streamtool'],'1');assert.equal(r.headers()['x-setup-token'],'installation-code');
   assert.deepEqual(r.postDataJSON(),{password:'owner-password-long-enough'});
   if(attempts===1){status=429;body={error:'too many attempts'};headers={'Retry-After':'60'};}
   else body={enrollment_token:'private-enrollment',secret:'TESTSECRET',qr:'data:image/png;base64,iVBORw0KGgo=',recovery_codes:['ONE-TIME-CODE','AAAAA-BBBBB-CCCCC-DDDDD-EEEEEE']};
  }else if(p==='/v1/auth/setup/confirm'){
   confirmations++;assert.deepEqual(r.postDataJSON(),{enrollment_token:'private-enrollment',code:confirmations===1?'000000':'123456'});
   if(confirmations===1){status=401;body={error:'invalid enrollment or code'};}else{owner=true;body={authenticated:true};}
  }else if(p==='/v1/me/source'){status=404;body={error:'source not found'};}
  else throw new Error('Unexpected API '+p);
  await route.fulfill({status,headers,contentType:'application/json',body:JSON.stringify(body)});
 });
 await page.goto((process.env.CABINET_TEST_URL||'http://127.0.0.1:18082')+'/dashboard');
 await page.locator('#setup-token').waitFor();assert.equal(await page.locator('#email').count(),0);
 await page.fill('#password','owner-password-long-enough');await page.fill('#setup-token','installation-code');await page.click('#submit-auth');
 await page.locator('#notice').filter({hasText:'Попробуйте через 1 мин.'}).waitFor();assert.equal(owner,false);
 await page.click('#submit-auth');await page.locator('#totp-enrollment').waitFor();
 assert.equal(owner,false);assert.equal(await page.locator('#cabinet').isVisible(),false);assert.equal(await page.locator('#password').inputValue(),'');assert.equal(await page.locator('#setup-token').inputValue(),'');
 await page.evaluate(() => { window.copiedSecret = ''; Object.defineProperty(navigator, 'clipboard', {value:{writeText: async value => {window.copiedSecret = value;}}}); });
 await page.click('#totp-copy'); assert.equal(await page.evaluate(() => window.copiedSecret),'TESTSECRET');
 assert.equal(await page.locator('#routing-enabled').count(),0);
 assert.equal(await page.locator('#totp-secret').innerText(),'TESTSECRET');assert.match(await page.locator('#recovery-codes').innerText(),/ONE-TIME-CODE/);
 await page.setViewportSize({width:320,height:844});assert(await page.evaluate(()=>document.querySelector('#page-scroll').scrollWidth===document.querySelector('#page-scroll').clientWidth),'Enrollment overflows mobile viewport');
 await page.fill('#enroll-code','000000');await page.click('#confirm-enrollment');assert.equal(confirmations,0);
 await page.check('#recovery-saved');await page.click('#confirm-enrollment');await page.locator('#notice').filter({hasText:'Не удалось подтвердить'}).waitFor();assert.equal(await page.locator('#totp-enrollment').isVisible(),true);
 await page.fill('#enroll-code','123456');await page.click('#confirm-enrollment');await page.locator('#cabinet').waitFor();
 assert.equal(attempts,2);assert.equal(confirmations,2);assert.equal(await page.locator('#totp-secret').innerText(),'');assert.equal(await page.locator('#recovery-codes').innerText(),'');assert.equal(await page.locator('#totp-qr').getAttribute('src'),null);
 assert.equal(await page.locator('#balance-form').count(),0);assert.deepEqual(errors,[]);
 console.log('PASS: password-only identity, rate message, local QR/codes, confirmation required, invalid-code retry and secret clearing');
}finally{await browser.close();}})().catch(e=>{console.error(e);process.exitCode=1;});
