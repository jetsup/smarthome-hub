#!/usr/bin/env python3
"""End-to-end smoke test: auth, gateway link, pairing, HMI token scoping.
Usage: e2e.py [http_port] [tcp_port]   (hub must already be running)"""
import json, socket, struct, sys, time, urllib.request, urllib.error

HTTP = int(sys.argv[1]) if len(sys.argv) > 1 else 9000
TCP = int(sys.argv[2]) if len(sys.argv) > 2 else 9010
BASE = f"http://127.0.0.1:{HTTP}"
fails = 0

def call(method, path, body=None, token=None):
    req = urllib.request.Request(BASE + path, method=method,
        data=json.dumps(body).encode() if body is not None else None,
        headers={"Content-Type": "application/json", **({"Authorization": "Bearer " + token} if token else {})})
    try:
        with urllib.request.urlopen(req, timeout=5) as r:
            return r.status, json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        try: return e.code, json.loads(e.read() or b"null")
        except Exception: return e.code, None

def check(name, cond, extra=""):
    global fails
    print(("PASS " if cond else "FAIL ") + name + (f"  {extra}" if not cond else ""))
    fails += (not cond)

email = f"t{int(time.time())}@example.com"
s, _ = call("POST", "/api/auth/register", {"firstName": "T", "lastName": "U", "email": email, "password": "secret1", "confirmPassword": "secret1"})
check("register", s in (200, 201), s)
s, d = call("POST", "/api/auth/login", {"email": email, "password": "secret1"})
check("login", s == 200 and d.get("token"), s)
tok = d["token"]
s, d = call("POST", "/api/gateways", {"name": "TestGW"}, tok)
check("create gateway", s in (200, 201), (s, d))
gw = d.get("gateway", d); gid = gw["id"]
s, d = call("GET", f"/api/gateways/{gid}/api-key", token=tok)
key = d.get("apiKey") or d.get("api_key"); check("api key", bool(key), d)

# fake gateway over TCP
sock = socket.create_connection(("127.0.0.1", TCP)); sock.sendall((key + "\n").encode()); sock.sendall(b"GWID:424242\n")
time.sleep(0.5)

# --- security checks
for p in ("/api/hmi/gateways/x/all-nodes", "/api/hmi/nodes/1", "/api/pair/code/" + gid):
    s, _ = call("GET", p); check(f"unauth GET {p} rejected", s == 401, s)
s, _ = call("POST", "/api/hmi/nodes/1/command", {"value": 1}); check("unauth HMI command rejected", s == 401, s)
s, _ = call("GET", "/api/hmi/gateways"); check("gateway list open for pairing scan", s == 200, s)

# --- pairing
s, d = call("POST", "/api/pair/request", {"gatewayId": "nonexistent", "name": ""}); check("pair unknown gateway 404", s == 404, s)
s, d = call("POST", "/api/pair/request", {"gatewayId": "424242", "name": "TestGW"}); check("pair request", s == 200 and d["success"], (s, d))
s, d = call("GET", f"/api/pair/code/{gid}", token=tok); code = d.get("code"); check("owner reads code", bool(code), d)
tok2 = call("POST", "/api/auth/login", {"email": email, "password": "secret1"})[1]["token"]
other = f"o{int(time.time())}@example.com"
call("POST", "/api/auth/register", {"firstName": "O", "lastName": "U", "email": other, "password": "secret1", "confirmPassword": "secret1"})
otok = call("POST", "/api/auth/login", {"email": other, "password": "secret1"})[1]["token"]
s, _ = call("GET", f"/api/pair/code/{gid}", token=otok); check("other user cannot read code", s == 404, s)
s, d = call("POST", "/api/pair/verify", {"gatewayId": "424242", "code": "000000" if code != "000000" else "111111"}); check("wrong code rejected", not d["success"], d)
s, d = call("POST", "/api/pair/verify", {"gatewayId": "424242", "code": code}); check("right code -> token", d.get("success") and d.get("token"), d)
htok = d["token"]
s, d = call("POST", "/api/pair/verify", {"gatewayId": "424242", "code": code}); check("code single-use", not d.get("success"), d)
s, d = call("GET", "/api/hmi/gateways/424242/all-nodes", token=htok); check("HMI token lists nodes", s == 200 and isinstance(d, list), (s, d))
s, _ = call("GET", "/api/gateways", token=htok); check("HMI token rejected on user API", s == 401, s)
s, _ = call("POST", "/api/auth/refresh", token=htok); check("HMI token cannot be refreshed into user token", s == 401, s)
s, _ = call("GET", "/api/hmi/nodes/99999", token=htok); check("HMI unknown node 404", s == 404, s)

# brute force lockout
call("POST", "/api/pair/request", {"gatewayId": "424242"})
codes = [call("POST", "/api/pair/verify", {"gatewayId": "424242", "code": "99999%d" % i})[0] for i in range(7)]
check("brute force locked out (429)", 429 in codes, codes)

# legacy routes
s, d = call("GET", "/api/devices", token=tok); check("devices list", s == 200, s)
s, _ = call("POST", "/api/device/123/control?state=1", token=tok); check("legacy control of foreign/unknown device 404", s == 404, s)
s, _ = call("POST", "/api/device/123/control?state=99", token=tok); check("reserved reset value 99 rejected", s == 400, s)
s, _ = call("GET", "/api/devices"); check("devices needs auth", s == 401, s)

# CORS
req = urllib.request.Request(BASE + "/api/gateways", method="OPTIONS", headers={"Origin": "http://evil.example", "Access-Control-Request-Method": "GET"})
try:
    r = urllib.request.urlopen(req); allow = r.headers.get("Access-Control-Allow-Origin")
except urllib.error.HTTPError as e: allow = e.headers.get("Access-Control-Allow-Origin")
check("CORS does not allow arbitrary origin", allow not in ("*", "http://evil.example"), allow)

sock.close()
print("\nFAILED: %d" % fails if fails else "\nALL PASSED")
sys.exit(1 if fails else 0)
