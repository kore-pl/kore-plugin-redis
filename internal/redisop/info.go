package redisop

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callInfo 处理 redis.info：解析实例参数并返回 INFO 全量结构化结果。
func callInfo(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p infoParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(instances, p.Instance)
	if perr != nil {
		return nil, perr
	}
	result, err := exec.Info(instance)
	if err != nil {
		return nil, classify(err)
	}
	return encode(result)
}

// buildInfoResult 把 INFO 原文解析为显式结构；段内字段缺失时保持零值，
// 但 redis_version 缺失视为上游协议异常（无法判定版本）。
func buildInfoResult(instanceName, raw string) InfoResult {
	fields := parseInfoFields(raw)
	result := InfoResult{
		Instance: instanceName,
		Server: InfoServer{
			Version: fields["redis_version"],
			UptimeS: safeAtoi(fields["uptime_in_seconds"]),
			TCPPort: int(safeAtoi(fields["tcp_port"])),
		},
		Clients: InfoClients{Connected: safeAtoi(fields["connected_clients"])},
		Memory: InfoMemory{
			UsedMemoryHuman:     fields["used_memory_human"],
			UsedMemoryPeakHuman: fields["used_memory_peak_human"],
			MaxMemoryHuman:      fields["maxmemory_human"],
		},
		Stats: InfoStats{
			TotalConnectionsReceived: safeAtoi(fields["total_connections_received"]),
			TotalCommandsProcessed:   safeAtoi(fields["total_commands_processed"]),
			KeyspaceHits:             safeAtoi(fields["keyspace_hits"]),
			KeyspaceMisses:           safeAtoi(fields["keyspace_misses"]),
		},
		Persistence: InfoPersistence{
			RDBlastSaveTime: safeAtoi(fields["rdb_last_save_time"]),
			AOFEnabled:      safeAtoi(fields["aof_enabled"]) == 1,
		},
		Keyspace: parseKeyspace(fields),
	}
	return result
}

// parseKeyspace 从 dbN 字段归一出 "keys=<n>,expires=<n>"，
// 丢弃 avg_ttl，保持对外形状稳定（需求指定 keys/expires）。
func parseKeyspace(fields map[string]string) map[string]string {
	keyspace := make(map[string]string)
	for key, value := range fields {
		if !isDatabaseKey(key) {
			continue
		}
		parts := map[string]string{}
		for _, item := range strings.Split(value, ",") {
			name, number, ok := strings.Cut(item, "=")
			if !ok {
				continue
			}
			parts[name] = number
		}
		keyspace[key] = fmt.Sprintf("keys=%s,expires=%s", parts["keys"], parts["expires"])
	}
	return keyspace
}

// isDatabaseKey 判断字段名是否为 INFO keyspace 段的 dbN。
func isDatabaseKey(name string) bool {
	if !strings.HasPrefix(name, "db") || len(name) < 3 {
		return false
	}
	for _, r := range name[2:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
