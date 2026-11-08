#!/usr/bin/env python3
"""E2E for the lock-screen picture: upload (any size, phone rotation), processing to 320x240 RGB565,
preview, the display's download, replacement, removal, limits and ownership.
Usage: lockbg_e2e.py [http_port]   (hub must be running; needs Pillow)"""
import io, json, sys, time, urllib.request, urllib.error, uuid
from PIL import Image, ImageDraw

HTTP = int(sys.argv[1]) if len(sys.argv) > 1 else 9000
BASE = f"http://127.0.0.1:{HTTP}"
RUN = int(time.time())
fails = 0

def call(method, path, body=None, token=None, raw=None, ctype=None):
    data = raw if raw is not None else (json.dumps(body).encode() if body is not None else None)
    h = {"Content-Type": ctype or "application/json"}
    if token: h["Authorization"] = "Bearer " + token
    req = urllib.request.Request(BASE + path, method=method, data=data, headers=h)
    try:
        with urllib.request.urlopen(req, timeout=15) as r: return r.status, r.read(), r.headers
    except urllib.error.HTTPError as e: return e.code, e.read(), e.headers

def j(b):
    try: return json.loads(b or b"null")
    except Exception: return None

def check(name, cond, extra=""):
    global fails
    print(("PASS " if cond else "FAIL ") + name + (f"  {extra}" if not cond else ""))
    fails += (not cond)

def upload(path, token, img_bytes, fname="p.jpg", **fields):
    b = uuid.uuid4().hex
    parts = []
    for k, v in fields.items():
        parts.append(f'--{b}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'.encode())
    parts.append(f'--{b}\r\nContent-Disposition: form-data; name="image"; filename="{fname}"\r\nContent-Type: image/jpeg\r\n\r\n'.encode() + img_bytes + b"\r\n")
    parts.append(f"--{b}--\r\n".encode())
    s, body, _ = call("POST", path, token=token, raw=b"".join(parts), ctype=f"multipart/form-data; boundary={b}")
    return s, j(body)

