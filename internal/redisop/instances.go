package redisop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callInstances 处理 redis.instances：校验内存策略后返回实例列表占位。
// 骨架口径下实例由使用者自行管理，不内置 Redis 服务、不发起连接。
func callInstances(raw json.RawMessage, defaultPrefix string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p instancesParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	policy, perr := validatePolicy(p.Policy)
	if perr != nil {
		return nil, perr
	}
	instances, err := exec.Instances(defaultPrefix)
	if err != nil {
		return nil, classify(err)
	}
	if instances == nil {
		instances = []Instance{}
	}
	return encode(InstancesResult{
		Instances:         instances,
		Count:             len(instances),
		Policy:            policy,
		ManagedExternally: true,
	})
}
