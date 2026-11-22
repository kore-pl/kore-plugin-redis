package redisop

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// fakeRedis 是本进程内的最小 RESP 服务端：记录收到的命令并按 handler 回复，
// 用于验证客户端协议行为与“密码只走 AUTH 命令”。
type fakeRedis struct {
	listener net.Listener
	mu       sync.Mutex
	commands [][]string
	handler  func(command []string) (any, error)
	wg       sync.WaitGroup
}

// startFakeRedis 启动假 Redis 并注册清理。
func startFakeRedis(t *testing.T, handler func(command []string) (any, error)) *fakeRedis {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动假 Redis 失败: %v", err)
	}
	server := &fakeRedis{listener: listener, handler: handler}
	server.wg.Add(1)
	go server.accept()
	t.Cleanup(server.Close)
	return server
}

// Close 关闭监听并等待连接退出。
func (f *fakeRedis) Close() {
	_ = f.listener.Close()
	f.wg.Wait()
}

// accept 接收连接并为每条连接起一个会话。
func (f *fakeRedis) accept() {
	defer f.wg.Done()
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			f.serve(conn)
		}()
	}
}

// serve 读取命令、记录并写回 handler 的回复。
func (f *fakeRedis) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	for {
		command, err := readTestCommand(reader)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.commands = append(f.commands, command)
		f.mu.Unlock()
		reply, handlerErr := f.handler(command)
		if handlerErr != nil {
			_, _ = fmt.Fprintf(writer, "-%s\r\n", handlerErr.Error())
			_ = writer.Flush()
			continue
		}
		if err := writeTestReply(writer, reply); err != nil {
			return
		}
		if err := writer.Flush(); err != nil {
			return
		}
	}
}

// CommandList 返回已收到的命令快照。
func (f *fakeRedis) CommandList() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.commands))
	copy(out, f.commands)
	return out
}

// Instance 返回指向假服务的实例配置。
func (f *fakeRedis) Instance() InstanceConfig {
	address, ok := f.listener.Addr().(*net.TCPAddr)
	if !ok {
		return InstanceConfig{Name: "default", Host: "127.0.0.1", Port: 6379}
	}
	return InstanceConfig{Name: "default", Host: "127.0.0.1", Port: address.Port}
}

// readTestCommand 解析客户端发来的 RESP 命令数组。
func readTestCommand(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(line, "\r\n")
	if line == "" || line[0] != '*' {
		return nil, fmt.Errorf("非法命令帧: %q", line)
	}
	count, err := strconv.Atoi(line[1:])
	if err != nil || count < 0 {
		return nil, fmt.Errorf("非法参数个数: %q", line[1:])
	}
	args := make([]string, 0, count)
	for index := 0; index < count; index++ {
		bulk, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		bulk = strings.TrimSuffix(bulk, "\r\n")
		if bulk == "" || bulk[0] != '$' {
			return nil, fmt.Errorf("非法 bulk 帧: %q", bulk)
		}
		length, err := strconv.Atoi(bulk[1:])
		if err != nil || length < 0 {
			return nil, fmt.Errorf("非法 bulk 长度: %q", bulk[1:])
		}
		buffer := make([]byte, length+2)
		if _, err := io.ReadFull(reader, buffer); err != nil {
			return nil, err
		}
		args = append(args, string(buffer[:length]))
	}
	return args, nil
}

