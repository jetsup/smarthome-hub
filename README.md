# smarthome-hub

Go backend — REST API + TCP gateway bridge for the SmartHome ecosystem. Built with **Gin**, **GORM**, and **MariaDB**.

## The whole system at a glance

```
 Vue web UI ─┐                                     ┌─ ESP32 node (sensors, LEDs, relays…)
 Flutter app ┼─ HTTP :9000 ─► HUB (this project) ◄─ TCP :9010 ─ ESP32 gateway ─ ESP-NOW ─┤
 ESP32-S3 HMI┘            (Go + MariaDB)                                                  └─ ESP32 node
```

| Project | What it is | Setup guide |
|---|---|---|
| `smarthome-hub` | Go server: REST API, gateway TCP bridge, web UI host, database | this file |
| `smarthome-ui` | Vue 3 web dashboard (built files are served by the hub) | `../smarthome-ui/README.md` |
| `smarthome-app` | Flutter Android app | `../smarthome-app/README.md` |
| `smarthome-gateway` | ESP32 firmware bridging ESP-NOW ⇄ hub | `../smarthome-gateway/README.md` |
| `smarthome-node` | ESP32 firmware for sensor/actuator nodes | `../smarthome-node/README.md` |
| `smarthome-hmi` | ESP32-S3 touch display firmware (Viewe 2.8") | `../smarthome-hmi/README.md` |

### Set up in this order

1. **Hub** (this file) – database, `.env`, run it. Note the hub computer's **LAN IP address** (`ip -4 addr` / `ipconfig`) and give it a fixed address (DHCP reservation on your router).
2. **Web UI** – build once; the hub serves it. Open `http://<HUB_IP>:9000`, register your account.
3. **Gateway firmware** – put the hub IP in, flash, then link it to a gateway you create in the UI.
4. **Node firmware** – put your home Wi-Fi name/password in, flash, then discover and activate it from the UI/app.
5. **HMI display** – put the hub IP in, flash, join Wi-Fi, pair with the code it shows.
6. **Android app** – build with the hub URL, install on the phone.

### ⚠️ Values you MUST change for your own installation

Everything below ships with the author's bench values. Change them or nothing will connect.

| Where | Setting | Shipped value | Change it to |
|---|---|---|---|
| `smarthome-hub/.env` | `DB_PASS` | `smarthome` | a strong database password (same one you give MariaDB below) |
| `smarthome-hub/.env` | `JWT_SECRET` | (placeholder) | a long random string: `openssl rand -hex 32` |
| `smarthome-hub/.env` | `SMTP_*`, `PUBLIC_URL` | unset | only if you want email codes (see table below) |
| `smarthome-gateway/include/Config.hpp` | `HUB_ADDR` | `192.168.100.100` | **your hub's IP** (or pass `-DHUB_ADDR=\"x.x.x.x\"` in `platformio.ini`) |
| `smarthome-hmi/include/config.hpp` | `HMI_HUB_HOST` | `192.168.100.100` | **your hub's IP** (or `-DHMI_HUB_HOST=\"x.x.x.x\"`) |
| `smarthome-hmi/include/network_manager.hpp` | `m_hubHost` | `192.168.100.100` | same IP (fallback value) |
| `smarthome-node/include/Config.hpp` | `WIFI_SSID`, `WIFI_PASS` | the author's Wi-Fi | **your 2.4 GHz home Wi-Fi** (the node joins it only to learn the radio channel so ESP-NOW matches the gateway) |
| `smarthome-app/lib/config.dart` | `HUB_URL` | `http://192.168.100.100:9000` | **your hub URL**, best via `--dart-define=HUB_URL=http://<HUB_IP>:9000` |

Ports (`9000` HTTP, `9010` TCP) only need changing if they clash; if you change them, change them in the gateway (`HUB_WEB_PORT`, `HUB_TCP_PORT`), HMI (`HMI_HUB_PORT`) and app URL too.
Everything must be on the **same LAN**; gateway, HMI and nodes need **2.4 GHz** Wi-Fi.

## Installation (hub)

### Prerequisites

- **Go** 1.21+
- **MariaDB** 10.6+ (or MySQL-compatible)
- **Node.js** 18+ (to build the web UI)
- Python 3 (only for the test scripts)

### 1. Create the database

```bash
sudo mariadb <<'SQL'
CREATE DATABASE smarthome CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'smarthome'@'localhost' IDENTIFIED BY 'CHANGE_ME';   -- same value as DB_PASS
GRANT ALL PRIVILEGES ON smarthome.* TO 'smarthome'@'localhost';
FLUSH PRIVILEGES;
SQL
```

Tables are created and migrated automatically on start-up.

### 2. Configure `.env`

```bash
cp .env.example .env
#   Edit: DB_HOST / DB_USER / DB_PASS / DB_NAME must match step 1, and set JWT_SECRET
```

### 3. Build the web UI, then run

```bash
cd ../smarthome-ui && npm install && npm run build && cd ../smarthome-hub
go mod tidy
go run .                 # development
# or a binary:  make local   ->  build/smarthome-hub   (make pi-arm64 for a Raspberry Pi)
```

The hub listens on `:9000` (HTTP + web UI) and `:9010` (TCP, gateways). Open `http://<HUB_IP>:9000` and register the first user.
Allow both ports through the hub computer's firewall (`sudo ufw allow 9000/tcp && sudo ufw allow 9010/tcp`).

Run it as a service on a Pi/server (optional): create `/etc/systemd/system/smarthome-hub.service` with `WorkingDirectory=` the hub folder, `ExecStart=` the built binary, `Restart=always`, then `systemctl enable --now smarthome-hub`.

## Environment Variables (`.env`)

|       Variable        |       Default     |              Description                          |
|-----------------------|-------------------|---------------------------------------------------|
| `SERVER_PORT`         | `9000`            | HTTP API + Vue SPA port                           |
| `TCP_PORT`            | `9010`            | TCP port for gateway bridge                       |
| `DB_HOST`             | `127.0.0.1`       | MariaDB host                                      |
| `DB_PORT`             | `3306`            | MariaDB port                                      |
| `DB_USER`             | `smarthome`       | Database user                                     |
| `DB_PASS`             | `smarthome` **change** | Database password                            |
| `DB_NAME`             | `smarthome`       | Database name                                     |
| `JWT_SECRET`          | (required) **change** | Secret for JWT token signing (`openssl rand -hex 32`) |
| `NODE_OFFLINE_TIMEOUT`| `300`             | Seconds without ping before node marked offline   |
| `UI_DIR`              | auto-detected     | Directory of the built Vue SPA                    |
| `CORS_ORIGINS`        | Vite dev server   | Comma-separated allowed cross-origin origins      |
| `MFA_EMAIL_CODE_TTL_MINUTES` | `10`       | Minutes an emailed verification code stays valid (1-60) |
| `SMTP_HOST` / `SMTP_PORT` | (unset) / `587` | Mail server for email codes (email is off without a host) |
| `SMTP_USER` / `SMTP_PASS` | (unset)       | SMTP credentials                                  |
| `SMTP_SECURITY`       | `starttls`        | `starttls`, `tls` (implicit) or `none` (local relay only) |
| `SMTP_FROM` / `SMTP_FROM_NAME` | (unset) / `SmartHome` | Sender address and display name      |
| `PUBLIC_URL`          | (unset)           | Web UI address, used for the button in emails     |

## API Endpoints

All authenticated endpoints require `Authorization: Bearer <token>` header.

### Auth

| Method |          Path        |                       Description                         |
|--------|----------------------|-----------------------------------------------------------|
| `POST` | `/api/auth/register` | Register new user (firstName, lastName, email, password)  |
| `POST` | `/api/auth/login`    | Login, returns JWT token                                  |

### Profile, devices & two-step verification

| Method |                  Path                       |                       Description                         |
|--------|---------------------------------------------|-----------------------------------------------------------|
| `GET`/`PUT` | `/api/profile`                         | Read / update name and email (email change needs the password) |
| `POST` | `/api/profile/password`                     | Change password (signs out all other devices)             |
| `GET`  | `/api/sessions`                             | Signed-in devices (browser / app) with a `current` flag   |
| `DELETE` | `/api/sessions/:id`, `POST /api/sessions/revoke-others` | Sign a device out immediately       |
| `GET`/`POST`/`DELETE` | `/api/mfa/devices[/:id]`     | Phones that can approve sign-ins (real model/name); removing one also signs it out |
| `POST` | `/api/mfa/enable/{start,confirm}`           | Turn on *approve in the app*                              |
| `POST` | `/api/mfa/totp/{setup,confirm}`             | Authenticator app (Google Authenticator, Authy, ...); returns QR + key |
| `POST` | `/api/mfa/email/{start,confirm}`            | Email codes (lifetime `MFA_EMAIL_CODE_TTL_MINUTES`)       |
| `POST` | `/api/mfa/methods/:method/disable`, `/api/mfa/disable`, `/api/mfa/recovery` | Turn methods off / new recovery codes (password required) |
| `POST` | `/api/auth/mfa/{challenge,poll,verify}`     | Sign-in step 2 (phone number-matching, email, authenticator, recovery) |
| `GET`  | `/api/mfa/pending`, `POST /api/mfa/challenges/:id/{approve,deny}` | The phone's side of an approval |

Sign-in with the app uses **number matching**: the website shows a number, the phone asks you to type it and tap
*Approve*, and the website signs you in on its own.

### Gateways

| Method    |               Path            |                       Description                         |
|-----------|-------------------------------|-----------------------------------------------------------|
| `GET`     | `/api/gateways`               | List user's gateways                                      |
| `POST`    | `/api/gateways`               | Create gateway (name)                                     |
| `GET`     | `/api/gateways/:id`           | Gateway details with nodes                                |
| `DELETE`  | `/api/gateways/:id`           | Delete gateway                                            |
| `GET`     | `/api/gateways/:id/api-key`   | Retrieve API key (marks as assigned)                      |
| `GET`     | `/api/gateways/:id/nodes`     | List provisioned nodes (includes isOnline)                |

### Node Discovery & Provisioning

| Method     |              Path                 |                      Description                     |
|------------|-----------------------------------|------------------------------------------------------|
| `POST`     | `/api/gateways/:id/scan`          | Scan on a specific gateway                           |
| `GET`      | `/api/gateways/:id/discovered`    | Discovered nodes for a gateway                       |
| `POST`     | `/api/gateways/:id/provision`     | Provision node on a specific gateway                 |
| `POST`     | `/api/nodes/scan`                 | Scan all online gateways                             |
| `GET`      | `/api/nodes/discovered`           | Discovered nodes across all gateways                 |
| `POST`     | `/api/nodes/provision`            | Provision node (takes deviceId + gatewayId)          |

### HMI pairing & access

| Method |            Path                 |                       Description                         |
|--------|---------------------------------|-----------------------------------------------------------|
| `POST` | `/api/pair/request`             | HMI asks to pair; a 6-digit code (5 min TTL) is created   |
| `GET`  | `/api/pair/code/:gatewayId`     | **Auth required**: owner reads the code to type on HMI    |
| `POST` | `/api/pair/verify`              | HMI submits the code (max 5 tries); returns an HMI token  |
| `GET`  | `/api/hmi/gateways`             | Open: list of gateway id/name/online for the pairing scan |
| `*`    | `/api/hmi/...`                  | Require the HMI token; scoped to the paired gateway only  |

### Devices

| Method |              Path                |                       Description                         |
|--------|----------------------------------|-----------------------------------------------------------|
| `GET`  | `/api/devices`                   | Real-time device registry (from TCP)                      |
| `POST` | `/api/device/:id/control?state=` | Send command to device                                    |
| `POST` | `/api/devices/:id/ping`          | Update LastSeen heartbeat                                 |

## Key Concepts

- **TCP Gateway Bridge**: Gateways authenticate via API key on connect. Once connected, the hub and gateway exchange 9-byte ESP-NOW packets bidirectionally over a persistent TCP connection.
- **Auto-provisioning**: Nodes must be explicitly provisioned through the discovery flow. The `ReportNode` function only updates existing nodes — new nodes rejected by telemetry unless provisioned.
- **Online Status**: Nodes are considered online if their `LastSeen` is within `NODE_OFFLINE_TIMEOUT` (default 5 min).
- **Audit Logging**: All gateway and node operations are logged to the `audit_logs` table.

## Testing

With the hub running (`go run .`), run the end-to-end smoke test:

```bash
python3 scripts/e2e.py 9000 9010   # http port, tcp port
```

It registers a user, links a fake gateway over TCP and exercises auth, pairing and HMI token scoping.
Further suites (each creates its own users and fake gateway):

```bash
python3 scripts/wifi_e2e.py      # saved Wi-Fi priority + gateway rename
python3 scripts/hmi_e2e.py       # displays: code/QR pairing, node ownership (a node is on ONE display)
python3 scripts/mfa_e2e.py       # profile, sessions, phone approval, authenticator, email, recovery
```

`mfa_e2e.py` starts its own SMTP sink on port 2525, so run the hub with
`SMTP_HOST=127.0.0.1 SMTP_PORT=2525 SMTP_SECURITY=none SMTP_FROM=no-reply@smarthome.test` to exercise the email path.

Note: command value `99` is reserved (node factory reset) and rejected on user command endpoints.


### Activity log (audit)

`GET /api/audit?category=&outcome=ok|failed&q=&page=1&limit=50` returns one page (newest first) plus `total`, `pages` and `page` (`before=<id>` still works for cursor paging); who did what, from which device and IP (sign-ins and failures, security and device changes, gateway/node/display/Wi-Fi changes); `&format=csv` exports. Shown in the web UI under **Activity log**. Covered by `scripts/mfa_e2e.py`.

### Links use slugs, not database ids

Tables addressed by URL (displays, phones, signed-in devices, pending actions, bindings, saved Wi-Fi networks) carry a random 12-character `slug`; routes such as `/api/hmis/:id` take that slug and a numeric database id finds nothing. Gateways and nodes already use random hex ids. Existing rows get slugs automatically at startup.

### Modules: NeoPixel, sensors, buzzer, RTC (extensible)

Every kind of hardware a node can carry is one entry in `hub/catalog.go` (`GET /api/capabilities/catalog`): its form fields, the readings it reports, and the actions it accepts. The web UI and the app build their forms and controls from that catalog, so a new module needs only a catalog entry plus its driver in the node firmware (`smarthome-node/src/modules.cpp`).

| Module | Fields | Readings | Actions |
|---|---|---|---|
| NeoPixel strip/ring | data GPIO, LED count, ring | – | `setEffect` (22 patterns incl. ring animations, colour, speed, brightness, reverse) |
| Buzzer | GPIO | – | `play` (beep, double/triple, alarm, siren, chime, doorbell) |
| DHT11/22 | GPIO, model | temperature, humidity | – |
| LD2410 radar | RX/TX GPIO | presence, moving/still distance & energy | – |
| DS3231 RTC | SDA/SCL | time, chip temperature, valid | `syncTime` |
| Analog / digital input | GPIO | value (per pin) | – |

* `POST /api/nodes/:nodeId/action {cap, action, params}` runs an action; readings and last settings are in `GET /api/nodes/:nodeId` (`readings`, `effects`).
* `PUT /api/nodes/:nodeId {name}` renames a node in place (no re-pairing). `POST /api/nodes/:nodeId/restart` reboots one node; `POST /api/gateways/:id/restart-nodes` reboots every node on a gateway. Pairing and saved settings are kept.
* Display power settings (`PUT /api/hmis/:id/appearance`, also `POST /api/hmi/display` from the display itself): `sleepAfter` (seconds, 0 = never, else 10-3600), `ledSleepOff` (turn the RGB LED off with the screen), `brightness` (10-100), `ledBrightness` (1-100).
* A gateway counts as online while its TCP link to the hub is open (TCP keepalive detects a powered-off gateway); it is not marked offline just because no node is talking.
* **Rules** connect modules to each other: `POST /api/gateways/:id/rules` (“when DHT humidity > 65 → buzzer alarm; otherwise stop”). They fire on the edge and are listed/enabled/deleted under `/api/rules/:slug`.
* **Shared clock**: a DS3231 becomes the system clock (`GET /api/system/time`); the hub pushes `TIME:` to every gateway, which broadcasts it to the nodes — the base for scheduled tasks.
* Wire protocol: classic 9-byte frames, plus length-prefixed frames (type ≥ 8) for readings (8), actions (9) and time (11). Gateway→node commands are sent several times because ESP-NOW broadcasts are unacknowledged. Tested by `scripts/modules_e2e.py`.
