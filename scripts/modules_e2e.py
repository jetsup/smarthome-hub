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

email = f"m{RUN}@example.com"; other = f"mo{RUN}@example.com"
for e in (email, other): call("POST", "/api/auth/register", {"firstName": "M", "lastName": "U", "email": e, "password": "secret1", "confirmPassword": "secret1"})
tok = call("POST", "/api/auth/login", {"email": email, "password": "secret1"})[1]["token"]
otok = call("POST", "/api/auth/login", {"email": other, "password": "secret1"})[1]["token"]
d = call("POST", "/api/gateways", {"name": "Mod-GW"}, tok)[1]; gid, key = d["id"], d["apiKey"]
gw = Gw(key); gw.pump(1.0)
check("gateway is told the system time when it connects", "TIME:" in gw.text(), gw.text()[:80])

# ── catalog ─────────────────────────────────────────────────────────────────
s, cat = call("GET", "/api/capabilities/catalog", token=tok)
keys = {t["key"] for t in cat["types"]} if s == 200 else set()
check("catalog lists the modules", {"neopixel", "buzzer", "dht", "ld2410", "ds3231", "analogInput"} <= keys, keys)
neo = next(t for t in cat["types"] if t["key"] == "neopixel")
pats = neo["actions"][0]["params"][0]["choices"]
check("NeoPixel catalog has strip and ring patterns", any(p.get("ring") for p in pats) and any(not p.get("ring") for p in pats) and len(pats) >= 20, len(pats))
check("catalog needs login", call("GET", "/api/capabilities/catalog")[0] == 401)

# ── validation ──────────────────────────────────────────────────────────────
DEV = 7000000 + RUN % 900000
def prov(caps, dev=DEV, name="Mod node"):
    return call("POST", f"/api/gateways/{gid}/provision", {"deviceId": dev, "name": name, "capabilitiesConfig": caps}, tok)
check("NeoPixel needs an LED count", prov([{"type": "neopixel", "pin": 27, "count": 0}])[0] == 400)
check("a ring needs 3+ LEDs", prov([{"type": "neopixel", "pin": 27, "count": 2, "ring": True}])[0] == 400)
check("NeoPixel data pin must be an output pin", prov([{"type": "neopixel", "pin": 35, "count": 8}])[0] == 400)
check("only one LD2410 per node", prov([{"type": "ld2410", "pin": 16, "pin2": 17}, {"type": "ld2410", "pin": 18, "pin2": 19}])[0] == 400)
check("the same GPIO cannot be used by two modules", prov([{"type": "dht", "pin": 4}, {"type": "buzzer", "pin": 4}])[0] == 400)
check("DHT model must be 11 or 22", prov([{"type": "dht", "pin": 4, "extra": 33}])[0] == 400)
check("unknown module type rejected", prov([{"type": "laserCannon", "pin": 4}])[0] == 400)

caps = [{"type": "neopixel", "pin": 27, "count": 16, "ring": True, "label": "Ring"},   # 0
        {"type": "dht", "pin": 4, "extra": 22, "label": "Climate"},                    # 1
        {"type": "ds3231", "pin": 21, "pin2": 22, "label": "Clock"},                   # 2
        {"type": "buzzer", "pin": 33, "label": "Buzz"},                                # 3
        {"type": "analogInput", "pin": 34, "label": "Pot"},                            # 4
        {"type": "analogInput", "pin": 35, "label": "Light"},                          # 5
        {"type": "ld2410", "pin": 16, "pin2": 17, "label": "Radar"}]                   # 6
gw.clear()
s, r = prov(caps); check("a node with many modules can be provisioned", s == 200, (s, r))
gw.pump(0.6)
bb = [l for l in gw.text().split("\n") if l.startswith("BB:")]
check("the gateway gets the extended capability string (count, flags, pin2)", bool(bb) and ":9:27:0:16:1:0:Ring:" in bb[0] and ":11:4:22:0:0:0:Climate:" in bb[0] and ":12:16:0:0:0:17:Radar" in bb[0], bb[:1])
gw.s.sendall(pkt(DEV)); time.sleep(0.6)  # first telemetry activates the node
s, nodes = call("GET", f"/api/gateways/{gid}/nodes", token=tok)
node = next((n for n in nodes if n["deviceId"] == DEV), None)
check("node is active", node is not None)
nodeId = node["nodeId"]

