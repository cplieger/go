package measure

import (
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"time"
)

// The runtime/metrics values the go_memstats_* and runtime setting families
// read, as indexes into runtimeMetricNames.
const (
	rmHeapObjectsBytes = iota
	rmHeapUnusedBytes
	rmHeapReleasedBytes
	rmHeapFreeBytes
	rmHeapStacksBytes
	rmOSStacksBytes
	rmMSpanInuseBytes
	rmMSpanFreeBytes
	rmMCacheInuseBytes
	rmMCacheFreeBytes
	rmProfilingBucketsBytes
	rmMetadataOtherBytes
	rmOtherBytes
	rmTotalBytes
	rmAllocsBytes
	rmAllocsObjects
	rmFreesObjects
	rmTinyAllocsObjects
	rmHeapObjects
	rmHeapGoalBytes
	rmGOGC
	rmGOMEMLIMIT
	rmGOMAXPROCS
	rmCount
)

// runtimeMetricNames are the runtime/metrics names behind each rm index. Every
// one is a KindUint64 metric; TestRuntimeMetricNamesAreSupported fails on a Go
// release that drops or retypes one.
var runtimeMetricNames = [rmCount]string{
	rmHeapObjectsBytes:      "/memory/classes/heap/objects:bytes",
	rmHeapUnusedBytes:       "/memory/classes/heap/unused:bytes",
	rmHeapReleasedBytes:     "/memory/classes/heap/released:bytes",
	rmHeapFreeBytes:         "/memory/classes/heap/free:bytes",
	rmHeapStacksBytes:       "/memory/classes/heap/stacks:bytes",
	rmOSStacksBytes:         "/memory/classes/os-stacks:bytes",
	rmMSpanInuseBytes:       "/memory/classes/metadata/mspan/inuse:bytes",
	rmMSpanFreeBytes:        "/memory/classes/metadata/mspan/free:bytes",
	rmMCacheInuseBytes:      "/memory/classes/metadata/mcache/inuse:bytes",
	rmMCacheFreeBytes:       "/memory/classes/metadata/mcache/free:bytes",
	rmProfilingBucketsBytes: "/memory/classes/profiling/buckets:bytes",
	rmMetadataOtherBytes:    "/memory/classes/metadata/other:bytes",
	rmOtherBytes:            "/memory/classes/other:bytes",
	rmTotalBytes:            "/memory/classes/total:bytes",
	rmAllocsBytes:           "/gc/heap/allocs:bytes",
	rmAllocsObjects:         "/gc/heap/allocs:objects",
	rmFreesObjects:          "/gc/heap/frees:objects",
	rmTinyAllocsObjects:     "/gc/heap/tiny/allocs:objects",
	rmHeapObjects:           "/gc/heap/objects:objects",
	rmHeapGoalBytes:         "/gc/heap/goal:bytes",
	rmGOGC:                  "/gc/gogc:percent",
	rmGOMEMLIMIT:            "/gc/gomemlimit:bytes",
	rmGOMAXPROCS:            "/sched/gomaxprocs:threads",
}

// runtimeFamily is a Go family whose value is the sum of runtime/metrics
// values, the mapping client_golang's Go collector uses for the same name.
type runtimeFamily struct {
	name, help string
	typ        Type
	sum        []int
}

