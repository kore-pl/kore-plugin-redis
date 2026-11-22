// Package e2e 对真实 Redis 容器做端到端验证：
// 启动真实插件二进制 → UDS 握手 → config.set 配置实例 → instances/info/keys/get/delete。
//
// 默认跳过；需要先启动容器并导出开关：
//
//	docker run -d --name kore-redis-test -p 16379:6379 redis:7-alpine
//	KORE_REDIS_E2E=1 KORE_REDIS_E2E_PORT=16379 go test -timeout 180s -v ./tests/e2e
//
// 数据种子由测试通过 `docker exec <容器> redis-cli` 写入（插件自身不依赖 redis-cli）。
package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

const defaultContainer = "kore-redis-test"

// TestDockerRedisE2E 真实二进制 + 真实 Redis 的全流程验收。
func TestDockerRedisE2E(t *testing.T) {
	if os.Getenv("KORE_REDIS_E2E") != "1" {
		t.Skip("设置 KORE_REDIS_E2E=1 并启动 kore-redis-test 容器后运行")
	}
	port := 16379
	if raw := strings.TrimSpace(os.Getenv("KORE_REDIS_E2E_PORT")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 65535 {
			t.Fatalf("KORE_REDIS_E2E_PORT=%q 非法", raw)
		}
		port = parsed
	}
	container := strings.TrimSpace(os.Getenv("KORE_REDIS_E2E_CONTAINER"))
	if container == "" {
		container = defaultContainer
	}

	seedRedis(t, container)
	session := startPlugin(t)
	defer session.Close()

	// 步骤 1：初始 config.get 为默认来源（未 export KORE_REDIS_*）。
	raw := session.invokeOK(t, "redis.config.get", map[string]any{})
	logJSON(t, "STEP1 redis.config.get (default)", raw)
	assertJSONField(t, raw, "source", `"default"`)

	// 步骤 2：config.set 配置真实实例。
	raw = session.invokeOK(t, "redis.config.set", map[string]any{
		"values": map[string]any{"host": "127.0.0.1", "port": port, "password_env": ""},
	})
	logJSON(t, "STEP2 redis.config.set", raw)
	assertJSONField(t, raw, "saved", "true")
	assertJSONField(t, raw, "restart_required", "false")

	// 步骤 3：config.get 来源变为 file，端口已生效。
	raw = session.invokeOK(t, "redis.config.get", map[string]any{})
	logJSON(t, "STEP3 redis.config.get (file)", raw)
	assertJSONField(t, raw, "source", `"file"`)
	assertJSONContains(t, raw, fmt.Sprintf(`"port":%d`, port))

	// 步骤 4：instances 显示 reachable + 版本 + 运行时长。
	raw = session.invokeOK(t, "redis.instances", map[string]any{})
	logJSON(t, "STEP4 redis.instances", raw)
	var instances struct {
		Count     int `json:"count"`
		Instances []struct {
			Name      string `json:"name"`
			Host      string `json:"host"`
			Port      int    `json:"port"`
			Reachable bool   `json:"reachable"`
			Version   string `json:"version"`
			UptimeS   int64  `json:"uptime_s"`
			Error     string `json:"error"`
		} `json:"instances"`
	}
	mustUnmarshal(t, raw, &instances)
	if instances.Count != 1 || len(instances.Instances) != 1 {
		t.Fatalf("实例数=%d，期望 1: %s", instances.Count, raw)
	}
	status := instances.Instances[0]
	if !status.Reachable || status.Error != "" {
		t.Fatalf("实例不可达: %+v", status)
	}
	if !strings.HasPrefix(status.Version, "7.") || status.UptimeS <= 0 {
		t.Fatalf("版本/运行时长异常: %+v", status)
	}
	if status.Host != "127.0.0.1" || status.Port != port {
		t.Fatalf("地址回显异常: %+v", status)
	}

	// 步骤 5：info 有 memory/clients/stats/persistence/keyspace。
	raw = session.invokeOK(t, "redis.info", map[string]any{"instance": "default"})
	logJSON(t, "STEP5 redis.info", raw)
	var info struct {
		Server struct {
			Version string `json:"version"`
			UptimeS int64  `json:"uptime_s"`
			TCPPort int    `json:"tcp_port"`
		} `json:"server"`
		Clients struct {
			Connected int64 `json:"connected"`
		} `json:"clients"`
		Memory struct {
			UsedMemoryHuman string `json:"used_memory_human"`
			MaxMemoryHuman  string `json:"maxmemory_human"`
		} `json:"memory"`
		Stats struct {
			TotalCommandsProcessed int64 `json:"total_commands_processed"`
			KeyspaceHits           int64 `json:"keyspace_hits"`
		} `json:"stats"`
		Persistence struct {
			RDBlastSaveTime int64 `json:"rdb_last_save_time"`
			AOFEnabled      bool  `json:"aof_enabled"`
		} `json:"persistence"`
		Keyspace map[string]string `json:"keyspace"`
	}
	mustUnmarshal(t, raw, &info)
	if info.Server.Version == "" || info.Server.UptimeS <= 0 || info.Server.TCPPort != 6379 {
		t.Fatalf("server 段异常: %+v", info.Server)
	}
	if info.Clients.Connected < 1 {
		t.Fatalf("clients.connected=%d，期望 ≥1", info.Clients.Connected)
	}
	if info.Memory.UsedMemoryHuman == "" {
		t.Fatalf("memory.used_memory_human 为空: %+v", info.Memory)
	}
	if info.Stats.TotalCommandsProcessed <= 0 {
		t.Fatalf("stats.total_commands_processed=%d，期望 >0", info.Stats.TotalCommandsProcessed)
	}
	if info.Persistence.RDBlastSaveTime <= 0 {
		t.Fatalf("persistence.rdb_last_save_time=%d，期望 >0", info.Persistence.RDBlastSaveTime)
	}
	// 空库时 Redis 不返回 keyspace 段；建数据后再校验 db0。

	// 步骤 6：用容器内 redis-cli 写入 foo（插件不依赖 redis-cli）。
	runRedisCLI(t, container, "SET", "foo", "bar")

	// 步骤 6b：写入后 INFO 的 keyspace 应出现 db0。
	raw = session.invokeOK(t, "redis.info", map[string]any{"instance": "default"})
	logJSON(t, "STEP6 redis.info (after seed)", raw)
	mustUnmarshal(t, raw, &info)
	keyspace, ok := info.Keyspace["db0"]
	if !ok || !strings.HasPrefix(keyspace, "keys=1,expires=") {
		t.Fatalf("keyspace 缺 db0 或形状异常: %+v", info.Keyspace)
	}

	// 步骤 7：keys 用 SCAN 找到 foo，类型 string、TTL -1。
	raw = session.invokeOK(t, "redis.keys", map[string]any{"instance": "default", "pattern": "foo*", "limit": 200})
	logJSON(t, "STEP7 redis.keys pattern=foo*", raw)
	var keys struct {
		Keys []struct {
			Key  string `json:"key"`
			Type string `json:"type"`
			TTLS int64  `json:"ttl_s"`
		} `json:"keys"`
		Scanned   int  `json:"scanned"`
		Truncated bool `json:"truncated"`
	}
	mustUnmarshal(t, raw, &keys)
	if len(keys.Keys) != 1 || keys.Keys[0].Key != "foo" || keys.Keys[0].Type != "string" || keys.Keys[0].TTLS != -1 {
		t.Fatalf("keys 结果异常: %+v", keys)
	}
	if keys.Truncated {
		t.Fatalf("单键结果不应截断: %+v", keys)
	}

	// 步骤 8：get foo 返回 bar + type string。
	raw = session.invokeOK(t, "redis.get", map[string]any{"instance": "default", "key": "foo"})
	logJSON(t, "STEP8 redis.get foo", raw)
	var got struct {
		Key   string          `json:"key"`
		Type  string          `json:"type"`
		TTLS  int64           `json:"ttl_s"`
		Value json.RawMessage `json:"value"`
	}
	mustUnmarshal(t, raw, &got)
	if got.Key != "foo" || got.Type != "string" || got.TTLS != -1 || string(got.Value) != `"bar"` {
		t.Fatalf("get 结果异常: %+v value=%s", got, got.Value)
	}

	// 步骤 9：delete foo → deleted=1。
	raw = session.invokeOK(t, "redis.delete", map[string]any{"instance": "default", "keys": []string{"foo"}})
	logJSON(t, "STEP9 redis.delete foo", raw)
	var deleted struct {
		Deleted int64    `json:"deleted"`
		Keys    []string `json:"keys"`
	}
	mustUnmarshal(t, raw, &deleted)
	if deleted.Deleted != 1 || len(deleted.Keys) != 1 || deleted.Keys[0] != "foo" {
		t.Fatalf("delete 结果异常: %+v", deleted)
	}

	// 步骤 10：get 已删除的键 → E_NOTFOUND。
	response := session.invoke(t, "redis.get", map[string]any{"instance": "default", "key": "foo"})
	if response.Error == nil || response.Error.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("删除后 get 错误=%v，期望 E_NOTFOUND", response.Error)
	}
	t.Logf("STEP10 redis.get foo 删除后: %s: %s", response.Error.Code, response.Error.Message)

	// 步骤 11：危险命令不在方法面（FLUSHALL/CONFIG/SHUTDOWN 均 E_NOTFOUND）。
	for _, method := range []string{"redis.flushall", "redis.flushdb", "redis.shutdown", "redis.command"} {
		response := session.invoke(t, method, map[string]any{})
		if response.Error == nil || response.Error.Code != pluginapi.E_NOTFOUND {
			t.Fatalf("%s 错误=%v，期望 E_NOTFOUND", method, response.Error)
		}
		t.Logf("STEP11 %s 不在方法面: %s: %s", method, response.Error.Code, response.Error.Message)
	}
}

