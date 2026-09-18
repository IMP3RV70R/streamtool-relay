import json
import socket
import subprocess
import sys

allocation = sys.argv[1]
pid = subprocess.check_output([
    "docker", "inspect", "--format", "{{.State.Pid}}", "stream-worker-" + allocation
], text=True).strip()
prefix = ["nsenter", "--target", pid, "--net", "--", "python3", "-c"]
def probe(code):
    subprocess.run(prefix + [code], check=True, timeout=10)

probe("""
import socket
for host, port in [('172.30.249.3',9090),('172.30.249.1',8443),('169.254.169.254',80)]:
    try:
        socket.create_connection((host,port),2)
    except OSError:
        continue
    raise RuntimeError('private/metadata connection admitted: '+host)
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM)
s.settimeout(3)
s.sendto(b'source-check',('172.30.249.3',8890))
assert s.recv(100)==b'source-check'
""")
qdisc = subprocess.check_output(["nsenter", "--target", pid, "--net", "--", "tc", "-j", "qdisc", "show", "dev", "eth0"], text=True)
assert {q["kind"] for q in json.loads(qdisc)} >= {"tbf", "ingress"}
print("Kernel network policy passed: private/metadata denied, SRT allowed, bandwidth policing installed")
