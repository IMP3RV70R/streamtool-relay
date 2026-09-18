const {chromium}=require('playwright');const assert=require('node:assert/strict');
(async()=>{const browser=await chromium.launch({channel:'chrome',headless:true});try{
 const page=await browser.newPage();let asset=null,active=false;const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.route('**/v1/**',async route=>{const req=route.request(),url=new URL(req.url()),p=url.pathname;let body={},status=200;
 if(p==='/v1/me')body={account_id:'owner-test-account'};
 else if(p==='/v1/auth/setup')body={required:false};
 else if(p==='/v1/me/source')body={source_id:'source',enabled:true,media_configured:true,srt_url:'srt://example.invalid',rtmp_url:'rtmp://example.invalid'};
 else if(p==='/v1/me/source/media')body={width:1280,height:720,fps_num:30,fps_den:1,video_kbps:3000,audio_kbps:160,generation:1};
 else if(p==='/v1/me/source/slate')body={on_source_loss:true,forced:false,generation:1};
 else if(p==='/v1/me/source/outputs')body=[];
 else if(p==='/v1/me/source/status')body={status:'LIVE',can_stop:false,session:{input_live:true,destinations:[]}};
 else if(p==='/v1/me/source/fallback'){
  body=asset;
  if(req.method()!=='GET'){
   assert.equal(req.headers()['x-streamtool'],'1');assert.equal(Number(url.searchParams.get('generation')),asset?.generation||0);
   if(active){status=409;body={error:'stop and disconnect source before changing fallback'};}
   else if(req.method()==='PUT'){assert.equal(req.headers()['content-type'],'video/mp4');assert(req.postDataBuffer().equals(Buffer.from('ui movie')));asset={kind:'video',bytes:8,generation:(asset?.generation||0)+1};status=204;}
   else {asset={kind:'default',bytes:0,generation:asset.generation+1};status=204;}
  }
 }else throw new Error('Unexpected API '+p);
 await route.fulfill({status,contentType:'application/json',body:status===204?'':JSON.stringify(body)});
 });
 await page.goto((process.env.CABINET_TEST_URL||'http://127.0.0.1:18080')+'/dashboard');
 await page.waitForFunction(()=>document.getElementById('fallback-file')&&!document.getElementById('cabinet').hidden);
 const upload=async()=>{await page.locator('#fallback-file').setInputFiles({name:'loop.mp4',mimeType:'video/mp4',buffer:Buffer.from('ui movie')});await page.click('#fallback-save');};
 await upload();await page.waitForFunction(()=>document.getElementById('fallback-info').textContent.includes('Зацикленное видео'));assert.equal(await page.locator('#fallback-file').inputValue(),'');
 await page.click('#fallback-delete');await page.waitForFunction(()=>document.getElementById('fallback-delete').hidden);assert.equal(asset.generation,2);
 active=true;await upload();await page.waitForFunction(()=>document.getElementById('notice').textContent.includes('Сначала завершите эфир'));assert.equal(asset.kind,'default');assert.equal(asset.generation,2);
 active=false;await upload();await page.waitForFunction(()=>document.getElementById('fallback-info').textContent.includes('Зацикленное видео'));assert.equal(asset.generation,3);assert.deepEqual(errors,[]);
 console.log('PASS: binary video upload, file clearing, default fallback, durable generation and active-stream conflict');
}finally{await browser.close();}})().catch(e=>{console.error(e);process.exitCode=1;});