// pluginSession 是真实插件进程 + 假内核连接。
type pluginSession struct {
	t       *testing.T
	conn    net.Conn
	command *exec.Cmd
	stderr  *bytes.Buffer
	nextID  int
}

// startPlugin 构建并启动插件，完成 hello 握手。
func startPlugin(t *testing.T) *pluginSession {
	t.Helper()
	temp := t.TempDir()
	binary := filepath.Join(temp, "redis")
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", binary, ".")
	build.Dir = filepath.Join("..", "..")
	build.Env = append(filteredEnv("KORE_REDIS_"), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建插件失败: %v\n%s", err, output)
	}

	socket := filepath.Join(temp, "plugin.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("监听 UDS 失败: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	command := exec.Command(binary)
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	command.Env = append(filteredEnv("KORE_REDIS_"),
		"KORE_PLUGIN_SOCK="+socket,
		"KORE_PLUGIN_ID=redis",
		"KORE_STATE_DIR="+filepath.Join(temp, "state"),
		"KORE_LOG_LEVEL=error",
	)
	if err := command.Start(); err != nil {
		t.Fatalf("启动插件失败: %v", err)
	}
	session := &pluginSession{t: t, command: command, stderr: stderr}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})

	deadline := time.Now().Add(10 * time.Second)
	if unixListener, ok := listener.(*net.UnixListener); ok {
		_ = unixListener.SetDeadline(deadline)
	}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatalf("等待插件连接失败: %v\n%s", err, stderr.String())
	}
	session.conn = conn
	t.Cleanup(func() { _ = conn.Close() })

	// 读取 hello 并回应。
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	payload, err := pluginapi.ReadFrame(conn, pluginapi.MaxFrameSize)
	if err != nil {
		t.Fatalf("读取 hello 失败: %v\n%s", err, stderr.String())
	}
	envelope, err := pluginapi.DecodeFrame(payload)
	if err != nil {
		t.Fatalf("解码 hello 失败: %v", err)
	}
	hello, ok := envelope.(*pluginapi.Request)
	if !ok || hello.Method != "kore.hello" {
		t.Fatalf("首帧不是 kore.hello: %+v", envelope)
	}
	reply, _ := json.Marshal(map[string]any{"ok": true, "protocol": pluginapi.Protocol})
	if err := pluginapi.WriteJSON(conn, &pluginapi.Response{
		Type: pluginapi.TypeResponse, ID: hello.ID, ReqID: hello.ReqID, Result: reply,
	}); err != nil {
		t.Fatalf("回写 hello 失败: %v", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	return session
}

// Close 关闭连接并等待插件退出。
func (s *pluginSession) Close() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
	_ = s.command.Process.Kill()
	_, _ = s.command.Process.Wait()
}

