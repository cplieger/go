# Third-party notices

This library has no dependencies and contains no third-party code.

Three designs are followed from [prometheus/client_golang](https://github.com/prometheus/client_golang) (Apache-2.0) without any of its code being included; each is named at the lines of ours that follow it:

- **A construction error is captured into the metric value instead of panicking**, the `Desc.err` shape in client_golang's `prometheus/desc.go`. Ours is the `err` field on each of the six metric types (`counter.go:38`, `counter.go:132`, `gauge.go:11`, `gauge.go:84`, `histogram.go:97`, `histogram.go:184`), the same field on the registry for an invalid prefix (`measure.go:103`), and the constructor-side check that returns the error to capture (`validate.go:121`). Recording then diverges: a metric carrying an error records nothing here, while client_golang keeps recording and `Register` returns the error, so the metric is never exposed.
- **`MustRegister` panics on the first registration error**, the shape of `(*Registry).MustRegister` in client_golang's `prometheus/registry.go`. Ours is `measure.go:227`.
- **The Go and process families take the names, types and value mappings of client_golang's default collectors**, so dashboards built for client_golang read measure's page: the `go_memstats_*` mapping from `runtime/metrics` (`memStatsFromRM` in `prometheus/go_collector_latest.go`), the GC pause summary, `go_info`, `go_threads` and `go_memstats_last_gc_time_seconds` (`prometheus/go_collector.go`), and the `/proc` families (`prometheus/process_collector_procfsenabled.go`). Ours is `runtimeFamilies` and `goRuntime.families` in `goruntime.go` and `processGather` in `gather.go`. The HELP texts are our own.
