const {chromium}=require('playwright');const assert=require('node:assert/strict');
(async()=>{const browser=await chromium.launch({channel:'chrome',headless:true});try{
 const page=await browser.newPage();let logged=false,login=0,replace=0;const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.route('**/v1/**',async route=>{
  const r=route.request(),p=new URL(r.url()).pathname;let status=200,body={};
  if(p==='/v1/auth/setup')body={required:false};
  else if(p==='/v1/me'){if(logged)body={account_id:'owner-test-account'};else{status=401;body={error:'unauthorized'};}}
  else if(p==='/v1/me/source'){status=404;body={error:'source not found'};}
  else if(p==='/v1/auth/login'){
   login++;assert.equal(r.headers()['x-streamtool'],'1');assert.equal(r.headers()['x-setup-token'],undefined);
   assert.deepEqual(r.postDataJSON(),login===1?{password:'owner-password-long-enough',code:'000000'}:{password:'owner-password-long-enough',recovery_code:'RECOVERY-CODE'});
   if(login===1){status=401;body={error:'invalid password or code'};}else{logged=true;body={authenticated:true};}
  }else if(p==='/v1/auth/totp/replace'){
   replace++;assert.deepEqual(r.postDataJSON(),{password:'owner-password-long-enough',code:'123456'});
   body={enrollment_token:'replacement-token',secret:'NEWSECRET',qr:'data:image/png;base64,iVBORw0KGgo=',recovery_codes:['NEW-RECOVERY-CODE']};
  }else if(p==='/v1/auth/setup/confirm'){
   assert.deepEqual(r.postDataJSON(),{enrollment_token:'replacement-token',code:'654321'});logged=true;body={authenticated:true};
  }else throw new Error('Unexpected API '+p);
  await route.fulfill({status,contentType:'application/json',body:JSON.stringify(body)});
 });
 await page.goto((process.env.CABINET_TEST_URL||'http://127.0.0.1:18082')+'/dashboard');
 await page.waitForFunction(()=>!document.querySelector('#submit-auth').disabled);
 assert.equal(await page.locator('#setup-token-label').isVisible(),false);assert.equal(await page.locator('#email').count(),0);
 await page.fill('#password','owner-password-long-enough');await page.fill('#factor','000000');await page.click('#submit-auth');
 await page.locator('#notice').filter({hasText:'Неверный пароль'}).waitFor();assert.equal(logged,false);
 await page.selectOption('#factor-kind','recovery');await page.fill('#password','owner-password-long-enough');await page.fill('#factor','RECOVERY-CODE');await page.click('#submit-auth');
 await page.locator('#cabinet').waitFor();assert.equal(await page.locator('#factor').inputValue(),'');assert.equal(await page.locator('#password').inputValue(),'');
 await page.click('#replace-totp');await page.selectOption('#factor-kind','totp');await page.fill('#password','owner-password-long-enough');await page.fill('#factor','123456');await page.click('#submit-auth');
 await page.locator('#totp-enrollment').waitFor();assert.equal(await page.locator('#totp-secret').innerText(),'NEWSECRET');assert.equal(await page.locator('#cabinet').isVisible(),false);
 await page.check('#recovery-saved');await page.fill('#enroll-code','654321');await page.click('#confirm-enrollment');await page.locator('#cabinet').waitFor();
 assert.equal(replace,1);assert.equal(await page.locator('#totp-secret').innerText(),'');assert.equal(await page.locator('#recovery-codes').innerText(),'');assert.deepEqual(errors,[]);
 console.log('PASS: password+TOTP rejection, recovery login, protected authenticator replacement and secret clearing');
}finally{await browser.close();}})().catch(e=>{console.error(e);process.exitCode=1;});
