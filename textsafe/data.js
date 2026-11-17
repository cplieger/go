window.BENCHMARK_DATA = {
  "lastUpdate": 1794959651808,
  "repoUrl": "https://github.com/cplieger/ci",
  "entries": {
    "Benchmark": [
      {
        "commit": {
          "author": {
            "name": "tribble-trouble[bot]",
            "username": "tribble-trouble[bot]",
            "email": "338123824+tribble-trouble[bot]@users.noreply.github.com"
          },
          "committer": {
            "name": "GitHub",
            "username": "web-flow",
            "email": "noreply@github.com"
          },
          "id": "1a1cd0abda34b43c32b00ff977c7f8297d4c72e3",
          "message": "chore(deps): update cplieger/ci digest to 3b145f1 (#1156)\n\nCo-authored-by: tribble-trouble[bot] <338123824+tribble-trouble[bot]@users.noreply.github.com>",
          "timestamp": "2026-11-17T23:23:06Z",
          "url": "https://github.com/cplieger/ci/commit/1a1cd0abda34b43c32b00ff977c7f8297d4c72e3"
        },
        "date": 1794959651404,
        "tool": "customSmallerIsBetter",
        "benches": [
          {
            "name": "BenchmarkAggregate/join_then_capped - B/op",
            "value": 624,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkAggregate/join_then_capped - allocs/op",
            "value": 3,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkAggregate/join_then_capped",
            "value": 1125,
            "range": "± 1.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkAggregate/streaming - B/op",
            "value": 624,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkAggregate/streaming - allocs/op",
            "value": 3,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkAggregate/streaming",
            "value": 1092.5,
            "range": "± 2.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCapBytes - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCapBytes - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCapBytes",
            "value": 2.8515,
            "range": "± 0.003",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNewUntrusted - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNewUntrusted - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNewUntrusted",
            "value": 11434,
            "range": "± 86.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/clean_ascii/bytes_256 - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/clean_ascii/bytes_256 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/clean_ascii/bytes_256",
            "value": 722.6,
            "range": "± 3.15",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/clean_ascii/bytes_4096 - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/clean_ascii/bytes_4096 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/clean_ascii/bytes_4096",
            "value": 11386,
            "range": "± 83.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/clean_ascii/bytes_65536 - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/clean_ascii/bytes_65536 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/clean_ascii/bytes_65536",
            "value": 182301,
            "range": "± 1937.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/clean_utf8/bytes_4096 - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/clean_utf8/bytes_4096 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/clean_utf8/bytes_4096",
            "value": 7716,
            "range": "± 7.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/invalid_utf8/bytes_4096 - B/op",
            "value": 11392,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/invalid_utf8/bytes_4096 - allocs/op",
            "value": 2,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/invalid_utf8/bytes_4096",
            "value": 19755.5,
            "range": "± 47.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/unsafe_dense/bytes_4096 - B/op",
            "value": 4864,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/unsafe_dense/bytes_4096 - allocs/op",
            "value": 1,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/unsafe_dense/bytes_4096",
            "value": 10950.5,
            "range": "± 26.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/unsafe_dense/bytes_65536 - B/op",
            "value": 73728,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/unsafe_dense/bytes_65536 - allocs/op",
            "value": 1,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/unsafe_dense/bytes_65536",
            "value": 177799,
            "range": "± 439.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/unsafe_tail/bytes_4096 - B/op",
            "value": 4864,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/unsafe_tail/bytes_4096 - allocs/op",
            "value": 1,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitize/unsafe_tail/bytes_4096",
            "value": 11733,
            "range": "± 56.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeSingleLineBounded - B/op",
            "value": 208,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeSingleLineBounded - allocs/op",
            "value": 1,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeSingleLineBounded",
            "value": 11339.5,
            "range": "± 24.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkWorkBound/cap_first/bytes_1048576 - B/op",
            "value": 368,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkWorkBound/cap_first/bytes_1048576 - allocs/op",
            "value": 4,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkWorkBound/cap_first/bytes_1048576",
            "value": 526.85,
            "range": "± 1.3",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkWorkBound/cap_first/bytes_4096 - B/op",
            "value": 368,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkWorkBound/cap_first/bytes_4096 - allocs/op",
            "value": 4,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkWorkBound/cap_first/bytes_4096",
            "value": 515,
            "range": "± 2.7",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkWorkBound/sanitize_first/bytes_1048576 - B/op",
            "value": 4046913,
            "range": "± 1.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkWorkBound/sanitize_first/bytes_1048576 - allocs/op",
            "value": 4,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkWorkBound/sanitize_first/bytes_1048576",
            "value": 5576803,
            "range": "± 57364.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkWorkBound/sanitize_first/bytes_4096 - B/op",
            "value": 11456,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkWorkBound/sanitize_first/bytes_4096 - allocs/op",
            "value": 3,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkWorkBound/sanitize_first/bytes_4096",
            "value": 19866.5,
            "range": "± 77.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          }
        ]
      }
    ]
  }
}