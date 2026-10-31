package runtime

import "strconv"

// MemoryLimitEnv carries the memory limit, in bytes, that the adapter asked the runtime to put
// on the workspace container. The Agent compares it with its cgroup's memory.max
// (workspace/agent/internal/mempressure, ADR 0120 decision 1). It is set only where a limit is
// actually requested: absent means "not told" and the Agent reports the comparison as unknown,
// so a runtime without a limit (native, an uncapped ecs-ec2 rung) must never write a 0 here.
const MemoryLimitEnv = "AF_WS_MEMORY_LIMIT_BYTES"

// memoryLimitValue is MemoryLimitEnv's value for a limit in bytes, or "" when no limit is
// requested (the caller then leaves the variable out).
func memoryLimitValue(bytes int64) string {
	if bytes <= 0 {
		return ""
	}
	return strconv.FormatInt(bytes, 10)
}
