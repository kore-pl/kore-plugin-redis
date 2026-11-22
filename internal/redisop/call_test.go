package redisop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestBusinessVerb 方法名归一化：redis.<verb> 与裸 <verb> 都接受。
func TestBusinessVerb(t *testing.T) {
	cases := map[string]string{
		"redis.instances": "instances", "redis.scan": "scan",
		"instances": "instances", "scan": "scan",
	}
	for method, want := range cases {
		got, ok := businessVerb(method)
		if !ok || got != want {
			t.Fatalf("businessVerb(%q)=(%q,%v)，期望 (%q,true)", method, got, ok, want)
		}
	}
	if _, ok := businessVerb("redis.exec"); ok {
		t.Fatal("redis.exec 不应被接受")
	}
}

// TestCallResults 两方法正常路径返回结构化结果。
func TestCallResults(t *testing.T) {
	inst, perr := Call("redis.instances", json.RawMessage(`{"policy":"allkeys-lru"}`), "app:", nil)
	if perr != nil || !strings.Contains(string(inst), `"managed_externally":true`) {
		t.Fatalf("redis.instances 结果=%s err=%v", inst, perr)
	}
	scan, perr := Call("redis.scan", json.RawMessage(`{"prefix":"app:"}`), "app:", nil)
	if perr != nil || !strings.Contains(string(scan), `"scanned":0`) {
		t.Fatalf("redis.scan 结果=%s err=%v", scan, perr)
	}
}

// TestCallParamValidation 非法参数形状、前缀、策略、limit 与 cursor 均返回 E_INVALID。
func TestCallParamValidation(t *testing.T) {
	bad := []struct {
		method string
		params string
	}{
		{"redis.scan", `[]`},
		{"redis.scan", `not-json`},
		{"redis.scan", `{"prefix":"app:*"}`},
		{"redis.scan", `{"prefix":"app:","limit":10001}`},
		{"redis.scan", `{"prefix":"app:","cursor":-1}`},
		{"redis.instances", `{"policy":"volatile-none"}`},
	}
	for _, c := range bad {
		_, perr := Call(c.method, json.RawMessage(c.params), "app:", nil)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("Call(%s,%s) 错误=%v，期望 E_INVALID", c.method, c.params, perr)
		}
	}
}

// TestCallUnknownMethod 未知方法返回 E_NOTFOUND。
func TestCallUnknownMethod(t *testing.T) {
	_, perr := Call("redis.exec", json.RawMessage(`{}`), "app:", nil)
	if perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("未知方法错误=%v，期望 E_NOTFOUND", perr)
	}
}

// TestCallDefaultPrefix 空前缀时回落到配置默认前缀。
func TestCallDefaultPrefix(t *testing.T) {
	result, perr := Call("redis.scan", json.RawMessage(`{}`), "fallback:", nil)
	if perr != nil {
		t.Fatalf("默认前缀路径意外错误: %v", perr)
	}
	if !strings.Contains(string(result), `"prefix":"fallback:"`) {
		t.Fatalf("未使用默认前缀: %s", result)
	}
}
