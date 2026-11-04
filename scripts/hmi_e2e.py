#!/usr/bin/env python3
"""E2E for displays (HMIs): code/QR pairing, listing, node assignment, and the rule
that a node is never on two displays. Acts as a fake gateway over TCP.
Usage: hmi_e2e.py [http_port] [tcp_port]   (hub must already be running)
Uses fresh node / display ids on every run, so it can be re-run against one database."""
import json, os, socket, struct, sys, time, urllib.request, urllib.error

HTTP = int(sys.argv[1]) if len(sys.argv) > 1 else 9000
TCP = int(sys.argv[2]) if len(sys.argv) > 2 else 9010
BASE = f"http://127.0.0.1:{HTTP}"
fails = 0

stamp = int(time.time())
RUN = (stamp * 7 + os.getpid()) % 9000000
B1, B2, B3, B4 = [3000000000 + RUN * 10 + i for i in range(1, 5)]   # node device ids (globally unique)
def dev_id(n): return f"AABB{RUN:07d}{n:02d}"                        # display hardware ids

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

def pkt(device, mtype=1, value=7):
    b = bytearray([0xAA, mtype]) + struct.pack("<I", device) + struct.pack("<H", value)
    x = 0
    for v in b: x ^= v
    return bytes(b + bytes([x]))

def drain(sock, wait=0.4):
    sock.settimeout(wait)
    try:
        while sock.recv(4096): pass
    except socket.timeout: pass

email = f"d{stamp}{RUN}@example.com"
call("POST", "/api/auth/register", {"firstName": "D", "lastName": "U", "email": email, "password": "secret1", "confirmPassword": "secret1"})
tok = call("POST", "/api/auth/login", {"email": email, "password": "secret1"})[1]["token"]
other = f"y{stamp}{RUN}@example.com"
call("POST", "/api/auth/register", {"firstName": "O", "lastName": "U", "email": other, "password": "secret1", "confirmPassword": "secret1"})
otok = call("POST", "/api/auth/login", {"email": other, "password": "secret1"})[1]["token"]
d = call("POST", "/api/gateways", {"name": "GW-A"}, tok)[1]; gid, key = d["id"], d["apiKey"]
gw2 = call("POST", "/api/gateways", {"name": "GW-B"}, tok)[1]["id"]
ogw = call("POST", "/api/gateways", {"name": "Not mine"}, otok)[1]["id"]
ANN = str(500000 + RUN % 400000)
sock = socket.create_connection(("127.0.0.1", TCP)); sock.sendall((key + "\n").encode()); sock.sendall(f"GWID:{ANN}\n".encode()); drain(sock)

def make_node(dev, name):
    s, r = call("POST", f"/api/gateways/{gid}/provision", {"deviceId": dev, "name": name, "capabilitiesConfig": [{"type": "digitalOutput", "pin": 26, "label": "L"}]}, tok)
    sock.sendall(pkt(dev)); time.sleep(0.5); drain(sock, 0.2)
    return s

def nid(dev):
    d = call("GET", f"/api/gateways/{gid}/nodes", token=tok)[1]
    return next(n["nodeId"] for n in d if n["deviceId"] == dev)

def hmi_nodes(htok):
    s, d = call("GET", "/api/hmi/gateways/x/all-nodes", token=htok)
    return sorted(n["deviceId"] for n in d if n.get("isProvisioned")) if s == 200 else None

def status(dev, secret):
    return call("GET", f"/api/hmi/register/status?deviceId={dev}&secret={secret}")

# two nodes exist BEFORE any display
check("node 1 created", make_node(B1, "Lamp") == 200)
check("node 2 created", make_node(B2, "Fan") == 200)

