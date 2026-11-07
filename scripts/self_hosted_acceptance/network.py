"""Check readiness and a bounded no-public-network baseline from the fixture network."""
import socket
import urllib.request

for service, port in (("api", 3001), ("worker", 3003)):
    with urllib.request.urlopen(f"http://{service}:{port}/readyz", timeout=10) as response:
        assert response.status == 200, service
with socket.socket() as connection:
    connection.settimeout(2)
    assert connection.connect_ex(("1.1.1.1", 443)) != 0, "public socket unexpectedly reachable"
print("PASS: API/control-worker readiness and public socket denial")
