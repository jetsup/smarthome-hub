#!/usr/bin/env python3
"""E2E for profile, signed-in devices, and two-step verification:
   phone approval (number matching), authenticator app (TOTP), email codes, recovery codes.
Usage: mfa_e2e.py [http_port]
Run the hub with SMTP_HOST=127.0.0.1 SMTP_PORT=2525 SMTP_SECURITY=none SMTP_FROM=no-reply@smarthome.test
to exercise the email path (this script starts the SMTP sink on :2525)."""
import base64, hashlib, hmac, json, os, re, socketserver, struct, sys, threading, time, urllib.request, urllib.error, email as emaillib
from email import policy

HTTP = int(sys.argv[1]) if len(sys.argv) > 1 else 9000
BASE = f"http://127.0.0.1:{HTTP}"
fails = 0

# ── a tiny SMTP sink so we can read the emails the hub sends ─────────────────
MAILS = []
class Sink(socketserver.StreamRequestHandler):
    def handle(self):
        w = lambda s: self.wfile.write((s + "\r\n").encode())
        w("220 sink ready"); data = None; buf = []
        for raw in self.rfile:
            line = raw.decode(errors="replace").rstrip("\r\n")
            if data is not None:
                if line == ".":
                    MAILS.append("\n".join(data)); data = None; w("250 queued")
                else:
                    data.append(line[1:] if line.startswith("..") else line)
                continue
            cmd = line.upper()
            if cmd.startswith(("EHLO", "HELO")): w("250 sink")
            elif cmd.startswith(("MAIL", "RCPT", "RSET", "NOOP")): w("250 ok")
            elif cmd.startswith("DATA"): data = []; w("354 go")
            elif cmd.startswith("QUIT"): w("221 bye"); return
            else: w("250 ok")
class Srv(socketserver.ThreadingTCPServer): allow_reuse_address = True; daemon_threads = True
try:
    sink = Srv(("127.0.0.1", 2525), Sink); threading.Thread(target=sink.serve_forever, daemon=True).start()
except OSError:
    pass

def call(method, path, body=None, token=None, device=None, ua=None):
    h = {"Content-Type": "application/json"}
    if token: h["Authorization"] = "Bearer " + token
    if device: h["X-Device-Key"] = device
    if ua: h["User-Agent"] = ua
    req = urllib.request.Request(BASE + path, method=method, data=json.dumps(body).encode() if body is not None else None, headers=h)
    try:
        with urllib.request.urlopen(req, timeout=8) as r:
            return r.status, json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        try: return e.code, json.loads(e.read() or b"null")
        except Exception: return e.code, None

def check(name, cond, extra=""):
    global fails
    print(("PASS " if cond else "FAIL ") + name + (f"  {extra}" if not cond else ""))
    fails += (not cond)

