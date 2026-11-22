package redisop

import (
	"encoding/json"
	"strconv"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callKeys 处理 redis.keys：校验实例/模式/上限后用 SCAN 游标遍历。
func callKeys(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p keysParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(instances, p.Instance)
	if perr != nil {
		return nil, perr
	}
	pattern := p.Pattern
	if pattern == "" {
		pattern = "*"
	}
	if perr := validatePattern(pattern); perr != nil {
		return nil, perr
	}
	limit := p.Limit
	if limit <= 0 {
		limit = defaultKeysLimit
	}
	if limit > maxKeysLimit {
		return nil, invalid("limit 超过上限 " + strconv.Itoa(maxKeysLimit))
	}
	result, err := exec.Keys(instance, pattern, limit)
	if err != nil {
		return nil, classify(err)
	}
	if result.Keys == nil {
		result.Keys = []KeyEntry{}
	}
	return encode(result)
}