# ── pairing by code ──────────────────────────────────────────────────────────
s, d = call("POST", "/api/hmi/register", {"deviceId": dev_id(1), "name": "Kitchen"}); reg1 = d
check("display registers (no auth) and gets a 6-digit code", s == 200 and len(d["code"]) == 6 and d["secret"], (s, d))
s, d = call("POST", "/api/hmi/register", {"deviceId": "x"}); check("bad device id rejected", s == 400, s)
s, d = status(dev_id(1), reg1["secret"]); check("status is pending before claim", d["status"] == "pending", d)
s, _ = status(dev_id(1), "nope"); check("status needs the right secret", s == 403, s)
s, _ = call("POST", "/api/hmis/claim", {"code": reg1["code"], "gatewayId": gid}); check("claim needs login", s == 401, s)
s, _ = call("POST", "/api/hmis/claim", {"code": reg1["code"], "gatewayId": ogw}, tok); check("cannot claim onto someone else's gateway", s == 404, s)
s, d = call("POST", "/api/hmis/claim", {"code": reg1["code"], "gatewayId": gid}, tok)
check("owner claims with the code", s == 200 and d["hmi"]["gatewayId"] == gid and d["hmi"]["name"] == "Kitchen", (s, d))
hmi1 = d["hmi"]["id"]; slug1 = d["hmi"]["slug"]
check("display has a random slug", len(slug1) == 12 and slug1 != str(hmi1), slug1)
s, d = status(dev_id(1), reg1["secret"]); check("display collects its token", d["status"] == "claimed" and d["token"], d)
h1 = d["token"]
s, d = status(dev_id(1), reg1["secret"]); check("token is delivered only once", d["status"] == "expired", d)
s, _ = call("POST", "/api/hmis/claim", {"code": reg1["code"], "gatewayId": gid}, tok); check("code cannot be reused", s == 404, s)

check("first display automatically gets existing nodes", hmi_nodes(h1) == [B1, B2], hmi_nodes(h1))
s, d = call("GET", "/api/hmi/gateway", token=h1); check("display reads its gateway + name", s == 200 and d["hmiName"] == "Kitchen" and d["name"] == "GW-A", d)
s, d = call("GET", "/api/hmi/gateway", token=h1); check("new display defaults to dark / #00A8E8 / 100%", d["appearance"] == {"theme": "dark", "accent": "00A8E8", "brightness": 100, "statusLed": True}, d.get("appearance"))
s, d = call("PUT", f"/api/hmis/{slug1}/appearance", {"theme": "light", "accent": "#f0a030", "brightness": 55, "statusLed": False}, tok); check("owner changes appearance", s == 200 and d["theme"] == "light", (s, d))
d = call("GET", "/api/hmi/gateway", token=h1)[1]; check("display receives the new appearance", d["appearance"] == {"theme": "light", "accent": "F0A030", "brightness": 55, "statusLed": False}, d.get("appearance"))
check("bad theme rejected", call("PUT", f"/api/hmis/{slug1}/appearance", {"theme": "neon"}, tok)[0] == 400)
check("bad colour rejected", call("PUT", f"/api/hmis/{slug1}/appearance", {"accent": "zzz"}, tok)[0] == 400)
check("brightness out of range rejected", call("PUT", f"/api/hmis/{slug1}/appearance", {"brightness": 3}, tok)[0] == 400)
check("another account cannot change it", call("PUT", f"/api/hmis/{slug1}/appearance", {"theme": "dark"}, otok)[0] == 404)

# ── listing ──────────────────────────────────────────────────────────────────
s, d = call("GET", "/api/hmis", token=tok)
row = next((x for x in d if x["id"] == hmi1), None)
check("owner sees the display in the list", s == 200 and row and row["gatewayName"] == "GW-A" and row["online"] and row["nodeCount"] == 2, d)
s, d = call("GET", "/api/hmis", token=otok); check("other user sees none of them", s == 200 and d == [], d)
s, _ = call("GET", f"/api/hmis/{slug1}", token=otok); check("a database id in the link finds nothing", call("GET", f"/api/hmis/{hmi1}", token=tok)[0] == 404)
s, _ = call("GET", f"/api/hmis/{slug1}", token=otok); check("other user cannot open the display", s == 404, s)

# ── brute force on the claim code ────────────────────────────────────────────
codes = [call("POST", "/api/hmis/claim", {"code": f"99999{i}", "gatewayId": gid}, otok)[0] for i in range(7)]
check("wrong codes are rate-limited (429)", 429 in codes and codes[0] == 404, codes)

