package redisop

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// fakeExecutor 返回固定结果，用于方法面成功路径与参数校验测试。
type fakeExecutor struct {
	instances   []InstanceStatus
	info        InfoResult
	keys        KeysResult
	get         GetResult
	deleted     DeleteResult
	persistence PersistenceResult
	memory      MemoryResult
	slowlog     SlowlogResult
	keyInfo     KeyInfoResult
	err         error
}

func (f fakeExecutor) Instances([]InstanceConfig) ([]InstanceStatus, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.instances, nil
}

func (f fakeExecutor) Info(InstanceConfig) (InfoResult, error) {
	if f.err != nil {
		return InfoResult{}, f.err
	}
	return f.info, nil
}

func (f fakeExecutor) Keys(InstanceConfig, string, int) (KeysResult, error) {
	if f.err != nil {
		return KeysResult{}, f.err
	}
	return f.keys, nil
}

func (f fakeExecutor) Get(InstanceConfig, string) (GetResult, error) {
	if f.err != nil {
		return GetResult{}, f.err
	}
	return f.get, nil
}

func (f fakeExecutor) Delete(InstanceConfig, []string) (DeleteResult, error) {
	if f.err != nil {
		return DeleteResult{}, f.err
	}
	return f.deleted, nil
}

func (f fakeExecutor) Persistence(InstanceConfig) (PersistenceResult, error) {
	if f.err != nil {
		return PersistenceResult{}, f.err
	}
	return f.persistence, nil
}

func (f fakeExecutor) Memory(InstanceConfig) (MemoryResult, error) {
	if f.err != nil {
		return MemoryResult{}, f.err
	}
	return f.memory, nil
}

func (f fakeExecutor) Slowlog(InstanceConfig, int) (SlowlogResult, error) {
	if f.err != nil {
		return SlowlogResult{}, f.err
	}
	return f.slowlog, nil
}

func (f fakeExecutor) KeyInfo(InstanceConfig, string) (KeyInfoResult, error) {
	if f.err != nil {
		return KeyInfoResult{}, f.err
	}
	return f.keyInfo, nil
}

// testInstances 是单实例配置。
func testInstances() []InstanceConfig {
	return []InstanceConfig{{Name: "default", Host: "127.0.0.1", Port: 6379}}
}

// TestBusinessVerb 方法名必须带 redis. 域前缀；危险命令不在白名单内。
func TestBusinessVerb(t *testing.T) {
	for _, verb := range []string{"instances", "info", "keys", "get", "delete"} {
		if got, ok := businessVerb("redis." + verb); !ok || got != verb {
			t.Fatalf("businessVerb(redis.%s)=(%q,%v)", verb, got, ok)
		}
	}
	for _, method := range []string{
		"instances", "scan",
		"redis.flushall", "redis.flushdb", "redis.config", "redis.config.set", "redis.shutdown",
	} {
		if _, ok := businessVerb(method); ok {
			t.Fatalf("%q 不应被方法面接受", method)
		}
	}
}

// TestCallSuccess 五个方法成功路径返回结构化结果。
func TestCallSuccess(t *testing.T) {
	exec := fakeExecutor{
		instances: []InstanceStatus{{Name: "default", Reachable: true, Version: "7.2.4"}},
		info:      InfoResult{Instance: "default", Server: InfoServer{Version: "7.2.4"}},
		keys:      KeysResult{Keys: []KeyEntry{{Key: "foo", Type: "string", TTLS: -1}}, Scanned: 1},
		get:       GetResult{Key: "foo", Type: "string", TTLS: -1, Value: json.RawMessage(`"bar"`)},
		deleted:   DeleteResult{Deleted: 1, Keys: []string{"foo"}},
	}
	cases := []struct {
		method string
		params string
		want   string
	}{
		{"redis.instances", `{}`, `"reachable":true`},
		{"redis.info", `{"instance":"default"}`, `"version":"7.2.4"`},
		{"redis.keys", `{"pattern":"foo*","limit":10}`, `"scanned":1`},
		{"redis.get", `{"key":"foo"}`, `"value":"bar"`},
		{"redis.delete", `{"keys":["foo"]}`, `"deleted":1`},
	}
	for _, c := range cases {
		result, perr := Call(c.method, json.RawMessage(c.params), testInstances(), exec)
		if perr != nil {
			t.Fatalf("%s 意外错误: %v", c.method, perr)
		}
		if !strings.Contains(string(result), c.want) {
			t.Fatalf("%s 结果=%s，未含 %q", c.method, result, c.want)
		}
	}
}

