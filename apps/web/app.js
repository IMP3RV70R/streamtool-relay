'use strict';

const $ = id => document.getElementById(id);
let selected = '', polling = false, epoch = 0;
let sourceLoading = false, mediaConfigured = false;
let slateGeneration = 0, slateForced = false, slateSaving = false;
let outputEditing = null;
let mediaGeneration = 0, fallbackGeneration = 0;
let setupRequired = false, replacingTOTP = false, enrollmentToken = '';

const labels = {LIVE: 'В эфире', FALLBACK: 'Заглушка', NO_SIGNAL: 'Нет сигнала', DEGRADED: 'Проблема доставки', STOPPED: 'Эфир завершён', FAILED: 'Ошибка', UNKNOWN: 'Нет свежих данных', TRANSITIONING: 'Подготовка эфира'};
const errors = {
 'stop and disconnect source before changing fallback':'Сначала завершите эфир и отключите источник, затем измените заглушку.',
 'fallback changed; reload':'Заглушка уже изменена. Обновите страницу.',
 'MP4 must contain one 8-bit 4:2:0 H.264 video (Baseline/Main/High), at most 1080p and 30 seconds':'Используйте MP4 с одним видео H.264: 8 бит, 4:2:0, до 1080p и 30 секунд.',
 'invalid media profile':'Проверьте разрешение, FPS и битрейт. Максимальный размер кадра — 1920×1080 или 1080×1920, FPS — от 15 до 60.',
 'media profile changed; reload':'Профиль уже изменился. Обновите подключение и повторите.',
 'stop and disconnect source before changing media profile':'Сначала завершите эфир и отключите источник, затем измените профиль.',
  'output limit reached or name already used': 'Можно настроить до 8 выходов. Названия должны быть разными.',
  'too many changes': 'Слишком много изменений. Подождите минуту.',
  'server busy': 'Сервис занят. Повторите запрос чуть позже.',
  'stop source transmission before ending broadcast': 'Сначала остановите передачу с источника, затем завершите эфир.',
  'source disconnection has not been confirmed': 'Ожидаем подтверждение отключения источника. Попробуйте после обновления состояния.',
  'stop and disconnect source before key rotation': 'Сначала завершите эфир и остановите передачу с источника.',
  'too many attempts': 'Слишком много попыток. Попробуйте через 15 минут.',
  'password (12–256 bytes) required': 'Пароль должен содержать от 12 до 256 байт.',
  'invalid setup token': 'Неверный код установки.',
  'setup unavailable': 'Настройка защиты временно недоступна.',
  'installation already configured': 'Защита уже настроена. Обновите страницу.',
  'authentication unavailable': 'Сервис входа временно недоступен.',
  'invalid password or code': 'Неверный пароль или код. Если код уже использован, дождитесь следующего.',
  'slate settings changed; reload': 'Настройки заглушки уже изменились. Обновите страницу и повторите.',
  'invalid output endpoint': 'Проверьте адрес RTMP/RTMPS. Локальные и служебные адреса запрещены.',
  'invalid output': 'Укажите название, адрес сервера без ключа и ключ трансляции.',
  'output changed; reload': 'Выход изменился. Обновите список перед сохранением.',
  'output disabled': 'Сначала включите выход.'
};

