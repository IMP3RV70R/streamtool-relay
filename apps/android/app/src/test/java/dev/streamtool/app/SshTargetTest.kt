package dev.streamtool.app

import org.junit.Assert.*
import org.junit.Test
import com.jcraft.jsch.HostKeyRepository

class SshTargetTest {
    @Test fun literalIpAndPort() {
        assertEquals("[203.0.113.4]:22", SshTarget.parse("203.0.113.4").identity)
        assertEquals(SshTarget.parse("203.0.113.4"), SshTarget.parse("203.0.113.4:22"))
        assertEquals(2222, SshTarget.parse("203.0.113.4:2222").port)
        assertEquals(22, SshTarget.parse("2001:db8::1").port)
        assertEquals(22, SshTarget.parse("[2001:db8::1]").port)
        assertEquals(2222, SshTarget.parse("[2001:db8::1]:2222").port)
        val ipv6 = SshTarget.parse("[2001:db8::1]:2222")
        assertEquals(ipv6, SshTarget.parse(ipv6.address))
    }
    @Test fun rejectAmbiguousAddressesAndShellInput() {
        for (value in listOf("127.1", "2130706433", "01.2.3.4", "server.example.com", "1.2.3.4;id", "user@1.2.3.4", "1.2.3.4/path", "fe80::1%eth0", "256.2.3.4", "203.0.113.4:", "203.0.113.4:+22", "203.0.113.4: 22", "[2001:db8::1]:", "[2001:db8::1]:65536", "[2001:db8::1]:22:33", "[203.0.113.4]:22", "2001:db8:::1")) {
            assertThrows(IllegalArgumentException::class.java) { SshTarget.parse(value) }
        }
        for (port in listOf("0", "65536", "22;id")) assertThrows(IllegalArgumentException::class.java) { SshTarget.parse("203.0.113.4:$port") }
    }
    @Test fun hostFingerprintUsesSshSha256Encoding() {
        assertEquals("SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU", SshHostIdentity(byteArrayOf()).fingerprint)
    }
    @Test fun unknownOrChangedHostKeysCannotAuthenticate() {
        val original = byteArrayOf(1, 2, 3)
        assertEquals(HostKeyRepository.NOT_INCLUDED, hostKeyDecision(null, original))
        assertEquals(HostKeyRepository.OK, hostKeyDecision(original, original.copyOf()))
        assertEquals(HostKeyRepository.CHANGED, hostKeyDecision(original, byteArrayOf(1, 2, 4)))
        assertEquals(HostKeyRepository.CHANGED, hostKeyDecision(original, byteArrayOf(1, 2)))
    }
}
