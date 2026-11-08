#!/usr/bin/env python3
"""E2E for the module framework: catalog, NeoPixel/buzzer/DHT/LD2410/DS3231 config, multi-value
readings, actions, rules between modules, and the shared (RTC) clock.
Usage: modules_e2e.py [http_port] [tcp_port]   (hub must be running)"""
import json, socket, struct, sys, time, urllib.request, urllib.error

HTTP = int(sys.argv[1]) if len(sys.argv) > 1 else 9000
TCP = int(sys.argv[2]) if len(sys.argv) > 2 else 9010
BASE = f"http://127.0.0.1:{HTTP}"
RUN = int(time.time())
fails = 0

def call(method, path, body=None, token=None):
    req = urllib.request.Request(BASE + path, method=method, data=json.dumps(body).encode() if body is not None else None,
        headers={"Content-Type": "application/json", **({"Authorization": "Bearer " + token} if token else {})})
    try:
        with urllib.request.urlopen(req, timeout=6) as r: return r.status, json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        try: return e.code, json.loads(e.read() or b"null")
        except Exception: return e.code, None

def check(name, cond, extra=""):
    global fails
    print(("PASS " if cond else "FAIL ") + name + (f"  {extra}" if not cond else ""))
    fails += (not cond)

def xor(b):
    x = 0
    for v in b: x ^= v
    return x

def pkt(dev, t=1, value=7):
    b = bytearray([0xAA, t]) + struct.pack("<I", dev) + struct.pack("<H", value)
    return bytes(b + bytes([xor(b)]))

def frame(dev, t, payload):
    b = bytearray([0xAA, t]) + struct.pack("<I", dev) + bytes([len(payload)]) + payload
    return bytes(b + bytes([xor(b)]))

def readings(dev, items):  # items: (cap, channel, int32)
    return frame(dev, 8, b"".join(struct.pack("<BBi", c, ch, v) for c, ch, v in items))

class Gw:
    def __init__(self, key):
        self.s = socket.create_connection(("127.0.0.1", TCP)); self.s.sendall((key + "\n").encode())
        self.s.sendall(f"GWID:{600000 + RUN % 300000}\n".encode()); self.buf = b""
        self.s.settimeout(0.3)
    def pump(self, wait=0.5):
        end = time.time() + wait
        while time.time() < end:
            try:
                d = self.s.recv(4096)
                if not d: break
                self.buf += d
            except socket.timeout: pass
    def frames(self, t):  # all variable frames of a type received so far
        out, b, i = [], self.buf, 0
        while i < len(b):
            if b[i] == 0xAA and i + 8 <= len(b) and b[i + 1] >= 8:
                n = b[i + 6]; out.append((b[i + 1], b[i + 2:i + 6], b[i + 7:i + 7 + n])); i += 8 + n
            else: i += 1
        return [f for f in out if f[0] == t]
    def pincmds(self, dev):  # classic 9-byte pin commands for a device: [(pin, value)]
        out, b = [], self.buf
        for i in range(len(b) - 8):
            if b[i] == 0xAA and b[i + 1] == 6 and struct.unpack("<I", b[i + 2:i + 6])[0] == dev and xor(b[i:i + 8]) == b[i + 8]:
                out.append((b[i + 6], b[i + 7]))
        return out
    def text(self): return self.buf.decode(errors="replace")
    def clear(self): self.buf = b""


import re
email = f"p{RUN}@example.com"; other = f"po{RUN}@example.com"
for e in (email, other): call("POST", "/api/auth/register", {"firstName": "P", "lastName": "U", "email": e, "password": "secret1", "confirmPassword": "secret1"})
tok = call("POST", "/api/auth/login", {"email": email, "password": "secret1"})[1]["token"]
otok = call("POST", "/api/auth/login", {"email": other, "password": "secret1"})[1]["token"]
a = call("POST", "/api/gateways", {"name": "PC-A"}, tok)[1]
b = call("POST", "/api/gateways", {"name": "PC-B"}, tok)[1]
c = call("POST", "/api/gateways", {"name": "PC-C"}, otok)[1]
gA, gB, gC = Gw(a["apiKey"]), Gw(b["apiKey"]), Gw(c["apiKey"])
for g in (gA, gB, gC): g.pump(0.6)
dev = 700000 + RUN % 90000

