# Performance baseline — 2026-09-05

Go 1.26.3, darwin/arm64, Apple M1 Max, default GOMAXPROCS=10.
Five samples per fixture, measured after integration and smoke workloads finished.
Values below are medians, rounded; these are local baselines, not CI thresholds.

```sh
GOCACHE=/tmp/mcpmu-gocache go test ./internal/server ./internal/metrics \
  -run '^$' -bench 'Benchmark(ToolListing|Compression|Routing|MetricsQueries)$' \
  -benchmem -benchtime=200ms -count=5
```

Tool fixtures distribute 100, 1,000 or 10,000 synthetic tools across ten servers.
Listing measures catalog exposure and namespace permission checks. Compression
covers every level with a one-property schema and a two-sentence description;
output-B measures the compact listing text, excluding fixed wrapper schemas.
Routing uses a warmed mcptest subprocess with no configured delay and compares
direct MCP transport, Router dispatch and parallel Router calls. Transport latency
is included; differences are indicative, not a precise isolation of routing cost.
Logging is discarded and setup is outside the timed loop. Metrics measures table,
daily totals and summary over 60,000 buckets (60 days × 1,000 tools, ten servers).
The existing web parsed-file cache is unchanged.

| Fixture | Median μs/op | B/op | Allocations/op | Listing bytes |
|---|---:|---:|---:|---:|
| ToolListing/100 | 34.44 | 78,825 | 418 | 0 |
| ToolListing/1000 | 400.21 | 924,595 | 4,024 | 0 |
| ToolListing/10000 | 4,711.27 | 13,006,261 | 40,037 | 0 |
| Compression/100/low | 243.63 | 220,936 | 5,014 | 9,199 |
| Compression/100/medium | 245.32 | 211,464 | 5,013 | 6,199 |
| Compression/100/high | 206.48 | 176,776 | 4,711 | 3,599 |
| Compression/100/max | 2.58 | 11,184 | 11 | 3,199 |
| Compression/1000/low | 2,424.07 | 2,262,380 | 50,022 | 91,999 |
| Compression/1000/medium | 2,436.89 | 2,164,078 | 50,021 | 61,999 |
| Compression/1000/high | 2,107.24 | 1,809,004 | 47,019 | 35,999 |
| Compression/1000/max | 28.80 | 153,009 | 19 | 31,999 |
| Compression/10000/low | 23,527.98 | 24,040,260 | 500,032 | 919,999 |
| Compression/10000/medium | 23,931.74 | 22,041,400 | 500,030 | 619,999 |
| Compression/10000/high | 30,254.65 | 18,540,013 | 470,029 | 359,999 |
| Compression/10000/max | 579.06 | 1,537,465 | 27 | 319,999 |
| Routing/direct | 95.24 | 1,649 | 30 | 0 |
| Routing/router | 43.34 | 3,438 | 53 | 0 |
| Routing/parallel | 7.35 | 3,459 | 53 | 0 |
| MetricsQueries | 53,544.22 | 1,496,000 | 6,178 | 0 |

No optimization was introduced. At large cardinalities, schema parsing in
compression and metrics scans are reasonable profiling candidates, but these
synthetic measurements alone do not establish enough real request volume to
justify caching complexity and authorization invalidation risk. The fixtures
provide a repeatable baseline for a separately measured optimization.
