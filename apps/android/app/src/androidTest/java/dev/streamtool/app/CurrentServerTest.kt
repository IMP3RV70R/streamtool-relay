package dev.streamtool.app

import androidx.test.platform.app.InstrumentationRegistry
import kotlinx.coroutines.runBlocking
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Assume.assumeTrue
import org.junit.Test
import java.util.UUID
import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec

/** Optional real-server contract test. Credentials are supplied in private app files, never runner arguments. */
class CurrentServerTest {
    @Test fun realOwnerMfaCookieAndOutputs() = runBlocking {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val file = context.filesDir.resolve("integration-owner.json")
        assumeTrue("No private real-server fixture supplied", file.exists())
        val fixture = JSONObject(file.readText()); file.delete()
        val origin = serverOrigin(fixture.getString("origin"), BuildConfig.DEBUG)
        val password = fixture.getString("password")
        val code = totp(fixture.getString("totp_secret"))
        val login = ApiClient(origin)
        try { login.json("/v1/auth/login", "POST", JSONObject().put("password","incorrect-test-password").put("code",code)); fail("incorrect password accepted") }
        catch (e: ApiError) { assertTrue(e.status == 401) }
        login.json("/v1/auth/login", "POST", JSONObject().put("password",password).put("code",code))
        val session = requireNotNull(login.issuedSession); val c = ApiClient(origin, session.token)
        var created: String? = null
        try {
            val me = c.json("/v1/me"); assertTrue(me.has("account_id") && !me.has("email"))
            assertTrue(c.json("/v1/me/source").has("source_id"))
            val before = JSONArray(c.request("/v1/me/source/outputs")); assertTrue(before.length() < 8)
            val body = JSONObject().put("name","android-integration-${UUID.randomUUID()}").put("endpoint","rtmp://receiver:1935/live")
                .put("secret","test-only-${UUID.randomUUID()}").put("enabled",false).put("generation",0)
            created = c.json("/v1/me/source/outputs","POST",body).getString("id")
            val listing = JSONArray(c.request("/v1/me/source/outputs"))
            val item = (0 until listing.length()).map { listing.getJSONObject(it) }.single { it.getString("id") == created }
            assertFalse(item.has("secret")); assertTrue(item.getLong("generation") == 1L)
            body.put("name","android-renamed-${UUID.randomUUID()}").put("secret","").put("generation",1)
            c.request("/v1/me/source/outputs/$created","PUT",body)
            try { c.request("/v1/me/source/outputs/$created","PUT",body); fail("stale generation accepted") }
            catch (e: ApiError) { assertTrue(e.status == 409) }
            c.request("/v1/me/source/outputs/$created","DELETE",JSONObject().put("generation",2)); created = null
            assertTrue(JSONArray(c.request("/v1/me/source/outputs")).length() == before.length())
        } finally {
            if (created != null) {
                val list = JSONArray(c.request("/v1/me/source/outputs"))
                val item = (0 until list.length()).map { list.getJSONObject(it) }.find { it.getString("id") == created }
                if (item != null) c.request("/v1/me/source/outputs/$created","DELETE",JSONObject().put("generation",item.getLong("generation")))
            }
            c.request("/v1/auth/logout","POST")
        }
        try { c.json("/v1/me"); fail("revoked cookie accepted") } catch (e: ApiError) { assertTrue(e.status == 401) }
    }
    private fun totp(secret: String): String {
        val output = java.io.ByteArrayOutputStream(); var bits = 0; var accumulator = 0
        secret.forEach { ch -> val value = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567".indexOf(ch); require(value >= 0)
            accumulator = (accumulator shl 5) or value; bits += 5
            if (bits >= 8) { bits -= 8; output.write((accumulator shr bits) and 255) }
        }
        val counter = java.nio.ByteBuffer.allocate(8).putLong(System.currentTimeMillis()/30000).array()
        val mac = Mac.getInstance("HmacSHA1"); mac.init(SecretKeySpec(output.toByteArray(),"HmacSHA1"))
        val digest = mac.doFinal(counter); val offset = digest.last().toInt() and 15
        val number = java.nio.ByteBuffer.wrap(digest,offset,4).int and 0x7fffffff
        return (number % 1000000).toString().padStart(6,'0')
    }
}
