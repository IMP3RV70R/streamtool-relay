package dev.streamtool.app

import android.content.ClipData
import android.content.ClipboardManager
import android.graphics.BitmapFactory
import android.os.Build
import android.os.Bundle
import android.os.PersistableBundle
import android.util.Base64
import android.view.WindowManager
import androidx.activity.compose.BackHandler
import androidx.activity.enableEdgeToEdge
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.ViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject

class MainActivity: ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        window.addFlags(WindowManager.LayoutParams.FLAG_SECURE)
        val model = ViewModelProvider(this, object: ViewModelProvider.Factory {
            @Suppress("UNCHECKED_CAST")
            override fun <T: ViewModel> create(modelClass: Class<T>): T = CabinetModel(SessionStore(applicationContext)) as T
        })[CabinetModel::class.java]
        val installation = ViewModelProvider(this, object: ViewModelProvider.Factory {
            @Suppress("UNCHECKED_CAST")
            override fun <T: ViewModel> create(modelClass: Class<T>): T = InstallationModel(applicationContext) as T
        })[InstallationModel::class.java]
        lifecycleScope.launch { repeatOnLifecycle(Lifecycle.State.STARTED) { while (true) { model.poll(); delay(5000) } } }
        setContent {
            val dark = isSystemInDarkTheme()
            val colors = if (Build.VERSION.SDK_INT >= 31) {
                if (dark) dynamicDarkColorScheme(this) else dynamicLightColorScheme(this)
            } else if (dark) darkColorScheme() else lightColorScheme()
            MaterialTheme(colorScheme = colors) { Cabinet(model, installation) }
        }
    }
}
@Composable
private fun Field(label: String, value: String, change: (String) -> Unit, secret: Boolean = false, numeric: Boolean = false) {
    OutlinedTextField(value, change, label = { Text(label) }, singleLine = true,
        visualTransformation = if (secret) PasswordVisualTransformation() else androidx.compose.ui.text.input.VisualTransformation.None,
        keyboardOptions = KeyboardOptions(keyboardType = if (numeric) KeyboardType.Number else if (secret) KeyboardType.Password else KeyboardType.Text), modifier = Modifier.fillMaxWidth())
}
@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun Cabinet(model: CabinetModel, installation: InstallationModel) {
    val state = model.state
    var screen by rememberSaveable { mutableStateOf("home") }
    val focus = LocalFocusManager.current
    fun home() { focus.clearFocus(); if (screen == "setup") installation.reset(); screen = "home" }
    BackHandler(enabled = screen != "home") { home() }
    var server by rememberSaveable(state.server) { mutableStateOf(state.server) }
    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(when { screen == "home" -> "streamtool-relay"; screen == "setup" -> "Настройка сервера"; state.authenticated -> "Кабинет"; else -> "Подключение" }) },
                navigationIcon = {
                    if (screen != "home") IconButton(onClick = { home() }) {
                        Icon(painterResource(R.drawable.ic_arrow_back), contentDescription = "Назад")
                    }
                }
            )
        }
    ) { insets ->
        key(screen) {
            Column(Modifier.fillMaxSize().padding(insets).consumeWindowInsets(insets).imePadding().verticalScroll(rememberScrollState()).padding(horizontal = 24.dp, vertical = 16.dp), verticalArrangement = Arrangement.spacedBy(16.dp)) {
                if (screen == "home") {
                    Spacer(Modifier.height(40.dp))
                    Text("Эфир под защитой", style = MaterialTheme.typography.headlineLarge)
                    Text("Ваш сервер, один источник и до восьми выходов. Подключитесь к готовому серверу или настройте новый.", style = MaterialTheme.typography.bodyLarge, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    Spacer(Modifier.height(16.dp))
                    Button(onClick = { screen = "connect"; if (state.server.isNotBlank() && !state.checked) model.refreshAll() }, modifier = Modifier.fillMaxWidth().heightIn(min = 56.dp)) { Text("Подключиться") }
                    OutlinedButton(onClick = { screen = "setup" }, modifier = Modifier.fillMaxWidth().heightIn(min = 56.dp)) { Text("Настроить") }
                    return@Column
                }
                if (screen == "setup") {
                    SshPreparation(installation) { origin, token -> model.acceptInstallation(origin, token); installation.reset(); screen = "connect" }
                    return@Column
                }
                if (state.busy) LinearProgressIndicator(Modifier.fillMaxWidth())
                if (state.message.isNotBlank()) Text(state.message, modifier = Modifier.testTag("notice"))
                if (!state.authenticated && state.enrollment == null) {
                    Text("Подключение к вашему серверу", style = MaterialTheme.typography.titleLarge)
                    Field("HTTPS-адрес сервера", server, { server = it })
                    Button(onClick = { model.server(server) }, enabled = !state.busy && server.isNotBlank()) { Text("Подключить сервер") }
                }
                if (state.checked && (!state.authenticated || state.replacing)) {
                    if (state.enrollment != null) Enroll(model, state.enrollment, state.busy) else Auth(model, state)
                    if (state.replacing) TextButton(onClick = { model.cancelReplacement() }, enabled = !state.busy) { Text("Отмена смены аутентификатора") }
                }
                if (state.authenticated && !state.replacing) {
                    Text(state.server)
                    Text("Владелец сервера")
                    TextButton(onClick = { model.refreshAll() }, enabled = !state.busy) { Text("Обновить состояние") }
                    Routing(model, state)
                    if (state.source != null) {
                        Credentials(model, state)
                        Broadcast(model, state)
                        Outputs(model, state)
                        Quality(model, state)
                        Fallback(model, state)
                    }
                    HorizontalDivider()
                    Button(onClick = { model.replaceAuthenticator() }, enabled = !state.busy) { Text("Сменить аутентификатор") }
                    TextButton(onClick = { model.logout() }, enabled = !state.busy) { Text("Выйти") }
                }
            }
        }
    }
}
@Composable
private fun SshPreparation(model: InstallationModel, connected: (String, String) -> Unit) {
    val state = model.state
    var ip by remember { mutableStateOf("") }
    var password by remember { mutableStateOf("") }
    Text("Укажите данные VDS. Параметры сервера проверим во время настройки.")
    if (state.busy) LinearProgressIndicator(Modifier.fillMaxWidth())
    if (state.message.isNotBlank()) Text(state.message)
    if (state.target == null) {
        Field("Адрес сервера", ip, { ip = it })
        Text("Например: 203.0.113.10 или 203.0.113.10:2222. По умолчанию SSH-порт 22. IPv6 с портом: [2001:db8::1]:2222.")
        Field("Пароль root для SSH", password, { password = it }, secret = true)
        Button(onClick = { model.configure(ip, password); password = "" }, enabled = !state.busy && ip.isNotBlank() && password.isNotBlank()) { Text("Продолжить") }
    } else {
        Text(state.target.identity)
        Text(state.fingerprint)
        if (state.needsTrust) {
            Text("Сверьте отпечаток SSH с данными провайдера. Пароль отправится только после подтверждения.")
            Button(onClick = { model.trust() }) { Text("Подтвердить сервер") }
        }
        if (state.trusted && !state.busy && !state.sshPasswordAvailable) {
            Field("Пароль root для SSH", password, { password = it }, secret = true)
            if (state.report == null && state.preparation == null) {
                Button(onClick = { model.configure(state.target.address, password); password = "" }, enabled = !state.busy && (password.isNotBlank() || state.sshPasswordAvailable)) { Text("Продолжить настройку") }
            }
        }
        state.report?.let { report ->
            Text("${report.getString("os")} ${report.getString("version")} · ${report.getString("architecture")}")
            Text("${report.getInt("cpus")} CPU · ${report.getLong("memory_bytes") / (1L shl 20)} МиБ RAM · ${report.getLong("disk_free_bytes") / (1L shl 30)} ГиБ свободно")
            Text(if (report.optBoolean("installation_job")) "На сервере сохранена задача установки." else if (report.getBoolean("installed")) "На сервере уже есть установка. Повторная установка не выполнялась." else "Данные сервера получены.")
            if (report.getBoolean("installed") && !report.optBoolean("installation_job")) {
                Button(onClick = { model.openCabinet(password, connected); password = "" }, enabled = !state.busy && (password.isNotBlank() || state.sshPasswordAvailable)) { Text("Открыть кабинет") }
            }
            if (!report.getBoolean("installed") && !report.optBoolean("installation_job") && model.distributionConfigured) {
                Text("Установим streamtool-relay, зависимости и системные службы. Кабинет будет работать по IP с HTTPS.")
                Button(onClick = { model.installServer(password, true, connected); password = "" }, enabled = !state.busy && (password.isNotBlank() || state.sshPasswordAvailable)) { Text("Установить") }
            } else if (!report.getBoolean("installed") && !report.optBoolean("installation_job")) {
                Text("Автоустановка станет доступна после настройки подписанных релизов GitHub. В этой сборке можно подготовить зависимости.")
                Text("Подготовка установит системные зависимости и Docker, если его нет. ОС не обновляется целиком, сервер не перезагружается. Это ещё не установка streamtool-relay.")
                Button(onClick = { model.prepareServer(password, true); password = "" }, enabled = !state.busy && (password.isNotBlank() || state.sshPasswordAvailable)) { Text("Подготовить сервер") }
            }
        }
        if (state.trusted && (state.installation != null || state.report?.optBoolean("installation_job") == true)) {
            TextButton(onClick = { model.installServer(password, false, connected); password = "" }, enabled = !state.busy && (password.isNotBlank() || state.sshPasswordAvailable)) { Text("Продолжить после переподключения") }
            if (state.installation?.optString("phase") == "FAILED" && model.distributionConfigured) {
                Button(onClick = { model.installServer(password, true, connected); password = "" }, enabled = !state.busy && (password.isNotBlank() || state.sshPasswordAvailable)) { Text("Повторить текущий этап") }
            }
        }
        state.installation?.let { job ->
            Text(when (job.getString("phase")) {
                "PENDING" -> "Установка запланирована"
                "PREPARE" -> "Подготовка зависимостей"
                "METADATA" -> "Проверка подписанного релиза"
                "DOWNLOAD" -> "Скачивание пакета"
                "STAGE" -> "Проверка файлов пакета"
                "IMAGES" -> "Загрузка компонентов"
                "CONFIGURE" -> "Настройка сервера"
                "HOST" -> "Установка системных служб"
                "START" -> "Запуск сервера"
                "READY" -> "Проверка HTTPS и компонентов"
                "SUCCEEDED" -> "Сервер установлен"
                "FAILED" -> "Установка остановлена. Сохранённый этап можно повторить."
                else -> "Установка не запускалась"
            })
        }
        if (state.trusted && !model.distributionConfigured) {
            TextButton(onClick = { model.prepareServer(password, false); password = "" }, enabled = !state.busy && (password.isNotBlank() || state.sshPasswordAvailable)) { Text("Проверить подготовку после переподключения") }
        }
        state.preparation?.let { job ->
            val phase = when (job.getString("phase")) {
                "PENDING" -> "Задача сохранена"
                "PREREQUISITES" -> "Подготовка системных зависимостей"
                "DOCKER" -> "Подготовка Docker"
                "VERIFY" -> "Проверка зависимостей"
                "PREPARED" -> "Зависимости готовы. Установка приложения ещё разрабатывается."
                "FAILED" -> "Подготовка остановлена; можно повторить после устранения причины."
                else -> "Подготовка ещё не запускалась"
            }
            Text(phase)
        }
        TextButton(onClick = { password = ""; model.reset() }, enabled = !state.busy) { Text("Другой сервер") }
    }
}
@Composable
private fun Auth(model: CabinetModel, state: CabinetState) {
    var password by remember { mutableStateOf("") }
    var factor by remember { mutableStateOf("") }
    var installation by remember { mutableStateOf("") }
    var recovery by remember { mutableStateOf(false) }
    Text(if (state.setupRequired) "Настройка владельца" else if (state.replacing) "Новый аутентификатор" else "Вход владельца", style = MaterialTheme.typography.titleLarge)
    Field("Пароль", password, { password = it }, secret = true)
    Text("Не менее 12 символов. Пароль и второй фактор нужны для входа.")
    if (state.setupRequired) {
        if (!state.installationTokenAvailable) Field("Код установки", installation, { installation = it }, secret = true)
        else Text("Сервер подключён. Создайте отдельный пароль владельца.")
        Text("Если владелец уже создан, введите его действующий пароль.")
    } else {
        Row(verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
            Checkbox(recovery, { recovery = it; factor = "" }); Text("Использовать резервный код")
        }
        Field(if (recovery) "Резервный код" else "Код аутентификатора", factor, { factor = it }, numeric = !recovery)
    }
    Button(onClick = {
        model.authenticate(password, factor, recovery, installation); password = ""; factor = ""; installation = ""
    }, enabled = !state.busy && password.toByteArray().size in 12..256 &&
        (if (state.setupRequired) state.installationTokenAvailable || installation.isNotBlank() else if (recovery) factor.isNotBlank() else factor.matches(Regex("[0-9]{6}")))) {
        Text(if (state.setupRequired || state.replacing) "Настроить аутентификатор" else "Войти")
    }
}
@Composable
private fun Enroll(model: CabinetModel, enrollment: Enrollment, busy: Boolean) {
    val context = LocalContext.current; val scope = rememberCoroutineScope()
    var code by remember(enrollment.token) { mutableStateOf("") }
    var saved by remember(enrollment.token) { mutableStateOf(false) }
    var fileError by remember { mutableStateOf("") }
    val file = rememberLauncherForActivityResult(ActivityResultContracts.CreateDocument("text/plain")) { uri ->
        if (uri != null) scope.launch {
            try { withContext(Dispatchers.IO) { requireNotNull(context.contentResolver.openOutputStream(uri, "wt")).use { it.write(("streamtool-relay — одноразовые резервные коды. Для входа нужен пароль.\n\n" + enrollment.recovery.joinToString("\n")).toByteArray()) } }; fileError = "Коды сохранены." }
            catch (_: Exception) { fileError = "Не удалось сохранить коды. Выберите другой файл." }
        }
    }
    val qr = remember(enrollment.token) { runCatching {
        require(enrollment.qr.startsWith("data:image/png;base64,"))
        val bytes = Base64.decode(enrollment.qr.substringAfter(','), Base64.DEFAULT); require(bytes.size <= 65536)
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }; BitmapFactory.decodeByteArray(bytes, 0, bytes.size, bounds)
        require(bounds.outWidth in 1..512 && bounds.outHeight in 1..512)
        requireNotNull(BitmapFactory.decodeByteArray(bytes, 0, bytes.size)).asImageBitmap()
    }.getOrNull() }
    Text("Добавьте аутентификатор", style = MaterialTheme.typography.titleLarge)
    qr?.let { Image(it, "QR-код аутентификатора", modifier = Modifier.size(240.dp)) }
    Text("Ключ для ручной настройки: ${enrollment.secret}")
    Text("Резервные коды показываются только сейчас. Сохраните их отдельно от телефона.")
    Text(enrollment.recovery.joinToString("\n"))
    TextButton(onClick = { file.launch("streamtool-relay-recovery-codes.txt") }, enabled = !busy) { Text("Сохранить резервные коды") }
    if (fileError.isNotBlank()) Text(fileError)
    Row(verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) { Checkbox(saved, { saved = it }); Text("Я сохранил резервные коды") }
    Field("Код нового аутентификатора", code, { code = it }, numeric = true)
    Button(onClick = { model.confirm(code); code = "" }, enabled = !busy && saved && code.matches(Regex("[0-9]{6}"))) { Text("Подтвердить настройку") }
    TextButton(onClick = { model.restartEnrollment() }, enabled = !busy) { Text("Начать настройку заново") }
    Text("Настройка действует 10 минут. После подтверждения дождитесь нового TOTP-кода для следующего входа.")
}
@Composable
private fun Routing(model: CabinetModel, state: CabinetState) {
    var disable by remember { mutableStateOf(false) }
    val enabled = state.source?.optBoolean("enabled") == true
    Text("Источник", style = MaterialTheme.typography.titleLarge)
    Text(if (enabled) "Маршрутизация включена" else "Маршрутизация выключена")
    Button(onClick = { if (enabled) disable = true else model.routing(true) }, enabled = !state.busy) { Text(if (enabled) "Выключить сервис" else "Включить сервис") }
    if (disable) AlertDialog(onDismissRequest = { disable = false }, title = { Text("Выключить сервис?") }, text = { Text("Текущий эфир остановится. Настройки и ключи сохранятся.") },
        confirmButton = { TextButton(onClick = { disable = false; model.routing(false) }) { Text("Выключить") } }, dismissButton = { TextButton(onClick = { disable = false }) { Text("Отмена") } })
}
@Composable
private fun Credentials(model: CabinetModel, state: CabinetState) {
    val context = LocalContext.current
    var password by remember { mutableStateOf("") }; var rotation by remember { mutableStateOf(false) }
    Text("Подключение SRT/RTMP", style = MaterialTheme.typography.titleLarge)
    Text("SRT: ${state.source?.optString("srt_url")}"); Text("RTMP: ${state.source?.optString("rtmp_url")}")
    Text("Идентификатор источника: ${state.source?.optString("source_id")}")
    if (state.sourceKey.isNotBlank()) {
        Text("Ключ источника: ${state.sourceKey}")
        TextButton(onClick = {
            val clip = ClipData.newPlainText("Ключ источника", state.sourceKey)
            clip.description.extras = PersistableBundle().apply { putBoolean("android.content.extra.IS_SENSITIVE", true) }
            context.getSystemService(ClipboardManager::class.java).setPrimaryClip(clip)
        }) { Text("Скопировать ключ") }
        TextButton(onClick = { model.hideKey() }) { Text("Скрыть ключ") }
    } else Text("Используйте сохранённый ключ: сервер не выдаёт его повторно.")
    Text("SRT streamid: publish:${state.source?.optString("source_id")}:publisher:КЛЮЧ\nRTMP: добавьте /ИСТОЧНИК?user=publisher&pass=КЛЮЧ к адресу сервера.")
    TextButton(onClick = { rotation = true }, enabled = !state.busy) { Text("Сменить ключ источника") }
    if (rotation) AlertDialog(onDismissRequest = { password = ""; rotation = false }, title = { Text("Сменить ключ источника?") }, text = { Field("Пароль владельца", password, { password = it }, secret = true) },
        confirmButton = { TextButton(onClick = { model.rotateKey(password); password = ""; rotation = false }, enabled = password.isNotBlank()) { Text("Сменить ключ") } }, dismissButton = { TextButton(onClick = { password = ""; rotation = false }) { Text("Отмена") } })
}
@Composable
private fun Broadcast(model: CabinetModel, state: CabinetState) {
    var now by remember { mutableLongStateOf(System.currentTimeMillis()) }; var stop by remember { mutableStateOf(false) }
    LaunchedEffect(Unit) { while (true) { now = System.currentTimeMillis(); delay(1000) } }
    val fresh = state.observedAt > 0 && now - state.observedAt in 0..10000
    val canStop = fresh && state.status.optBoolean("can_stop")
    Text("Эфир", style = MaterialTheme.typography.titleLarge)
    Text(if (!fresh) "Состояние уточняется" else mapOf("LIVE" to "Источник в эфире", "FALLBACK" to "Передаётся заглушка", "NO_SIGNAL" to "Нет сигнала", "STOPPED" to "Эфир завершён")[state.status.optString("status")] ?: "Ожидаем источник")
    val auto = state.slate.optBoolean("on_source_loss", true); val forced = state.slate.optBoolean("forced")
    Row(verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) { Checkbox(auto, { model.slate(it, forced) }, enabled = !state.busy); Text("Заглушка при потере сигнала") }
    Button(onClick = { model.slate(auto, !forced) }, enabled = !state.busy) { Text(if (forced) "Вернуть источник" else "Показать заглушку") }
    Button(onClick = { stop = true }, enabled = !state.busy && canStop) { Text("Завершить эфир") }
    if (!canStop) Text("Для завершения остановите передачу с источника и дождитесь подтверждения отключения.")
    if (stop) AlertDialog(onDismissRequest = { stop = false }, title = { Text("Завершить эфир?") }, text = { Text("Выходы отключатся. Новое подключение источника начнёт следующий эфир.") },
        confirmButton = { TextButton(onClick = { stop = false; model.stop() }, enabled = canStop && !state.busy) { Text("Завершить") } }, dismissButton = { TextButton(onClick = { stop = false }) { Text("Отмена") } })
}
@Composable
private fun Outputs(model: CabinetModel, state: CabinetState) {
    var editing by remember { mutableStateOf<OutputItem?>(null) }
    var name by remember { mutableStateOf("") }; var endpoint by remember { mutableStateOf("") }; var secret by remember { mutableStateOf("") }; var enabled by remember { mutableStateOf(true) }
    var deleting by remember { mutableStateOf<OutputItem?>(null) }
    LaunchedEffect(state.outputSaved) { if (state.outputSaved > 0) { editing = null; name = ""; endpoint = ""; secret = ""; enabled = true } }
    Text("Выходы: ${state.outputs.size} из 8", style = MaterialTheme.typography.titleLarge)
    Text("Общие качество и заглушка. Подключения восстанавливаются независимо.")
    state.outputs.forEach { item ->
        Card(Modifier.fillMaxWidth()) { Column(Modifier.padding(16.dp)) {
            Text(item.name); Text(item.endpoint); Text(if (item.enabled) "Включён" else "Выключен")
            val runtime = state.status.optJSONObject("session")?.optJSONArray("destinations")
            val current = runtime?.let { array -> (0 until array.length()).map { array.getJSONObject(it) }.find { it.optString("id") == item.id && it.optLong("generation") == item.generation } }
            if (current != null && state.observedAt > 0) Text("Передача: " + (mapOf("STREAMING" to "В эфире", "CONNECTING" to "Подключение", "RETRYING" to "Повтор подключения", "FAILED" to "Ошибка подключения", "STOPPED" to "Остановлен")[current.optString("state")] ?: "Состояние уточняется"))
            TextButton(onClick = { editing = item; name = item.name; endpoint = item.endpoint; secret = ""; enabled = item.enabled }, enabled = !state.busy) { Text("Редактировать") }
            TextButton(onClick = { model.saveOutput(item, item.name, item.endpoint, "", !item.enabled) }, enabled = !state.busy) { Text(if (item.enabled) "Выключить" else "Включить") }
            TextButton(onClick = { model.outputAction(item, false) }, enabled = !state.busy && item.enabled) { Text("Повторить подключение") }
            TextButton(onClick = { deleting = item }, enabled = !state.busy) { Text("Удалить") }
        } }
    }
    if (editing != null || state.outputs.size < 8) {
        Text(if (editing == null) "Добавить выход" else "Редактирование выхода")
        Field("Название выхода", name, { name = it }); Field("Адрес RTMP/RTMPS", endpoint, { endpoint = it }); Field("Ключ трансляции", secret, { secret = it }, secret = true)
        if (editing != null) Text("Пустой ключ сохраняет прежний.")
        Row(verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) { Checkbox(enabled, { enabled = it }); Text("Выход включён") }
        Button(onClick = { model.saveOutput(editing, name, endpoint, secret, enabled); secret = "" }, enabled = !state.busy && name.isNotBlank() && endpoint.isNotBlank() && (editing != null || secret.isNotBlank())) { Text("Сохранить выход") }
        TextButton(onClick = { editing = null; name = ""; endpoint = ""; secret = ""; enabled = true }) { Text("Сбросить форму") }
    }
    deleting?.let { item -> AlertDialog(onDismissRequest = { deleting = null }, title = { Text("Удалить ${item.name}?") }, text = { Text("Подключение и ключ этого выхода будут удалены.") },
        confirmButton = { TextButton(onClick = { deleting = null; model.outputAction(item, true) }) { Text("Удалить выход") } }, dismissButton = { TextButton(onClick = { deleting = null }) { Text("Отмена") } }) }
}
@Composable
private fun Quality(model: CabinetModel, state: CabinetState) {
    var expanded by remember { mutableStateOf(false) }
    TextButton(onClick = { expanded = !expanded }) { Text("Качество выхода") }
    if (expanded && state.media.has("generation")) {
        val keys = listOf("width" to "Ширина", "height" to "Высота", "fps_num" to "FPS: числитель", "fps_den" to "FPS: знаменатель", "video_kbps" to "Видео, кбит/с", "audio_kbps" to "Аудио, кбит/с")
        val values = remember(state.media.toString()) { mutableStateMapOf<String, String>().apply { keys.forEach { (key, _) -> put(key, state.media.optString(key)) } } }
        Text("По умолчанию 720p30 / 3000 кбит/с. Меняйте качество только между эфирами.")
        keys.forEach { (key, label) -> Field(label, values[key].orEmpty(), { values[key] = it }, numeric = true) }
        Button(onClick = { model.media(JSONObject().apply { keys.forEach { (key, _) -> put(key, values[key]?.toIntOrNull()) } }) }, enabled = !state.busy && values.values.all { it.toIntOrNull() != null }) { Text("Сохранить качество") }
    }
}
@Composable
private fun Fallback(model: CabinetModel, state: CabinetState) {
    val resolver = LocalContext.current.contentResolver
    val file = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocument()) { uri -> if (uri != null) model.uploadFallback(resolver, uri) }
    Text("Заглушка", style = MaterialTheme.typography.titleLarge)
    Text(when (state.fallback.optString("kind")) { "video" -> "Зацикленное видео без звука"; "image" -> "Изображение"; else -> "Стандартная заглушка" })
    Text("PNG/JPEG или MP4/H.264: 8 бит, 4:2:0, до 1080p, 30 секунд и 50 МиБ. Менять только между эфирами.")
    Button(onClick = { file.launch(arrayOf("image/png", "image/jpeg", "video/mp4")) }, enabled = !state.busy) { Text("Загрузить заглушку") }
    TextButton(onClick = { model.resetFallback() }, enabled = !state.busy && state.fallback.optString("kind") in listOf("image", "video")) { Text("Вернуть стандартную заглушку") }
}