// writeTestReply 把测试回复编码为 RESP。
func writeTestReply(writer *bufio.Writer, reply any) error {
	switch value := reply.(type) {
	case nil:
		_, err := writer.WriteString("$-1\r\n")
		return err
	case string:
		if _, err := fmt.Fprintf(writer, "$%d\r\n%s\r\n", len(value), value); err != nil {
			return err
		}
		return nil
	case int:
		_, err := fmt.Fprintf(writer, ":%d\r\n", value)
		return err
	case int64:
		_, err := fmt.Fprintf(writer, ":%d\r\n", value)
		return err
	case []string:
		items := make([]any, len(value))
		for index, item := range value {
			items[index] = item
		}
		return writeTestReply(writer, items)
	case []any:
		if _, err := fmt.Fprintf(writer, "*%d\r\n", len(value)); err != nil {
			return err
		}
		for _, item := range value {
			if err := writeTestReply(writer, item); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("不支持的测试回复类型 %T", reply)
	}
}

// TestClientAuthAndProbe 验证密码只通过 AUTH 命令发送，PING/INFO 正常解析。
func TestClientAuthAndProbe(t *testing.T) {
	t.Setenv("KORE_REDIS_TEST_PASSWORD", "s3cr3t-value")
	server := startFakeRedis(t, func(command []string) (any, error) {
		switch strings.ToUpper(command[0]) {
		case "AUTH":
			if command[1] != "s3cr3t-value" {
				return nil, fmt.Errorf("WRONGPASS")
			}
			return "OK", nil
		case "PING":
			return "PONG", nil
		case "INFO":
			return "redis_version:7.2.4\r\nuptime_in_seconds:321\r\ntcp_port:6379\r\n", nil
		default:
			return nil, fmt.Errorf("unexpected command %q", command[0])
		}
	})
	instance := server.Instance()
	instance.PasswordEnv = "KORE_REDIS_TEST_PASSWORD"

	result, perr := Call("redis.instances", json.RawMessage(`{}`), []InstanceConfig{instance}, nil)
	if perr != nil {
		t.Fatalf("instances 失败: %v", perr)
	}
	if !strings.Contains(string(result), `"reachable":true`) || !strings.Contains(string(result), `"version":"7.2.4"`) {
		t.Fatalf("instances 结果异常: %s", result)
	}
	if strings.Contains(string(result), "s3cr3t-value") {
		t.Fatal("结果泄漏密码")
	}
	commands := server.CommandList()
	if len(commands) < 3 || len(commands[0]) != 2 || commands[0][0] != "AUTH" || commands[0][1] != "s3cr3t-value" {
		t.Fatalf("首条命令应为 AUTH <密码>，实际: %v", commands)
	}
}

// TestClientAuthFailureSanitized 认证失败时错误信息不得回显密码。
func TestClientAuthFailureSanitized(t *testing.T) {
	t.Setenv("KORE_REDIS_TEST_PASSWORD", "s3cr3t-value")
	server := startFakeRedis(t, func(command []string) (any, error) {
		return nil, fmt.Errorf("WRONGPASS invalid password s3cr3t-value")
	})
	instance := server.Instance()
	instance.PasswordEnv = "KORE_REDIS_TEST_PASSWORD"
	statuses, err := NewClient().Instances([]InstanceConfig{instance})
	if err != nil {
		t.Fatalf("Instances 不应返回整体错误: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Reachable {
		t.Fatalf("认证失败应标记不可达: %+v", statuses)
	}
	if strings.Contains(statuses[0].Error, "s3cr3t-value") {
		t.Fatalf("错误信息泄漏密码: %q", statuses[0].Error)
	}
	if !strings.Contains(statuses[0].Error, "***") {
		t.Fatalf("密码应被脱敏为 ***: %q", statuses[0].Error)
	}
}

// TestClientInfoParsing INFO 全量解析为结构化字段与规范化 keyspace。
func TestClientInfoParsing(t *testing.T) {
	info := strings.Join([]string{
		"# Server",
		"redis_version:7.2.4",
		"uptime_in_seconds:1000",
		"tcp_port:6379",
		"# Clients",
		"connected_clients:3",
		"# Memory",
		"used_memory_human:1.02M",
		"used_memory_peak_human:2.00M",
		"maxmemory_human:0B",
		"# Stats",
		"total_connections_received:12",
		"total_commands_processed:34",
		"keyspace_hits:5",
		"keyspace_misses:6",
		"# Persistence",
		"rdb_last_save_time:1700000000",
		"aof_enabled:1",
		"# Keyspace",
		"db0:keys=7,expires=2,avg_ttl=0",
		"db1:keys=1,expires=0,avg_ttl=0",
		"",
	}, "\r\n")
	server := startFakeRedis(t, func(command []string) (any, error) {
		if strings.ToUpper(command[0]) != "INFO" {
			return nil, fmt.Errorf("unexpected command %q", command[0])
		}
		return info, nil
	})
	result, perr := Call("redis.info", json.RawMessage(`{"instance":"default"}`),
		[]InstanceConfig{server.Instance()}, nil)
	if perr != nil {
		t.Fatalf("info 失败: %v", perr)
	}
	var parsed InfoResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("解析 info 结果失败: %v", err)
	}
	if parsed.Server.Version != "7.2.4" || parsed.Server.UptimeS != 1000 || parsed.Server.TCPPort != 6379 {
		t.Fatalf("server 段异常: %+v", parsed.Server)
	}
	if parsed.Clients.Connected != 3 {
		t.Fatalf("clients 段异常: %+v", parsed.Clients)
	}
	if parsed.Memory.UsedMemoryHuman != "1.02M" || parsed.Memory.MaxMemoryHuman != "0B" {
		t.Fatalf("memory 段异常: %+v", parsed.Memory)
	}
	if parsed.Stats.TotalCommandsProcessed != 34 || parsed.Stats.KeyspaceMisses != 6 {
		t.Fatalf("stats 段异常: %+v", parsed.Stats)
	}
	if !parsed.Persistence.AOFEnabled || parsed.Persistence.RDBlastSaveTime != 1700000000 {
		t.Fatalf("persistence 段异常: %+v", parsed.Persistence)
	}
	if parsed.Keyspace["db0"] != "keys=7,expires=2" || parsed.Keyspace["db1"] != "keys=1,expires=0" {
		t.Fatalf("keyspace 段异常: %+v", parsed.Keyspace)
	}
}

// TestClientKeysScanAndTypes SCAN 游标遍历 + TYPE/TTL pipeline。
func TestClientKeysScanAndTypes(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		switch strings.ToUpper(command[0]) {
		case "SCAN":
			if command[1] == "0" {
				return []any{"2", []any{"foo", "bar"}}, nil
			}
			return []any{"0", []any{"baz"}}, nil
		case "TYPE":
			return "string", nil
		case "TTL":
			if command[1] == "bar" {
				return int64(30), nil
			}
			return int64(-1), nil
		default:
			return nil, fmt.Errorf("unexpected command %q", command[0])
		}
	})
	instance := server.Instance()

	result, perr := Call("redis.keys", json.RawMessage(`{"pattern":"*","limit":2}`),
		[]InstanceConfig{instance}, nil)
	if perr != nil {
		t.Fatalf("keys 失败: %v", perr)
	}
	var keys KeysResult
	if err := json.Unmarshal(result, &keys); err != nil {
		t.Fatalf("解析 keys 结果失败: %v", err)
	}
	if len(keys.Keys) != 2 || keys.Scanned != 2 {
		t.Fatalf("keys 结果异常: %+v", keys)
	}
	if !keys.Truncated {
		t.Fatalf("limit=2 且游标未归零应标记 truncated: %+v", keys)
	}
	if keys.Keys[1].TTLS != 30 || keys.Keys[0].TTLS != -1 {
		t.Fatalf("TTL 解析异常: %+v", keys.Keys)
	}

	result, perr = Call("redis.keys", json.RawMessage(`{"pattern":"*","limit":10}`),
		[]InstanceConfig{instance}, nil)
	if perr != nil {
		t.Fatalf("keys 失败: %v", perr)
	}
	if err := json.Unmarshal(result, &keys); err != nil {
		t.Fatalf("解析 keys 结果失败: %v", err)
	}
	if len(keys.Keys) != 3 || keys.Truncated {
		t.Fatalf("扫完应返回 3 个键且不截断: %+v", keys)
	}
}

