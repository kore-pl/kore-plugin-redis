package redisop

import "github.com/pengtang1024/kore/pkg/pluginapi"

// supportedPolicies 是骨架支持的 Redis 内存淘汰策略全集（Redis 官方 8 种）。
// allkeys-lru / noeviction 等均在此列；空字符串按 noeviction 处理。
var supportedPolicies = map[string]bool{
	"noeviction":      true,
	"allkeys-lru":     true,
	"allkeys-lfu":     true,
	"allkeys-random":  true,
	"volatile-lru":    true,
	"volatile-lfu":    true,
	"volatile-random": true,
	"volatile-ttl":    true,
}

// validatePolicy 校验内存策略；空值归一为 noeviction。
// policy 只是逻辑配置，不改变任何真实实例的运行状态。
func validatePolicy(raw string) (string, *pluginapi.Error) {
	policy := raw
	if policy == "" {
		policy = "noeviction"
	}
	if !supportedPolicies[policy] {
		return "", invalid("不支持的内存策略: " + policy)
	}
	return policy, nil
}
