package redisop

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callWithServer 通过公开方法面（Call + 真实 RESP 客户端）对假服务端执行一次调用。
func callWithServer(t *testing.T, server *fakeRedis, method, params string) (json.RawMessage, *pluginapi.Error) {
	t.Helper()
	return Call(method, json.RawMessage(params), []InstanceConfig{server.Instance()}, nil)
}

// TestClientSetString string 写入用 SET ... EX（TTL 原子生效），结果含类型/计数/TTL。
func TestClientSetString(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		switch command[0] {
		case "TYPE":
			return "none", nil
		case "SET":
			return "OK", nil
		case "TTL":
			return int64(120), nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	raw, perr := callWithServer(t, server, "redis.set",
		`{"key":"kore:s","type":"string","value":"bar","ttl_s":120}`)
	if perr != nil {
		t.Fatalf("set 失败: %v", perr)
	}
	for _, want := range []string{`"key":"kore:s"`, `"type":"string"`, `"written":1`, `"ttl_s":120`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("结果 %s 未含 %s", raw, want)
		}
	}
	commands := server.CommandList()
	setArgs := findCommand(commands, "SET")
	if setArgs == nil || len(setArgs) != 5 || setArgs[3] != "EX" || setArgs[4] != "120" {
		t.Fatalf("SET 命令未带原子 EX 过期: %v", commands)
	}
}

