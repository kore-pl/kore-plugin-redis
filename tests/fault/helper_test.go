// Package fault 对 redis 插件做四类故障注入，验证方法级错误返回。
// 每个用例向 redisop.Call 的执行面注入一种故障，断言业务方法返回结构化错误码。
package fault

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengtang1024/kore-plugin-redis/internal/redisop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// testInstances 是故障注入使用的单实例配置（不发起真实连接）。
var testInstances = []redisop.InstanceConfig{{Name: "default", Host: "127.0.0.1", Port: 6379}}

// faultExecutor 按注入类别让每个方法都失败，模拟四类运行时故障。
type faultExecutor struct {
	kind redisop.FaultKind
}

func (f faultExecutor) fail(action string) error {
	return redisop.NewFaultError(f.kind, action+" 执行面故障")
}

func (f faultExecutor) Instances([]redisop.InstanceConfig) ([]redisop.InstanceStatus, error) {
	return nil, f.fail("instances")
}

func (f faultExecutor) Info(redisop.InstanceConfig) (redisop.InfoResult, error) {
	return redisop.InfoResult{}, f.fail("info")
}

func (f faultExecutor) Keys(redisop.InstanceConfig, string, int) (redisop.KeysResult, error) {
	return redisop.KeysResult{}, f.fail("keys")
}

func (f faultExecutor) Get(redisop.InstanceConfig, string) (redisop.GetResult, error) {
	return redisop.GetResult{}, f.fail("get")
}

func (f faultExecutor) Delete(redisop.InstanceConfig, []string) (redisop.DeleteResult, error) {
	return redisop.DeleteResult{}, f.fail("delete")
}

func (f faultExecutor) Persistence(redisop.InstanceConfig) (redisop.PersistenceResult, error) {
	return redisop.PersistenceResult{}, f.fail("persistence")
}

func (f faultExecutor) Memory(redisop.InstanceConfig) (redisop.MemoryResult, error) {
	return redisop.MemoryResult{}, f.fail("memory")
}

func (f faultExecutor) Slowlog(redisop.InstanceConfig, int) (redisop.SlowlogResult, error) {
	return redisop.SlowlogResult{}, f.fail("slowlog")
}

func (f faultExecutor) KeyInfo(redisop.InstanceConfig, string) (redisop.KeyInfoResult, error) {
	return redisop.KeyInfoResult{}, f.fail("keyInfo")
}

func (f faultExecutor) Set(redisop.InstanceConfig, redisop.SetRequest) (redisop.SetResult, error) {
	return redisop.SetResult{}, f.fail("set")
}

func (f faultExecutor) TTL(redisop.InstanceConfig, string) (redisop.TTLResult, error) {
	return redisop.TTLResult{}, f.fail("ttl")
}

func (f faultExecutor) Expire(redisop.InstanceConfig, string, int64) (redisop.ExpireResult, error) {
	return redisop.ExpireResult{}, f.fail("expire")
}

func (f faultExecutor) Persist(redisop.InstanceConfig, string) (redisop.PersistResult, error) {
	return redisop.PersistResult{}, f.fail("persist")
}

func (f faultExecutor) Bgsave(redisop.InstanceConfig) (redisop.BgsaveResult, error) {
	return redisop.BgsaveResult{}, f.fail("bgsave")
}

func (f faultExecutor) ServerConfigGet(redisop.InstanceConfig) (redisop.ServerConfigResult, error) {
	return redisop.ServerConfigResult{}, f.fail("serverConfig.get")
}

func (f faultExecutor) ServerConfigSet(redisop.InstanceConfig, redisop.ServerConfigPatch) (redisop.ServerConfigSetResult, error) {
	return redisop.ServerConfigSetResult{}, f.fail("serverConfig.set")
}

// healthyExecutor 是 sanity 反证用的正常执行面：返回空结果、不连网络。
type healthyExecutor struct{}

func (healthyExecutor) Instances([]redisop.InstanceConfig) ([]redisop.InstanceStatus, error) {
	return []redisop.InstanceStatus{}, nil
}