// invoke 发送请求并读回响应。
func (s *pluginSession) invoke(t *testing.T, method string, params map[string]any) *pluginapi.Response {
	t.Helper()
	s.nextID++
	rawParams, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}
	request := &pluginapi.Request{
		Type:   pluginapi.TypeRequest,
		ID:     fmt.Sprintf("e2e-%02d", s.nextID),
		ReqID:  fmt.Sprintf("r-e2e-%02d", s.nextID),
		Method: method, Params: rawParams,
	}
	if err := pluginapi.WriteJSON(s.conn, request); err != nil {
		t.Fatalf("%s 写请求失败: %v\n%s", method, err, s.stderr.String())
	}
	_ = s.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	payload, err := pluginapi.ReadFrame(s.conn, pluginapi.MaxFrameSize)
	if err != nil {
		t.Fatalf("%s 读响应失败: %v\n%s", method, err, s.stderr.String())
	}
	envelope, err := pluginapi.DecodeFrame(payload)
	if err != nil {
		t.Fatalf("%s 解码响应失败: %v", method, err)
	}
	response, ok := envelope.(*pluginapi.Response)
	if !ok || response.ID != request.ID {
		t.Fatalf("%s 响应帧异常: %+v", method, envelope)
	}
	return response
}

// invokeOK 调用方法并要求成功。
func (s *pluginSession) invokeOK(t *testing.T, method string, params map[string]any) json.RawMessage {
	t.Helper()
	response := s.invoke(t, method, params)
	if response.Error != nil {
		t.Fatalf("%s 失败: %s: %s", method, response.Error.Code, response.Error.Message)
	}
	return response.Result
}

