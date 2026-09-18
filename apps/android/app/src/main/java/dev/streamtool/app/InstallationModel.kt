package dev.streamtool.app

import android.content.Context
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import org.json.JSONObject
import java.util.Base64

data class InstallationState(
    val target: SshTarget? = null, val fingerprint: String = "", val needsTrust: Boolean = false,
    val previousFingerprint: String = "", val trusted: Boolean = false, val busy: Boolean = false, val message: String = "",
    val diagnostics: JSONObject? = null, val report: JSONObject? = null, val preparation: JSONObject? = null, val installation: JSONObject? = null, val sshPasswordAvailable: Boolean = false
)
class InstallationModel(context: Context): ViewModel() {
    private val app = context.applicationContext
    private val pins = context.applicationContext.getSharedPreferences("ssh_host_keys", Context.MODE_PRIVATE)
    private var pendingKey: ByteArray? = null
    private var pendingPassword: String? = null
    private var connectionPassword: String? = null
    var state by mutableStateOf(InstallationState())
        private set
    fun configure(address: String, password: String) {
        if (state.busy || password.isBlank()) return
        state = InstallationState(busy = true); pendingKey = null; pendingPassword = null; connectionPassword = null
        viewModelScope.launch {
            try {
                val target = SshTarget.parse(address)
                val saved = pins.getString(target.identity, null)
                if (saved != null) {
                    val key = Base64.getDecoder().decode(saved)
                    state = InstallationState(target, SshHostIdentity(key).fingerprint, trusted = true)
                    checkServer(password)
                } else {
                    withContext(Dispatchers.IO) { SshConnection(target, null).use { it.connect(null) } }
                }
            } catch (e: SshHostIdentity) {
                pendingKey = e.key.copyOf(); pendingPassword = password
                state = InstallationState(SshTarget.parse(address), e.fingerprint, needsTrust = true)
            } catch (_: Exception) {
                state = InstallationState(message = "Не удалось проверить SSH. Проверьте IP, порт и доступность сервера.")
            }
        }
    }
    fun trust() {
        if (state.busy || !state.needsTrust) return
        val key = pendingKey ?: return
        val target = state.target ?: return
        if (!pins.edit().putString(target.identity, Base64.getEncoder().encodeToString(key)).commit()) {
            state = state.copy(message = "Не удалось сохранить ключ сервера. Попробуйте подтвердить ещё раз.")
            return
        }
        pendingKey = null; state = state.copy(needsTrust = false, trusted = true, previousFingerprint = "", message = "")
        val password = pendingPassword; pendingPassword = null
        if (password != null) checkServer(password)
    }
    private fun confirmChangedIdentity(identity: SshIdentityChanged, password: String, waitForClose: Boolean = false) {
        val target = state.target ?: return
        val saved = pins.getString(target.identity, null)
        val previous = saved?.let { SshHostIdentity(Base64.getDecoder().decode(it)).fingerprint }.orEmpty()
        pendingKey = identity.key.copyOf()
        pendingPassword = password
        connectionPassword = null
        // The old host's installation/setup state cannot describe the replacement host.
        state = InstallationState(target = target, fingerprint = identity.fingerprint,
            previousFingerprint = previous, needsTrust = true, busy = waitForClose)
    }
    private fun checkServer(password: String) {
        if (state.busy || !state.trusted || password.isBlank()) return
        val target = state.target ?: return
        val key = Base64.getDecoder().decode(requireNotNull(pins.getString(target.identity, null)))
        state = state.copy(busy = true, report = null, message = "")
        viewModelScope.launch {
            try {
                val result = withContext(Dispatchers.IO) {
                    SshConnection(target, key).use { connection ->
                        connection.connect(password)
                        val report = JSONObject(connection.preflight())
                        if (report.optBoolean("installation_job")) {
                            val job = JSONObject(connection.installationStatus())
                            require(job.getInt("protocol") == 1 && SshTarget.parse(job.getString("address")).host == target.host)
                            report.put("job_status", job)
                        }
                        report.toString()
                    }
                }
                val report = JSONObject(result)
                require(report.getInt("protocol") == 1)
                connectionPassword = password
                state = state.copy(busy = false, report = report, installation = report.optJSONObject("job_status"), sshPasswordAvailable = true)
            } catch (e: SshIdentityChanged) {
                confirmChangedIdentity(e, password)
            } catch (_: Exception) {
                state = state.copy(busy = false, message = "SSH-проверка не завершилась. Проверьте пароль root; на сервере нужен Python 3.")
            }
        }
    }
    override fun onCleared() { pendingPassword = null; pendingKey = null; connectionPassword = null; super.onCleared() }
    fun reset() { if (!state.busy) { pendingKey = null; pendingPassword = null; connectionPassword = null; state = InstallationState() } }
    private fun takeCredential(entered: String): String? {
        val value = entered.ifBlank { connectionPassword.orEmpty() }
        connectionPassword = null
        state = state.copy(sshPasswordAvailable = false)
        return value.ifBlank { null }
    }
    fun openCabinet(password: String, connected: (String, String) -> Unit) {
        if (state.busy || !state.trusted) return
        val credential = takeCredential(password) ?: return
        val target = state.target ?: return
        val key = Base64.getDecoder().decode(requireNotNull(pins.getString(target.identity, null)))
        state = state.copy(busy = true, message = "")
        viewModelScope.launch {
            try {
                val reply = withContext(Dispatchers.IO) {
                    SshConnection(target, key).use { it.connect(credential); JSONObject(it.cabinetHandoff()) }
                }
                require(reply.getInt("protocol") == 1)
                // The trusted SSH host cannot redirect the owner's setup to another origin.
                val origin = cabinetOriginForTarget(reply.getString("origin"), target)
                val setupToken = reply.getString("setup_token")
                require(setupToken.isEmpty() || Base64.getDecoder().decode(setupToken).size == 32)
                state = state.copy(busy = false)
                connected(origin, setupToken)
            } catch (e: CancellationException) { throw e
            } catch (e: SshIdentityChanged) {
                confirmChangedIdentity(e, credential)
            } catch (_: Exception) {
                state = state.copy(busy = false, message = "Не удалось открыть установленный кабинет по IP. Проверьте завершение установки и доступность HTTPS.")
            }
        }
    }
    val distributionConfigured: Boolean get() = runCatching {
        val data = app.assets.open("distribution.json").use { it.readBytes().toString(Charsets.UTF_8) }
        JSONObject(data).has("public_key")
    }.getOrDefault(false)
    fun diagnose(password: String) {
        if (state.busy || !state.trusted) return
        val credential = takeCredential(password) ?: return
        val target = state.target ?: return
        val key = Base64.getDecoder().decode(requireNotNull(pins.getString(target.identity, null)))
        state = state.copy(busy = true, message = "")
        viewModelScope.launch {
            try {
                val result = withContext(Dispatchers.IO) {
                    SshConnection(target, key).use {
                        it.connect(credential)
                        val job = JSONObject(it.installationStatus())
                        require(job.getInt("protocol") == 1 && SshTarget.parse(job.getString("address")).host == target.host)
                        val helper = app.assets.open("installer_diagnostics.py").use { file -> file.readBytes() }
                        job to JSONObject(it.installationDiagnostics(helper))
                    }
                }
                require(result.second.getInt("protocol") == 1)
                state = state.copy(installation = result.first, diagnostics = result.second)
            } catch (e: CancellationException) { throw e
            } catch (e: SshIdentityChanged) {
                confirmChangedIdentity(e, credential, waitForClose = true)
            } catch (_: Exception) {
                state = state.copy(message = "Не удалось получить диагностику по SSH. Проверьте доступность сервера и пароль root.")
            } finally { state = state.copy(busy = false) }
        }
    }
    fun installServer(password: String, start: Boolean, connected: (String, String) -> Unit) {
        if (state.busy || !state.trusted || start && !distributionConfigured) return
        val credential = takeCredential(password) ?: return
        val target = state.target ?: return
        val key = Base64.getDecoder().decode(requireNotNull(pins.getString(target.identity, null)))
        state = state.copy(busy = true, message = "")
        viewModelScope.launch {
            val connection = SshConnection(target, key)
            try {
                withContext(Dispatchers.IO) {
                    connection.connect(credential)
                    if (start) {
                        val payload = app.assets.open("installer-payload.zip").use { it.readBytes() }
                        require(payload.size <= 33554432)
                        try { connection.install(payload) } catch (e: Exception) {
                            // A reconnect can meet the autonomous runner's exclusive
                            // lock. Attach only to an existing valid same-IP job.
                            val report = JSONObject(connection.installationStatus())
                            require(report.getInt("protocol") == 1 && SshTarget.parse(report.getString("address")).host == target.host)
                        }
                    }
                }
                val deadline = System.nanoTime() + 2_700_000_000_000L
                var observedFailure = ""
                while (System.nanoTime() < deadline) {
                    val job = JSONObject(withContext(Dispatchers.IO) { connection.installationStatus() })
                    require(job.getInt("protocol") == 1)
                    if (job.getString("phase") != "NOT_STARTED") require(SshTarget.parse(job.getString("address")).host == target.host)
                    state = state.copy(installation = job)
                    val failure = "${job.optString("resume_phase")}:${job.optInt("attempts")}:${job.optString("error")}"
                    if ((job.optString("phase") == "IMAGES" || !job.isNull("error") && job.optString("error").isNotBlank()) && failure != observedFailure) {
                        observedFailure = failure
                        val diagnostics = withContext(Dispatchers.IO) {
                            runCatching {
                                val helper = app.assets.open("installer_diagnostics.py").use { it.readBytes() }
                                JSONObject(connection.installationDiagnostics(helper))
                            }.getOrNull()
                        }
                        state = state.copy(diagnostics = diagnostics)
                    }
                    if (job.getString("phase") == "SUCCEEDED") {
                        val reply = JSONObject(withContext(Dispatchers.IO) { connection.cabinetHandoff() })
                        require(reply.getInt("protocol") == 1)
                        val origin = cabinetOriginForTarget(reply.getString("origin"), target)
                        val token = reply.getString("setup_token")
                        require(token.isEmpty() || Base64.getDecoder().decode(token).size == 32)
                        withContext(Dispatchers.IO) { connection.close() }
                        state = state.copy(busy = false)
                        connected(origin, token)
                        return@launch
                    }
                    if (job.getString("phase") in listOf("FAILED", "NOT_STARTED")) break
                    delay(5000)
                }
                if (state.installation?.optString("phase") !in listOf("FAILED", "NOT_STARTED", "SUCCEEDED")) {
                    state = state.copy(message = "Ожидание установки истекло (45 минут). Получите диагностику: состояние задачи сохранено на сервере.")
                }
            } catch (e: CancellationException) { throw e
            } catch (e: SshIdentityChanged) {
                confirmChangedIdentity(e, credential, waitForClose = true)
            } catch (_: Exception) {
                state = state.copy(message = "Связь с установкой потеряна или запуск отклонён. Если задача запущена, она продолжится на сервере. Подключитесь снова для просмотра состояния.")
            } finally {
                withContext(kotlinx.coroutines.NonCancellable + Dispatchers.IO) { connection.close() }
                state = state.copy(busy = false)
            }
        }
    }
    fun prepareServer(password: String, start: Boolean) {
        if (state.busy || !state.trusted) return
        val credential = takeCredential(password) ?: return
        val target = state.target ?: return
        val key = Base64.getDecoder().decode(requireNotNull(pins.getString(target.identity, null)))
        state = state.copy(busy = true, message = "")
        viewModelScope.launch {
            val connection = SshConnection(target, key)
            try {
                withContext(Dispatchers.IO) {
                    connection.connect(credential)
                    if (start) {
                        val helper = app.assets.open("installer.py").use { it.readBytes() }
                        require(helper.size <= 131072)
                        connection.prepare(helper)
                    }
                }
                val deadline = System.nanoTime() + 2_700_000_000_000L
                while (System.nanoTime() < deadline) {
                    val report = JSONObject(withContext(Dispatchers.IO) { connection.preparationStatus() })
                    require(report.getInt("protocol") == 1)
                    state = state.copy(preparation = report)
                    if (report.getString("phase") in listOf("PREPARED", "FAILED", "NOT_STARTED")) break
                    delay(5000)
                }
            } catch (e: CancellationException) { throw e
            } catch (e: SshIdentityChanged) {
                confirmChangedIdentity(e, credential, waitForClose = true)
            } catch (_: Exception) {
                state = state.copy(message = "Связь с задачей потеряна или подготовка отклонена. Если задача запущена, она продолжится на сервере. Подключитесь снова для проверки.")
            } finally {
                withContext(kotlinx.coroutines.NonCancellable + Dispatchers.IO) { connection.close() }
                state = state.copy(busy = false)
            }
        }
    }
}
