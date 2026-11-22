package redisop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callGet 处理 redis.get：按 key 的类型分派读取，返回类型 / TTL / 取值。
func callGet(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p getParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(instances, p.Instance)
	if perr != nil {
		return nil, perr
	}
	if perr := validateKey(p.Key); perr != nil {
		return nil, perr
	}
	result, err := exec.Get(instance, p.Key)
	if err != nil {
		return nil, classify(err)
	}
	return encode(result)
}