// seedRedis 清理测试键（用容器内 redis-cli，不经过插件）。
func seedRedis(t *testing.T, container string) {
	t.Helper()
	runRedisCLI(t, container, "DEL", "foo")
}

// runRedisCLI 通过 docker exec 在容器内执行 redis-cli。
func runRedisCLI(t *testing.T, container string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"exec", container, "redis-cli"}, args...)
	output, err := exec.Command("docker", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker exec redis-cli %s 失败: %v (%s)", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

// filteredEnv 从当前环境剔除指定前缀，保证 E2E 不被外部 KORE_REDIS_* 干扰。
func filteredEnv(prefix string) []string {
	env := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, prefix) {
			continue
		}
		env = append(env, item)
	}
	return env
}

func mustUnmarshal(t *testing.T, raw json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("解析结果失败: %v (%s)", err, raw)
	}
}

// logJSON 以缩进 JSON 输出到测试日志，作为验收证据。
func logJSON(t *testing.T, label string, raw json.RawMessage) {
	t.Helper()
	var buffer bytes.Buffer
	if err := json.Indent(&buffer, raw, "", "  "); err != nil {
		t.Logf("%s: %s", label, raw)
		return
	}
	t.Logf("%s:\n%s", label, buffer.String())
}

// assertJSONField 断言 JSON 对象顶层字段等于给定 JSON 文本值（true/false/数字/字符串）。
func assertJSONField(t *testing.T, raw json.RawMessage, key, want string) {
	t.Helper()
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatalf("解析 %s 失败: %v", key, err)
	}
	got, ok := values[key]
	if !ok {
		t.Fatalf("缺少字段 %s: %s", key, raw)
	}
	if string(got) != want {
		t.Fatalf("字段 %s=%s，期望 %s", key, got, want)
	}
}

// assertJSONContains 断言结果包含给定文本。
func assertJSONContains(t *testing.T, raw json.RawMessage, want string) {
	t.Helper()
	if !bytes.Contains(raw, []byte(want)) {
		t.Fatalf("结果未含 %q: %s", want, raw)
	}
}