gA.s.sendall(pkt(dev, 3, 1)); gA.pump(0.5)
st, disc = call("GET", f"/api/gateways/{a['id']}/discovered", token=tok)
check("new node is discovered, not paired", st == 200 and any(d["deviceId"] == dev and not d["paired"] for d in disc), disc)
st, r = call("POST", f"/api/gateways/{a['id']}/provision", {"deviceId": dev, "name": "Hi", "capabilitiesConfig": [{"type": "digitalOutput", "pin": 2, "label": "L"}]}, tok)
check("pair new node to A", st == 200, (st, r))
gA.pump(0.5)
m = re.search(r"PCODE:%d:([A-Z0-9]+)" % dev, gA.text())
code = m.group(1) if m else ""
check("gateway A got an 8-char upper-case alphanumeric code before BB:", len(code) == 8 and code.isalnum() and code.upper() == code and gA.text().index("PCODE:") < gA.text().index("BB:"), gA.text()[:200])
gA.s.sendall(pkt(dev, 1, 5)); gA.pump(0.6)
st, nodes = call("GET", f"/api/gateways/{a['id']}/nodes", token=tok)
n = next((x for x in nodes if x["deviceId"] == dev), None)
check("node active on A", n is not None, nodes)
st, det = call("GET", f"/api/nodes/{n['nodeId']}", token=tok) if n else (0, {})
check("node detail carries the same code", det.get("pairCode") == code, det.get("pairCode"))

# paired node answers B's scan: flagged paired
gB.s.sendall(pkt(dev, 3, 0x101)); gB.pump(0.5)
st, disc = call("GET", f"/api/gateways/{b['id']}/discovered", token=tok)
check("paired node shows up on B flagged paired", any(d["deviceId"] == dev and d["paired"] for d in disc), disc)
st, disc = call("GET", f"/api/gateways/{a['id']}/discovered", token=tok)
check("A does not list its own node as new", not any(d["deviceId"] == dev for d in disc), disc)

body_ = {"deviceId": dev, "name": "Moved", "capabilitiesConfig": [{"type": "digitalOutput", "pin": 2, "label": "L"}]}
st, r = call("POST", f"/api/gateways/{b['id']}/provision", body_, tok)
check("moving without code refused", st == 403 and r.get("status") == "code_required", (st, r))
st, r = call("POST", f"/api/gateways/{b['id']}/provision", {**body_, "code": "WRONG123"}, tok)
check("moving with a wrong code refused", st == 403 and r.get("status") == "code_invalid", (st, r))
st, r = call("POST", f"/api/gateways/{b['id']}/provision", {**body_, "force": True}, tok)
check("force no longer bypasses the code", st == 403, (st, r))
st, r = call("POST", f"/api/gateways/{c['id']}/provision", body_, otok)
check("another account cannot take it without the code", st == 403, (st, r))
check("nothing was sent to B/C before the code was right", "BB:" not in gB.text() and "BB:" not in gC.text())
st, nodes = call("GET", f"/api/gateways/{a['id']}/nodes", token=tok)
check("node still on A after refusals", any(x["deviceId"] == dev for x in nodes))

pretty = code[:4] + " - " + code[4:].lower()
st, r = call("POST", f"/api/gateways/{b['id']}/provision", {**body_, "code": "shnode:" + pretty}, tok)
check("moving with the right code (any case/spacing/QR prefix) accepted", st == 200, (st, r))
gB.pump(0.5)
check("B was handed the same code", ("PCODE:%d:%s" % (dev, code)) in gB.text(), gB.text()[:200])
gB.s.sendall(pkt(dev, 1, 6)); gB.pump(0.6)
st, nodes = call("GET", f"/api/gateways/{b['id']}/nodes", token=tok)
nb = next((x for x in nodes if x["deviceId"] == dev), None)
check("node now on B", nb is not None, nodes)
st, nodes = call("GET", f"/api/gateways/{a['id']}/nodes", token=tok)
check("node gone from A", not any(x["deviceId"] == dev for x in nodes))
st, det = call("GET", f"/api/nodes/{nb['nodeId']}", token=tok) if nb else (0, {})
check("code unchanged after the move", det.get("pairCode") == code, det.get("pairCode"))