# ── readings ────────────────────────────────────────────────────────────────
gw.s.sendall(readings(DEV, [(1, 0, 2345), (1, 1, 6120), (4, 0, 2048), (5, 0, 311), (6, 0, 1), (6, 1, 140)])); time.sleep(0.5)
s, nd = call("GET", f"/api/nodes/{nodeId}", token=tok)
rd = {(r["cap"], r["channel"]): r for r in nd.get("readings", [])}
check("temperature and humidity arrive scaled", rd.get((1, 0), {}).get("value") == 23.45 and rd.get((1, 1), {}).get("value") == 61.2, rd.get((1, 0)))
check("readings carry names and units", rd.get((1, 0), {}).get("key") == "temperature" and rd.get((1, 0), {}).get("unit") == "°C", rd.get((1, 0)))
check("radar presence and distance arrive", rd.get((6, 0), {}).get("value") == 1 and rd.get((6, 1), {}).get("value") == 140, rd.get((6, 0)))
pv = {c["pin"]: c.get("value") for c in nd["capabilitiesConfig"] if c["type"] == "analogInput"}
check("every analog input pin has its OWN value", pv.get(34) == 2048 and pv.get(35) == 311, pv)
gw.s.sendall(readings(DEV, [(9, 0, 5)])); time.sleep(0.3)
check("a reading for a missing module is ignored", True)

# ── actions ─────────────────────────────────────────────────────────────────
gw.clear()
s, r = call("POST", f"/api/nodes/{nodeId}/action", {"cap": 0, "action": "setEffect", "params": {"pattern": 22, "color": "#ff8000", "speed": 70, "brightness": 40, "reverse": True}}, tok)
gw.pump(0.5); fr = gw.frames(9)
check("NeoPixel ring pattern is sent to the node", s == 200 and len(fr) == 1 and list(fr[0][2]) == [0, 1, 22, 255, 128, 0, 70, 40, 1], (s, r, [list(f[2]) for f in fr]))
s, nd = call("GET", f"/api/nodes/{nodeId}", token=tok)
check("the last setting is remembered for the UI", nd["effects"].get("0", {}).get("pattern") == 22 and nd["effects"]["0"]["color"] == "#FF8000", nd.get("effects"))
s, _ = call("POST", f"/api/nodes/{nodeId}/action", {"cap": 0, "action": "setEffect", "params": {"pattern": 999}}, tok); check("unknown pattern rejected", s == 400, s)
s, _ = call("POST", f"/api/nodes/{nodeId}/action", {"cap": 3, "action": "setEffect", "params": {}}, tok); check("an action the module doesn't have is rejected", s == 400, s)
s, _ = call("POST", f"/api/nodes/{nodeId}/action", {"cap": 42, "action": "setEffect", "params": {}}, tok); check("action on a missing module rejected", s == 400, s)
s, _ = call("POST", f"/api/nodes/{nodeId}/action", {"cap": 0, "action": "setEffect", "params": {"pattern": 5}}, otok); check("another account cannot control the node", s == 404, s)
gw.clear()
s, r = call("POST", f"/api/nodes/{nodeId}/action", {"cap": 3, "action": "play", "params": {"pattern": 4, "repeat": 3}}, tok); gw.pump(0.4)
check("buzzer sound is sent", s == 200 and [list(f[2]) for f in gw.frames(9)] == [[3, 1, 4, 3]], (s, r))

# ring-only pattern on a plain strip
strip = DEV + 1
prov([{"type": "neopixel", "pin": 27, "count": 30, "label": "Strip"}], dev=strip, name="Strip node"); gw.s.sendall(pkt(strip)); time.sleep(0.6)
sid = next(n["nodeId"] for n in call("GET", f"/api/gateways/{gid}/nodes", token=tok)[1] if n["deviceId"] == strip)
s, r = call("POST", f"/api/nodes/{sid}/action", {"cap": 0, "action": "setEffect", "params": {"pattern": 20}}, tok); check("ring-only patterns are refused on a straight strip", s == 400 and "ring" in json.dumps(r).lower(), r)
s, r = call("POST", f"/api/nodes/{sid}/action", {"cap": 0, "action": "setEffect", "params": {"pattern": 8}}, tok); check("strip patterns work on a strip", s == 200, r)

# ── shared clock (RTC) ──────────────────────────────────────────────────────
s, t0 = call("GET", "/api/system/time", token=tok); check("system time defaults to the hub clock", s == 200 and t0["source"] in ("hub", "rtc"), t0)
rtc_epoch = int(time.time()) - 37
gw.s.sendall(readings(DEV, [(2, 0, rtc_epoch), (2, 1, 2450), (2, 2, 1)])); time.sleep(0.5)
s, t1 = call("GET", "/api/system/time", token=tok)
check("the RTC becomes the system clock", t1["source"] == "rtc" and t1.get("rtcNodeId") == nodeId and abs(t1["driftSeconds"] + 37) < 3, t1)
gw.clear()
s, r = call("POST", f"/api/nodes/{nodeId}/action", {"cap": 2, "action": "syncTime"}, tok); gw.pump(0.4)
fr = [f for f in gw.frames(9) if f[2][0] == 2]
ep = struct.unpack("<I", bytes(fr[0][2][2:6]))[0] if fr else 0
check("RTC can be set from the system clock", s == 200 and bool(fr) and fr[0][2][0] == 2 and fr[0][2][1] == 2 and abs(ep - time.time()) < 100, (s, r, [list(f[2]) for f in fr], ep))

