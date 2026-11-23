package redisop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callPersistence 处理 redis.persistence：解析实例参数并返回持久化状态。
func callPersistence(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p infoParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(instances, p.Instance)
	if perr != nil {
		return nil, perr
	}
	result, err := exec.Persistence(instance)
	if err != nil {
		return nil, classify(err)
	}
	return encode(result)
}

// Persistence 读取 INFO persistence 并解析为显式结构；字段缺失时保持零值，
// aof_current_size 在 AOF 关闭的实例上不存在，此时 aof_size=0。
func (c *Client) Persistence(instance InstanceConfig) (PersistenceResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return PersistenceResult{}, err
	}
	defer func() { _ = conn.Close() }()

	reply, err := conn.do("INFO", "persistence")
	if err != nil {
		return PersistenceResult{}, upstreamError(err, "INFO persistence 失败")
	}
	text, ok := asString(reply)
	if !ok {
		return PersistenceResult{}, opErrorf(pluginapi.E_UPSTREAM, "INFO persistence 返回非字符串")
	}
	return buildPersistenceResult(instance.Name, text), nil
}

// buildPersistenceResult 把 INFO persistence 原文解析为显式结果。
func buildPersistenceResult(instanceName, raw string) PersistenceResult {
	fields := parseInfoFields(raw)
	return PersistenceResult{
		Instance:            instanceName,
		AOFEnabled:          safeAtoi(fields["aof_enabled"]) == 1,
		AOFLastWriteStatus:  fields["aof_last_write_status"],
		AOFSize:             safeAtoi(fields["aof_current_size"]),
		RDBLastSaveTime:     safeAtoi(fields["rdb_last_save_time"]),
		RDBChangesSinceSave: safeAtoi(fields["rdb_changes_since_save"]),
		RDBBgsaveInProgress: safeAtoi(fields["rdb_bgsave_in_progress"]) == 1,
	}
}