// TestCallParamValidation 非法参数形状、上限与实例选择均返回明确错误码。
func TestCallParamValidation(t *testing.T) {
	cases := []struct {
		method string
		params string
		code   string
	}{
		{"redis.info", `[]`, pluginapi.E_INVALID},
		{"redis.info", `not-json`, pluginapi.E_INVALID},
		{"redis.info", `{}`, pluginapi.E_INVALID}, // 多实例时可省略，单实例时应成功；这里用多实例
		{"redis.keys", `{"pattern":"` + strings.Repeat("a", 257) + `"}`, pluginapi.E_INVALID},
		{"redis.keys", `{"limit":10001}`, pluginapi.E_INVALID},
		{"redis.get", `{"key":""}`, pluginapi.E_INVALID},
		{"redis.get", `{"key":"` + strings.Repeat("k", 1025) + `"}`, pluginapi.E_INVALID},
		{"redis.delete", `{"keys":[]}`, pluginapi.E_INVALID},
		{"redis.delete", `{"keys":["a","a"]}`, pluginapi.E_INVALID},
		{"redis.delete", `{"keys":["` + strings.Repeat("k", 1025) + `"]}`, pluginapi.E_INVALID},
		{"redis.delete", `{"keys":` + manyKeys(101) + `}`, pluginapi.E_INVALID},
		{"redis.nope", `{}`, pluginapi.E_NOTFOUND},
	}
	multiple := []InstanceConfig{
		{Name: "default", Host: "127.0.0.1", Port: 6379},
		{Name: "cache", Host: "127.0.0.1", Port: 6380},
	}
	for _, c := range cases {
		instances := testInstances()
		if c.method == "redis.info" && c.params == `{}` {
			instances = multiple
		}
		_, perr := Call(c.method, json.RawMessage(c.params), instances, fakeExecutor{})
		if perr == nil || perr.Code != c.code {
			t.Fatalf("Call(%s,%s) 错误=%v，期望 %s", c.method, c.params, perr, c.code)
		}
	}
}

// TestCallNoInstances 未配置实例时业务方法返回 E_NOTFOUND，instances 返回空列表。
func TestCallNoInstances(t *testing.T) {
	result, perr := Call("redis.instances", json.RawMessage(`{}`), nil, fakeExecutor{})
	if perr != nil || !strings.Contains(string(result), `"count":0`) {
		t.Fatalf("空实例 instances 结果=%s err=%v", result, perr)
	}
	_, perr = Call("redis.info", json.RawMessage(`{}`), nil, fakeExecutor{})
	if perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("空实例 info 错误=%v，期望 E_NOTFOUND", perr)
	}
}

// TestCallDangerousCommandsAbsent FLUSHALL/FLUSHDB/CONFIG/SHUTDOWN 不在方法面。
func TestCallDangerousCommandsAbsent(t *testing.T) {
	for _, method := range []string{
		"redis.flushall", "redis.flushdb", "redis.config.set", "redis.config.get", "redis.shutdown",
	} {
		_, perr := Call(method, json.RawMessage(`{}`), testInstances(), fakeExecutor{})
		if perr == nil || perr.Code != pluginapi.E_NOTFOUND {
			t.Fatalf("%s 错误=%v，期望 E_NOTFOUND（危险命令不在方法面）", method, perr)
		}
	}
}

// TestFindInstance 单实例可省略 instance，多实例必须指定，未知实例报 E_NOTFOUND。
func TestFindInstance(t *testing.T) {
	if _, perr := findInstance(testInstances(), ""); perr != nil {
		t.Fatalf("单实例省略 instance 应成功: %v", perr)
	}
	multiple := []InstanceConfig{
		{Name: "default", Host: "127.0.0.1", Port: 6379},
		{Name: "cache", Host: "127.0.0.1", Port: 6380},
	}
	if _, perr := findInstance(multiple, ""); perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("多实例省略 instance 错误=%v，期望 E_INVALID", perr)
	}
	if _, perr := findInstance(multiple, "cache"); perr != nil {
		t.Fatalf("指定 cache 应成功: %v", perr)
	}
	if _, perr := findInstance(multiple, "missing"); perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("未知实例错误=%v，期望 E_NOTFOUND", perr)
	}
	if _, perr := findInstance(multiple, "Bad Name"); perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("非法实例名错误=%v，期望 E_INVALID", perr)
	}
}

// manyKeys 生成 n 个键的 JSON 数组文本。
func manyKeys(n int) string {
	keys := make([]string, 0, n)
	for index := 0; index < n; index++ {
		keys = append(keys, `"k`+strconv.Itoa(index)+`"`)
	}
	return "[" + strings.Join(keys, ",") + "]"
}