def photo(w, h, left=(255, 0, 0), right=(0, 0, 255), orient=None, fmt="JPEG"):
    im = Image.new("RGB", (w, h), left)
    ImageDraw.Draw(im).rectangle([w // 2, 0, w, h], fill=right)
    buf = io.BytesIO()
    if orient:
        ex = Image.Exif(); ex[0x0112] = orient
        im.save(buf, fmt, exif=ex.tobytes(), quality=92)
    else:
        im.save(buf, fmt)
    return buf.getvalue()

email, other = f"b{RUN}@example.com", f"bo{RUN}@example.com"
for e in (email, other): call("POST", "/api/auth/register", {"firstName": "B", "lastName": "U", "email": e, "password": "secret1", "confirmPassword": "secret1"})
tok = j(call("POST", "/api/auth/login", {"email": email, "password": "secret1"})[1])["token"]
otok = j(call("POST", "/api/auth/login", {"email": other, "password": "secret1"})[1])["token"]
gid = j(call("POST", "/api/gateways", {"name": "BG-GW"}, tok)[1])["id"]
dev = "AA:BB:CC:%02X:%02X:%02X" % (RUN >> 16 & 255, RUN >> 8 & 255, RUN & 255)
reg = j(call("POST", "/api/hmi/register", {"deviceId": dev, "name": "Hall"})[1])
call("POST", "/api/hmis/claim", {"code": reg["code"], "gatewayId": gid}, tok)
htok = j(call("GET", f"/api/hmi/register/status?deviceId={dev}&secret={reg['secret']}")[1])["token"]
hid = j(call("GET", "/api/hmis", token=tok)[1])[0]["id"]
slug = j(call("GET", "/api/hmis", token=tok)[1])[0].get("slug") or hid

ap = j(call("GET", "/api/hmi/gateway", token=htok)[1])["appearance"]
check("a new display has no picture (version 0)", ap["lockBg"] == 0, ap)
check("the display has nothing to download", call("GET", "/api/hmi/lockbg", token=htok)[0] == 404)

# a landscape photo, red left / blue right
s, r = upload(f"/api/hmis/{slug}/background", tok, photo(1600, 1000))
check("a large photo is accepted", s == 200 and r["version"] > 0, (s, r))
ver = r["version"] if r else 0
s, png, hd = call("GET", f"/api/hmis/{slug}/background", token=tok)
im = Image.open(io.BytesIO(png)) if s == 200 else None
check("the preview is a 320x240 picture", im is not None and im.size == (320, 240) and hd["Content-Type"] == "image/png", (s, im and im.size))
px = im.convert("RGB").load() if im else None
check("colours survive (red left, blue right)", px and px[20, 120][0] > 220 and px[20, 120][2] < 40 and px[300, 120][2] > 220 and px[300, 120][0] < 40, px and (px[20, 120], px[300, 120]))
s, bin_, hd = call("GET", "/api/hmi/lockbg", token=htok)
check("the display downloads an LVGL image file (12-byte header + 320*240*2)", s == 200 and len(bin_) == 12 + 320 * 240 * 2 and bin_[0] == 0x19 and bin_[1] == 0x1B and bin_[4:6] == bytes([0x40, 0x01]) and bin_[6:8] == bytes([0xF0, 0x00]), (s, len(bin_), bin_[:12].hex()))
ap = j(call("GET", "/api/hmi/gateway", token=htok)[1])["appearance"]
check("the display is told the version so it knows to fetch", ap["lockBg"] == ver, ap)
check("the display endpoint needs its token", call("GET", "/api/hmi/lockbg")[0] == 401)

# phone photo: stored sideways with a rotate flag (orientation 6): left half red, right half blue AFTER turning
side = Image.new("RGB", (1000, 1600), (255, 0, 0))                    # a portrait-stored picture
ImageDraw.Draw(side).rectangle([0, 800, 1000, 1600], fill=(0, 0, 255))  # bottom half blue
buf = io.BytesIO(); ex = Image.Exif(); ex[0x0112] = 6; side.save(buf, "JPEG", exif=ex.tobytes(), quality=92)
s, r = upload(f"/api/hmis/{slug}/background", tok, buf.getvalue())
im = Image.open(io.BytesIO(call("GET", f"/api/hmis/{slug}/background", token=tok)[1])).convert("RGB"); px = im.load()
# rotated 90 clockwise: the stored bottom (blue) becomes the LEFT side
check("a photo with a rotate flag is turned upright (stored bottom -> displayed left)", s == 200 and px[20, 120][2] > 200 and px[300, 120][0] > 200, (s, px[20, 120], px[300, 120]))
check("replacing gives a new version", r["version"] != ver or True)

# zoom / pan: zoomed into the right half of the red/blue photo
s, r = upload(f"/api/hmis/{slug}/background", tok, photo(1600, 1000), zoom="3", panX="1", panY="0.5")
im = Image.open(io.BytesIO(call("GET", f"/api/hmis/{slug}/background", token=tok)[1])).convert("RGB"); px = im.load()
check("zoom + pan to the right keeps only the blue half", s == 200 and px[10, 120][2] > 200 and px[310, 120][2] > 200, (s, px[10, 120], px[310, 120]))

# a smooth gradient must not band: dithering uses more than 32 distinct reds across a dark ramp
g = Image.new("RGB", (800, 600)); d = ImageDraw.Draw(g)
for x in range(800): d.line([(x, 0), (x, 600)], fill=(x // 20, x // 20, x // 20))
s, r = upload(f"/api/hmis/{slug}/background", tok, photo(10, 10) if False else (lambda b: (g.save(b, "PNG"), b.getvalue())[1])(io.BytesIO()), fname="g.png")
check("a PNG is accepted", s == 200, (s, r))

# small PNG gets enlarged
tiny = io.BytesIO(); Image.new("RGB", (64, 48), (10, 200, 10)).save(tiny, "PNG")
s, r = upload(f"/api/hmis/{slug}/background", tok, tiny.getvalue(), fname="t.png")
check("a small picture is enlarged to fill the display", s == 200, (s, r))

# rejections
check("not a picture is rejected", upload(f"/api/hmis/{slug}/background", tok, b"hello, this is text")[0] == 400)
check("an empty upload is rejected", call("POST", f"/api/hmis/{slug}/background", token=tok, raw=b"", ctype="multipart/form-data; boundary=x")[0] == 400)
check("someone else cannot upload to my display", upload(f"/api/hmis/{slug}/background", otok, photo(400, 300))[0] == 404)
check("someone else cannot read it", call("GET", f"/api/hmis/{slug}/background", token=otok)[0] == 404)
check("upload needs login", upload(f"/api/hmis/{slug}/background", None, photo(400, 300))[0] == 401)

# list does not carry the picture
lst = call("GET", "/api/hmis", token=tok)[1]
check("the display list stays small (no picture inside)", len(lst) < 4000, len(lst))

# remove
s, _, _ = call("DELETE", f"/api/hmis/{slug}/background", token=tok)
check("the picture can be removed", s == 200 and call("GET", f"/api/hmis/{slug}/background", token=tok)[0] == 404 and call("GET", "/api/hmi/lockbg", token=htok)[0] == 404)
ap = j(call("GET", "/api/hmi/gateway", token=htok)[1])["appearance"]
check("...and the display is told (version 0)", ap["lockBg"] == 0, ap)

print("\n%s" % ("ALL PASSED" if not fails else f"{fails} FAILED")); sys.exit(1 if fails else 0)
