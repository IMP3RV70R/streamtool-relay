package dev.streamtool.app

import org.junit.Assert.*
import org.junit.Test

class OwnerSessionTest {
    private val token = "a".repeat(43)
    private val cookie = "streamtool_session=$token; Path=/; Max-Age=604800; HttpOnly; Secure; SameSite=Strict"
    @Test fun acceptsOnlyBoundedHostSession() {
        assertEquals(OwnerSession(token, 604800), ownerSession(listOf(cookie), "https://example.com"))
        assertNull(ownerSession(emptyList(), "https://example.com"))
    }
    @Test fun rejectsUnsafeCookieAttributes() {
        for (value in listOf(cookie.replace("; Secure", ""), cookie.replace("; HttpOnly", ""), cookie.replace("Path=/", "Path=/v1"), cookie.replace("604800", "604801"), cookie.replace(token, "unsafe;Cookie=x"), "$cookie; Domain=other.example", "$cookie; Domain=.example.com")) {
            assertThrows(IllegalArgumentException::class.java) { ownerSession(listOf(value), "https://example.com") }
        }
        assertThrows(IllegalArgumentException::class.java) { ownerSession(listOf(cookie, cookie), "https://example.com") }
    }
}
