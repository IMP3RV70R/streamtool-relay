package dev.streamtool.app

import org.json.JSONObject

fun installationFailureMessage(code: String): String = when (code) {
    "recovery_budget_exhausted" -> "Лимит времени или попыток установки исчерпан."
    "command_timeout", "installation_timeout" -> "Операция не завершилась за отведённое время."
    "stage_inventory_changed" -> "Состав проверенного пакета изменился. Установка заблокирована ради безопасности."
    "image_platform_mismatch" -> "Архитектура загруженных компонентов не соответствует серверу."
    "staging_space_insufficient" -> "Недостаточно свободного места для распаковки пакета."
    "disk_full" -> "На сервере закончилось место на диске."
    "command_failed" -> "Системная команда завершилась с ошибкой."
    "installation_failed" -> "Не удалось завершить этап установки. Диагностика ниже поможет найти причину."
    else -> "Установка столкнулась с ошибкой."
}

fun installationDiagnosticReport(job: JSONObject, diagnostics: JSONObject?): String = buildString {
    appendLine("streamtool-relay Android ${BuildConfig.VERSION_NAME}")
    // Only explicitly allowed bounded identifiers/observations; never serialize remote JSON.
    for (key in listOf("job_id", "phase", "resume_phase", "version", "error")) {
        val value = job.optString(key)
        if (value.matches(Regex("[a-zA-Z0-9._-]{1,100}"))) appendLine("$key: $value")
    }
    appendLine("attempts: ${job.optInt("attempts", 0).coerceIn(0, 3)}")
    job.optJSONObject("failure")?.let { failure ->
        for (key in listOf("command", "kind")) {
            val value = failure.optString(key)
            if (value.matches(Regex("[a-zA-Z0-9_-]{1,40}"))) appendLine("$key: $value")
        }
        for (key in listOf("exit_code", "errno")) if (failure.has(key)) appendLine("$key: ${failure.optInt(key)}")
    }
    diagnostics?.let { data ->
        for (unit in listOf("installation_service", "docker_service")) data.optJSONObject(unit)?.let { service ->
            for (field in listOf("ActiveState", "SubState", "Result", "ExecMainStatus")) {
                val value = service.optString(field)
                if (value.matches(Regex("[a-z0-9-]{1,40}"))) appendLine("$unit.$field: $value")
            }
        }
        if (data.has("docker_available")) appendLine("docker_available: ${data.optBoolean("docker_available")}")
        for (key in listOf("installation_disk_free_bytes", "docker_disk_free_bytes", "MemTotal", "MemAvailable", "SwapFree")) {
            val value = data.optLong(key, -1)
            if (value >= 0) appendLine("$key: ${value / (1024 * 1024)} MiB")
        }
        for (key in listOf("installation_free_inodes", "docker_free_inodes")) {
            val value = data.optLong(key, -1)
            if (value >= 0) appendLine("$key: $value")
        }
    }
    if (diagnostics == null) appendLine("Наблюдения сервера ещё не получены. Нажмите «Получить диагностику».")
}.trim()
