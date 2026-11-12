window.BENCHMARK_DATA = {
  "lastUpdate": 1794451717798,
  "repoUrl": "https://github.com/cplieger/ci",
  "entries": {
    "Benchmark": [
      {
        "commit": {
          "author": {
            "name": "Christopher Plieger",
            "username": "cplieger",
            "email": "917744+cplieger@users.noreply.github.com"
          },
          "committer": {
            "name": "GitHub",
            "username": "web-flow",
            "email": "noreply@github.com"
          },
          "id": "cfca5f4e73eeba96fcf294f8fcd4851873467031",
          "message": "feat: pass each repository's CI_RUNNER to the shared workflows (#1053)\n\n## Release note\n\nThe synced `ci.yml` and `security.yml` callers now pass the repository's `CI_RUNNER` variable as the shared workflows' `runner` input, so setting that variable moves a repository's CI onto a self-hosted runner, and leaving it unset keeps GitHub-hosted runners.",
          "timestamp": "2026-11-12T02:09:18Z",
          "url": "https://github.com/cplieger/ci/commit/cfca5f4e73eeba96fcf294f8fcd4851873467031"
        },
        "date": 1794451717524,
        "tool": "customSmallerIsBetter",
        "benches": [
          {
            "name": "BenchmarkIsHashed/hashed - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIsHashed/hashed - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIsHashed/hashed",
            "value": 33.585,
            "range": "± 0.535",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIsHashed/raw - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIsHashed/raw - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIsHashed/raw",
            "value": 2.806,
            "range": "± 0.0045",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinAllReservedBytes/hashed_8196B - B/op",
            "value": 9456,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinAllReservedBytes/hashed_8196B - allocs/op",
            "value": 8,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinAllReservedBytes/hashed_8196B",
            "value": 6981.5,
            "range": "± 141.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinAllReservedBytes/joined_4096B - B/op",
            "value": 25920,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinAllReservedBytes/joined_4096B - allocs/op",
            "value": 10,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinAllReservedBytes/joined_4096B",
            "value": 21461,
            "range": "± 54.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinAllReservedBytes/joined_8192B_at_bound - B/op",
            "value": 51264,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinAllReservedBytes/joined_8192B_at_bound - allocs/op",
            "value": 10,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinAllReservedBytes/joined_8192B_at_bound",
            "value": 42498,
            "range": "± 1354.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinComponentCount/parts_128 - B/op",
            "value": 4608,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinComponentCount/parts_128 - allocs/op",
            "value": 2,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinComponentCount/parts_128",
            "value": 4108,
            "range": "± 165.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinComponentCount/parts_32 - B/op",
            "value": 1088,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinComponentCount/parts_32 - allocs/op",
            "value": 2,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinComponentCount/parts_32",
            "value": 1130.5,
            "range": "± 3.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinComponentCount/parts_8 - B/op",
            "value": 272,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinComponentCount/parts_8 - allocs/op",
            "value": 2,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinComponentCount/parts_8",
            "value": 322.85,
            "range": "± 13.15",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinTotalSize/hashed_65536B - B/op",
            "value": 65776,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinTotalSize/hashed_65536B - allocs/op",
            "value": 8,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinTotalSize/hashed_65536B",
            "value": 50728.5,
            "range": "± 1389.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinTotalSize/hashed_8196B - B/op",
            "value": 9456,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinTotalSize/hashed_8196B - allocs/op",
            "value": 8,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinTotalSize/hashed_8196B",
            "value": 6966.5,
            "range": "± 29.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinTotalSize/joined_64B - B/op",
            "value": 144,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinTotalSize/joined_64B - allocs/op",
            "value": 2,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinTotalSize/joined_64B",
            "value": 167.7,
            "range": "± 0.65",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinTotalSize/joined_8188B - B/op",
            "value": 8256,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinTotalSize/joined_8188B - allocs/op",
            "value": 2,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinTotalSize/joined_8188B",
            "value": 2177.5,
            "range": "± 108.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinTotalSize/joined_8192B_at_bound - B/op",
            "value": 9536,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinTotalSize/joined_8192B_at_bound - allocs/op",
            "value": 2,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkJoinTotalSize/joined_8192B_at_bound",
            "value": 1922,
            "range": "± 128.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSplit/escaped_2048B - B/op",
            "value": 4128,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSplit/escaped_2048B - allocs/op",
            "value": 29,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSplit/escaped_2048B",
            "value": 11923,
            "range": "± 299.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSplit/verbatim_2048B - B/op",
            "value": 4128,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSplit/verbatim_2048B - allocs/op",
            "value": 29,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSplit/verbatim_2048B",
            "value": 8361.5,
            "range": "± 29.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSplitRefusal/hashed - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSplitRefusal/hashed - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSplitRefusal/hashed",
            "value": 36.09,
            "range": "± 0.245",
            "unit": "ns/op",
            "extra": "10 samples, median"
          }
        ]
      }
    ]
  }
}