function notice(text = '') { $('notice').textContent = text; }
async function api(path, method = 'GET', body) {
  const response = await fetch('/v1' + path, {method, credentials: 'same-origin', headers: {'Content-Type': 'application/json', 'X-Streamtool': '1', ...(path === '/auth/setup' && method === 'POST' ? {'X-Setup-Token': $('setup-token').value} : {})}, body: method === 'GET' ? undefined : JSON.stringify(body || {})});
  const value = await response.json().catch(() => ({}));
  if (!response.ok) {
    const retry = Number(response.headers.get('Retry-After'));
    const message = response.status === 429 && value.error === 'too many attempts' && retry > 0 && retry <= 900
      ? `Слишком много попыток. Попробуйте через ${Math.ceil(retry / 60)} мин.`
      : errors[value.error] || 'Не удалось выполнить запрос. Попробуйте ещё раз.';
    const error = new Error(message);
    error.status = response.status; error.code = value.error;
    throw error;
  }
  return value;
}
async function run(fn) {
  try { notice(); await fn(); }
  catch (error) {
    if (error.status === 401 && enrollmentToken) { notice('Не удалось подтвердить настройку. Проверьте код; если прошло 10 минут, начните заново.'); }
    else if (error.status === 401) { signedOut(); notice(errors[error.code] || 'Войдите снова. Проверьте пароль и второй фактор.'); }
    else notice(error.message);
  }
}
function signedOut() {
  clearEnrollment(); $('password').value = ''; $('setup-token').value = ''; $('replace-totp').hidden = true;
  $('menu-toggle').hidden = true;
  setMenu(false);
  $('sidebar').hidden = true;
  document.body.classList.remove('authenticated');
  $('page-title').textContent = 'Личный кабинет';
  $('page-description').textContent = 'Войдите, чтобы управлять своим эфиром.';
  epoch++; selected = '';
  $('cabinet').hidden = true; $('auth').hidden = false; $('logout').hidden = true;
  $('identity').textContent = ''; $('ingest-key').value = ''; $('source-password').value = ''; $('ingest').hidden = true;
  resetOutputForm(); $('outputs-list').replaceChildren();
 $('fallback-form').reset(); fallbackGeneration=0; $('fallback-info').textContent='Используется стандартная заглушка.'; $('fallback-delete').hidden=true;
}
async function boot() {
  const me = await api('/me');
  $('identity').textContent = 'Владелец сервера'; $('replace-totp').hidden = false;
  $('auth').hidden = true; $('cabinet').hidden = false; $('logout').hidden = false;
  $('sidebar').hidden = false; $('menu-toggle').hidden = false;
  document.body.classList.add('authenticated');
  showPage();
}

