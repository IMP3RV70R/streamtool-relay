// UI contract only; SQLite tests cover concurrency, ownership and encryption.
const {chromium}=require('playwright');const assert=require('node:assert/strict');
(async()=>{const browser=await chromium.launch({channel:'chrome',headless:true});try{
 const page=await browser.newPage();let outputs=[],serial=0;const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.route('**/v1/**',async route=>{
  const r=route.request(),path=new URL(r.url()).pathname,method=r.method();let result={},status=200;
  if(path==='/v1/me')result={account_id:'owner-test-account'};
  else if(path==='/v1/auth/setup')result={required:false};
  else if(path==='/v1/me/source')result={source_id:'ui-source',enabled:true,media_configured:true,srt_url:'srt://example.invalid:8890',rtmp_url:'rtmp://example.invalid/live'};
  else if(path==='/v1/me/source/fallback')result=null;
  else if(path==='/v1/me/source/media')result={width:1280,height:720,fps_num:30,fps_den:1,video_kbps:3000,audio_kbps:160,generation:1};
  else if(path==='/v1/me/source/slate')result={on_source_loss:true,forced:false,generation:1};
  else if(path==='/v1/me/source/status')result={status:'LIVE',can_stop:false,stop_block_reason:'SOURCE_CONNECTED',session:{input_live:true,destinations:[]}};
  else if(path==='/v1/me/source/outputs'||path.startsWith('/v1/me/source/outputs/')){
   const id=path.split('/')[5],output=outputs.find(x=>x.id===id);result=outputs;
   if(method!=='GET'){
    const body=r.postDataJSON();assert.equal(body.generation,output?.generation||0);
    if(method==='POST'&&!id){assert(body.secret);const created={id:String(++serial),name:body.name,endpoint:body.endpoint,enabled:body.enabled,generation:1};outputs.push(created);status=201;result={id:created.id};}
    else if(method==='PUT'){assert.equal(body.secret,'');Object.assign(output,{name:body.name,endpoint:body.endpoint,enabled:body.enabled,generation:output.generation+1});status=204;}
    else if(method==='DELETE'){outputs=outputs.filter(x=>x.id!==id);status=204;}else{output.generation++;status=204;}
   }
  }else throw new Error('Unexpected API '+path);
  await route.fulfill({status,contentType:'application/json',body:status===204?'':JSON.stringify(result)});
 });
 await page.goto((process.env.CABINET_TEST_URL||'http://127.0.0.1:18082')+'/dashboard');await page.locator('#ingest').waitFor();
 assert.equal(await page.locator('nav a').count(),1);assert.equal(await page.locator('#channel-connect').count(),0);assert.equal(await page.locator('#title-form').count(),0);
 await page.locator('#output-name').fill('Twitch');await page.locator('#output-endpoint').fill('rtmps://example.com/live');await page.locator('#output-secret').fill('private-key');await page.locator('#output-save').click();await page.getByRole('heading',{name:'Twitch',exact:true}).waitFor();
 assert.equal(await page.locator('#output-secret').inputValue(),'');assert(await page.locator('#output-form').isVisible(),'Second output form missing');
 for(let i=1;i<8;i++){await page.locator('#output-name').fill('Destination '+i);await page.locator('#output-endpoint').fill('rtmps://example.com/live');await page.locator('#output-secret').fill('private-'+i);await page.locator('#output-save').click();await page.getByRole('heading',{name:'Destination '+i,exact:true}).waitFor();}
 assert(await page.locator('#output-form').isHidden(),'Limit does not hide creation form');
 const section=page.locator('#outputs-list section').first();await section.getByRole('button',{name:'Выключить',exact:true}).click();await section.getByRole('button',{name:'Включить',exact:true}).waitFor();assert(!outputs[0].enabled);
 await section.getByRole('button',{name:'Включить',exact:true}).click();await section.getByRole('button',{name:'Повторить подключение',exact:true}).waitFor();
 await section.getByRole('button',{name:'Редактировать',exact:true}).click();await page.locator('#output-name').fill('Renamed');await page.locator('#output-save').click();await page.getByRole('heading',{name:'Renamed',exact:true}).waitFor();
 await section.getByRole('button',{name:'Повторить подключение',exact:true}).click();await page.waitForFunction(()=>!document.querySelector('#outputs-list button').disabled);
 page.on('dialog',d=>d.accept());await section.getByRole('button',{name:'Удалить',exact:true}).click();await page.locator('#output-form').waitFor();assert.equal(outputs.length,7);assert.deepEqual(errors,[]);
 console.log('PASS: eight outputs, no OAuth/title UI, secret clearing, generation, disable/enable, retry, edit, deletion');
}finally{await browser.close()}})().catch(e=>{console.error(e);process.exit(1)});
