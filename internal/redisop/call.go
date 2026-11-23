package redisop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// methodHandler 是单个业务方法的处理函数：参数 → 执行面 → 结果。
type methodHandler func(json.RawMessage, []InstanceConfig, Executor) (json.RawMessage, *pluginapi.Error)

// methodTable 是方法面的唯一真源：动词 → 处理函数。
//
// 新增方法必须同时登记到这里与 info.json 的 method_capabilities；
// capability_test.go 对两侧做双向断言（声明必须有实现、实现必须有声明）。
// 这里没有任意命令 / 脚本入口：方法名是闭合集合，白名单外的名字一律拒绝。
var methodTable = map[string]methodHandler{
	"instances":   callInstances,
	"info":        callInfo,
	"keys":        callKeys,
	"get":         callGet,
	"delete":      callDelete,
	"persistence": callPersistence,
	"memory":      callMemory,
	"slowlog":     callSlowlog,
	"keyInfo":     callKeyInfo,
	"set":         callSet,
	"ttl":         callTTL,
	"expire":      callExpire,
	"persist":     callPersist,
	"bgsave":      callBgsave,
}

// dottedMethods 是点分动词的方法（动作段允许点分，见内核 methodActionPattern）。
var dottedMethods = map[string]methodHandler{
	"serverConfig.get": callServerConfigGet,
	"serverConfig.set": callServerConfigSet,
}

// Methods 返回方法面全部完整方法名（按字典序），供能力声明一致性测试双向断言。
func Methods() []string {
	methods := make([]string, 0, len(methodTable)+len(dottedMethods))
	for verb := range methodTable {
		methods = append(methods, methodPrefix+verb)
	}
	for verb := range dottedMethods {
		methods = append(methods, methodPrefix+verb)
	}
	sort.Strings(methods)
	return methods
}

// Call 执行一个 redis 方法：先校验方法名与参数，再调用执行面并结构化结果。
// exec 为 nil 时使用真实 RESP 客户端；instances 来自配置契约，不在本包内落盘。
func Call(method string, params json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	verb, ok := businessVerb(method)
	if !ok {
		return nil, methodAbsentError(method)
	}
	if exec == nil {
		exec = NewClient()
	}
	if handler, ok := dottedMethods[verb]; ok {
		return handler(params, instances, exec)
	}
	return methodTable[verb](params, instances, exec)
}

// methodPrefix 是方法名的插件域前缀：协议要求 `<plugin-id>.<action>`。
const methodPrefix = "redis."

// businessVerb 把 redis.<verb> 归一为动词名；域不匹配或动词不在方法面一律拒绝。
// 动词大小写敏感（keyInfo ≠ keyinfo），点分动词只允许 serverConfig.get/set。
func businessVerb(method string) (string, bool) {
	verb, ok := strings.CutPrefix(method, methodPrefix)
	if !ok || verb == "" {
		return "", false
	}
	if _, ok := dottedMethods[verb]; ok {
		return verb, true
	}
	if _, ok := methodTable[verb]; ok {
		return verb, true
	}
	return "", false
}

// deniedVerbReasons 是明确不开放的方法动词及原因；错误码保持 E_NOTFOUND（未知方法），
// 但错误信息必须说明「为什么拒绝」与「用什么替代」，不做静默忽略。
var deniedVerbReasons = map[string]string{
	"flushall": "FLUSHALL 会清空全部数据库且不可恢复；请用 redis.keys 定位后调用 redis.delete 逐键删除",
	"flushdb":  "FLUSHDB 会清空当前数据库且不可恢复；请用 redis.keys 定位后调用 redis.delete 逐键删除",
	"eval":     "本插件不提供任意脚本执行入口（可绕过命令白名单）；派生值请在客户端计算",
	"evalsha":  "本插件不提供任意脚本执行入口（可绕过命令白名单）；派生值请在客户端计算",
	"script":   "本插件不提供服务端脚本管理入口；派生值请在客户端计算",
	"function": "本插件不提供服务端函数入口（可绕过命令白名单）",
	"save":     "SAVE 会阻塞 Redis 主线程直到落盘完成；请用 redis.bgsave 在后台落盘",
	"shutdown": "关闭 Redis 请用系统服务管理器（如 systemctl restart redis），本插件不提供该入口",
	"keys":     "KEYS 会阻塞 Redis 主线程；请用 redis.keys（SCAN 游标遍历）",
	"debug":    "DEBUG 系列可读内存、改运行时行为；不开放",
	"config":   "CONFIG 只通过 redis.serverConfig.get/set 的白名单参数开放；请用这两个方法",
}

// methodAbsentError 构造未知方法错误；对已知危险动词补充拒绝原因与替代做法。
func methodAbsentError(method string) *pluginapi.Error {
	if verb, ok := strings.CutPrefix(method, methodPrefix); ok {
		if reason, denied := deniedVerbReasons[verb]; denied {
			return &pluginapi.Error{
				Code:    pluginapi.E_NOTFOUND,
				Message: "未知方法: " + method + "；" + reason,
			}
		}
	}
	return errNotFound(method)
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

// decodeStrictParams 是新增方法的严格参数解码：拒绝未知字段与尾随内容。
// 目的不是「更严格」，而是堵住「把任意命令/脚本塞进 params 期望被转发」的路径——
// 未知字段必须显式失败，且错误信息说明原因是本插件不接受任意命令参数。
func decodeStrictParams(raw json.RawMessage, target any) *pluginapi.Error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return invalid("缺少必填参数对象")
	}
	if trimmed[0] != '{' {
		return invalid("params 必须是 JSON 对象")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return invalid("params 校验失败: " + err.Error() +
			"；本插件只接受方法契约声明的参数，不接受任意命令/脚本参数")
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return invalid("params 之后存在多余内容；本插件只接受单个 JSON 对象")
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