function renderSourceAvailability(available) {
  $('routing-state').textContent = available ? 'Источник готов. Эфир начнётся при подключении источника и включённого выхода.' : 'Подготавливаем источник…';
  document.querySelectorAll('[data-routing-active]').forEach(element => { element.hidden = !available; });
}
async function loadSource() {
  if (sourceLoading) return;
  sourceLoading = true;
  const version = epoch;
  try {
    const source = await api('/me/source', 'POST');
    if (version !== epoch || $('cabinet').hidden) return;
    renderSourceAvailability(true);
    selected = source.source_id;
    mediaConfigured = source.media_configured;
    $('ingest').hidden = false;
    const sameSource = $('ingest-id').value === source.source_id;
    $('ingest-id').value = source.source_id;
    $('ingest-key').value = source.ingest_key || (sameSource ? $('ingest-key').value : '');
    $('ingest-key').type = 'password';
    $('source-server').value = source.srt_url || 'Видеосервер ещё не настроен';
    $('source-rtmp').value = source.rtmp_url || 'RTMP-вход не настроен';
    updateSourceCopyButtons();
    $('srt-hint').textContent = 'publish:' + source.source_id + ':publisher:КЛЮЧ';
    $('source-state').textContent = !source.media_configured ? 'Приём и передача видео на этом сервере не включены.' : !source.delivery_configured ? 'Источник настроен. Добавьте или включите выход для передачи.' : 'Источник и выход настроены. Начните передачу с источника для начала эфира.';
    $('source-state').textContent += $('ingest-key').value ? ' Сохраните ключ сейчас: после перезагрузки он больше не выдаётся.' : ' Используйте сохранённый ключ. Если он потерян, смените его ниже.';
    await loadMedia(); await loadFallback(); await loadOutputs(); await refresh();
  } catch (error) {
    throw error;
  } finally { sourceLoading = false; }
}
function renderDashboardUnavailable() {
  selected = '';
  $('dashboard-empty').hidden = false;
  $('dashboard-empty-title').textContent = 'Источник ещё не подготовлен';
  $('dashboard-empty-text').textContent = 'Откройте настройки источника или повторите загрузку.';
  $('controls').hidden = true;
}
async function loadDashboard() {
  const version = epoch;
  try {
    const source = await api('/me/source');
    if (version !== epoch || $('cabinet').hidden) return;
    selected = source.source_id; mediaConfigured = source.media_configured;
    $('dashboard-empty').hidden = true; $('controls').hidden = false;
    await loadSlate(); await refresh();
  } catch (error) {
    if (error.status !== 404) throw error;
    if (version === epoch) renderDashboardUnavailable();
  }
}
function renderSlate(value) {
  slateGeneration = value.generation; slateForced = value.forced;
  $('slate-on-loss').checked = value.on_source_loss;
  $('slate-force').textContent = value.forced ? 'Вернуть источник' : 'Показать заглушку';
}
async function loadSlate() { renderSlate(await api('/me/source/slate')); }
async function saveSlate(next) {
  if (slateSaving) return;
  slateSaving = true; $('slate-force').disabled = true; $('slate-on-loss').disabled = true;
  try { renderSlate(await api('/me/source/slate', 'PUT', {...next, generation: slateGeneration})); notice('Настройки заглушки сохранены.'); await refresh(); }
  finally { slateSaving = false; $('slate-force').disabled = false; $('slate-on-loss').disabled = false; }
}
async function refresh() {
  if (!selected || polling) return;
  polling = true;
  const version = epoch;
  try {
    const result = await api('/me/source/status').then(value => ({value}), error => ({error}));
    if (version !== epoch) return;
    if (result.error?.status === 401) { signedOut(); notice('Сессия истекла. Войдите снова.'); return; }
    if (result.value) {
      const state = result.value;
      $('stop').disabled = !state.can_stop;
      $('stop-hint').textContent = state.can_stop ? 'Источник отключён. Можно завершить эфир на площадке.' : state.stop_block_reason === 'SOURCE_CONNECTED' ? 'Сначала остановите передачу с источника. Затем здесь можно завершить эфир.' : state.stop_block_reason === 'SESSION_INACTIVE' ? 'Эфир не запущен или уже завершается.' : 'Ожидаем подтверждение отключения источника. При потере связи эфир сохраняется согласно настройке заглушки.';
      $('status').textContent = labels[state.status] || state.status;
      $('status-detail').textContent = state.session.input_error === 'UNSUPPORTED_MEDIA' ? 'Источник использует неподдерживаемый кодек или параметры выше 1080p60. Исправьте настройки источника; заглушка работает по выбранной политике.' : state.session.fallback_active ? state.session.fallback_forced ? 'Заглушка показана вручную' + (state.session.input_live ? ', источник продолжает передавать сигнал.' : '.') : 'Источник недоступен. Сервер передаёт выбранную заглушку.' : state.status === 'NO_SIGNAL' ? 'Источник недоступен, автоматический показ заглушки выключен. Передаём чёрный кадр с тишиной.' : state.status === 'LIVE' ? 'Сервер передаёт видео и звук источника.' : '';
      renderOutputStates(state.session.destinations || []);
    } else {
      $('stop').disabled = true;
      $('stop-hint').textContent = result.error.status === 404 ? 'Нет активного эфира для завершения.' : 'Не удалось проверить подключение источника. Завершение пока недоступно.';
      $('status').textContent = result.error.status === 404 ? 'Ожидаем источник' : 'Статус недоступен';
      $('status-detail').textContent = result.error.status === 404 ? 'Начните передачу с источника по SRT или RTMP.' : 'Проверьте соединение.';
      renderOutputStates([]);
    }
    $('checked').textContent = 'Обновлено автоматически в ' + new Date().toLocaleTimeString();
  } finally { polling = false; }
}