// TestClientGetDispatch 按类型分派读取，返回对应 JSON 载荷。
func TestClientGetDispatch(t *testing.T) {
	cases := []struct {
		name   string
		keyTyp string
		reply  func(command []string) (any, error)
		want   string
	}{
		{
			name: "string", keyTyp: "string",
			reply: func(command []string) (any, error) { return "bar", nil },
			want:  `"bar"`,
		},
		{
			name: "hash", keyTyp: "hash",
			reply: func(command []string) (any, error) { return []string{"f1", "v1", "f2", "v2"}, nil },
			want:  `{"f1":"v1","f2":"v2"}`,
		},
		{
			name: "list", keyTyp: "list",
			reply: func(command []string) (any, error) { return []string{"a", "b"}, nil },
			want:  `["a","b"]`,
		},
		{
			name: "set", keyTyp: "set",
			reply: func(command []string) (any, error) { return []string{"x"}, nil },
			want:  `["x"]`,
		},
		{
			name: "zset", keyTyp: "zset",
			reply: func(command []string) (any, error) { return []string{"m", "1.5"}, nil },
			want:  `[{"member":"m","score":"1.5"}]`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := startFakeRedis(t, func(command []string) (any, error) {
				switch strings.ToUpper(command[0]) {
				case "TYPE":
					return c.keyTyp, nil
				case "TTL":
					return int64(-1), nil
				default:
					return c.reply(command)
				}
			})
			result, perr := Call("redis.get", json.RawMessage(`{"key":"foo"}`),
				[]InstanceConfig{server.Instance()}, nil)
			if perr != nil {
				t.Fatalf("get 失败: %v", perr)
			}
			var got GetResult
			if err := json.Unmarshal(result, &got); err != nil {
				t.Fatalf("解析 get 结果失败: %v", err)
			}
			if got.Type != c.keyTyp || got.TTLS != -1 {
				t.Fatalf("类型/TTL 异常: %+v", got)
			}
			if string(got.Value) != c.want {
				t.Fatalf("value=%s，期望 %s", got.Value, c.want)
			}
		})
	}
}

