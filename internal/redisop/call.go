package redisop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// Call 执行一个 redis 方法：先校验方法名与参数，再调用执行面并结构化结果。
// exec 为 nil 时使用真实 RESP 客户端；instances 来自配置契约，不在本包内落盘。
func Call(method string, params json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	verb, ok := businessVerb(method)
	if !ok {
		return nil, errNotFound(method)
	}
	if exec == nil {
		exec = NewClient()
	}
	switch verb {
	case "instances":
		return callInstances(params, instances, exec)
	case "info":
		return callInfo(params, instances, exec)
	case "keys":
		return callKeys(params, instances, exec)
	case "get":
		return callGet(params, instances, exec)
	case "delete":
		return callDelete(params, instances, exec)
	case "persistence":
		return callPersistence(params, instances, exec)
	case "memory":
		return callMemory(params, instances, exec)
	case "slowlog":
		return callSlowlog(params, instances, exec)
	case "keyInfo":
		return callKeyInfo(params, instances, exec)
	}
	return nil, errNotFound(method)
}

// methodPrefix 是方法名的插件域前缀：协议要求 `<plugin-id>.<action>`。
const methodPrefix = "redis."

// businessVerb 把 redis.<verb> 归一为动词名；域不匹配的一律拒绝。
func businessVerb(method string) (string, bool) {
	verb, ok := strings.CutPrefix(method, methodPrefix)
	if !ok {
		return "", false
	}
	switch verb {
	case "instances", "info", "keys", "get", "delete", "persistence", "memory", "slowlog", "keyInfo":
		return verb, true
	}
	return "", false
}

// findInstance 按名字取实例配置；单实例时允许省略 instance。
// 名字非法返回 E_INVALID，未配置返回 E_NOTFOUND。
func findInstance(instances []InstanceConfig, name string) (InstanceConfig, *pluginapi.Error) {
	if name == "" {
		switch len(instances) {
		case 0:
			return InstanceConfig{}, &pluginapi.Error{
				Code:    pluginapi.E_NOTFOUND,
				Message: "尚未配置 Redis 实例，请先在设置中填写地址",
			}
		case 1:
			return instances[0], nil
		default:
			return InstanceConfig{}, invalid("存在多个实例，请指定 instance")
		}
	}
	if err := ValidateInstanceName(name); err != nil {
		return InstanceConfig{}, invalid(err.Error())
	}
	for _, instance := range instances {
		if instance.Name == name {
			return instance, nil
		}
	}
	return InstanceConfig{}, &pluginapi.Error{
		Code:    pluginapi.E_NOTFOUND,
		Message: fmt.Sprintf("未配置实例 %q（已配置: %s）", name, configuredNames(instances)),
	}
}

// configuredNames 返回已配置实例名摘要，用于 E_NOTFOUND 提示（不含地址与密码）。
func configuredNames(instances []InstanceConfig) string {
	if len(instances) == 0 {
		return "无"
	}
	names := make([]string, 0, len(instances))
	for index, instance := range instances {
		if index == 10 {
			names = append(names, "…")
			break
		}
		names = append(names, instance.Name)
	}
	return strings.Join(names, ", ")
}

// classify 把执行面错误映射为协议错误码：OpError 优先，其次四类故障注入。
func classify(err error) *pluginapi.Error {
	var opErr *OpError
	if errors.As(err, &opErr) {
		return &pluginapi.Error{Code: opErr.Code, Message: opErr.Message}
	}
	var faultErr *FaultError
	if errors.As(err, &faultErr) {
		switch faultErr.Kind {
		case FaultKill:
			return upstream("进程被信号 9 终止: " + faultErr.Message)
		case FaultTimeout:
			return upstream("调用超时: " + faultErr.Message)
		case FaultDisk:
			return internalErr("磁盘满: " + faultErr.Message)
		case FaultCgroup:
			return unsupported("cgroup 限额触发: " + faultErr.Message)
		}
	}
	return internalErr("执行面失败: " + err.Error())
}

func invalid(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_INVALID, Message: msg}
}

func errNotFound(method string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_NOTFOUND, Message: "未知方法: " + method}
}

func upstream(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_UPSTREAM, Message: msg}
}

func internalErr(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_INTERNAL, Message: msg}
}

func unsupported(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_UNSUPPORTED, Message: msg}
}

// decodeParams 要求 params 是 JSON 对象并反序列化到目标形状。
func decodeParams(raw json.RawMessage, target any) *pluginapi.Error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return invalid("缺少必填参数对象")
	}
	if trimmed[0] != '{' {
		return invalid("params 必须是 JSON 对象")
	}
	if err := json.Unmarshal(trimmed, target); err != nil {
		return invalid("params 解析失败: " + err.Error())
	}
	return nil
}

// decodeObjectOrEmpty 允许 params 缺省/空对象（instances 无业务参数）。
func decodeObjectOrEmpty(raw json.RawMessage) *pluginapi.Error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" || string(trimmed) == "{}" {
		return nil
	}
	if trimmed[0] != '{' {
		return invalid("params 必须是 JSON 对象")
	}
	var probe struct{}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
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
