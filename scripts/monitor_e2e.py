#!/usr/bin/env python3
"""E2E for the monitoring dashboard API: widgets per module, symbols, hide/show, gateway scope.
Usage: monitor_e2e.py [http_port] [tcp_port]   (hub must be running)"""
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


DEV = 7500000 + RUN % 400000
caps = [{"type": "digitalOutput", "pin": 2, "label": "Lamp", "symbol": "lightbulb"},   # 0
        {"type": "analogOutput", "pin": 26, "label": "Dimmer"},                      # 1
        {"type": "neopixel", "pin": 27, "count": 8, "label": "Strip"},               # 2
        {"type": "dht", "pin": 4, "extra": 22, "label": "Climate"},                  # 3
        {"type": "digitalInput", "pin": 35, "label": "Door", "symbol": "door"},      # 4
        {"type": "i2c", "extra": 60},                                                # 5 (not monitorable)
        {"type": "ld2410", "pin": 16, "pin2": 17, "label": "Radar"}]                 # 6
s, r = call("POST", f"/api/gateways/{gid}/provision", {"deviceId": DEV, "name": "Hall", "capabilitiesConfig": caps}, tok)
check("symbols are accepted on provision", s == 200, (s, r))
s, r = call("POST", f"/api/gateways/{gid}/provision", {"deviceId": DEV + 1, "name": "Bad", "capabilitiesConfig": [{"type": "relay", "pin": 32, "symbol": "<script>"}]}, tok)
check("a bad symbol is rejected", s == 400, (s, r))
gw.pump(0.5); gw.s.sendall(pkt(DEV)); time.sleep(0.6)
gw.s.sendall(readings(DEV, [(3, 0, 2345), (3, 1, 6120), (6, 0, 1), (6, 5, 140)])); time.sleep(0.5)

s, m = call("GET", "/api/monitor", token=tok)
check("monitor needs login", call("GET", "/api/monitor")[0] == 401)
check("monitor lists the gateway", s == 200 and [g["id"] for g in m["gateways"]] == [gid], m)
W = {w["id"]: w for w in m["widgets"]}
kinds = sorted(w["kind"] for w in m["widgets"])
by = lambda lbl: next((w for w in m["widgets"] if w["label"] == lbl), None)
check("only modules that can be watched or controlled are listed (no raw I2C)", all(w["cap"] != 5 for w in m["widgets"]), kinds)
check("lamp is a switch with the chosen symbol", by("Lamp") and by("Lamp")["kind"] == "switch" and by("Lamp")["symbol"] == "lightbulb" and by("Lamp")["control"], by("Lamp"))
check("outputs never commanded still load with a KNOWN state (off), not unknown", by("Lamp")["hasValue"] and by("Lamp")["on"] is False and by("Dimmer")["hasValue"] and by("Dimmer")["value"] == 0, (by("Lamp"), by("Dimmer")))
check("dimmer is a slider with a default symbol", by("Dimmer") and by("Dimmer")["kind"] == "slider" and by("Dimmer")["symbol"] == "sliders", by("Dimmer"))
check("NeoPixel is a light", by("Strip") and by("Strip")["kind"] == "light", by("Strip"))
check("door input is a read-only on/off", by("Door") and by("Door")["kind"] == "bool" and by("Door")["symbol"] == "door" and not by("Door")["control"], by("Door"))
t = next((w for w in m["widgets"] if w["cap"] == 3 and w["unit"] == "°C"), None)
h = next((w for w in m["widgets"] if w["cap"] == 3 and w["unit"] == "%"), None)
check("temperature and humidity are gauges with values", t and h and t["kind"] == "gauge" and abs(t["value"] - 23.45) < .01 and abs(h["value"] - 61.2) < .01 and t["symbol"] == "thermometer" and h["symbol"] == "droplet", (t, h))
pr = next((w for w in m["widgets"] if w["cap"] == 6 and w["kind"] == "bool"), None)
check("sensor tiles say which channel they show and that they have history (for the charts)", t and h and t["history"] and t["channel"] == 0 and h["channel"] == 1 and not any(w["history"] for w in m["widgets"] if w["kind"] in ("switch", "slider", "light")), (t, h))
check("radar presence is an on/off indicator", pr and pr["on"] is True, pr)
check("radar energy channels are not on the dashboard", not any(w["cap"] == 6 and w["kind"] == "gauge" and "energy" in w["label"].lower() for w in m["widgets"]))
check("nothing is hidden at first", not any(w["hidden"] for w in m["widgets"]) and m["prefs"]["gateway"] == "all", m["prefs"])