# ── second display on the same gateway ───────────────────────────────────────
reg2 = call("POST", "/api/hmi/register", {"deviceId": dev_id(2), "name": "Hallway"})[1]
d = call("POST", "/api/hmis/claim", {"code": reg2["code"], "gatewayId": gid}, tok)[1]; hmi2 = d["hmi"]["id"]; slug2 = d["hmi"]["slug"]
h2 = status(dev_id(2), reg2["secret"])[1]["token"]
check("second display starts with no nodes", hmi_nodes(h2) == [], hmi_nodes(h2))
check("first display keeps its nodes", hmi_nodes(h1) == [B1, B2], hmi_nodes(h1))

# ── a node is on one display only ────────────────────────────────────────────
s, d = call("PUT", f"/api/nodes/{nid(B2)}/hmi", {"hmiId": hmi2}, tok); check("assign node 2 to display 2", s == 200, (s, d))
check("node 2 left display 1", hmi_nodes(h1) == [B1], hmi_nodes(h1))
check("node 2 is now on display 2 only", hmi_nodes(h2) == [B2], hmi_nodes(h2))
s, _ = call("POST", f"/api/hmi/nodes/{B2}/command", {"pin": 26, "value": 1}, h1); check("display 1 cannot control node 2", s == 404, s)
s, _ = call("GET", f"/api/hmi/nodes/{B2}", token=h1); check("display 1 cannot read node 2", s == 404, s)
s, _ = call("POST", f"/api/hmi/nodes/{B2}/command", {"pin": 26, "value": 1}, h2); check("display 2 can control node 2", s == 200, s)
s, d = call("PUT", f"/api/hmis/{slug2}/nodes", {"deviceIds": [B1, B2]}, tok)
check("bulk assign moves nodes (never shares)", s == 200, (s, d))
check("display 1 lost node 1 (moved to display 2)", hmi_nodes(h1) == [], hmi_nodes(h1))
check("display 2 now has both", hmi_nodes(h2) == [B1, B2], hmi_nodes(h2))
both = set(hmi_nodes(h1) or []) & set(hmi_nodes(h2) or []); check("INVARIANT: no node on two displays", not both, both)
s, _ = call("PUT", f"/api/hmis/{slug1}/nodes", {"deviceIds": [999999]}, tok); check("unknown node id rejected", s == 400, s)
s, _ = call("PUT", f"/api/hmis/{slug1}/nodes", {"deviceIds": [B1]}, otok); check("other user cannot assign", s == 404, s)
s, _ = call("PUT", f"/api/nodes/{nid(B1)}/hmi", {"hmiId": hmi1}, otok); check("other user cannot assign (node route)", s == 404, s)
s, d = call("PUT", f"/api/nodes/{nid(B1)}/hmi", {"hmiId": None}, tok); check("unassign a node", s == 200 and hmi_nodes(h2) == [B2], hmi_nodes(h2))
s, d = call("GET", f"/api/hmis/{slug1}", token=tok)
nv = {n["deviceId"]: n for n in d["nodes"]}
check("detail shows who has each node", nv[B1]["hmiId"] is None and nv[B2]["hmiName"] == "Hallway" and d["siblings"] == 1, d)

# ── new nodes: auto-assign only when the gateway has exactly one display ─────
make_node(B3, "Heater")
d = call("GET", f"/api/gateways/{gid}/nodes", token=tok)[1]
n3 = next(n for n in d if n["deviceId"] == B3)
check("with two displays a new node is left unassigned", n3.get("hmiId") is None, n3)
reg3 = call("POST", "/api/hmi/register", {"deviceId": dev_id(3), "name": "Garage"})[1]
call("POST", "/api/hmis/claim", {"code": reg3["code"], "gatewayId": gw2}, tok)
h3 = status(dev_id(3), reg3["secret"])[1]["token"]
check("display on another gateway sees none of these nodes", hmi_nodes(h3) == [], hmi_nodes(h3))

