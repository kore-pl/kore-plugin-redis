package fault

import (
	"testing"

	"github.com/pengtang1024/kore-plugin-redis/internal/redisop"
)

// TestCgroupLimitFault 注入 cgroup 超限，期望两方法返回 E_UNSUPPORTED。
func TestCgroupLimitFault(t *testing.T) {
	assertFault(t, redisop.FaultCgroup, "E_UNSUPPORTED", "cgroup")
}

// TestCgroupFaultSanity 无故障时正常返回，证明 cgroup 断言不是假通过。
func TestCgroupFaultSanity(t *testing.T) {
	sanityNoFault(t)
}