# ── rules: modules talking to each other ────────────────────────────────────
rule = {"name": "Humid -> alarm", "sourceDeviceId": DEV, "sourceCap": 1, "channel": 1, "op": ">", "threshold": 65,
        "targetDeviceId": DEV, "targetCap": 3, "onTrue": {"action": "play", "params": {"pattern": 4, "repeat": 2}},
        "onFalse": {"action": "play", "params": {"pattern": 0}}}
s, r = call("POST", f"/api/gateways/{gid}/rules", rule, tok); check("create a rule between two modules", s == 201 and len(r.get("slug", "")) == 12, (s, r))
slug = r.get("slug")
check("rule with a bad operator rejected", call("POST", f"/api/gateways/{gid}/rules", {**rule, "op": "~"}, tok)[0] == 400)
check("rule on a reading the module doesn't have is rejected", call("POST", f"/api/gateways/{gid}/rules", {**rule, "channel": 9}, tok)[0] == 400)
check("another account cannot add rules to my gateway", call("POST", f"/api/gateways/{gid}/rules", rule, otok)[0] == 404)
gw.s.sendall(readings(DEV, [(1, 1, 5000)])); time.sleep(0.4); gw.clear()   # humidity 50: condition false (first time)
gw.s.sendall(readings(DEV, [(1, 1, 7000)])); time.sleep(0.8); gw.pump(0.3)   # 70 > 65: fires
fr = [list(f[2]) for f in gw.frames(9)]
check("rule fires when humidity rises above 65 (buzzer alarm)", [3, 1, 4, 2] in fr, fr)
gw.clear(); gw.s.sendall(readings(DEV, [(1, 1, 7200)])); time.sleep(0.5); gw.pump(0.3)
check("rule does not repeat while the condition stays true", gw.frames(9) == [], [list(f[2]) for f in gw.frames(9)])
gw.clear(); gw.s.sendall(readings(DEV, [(1, 1, 4000)])); time.sleep(0.6); gw.pump(0.3)
check("rule's 'otherwise' action runs when it clears (stop sound)", [3, 1, 0, 1] in [list(f[2]) for f in gw.frames(9)], [list(f[2]) for f in gw.frames(9)])
s, r = call("PUT", f"/api/rules/{slug}/enabled", {"enabled": False}, tok); check("rule can be disabled", s == 200 and r["enabled"] is False, r)
gw.clear(); gw.s.sendall(readings(DEV, [(1, 1, 9000)])); time.sleep(0.5); gw.pump(0.3)
check("a disabled rule does nothing", gw.frames(9) == [])
s, rl = call("GET", f"/api/gateways/{gid}/rules", token=tok); check("rules are listed", s == 200 and len(rl) == 1, rl)
check("another account cannot delete my rule", call("DELETE", f"/api/rules/{slug}", token=otok)[0] == 404)
check("owner deletes the rule", call("DELETE", f"/api/rules/{slug}", token=tok)[0] == 200)


# ── a restarted node gets its outputs back ─────────────────────────────────
RDEV = DEV + 2
prov([{"type": "digitalOutput", "pin": 26, "label": "LED"}, {"type": "neopixel", "pin": 27, "count": 12, "ring": True, "label": "Ring"}], dev=RDEV, name="Restart node")
gw.s.sendall(pkt(RDEV)); time.sleep(0.6)
rid = next(n["nodeId"] for n in call("GET", f"/api/gateways/{gid}/nodes", token=tok)[1] if n["deviceId"] == RDEV)
call("POST", f"/api/nodes/{rid}/command", {"pin": 26, "value": 1}, tok)
call("POST", f"/api/nodes/{rid}/action", {"cap": 1, "action": "setEffect", "params": {"pattern": 20, "color": "#00ff00", "speed": 60, "brightness": 30}}, tok)
gw.pump(0.8); gw.clear()
gw.s.sendall(frame(RDEV, 12, bytes([1]))); time.sleep(0.8); gw.pump(0.4)
check("a restarted node gets its LED switched back on", (26, 1) in gw.pincmds(RDEV), gw.pincmds(RDEV))
check("...and its NeoPixel pattern restored", [list(f[2]) for f in gw.frames(9)] == [[1, 1, 20, 0, 255, 0, 60, 30, 0]], [list(f[2]) for f in gw.frames(9)])
gw.clear(); gw.s.sendall(frame(RDEV, 12, bytes([1]))); time.sleep(0.6); gw.pump(0.3)
check("the repeated boot frames are ignored", gw.pincmds(RDEV) == [] and gw.frames(9) == [], gw.pincmds(RDEV))
call("POST", f"/api/nodes/{rid}/command", {"pin": 26, "value": 0}, tok); time.sleep(1.5)

# ── pins can still be edited, and a removed module drops its rules ─────────
s, r = call("PUT", f"/api/nodes/{nodeId}/capabilities", {"capabilitiesConfig": caps[:2] + [{"type": "digitalOutput", "pin": 26, "label": "LED"}]}, tok)
check("module list can be edited later", s == 200, (s, r))
print("\nFAILED" if fails else "\nALL PASSED", fails)
sys.exit(1 if fails else 0)