# ── rename / unpair ──────────────────────────────────────────────────────────
caps_ok = [{"type": "analogInput", "pin": 34, "label": "Pot"}, {"type": "digitalOutput", "pin": 25, "label": "Green"}]
s, d = call("PUT", f"/api/nodes/{nid(B1)}/capabilities", {"capabilitiesConfig": caps_ok}, tok); check("owner changes a node's pins", s == 200, (s, d))
check("duplicate pin rejected", call("PUT", f"/api/nodes/{nid(B1)}/capabilities", {"capabilitiesConfig": [{"type": "digitalOutput", "pin": 25}, {"type": "analogInput", "pin": 25}]}, tok)[0] == 400)
check("output on an input-only pin rejected", call("PUT", f"/api/nodes/{nid(B1)}/capabilities", {"capabilitiesConfig": [{"type": "digitalOutput", "pin": 34}]}, tok)[0] == 400)
check("flash pins rejected", call("PUT", f"/api/nodes/{nid(B1)}/capabilities", {"capabilitiesConfig": [{"type": "digitalOutput", "pin": 9}]}, tok)[0] == 400)
check("empty pin list rejected", call("PUT", f"/api/nodes/{nid(B1)}/capabilities", {"capabilitiesConfig": []}, tok)[0] == 400)
check("another account cannot change pins", call("PUT", f"/api/nodes/{nid(B1)}/capabilities", {"capabilitiesConfig": caps_ok}, otok)[0] == 404)
s, d = call("GET", f"/api/nodes/{nid(B1)}", token=tok); check("new pins are stored", s == 200 and "Green" in json.dumps(d), d)
s, d = call("PUT", f"/api/hmis/{slug1}", {"name": "  Living   Room "}, tok); check("rename tidies the name", s == 200 and d["name"] == "Living Room", (s, d))
s, _ = call("PUT", f"/api/hmis/{slug1}", {"name": "  "}, tok); check("blank name rejected", s == 400, s)
call("PUT", f"/api/hmis/{slug2}/nodes", {"deviceIds": [B2, B3]}, tok)
s, _ = call("DELETE", f"/api/hmis/{slug2}", token=otok); check("other user cannot unpair", s == 404, s)
s, _ = call("DELETE", f"/api/hmis/{slug2}", token=tok); check("owner unpairs a display", s == 200, s)
s, _ = call("GET", "/api/hmi/gateway", token=h2); check("unpaired display's token stops working at once", s == 401, s)
d = call("GET", f"/api/gateways/{gid}/nodes", token=tok)[1]
check("its nodes are released, not left on a dead display", all(n.get("hmiId") is None for n in d if n["deviceId"] in (B2, B3)), d)
make_node(B4, "Pump")
d = call("GET", f"/api/gateways/{gid}/nodes", token=tok)[1]
check("with exactly one display left, a new node is auto-assigned to it", next(n for n in d if n["deviceId"] == B4).get("hmiId") == hmi1, d)

# ── display unpairs itself; re-pairing the same hardware ─────────────────────
s, _ = call("POST", "/api/hmi/unpair", token=h3); check("display can unpair itself", s == 200, s)
s, _ = call("GET", "/api/hmi/gateway", token=h3); check("...and then its token is dead", s == 401, s)
reg = call("POST", "/api/hmi/register", {"deviceId": dev_id(1), "name": "Kitchen"})[1]
d = call("POST", "/api/hmis/claim", {"code": reg["code"], "gatewayId": gw2}, tok)[1]
check("re-pairing the same hardware moves it (same record)", d["hmi"]["id"] == hmi1 and d["hmi"]["gatewayId"] == gw2, d)
d = call("GET", f"/api/gateways/{gid}/nodes", token=tok)[1]
check("old gateway's nodes are unassigned after the move", all(n.get("hmiId") is None for n in d), [(n["deviceId"], n.get("hmiId")) for n in d])

# ── legacy pairing path now registers a display too ──────────────────────────
call("POST", "/api/pair/request", {"gatewayId": ANN, "name": "GW-A"})
code = call("GET", f"/api/pair/code/{gid}", token=tok)[1].get("code")
d = call("POST", "/api/pair/verify", {"gatewayId": ANN, "code": code, "deviceId": dev_id(9), "name": "Old Flow"})[1]
check("legacy code pairing yields a display token", d.get("success") and d.get("token") and d.get("hmiId"), d)
s, d2 = call("GET", "/api/hmis", token=tok); check("...and the display is listed", any(x["deviceId"] == dev_id(9) for x in d2), d2)

print("\nFAILED" if fails else "\nALL PASSED", fails)
sys.exit(1 if fails else 0)
