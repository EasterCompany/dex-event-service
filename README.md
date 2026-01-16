# Dexter Event Service

**The central nervous system of the Dexter ecosystem.**

The `dex-event-service` is a high-performance, asynchronous event bus designed to decouple services and manage communication flows. It leverages **Redis** for durable event storage and queuing, providing a robust backbone for the entire platform.

## 🏗 Architecture

Unlike traditional message brokers, `dex-event-service` combines a fast, Redis-backed event queue with a unique "Handler" system:

1.  **Event Ingestion**: Services publish events (JSON) via a simple HTTP API (`POST /events`).
2.  **Redis Storage**: Events are immediately persisted to Redis for durability and ordered processing.
3.  **Handler Dispatch**: The service monitors the queue and dispatches events to specialized **Handler Executables**.
    - Handlers are standalone binaries (e.g., `event-chat-handler`) that perform the actual business logic.
    - This allows handlers to be written in any language and updated independently.
4.  **Log Aggregation**: The service also acts as a centralized log reader, capable of fetching and serving logs from all other Dexter services (`~/Dexter/logs/`).

## 🚀 Tech Stack

- **Language:** Go 1.24
- **Storage:** Redis (via `go-redis/v9`)
- **Communication:** HTTP (REST)
- **Dependencies:** `dex-cli` (for build/management)

## 🔌 Ports & Networking

- **Port:** `8100` (Default)
- **Host:** `0.0.0.0` (Binds to all interfaces)

## 🛠 Prerequisites

- **Go 1.24+**
- **Redis 6+** (Running on default port `6379` or configured in `service-map.json`)
- **dex-cli** (Installed and configured)

## 📦 Getting Started

The recommended way to manage this service is via the `dex-cli`.

### 1. Build

Build the service from source using the unified build pipeline:

```bash
dex build event
```

_This command handles dependencies (`go mod tidy`), formatting, linting, testing, and compilation._

### 2. Run

Start the service (systemd integration handled by `dex-cli`):

```bash
dex start event
```

### 3. Verify

Check the service status:

```bash
dex status event
```

View logs:

```bash
dex logs event
```

## 📡 API Documentation

The service exposes a RESTful API for event management and system monitoring.

### Base URL

`http://localhost:8100`

### Endpoints

#### 1. Service Health & Info

Get the current status, version, and basic metrics of the service.

- **GET** `/service`
- **Query Param:** `?format=version` (Optional, returns version string only)

```json
{
  "version": {
    "str": "2.5.0.main.abc1234...",
    "obj": { ... }
  },
  "health": {
    "status": "OK",
    "uptime": "24h"
  },
  "metrics": {
    "cpu": 0.5,
    "memory": 12.4
  }
}
```

#### 2. Create Event

Publish a new event to the system.

- **POST** `/events`
- **Headers:**
  - `Content-Type: application/json`
  - `X-Service-Name: <calling-service-id>` (Required for auth)

**Payload:**

```json
{
  "service": "dex-discord-service",
  "handler": "chat",
  "event": {
    "type": "message",
    "content": "Hello World"
  }
}
```

#### 3. Get Event

Retrieve a specific event by its ID.

- **GET** `/events/{event_id}`
- **Headers:** `X-Service-Name: <calling-service-id>`

#### 4. Delete Event

Remove an event from the system.

- **DELETE** `/events/{event_id}`
- **Headers:** `X-Service-Name: <calling-service-id>`

#### 5. System Monitor

Get detailed system resource usage metrics (CPU, Memory) for the host.

- **GET** `/system_monitor_metrics`

#### 6. Fetch Logs

Retrieve logs from this or other services.

- **GET** `/logs`
- **Headers:** `X-Service-Name: <calling-service-id>`

## ⚙️ Configuration

Configuration is managed centrally by `dex-cli` and stored in `~/Dexter/config/`.

- **`service-map.json`**: Defines the service's port (`8100`) and Redis credentials.
- **Redis**: The service connects to the Redis instance defined as `local-cache-0` in the service map.

## 🧪 Testing

Run the built-in test suite to verify Redis connectivity and handler dispatching:

```bash
dex-event-service test
```

_Note: This requires the service to be running._

## 🔍 Troubleshooting

**"Redis service 'local-cache-0' not found"**

- Ensure `service-map.json` exists in `~/Dexter/config/` and contains a valid `os` entry for `local-cache-0`.

**"Handler not found"**

- Verify that the requested handler binary (e.g., `event-chat-handler`) exists in `~/Dexter/bin/` and is executable.

**Connection Refused**

- Check if the service is running: `systemctl --user status dex-event-service`.
- Check logs: `dex logs event`.
