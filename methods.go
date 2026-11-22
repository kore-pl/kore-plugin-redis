package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"runtime"
	"time"

	"github.com/pengtang1024/kore-plugin-redis/internal/plugincfg"
	"github.com/pengtang1024/kore-plugin-redis/internal/redisop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// statusResult 是 kore.ping 的结果（PLUGIN-PROTOCOL §7.1）。
type statusResult struct {
	Status     string `json:"status"`
	UptimeS    int64  `json:"uptime_s"`
	Goroutines int    `json:"goroutines"`
	RSSKB      int64  `json:"rss_kb"`
}

// dispatch 把请求分发为响应帧；第二个返回值表示收到 kore.shutdown。
func dispatch(request *pluginapi.Request, cfg *config, logger *slog.Logger) (*pluginapi.Response, bool) {
	response := &pluginapi.Response{Type: pluginapi.TypeResponse, ID: request.ID, ReqID: request.ReqID}
	switch request.Method {
	case "kore.shutdown":
		response.Result = json.RawMessage(`{"ok":true}`)
		logger.Info("收到 kore.shutdown，优雅退出")
		return response, true
	case "kore.ping":
		response.Result = pingResult(*cfg)
		return response, false
	case "kore.reload":
		response.Result = json.RawMessage(`{"ok":true,"config_digest":""}`)
		return response, false
	case cfg.id + ".config.get", "config.get":
		result, perr := handleConfigGet(cfg)
		if perr != nil {
			response.Error = perr
			return response, false
		}
		response.Result = result
		return response, false
	case cfg.id + ".config.set", "config.set":
		result, perr := handleConfigSet(request.Params, cfg)
		if perr != nil {
			response.Error = perr
			return response, false
		}
		response.Result = result
		return response, false
	}
	result, perr := redisop.Call(request.Method, request.Params, cfg.instances, nil)
	if perr != nil {
		response.Error = perr
		response.Result = nil
		return response, false
	}
	response.Result = result
	return response, false
}

// handleConfigGet 返回表单 schema、当前值与来源；敏感字段经 plugincfg 掩码后不回传原值。
func handleConfigGet(cfg *config) (json.RawMessage, *pluginapi.Error) {
	schema := redisSchema()
	values, err := json.Marshal(cfg.values)
	if err != nil {
		return nil, internalErr("序列化配置值失败: " + err.Error())
	}
	masked, err := plugincfg.MaskValues(schema, values)
	if err != nil {
		return nil, internalErr("掩码配置值失败: " + err.Error())
	}
	return encodeResult(plugincfg.GetResult{Schema: schema, Values: masked, Source: cfg.source}, "redis.config.get")
}

// handleConfigSet 执行「校验 → 原子写 → 更新内存」：写盘成功后才替换运行期实例表，
// 下一次 redis.* 调用立即使用新地址（无需重启，连接本身是每次调用建立的短连接）。
func handleConfigSet(params json.RawMessage, cfg *config) (json.RawMessage, *pluginapi.Error) {
	var p plugincfg.SetParams
	if perr := decodeParams(params, &p); perr != nil {
		return nil, perr
	}
	if len(p.Values) == 0 || string(p.Values) == "null" || p.Values[0] != '{' {
		return nil, invalid("values 必须是 JSON 对象")
	}
	merged := cfg.values
	if err := strictDecode(p.Values, &merged); err != nil {
		return nil, invalid("配置值非法: " + err.Error())
	}
	if merged.Password == plugincfg.SensitivePlaceholder {
		merged.Password = cfg.values.Password
	}
	normalized, err := validateValues(merged)
	if err != nil {
		return nil, invalid("Redis 设置非法: " + err.Error())
	}
	if cfg.store == nil || cfg.store.Path() == "" {
		return nil, internalErr("KORE_STATE_DIR 未设置，无法保存插件配置")
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return nil, internalErr("序列化配置值失败: " + err.Error())
	}
	if err := cfg.store.Write(raw); err != nil {
		return nil, internalErr("保存插件配置失败: " + err.Error())
	}
	cfg.values = normalized
	cfg.instances = buildInstances(normalized)
	cfg.source = plugincfg.SourceFile
	return encodeResult(plugincfg.SetResult{
		Saved: true, Applied: true, RestartRequired: false, Warnings: []string{},
	}, "redis.config.set")
}

// pingResult 组装 kore.ping 结果。
func pingResult(cfg config) json.RawMessage {
	data, _ := json.Marshal(statusResult{
		Status:     "ready",
		UptimeS:    int64(time.Since(cfg.startedAt).Seconds()),
		Goroutines: runtime.NumGoroutine(),
		RSSKB:      rssKB(),
	})
	return data
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

func invalid(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_INVALID, Message: msg}
}

func internalErr(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_INTERNAL, Message: msg}
}

// encodeResult 序列化方法结果为响应载荷。
func encodeResult(v any, method string) (json.RawMessage, *pluginapi.Error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, internalErr("序列化 " + method + " 结果失败: " + err.Error())
	}
	return data, nil
}
