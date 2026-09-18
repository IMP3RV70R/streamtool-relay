package dev.streamtool.app

import java.io.ByteArrayOutputStream
import java.io.InputStream
import java.net.HttpCookie
import java.net.HttpURLConnection
import java.net.URI
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.json.JSONObject

class ApiError(val status: Int, message: String): Exception(message)
fun serverOrigin(raw: String, debug: Boolean): String {
    val uri = URI(raw.trim())
    val local = debug && uri.scheme == "http" && uri.host in setOf("10.0.2.2", "127.0.0.1", "localhost")
    require((uri.scheme == "https" || local) && !uri.host.isNullOrBlank() && uri.rawUserInfo == null &&
        uri.rawQuery == null && uri.rawFragment == null && uri.path in listOf("", "/") && (uri.port == -1 || uri.port in 1..65535))
    return URI(uri.scheme, null, uri.host.lowercase(), uri.port, null, null, null).toString()
}
data class OwnerSession(val token: String, val seconds: Long)
fun ownerSession(headers: List<String>, origin: String): OwnerSession? {
    val host = URI(origin).host
    val cookies = headers.flatMap { HttpCookie.parse(it) }.filter { it.name == "streamtool_session" }
    require(cookies.size <= 1)
    val cookie = cookies.singleOrNull() ?: return null
    require(cookie.value.matches(Regex("[A-Za-z0-9_-]{43}")) && cookie.maxAge in 1..604800 && cookie.isHttpOnly &&
        cookie.path == "/" && (cookie.domain == null || cookie.domain.equals(host, true)) &&
        (URI(origin).scheme != "https" || cookie.secure))
    return OwnerSession(cookie.value, cookie.maxAge)
}
class ApiClient(private val origin: String, private val token: String? = null) {
    var issuedSession: OwnerSession? = null
        private set
    suspend fun request(path: String, method: String = "GET", body: JSONObject? = null,
                        setupToken: String? = null, upload: InputStream? = null,
                        mediaType: String = "application/json"): String = withContext(Dispatchers.IO) {
        require(path.startsWith("/v1/") && !path.contains('#'))
        val connection = URI(origin + path).toURL().openConnection() as HttpURLConnection
        try {
            connection.instanceFollowRedirects = false
            connection.connectTimeout = 10_000; connection.readTimeout = 15_000
            connection.requestMethod = method
            connection.setRequestProperty("Accept", "application/json")
            connection.setRequestProperty("Content-Type", mediaType)
            connection.setRequestProperty("X-Streamtool", "1")
            token?.let { connection.setRequestProperty("Cookie", "streamtool_session=$it") }
            setupToken?.let { require(path == "/v1/auth/setup"); connection.setRequestProperty("X-Setup-Token", it) }
            if (method != "GET") {
                connection.doOutput = true
                if (upload != null) connection.setChunkedStreamingMode(65536)
                connection.outputStream.use { output ->
                    if (upload == null) output.write((body ?: JSONObject()).toString().toByteArray())
                    else {
                        val buffer = ByteArray(65536); var total = 0L
                        while (true) { val count = upload.read(buffer); if (count < 0) break
                            total += count; require(total <= 50L * 1024 * 1024) { "Файл превышает 50 МиБ." }; output.write(buffer, 0, count)
                        }
                    }
                }
            }
            val status = connection.responseCode
            val stream = if (status in 200..299) connection.inputStream else connection.errorStream
            val data = stream?.use { input ->
                val output = ByteArrayOutputStream(); val buffer = ByteArray(8192)
                while (true) { val count = input.read(buffer); if (count < 0) break
                    require(output.size() + count <= 1_048_576); output.write(buffer, 0, count)
                }; output.toString("UTF-8")
            } ?: ""
            if (status !in 200..299) {
                val error = runCatching { JSONObject(data).optString("error") }.getOrDefault("")
                val wait = connection.getHeaderField("Retry-After")?.toLongOrNull()?.coerceIn(1, 900) ?: 60
                throw ApiError(status, when {
                    status == 401 && path == "/v1/auth/setup/confirm" -> "Неверный код или настройка истекла. Проверьте код либо начните заново."
                    status == 401 && path.startsWith("/v1/me") -> "Сессия истекла. Войдите снова."
                    status == 401 -> "Неверный пароль или второй фактор. Использованный TOTP-код нельзя повторять."
                    status == 429 -> "Слишком много попыток. Повторите через $wait сек."
                    error == "invalid setup token" -> "Неверный код установки."
                    error == "output limit reached or name already used" -> "Можно настроить до 8 выходов; названия должны быть разными."
                    status == 409 -> "Настройки изменились или источник ещё подключён. Обновите состояние и повторите действие."
                    status == 400 || status == 422 -> "Проверьте данные и формат файла."
                    status == 404 -> "Источник или выход не найден."
                    else -> "Сервер недоступен. Повторите попытку."
                })
            }
            if (method == "POST" && path in listOf("/v1/auth/login", "/v1/auth/setup/confirm")) {
                issuedSession = ownerSession(connection.headerFields.filterKeys { it?.equals("Set-Cookie", true) == true }.values.flatten(), origin)
            }
            data
        } finally { connection.disconnect() }
    }
    suspend fun json(path: String, method: String = "GET", body: JSONObject? = null, setupToken: String? = null): JSONObject =
        request(path, method, body, setupToken).let { if (it.isBlank() || it == "null") JSONObject() else JSONObject(it) }
}
