package dev.streamtool.app

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import org.json.JSONObject

// Only the encrypted session is persisted. Password, TOTP seed and source key stay in memory.
class SessionStore(context: Context) {
    private val prefs = context.getSharedPreferences("native_session", Context.MODE_PRIVATE)
    private val alias = "streamtool_session_v1"
    var origin: String
        get() = prefs.getString("origin", "") ?: ""
        set(value) { check(prefs.edit().putString("origin", value).commit()) }
    private fun key(): SecretKey {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getKey(alias, null) as? SecretKey)?.let { return it }
        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").apply {
            init(KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256).build())
        }.generateKey()
    }
    fun save(token: String, seconds: Long) {
        val cipher = Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.ENCRYPT_MODE, key()); updateAAD(origin.toByteArray()) }
        val data = JSONObject().put("token", token).put("expires", System.currentTimeMillis() + seconds * 1000).toString()
        val encrypted = cipher.doFinal(data.toByteArray())
        check(prefs.edit().putString("session", Base64.encodeToString(cipher.iv + encrypted, Base64.NO_WRAP)).commit())
    }
    fun load(): String? = try {
        val encoded = prefs.getString("session", null)
        if (encoded == null) null else {
            require(encoded.length <= 4096)
            val data = Base64.decode(encoded, Base64.NO_WRAP)
            val cipher = Cipher.getInstance("AES/GCM/NoPadding").apply {
                init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, data.copyOfRange(0, 12)))
                updateAAD(origin.toByteArray())
            }
            val value = JSONObject(String(cipher.doFinal(data.copyOfRange(12, data.size))))
            val token = value.getString("token")
            if (value.getLong("expires") <= System.currentTimeMillis() || !token.matches(Regex("[A-Za-z0-9_-]{43}"))) { clear(); null } else token
        }
    } catch (_: Exception) { clear(); null }
    fun clear() { check(prefs.edit().remove("session").commit()) }
}
