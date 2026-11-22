package redisop

import (
	"encoding/json"
	"strconv"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callDelete 处理 redis.delete：一次 DEL 最多 100 个键。
func callDelete(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p deleteParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(instances, p.Instance)
	if perr != nil {
		return nil, perr
	}
	if len(p.Keys) == 0 {
		return nil, invalid("keys 不能为空")
	}
	if len(p.Keys) > maxDeleteKeys {
		return nil, invalid("keys 超过上限 " + strconv.Itoa(maxDeleteKeys))
	}
	seen := make(map[string]struct{}, len(p.Keys))
	for _, key := range p.Keys {
		if perr := validateKey(key); perr != nil {
			return nil, perr
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, invalid("keys 含重复键: " + key)
		}
		seen[key] = struct{}{}
	}
	result, err := exec.Delete(instance, p.Keys)
	if err != nil {
		return nil, classify(err)
	}
	if result.Keys == nil {
		result.Keys = []string{}
	}
	return encode(result)
}
