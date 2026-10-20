# GPS Manager

The GPS manager resolves **agent geographic coordinates** for Controller reporting. It supports `auto`, `dynamic`, `manual`, and `off`, and starts initialization asynchronously.

**Code:** `internal/gps/`

## Purpose

- Read coordinates from a serial GPS device, from IP geolocation, or from manual config
- Expose `gpsStatus` on `edgelet system info` and on the Controller status post
- Post latitude and longitude to the Controller when the stored coordinates change

## Dependencies

| Depends on | Reason |
|------------|--------|
| `config` | GPS mode, manual coordinates, device path, scan frequency |
| `fieldagent` | Posts latitude and longitude when the GPS callback runs |

| Used by | Reason |
|---------|--------|
| `supervisor` | Started before EdgeletAPI; wires the GPS config callback |
| `edgeletapi` | `GET/POST /v1/system/gps`, `gpsStatus` on `GET /v1/system/info` |
| `fieldagent` | `gpsStatus` on the Controller status post |

## Modes

| Mode | Behavior |
|------|----------|
| `off` | No coordinate collection |
| `manual` | Static `gpsCoordinates` from config. `POST /v1/system/gps` switches to this mode |
| `dynamic` | Serial NMEA device (`gpsDevice`). IP geolocation runs only when that device cannot be read |
| `auto` | IP geolocation only |

`gpsScanFrequency` is the scan interval in seconds. `0` disables the scheduler, so neither the device nor IP geolocation is polled.

Implementation split: `device.go`, `nmea/`, `web.go`.

## Dynamic scan

`dynamic` with a configured `gpsDevice` opens that serial device read-only. Edgelet does not set baud, data bits, or parity; the port must already be configured by the OS. Serial polling is unix. Windows returns a device error for this path.

Each scan reads the lines already buffered on the port, up to 128, and stops when the port is idle. The latest `RMC` or `GGA` sentence in that burst wins. Other NMEA sentences are ignored. A valid fix is stored as `lat,lon` with five decimal places.

The Controller GPS post (`config/gps`) runs only when that five-decimal string changes. A repeated fix at the same rounded position does not post again. The status post still carries `gpsStatus` on its normal interval.

## Lifecycle

### Start

1. Reset context (supervisor restart safe)
2. `startCoordinateUpdateScheduler()`
3. Async `initializeGps()` in a goroutine. A panic or init error falls back to `off`

If `dynamic` is selected and the device cannot be opened, startup falls through to the IP handler. The configured mode string stays `dynamic`, and later scans keep using the IP fallback while the device handler is not running.

### Stop

Cancel the scheduler and the main context, then close the device or web handler.

## Configuration

GPS settings live in config profiles and in Controller-pushed fog config. Keys: `gpsMode`, `gpsDevice`, `gpsScanFrequency`, `gpsCoordinates`. See [config.md](../config.md).

The supervisor registers `SetGPSConfigCallback`. That callback posts coordinates; it does not run on every config reload. A reload calls `InstanceConfigUpdated`, which restarts the scheduler and re-initializes the active mode.

## Module status

| Property | Value |
|----------|-------|
| StatusReporter index | `6` (`utils.GPSManager`) |

`gps.Status` stores only the health value. Coordinates stay on `gpsCoordinates`. `edgelet system info` and the Controller status field `gpsStatus` both read this health value.

| `gpsStatus` | Meaning |
|-------------|---------|
| `HEALTHY` | Latest `RMC` or `GGA` is a valid fix, or `auto` / `manual` is in use. A dynamic port that just opened stays `HEALTHY` until the first scan |
| `NO_FIX` | The serial port is producing data and the latest position sentence has no fix. Previous device coordinates stay. No IP lookup |
| `DEVICE_ERROR` | The port produced no data, or it could not be read. That scan does one IP lookup. A successful lookup leaves this status in place |
| `IP_ERROR` | The IP lookup failed |
| `OFF` | Mode is `off`, or initialization failed |

A change into `NO_FIX` logs `GPS no fix; keeping last coordinates`. A later valid fix logs `GPS fix acquired`.

## External APIs

| Route | Role |
|-------|------|
| `GET /v1/system/info` | Includes `gpsStatus` with the GPS config fields |
| `GET /v1/system/gps` | Current `lat` and `lon` |
| `POST /v1/system/gps` | Body `lat` and `lon`. Sets mode to `manual` and persists `gpsCoordinates` |

Controller sync is `PATCH config/gps` with `latitude` and `longitude`, and `gpsStatus` on the status post.

## Observability

- Log module: `"GPS Manager"` for mode and status changes
- Log module: `"GPS Device Handler"` for the serial read
- Log module: `"GPS Web Handler"` for IP geolocation

## Failure modes

| Symptom | Typical cause |
|---------|----------------|
| `gpsStatus` `NO_FIX` | Receiver has no satellite fix yet. Coordinates are unchanged |
| `gpsStatus` `DEVICE_ERROR` | Device missing, port silent, or open failed. Coordinates may come from IP geolocation |
| `gpsStatus` `IP_ERROR` | IP lookup failed. Previous coordinates stay |
| `OFF` after start | Init error |
| Coordinates never move | `gpsScanFrequency` is `0`, mode is `off` or `manual`, or scans stay on `NO_FIX` |

## Code map

| File | Role |
|------|------|
| `manager.go` | Mode scheduling, init, status, Controller post gate |
| `device.go` | Serial scan and coordinate update |
| `device_poll_unix.go` | Bounded line read |
| `web.go` | IP geolocation |
| `nmea/` | `RMC` and `GGA` parsing |
| `status.go` | `gpsStatus` values |

Related: [fieldagent.md](fieldagent.md), [edgeletapi.md](edgeletapi.md), [statusreporter.md](statusreporter.md).
