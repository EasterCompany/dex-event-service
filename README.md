# Dexter Event Service

**A lightweight, file-based event processing system for asynchronous inter-service communication**

Dexter Event Service provides a simple yet powerful event-driven architecture for the Dexter ecosystem. It enables asynchronous communication between services through a file-based event queue, where events are stored as JSON files and processed by specialized handlers. This design ensures durability, observability, and easy debugging while maintaining minimal overhead.

The service monitors an events directory for new event files, dispatches them to registered handlers (standalone executables that communicate via stdin/stdout), and manages event lifecycle including retry logic and error handling. By treating events as files, the system gains natural persistence, inspectability, and the ability to replay or manually intervene when needed.

**Platform Support:** Linux (systemd-based distributions)

## Standard Service Interface

All Dexter services implement a universal interface for version information and health monitoring.

### Version Command

Query the service version using the `version` argument:

```bash
dex-event-service version
```

**Example Output:**
```
2.5.3.main.4479d25.2025-11-28-00-03-40.linux-amd64.yunhmbpp
```

Version format: `major.minor.patch.branch.commit.buildDate.arch.buildHash`

### Service Endpoint

When running in server mode, the service exposes a `/service` endpoint on port `8100` that returns detailed information:

```bash
curl http://localhost:8100/service
```

**Example Response:**
```json
{
  "version": {
    "str": "2.5.3.main.4479d25.2025-11-28-00-03-40.linux-amd64.yunhmbpp",
    "obj": {
      "major": "2",
      "minor": "5",
      "patch": "3",
      "branch": "main",
      "commit": "4479d25",
      "build_date": "2025-11-28-00-03-40",
      "arch": "linux-amd64",
      "build_hash": "yunhmbpp"
    }
  },
  "health": {
    "status": "OK",
    "uptime": "1h23m45s",
    "message": "Service is running normally"
  },
  "metrics": {
    "events_processed": 1234,
    "goroutines": 8,
    "memory_alloc_mb": 12.5
  }
}
```

For simple version string only:
```bash
curl http://localhost:8100/service?format=version
```

## Event Service Specifics

### Event Processing

The event service monitors `~/Dexter/data/events/` for new JSON event files. Events are processed by registered handlers located in `~/Dexter/bin/`:

- **Event Handlers**: Standalone executables prefixed with `event-` (e.g., `event-test-handler`)
- **Communication**: JSON via stdin/stdout
- **Lifecycle**: Events are moved to `processed/` or `failed/` directories after handling

### CLI Mode Commands

```bash
# List all events
dex-event-service -list

# Delete events matching pattern
dex-event-service -delete "pattern"
```

### Server Mode

Running without arguments starts the HTTP server on port `8100`:

```bash
dex-event-service
```

The service runs as a systemd user service and automatically processes events in the background.
