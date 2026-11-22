package fault

import (
	"encoding/json"
	"testing"

	"github.com/pengtang1024/kore-plugin-redis/internal/redisop"
)

// TestDiskFullFault 注入磁盘满，期望两方法返回 E_INTERNAL。
func TestDiskFullFault(t *testing.T) {
	assertFault(t, redisop.FaultDisk, "E_INTERNAL", "磁盘满")
}

// TestDiskFaultContract 单独验证磁盘满故障的错误体结构完整（真实调用，非空壳）。
func TestDiskFaultContract(t *testing.T) {
	_, perr := redisop.Call("redis.instances", json.RawMessage(`{"policy":"noeviction"}`), testPrefix, faultExecutor{kind: redisop.FaultDisk})
	verifyErrorContract(t, perr)
}
