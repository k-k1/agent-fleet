# #3102 study: making `internal/memoryx` tests faster (throwaway, not for merge)

Host: shared Workspace, 8 CPUs, no CPU quota. Load average recorded for every run.

## Runs (`summary.txt`)

| label | what | wall |
|---|---|---|
| base2/3/4 | serial, `main` test code, test binary + test2json | 107.6 / 105.3 / 105.4 s (load 3–7) |
| count1 | serial with the counting `git` shim on PATH (shim adds overhead) | 153.6 s |
| shard2a/4a/8a | serial binary, N concurrent `-test.run` shards balanced by base1 timings (`shard.py`) | 49.5 / 35.3 / 21.2 s |
| tpl2/3 | clone-template prototype only (commit ff74edc10) | 90.4 / 93.5 s |
| gotest shards=1/2/4/8 | real `go test -json`, template + self-sharding TestMain | 89.7 / 45.3 / 26.0 / 23.9 s (load 4–10) |
| gotest(load) shards=1/4 | same, while the host was busy | 185.4 s → 95.7 s (load 12–19) |
| gotest -race shards=4/1 | `go test -race` | 131.7 s vs 302.6 s (load 9–12) |

Ignore `base1` (wall not captured; its per-test sum is 140 s) and `tpl1` (prototype bug, fixed in tpl2).

## Where the 16.5k git processes come from (`git-call-attribution.txt`)

Test code was patched (`count-instrumentation.patch`, `zz_count_test.go.txt`) to mark every git
call it issues itself; the rest come from the code under test.

- code under test: 14,607 (88 %) — 10,107 on af-memory.git, 4,500 in working copies
  (project identity: `rev-parse`, `remote get-url`, `worktree list`; `ls-files`)
- test code (fixtures and assertions): 1,934 (12 %)
- 979 `gc --auto` calls come from production commits.
