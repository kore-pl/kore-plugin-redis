package redisop

import (
	"encoding/json"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// Call 执行一个 redis 方法：先校验方法名与参数，再调用执行面并结构化结果。
// exec 为 nil 时使用 noopExecutor（骨架口径，不连接任何 Redis 实例）。
func Call(method string, params json.RawMessage, defaultPrefix string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	verb, ok := businessVerb(method)
	if !ok {
		return nil, errNotFound(method)
	}
	if exec == nil {
		exec = noopExecutor{}
	}
	switch verb {
	case "instances":
		return callInstances(params, defaultPrefix, exec)
	case "scan":
		return callScan(params, defaultPrefix, exec)
	}
	return nil, errNotFound(method)
}

// businessVerb 把 redis.<verb> 或裸 <verb> 归一为动词名。
func businessVerb(method string) (string, bool) {
	verb := method
	if strings.HasPrefix(method, "redis.") {
		verb = strings.TrimPrefix(method, "redis.")
	}
	switch verb {
	case "instances", "scan":
		return verb, true
	}
	return "", false
}

// decodeParams 校验 params 是 JSON 对象并反序列化到目标形状。
func decodeParams(raw json.RawMessage, target any) *pluginapi.Error {
	if len(raw) == 0 || string(raw) == "null" {
		return invalid("缺少必填参数对象")
	}
	if raw[0] != '{' {
		return invalid("params 必须是 JSON 对象")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return invalid("params 解析失败: " + err.Error())
	}
	return nil
}

// encode 把结果编码为 json.RawMessage；序列化失败归 E_INTERNAL。
func encode(v any) (json.RawMessage, *pluginapi.Error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, internalErr("序列化结果失败: " + err.Error())
	}
	return data, nil
}
