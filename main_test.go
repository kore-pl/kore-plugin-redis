package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pengtang1024/kore-plugin-redis/internal/plugincfg"
	"github.com/pengtang1024/kore-plugin-redis/internal/redisop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// testConfig 构造握手与会话循环共用的一致配置（不依赖环境变量）。
func testConfig() config {
	return config{
		id:           "redis",
		version:      "0.2.0",
		requiresKore: ">=0.1.0 <0.2.0",
		capabilities: []string{"system:admin"},
		startedAt:    time.Now(),
		values: fileValues{
			Host: defaultHost, Port: defaultPort, DB: 0, TimeoutS: defaultTimeoutS,
			Instances: []string{}, PasswordEnvs: []string{},
		},
		source:    plugincfg.SourceDefault,
		instances: []redisop.InstanceConfig{},
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func readFrame(t *testing.T, conn net.Conn) any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	payload, err := pluginapi.ReadFrame(conn, pluginapi.MaxFrameSize)
	if err != nil {
		t.Fatalf("读帧失败: %v", err)
	}
	env, err := pluginapi.DecodeFrame(payload)
	if err != nil {
		t.Fatalf("解码帧失败: %v", err)
	}
	return env
}

func readRequest(t *testing.T, conn net.Conn) *pluginapi.Request {
	t.Helper()
	request, ok := readFrame(t, conn).(*pluginapi.Request)
	if !ok {
		t.Fatal("帧类型不是 request")
	}
	return request
}

func writeResponse(t *testing.T, conn net.Conn, id, reqID string, result json.RawMessage) {
	t.Helper()
	if err := pluginapi.WriteJSON(conn, &pluginapi.Response{
		Type: pluginapi.TypeResponse, ID: id, ReqID: reqID, Result: result,
	}); err != nil {
		t.Fatalf("写响应失败: %v", err)
	}
}

// TestHandshake 校验 hello 首帧字段与三步握手。
func TestHandshake(t *testing.T) {
	pluginConn, kernelConn := net.Pipe()
	defer func() { _ = kernelConn.Close() }()
	done := make(chan error, 1)
	go func() { done <- handshake(pluginConn, testConfig()); _ = pluginConn.Close() }()

	request := readRequest(t, kernelConn)
	if request.Method != helloMethod {
		t.Fatalf("首帧方法=%q，期望 %q", request.Method, helloMethod)
	}
	var params helloParams
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatalf("解析 hello 参数失败: %v", err)
	}
	if params.PluginID != "redis" || params.ID != "redis" {
		t.Fatalf("plugin_id/id=%q/%q，期望 redis", params.PluginID, params.ID)
	}
	if params.Protocol != pluginapi.Protocol || params.Version != "0.2.0" {
		t.Fatalf("protocol/version=%q/%q 不符合 §5", params.Protocol, params.Version)
	}
	if params.PID != os.Getpid() {
		t.Fatalf("pid=%d，期望 %d", params.PID, os.Getpid())
	}
	result, err := json.Marshal(helloResult{OK: true, Protocol: pluginapi.Protocol})
	if err != nil {
		t.Fatalf("序列化握手响应失败: %v", err)
	}
	writeResponse(t, kernelConn, request.ID, request.ReqID, result)
	if err := <-done; err != nil {
		t.Fatalf("握手返回错误: %v", err)
	}
}

// TestServeShutdown 会话循环收到 kore.shutdown 后优雅退出。
// 关键：net.Pipe 无缓冲——必须先读走响应帧，serve 的 WriteJSON 才能返回，
// 否则 goroutine 阻塞在写、<-done 永远等不到，测试直接超时死锁。
func TestServeShutdown(t *testing.T) {
	pluginConn, kernelConn := net.Pipe()
	defer func() { _ = kernelConn.Close() }()
	done := make(chan error, 1)
	cfg := testConfig()
	go func() { done <- serve(pluginConn, &cfg, testLogger()); _ = pluginConn.Close() }()

	request := &pluginapi.Request{
		Type: pluginapi.TypeRequest, ID: "id-1", ReqID: "r-1", Method: "kore.shutdown",
	}
	if err := pluginapi.WriteJSON(kernelConn, request); err != nil {
		t.Fatalf("写请求失败: %v", err)
	}
	env := readFrame(t, kernelConn) // 配对读响应帧，解除无缓冲管道阻塞
	response, ok := env.(*pluginapi.Response)
	if !ok || response.ID != "id-1" {
		t.Fatalf("响应帧异常: %+v", env)
	}
	if err := <-done; err != nil {
		t.Fatalf("serve 返回错误: %v", err)
	}
}

// TestServeMethodFrames 会话循环对两个业务方法都回结构化响应帧。
// 每个请求都配对一次读响应，保持 net.Pipe 两侧严格同步。
func TestServeMethodFrames(t *testing.T) {
	pluginConn, kernelConn := net.Pipe()
	defer func() { _ = kernelConn.Close() }()
	done := make(chan error, 1)
	cfg := testConfig()
	go func() { done <- serve(pluginConn, &cfg, testLogger()); _ = pluginConn.Close() }()

	cases := []struct {
		id, method, params, want string
	}{
		{"id-a", "redis.instances", `{}`, `"count":0`},
		{"id-b", "redis.config.get", `{}`, `"key":"host"`},
	}
	for _, c := range cases {
		req := &pluginapi.Request{
			Type: pluginapi.TypeRequest, ID: c.id, ReqID: "r-" + c.id,
			Method: c.method, Params: json.RawMessage(c.params),
		}
		if err := pluginapi.WriteJSON(kernelConn, req); err != nil {
			t.Fatalf("写请求 %s 失败: %v", c.method, err)
		}
		env := readFrame(t, kernelConn)
		resp, ok := env.(*pluginapi.Response)
		if !ok || resp.ID != c.id || resp.Error != nil {
			t.Fatalf("%s 响应帧异常: %+v", c.method, env)
		}
		if !strings.Contains(string(resp.Result), c.want) {
			t.Fatalf("%s 结果=%s，未含 %q", c.method, resp.Result, c.want)
		}
	}

	// 收尾 shutdown 同样配对读响应帧。
	shutdown := &pluginapi.Request{Type: pluginapi.TypeRequest, ID: "id-z", ReqID: "r-z", Method: "kore.shutdown"}
	if err := pluginapi.WriteJSON(kernelConn, shutdown); err != nil {
		t.Fatalf("写 shutdown 失败: %v", err)
	}
	env := readFrame(t, kernelConn)
	if resp, ok := env.(*pluginapi.Response); !ok || resp.ID != "id-z" {
		t.Fatalf("shutdown 响应帧异常: %+v", env)
	}
	if err := <-done; err != nil {
		t.Fatalf("serve 返回错误: %v", err)
	}
}