func (healthyExecutor) Info(redisop.InstanceConfig) (redisop.InfoResult, error) {
	return redisop.InfoResult{Instance: "default", Server: redisop.InfoServer{Version: "7.2.4"}}, nil
}

func (healthyExecutor) Keys(redisop.InstanceConfig, string, int) (redisop.KeysResult, error) {
	return redisop.KeysResult{Keys: []redisop.KeyEntry{}}, nil
}

func (healthyExecutor) Get(redisop.InstanceConfig, string) (redisop.GetResult, error) {
	return redisop.GetResult{Key: "foo", Type: "string", Value: json.RawMessage(`""`)}, nil
}

func (healthyExecutor) Delete(redisop.InstanceConfig, []string) (redisop.DeleteResult, error) {
	return redisop.DeleteResult{Deleted: 1, Keys: []string{"foo"}}, nil
}

func (healthyExecutor) Persistence(redisop.InstanceConfig) (redisop.PersistenceResult, error) {
	return redisop.PersistenceResult{Instance: "default", AOFLastWriteStatus: "ok"}, nil
}

func (healthyExecutor) Memory(redisop.InstanceConfig) (redisop.MemoryResult, error) {
	return redisop.MemoryResult{Instance: "default", Used: 1024}, nil
}

func (healthyExecutor) Slowlog(redisop.InstanceConfig, int) (redisop.SlowlogResult, error) {
	return redisop.SlowlogResult{Entries: []redisop.SlowlogEntry{}}, nil
}

func (healthyExecutor) KeyInfo(redisop.InstanceConfig, string) (redisop.KeyInfoResult, error) {
	return redisop.KeyInfoResult{Key: "foo", Type: "string", TTLS: -1}, nil
}

func (healthyExecutor) Set(redisop.InstanceConfig, redisop.SetRequest) (redisop.SetResult, error) {
	return redisop.SetResult{Key: "foo", Type: "string", Written: 1, TTLs: -1}, nil
}

func (healthyExecutor) TTL(redisop.InstanceConfig, string) (redisop.TTLResult, error) {
	return redisop.TTLResult{Key: "foo", Type: "string", TTLs: -1, Persistent: true}, nil
}

func (healthyExecutor) Expire(redisop.InstanceConfig, string, int64) (redisop.ExpireResult, error) {
	return redisop.ExpireResult{Key: "foo", TTLs: 60}, nil
}

func (healthyExecutor) Persist(redisop.InstanceConfig, string) (redisop.PersistResult, error) {
	return redisop.PersistResult{Key: "foo", Persisted: true, TTLs: -1}, nil
}

func (healthyExecutor) Bgsave(redisop.InstanceConfig) (redisop.BgsaveResult, error) {
	return redisop.BgsaveResult{Started: true, Message: "Background saving started"}, nil
}

func (healthyExecutor) ServerConfigGet(redisop.InstanceConfig) (redisop.ServerConfigResult, error) {
	return redisop.ServerConfigResult{Instance: "default", Config: redisop.ServerConfig{MaxMemory: "0", Timeout: "0"}}, nil
}

func (healthyExecutor) ServerConfigSet(redisop.InstanceConfig, redisop.ServerConfigPatch) (redisop.ServerConfigSetResult, error) {
	return redisop.ServerConfigSetResult{Instance: "default", Changed: []string{"maxmemory"}, Config: redisop.ServerConfig{MaxMemory: "268435456"}}, nil
}

// methods 是四类故障注入覆盖的方法及其合法参数。
var methods = []struct {
	name   string
	method string
	params string
}{
	{"instances", "redis.instances", `{}`},
	{"info", "redis.info", `{}`},
	{"keys", "redis.keys", `{"pattern":"*","limit":10}`},
	{"get", "redis.get", `{"key":"foo"}`},
	{"delete", "redis.delete", `{"keys":["foo"]}`},
	{"persistence", "redis.persistence", `{}`},
	{"memory", "redis.memory", `{}`},
	{"slowlog", "redis.slowlog", `{}`},
	{"keyInfo", "redis.keyInfo", `{"key":"foo"}`},
	{"set", "redis.set", `{"key":"foo","type":"string","value":"bar"}`},
	{"ttl", "redis.ttl", `{"key":"foo"}`},
	{"expire", "redis.expire", `{"key":"foo","ttl_s":60}`},
	{"persist", "redis.persist", `{"key":"foo"}`},
	{"bgsave", "redis.bgsave", `{}`},
	{"serverConfig.get", "redis.serverConfig.get", `{}`},
	{"serverConfig.set", "redis.serverConfig.set", `{"maxmemory":"268435456"}`},
	{"rename", "redis.rename", `{"key":"foo","new_key":"bar"}`},
	{"move", "redis.move", `{"key":"foo","db":1}`},
	{"copy", "redis.copy", `{"key":"foo","dest":"bar"}`},
}

