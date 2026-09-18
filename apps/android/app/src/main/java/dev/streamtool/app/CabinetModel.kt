package dev.streamtool.app

import android.content.ContentResolver
import android.net.Uri
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import org.json.JSONArray
import org.json.JSONObject

data class OutputItem(val id: String, val name: String, val endpoint: String, val enabled: Boolean, val generation: Long)
data class Enrollment(val token: String, val secret: String, val qr: String, val recovery: List<String>)
data class CabinetState(
    val server: String = "", val authenticated: Boolean = false, val setupRequired: Boolean = false,
    val checked: Boolean = false, val busy: Boolean = false, val message: String = "",
    val source: JSONObject? = null, val outputs: List<OutputItem> = emptyList(),
    val media: JSONObject = JSONObject(), val slate: JSONObject = JSONObject(),
    val fallback: JSONObject = JSONObject(), val status: JSONObject = JSONObject(),
    val observedAt: Long = 0, val sourceKey: String = "", val enrollment: Enrollment? = null,
    val outputSaved: Long = 0, val replacing: Boolean = false, val installationTokenAvailable: Boolean = false
)
class CabinetModel(private val sessions: SessionStore): ViewModel() {
    var state by mutableStateOf(CabinetState(server = sessions.origin))
        private set
    private val gate = Mutex()
    private var installationToken: String? = null
    private var token = sessions.load()
    private fun client() = ApiClient(serverOrigin(state.server, BuildConfig.DEBUG), token)
    private fun action(block: suspend () -> Unit) { viewModelScope.launch { gate.withLock {
        state = state.copy(busy = true, message = "")
        try { block() } catch (e: CancellationException) { throw e } catch (e: Exception) { failure(e) }
        finally { state = state.copy(busy = false) }
    } } }
    private fun failure(e: Exception) {
        if (e is ApiError && e.status == 401 && state.authenticated && state.enrollment == null && !state.replacing) {
            sessions.clear(); token = null; state = CabinetState(server = state.server, checked = true)
        }
        state = state.copy(message = if (e is ApiError) e.message.orEmpty() else "Не удалось выполнить действие. Проверьте соединение и введённые данные.", observedAt = 0)
    }
    fun acceptInstallation(value: String, setupToken: String) = action {
        val origin = serverOrigin(value, BuildConfig.DEBUG)
        require(!state.authenticated) { "Сначала выйдите из кабинета." }
        require(setupToken.isEmpty() || java.util.Base64.getDecoder().decode(setupToken).size == 32)
        installationToken = null
        sessions.clear(); sessions.origin = origin; token = null
        state = CabinetState(server = origin, busy = true)
        reconnect() // Ordinary platform HTTPS verification precedes owner credential submission.
        if (state.setupRequired && setupToken.isNotEmpty()) {
            installationToken = setupToken
            state = state.copy(installationTokenAvailable = true)
        }
    }
    override fun onCleared() { installationToken = null; super.onCleared() }
    fun server(value: String) = action {
        val origin = serverOrigin(value, BuildConfig.DEBUG)
        require(!state.authenticated) { "Сначала выйдите из кабинета." }
        installationToken = null
        sessions.clear(); sessions.origin = origin; token = null; state = CabinetState(server = origin, busy = true)
        reconnect()
    }
    private suspend fun reconnect() {
        state = state.copy(setupRequired = client().json("/v1/auth/setup").getBoolean("required"), checked = true)
        if (!state.setupRequired) { installationToken = null; state = state.copy(installationTokenAvailable = false) }
        if (token != null) {
            client().json("/v1/me"); state = state.copy(authenticated = true); refresh()
        }
    }
    fun authenticate(password: String, factor: String, recovery: Boolean, setupToken: String) = action {
        val c = client(); val input = JSONObject().put("password", password)
        val path = when { state.setupRequired -> "/v1/auth/setup"; state.replacing -> "/v1/auth/totp/replace"; else -> "/v1/auth/login" }
        if (!state.setupRequired) input.put(if (recovery) "recovery_code" else "code", factor.trim())
        val result = c.json(path, "POST", input, if (state.setupRequired) installationToken ?: setupToken else null)
        if (path == "/v1/auth/login") acceptSession(c) else {
            val codes = result.getJSONArray("recovery_codes")
            state = state.copy(enrollment = Enrollment(result.getString("enrollment_token"), result.getString("secret"), result.getString("qr"), (0 until codes.length()).map { codes.getString(it) }))
        }
    }
    private suspend fun acceptSession(c: ApiClient) {
        val session = requireNotNull(c.issuedSession) { "Отсутствует сессия." }
        installationToken = null
        sessions.save(session.token, session.seconds); token = session.token
        state = CabinetState(server = state.server, authenticated = true, checked = true, busy = true)
        client().json("/v1/me"); refresh()
    }
    fun confirm(code: String) = action {
        val e = requireNotNull(state.enrollment); val c = client()
        c.json("/v1/auth/setup/confirm", "POST", JSONObject().put("enrollment_token", e.token).put("code", code.trim()))
        acceptSession(c)
    }
    fun restartEnrollment() { state = state.copy(enrollment = null, message = "") }
    fun replaceAuthenticator() { state = state.copy(replacing = true, enrollment = null, sourceKey = "", message = "") }
    fun cancelReplacement() { state = state.copy(replacing = false, enrollment = null, message = "") }
    fun logout() = action {
        try { client().request("/v1/auth/logout", "POST") } catch (e: ApiError) { if (e.status != 401) throw e }
        sessions.clear(); token = null; state = CabinetState(server = state.server, checked = true)
        reconnect()
    }
    fun refreshAll() = action { if (state.authenticated) refresh() else reconnect() }
    private suspend fun refresh() {
        val c = client()
        val source = try { c.json("/v1/me/source") } catch (e: ApiError) { if (e.status != 404) throw e; null }
        state = state.copy(source = source)
        if (source == null) { state = state.copy(outputs = emptyList(), observedAt = 0); return }
        val data = JSONArray(c.request("/v1/me/source/outputs"))
        val outputs = (0 until data.length()).map { data.getJSONObject(it).let { d -> OutputItem(d.getString("id"), d.getString("name"), d.getString("endpoint"), d.getBoolean("enabled"), d.getLong("generation")) } }
        state = state.copy(outputs = outputs, media = c.json("/v1/me/source/media"), slate = c.json("/v1/me/source/slate"), fallback = c.json("/v1/me/source/fallback"))
        readStatus()
    }
    private suspend fun readStatus() {
        try { state = state.copy(status = client().json("/v1/me/source/status"), observedAt = System.currentTimeMillis()) }
        catch (e: ApiError) { if (e.status != 404) throw e; state = state.copy(status = JSONObject(), observedAt = 0) }
    }
    suspend fun poll() {
        if (!state.authenticated || state.replacing || state.source == null || !gate.tryLock()) return
        try { readStatus() } catch (e: CancellationException) { throw e } catch (e: Exception) { failure(e) } finally { gate.unlock() }
    }
    fun routing(enabled: Boolean) = action {
        val result = client().json("/v1/me/source", if (enabled) "POST" else "DELETE")
        if (result.has("ingest_key")) state = state.copy(sourceKey = result.getString("ingest_key"))
        refresh()
    }
    fun hideKey() { state = state.copy(sourceKey = "") }
    fun rotateKey(password: String) = action {
        val result = client().json("/v1/me/source/credential", "POST", JSONObject().put("password", password))
        state = state.copy(sourceKey = result.getString("ingest_key"), message = "Сохраните ключ сейчас и замените его в настройках источника.")
    }
    fun stop() = action { client().json("/v1/me/source/stop", "POST"); refresh(); state = state.copy(message = "Команда завершения отправлена.") }
    fun slate(auto: Boolean, forced: Boolean) = action {
        client().json("/v1/me/source/slate", "PUT", JSONObject().put("on_source_loss", auto).put("forced", forced).put("generation", state.slate.getLong("generation"))); refresh()
    }
    fun saveOutput(item: OutputItem?, name: String, endpoint: String, secret: String, enabled: Boolean) = action {
        client().request("/v1/me/source/outputs" + (item?.let { "/${it.id}" } ?: ""), if (item == null) "POST" else "PUT",
            JSONObject().put("name", name.trim()).put("endpoint", endpoint.trim()).put("secret", secret).put("enabled", enabled).put("generation", item?.generation ?: 0)); refresh(); state = state.copy(outputSaved = state.outputSaved + 1)
    }
    fun outputAction(item: OutputItem, delete: Boolean) = action {
        client().request("/v1/me/source/outputs/${item.id}" + if (delete) "" else "/retry", if (delete) "DELETE" else "POST", JSONObject().put("generation", item.generation)); refresh()
    }
    fun media(profile: JSONObject) = action { profile.put("generation", state.media.getLong("generation")); client().json("/v1/me/source/media", "PUT", profile); refresh() }
    fun resetFallback() = action { client().request("/v1/me/source/fallback?generation=${state.fallback.optLong("generation")}", "DELETE"); refresh() }
    fun uploadFallback(resolver: ContentResolver, uri: Uri) = action {
        require(uri.scheme == "content")
        val type = resolver.getType(uri); require(type in listOf("image/png", "image/jpeg", "video/mp4"))
        // The stream remains bounded and is never copied into a saved state or preference.
        withContext(Dispatchers.IO) { requireNotNull(resolver.openInputStream(uri)).use { input ->
            client().request("/v1/me/source/fallback?generation=${state.fallback.optLong("generation")}", "PUT", upload = input, mediaType = requireNotNull(type))
        } }; refresh()
    }
}