# a node that was reset by hand says "not paired": no code needed
gA.s.sendall(pkt(dev, 3, 1)); gA.pump(0.5)
gA.clear()
st, r = call("POST", f"/api/gateways/{a['id']}/provision", body_, tok)
check("hand-reset node (heard unpaired) needs no code", st == 200, (st, r))
gA.pump(0.4)
m2 = re.search(r"PCODE:%d:([A-Z0-9]+)" % dev, gA.text())
check("and receives a fresh code", m2 and m2.group(1) != code, gA.text()[:120])

# ── MAC address reported by the node ─────────────────────────────────────────
st, nodes = call("GET", f"/api/gateways/{a['id']}/nodes", token=tok)
d3 = dev + 1; d4 = dev + 2
gA.s.sendall(pkt(d3, 3, 1)); gA.pump(0.3)
gA.s.sendall(frame(d3, 13, bytes([0xAA, 0xBB, 0xCC, 0x01, 0x02, 0x03]))); gA.pump(0.3)
caps3 = [{"type": "digitalOutput", "pin": 2, "label": "Lamp"}, {"type": "relay", "pin": 32, "label": "Fan"}]
st, r = call("POST", f"/api/gateways/{a['id']}/provision", {"deviceId": d3, "name": "Orig", "capabilitiesConfig": caps3}, tok)
gA.s.sendall(pkt(d3, 1, 1)); gA.pump(0.6)
st, nodes = call("GET", f"/api/gateways/{a['id']}/nodes", token=tok)
n3 = next((x for x in nodes if x["deviceId"] == d3), None)
st, det3 = call("GET", f"/api/nodes/{n3['nodeId']}", token=tok) if n3 else (0, {})
check("node MAC (sent while still unpaired) is stored", det3.get("mac") == "AA:BB:CC:01:02:03", det3.get("mac"))
st, gws = call("GET", "/api/gateways", token=tok)
gA.s.sendall(b"GWMAC:11:22:33:44:55:66\n"); gA.pump(0.4)
st, gws = call("GET", "/api/gateways", token=tok)
check("gateway MAC stored", any(g["id"] == a["id"] and g.get("mac") == "11:22:33:44:55:66" for g in gws), [g.get("mac") for g in gws])

# reset node that the hub still knows: offered again with its saved name
gA.s.sendall(pkt(d3, 3, 1)); gA.pump(0.4)
st, disc = call("GET", f"/api/gateways/{a['id']}/discovered", token=tok)
check("reset node the hub still knows is offered again with its saved name", any(d["deviceId"] == d3 and d.get("known") and d.get("knownName") == "Orig" for d in disc), disc)
gA.clear()
st, r = call("POST", f"/api/gateways/{a['id']}/provision", {"deviceId": d3}, tok)
gA.pump(0.4)
check("re-pair with no settings sends the saved modules", st == 200 and "Orig" in gA.text() and "Lamp" in gA.text() and "Fan" in gA.text(), gA.text()[:200])

# a new node takes over the old node's settings with its code
code3 = det3.get("pairCode")
gA.s.sendall(pkt(d4, 3, 1)); gA.pump(0.3)
st, r = call("POST", f"/api/gateways/{a['id']}/provision", {"deviceId": d4, "adoptCode": "nope"}, tok)
check("unknown adopt code refused", st == 404 and r.get("status") == "adopt_invalid", (st, r))
st, r = call("POST", f"/api/gateways/{a['id']}/provision", {"deviceId": d4, "adoptCode": code3}, otok)
check("another account cannot adopt it", st == 404, (st, r))
gA.clear()
st, r = call("POST", f"/api/gateways/{a['id']}/provision", {"deviceId": d4, "adoptCode": code3.lower()}, tok)
gA.pump(0.4)
check("adopt accepted", st == 200, (st, r))
check("new node is sent the old node's name and modules", "Orig" in gA.text() and "Lamp" in gA.text() and "Fan" in gA.text(), gA.text()[:200])
gA.s.sendall(pkt(d4, 1, 1)); gA.pump(0.6)
st, nodes = call("GET", f"/api/gateways/{a['id']}/nodes", token=tok)
check("new node active with the old name", any(x["deviceId"] == d4 and x["name"] == "Orig" for x in nodes), [(x["deviceId"], x["name"]) for x in nodes])
check("old node record replaced", not any(x["deviceId"] == d3 for x in nodes))

