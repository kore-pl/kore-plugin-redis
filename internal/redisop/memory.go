package redisop

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callMemory 处理 redis.memory：解析实例参数并返回内存策略与用量。
func callMemory(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p infoParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(instances, p.Instance)
	if perr != nil {
		return nil, perr
	}
	result, err := exec.Memory(instance)
	if err != nil {
		return nil, classify(err)
	}
	return encode(result)
}

// Memory 读取 INFO memory + INFO stats + CONFIG GET maxmemory-policy。
// 三条命令共用一条连接；云托管实例禁用 CONFIG 时策略降级为空串（不失败），
// evicted_keys 来自 INFO stats（INFO memory 无此字段）。
func (c *Client) Memory(instance InstanceConfig) (MemoryResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return MemoryResult{}, err
	}
	defer func() { _ = conn.Close() }()

	memoryReply, err := conn.do("INFO", "memory")
	if err != nil {
		return MemoryResult{}, upstreamError(err, "INFO memory 失败")
	}
	memoryText, ok := asString(memoryReply)
	if !ok {
		return MemoryResult{}, opErrorf(pluginapi.E_UPSTREAM, "INFO memory 返回非字符串")
	}
	statsReply, err := conn.do("INFO", "stats")
	if err != nil {
		return MemoryResult{}, upstreamError(err, "INFO stats 失败")
	}
	statsText, ok := asString(statsReply)
	if !ok {
		return MemoryResult{}, opErrorf(pluginapi.E_UPSTREAM, "INFO stats 返回非字符串")
	}
	policy := ""
	if configReply, err := conn.do("CONFIG", "GET", "maxmemory-policy"); err == nil {
		policy = configGetString(configReply, "maxmemory-policy")
	}
	return buildMemoryResult(instance.Name, memoryText, statsText, policy), nil
}

// buildMemoryResult 把 INFO memory / INFO stats 原文与策略值组装为显式结果。
func buildMemoryResult(instanceName, memoryRaw, statsRaw, policy string) MemoryResult {
	memory := parseInfoFields(memoryRaw)
	stats := parseInfoFields(statsRaw)
	ratio, err := strconv.ParseFloat(strings.TrimSpace(memory["mem_fragmentation_ratio"]), 64)
	if err != nil {
		ratio = 0
	}
	return MemoryResult{
		Instance:           instanceName,
		Used:               safeAtoi(memory["used_memory"]),
		Peak:               safeAtoi(memory["used_memory_peak"]),
		MaxMemory:          safeAtoi(memory["maxmemory"]),
		MaxMemoryPolicy:    policy,
		FragmentationRatio: ratio,
		EvictionKeys:       safeAtoi(stats["evicted_keys"]),
	}
}

// configGetString 从 CONFIG GET 的扁平数组回复（[k1,v1,k2,v2,…]）里取指定键的值；
// 形状异常或键不存在返回空串。
func configGetString(reply any, name string) string {
	items, ok := asArray(reply)
	if !ok || len(items)%2 != 0 {
		return ""
	}
	for index := 0; index+1 < len(items); index += 2 {
		key, keyOK := asString(items[index])
		value, valueOK := asString(items[index+1])
		if keyOK && valueOK && strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}
