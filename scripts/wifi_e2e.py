#!/usr/bin/env python3
"""E2E for saved Wi-Fi priority + gateway rename. Acts as a fake gateway over TCP.
Usage: wifi_e2e.py [http_port] [tcp_port]   (hub must already be running)"""
import json, socket, sys, time, urllib.request, urllib.error

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

def connect(key, announce="515151"):
    s = socket.create_connection(("127.0.0.1", TCP)); s.sendall((key + "\n").encode()); s.sendall(f"GWID:{announce}\n".encode())
    return s

def lines(sock, wait=0.7):
    sock.settimeout(wait); buf = b""
    try:
        while True:
            d = sock.recv(4096)
            if not d: break
            buf += d
    except socket.timeout:
        pass
    return [l for l in buf.decode(errors="replace").split("\n") if l.strip()]

def last(ls, prefix):
    m = [l for l in ls if l.startswith(prefix)]
    return m[-1] if m else None

def order(ls):
    l = last(ls, "WIFISET:")
    return [n["s"] for n in json.loads(l[8:])["nets"]] if l else None

def listed(tok, gid):
    s, d = call("GET", f"/api/gateways/{gid}/wifi", token=tok)
    return [c["ssid"] for c in d], d

stamp = int(time.time())
email = f"w{stamp}@example.com"
call("POST", "/api/auth/register", {"firstName": "W", "lastName": "U", "email": email, "password": "secret1", "confirmPassword": "secret1"})
tok = call("POST", "/api/auth/login", {"email": email, "password": "secret1"})[1]["token"]
s, d = call("POST", "/api/gateways", {"name": "Hall Gateway"}, tok); gid = d["id"]; key = d["apiKey"]

gw = connect(key)
ls = lines(gw)
check("gateway is told its name on connect", last(ls, "GWNAME:") == "GWNAME:Hall Gateway", ls)

# ── save three networks: appended in order, each push carries the full ordered list
for ssid in ("HomeWiFi", "Office:5G", "Phone Hotspot"):   # note ':' in an SSID
    s, d = call("POST", f"/api/gateways/{gid}/wifi", {"ssid": ssid, "password": "pw-" + ssid}, tok)
    check(f"save {ssid!r}", s == 201, (s, d))
ls = lines(gw)
check("gateway receives full ordered list", order(ls) == ["HomeWiFi", "Office:5G", "Phone Hotspot"], order(ls))
names, rows = listed(tok, gid)
check("hub lists in priority order", names == ["HomeWiFi", "Office:5G", "Phone Hotspot"], names)
check("priorities are 0,1,2", [r["priority"] for r in rows] == [0, 1, 2], rows)

# updating an existing SSID's password must not duplicate it
s, _ = call("POST", f"/api/gateways/{gid}/wifi", {"ssid": "HomeWiFi", "password": "new-pass"}, tok)
names, rows = listed(tok, gid); lines(gw)
check("re-saving an SSID updates it (no duplicate)", names.count("HomeWiFi") == 1 and rows[0]["password"] == "new-pass", rows)

# ── reorder
ids = {r["ssid"]: r["id"] for r in rows}
slugs = {r["ssid"]: r["slug"] for r in rows}
check("saved networks carry a slug", all(len(v) == 12 for v in slugs.values()), slugs)
s, _ = call("PUT", f"/api/gateways/{gid}/wifi/order", {"ids": [ids["Phone Hotspot"], ids["HomeWiFi"], ids["Office:5G"]]}, tok)
ls = lines(gw)
check("reorder accepted", s == 200, s)
check("gateway gets the new order", order(ls) == ["Phone Hotspot", "HomeWiFi", "Office:5G"], order(ls))
names, _ = listed(tok, gid); check("hub list reflects new order", names == ["Phone Hotspot", "HomeWiFi", "Office:5G"], names)
s, _ = call("PUT", f"/api/gateways/{gid}/wifi/order", {"ids": [ids["HomeWiFi"], 99999]}, tok); ls = lines(gw)
check("unknown ids are ignored, unmentioned keep order", order(ls) == ["HomeWiFi", "Phone Hotspot", "Office:5G"], order(ls))

# ── delete re-packs priorities and tells the gateway
s, _ = call("DELETE", f"/api/gateways/{gid}/wifi/{ids['Phone Hotspot']}", token=tok); check("a database id is not accepted in the link", s == 404, s)
s, _ = call("DELETE", f"/api/gateways/{gid}/wifi/{slugs['Phone Hotspot']}", token=tok); ls = lines(gw)
check("delete ok", s == 200, s)
check("gateway told the network was removed", order(ls) == ["HomeWiFi", "Office:5G"], order(ls))
names, rows = listed(tok, gid); check("priorities re-packed 0,1", [r["priority"] for r in rows] == [0, 1], rows)