# reload saved settings onto a node that lost them
st, nodes = call("GET", f"/api/gateways/{a['id']}/nodes", token=tok)
n4 = next(x for x in nodes if x["deviceId"] == d4)
gA.clear()
st, r = call("POST", f"/api/nodes/{n4['nodeId']}/reload-config", token=tok); gA.pump(0.4)
check("reload sends the saved key, name and modules to the node", st == 200 and f"BB:{d4}:" in gA.text() and "Orig" in gA.text() and "Lamp" in gA.text() and f"PCODE:{d4}:" in gA.text(), (st, gA.text()[:200]))
st, r = call("POST", f"/api/nodes/{n4['nodeId']}/reload-config", token=otok)
check("another account cannot reload it", st == 404, st)
gA.clear()
st, r = call("POST", f"/api/gateways/{a['id']}/reload-nodes", token=tok); gA.pump(1.5)
check("gateway-wide reload reaches every node", st == 200 and r.get("count", 0) >= 1 and "BB:" in gA.text(), (st, r))

# ── deleting a gateway keeps its nodes ───────────────────────────────────────
e = call("POST", "/api/gateways", {"name": "PC-E"}, tok)[1]
f = call("POST", "/api/gateways", {"name": "PC-F"}, tok)[1]
gE, gF = Gw(e["apiKey"]), Gw(f["apiKey"])
for g in (gE, gF): g.pump(0.5)
dx = dev + 10
gE.s.sendall(pkt(dx, 3, 1)); gE.pump(0.3)
call("POST", f"/api/gateways/{e['id']}/provision", {"deviceId": dx, "name": "Kept", "capabilitiesConfig": caps3}, tok)
gE.pump(0.3)
codex = re.search(r"PCODE:%d:([A-Z0-9]+)" % dx, gE.text()).group(1)
gE.s.sendall(pkt(dx, 1, 1)); gE.pump(0.5)
gE.clear()
st, r = call("DELETE", f"/api/gateways/{e['id']}", token=tok); gE.pump(0.5)
check("gateway deleted", st == 200 and r.get("keptNodes") == 1, (st, r))
reset = bytes([0xAA, 2]) + struct.pack("<I", dx) + struct.pack("<H", 99)
check("its nodes were NOT reset", reset not in gE.buf, gE.buf[:40])
st, gl = call("GET", "/api/gateways", token=tok)
check("gateway is gone", not any(g["id"] == e["id"] for g in gl))
st, un = call("GET", "/api/nodes/unassigned", token=tok)
u = next((x for x in un if x["deviceId"] == dx), None)
check("node is listed as unassigned with its name and code", u and u["name"] == "Kept" and u["pairCode"] == codex and u["modules"] == 2, un)
st, _ = call("GET", "/api/nodes/unassigned", token=otok)
check("other accounts do not see it", _ == [] or not any(x["deviceId"] == dx for x in _), _)
st, r = call("POST", f"/api/nodes/unassigned/{u['id']}/assign", {"gatewayId": f["id"]}, otok)
check("another account cannot assign it", st == 404, (st, r))
gF.clear()
st, r = call("POST", f"/api/nodes/unassigned/{u['id']}/assign", {"gatewayId": f["id"]}, tok); gF.pump(0.4)
check("assign to another gateway sends the saved setup and the node's code", st == 200 and "Kept" in gF.text() and "Lamp" in gF.text() and ("PCODE:%d:%s" % (dx, codex)) in gF.text(), (st, r, gF.text()[:200]))
gF.s.sendall(pkt(dx, 1, 2)); gF.pump(0.6)
st, nodes = call("GET", f"/api/gateways/{f['id']}/nodes", token=tok)
nx = next((x for x in nodes if x["deviceId"] == dx), None)
check("node is now on the new gateway with its name", nx and nx["name"] == "Kept", nodes)
st, detx = call("GET", f"/api/nodes/{nx['nodeId']}", token=tok) if nx else (0, {})
check("and keeps the same pairing code", detx.get("pairCode") == codex, detx.get("pairCode"))
st, un = call("GET", "/api/nodes/unassigned", token=tok)
check("it is no longer unassigned", not any(x["deviceId"] == dx for x in un), un)
print("FAILS", fails); sys.exit(1 if fails else 0)
