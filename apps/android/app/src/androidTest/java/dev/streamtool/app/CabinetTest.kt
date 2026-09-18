package dev.streamtool.app

import android.content.Context
import android.graphics.Bitmap
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.test.platform.app.InstrumentationRegistry
import androidx.lifecycle.ViewModelProvider
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import org.junit.*
import org.junit.Assert.*
import org.json.JSONArray
import org.json.JSONObject
import java.net.ServerSocket
import java.util.concurrent.CopyOnWriteArrayList
import kotlin.concurrent.thread

class CabinetTest {
    @get:Rule val ui = createEmptyComposeRule()
    private val context get() = ApplicationProvider.getApplicationContext<Context>()
    private val token = "a".repeat(43)
    @Test fun encryptedSessionIsBoundToOrigin() {
        val store = SessionStore(context); store.clear(); store.origin = "https://first.example"
        store.save(token, 600); assertEquals(token, SessionStore(context).load())
        val preferences = context.getSharedPreferences("native_session", 0)
        assertFalse(preferences.getString("session", "")!!.contains(token))
        store.origin = "https://other.example"; assertNull(store.load())
        store.save(token, -1); assertNull(store.load())
        preferences.edit().putString("session", "corrupt").commit(); assertNull(store.load()); store.clear()
    }
    @Test fun currentOwnerCabinetWorkflow() {
        FakeAPI().use { server ->
            val store = SessionStore(context); store.clear(); store.origin = server.origin
            ActivityScenario.launch(MainActivity::class.java).use {
                ui.onNodeWithText("Подключиться").performClick()
                await("Вход владельца")
                ui.onNodeWithText("Пароль").performTextInput("owner-password-long-enough")
                ui.onNodeWithText("Войти").assertIsNotEnabled()
                ui.onNodeWithText("Код аутентификатора").performTextInput("000000")
                ui.onNodeWithText("Войти").performScrollTo().performClick()
                await("Неверный пароль", substring = true)
                ui.onNodeWithText("Пароль").performTextInput("owner-password-long-enough")
                ui.onNodeWithText("Код аутентификатора").performTextInput("123456")
                ui.onNodeWithText("Войти").performScrollTo().performClick()
                await("Включить сервис")
                assertEquals(token, store.load())
                assertFalse(server.requests.any { it.contains("/native/") || it.contains("/analytics") || it.contains("/camera") })
                ui.onNodeWithText("Включить сервис").performScrollTo().performClick(); await("Ключ источника:", true)
                ui.onNodeWithText("Скрыть ключ").performScrollTo().performClick()
                for (index in 1..8) {
                    ui.onNodeWithText("Название выхода").performScrollTo().performTextReplacement("Output $index")
                    ui.onNodeWithText("Адрес RTMP/RTMPS").performScrollTo().performTextReplacement("rtmps://example.com/live")
                    ui.onNodeWithText("Ключ трансляции").performScrollTo().performTextReplacement("private-output-key")
                    ui.onNodeWithText("Сохранить выход").performScrollTo().performClick(); await("Выходы: $index из 8")
                }
                assertEquals(8, server.outputs.size)
                ui.onNodeWithText("Название выхода").assertDoesNotExist()
                ui.onNodeWithText("Завершить эфир").performScrollTo().assertIsNotEnabled()
                server.canStop = true
                ui.waitUntil(20000) { ui.onAllNodes(hasText("Завершить эфир") and isEnabled()).fetchSemanticsNodes().isNotEmpty() }
                ui.onNodeWithText("Завершить эфир").performClick(); ui.onNodeWithText("Завершить", useUnmergedTree = true).performClick()
                ui.waitUntil(15000) { server.stopped }
                ui.onNodeWithText("Выйти").performScrollTo().performClick(); await("Вход владельца")
                assertNull(store.load()); assertTrue(server.requests.any { it.startsWith("POST /v1/auth/logout") })
            }
        }
    }
    @Test fun setupCannotCreateSessionBeforeTotpConfirmation() {
        FakeAPI(setup = true).use { server ->
            val store = SessionStore(context); store.clear(); store.origin = server.origin
            ActivityScenario.launch(MainActivity::class.java).use {
                ui.onNodeWithText("Подключиться").performClick()
                await("Настройка владельца")
                ui.onNodeWithText("Пароль").performTextInput("owner-password-long-enough")
                ui.onNodeWithText("Код установки").performTextInput("private-installation-token")
                ui.onNodeWithText("Настроить аутентификатор").performScrollTo().performClick(); await("Добавьте аутентификатор")
                assertNull(store.load())
                ui.onNodeWithText("Код нового аутентификатора").performScrollTo().performTextInput("000000")
                ui.onNodeWithText("Подтвердить настройку").performScrollTo().assertIsNotEnabled()
                ui.onNode(isToggleable()).performScrollTo().performClick()
                ui.onNodeWithText("Подтвердить настройку").performClick(); await("Неверный код", true)
                ui.onNodeWithText("Код нового аутентификатора").performScrollTo().performTextInput("123456")
                ui.onNodeWithText("Подтвердить настройку").performScrollTo().performClick(); await("Включить сервис")
                assertEquals(token, store.load()); ui.onNodeWithText("Добавьте аутентификатор").assertDoesNotExist()
            }
        }
    }
    @Test fun sshHandoffSkipsManualInstallationToken() {
        FakeAPI(setup = true).use { server ->
            val store = SessionStore(context); store.clear(); store.origin = ""
            ActivityScenario.launch(MainActivity::class.java).use { scenario ->
                scenario.onActivity { activity ->
                    ViewModelProvider(activity)[CabinetModel::class.java].acceptInstallation(server.origin, "a".repeat(43) + "=")
                }
                ui.onNodeWithText("Подключиться").performClick()
                await("Настройка владельца")
                ui.onNodeWithText("Код установки").assertDoesNotExist()
                ui.onNodeWithText("Пароль").performTextInput("owner-password-long-enough")
                ui.onNodeWithText("Настроить аутентификатор").performScrollTo().assertIsEnabled().performClick()
                await("Добавьте аутентификатор")
                assertNull(store.load())
                assertTrue(server.requests.any { it.startsWith("POST /v1/auth/setup") })
            }
        }
    }
    @Test fun startScreenSeparatesConnectionAndSetup() {
        val store = SessionStore(context); store.clear(); store.origin = ""
        ActivityScenario.launch(MainActivity::class.java).use {
            ui.onNodeWithText("Подключиться").assertIsDisplayed()
            ui.onNodeWithText("Настроить").assertIsDisplayed()
            ui.onNodeWithText("HTTPS-адрес сервера").assertDoesNotExist()
            ui.onNodeWithContentDescription("Назад").assertDoesNotExist()
            capturePreview("welcome")
            ui.onNodeWithText("Настроить").performClick()
            ui.onNodeWithText("Адрес сервера").assertExists()
            ui.onNodeWithText("SSH-порт").assertDoesNotExist()
            ui.onNodeWithText("Пароль root для SSH").assertExists()
            ui.onNodeWithText("Проверить подключение").assertDoesNotExist()
            ui.onNodeWithText("Проверить VDS").assertDoesNotExist()
            ui.onNodeWithText("Продолжить").assertIsNotEnabled()
            capturePreview("setup")
            ui.onNodeWithContentDescription("Назад").assertIsDisplayed().performClick()
            ui.onNodeWithText("Подключиться").performClick()
            ui.onNodeWithText("HTTPS-адрес сервера").assertExists()
            ui.onNodeWithText("Адрес сервера").assertDoesNotExist()
        }
    }
    // Opt-in previews contain only empty forms; production screenshot protection stays enabled.
    private fun capturePreview(name: String) {
        if (InstrumentationRegistry.getArguments().getString("capturePreviews") != "true") return
        val bitmap = ui.onRoot().captureToImage().asAndroidBitmap()
        context.cacheDir.resolve("preview-$name.png").outputStream().use {
            check(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
        }
    }
    private fun await(text: String, substring: Boolean = false) { ui.waitUntil(15000) { ui.onAllNodes(hasText(text, substring = substring)).fetchSemanticsNodes().isNotEmpty() } }
}

// UI fixture only: backend tests separately verify real MFA and database authority.
private class FakeAPI(private var setup: Boolean = false): AutoCloseable {
    private val socket = ServerSocket(0)
    val origin = "http://127.0.0.1:${socket.localPort}"
    val requests = CopyOnWriteArrayList<String>()
    val outputs = CopyOnWriteArrayList<JSONObject>()
    @Volatile var canStop = false
    @Volatile var stopped = false
    private var source = false
    private val token = "a".repeat(43)
    private val worker = thread(isDaemon = true) {
        while (!socket.isClosed) {
            try { socket.accept().use { client ->
                client.soTimeout = 10000
                val input = client.getInputStream().buffered()
                fun line(): String? { val bytes = java.io.ByteArrayOutputStream(); while (true) { val b = input.read(); if (b < 0) return null; if (b == 10) break; if (b != 13) bytes.write(b) }; return bytes.toString("UTF-8") }
                val first = line() ?: return@use; val headers = mutableMapOf<String,String>()
                while (true) { val value = line() ?: break; if (value.isEmpty()) break; val index = value.indexOf(':'); headers[value.substring(0,index).lowercase()] = value.substring(index+1).trim() }
                val bytes = ByteArray(headers["content-length"]?.toInt() ?: 0); var count = 0
                while (count < bytes.size) { val n = input.read(bytes, count, bytes.size-count); if (n < 0) break; count += n }
                val body = if (bytes.isEmpty()) JSONObject() else JSONObject(String(bytes)); val parts = first.split(' '); val method = parts[0]; val path = parts[1]
                requests.add("$method $path ${headers["cookie"]}")
                var status = 200; var session = false
                val response: String = when {
                    path == "/v1/auth/setup" && method == "GET" -> JSONObject().put("required",setup).toString()
                    path == "/v1/auth/setup" -> {
                        if (headers["x-setup-token"] !in listOf("private-installation-token", "a".repeat(43) + "=") || body.has("email")) { status = 403; "{}" }
                        else JSONObject().put("enrollment_token","private-challenge").put("secret","MANUALSECRET").put("qr","").put("recovery_codes",JSONArray((1..10).map { "RECOVERY-$it" })).toString()
                    }
                    path == "/v1/auth/setup/confirm" -> {
                        if (body.optString("enrollment_token") == "private-challenge" && body.optString("code") == "123456") { setup = false; session = true; "{\"authenticated\":true}" }
                        else { status = 401; "{}" }
                    }
                    path == "/v1/auth/login" -> {
                        if (body.optString("password") == "owner-password-long-enough" && body.optString("code") == "123456" && !body.has("email")) { session = true; "{\"authenticated\":true}" }
                        else { status = 401; "{}" }
                    }
                    headers["cookie"] != "streamtool_session=$token" -> { status = 401; "{}" }
                    path == "/v1/auth/logout" -> "{}"
                    path == "/v1/me" -> "{\"account_id\":\"owner\"}"
                    path == "/v1/me/source" -> {
                        if (method == "POST") source = true
                        if (!source) { status = 404; "{}" } else JSONObject().put("source_id","source").put("enabled",true).put("media_configured",true).put("srt_url","srt://example.com:8890").put("rtmp_url","rtmp://example.com/live").apply { if (method == "POST") put("ingest_key","private-source-key") }.toString()
                    }
                    path == "/v1/me/source/outputs" -> {
                        if (method == "POST") { val d = JSONObject(body.toString()); d.remove("secret"); d.put("id","out-${outputs.size}").put("generation",1); outputs.add(d); "{}" } else JSONArray(outputs).toString()
                    }
                    path == "/v1/me/source/media" -> "{\"width\":1280,\"height\":720,\"fps_num\":30,\"fps_den\":1,\"video_kbps\":3000,\"audio_kbps\":160,\"generation\":1}"
                    path == "/v1/me/source/slate" -> "{\"on_source_loss\":true,\"forced\":false,\"generation\":1}"
                    path == "/v1/me/source/fallback" -> "null"
                    path == "/v1/me/source/status" -> JSONObject().put("status",if (stopped) "STOPPED" else "LIVE").put("can_stop",canStop && !stopped).put("session",JSONObject().put("destinations",JSONArray())).toString()
                    path == "/v1/me/source/stop" -> { stopped = true; "{}" }
                    else -> { status = 404; "{}" }
                }
                val data = response.toByteArray()
                val cookie = if (session) "Set-Cookie: streamtool_session=$token; Path=/; Max-Age=604800; HttpOnly; SameSite=Strict\r\n" else ""
                client.getOutputStream().apply { write("HTTP/1.1 $status OK\r\nContent-Type: application/json\r\nContent-Length: ${data.size}\r\n${cookie}Connection: close\r\n\r\n".toByteArray()); write(data); flush() }
            } } catch (_: Exception) { if (socket.isClosed) break }
        }
    }
    override fun close() { socket.close(); worker.join(2000) }
}