# prefs: hide a tile, choose a gateway
lid = by("Lamp")["id"]
s, _ = call("PUT", "/api/monitor/prefs", {"gateway": gid, "hidden": [lid]}, tok)
s2, m2 = call("GET", "/api/monitor", token=tok)
check("hiding a tile and picking a gateway is remembered", s == 200 and m2["prefs"]["gateway"] == gid and next(w for w in m2["widgets"] if w["id"] == lid)["hidden"], m2["prefs"])
s, _ = call("PUT", "/api/monitor/prefs", {"gateway": gid, "hidden": [], "order": ["zzz", "yyy"], "tiles": {"zzz": ["a", "b"]}}, tok)
pf = call("GET", "/api/monitor", token=tok)[1]["prefs"]
check("card order and tile order are stored", s == 200 and pf["order"] == ["zzz", "yyy"] and pf["tiles"] == {"zzz": ["a", "b"]}, pf)
call("PUT", "/api/monitor/prefs", {"gateway": "nope", "hidden": []}, tok)
check("an unknown gateway falls back to all", call("GET", "/api/monitor", token=tok)[1]["prefs"]["gateway"] == "all")
s, om = call("GET", "/api/monitor", token=otok)
check("another account sees none of it", s == 200 and om["widgets"] == [] and om["gateways"] == [], om)

# changing the symbol of a module in the node settings
caps[0]["symbol"] = "power"
s, _ = call("PUT", f"/api/nodes/{[n for n in call('GET', f'/api/gateways/{gid}/nodes', token=tok)[1] if n['deviceId']==DEV][0]['nodeId']}/capabilities", {"capabilitiesConfig": caps}, tok)
check("symbol can be changed later", s == 200 and by.__call__ and next(w for w in call("GET", "/api/monitor", token=tok)[1]["widgets"] if w["label"] == "Lamp")["symbol"] == "power", s)

# controls the tiles use
nid = [n for n in call('GET', f'/api/gateways/{gid}/nodes', token=tok)[1] if n['deviceId']==DEV][0]['nodeId']
gw.clear()
s, _ = call("POST", f"/api/nodes/{nid}/command", {"pin": 2, "value": 1}, tok); gw.pump(0.4)
check("switch tile sends a pin command", s == 200 and (2, 1) in gw.pincmds(DEV), gw.pincmds(DEV))
time.sleep(0.2)
lamp = next(w for w in call("GET", "/api/monitor", token=tok)[1]["widgets"] if w["label"] == "Lamp")
check("switch state is reflected", lamp["on"] is True and lamp["hasValue"], lamp)
s, _ = call("POST", f"/api/nodes/{nid}/action", {"cap": 2, "action": "setEffect", "params": {"pattern": 3, "color": "#00ff00", "brightness": 80}}, tok)
st = next(w for w in call("GET", "/api/monitor", token=tok)[1]["widgets"] if w["label"] == "Strip")
check("light tile shows pattern, brightness, colour", st["on"] and st["value"] == 80 and st["pattern"] == "Breathe" and st["color"].lower() == "#00ff00" and st["params"]["pattern"] == 3, st)

print("\n%s" % ("ALL PASSED" if not fails else f"{fails} FAILED")); sys.exit(1 if fails else 0)