function resetOutputForm() {
  outputEditing = null; $('output-form').reset(); $('output-secret').required = true;
  $('output-form-title').textContent = 'Добавить выход'; $('output-save').textContent = 'Добавить выход'; $('output-cancel').hidden = true;
}
function renderOutputStates(states = []) {
  for (const node of document.querySelectorAll('[data-output-status]')) {
    const destination = states.find(value => value.id === node.dataset.outputStatus);
    const fresh = destination && destination.generation === Number(node.dataset.generation) && Date.now() - Date.parse(destination.last_seen) < 30000;
    const names = {STREAMING: 'Передаётся', CONNECTING: 'Подключение', RECONNECTING: 'Переподключение', FAILED: 'Ошибка', STOPPED: 'Остановлен', DISABLED: 'Выключен'};
    node.textContent = node.dataset.enabled === 'false' ? 'Выключен' : fresh ? (names[destination.state] || 'Статус уточняется') : 'Нет данных о передаче';
  }
}
async function loadOutputs() {
  if (!selected) return;
  const source = selected, version = epoch, list = await api('/me/source/outputs');
  if (source !== selected || version !== epoch) return;
  if (mediaConfigured) $('source-state').textContent = (list.some(destination => destination.enabled) ? 'Источник и выход настроены. Начните передачу с источника для начала эфира.' : 'Источник настроен. Добавьте или включите выход для передачи.') + ($('ingest-key').value ? ' Сохраните ключ сейчас: после перезагрузки он больше не выдаётся.' : ' Используйте сохранённый ключ.');
  $('outputs-message').textContent = list.length + ' из 8 выходов. Общее качество и заглушка; подключения независимы.';
  $('output-form').hidden = list.length >= 8 && !outputEditing;
  $('output-form-title').hidden = list.length >= 8 && !outputEditing;
  const root = $('outputs-list'); root.replaceChildren();
  for (const destination of list) {
    const section = document.createElement('section'); section.className = 'card';
    const title = document.createElement('h3'); title.textContent = destination.name;
    const endpoint = document.createElement('p'); endpoint.textContent = destination.endpoint;
    const status = document.createElement('p'); status.dataset.outputStatus = destination.id; status.dataset.enabled = String(destination.enabled); status.dataset.generation = String(destination.generation); status.textContent = destination.enabled ? 'Нет данных о передаче' : 'Выключен';
    section.append(title, endpoint, status);
    const button = (label, action) => {
      const element = document.createElement('button'); element.type = 'button'; element.className = 'secondary'; element.textContent = label;
      element.onclick = () => run(async () => { element.disabled = true; try { await action(); } finally { element.disabled = false; } });
      section.append(element);
    };
    const path = '/me/source/outputs/' + destination.id;
    button(destination.enabled ? 'Выключить' : 'Включить', async () => { await api(path, 'PUT', {name: destination.name, endpoint: destination.endpoint, generation: destination.generation, enabled: !destination.enabled, secret: ''}); await loadOutputs(); await refresh(); });
    if (destination.enabled) button('Повторить подключение', async () => { await api(path + '/retry', 'POST', {generation: destination.generation}); await loadOutputs(); await refresh(); });
    {
      button('Редактировать', async () => { outputEditing = {...destination, source}; $('output-form').hidden = false; $('output-form-title').hidden = false; $('output-name').value = destination.name; $('output-endpoint').value = destination.endpoint; $('output-secret').value = ''; $('output-secret').required = false; $('output-enabled').checked = destination.enabled; $('output-form-title').textContent = 'Редактировать выход'; $('output-save').textContent = 'Сохранить выход'; $('output-cancel').hidden = false; $('output-name').focus(); });
      button('Удалить', async () => { if (!confirm('Удалить выход «' + destination.name + '»?')) return; await api(path, 'DELETE', {generation: destination.generation}); if (outputEditing?.id === destination.id) resetOutputForm(); await loadOutputs(); });
    }
    root.append(section);
  }
}