def totp_code(secret_b32, t=None):
    key = base64.b32decode(secret_b32.upper() + "=" * (-len(secret_b32) % 8))
    step = int((t or time.time()) // 30)
    h = hmac.new(key, struct.pack(">Q", step), hashlib.sha1).digest()
    o = h[-1] & 0xF
    return f"{(struct.unpack('>I', h[o:o+4])[0] & 0x7FFFFFFF) % 1000000:06d}"

def wait_mail(n, timeout=6):
    end = time.time() + timeout
    while time.time() < end and len(MAILS) < n: time.sleep(0.1)
    return MAILS[n - 1] if len(MAILS) >= n else None

def mail_parts(raw):
    m = emaillib.message_from_string(raw, policy=policy.default)
    html = m.get_body(preferencelist=("html",)).get_content()
    text = m.get_body(preferencelist=("plain",)).get_content()
    return m, html, text

stamp = f"{int(time.time())}{os.getpid() % 1000}"
email = f"m{stamp}@example.com"; pw0 = "secret-one-1"; pw = pw0
call("POST", "/api/auth/register", {"firstName": "Ada", "lastName": "Lovelace", "email": email, "password": pw, "confirmPassword": pw})
s, d = call("POST", "/api/auth/login", {"email": email, "password": pw}, ua="Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/126 Safari/537.36"); tok = d["token"]
check("plain login still returns a token when MFA is off", s == 200 and "mfaRequired" not in d, d)

# ── profile ──────────────────────────────────────────────────────────────────
s, d = call("GET", "/api/profile", token=tok)
check("profile shows details + security state", s == 200 and d["email"] == email and d["mfaEnabled"] is False and d["phones"] == [] and d["methods"] == {"phone": False, "totp": False, "email": False}, d)
ttl_min = d["emailCodeMinutes"]; mail_ok = d["emailConfigured"]
check("profile reports the email-code lifetime from the environment", isinstance(ttl_min, int) and 1 <= ttl_min <= 60, d)
s, _ = call("GET", "/api/profile"); check("profile needs login", s == 401, s)
s, d = call("PUT", "/api/profile", {"firstName": "  Ada  ", "lastName": "King", "email": email}, tok); check("update name", s == 200 and d["user"]["lastName"] == "King", (s, d))
s, _ = call("PUT", "/api/profile", {"firstName": "", "lastName": "X", "email": email}, tok); check("blank name rejected", s == 400, s)
new_email = f"m2{stamp}@example.com"
s, _ = call("PUT", "/api/profile", {"firstName": "Ada", "lastName": "King", "email": new_email}, tok); check("email change needs the password", s == 403, s)
s, d = call("PUT", "/api/profile", {"firstName": "Ada", "lastName": "King", "email": new_email, "currentPassword": pw}, tok)
check("email change succeeds and returns a fresh token", s == 200 and d["user"]["email"] == new_email and d.get("token"), (s, d)); tok = d["token"]; email = new_email
s, _ = call("GET", "/api/profile", token=tok); check("the re-issued token keeps its session", s == 200, s)

# ── signed-in devices ────────────────────────────────────────────────────────
s, ss = call("GET", "/api/sessions", token=tok)
cur = [x for x in ss if x["current"]]
check("sessions list marks this device and labels the browser", s == 200 and len(cur) == 1 and cur[0]["label"] == "Chrome on Linux" and cur[0]["kind"] == "browser", ss)
app_key = "app-device-key-" + stamp.ljust(16, "0")
s, d = call("POST", "/api/auth/login", {"email": email, "password": pw, "deviceKey": app_key, "deviceLabel": "INFINIX X6855 · SmartHomes app"}); app_tok = d["token"]
s, ss = call("GET", "/api/sessions", token=tok)
check("an app sign-in is listed with the phone's real model", any(x["label"] == "INFINIX X6855 · SmartHomes app" and x["kind"] == "app" and not x["current"] for x in ss), ss)
app_sess = next(x["slug"] for x in ss if x["kind"] == "app")
check("sessions are addressed by slug, not database id", len(app_sess) == 12 and not app_sess.isdigit(), app_sess)
s, _ = call("DELETE", "/api/sessions/1", token=tok); check("a numeric id in the link finds nothing", s == 404, s)
s, _ = call("DELETE", f"/api/sessions/{app_sess}", token=tok); check("revoking a device works", s == 200, s)
s, _ = call("GET", "/api/profile", token=app_tok); check("the revoked device is signed out immediately", s == 401, s)
s, _ = call("DELETE", f"/api/sessions/{app_sess}", token=tok); check("revoking twice is a 404", s == 404, s)

# ── password ─────────────────────────────────────────────────────────────────
old_tok = tok
s, _ = call("POST", "/api/profile/password", {"currentPassword": "nope", "newPassword": "brand-new-pass"}, tok); check("wrong current password rejected", s == 403, s)
s, _ = call("POST", "/api/profile/password", {"currentPassword": pw, "newPassword": "short"}, tok); check("short new password rejected", s == 400, s)
pw2 = "brand-new-pass-2"
s, d = call("POST", "/api/profile/password", {"currentPassword": pw, "newPassword": pw2}, tok); check("password changes", s == 200 and d.get("token"), (s, d)); tok = d["token"]
s, _ = call("GET", "/api/profile", token=old_tok); check("other sessions end when the password changes", s == 401, s)
s, _ = call("GET", "/api/profile", token=tok); check("the session that changed it keeps working", s == 200, s)
pw = pw2

# ── turn on: approve in the app ──────────────────────────────────────────────
phone = "phone-device-key-" + stamp.ljust(16, "0")
s, d = call("POST", "/api/mfa/enable/start", {}, tok); check("cannot enable phone approval without a phone", s == 409, (s, d))
s, d = call("POST", "/api/mfa/devices", {"deviceKey": phone, "name": "George's phone", "model": "X6855", "manufacturer": "INFINIX", "os": "Android 14"}, tok); check("phone registers with its real model and name", s == 200, (s, d))
s, ds = call("GET", "/api/mfa/devices", token=tok)
check("phone list shows name, model, maker and OS", ds[0]["name"] == "George's phone" and ds[0]["model"] == "X6855" and ds[0]["manufacturer"] == "INFINIX" and ds[0]["os"] == "Android 14", ds)
s, d = call("GET", "/api/profile", token=tok); check("profile carries the same details", d["phones"][0]["model"] == "X6855", d["phones"])
s, d = call("POST", "/api/mfa/enable/start", {}, tok); ch = d["challengeId"]
s, p = call("GET", "/api/mfa/pending", token=tok, device=phone); code = p[0]["code"]
check("enabling: the PHONE shows the number (typed on the website)", len(p) == 1 and p[0]["purpose"] == "enable" and len(code) == 6, p)
s, _ = call("POST", "/api/mfa/enable/confirm", {"challengeId": ch, "code": "000000" if code != "000000" else "111111"}, tok); check("wrong number rejected", s == 401, s)
s, d = call("POST", "/api/mfa/enable/confirm", {"challengeId": ch, "code": code}, tok); rec = d.get("recoveryCodes", [])
check("right number turns it on and returns 10 recovery codes", s == 200 and len(rec) == 10, (s, d))
s, d = call("GET", "/api/profile", token=tok); check("profile shows phone approval on", d["mfaEnabled"] and d["methods"]["phone"] and d["recoveryCodesLeft"] == 10, d)

# ── sign in: number matching ─────────────────────────────────────────────────
APP = {"deviceKey": app_key, "deviceLabel": "INFINIX X6855 · SmartHomes app"}
s, d = call("POST", "/api/auth/login", {"email": email, "password": pw})
check("login stops at step 2 and lists the available methods", d.get("mfaRequired") and "token" not in d and d["methods"]["phone"] and not d["methods"]["totp"] and d["methods"]["recovery"] and d["emailTo"].endswith("@example.com"), d)
mt = d["mfaToken"]
s, _ = call("GET", "/api/profile", token=mt); check("the step token is useless as an API token", s == 401, s)
s, d = call("POST", "/api/auth/mfa/challenge", {"mfaToken": mt, "method": "phone"}); lc = d["challengeId"]; number = d["number"]
check("the WEBSITE is given the number", s == 200 and len(number) == 6 and d["method"] == "phone", d)
s, p = call("GET", "/api/mfa/pending", token=tok, device=phone); req = [x for x in p if x["id"] == lc]
check("the phone gets a request WITHOUT the number (it must be typed)", len(req) == 1 and req[0]["purpose"] == "login" and req[0]["needsNumber"] and "code" not in req[0] and req[0]["device"], p)
s, d = call("POST", "/api/auth/mfa/poll", {"mfaToken": mt, "challengeId": lc}); check("website polls: still pending", d["status"] == "pending", d)
wrong = "000000" if number != "000000" else "111111"
s, d = call("POST", f"/api/mfa/challenges/{lc}/approve", {"code": wrong}, tok, device=phone); check("wrong number on the phone is refused (2 tries left)", s == 401 and d["attemptsLeft"] == 2, (s, d))
s, _ = call("POST", f"/api/mfa/challenges/{lc}/approve", {"code": number}, tok); check("approving needs a registered phone", s == 403, s)
s, d = call("POST", "/api/auth/mfa/poll", {"mfaToken": mt, "challengeId": lc}); check("still pending after a wrong number", d["status"] == "pending", d)
s, d = call("POST", f"/api/mfa/challenges/{lc}/approve", {"code": number}, tok, device=phone); check("right number + Approve", s == 200 and d["status"] == "approved", (s, d))
s, d = call("POST", "/api/auth/mfa/poll", {"mfaToken": mt, "challengeId": lc})
check("the website signs in by itself", d["status"] == "approved" and d.get("token") and d["user"]["email"] == email, d)
tok2 = d["token"]; s, _ = call("GET", "/api/profile", token=tok2); check("...and that session works", s == 200, s)
s, d = call("POST", "/api/auth/mfa/poll", {"mfaToken": mt, "challengeId": lc}); check("an approval can be used only once", d["status"] == "expired", d)

# 3 wrong numbers block the sign-in
mt = call("POST", "/api/auth/login", {"email": email, "password": pw})[1]["mfaToken"]
d = call("POST", "/api/auth/mfa/challenge", {"mfaToken": mt, "method": "phone"})[1]; lc = d["challengeId"]; number = d["number"]; wrong = "000000" if number != "000000" else "111111"
codes = [call("POST", f"/api/mfa/challenges/{lc}/approve", {"code": wrong}, tok, device=phone)[0] for _ in range(3)]
check("3 wrong numbers on the phone block the sign-in", codes == [401, 401, 403], codes)
s, _ = call("POST", f"/api/mfa/challenges/{lc}/approve", {"code": number}, tok, device=phone); check("...even the right number is then refused", s == 410, s)
s, d = call("POST", "/api/auth/mfa/poll", {"mfaToken": mt, "challengeId": lc}); check("website learns it was denied", d["status"] == "denied", d)

# deny from the phone
mt = call("POST", "/api/auth/login", {"email": email, "password": pw})[1]["mfaToken"]
lc = call("POST", "/api/auth/mfa/challenge", {"mfaToken": mt, "method": "phone"})[1]["challengeId"]
s, _ = call("POST", f"/api/mfa/challenges/{lc}/deny", {}, tok, device=phone); check("phone can deny", s == 200, s)
s, d = call("POST", "/api/auth/mfa/poll", {"mfaToken": mt, "challengeId": lc}); check("denied request is reported", d["status"] == "denied", d)

# someone else's phone cannot approve my sign-in
other = f"o{stamp}@example.com"; call("POST", "/api/auth/register", {"firstName": "O", "lastName": "U", "email": other, "password": pw0, "confirmPassword": pw0})
ot = call("POST", "/api/auth/login", {"email": other, "password": pw0})[1]["token"]; ph2 = "other-phone-key-" + stamp.ljust(16, "0")
call("POST", "/api/mfa/devices", {"deviceKey": ph2, "name": "Other"}, ot)
mt = call("POST", "/api/auth/login", {"email": email, "password": pw})[1]["mfaToken"]
d = call("POST", "/api/auth/mfa/challenge", {"mfaToken": mt, "method": "phone"})[1]
s, _ = call("POST", f"/api/mfa/challenges/{d['challengeId']}/approve", {"code": d["number"]}, ot, device=ph2); check("another account's phone cannot approve my sign-in", s == 410, s)
s, p2 = call("GET", "/api/mfa/pending", token=ot, device=ph2); check("...and does not even see it", p2 == [], p2)
s, _ = call("POST", "/api/mfa/devices", {"deviceKey": phone, "name": "steal"}, ot); check("a phone cannot be claimed by two accounts", s == 409, s)

# ── authenticator app (TOTP) ─────────────────────────────────────────────────
s, d = call("POST", "/api/mfa/totp/setup", {}, tok)
secret = d.get("secret", "")
check("authenticator setup returns a key, an otpauth URI and a QR image", s == 200 and len(secret) >= 16 and d["uri"].startswith("otpauth://totp/") and email in d["uri"] and d["qr"].startswith("data:image/png;base64,") and len(d["qr"]) > 500, d)
s, _ = call("POST", "/api/mfa/totp/confirm", {"code": "000000"}, tok); check("wrong authenticator code rejected", s == 401, s)
s, d = call("POST", "/api/mfa/totp/confirm", {"code": totp_code(secret)}, tok)
check("right code turns it on (no second set of recovery codes)", s == 200 and d["recoveryCodes"] == [], (s, d))
s, d = call("GET", "/api/profile", token=tok); check("profile shows authenticator on", d["methods"]["totp"] and d["methods"]["phone"], d)
mt = call("POST", "/api/auth/login", {"email": email, "password": pw})[1]
check("login now offers the authenticator too", mt["methods"]["totp"] and mt["methods"]["phone"], mt); mtk = mt["mfaToken"]
s, _ = call("POST", "/api/auth/mfa/verify", {"mfaToken": mtk, "method": "totp", "code": "000000"}); check("wrong authenticator code rejected at sign-in", s == 401, s)
s, d = call("POST", "/api/auth/mfa/verify", {"mfaToken": mtk, "method": "totp", "code": totp_code(secret, time.time() + 30)})
check("an authenticator code signs in", s == 200 and d.get("token"), (s, d))
s, _ = call("POST", "/api/auth/mfa/verify", {"mfaToken": mtk, "method": "totp", "code": totp_code(secret, time.time() + 30)}); check("the same code cannot be replayed", s == 401, s)
s, _ = call("POST", "/api/auth/mfa/verify", {"mfaToken": mtk, "method": "totp", "code": totp_code(secret, time.time() - 30)}); check("an older step cannot be replayed either", s == 401, s)
s, d = call("POST", "/api/auth/mfa/verify", {"mfaToken": mtk, "method": "totp", "code": totp_code(secret, time.time() - 600)}); check("a code from 10 minutes ago is rejected", s == 401, s)

# ── email codes ──────────────────────────────────────────────────────────────
if mail_ok:
    n0 = len(MAILS)
    s, d = call("POST", "/api/mfa/email/start", {}, tok); ech = d.get("challengeId")
    check("email setup sends a code and reports the lifetime", s == 200 and d["expiresIn"] == ttl_min * 60 and d["sentTo"].endswith("@example.com") and "•" in d["sentTo"], (s, d))
    raw = wait_mail(n0 + 1); check("an email arrived", raw is not None)
    m, html, text = mail_parts(raw)
    ecode = re.search(r"\n\s+(\d{6})\n", text).group(1)
    check("email: addressed to the user with a clear subject", m["To"] == email and "two-step" in m["Subject"].lower(), (m["To"], m["Subject"]))
    check("email: HTML + plain-text versions both contain the code", ecode in text and all(f">{ch_}</div>" in html for ch_ in ecode) and f"{ttl_min} minutes" in html and f"{ttl_min} minutes" in text, ttl_min)
    check("email: branded template (header, digit tiles, expiry, security notice)", "Smart" in html and "digit-box" in html and "Didn’t request this?" in html and "Never share this code" in html and "color-scheme" in html)
    s, _ = call("POST", "/api/mfa/email/confirm", {"challengeId": ech, "code": "000000" if ecode != "000000" else "111111"}, tok); check("wrong emailed code rejected", s == 401, s)
    s, d = call("POST", "/api/mfa/email/confirm", {"challengeId": ech, "code": ecode}, tok); check("right emailed code turns email on", s == 200 and d["recoveryCodes"] == [], (s, d))
    mt = call("POST", "/api/auth/login", {"email": email, "password": pw})[1]; check("login offers email", mt["methods"]["email"], mt); mtk = mt["mfaToken"]
    n1 = len(MAILS); s, d = call("POST", "/api/auth/mfa/challenge", {"mfaToken": mtk, "method": "email"})
    check("sign-in emails a code (lifetime from the environment)", s == 200 and d["expiresIn"] == ttl_min * 60, (s, d)); ech = d["challengeId"]
    m, html, text = mail_parts(wait_mail(n1 + 1)); lcode = re.search(r"\n\s+(\d{6})\n", text).group(1)
    check("sign-in email names the requesting device, IP and time", "Requested from" in text and "IP address" in text and "Time:" in text and "sign-in" in m["Subject"].lower(), text[:400])
    s, d = call("POST", "/api/auth/mfa/verify", {"mfaToken": mtk, "method": "email", "challengeId": ech, "code": lcode}); check("emailed code signs in", s == 200 and d.get("token"), (s, d))
    s, _ = call("POST", "/api/auth/mfa/verify", {"mfaToken": mtk, "method": "email", "challengeId": ech, "code": lcode}); check("an emailed code works once", s == 410, s)
else:
    print("SKIP email tests (hub has no SMTP configured)")

# ── recovery codes ───────────────────────────────────────────────────────────
mtk = call("POST", "/api/auth/login", {"email": email, "password": pw})[1]["mfaToken"]
s, d = call("POST", "/api/auth/mfa/verify", {"mfaToken": mtk, "recoveryCode": rec[0]}); check("a recovery code signs in", s == 200 and d.get("token"), (s, d))
s, _ = call("POST", "/api/auth/mfa/verify", {"mfaToken": mtk, "recoveryCode": rec[0]}); check("a recovery code works only once", s == 401, s)
s, _ = call("POST", "/api/auth/mfa/verify", {"mfaToken": mtk, "recoveryCode": rec[1].upper().replace("-", "")}); check("recovery codes ignore case and the dash", s == 200, s)
s, _ = call("POST", "/api/mfa/recovery", {"password": "wrong"}, tok); check("new recovery codes need the password", s == 403, s)
s, d = call("POST", "/api/mfa/recovery", {"password": pw}, tok); check("new recovery codes replace the old", s == 200 and len(d["recoveryCodes"]) == 10 and d["recoveryCodes"][0] != rec[2], d)

# ── removing a phone signs out its sessions ──────────────────────────────────
ph3 = "second-phone-key-" + stamp.ljust(16, "0")
call("POST", "/api/mfa/devices", {"deviceKey": ph3, "name": "Work phone", "model": "Pixel 8", "manufacturer": "Google", "os": "Android 15"}, tok)
phone_tok = None
mt3 = call("POST", "/api/auth/login", {"email": email, "password": pw, "deviceKey": ph3, "deviceLabel": "Google Pixel 8 · SmartHomes app"})[1]["mfaToken"]
phone_tok = call("POST", "/api/auth/mfa/verify", {"mfaToken": mt3, "recoveryCode": call("POST", "/api/mfa/recovery", {"password": pw}, tok)[1]["recoveryCodes"][0]})[1]["token"]
s, _ = call("GET", "/api/profile", token=phone_tok); check("an app session on the second phone works", s == 200, s)
pid = next(x["slug"] for x in call("GET", "/api/mfa/devices", token=tok)[1] if x["model"] == "Pixel 8")
s, d = call("DELETE", f"/api/mfa/devices/{pid}", token=tok); check("removing a phone reports what it signed out", s == 200 and d["signedOut"] >= 1, (s, d))
s, _ = call("GET", "/api/profile", token=phone_tok); check("that phone's session is dead at once", s == 401, s)
s, _ = call("POST", "/api/auth/mfa/challenge", {"mfaToken": mt3, "method": "phone"}); check("(the removed phone no longer counts)", s in (200, 409), s)
pid1 = call("GET", "/api/mfa/devices", token=tok)[1][0]["slug"]
s, d = call("DELETE", f"/api/mfa/devices/{pid1}", token=tok); check("cannot remove the only phone while app approval is on", s == 409, (s, d))

# ── brute force limits (last: they lock this account for a while) ────────────
if mail_ok:
    mtk = call("POST", "/api/auth/login", {"email": email, "password": pw})[1]["mfaToken"]
    s, d = call("POST", "/api/auth/mfa/challenge", {"mfaToken": mtk, "method": "email"}); ech = d.get("challengeId")
    check("sending too many emails is rate limited", s in (200, 429), (s, d))
    codes = [call("POST", "/api/auth/mfa/verify", {"mfaToken": mtk, "method": "email", "challengeId": ech, "code": "000000"})[0] for _ in range(12)]
    check("repeated wrong codes get rate limited", ech is None or 429 in codes or 410 in codes, codes)

# ── audit log ────────────────────────────────────────────────────────────────
s, d = call("GET", "/api/audit?limit=200", token=tok)
acts = {(i["action"], i["outcome"]) for i in d["items"]} if s == 200 else set()
check("audit log records sign-ins, failures, device and security changes",
      {("user_login", "ok"), ("session_revoked", "ok"), ("device_registered", "ok"), ("mfa_enabled", "ok")} <= acts, sorted(acts))
check("audit entries carry actor and device", all(i["actor"] and i["device"] for i in d["items"] if i["action"] == "user_login"), d["items"][:2])
s, d = call("GET", "/api/audit?category=security&outcome=failed", token=tok)
check("audit filters work", s == 200 and all(i["category"] == "security" and i["outcome"] == "failed" for i in d["items"]), d)
s, _ = call("GET", "/api/audit"); check("audit log needs login", s == 401, s)
s, d = call("GET", "/api/audit", token=ot); check("another account sees none of my activity", all(i["actor"] != email for i in d["items"]), d)

# ── turning methods off ──────────────────────────────────────────────────────
s, _ = call("POST", "/api/mfa/methods/totp/disable", {"password": "wrong"}, tok); check("turning a method off needs the password", s == 403, s)
s, _ = call("POST", "/api/mfa/methods/totp/disable", {"password": pw}, tok)
d = call("GET", "/api/profile", token=tok)[1]; check("authenticator turns off, the rest stay on", s == 200 and not d["methods"]["totp"] and d["methods"]["phone"] and d["mfaEnabled"], d)
if mail_ok: call("POST", "/api/mfa/methods/email/disable", {"password": pw}, tok)
call("POST", "/api/mfa/methods/phone/disable", {"password": pw}, tok)
d = call("GET", "/api/profile", token=tok)[1]; check("turning off the last method turns two-step off and clears recovery codes", d["mfaEnabled"] is False and d["recoveryCodesLeft"] == 0 and d["methods"] == {"phone": False, "totp": False, "email": False}, d)
s, d = call("POST", "/api/auth/login", {"email": email, "password": pw}); check("login returns a token again", s == 200 and d.get("token") and "mfaRequired" not in d, d)
s, _ = call("DELETE", f"/api/mfa/devices/{pid1}", token=tok); check("the last phone can be removed once approval is off", s == 200, s)

print("\nFAILED" if fails else "\nALL PASSED", fails)
sys.exit(1 if fails else 0)
