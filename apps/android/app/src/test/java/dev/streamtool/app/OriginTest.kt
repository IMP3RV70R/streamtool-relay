package dev.streamtool.app
import org.junit.Assert.*
import org.junit.Test
class OriginTest {
 @Test fun validOrigins() {
  assertEquals("https://example.com",serverOrigin("https://EXAMPLE.com/",false))
  assertEquals("http://10.0.2.2:18080",serverOrigin("http://10.0.2.2:18080",true))
 }
 @Test fun rejectsUnsafeOrigins() {
  listOf("http://example.com","http://10.0.2.2","https://user:pass@example.com","https://example.com/path","https://example.com?secret=x","https://example.com#x","file:///etc/passwd","https://example.com:0").forEach {
   assertThrows(IllegalArgumentException::class.java) { serverOrigin(it,false) }
  }
 }
 @Test fun installationHandoffIsBoundToSshIpAndHttps() {
  val target = SshTarget.parse("8.8.8.8:2222")
  assertEquals("https://8.8.8.8", cabinetOriginForTarget("https://8.8.8.8", target))
  assertEquals("https://[2606:4700:4700::1111]", cabinetOriginForTarget("https://[2606:4700:4700::1111]", SshTarget.parse("[2606:4700:4700::1111]:22")))
  for (value in listOf("https://1.1.1.1", "https://example.com", "http://8.8.8.8", "https://8.8.8.8:8443", "https://8.8.8.8/?token=secret")) {
   assertThrows(IllegalArgumentException::class.java) { cabinetOriginForTarget(value, target) }
  }
 }
}