function showPage() {
  if ($('cabinet').hidden) return;
  if (location.pathname !== '/dashboard') history.replaceState({}, '', '/dashboard');
  document.querySelectorAll('[data-page]').forEach(element => { element.hidden = false; });
  $('page-title').textContent = 'Эфир';
  $('page-description').textContent = 'Один SRT/RTMP-источник. Заставка при потере сигнала. До 8 независимых выходов с автоматическим возвратом.';
  document.title = 'Эфир — streamtool-relay';
  run(async () => { await loadSource(); await loadDashboard(); });
}
function setMenu(open) { document.body.classList.toggle('menu-open', open); $('menu-toggle').setAttribute('aria-expanded', String(open)); $('menu-toggle').textContent = open ? 'Закрыть меню' : 'Меню'; }

async function loadMedia() {
 const version=epoch, source=selected, p=await api('/me/source/media');
 if (version!==epoch || source!==selected) return;
 mediaGeneration=p.generation;
 $('media-width').value=p.width; $('media-height').value=p.height;
 const fps=p.fps_num+'/'+p.fps_den;
 if (![...$('media-fps').options].some(o=>o.value===fps)) {const option=document.createElement('option');option.value=fps;option.textContent=String(p.fps_num/p.fps_den);$('media-fps').append(option);}
 $('media-fps').value=fps; $('media-video').value=p.video_kbps; $('media-audio').value=p.audio_kbps;
}
$('media-form').onsubmit=event=>{event.preventDefault();run(async()=>{
 const version=epoch, [fps_num,fps_den]=$('media-fps').value.split('/').map(Number);
 $('media-save').disabled=true;
 try {const p=await api('/me/source/media','PUT',{width:Number($('media-width').value),height:Number($('media-height').value),fps_num,fps_den,video_kbps:Number($('media-video').value),audio_kbps:Number($('media-audio').value),generation:mediaGeneration});if(version!==epoch)return;mediaGeneration=p.generation;notice('Профиль сохранён для следующего эфира.');}
 finally {$('media-save').disabled=false;}
});};

