#!/usr/bin/env python3
"""E2E for gateway data logging: batches stored with their own time, acknowledgements, duplicates,
settings pushed to the gateway, history API.
Usage: datalog_e2e.py [http_port] [tcp_port]   (hub must be running)"""
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


DEV = 7600000 + RUN % 300000
caps = [{"type": "dht", "pin": 4, "extra": 22, "label": "Climate"}]
call("POST", f"/api/gateways/{gid}/provision", {"deviceId": DEV, "name": "Logger", "capabilitiesConfig": caps}, tok)
gw.pump(0.5); gw.s.sendall(pkt(DEV)); time.sleep(0.6)
nid = [n for n in call("GET", f"/api/gateways/{gid}/nodes", token=tok)[1] if n["deviceId"] == DEV][0]["nodeId"]

# settings: defaults, pushed on connect, validation, change pushed live
s, lg = call("GET", f"/api/gateways/{gid}/logging", token=tok)
check("default: flash, 256 KB, RTC on 21/22", s == 200 and lg["store"] == 0 and lg["maxKb"] == 256 and lg["rtcSda"] == 21 and lg["rtcScl"] == 22, lg)
gw.pump(0.3)
check("the gateway is sent its log settings when it connects", "LOGCFG:0:256:5:21:22" in gw.text(), gw.text()[:200])
check("space limit below 32 KB rejected", call("PUT", f"/api/gateways/{gid}/logging", {"store": 0, "maxKb": 8, "sdCs": 5, "rtcSda": 21, "rtcScl": 22}, tok)[0] == 400)
check("flash limit above 1 MB rejected", call("PUT", f"/api/gateways/{gid}/logging", {"store": 0, "maxKb": 4096, "sdCs": 5, "rtcSda": 21, "rtcScl": 22}, tok)[0] == 400)
check("a flash-chip pin is rejected", call("PUT", f"/api/gateways/{gid}/logging", {"store": 0, "maxKb": 256, "sdCs": 5, "rtcSda": 9, "rtcScl": 22}, tok)[0] == 400)
check("RTC pins must differ", call("PUT", f"/api/gateways/{gid}/logging", {"store": 0, "maxKb": 256, "sdCs": 5, "rtcSda": 21, "rtcScl": 21}, tok)[0] == 400)
check("SD chip select cannot clash with the SPI pins", call("PUT", f"/api/gateways/{gid}/logging", {"store": 1, "maxKb": 4096, "sdCs": 18, "rtcSda": 21, "rtcScl": 22}, tok)[0] == 400)
gw.clear()
s, r = call("PUT", f"/api/gateways/{gid}/logging", {"store": 1, "maxKb": 8192, "sdCs": 15, "rtcSda": 32, "rtcScl": 33}, tok); gw.pump(0.4)
check("a larger SD limit is accepted and sent to the gateway", s == 200 and r["store"] == 1 and "LOGCFG:1:8192:15:32:33" in gw.text(), (s, r, gw.text()))
check("another account cannot read it", call("GET", f"/api/gateways/{gid}/logging", token=otok)[0] == 404)
call("PUT", f"/api/gateways/{gid}/logging", {"store": 0, "maxKb": 256, "sdCs": 5, "rtcSda": 21, "rtcScl": 22}, tok)

# a batch the gateway stored while the hub was away
now = int(time.time())
t0 = now - 3 * 3600
rec = lambda t, cap, ch, v, est=0: f"{t},{est},{DEV},{cap},{ch},1,{v}"
gw.clear()
batch = ";".join([rec(t0, 0, 0, 2210), rec(t0, 0, 1, 5530), rec(t0 + 600, 0, 0, 2260), rec(t0 + 600, 0, 1, 5480, 1)])
gw.s.sendall(f"LOG:1:4:{batch}\n".encode()); gw.pump(0.6)
check("the hub acknowledges the batch (so the gateway can clear it)", "LOGACK:1" in gw.text(), gw.text()[-100:])
s, h = call("GET", f"/api/nodes/{nid}/history?cap=0&channel=0&hours=6", token=tok)
pts = h["points"] if s == 200 else []
check("history has the readings with the time they were MEASURED", [p["t"] for p in pts] == [t0, t0 + 600] and [p["v"] for p in pts] == [22.1, 22.6], h)
check("values are scaled to real units and carry the unit", h.get("unit") == "°C", h)
s, h1 = call("GET", f"/api/nodes/{nid}/history?cap=0&channel=1&hours=6", token=tok)
check("a time worked out afterwards is flagged as estimated", any(p.get("estimated") for p in h1["points"]) and not any(p.get("estimated") for p in pts), h1)

# the same batch again (acknowledgement was lost): stored once
gw.clear()
gw.s.sendall(f"LOG:2:4:{batch}\n".encode()); gw.pump(0.6)
s, h = call("GET", f"/api/nodes/{nid}/history?cap=0&channel=0&hours=6", token=tok)
check("a repeated batch is acknowledged but not stored twice", "LOGACK:2" in gw.text() and len(h["points"]) == 2, (gw.text()[-60:], len(h["points"])))
check("nonsense times are ignored", (gw.s.sendall(f"LOG:3:1:5,0,{DEV},0,0,1,9\n".encode()), gw.pump(0.4), "LOGACK:3" in gw.text())[2])
check("history of one channel excludes the other", all(abs(p["v"] - 55) < 2 for p in call("GET", f"/api/nodes/{nid}/history?cap=0&channel=1&hours=6", token=tok)[1]["points"]))
check("history only from the asked window", len(call("GET", f"/api/nodes/{nid}/history?cap=0&channel=0&hours=1", token=tok)[1]["points"]) == 0)
check("another account cannot read the history", call("GET", f"/api/nodes/{nid}/history?cap=0", token=otok)[0] == 404)

# bucketed history: a window averaged into slices, with the lowest/highest value of each
gw.s.sendall(f"LOG:4:3:{rec(t0 + 1200, 0, 0, 1000)};{rec(t0 + 1260, 0, 0, 3000)};{rec(t0 + 7200, 0, 0, 2000)}\n".encode()); gw.pump(0.5)
s, hb = call("GET", f"/api/nodes/{nid}/history?cap=0&channel=0&hours=6&buckets=12", token=tok)
bp = hb["points"] if s == 200 else []
check("buckets cover the window and carry min/max", s == 200 and 1 < len(bp) <= 12 and all("lo" in p and "hi" in p for p in bp) and hb["to"] > hb["from"], hb)
check("a bucket's value is the average of its readings (22.6, 10, 30), with min and max", any(abs(p["v"] - (22.6 + 10 + 30) / 3) < .01 and abs(p["lo"] - 10) < .01 and abs(p["hi"] - 30) < .01 for p in bp), bp)

# status line from the gateway
gw.s.sendall(b"LOGSTAT:12:0:256:2:1:340\n"); time.sleep(0.4)
st = call("GET", f"/api/gateways/{gid}/logging", token=tok)[1].get("status")
check("the gateway's own status (records waiting, RTC, time source) is shown", st and st["pending"] == 12 and st["rtc"] == 2 and st["timeSource"] == 1 and st["uploaded"] == 340, st)

print("\n%s" % ("ALL PASSED" if not fails else f"{fails} FAILED")); sys.exit(1 if fails else 0)