// assertFault 对每个方法注入故障，断言返回 nil 结果 + 预期的协议错误码与类别关键词。
func assertFault(t *testing.T, kind redisop.FaultKind, wantCode, wantHint string) {
	t.Helper()
	exec := faultExecutor{kind: kind}
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			result, perr := redisop.Call(m.method, json.RawMessage(m.params), testInstances, exec)
			if perr == nil {
				t.Fatalf("%s 注入 %s 后未返回错误（结果=%s）", m.method, kind, result)
			}
			if result != nil {
				t.Fatalf("%s 出错时不应带结果: %s", m.method, result)
			}
			if perr.Code != wantCode {
				t.Fatalf("%s 错误码=%q，期望 %q（message=%q）", m.method, perr.Code, wantCode, perr.Message)
			}
			if !strings.Contains(perr.Message, wantHint) {
				t.Fatalf("%s 错误信息 %q 未含类别关键词 %q", m.method, perr.Message, wantHint)
			}
		})
	}
}

// sanityNoFault 验证无故障时各方法正常返回，证明故障断言不是"永远失败"的假通过。
func sanityNoFault(t *testing.T) {
	t.Helper()
	for _, m := range methods {
		result, perr := redisop.Call(m.method, json.RawMessage(m.params), testInstances, healthyExecutor{})
		if perr != nil {
			t.Fatalf("无故障时 %s 意外返回错误: %v", m.method, perr)
		}
		if len(result) == 0 {
			t.Fatalf("无故障时 %s 返回空结果", m.method)
		}
	}
}

// verifyErrorContract 断言错误体是标准协议结构（含 code 字段）。
func verifyErrorContract(t *testing.T, perr *pluginapi.Error) {
	t.Helper()
	if perr == nil {
		t.Fatal("错误体为 nil")
	}
	if perr.Code == "" || perr.Message == "" {
		t.Fatalf("错误体字段不完整: %+v", perr)
	}
}

func (healthyExecutor) KeyBytes(redisop.InstanceConfig, string) (int64, error) {
	return 1024, nil
}

func (healthyExecutor) Rename(redisop.InstanceConfig, string, string, bool) (redisop.RenameResult, error) {
	return redisop.RenameResult{Key: "foo", NewKey: "bar", Renamed: true, NX: true}, nil
}

func (healthyExecutor) Move(redisop.InstanceConfig, string, int) (redisop.MoveResult, error) {
	return redisop.MoveResult{Key: "foo", DB: 1, Moved: true}, nil
}

func (healthyExecutor) Copy(redisop.InstanceConfig, string, string, int, bool) (redisop.CopyResult, error) {
	return redisop.CopyResult{Key: "foo", Dest: "bar", DestDB: -1, Copied: true}, nil
}

func (f faultExecutor) KeyBytes(redisop.InstanceConfig, string) (int64, error) {
	return 0, f.fail("keyBytes")
}

func (f faultExecutor) Rename(redisop.InstanceConfig, string, string, bool) (redisop.RenameResult, error) {
	return redisop.RenameResult{}, f.fail("rename")
}

func (f faultExecutor) Move(redisop.InstanceConfig, string, int) (redisop.MoveResult, error) {
	return redisop.MoveResult{}, f.fail("move")
}

func (f faultExecutor) Copy(redisop.InstanceConfig, string, string, int, bool) (redisop.CopyResult, error) {
	return redisop.CopyResult{}, f.fail("copy")
}
