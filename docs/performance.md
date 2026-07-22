# Gateway performance

The Go gateway was accepted against the historical Ruby/Puma resident-memory baseline. Pi and its Node subprocesses are excluded from the gateway target.

## Repeatable Go benchmark

On Linux, run the seeded workload with:

```sh
mise run benchmark-go
```

The benchmark creates an isolated E2E home and session fixture, builds and starts the Go gateway in production mode on a free loopback port, warms `/` and `/sidebar`, measures 100 requests, samples the complete gateway process tree through `/proc`, and removes the fixture. It does not use or restart `gripi.service` and does not start real Pi.

The migration acceptance thresholds were:

- Go gateway median RSS no greater than 40% of the Ruby gateway median RSS.
- Go gateway p95 request time no greater than 125% of the Ruby gateway p95 under this workload.
- All external browser contract scenarios pass; a fast 404-only server is not a valid result.

## Final three-run medians

Measured on 2026-07-22 with three complete warmed runs per implementation:

| Implementation | Median RSS | Maximum RSS | Median request | p95 request |
| --- | ---: | ---: | ---: | ---: |
| Historical Ruby / Puma baseline | 60.62 MiB | 60.62 MiB | 5.45 ms | 7.11 ms |
| Go gateway | 21.81 MiB | 21.87 MiB | 1.97 ms | 3.13 ms |

The Go result is a **64.0% RSS reduction**. Its 21.81 MiB median is below the 24.25 MiB memory threshold, and its p95 latency also improves on the historical baseline. **The performance threshold passed.**

The historical baseline remains here for traceability of the migration decision; the benchmark harness and runtime now support only the Go gateway.

For context, the long-running development service showed about 218 MiB Puma RSS and 456 MiB total cgroup memory during the initial audit. Total cgroup memory can remain substantially higher than gateway RSS because it includes active Pi, subagent, and other child processes that a gateway rewrite cannot eliminate.
