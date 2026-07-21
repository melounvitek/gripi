# Gateway performance

The Go rewrite targets Puma's resident memory rather than memory owned by Pi and its Node subprocesses.

## Repeatable benchmark

On Linux, run the seeded gateway workload with:

```sh
mise run benchmark-ruby
```

The equivalent Go command will be exposed once `/` and `/sidebar` have been ported; accepting placeholder 404 responses would produce a misleading comparison.

The benchmark creates an isolated E2E home and session fixture, starts the gateway in production mode on a free loopback port, warms `/` and `/sidebar`, measures 100 requests, samples the complete gateway process tree through `/proc`, and removes the fixture. It does not use or restart `gripi.service` and does not start real Pi.

The final comparison uses the median result from three runs. The acceptance thresholds are:

- Go gateway median RSS no greater than 40% of Ruby gateway median RSS.
- Go gateway p95 request time no greater than 125% of Ruby gateway p95 under this workload.
- All external browser contract scenarios pass; a fast 404-only server is not a valid result.

## Round 1 baseline

Measured on 2026-07-21 after four warmup requests against the seeded fixture. The table records the median result from three complete benchmark runs:

| Implementation | Median RSS | Maximum RSS | Median request | p95 request |
| --- | ---: | ---: | ---: | ---: |
| Ruby / Puma | 60.48 MiB | 60.48 MiB | 5.44 ms | 6.87 ms |

The final Go seeded-workload RSS target is therefore **24.19 MiB or less**. The benchmark records each machine-specific result as JSON, so final measurements can be repeated rather than relying solely on this snapshot.

For context, the long-running development service showed about 218 MiB Puma RSS and 456 MiB total cgroup memory during the initial audit. Total cgroup memory can remain substantially higher than gateway RSS because it includes active Pi, subagent, and other child processes that a Go rewrite cannot eliminate.
