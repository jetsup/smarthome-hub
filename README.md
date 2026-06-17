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

## API Endpoints

All authenticated endpoints require `Authorization: Bearer <token>` header.

### Auth

| Method |          Path        |                       Description                         |
|--------|----------------------|-----------------------------------------------------------|
| `POST` | `/api/auth/register` | Register new user (firstName, lastName, email, password)  |
| `POST` | `/api/auth/login`    | Login, returns JWT token                                  |

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
