package redisop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callScan 处理 redis.scan：校验前缀、策略、limit 与 cursor 后返回键列表占位。
// 骨架口径下不发起 SCAN 网络调用，只做逻辑校验与结构化占位。
func callScan(rawParams json.RawMessage, defaultPrefix string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p scanParams
	if perr := decodeParams(rawParams, &p); perr != nil {
		return nil, perr
	}
	prefix := p.Prefix
	if prefix == "" {
		prefix = defaultPrefix
	}
	prefix, perr := validatePrefix(prefix)
	if perr != nil {
		return nil, perr
	}
	policy, perr := validatePolicy(p.Policy)
	if perr != nil {
		return nil, perr
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > maxScanLimit {
		return nil, invalid("limit 超过上限")
	}
	if p.Cursor < 0 {
		return nil, invalid("cursor 不能为负")
	}
	keys, err := exec.Scan(prefix, policy, limit, p.Cursor)
	if err != nil {
		return nil, classify(err)
	}
	if keys == nil {
		keys = []KeyEntry{}
	}
	scanned := len(keys)
	return encode(ScanResult{
		Prefix:  prefix,
		Policy:  policy,
		Cursor:  p.Cursor,
		Scanned: scanned,
		Keys:    keys,
		HasMore: scanned == limit,
	})
}