// runtimeFamilies are the go_memstats_* families and the three runtime
// setting families, each the sum of the runtime/metrics values it lists.
var runtimeFamilies = []runtimeFamily{
	{pmGOGC, "The GOGC setting: the heap growth percentage that triggers a collection.", TypeGauge, []int{rmGOGC}},
	{pmGOMEMLIMIT, "The GOMEMLIMIT setting: the Go runtime's soft memory limit in bytes.", TypeGauge, []int{rmGOMEMLIMIT}},
	{"go_memstats_alloc_bytes", "Bytes of allocated heap objects, the same value as go_memstats_heap_alloc_bytes.", TypeGauge, []int{rmHeapObjectsBytes}},
	{"go_memstats_alloc_bytes_total", "Total bytes allocated on the heap, including freed objects.", TypeCounter, []int{rmAllocsBytes}},
	{"go_memstats_buck_hash_sys_bytes", "Bytes of memory in profiling bucket hash tables.", TypeGauge, []int{rmProfilingBucketsBytes}},
	{"go_memstats_frees_total", "Total heap objects freed.", TypeCounter, []int{rmFreesObjects, rmTinyAllocsObjects}},
	{"go_memstats_gc_sys_bytes", "Bytes of memory in garbage collection metadata.", TypeGauge, []int{rmMetadataOtherBytes}},
	{pmHeapAllocBytes, helpHeapAlloc, TypeGauge, []int{rmHeapObjectsBytes}},
	{"go_memstats_heap_idle_bytes", "Bytes of heap memory with no objects in it, free or released.", TypeGauge, []int{rmHeapReleasedBytes, rmHeapFreeBytes}},
	{"go_memstats_heap_inuse_bytes", "Bytes of heap memory in spans that hold objects.", TypeGauge, []int{rmHeapObjectsBytes, rmHeapUnusedBytes}},
	{"go_memstats_heap_objects", "Number of allocated heap objects.", TypeGauge, []int{rmHeapObjects}},
	{"go_memstats_heap_released_bytes", "Bytes of heap memory returned to the operating system.", TypeGauge, []int{rmHeapReleasedBytes}},
	{"go_memstats_heap_sys_bytes", "Bytes of heap memory obtained from the operating system.", TypeGauge, []int{rmHeapObjectsBytes, rmHeapUnusedBytes, rmHeapReleasedBytes, rmHeapFreeBytes}},
	{"go_memstats_mallocs_total", "Total heap objects allocated, including freed ones.", TypeCounter, []int{rmAllocsObjects, rmTinyAllocsObjects}},
	{"go_memstats_mcache_inuse_bytes", "Bytes of memory in mcache structures in use.", TypeGauge, []int{rmMCacheInuseBytes}},
	{"go_memstats_mcache_sys_bytes", "Bytes of memory obtained for mcache structures.", TypeGauge, []int{rmMCacheInuseBytes, rmMCacheFreeBytes}},
	{"go_memstats_mspan_inuse_bytes", "Bytes of memory in mspan structures in use.", TypeGauge, []int{rmMSpanInuseBytes}},
	{"go_memstats_mspan_sys_bytes", "Bytes of memory obtained for mspan structures.", TypeGauge, []int{rmMSpanInuseBytes, rmMSpanFreeBytes}},
	{"go_memstats_next_gc_bytes", "Heap size in bytes at which the next collection starts.", TypeGauge, []int{rmHeapGoalBytes}},
	{"go_memstats_other_sys_bytes", "Bytes of memory in other runtime allocations.", TypeGauge, []int{rmOtherBytes}},
	{"go_memstats_stack_inuse_bytes", "Bytes of heap memory used for goroutine stacks.", TypeGauge, []int{rmHeapStacksBytes}},
	{"go_memstats_stack_sys_bytes", "Bytes of memory used for stacks, including operating system thread stacks.", TypeGauge, []int{rmHeapStacksBytes, rmOSStacksBytes}},
	{"go_memstats_sys_bytes", "Bytes of memory obtained from the operating system.", TypeGauge, []int{rmTotalBytes}},
	{pmGOMAXPROCS, "The GOMAXPROCS setting: how many threads can run Go code at once.", TypeGauge, []int{rmGOMAXPROCS}},
}

// gcPauseQuantiles are the quantiles go_gc_duration_seconds reports: the
// minimum, the quartiles and the maximum of the recent pauses.
var gcPauseQuantiles = [...]float64{0, 0.25, 0.5, 0.75, 1}

// goRuntime is one read of the Go runtime for the go_* families and
// process_gc_pause_seconds_total.
type goRuntime struct {
	gc      debug.GCStats
	rm      [rmCount]uint64
	threads int
}

// readGoRuntime reads runtime/metrics and the GC statistics without stopping
// the world, which runtime.ReadMemStats would. A name the running Go release
// does not support reads as 0, as client_golang reads it.
func readGoRuntime() *goRuntime {
	g := &goRuntime{}
	samples := make([]metrics.Sample, rmCount)
	for i := range samples {
		samples[i].Name = runtimeMetricNames[i]
	}
	metrics.Read(samples)
	for i := range samples {
		if samples[i].Value.Kind() == metrics.KindUint64 {
			g.rm[i] = samples[i].Value.Uint64()
		}
	}
	g.gc.PauseQuantiles = make([]time.Duration, len(gcPauseQuantiles))
	debug.ReadGCStats(&g.gc)
	g.threads, _ = runtime.ThreadCreateProfile(nil)
	return g
}

// families appends the go_* families to fams.
func (g *goRuntime) families(fams []Family) []Family {
	for i := range runtimeFamilies {
		f := &runtimeFamilies[i]
		var v uint64
		for _, idx := range f.sum {
			v += g.rm[idx]
		}
		fams = append(fams, scalarFamily(f.name, f.help, f.typ, float64(v)))
	}
	quantiles := make([]Quantile, len(gcPauseQuantiles))
	for i, q := range gcPauseQuantiles {
		quantiles[i] = Quantile{Quantile: q, Value: g.gc.PauseQuantiles[i].Seconds()}
	}
	return append(fams,
		Family{Name: pmGCDuration, Help: helpGCDuration, Type: TypeSummary, Samples: []Sample{{
			Labels:  []Label{},
			Summary: &SummaryValue{Quantiles: quantiles, Count: uint64(max(g.gc.NumGC, 0)), Sum: g.gc.PauseTotal.Seconds()},
		}}},
		scalarFamily(pmGoroutines, helpGoroutines, TypeGauge, float64(runtime.NumGoroutine())),
		Family{Name: pmGoInfo, Help: helpGoInfo, Type: TypeGauge, Samples: []Sample{{
			Labels: []Label{{Name: "version", Value: runtime.Version()}}, Value: 1,
		}}},
		scalarFamily(pmLastGC, helpLastGC, TypeGauge, float64(g.gc.LastGC.UnixNano())/1e9),
		scalarFamily(pmThreads, helpThreads, TypeGauge, float64(g.threads)),
	)
}