# ── ownership
other = f"x{stamp}@example.com"
call("POST", "/api/auth/register", {"firstName": "O", "lastName": "U", "email": other, "password": "secret1", "confirmPassword": "secret1"})
otok = call("POST", "/api/auth/login", {"email": other, "password": "secret1"})[1]["token"]
s, _ = call("PUT", f"/api/gateways/{gid}/wifi/order", {"ids": [1]}, otok); check("other user cannot reorder", s == 404, s)
s, _ = call("DELETE", f"/api/gateways/{gid}/wifi/{slugs['HomeWiFi']}", token=otok); check("other user cannot delete", s == 404, s)
s, _ = call("PUT", f"/api/gateways/{gid}", {"name": "pwned"}, otok); check("other user cannot rename", s == 404, s)

# ── offline edit: hub copy wins over the gateway's stale list on reconnect
gw.sendall(b"ACK:wifiset:2\n"); time.sleep(0.3)
gw.close(); time.sleep(0.5)
s, _ = call("PUT", f"/api/gateways/{gid}/wifi/order", {"ids": [ids["Office:5G"], ids["HomeWiFi"]]}, tok)
gw = connect(key); lines(gw, 0.3)
gw.sendall(b'CREDSJ:{"nets":[{"s":"HomeWiFi","p":"x"},{"s":"Office:5G","p":"y"}]}\n')
ls = lines(gw)
check("offline edit is pushed on reconnect (hub wins)", order(ls) == ["Office:5G", "HomeWiFi"], order(ls))
names, _ = listed(tok, gid); check("hub list not overwritten by stale gateway list", names == ["Office:5G", "HomeWiFi"], names)

# ── clean state: gateway's own list (and order) is adopted
gw.sendall(b"ACK:wifiset:2\n"); time.sleep(0.3); gw.close(); time.sleep(0.5)
gw = connect(key); lines(gw, 0.3)
gw.sendall(b'CREDSJ:{"nets":[{"s":"Cafe Net","p":"a"},{"s":"A:B:C","p":"b:c"}]}\n'); time.sleep(0.5)
names, rows = listed(tok, gid)
check("gateway list adopted when hub has no pending edits", names == ["Cafe Net", "A:B:C"], names)
check("passwords with ':' survive the round trip", rows[1]["password"] == "b:c", rows)

# ── limits
for i in range(8):
    s, _ = call("POST", f"/api/gateways/{gid}/wifi", {"ssid": f"net{i}", "password": ""}, tok)
check("at most 8 networks", s == 409, s)
s, _ = call("POST", f"/api/gateways/{gid}/wifi", {"ssid": "x" * 40, "password": ""}, tok); check("SSID over 32 chars rejected", s in (400, 409), s)

# ── rename
lines(gw)
s, d = call("PUT", f"/api/gateways/{gid}", {"name": "  Kitchen   Hub "}, tok)
check("rename ok + whitespace tidied", s == 200 and d["name"] == "Kitchen Hub", (s, d))
ls = lines(gw); check("gateway told the new name", last(ls, "GWNAME:") == "GWNAME:Kitchen Hub", ls)
s, d = call("GET", f"/api/gateways/{gid}", token=tok); check("hub stores new name", d.get("name") == "Kitchen Hub", d)
s, _ = call("PUT", f"/api/gateways/{gid}", {"name": "   "}, tok); check("blank name rejected", s == 400, s)
s, _ = call("PUT", f"/api/gateways/{gid}", {"name": "n" * 41}, tok); check("name over 40 chars rejected", s == 400, s)

# ── HMI follows the rename (token-scoped endpoint)
call("POST", "/api/pair/request", {"gatewayId": "515151", "name": "Kitchen Hub"})
code = call("GET", f"/api/pair/code/{gid}", token=tok)[1].get("code")
htok = call("POST", "/api/pair/verify", {"gatewayId": "515151", "code": code})[1].get("token")
s, d = call("GET", "/api/hmi/gateway", token=htok); check("HMI reads its gateway name", s == 200 and d.get("name") == "Kitchen Hub", (s, d))
call("PUT", f"/api/gateways/{gid}", {"name": "Garage"}, tok)
s, d = call("GET", "/api/hmi/gateway", token=htok); check("HMI sees later rename", d.get("name") == "Garage", d)
s, _ = call("GET", "/api/hmi/gateway"); check("HMI endpoint needs a token", s == 401, s)

print("\nFAILED" if fails else "\nALL PASSED", fails)
sys.exit(1 if fails else 0)