// TestClientSetExistingTypeMismatch 请求类型与既有类型不一致时拒绝，避免 WRONGTYPE。
func TestClientSetExistingTypeMismatch(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		if command[0] == "TYPE" {
			return "hash", nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	_, perr := callWithServer(t, server, "redis.set", `{"key":"kore:h","type":"string","value":"x"}`)
	if perr == nil || perr.Code != pluginapi.E_INVALID || !strings.Contains(perr.Message, "不一致") {
		t.Fatalf("类型冲突错误=%v，期望 E_INVALID 且说明不一致", perr)
	}
	if findCommand(server.CommandList(), "SET") != nil {
		t.Fatal("类型冲突时不应发送 SET")
	}
}

// TestClientSetMissingWithoutType 键不存在且未指定类型时要求显式类型。
func TestClientSetMissingWithoutType(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		if command[0] == "TYPE" {
			return "none", nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	_, perr := callWithServer(t, server, "redis.set", `{"key":"kore:new","value":"x"}`)
	if perr == nil || perr.Code != pluginapi.E_INVALID || !strings.Contains(perr.Message, "未指定 type") {
		t.Fatalf("缺类型错误=%v，期望 E_INVALID 且提示指定 type", perr)
	}
}

// TestClientSetHash hash 写入用 HSET，计数取命令返回的新增字段数。
func TestClientSetHash(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		switch command[0] {
		case "TYPE":
			return "none", nil
		case "HSET":
			return int64(2), nil
		case "TTL":
			return int64(-1), nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	raw, perr := callWithServer(t, server, "redis.set",
		`{"key":"kore:h","type":"hash","value":[{"field":"a","value":"1"},{"field":"b","value":"2"}]}`)
	if perr != nil {
		t.Fatalf("hash set 失败: %v", perr)
	}
	if !strings.Contains(string(raw), `"written":2`) || !strings.Contains(string(raw), `"ttl_s":-1`) {
		t.Fatalf("hash 结果异常: %s", raw)
	}
	hset := findCommand(server.CommandList(), "HSET")
	if len(hset) != 6 || hset[2] != "a" || hset[3] != "1" || hset[4] != "b" || hset[5] != "2" {
		t.Fatalf("HSET 参数异常: %v", hset)
	}
}

// TestClientSetListWithTTL 集合类型写入后追加 EXPIRE，TTL 取执行后的实际值。
func TestClientSetListWithTTL(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		switch command[0] {
		case "TYPE":
			return "none", nil
		case "RPUSH":
			return int64(3), nil
		case "EXPIRE":
			return int64(1), nil
		case "TTL":
			return int64(60), nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	raw, perr := callWithServer(t, server, "redis.set",
		`{"key":"kore:l","type":"list","value":["a","b","c"],"ttl_s":60}`)
	if perr != nil {
		t.Fatalf("list set 失败: %v", perr)
	}
	if !strings.Contains(string(raw), `"written":3`) || !strings.Contains(string(raw), `"ttl_s":60`) {
		t.Fatalf("list 结果异常: %s", raw)
	}
	expire := findCommand(server.CommandList(), "EXPIRE")
	if len(expire) != 3 || expire[1] != "kore:l" || expire[2] != "60" {
		t.Fatalf("EXPIRE 参数异常: %v", expire)
	}
}

// TestClientSetZSet zset 写入用 ZADD，score 接受数字与字符串两种形态。
func TestClientSetZSet(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		switch command[0] {
		case "TYPE":
			return "none", nil
		case "ZADD":
			return int64(2), nil
		case "TTL":
			return int64(-1), nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	_, perr := callWithServer(t, server, "redis.set",
		`{"key":"kore:z","type":"zset","value":[{"member":"a","score":1.5},{"member":"b","score":"2"}]}`)
	if perr != nil {
		t.Fatalf("zset set 失败: %v", perr)
	}
	zadd := findCommand(server.CommandList(), "ZADD")
	if len(zadd) != 6 || zadd[2] != "1.5" || zadd[3] != "a" || zadd[4] != "2" || zadd[5] != "b" {
		t.Fatalf("ZADD 参数异常: %v", zadd)
	}
}

// TestSetParamValidation 写入参数上限与 TTL 边界：负数/0/超限一律 E_INVALID。
func TestSetParamValidation(t *testing.T) {
	cases := []struct {
		name   string
		params string
		want   string
	}{
		{"negative_ttl", `{"key":"k","type":"string","value":"v","ttl_s":-1}`, "redis.persist"},
		{"zero_ttl", `{"key":"k","type":"string","value":"v","ttl_s":0}`, "省略该字段"},
		{"huge_ttl", `{"key":"k","type":"string","value":"v","ttl_s":315360001}`, "上限"},
		{"bad_type", `{"key":"k","type":"bogus","value":"v"}`, "只允许"},
		{"no_value", `{"key":"k","type":"string"}`, "缺少 value"},
		{"too_many_items", `{"key":"k","type":"list","value":[` + repeatItem(1001) + `]}`, "上限"},
		{"empty_items", `{"key":"k","type":"list","value":[]}`, "不得为空"},
		{"bad_hash_shape", `{"key":"k","type":"hash","value":["a"]}`, "必须是"},
		{"bad_zset_score", `{"key":"k","type":"zset","value":[{"member":"a","score":"abc"}]}`, "score"},
		{"nul_field", "{\"key\":\"k\",\"type\":\"hash\",\"value\":[{\"field\":\"a\\u0000b\",\"value\":\"1\"}]}", "NUL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// exec=nil：参数校验必须先于建连，否则会得到 E_UPSTREAM。
			_, perr := Call("redis.set", json.RawMessage(c.params), testInstances(), nil)
			if perr == nil || perr.Code != pluginapi.E_INVALID {
				t.Fatalf("set(%s) 错误=%v，期望 E_INVALID", c.params, perr)
			}
			if !strings.Contains(perr.Message, c.want) {
				t.Fatalf("set(%s) 错误信息 %q 未含 %q", c.params, perr.Message, c.want)
			}
		})
	}
}

// TestClientSetValueTooLarge 单值超过 1 MiB 上限时拒绝，不发送写入命令。
func TestClientSetValueTooLarge(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		if command[0] == "TYPE" {
			return "none", nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	value := strings.Repeat("x", maxWriteValueBytes+1)
	_, perr := callWithServer(t, server, "redis.set",
		`{"key":"kore:big","type":"string","value":"`+value+`"}`)
	if perr == nil || perr.Code != pluginapi.E_INVALID || !strings.Contains(perr.Message, "上限") {
		t.Fatalf("超大 value 错误=%v，期望 E_INVALID 且提示上限", perr)
	}
	if findCommand(server.CommandList(), "SET") != nil {
		t.Fatal("超大 value 不应发送 SET")
	}
}

// TestTTLBoundaries TTL 三态：-1 永久、0 即将过期、>0 剩余秒；缺键与并发删除返回 E_NOTFOUND。
func TestTTLBoundaries(t *testing.T) {
	cases := []struct {
		name       string
		keyType    string
		ttl        any
		wantTTL    string
		wantPersis string
	}{
		{"permanent", "string", int64(-1), `"ttl_s":-1`, `"persistent":true`},
		{"expiring_now", "string", int64(0), `"ttl_s":0`, `"persistent":false`},
		{"countdown", "string", int64(300), `"ttl_s":300`, `"persistent":false`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := startFakeRedis(t, func(command []string) (any, error) {
				switch command[0] {
				case "TYPE":
					return c.keyType, nil
				case "TTL":
					return c.ttl, nil
				}
				return nil, fmt.Errorf("ERR unexpected %v", command)
			})
			raw, perr := callWithServer(t, server, "redis.ttl", `{"key":"kore:k"}`)
			if perr != nil {
				t.Fatalf("ttl 失败: %v", perr)
			}
			if !strings.Contains(string(raw), c.wantTTL) || !strings.Contains(string(raw), c.wantPersis) {
				t.Fatalf("ttl 结果=%s，期望 %s / %s", raw, c.wantTTL, c.wantPersis)
			}
			if c.name == "countdown" {
				var result TTLResult
				if err := json.Unmarshal(raw, &result); err != nil {
					t.Fatalf("解析 TTL 结果失败: %v", err)
				}
				want := time.Now().Unix() + 300
				if result.ExpiresAt < want-5 || result.ExpiresAt > want+5 {
					t.Fatalf("expires_at=%d，期望约 %d", result.ExpiresAt, want)
				}
			}
		})
	}

	// 键不存在。
	missing := startFakeRedis(t, func(command []string) (any, error) {
		if command[0] == "TYPE" {
			return "none", nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	if _, perr := callWithServer(t, missing, "redis.ttl", `{"key":"nope"}`); perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("缺键 ttl 错误=%v，期望 E_NOTFOUND", perr)
	}

	// TYPE 与 TTL 之间键被删除（TTL=-2）按不存在处理，不编造 TTL。
	race := startFakeRedis(t, func(command []string) (any, error) {
		switch command[0] {
		case "TYPE":
			return "string", nil
		case "TTL":
			return int64(-2), nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	if _, perr := callWithServer(t, race, "redis.ttl", `{"key":"gone"}`); perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("并发删除 ttl 错误=%v，期望 E_NOTFOUND", perr)
	}
}

// TestExpireBoundaries expire 只接受正整数；键不存在（EXPIRE=0）映射 E_NOTFOUND。
func TestExpireBoundaries(t *testing.T) {
	for _, params := range []string{
		`{"key":"k"}`,
		`{"key":"k","ttl_s":0}`,
		`{"key":"k","ttl_s":-5}`,
		`{"key":"k","ttl_s":315360001}`,
	} {
		_, perr := Call("redis.expire", json.RawMessage(params), testInstances(), nil)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("expire(%s) 错误=%v，期望 E_INVALID", params, perr)
		}
		if !strings.Contains(perr.Message, "ttl_s") {
			t.Fatalf("expire(%s) 错误信息 %q 未指明 ttl_s", params, perr.Message)
		}
	}

	ok := startFakeRedis(t, func(command []string) (any, error) {
		switch command[0] {
		case "EXPIRE":
			return int64(1), nil
		case "TTL":
			return int64(60), nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	raw, perr := callWithServer(t, ok, "redis.expire", `{"key":"kore:k","ttl_s":60}`)
	if perr != nil || !strings.Contains(string(raw), `"ttl_s":60`) {
		t.Fatalf("expire 结果=%s err=%v", raw, perr)
	}

	missing := startFakeRedis(t, func(command []string) (any, error) {
		if command[0] == "EXPIRE" {
			return int64(0), nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	if _, perr := callWithServer(t, missing, "redis.expire", `{"key":"nope","ttl_s":60}`); perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("缺键 expire 错误=%v，期望 E_NOTFOUND", perr)
	}
}

// TestPersistPersist 去掉 TTL；键不存在 E_NOTFOUND；本来无 TTL 时 Persisted=false。
func TestPersistPersist(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		switch command[0] {
		case "PERSIST":
			return int64(1), nil
		case "TYPE":
			return "string", nil
		case "TTL":
			return int64(-1), nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	raw, perr := callWithServer(t, server, "redis.persist", `{"key":"kore:k"}`)
	if perr != nil || !strings.Contains(string(raw), `"persisted":true`) || !strings.Contains(string(raw), `"ttl_s":-1`) {
		t.Fatalf("persist 结果=%s err=%v", raw, perr)
	}

	noTTL := startFakeRedis(t, func(command []string) (any, error) {
		switch command[0] {
		case "PERSIST":
			return int64(0), nil
		case "TYPE":
			return "string", nil
		case "TTL":
			return int64(-1), nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	raw, perr = callWithServer(t, noTTL, "redis.persist", `{"key":"kore:k"}`)
	if perr != nil || !strings.Contains(string(raw), `"persisted":false`) {
		t.Fatalf("无 TTL persist 结果=%s err=%v", raw, perr)
	}

	missing := startFakeRedis(t, func(command []string) (any, error) {
		switch command[0] {
		case "PERSIST":
			return int64(0), nil
		case "TYPE":
			return "none", nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	if _, perr := callWithServer(t, missing, "redis.persist", `{"key":"nope"}`); perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("缺键 persist 错误=%v，期望 E_NOTFOUND", perr)
	}
}

// TestClientBgsave 后台落盘成功透传状态行；已有落盘任务时给出可操作提示。
func TestClientBgsave(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		if command[0] == "BGSAVE" {
			return "Background saving started", nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	raw, perr := callWithServer(t, server, "redis.bgsave", `{}`)
	if perr != nil || !strings.Contains(string(raw), `"started":true`) ||
		!strings.Contains(string(raw), "Background saving started") {
		t.Fatalf("bgsave 结果=%s err=%v", raw, perr)
	}

	busy := startFakeRedis(t, func(command []string) (any, error) {
		return nil, errors.New("ERR Background save already in progress")
	})
	_, perr = callWithServer(t, busy, "redis.bgsave", `{}`)
	if perr == nil || perr.Code != pluginapi.E_UPSTREAM || !strings.Contains(perr.Message, "稍后") {
		t.Fatalf("并发落盘错误=%v，期望 E_UPSTREAM 且提示稍后重试", perr)
	}
}

// TestClientServerConfigGet 读取三个白名单参数（pipeline CONFIG GET）。
func TestClientServerConfigGet(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		if command[0] == "CONFIG" && command[1] == "GET" {
			return []any{command[2], map[string]string{
				"maxmemory": "268435456", "maxmemory-policy": "noeviction", "timeout": "0",
			}[command[2]]}, nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	raw, perr := callWithServer(t, server, "redis.serverConfig.get", `{}`)
	if perr != nil {
		t.Fatalf("serverConfig.get 失败: %v", perr)
	}
	for _, want := range []string{`"maxmemory":"268435456"`, `"maxmemory_policy":"noeviction"`, `"timeout":"0"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("结果 %s 未含 %s", raw, want)
		}
	}
}

// TestClientServerConfigSet 只下发白名单参数，回读确认生效并列出 changed。
func TestClientServerConfigSet(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		switch {
		case command[0] == "CONFIG" && command[1] == "SET":
			if command[2] != "maxmemory" || command[3] != "256mb" {
				return nil, fmt.Errorf("ERR unexpected %v", command)
			}
			return "OK", nil
		case command[0] == "CONFIG" && command[1] == "GET":
			value := map[string]string{
				"maxmemory": "268435456", "maxmemory-policy": "noeviction", "timeout": "0",
			}[command[2]]
			return []any{command[2], value}, nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	raw, perr := callWithServer(t, server, "redis.serverConfig.set", `{"maxmemory":"256mb"}`)
	if perr != nil {
		t.Fatalf("serverConfig.set 失败: %v", perr)
	}
	for _, want := range []string{`"changed":["maxmemory"]`, `"maxmemory":"268435456"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("结果 %s 未含 %s", raw, want)
		}
	}
}

// TestClientServerConfigDisabled 云托管实例禁用 CONFIG：返回 E_UNSUPPORTED 并说明替代做法。
func TestClientServerConfigDisabled(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		return nil, errors.New("ERR unknown command 'CONFIG'")
	})
	_, perr := callWithServer(t, server, "redis.serverConfig.get", `{}`)
	if perr == nil || perr.Code != pluginapi.E_UNSUPPORTED || !strings.Contains(perr.Message, "禁用") {
		t.Fatalf("禁用 CONFIG 错误=%v，期望 E_UNSUPPORTED 且说明禁用", perr)
	}
}

// TestClientSelectDBAndTimeout db>0 时连接后先 SELECT；timeout_s 覆盖默认超时。
func TestClientSelectDBAndTimeout(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		switch command[0] {
		case "SELECT":
			if command[1] != "3" {
				return nil, fmt.Errorf("ERR 期望 SELECT 3，实际 %v", command)
			}
			return "OK", nil
		case "TYPE":
			return "string", nil
		case "TTL":
			return int64(-1), nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	instance := server.Instance()
	instance.DB = 3
	instance.TimeoutS = 7
	if _, perr := Call("redis.ttl", json.RawMessage(`{"key":"k"}`), []InstanceConfig{instance}, nil); perr != nil {
		t.Fatalf("带 DB 的 ttl 失败: %v", perr)
	}
	commands := server.CommandList()
	if len(commands) == 0 || commands[0][0] != "SELECT" || commands[0][1] != "3" {
		t.Fatalf("首条命令应为 SELECT 3: %v", commands)
	}

	client := NewClient()
	connect, command := client.timeouts(InstanceConfig{TimeoutS: 7})
	if connect != 7*time.Second || command != 7*time.Second {
		t.Fatalf("timeout_s 未覆盖默认超时: %s/%s", connect, command)
	}
	connect, command = client.timeouts(InstanceConfig{})
	if connect != connectTimeout || command != commandTimeout {
		t.Fatalf("缺省应回退客户端默认超时: %s/%s", connect, command)
	}
}

// findCommand 返回命令列表中指定命令名的第一条；不存在返回 nil。
func findCommand(commands [][]string, name string) []string {
	for _, command := range commands {
		if len(command) > 0 && strings.EqualFold(command[0], name) {
			return command
		}
	}
	return nil
}

// repeatItem 生成 n 个元素的 JSON 数组片段（用于上限测试）。
func repeatItem(n int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = `"x"`
	}
	return strings.Join(items, ",")
}