function clearEnrollment() {
 enrollmentToken = ''; $('totp-enrollment').hidden = true; $('auth-form').hidden = false;
 $('totp-qr').removeAttribute('src'); $('totp-secret').textContent = ''; $('recovery-codes').textContent = '';
 $('enroll-form').reset(); $('factor').value = '';
}
function renderAuthMode() {
 $('setup-token-label').hidden = !setupRequired;
 $('factor-kind-label').hidden = setupRequired; $('factor-label').hidden = setupRequired; $('factor').required = !setupRequired;
 $('submit-auth').textContent = setupRequired ? 'Настроить защиту' : replacingTOTP ? 'Подключить новый аутентификатор' : 'Войти';
 $('auth-hint').textContent = setupRequired ? 'Введите код установки и задайте пароль. Если владелец уже создан, введите его действующий пароль.' : replacingTOTP ? 'Подтвердите пароль и действующий код аутентификатора или неиспользованный резервный код. После настройки старые коды и сессии будут отозваны.' : 'Вход владельца: пароль и код аутентификатора или резервный код.';
 $('password').autocomplete = setupRequired ? 'new-password' : 'current-password';
}
$('factor-kind').onchange = () => {
 const recovery = $('factor-kind').value === 'recovery'; $('factor').value = '';
 $('factor').inputMode = recovery ? 'text' : 'numeric'; $('factor').maxLength = recovery ? 40 : 6;
 if (recovery) $('factor').removeAttribute('pattern'); else $('factor').pattern = '[0-9]{6}';
};
$('auth-form').onsubmit = event => { event.preventDefault(); run(async () => {
 $('submit-auth').disabled = true;
 try {
  const input = {password: $('password').value};
  if (!setupRequired) input[$('factor-kind').value === 'recovery' ? 'recovery_code' : 'code'] = $('factor').value.trim();
  const result = await api(setupRequired ? '/auth/setup' : replacingTOTP ? '/auth/totp/replace' : '/auth/login', 'POST', input);
  $('password').value = ''; $('factor').value = ''; $('setup-token').value = '';
  if (result.enrollment_token) {
   enrollmentToken = result.enrollment_token; $('auth-form').hidden = true; $('totp-enrollment').hidden = false;
   $('totp-qr').src = result.qr; $('totp-secret').textContent = result.secret;
   $('recovery-codes').textContent = result.recovery_codes.join('\n');
  } else { setupRequired = false; replacingTOTP = false; await boot(); }
 } finally { $('submit-auth').disabled = false; }
}); };
$('enroll-form').onsubmit = event => { event.preventDefault(); run(async () => {
 $('confirm-enrollment').disabled = true;
 try { await api('/auth/setup/confirm', 'POST', {enrollment_token: enrollmentToken, code: $('enroll-code').value});
  clearEnrollment(); setupRequired = false; replacingTOTP = false; renderAuthMode(); await boot();
 } finally { $('confirm-enrollment').disabled = false; }
}); };
$('restart-enrollment').onclick = () => { clearEnrollment(); notice(); renderAuthMode(); };
$('download-recovery').onclick = () => {
 const blob = new Blob(['streamtool-relay — одноразовые резервные коды. Пароль потребуется при входе.\n\n' + $('recovery-codes').textContent + '\n'], {type: 'text/plain;charset=utf-8'});
 const url = URL.createObjectURL(blob), link = document.createElement('a'); link.href = url; link.download = 'streamtool-relay-recovery-codes.txt'; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
};
$('replace-totp').onclick = () => { signedOut(); setupRequired = false; replacingTOTP = true; renderAuthMode(); };
$('logout').onclick = () => run(async () => { await api('/auth/logout', 'POST'); signedOut(); });
$('source-retry').onclick = () => run(() => loadSource());
$('show-key').onclick = () => { $('ingest-key').type = $('ingest-key').type === 'password' ? 'text' : 'password'; };
$('slate-on-loss').onchange = () => run(() => saveSlate({on_source_loss: $('slate-on-loss').checked, forced: slateForced}));
$('slate-force').onclick = () => run(() => saveSlate({on_source_loss: $('slate-on-loss').checked, forced: !slateForced}));
$('stop').onclick = () => { if (confirm('Завершить эфир, включая заглушку?')) run(async () => { await api('/me/source/stop', 'POST'); notice('Команда завершения отправлена.'); await refresh(); }); };
$('source-key-form').onsubmit = event => { event.preventDefault(); run(async () => { const version = epoch, password = $('source-password').value; $('source-password').value = ''; const source = await api('/me/source/credential', 'POST', {password}); if (version !== epoch || $('cabinet').hidden) return; $('ingest-key').value = source.ingest_key; $('ingest-key').type = 'password'; updateSourceCopyButtons(); notice('Ключ заменён. Сохраните его и обновите настройки источника.'); }); };
$('outputs-refresh').onclick = () => run(async () => { await loadOutputs(); await refresh(); });
$('output-cancel').onclick = () => run(async () => { resetOutputForm(); await loadOutputs(); });
$('output-form').onsubmit = event => { event.preventDefault(); run(async () => {
  const version = epoch, source = selected, editing = outputEditing;
  if (!source || (editing && editing.source !== source)) return;
  const input = {name: $('output-name').value, endpoint: $('output-endpoint').value, secret: $('output-secret').value, enabled: $('output-enabled').checked, generation: editing?.generation || 0};
  $('output-secret').value = ''; $('output-save').disabled = true;
  try { await api('/me/source/outputs' + (editing ? '/' + editing.id : ''), editing ? 'PUT' : 'POST', input); if (version !== epoch) return; resetOutputForm(); await loadOutputs(); notice('Выход сохранён.'); }
  finally { $('output-save').disabled = false; }
}); };
$('menu-toggle').onclick = () => setMenu($('menu-toggle').getAttribute('aria-expanded') !== 'true');

document.addEventListener('click', event => {
  const link = event.target.closest('a[data-route]');
  if (!link || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey || event.button !== 0) return;
  event.preventDefault(); history.pushState({}, '', link.getAttribute('href')); notice(); showPage(); setMenu(false); $('page-scroll').scrollTo(0, 0); $('page-title').focus({preventScroll: true});
});
document.addEventListener('keydown', event => { if (event.key === 'Escape' && document.body.classList.contains('menu-open')) { setMenu(false); $('menu-toggle').focus(); } });
window.addEventListener('popstate', () => { notice(); showPage(); });
window.addEventListener('focus', () => { run(refresh); });
setInterval(() => { if (!document.hidden) run(refresh); }, 5000);
$('page-title').tabIndex = -1;


