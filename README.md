# smarthome-hub

Go backend — REST API + TCP gateway bridge for the SmartHome ecosystem. Built with **Gin**, **GORM**, and **MariaDB**.

## Prerequisites

- **Go** 1.21+
- **MariaDB** 10.6+ (or MySQL-compatible)
- **Node.js** 18+ (only needed to rebuild the Vue frontend)

## Quick Start

```bash
# 1. Copy and edit environment config
cp .env.example .env
#   At minimum, set DB_HOST, DB_USER, DB_PASS, DB_NAME, and JWT_SECRET

# 2. Build the Vue frontend (if not already built)
cd ../smarthome-ui && npm install && npm run build && cd ../smarthome-hub

# 3. Install Go dependencies
go mod tidy

# 4. Run
go run .
```

The server starts on `:9000` (HTTP) and `:9010` (TCP gateway).

## Environment Variables

|       Variable        |       Default     |              Description                          |
|-----------------------|-------------------|---------------------------------------------------|
| `SERVER_PORT`         | `9000`            | HTTP API + Vue SPA port                           |
| `TCP_PORT`            | `9010`            | TCP port for gateway bridge                       |
| `DB_HOST`             | `127.0.0.1`       | MariaDB host                                      |
| `DB_PORT`             | `3306`            | MariaDB port                                      |
| `DB_USER`             | `smarthome`       | Database user                                     |
| `DB_PASS`             | `smarthome`       | Database password                                 |
| `DB_NAME`             | `smarthome`       | Database name                                     |
| `JWT_SECRET`          | (required)        | Secret for JWT token signing                      |
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

`GET /api/audit?category=&outcome=ok|failed&q=&before=<id>&limit=` returns who did what, from which device and IP (sign-ins and failures, security and device changes, gateway/node/display/Wi-Fi changes); `&format=csv` exports. Shown in the web UI under **Activity log**. Covered by `scripts/mfa_e2e.py`.
