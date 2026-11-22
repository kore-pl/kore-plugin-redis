package redisop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callInstances 处理 redis.instances：对每个已配置实例做 PING + INFO server 探测。
func callInstances(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	if perr := decodeObjectOrEmpty(raw); perr != nil {
		return nil, perr
	}
	statuses, err := exec.Instances(instances)
	if err != nil {
		return nil, classify(err)
	}
	if statuses == nil {
		statuses = []InstanceStatus{}
	}
	return encode(InstancesResult{Instances: statuses, Count: len(statuses)})
}