async function loadFallback() {
  if (!selected) return;
  const version = epoch, source = selected;
  const value = await api('/me/source/fallback');
  if (version !== epoch || source !== selected) return;
  fallbackGeneration = value?.generation || 0;
  $('fallback-info').textContent = value && value.kind !== 'default' ? (value.kind === 'video' ? 'Зацикленное видео' : 'Изображение') + ' · ' + (value.bytes / 1048576).toFixed(1) + ' МБ · звук заменяется тишиной.' : 'Используется стандартная заглушка.';
  $('fallback-delete').hidden = !value || value.kind === 'default';
}
$('fallback-form').onsubmit = event => { event.preventDefault(); run(async () => {
  const file = $('fallback-file').files[0];
  if (!file) return;
  if (file.size > 50 * 1048576) throw new Error('Максимальный размер заглушки — 50 МБ.');
  const version = epoch, source = selected;
  const response = await fetch('/v1/me/source/fallback?generation=' + fallbackGeneration, {method:'PUT', credentials:'same-origin', headers:{'X-Streamtool':'1','Content-Type':file.type}, body:file});
  if (!response.ok) { const error = await response.json(); throw new Error(errors[error.error] || error.error || 'Не удалось загрузить заглушку.'); }
  if (version !== epoch || source !== selected) return;
  $('fallback-form').reset(); await loadFallback(); notice('Заглушка загружена. Она будет использована в следующем эфире.');
}); };
$('fallback-delete').onclick = () => run(async () => {
  await api('/me/source/fallback?generation=' + fallbackGeneration, 'DELETE');
  await loadFallback(); notice('Восстановлена стандартная заглушка.');
});

run(async () => {
 const state = await api('/auth/setup'); setupRequired = state.required;
 try { await boot(); } catch (error) { signedOut(); if (error.status !== 401) notice('Не удалось подключиться к серверу. Обновите страницу.'); }
 renderAuthMode();
 $('submit-auth').disabled = false;
});

$('totp-copy').onclick = () => run(async () => {
  const secret = $('totp-secret').textContent;
  if (!secret) return;
  if (!navigator.clipboard?.writeText) throw new Error('Копирование недоступно. Выделите ключ и скопируйте вручную.');
  await navigator.clipboard.writeText(secret);
  notice('Ключ скопирован. Добавьте его в аутентификаторе и вернитесь для подтверждения.');
});

function sourceCopyValue(name) {
  const key = $('ingest-key').value, id = $('ingest-id').value;
  if (name === 'srt-stream-id') return key && id ? 'publish:' + id + ':publisher:' + key : '';
  if (name === 'rtmp-stream-key') return key && id ? id + '?user=publisher&pass=' + encodeURIComponent(key) : '';
  const value = $(name).value;
  return /^(source-server|source-rtmp)$/.test(name) && !/^(srt|rtmp|rtmps):\/\//.test(value) ? '' : value;
}
function updateSourceCopyButtons() {
  for (const name of ['source-server','source-rtmp','ingest-id','ingest-key','srt-stream-id','rtmp-stream-key']) {
    $('copy-' + name).disabled = !sourceCopyValue(name);
  }
}
for (const name of ['source-server','source-rtmp','ingest-id','ingest-key','srt-stream-id','rtmp-stream-key']) {
  $('copy-' + name).onclick = () => run(async () => {
    const value = sourceCopyValue(name);
    if (!value) return;
    if (!navigator.clipboard?.writeText) throw new Error('Копирование недоступно. Выделите значение и скопируйте вручную.');
    await navigator.clipboard.writeText(value);
    notice('Скопировано. Вставьте значение в настройки источника.');
  });
}