// TestClientGetNotFound TYPE none 返回 E_NOTFOUND。
func TestClientGetNotFound(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		if strings.ToUpper(command[0]) == "TYPE" {
			return "none", nil
		}
		return nil, fmt.Errorf("unexpected command %q", command[0])
	})
	_, perr := Call("redis.get", json.RawMessage(`{"key":"missing"}`),
		[]InstanceConfig{server.Instance()}, nil)
	if perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("get 缺失键错误=%v，期望 E_NOTFOUND", perr)
	}
}

// TestClientGetUnsupportedType stream 等类型返回 E_UNSUPPORTED。
func TestClientGetUnsupportedType(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		switch strings.ToUpper(command[0]) {
		case "TYPE":
			return "stream", nil
		case "TTL":
			return int64(-1), nil
		default:
			return nil, fmt.Errorf("unexpected command %q", command[0])
		}
	})
	_, perr := Call("redis.get", json.RawMessage(`{"key":"events"}`),
		[]InstanceConfig{server.Instance()}, nil)
	if perr == nil || perr.Code != pluginapi.E_UNSUPPORTED {
		t.Fatalf("stream 类型错误=%v，期望 E_UNSUPPORTED", perr)
	}
}

// TestClientDelete DEL 返回实际删除数量。
func TestClientDelete(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		if strings.ToUpper(command[0]) != "DEL" {
			return nil, fmt.Errorf("unexpected command %q", command[0])
		}
		return int64(2), nil
	})
	result, perr := Call("redis.delete", json.RawMessage(`{"keys":["a","b"]}`),
		[]InstanceConfig{server.Instance()}, nil)
	if perr != nil {
		t.Fatalf("delete 失败: %v", perr)
	}
	var deleted DeleteResult
	if err := json.Unmarshal(result, &deleted); err != nil {
		t.Fatalf("解析 delete 结果失败: %v", err)
	}
	if deleted.Deleted != 2 || len(deleted.Keys) != 2 {
		t.Fatalf("delete 结果异常: %+v", deleted)
	}
}

// TestClientPasswordEnvMissing 密码环境变量缺失时返回 E_INVALID，且不尝试连接。
func TestClientPasswordEnvMissing(t *testing.T) {
	instance := InstanceConfig{Name: "default", Host: "127.0.0.1", Port: 6379,
		PasswordEnv: "KORE_REDIS_TEST_UNSET_PASSWORD"}
	_, perr := Call("redis.info", json.RawMessage(`{}`), []InstanceConfig{instance}, nil)
	if perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("缺失密码环境变量错误=%v，期望 E_INVALID", perr)
	}
	if strings.Contains(perr.Message, "KORE_REDIS_TEST_UNSET_PASSWORD=") {
		t.Fatalf("错误信息不应包含赋值形态: %q", perr.Message)
	}
}

// TestClientConnectionRefused 端口不可达返回 E_UPSTREAM。
func TestClientConnectionRefused(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	address := listener.Addr().(*net.TCPAddr)
	_ = listener.Close()
	instance := InstanceConfig{Name: "default", Host: "127.0.0.1", Port: address.Port}
	client := &Client{connectTimeout: time.Second, commandTimeout: time.Second}
	_, err = client.Info(instance)
	if err == nil {
		t.Fatal("不可达端口应返回错误")
	}
	if perr := classify(err); perr.Code != pluginapi.E_UPSTREAM {
		t.Fatalf("错误码=%s，期望 E_UPSTREAM", perr.Code)
	}
}

// TestNoSubprocessClient 静态断言执行面不引用 os/exec：
// 密码因此不可能进入任何子进程 argv，只会经 RESP AUTH 在 TCP 连接内传输。
func TestNoSubprocessClient(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取包目录失败: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", name, err)
		}
		if bytes.Contains(data, []byte(`"os/exec"`)) {
			t.Fatalf("%s 引用了 os/exec；密码可能进入子进程 argv，违反安全约束", name)
		}
	}
}
