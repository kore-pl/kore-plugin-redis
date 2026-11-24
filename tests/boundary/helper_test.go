// Package boundary_test 是 redis 插件的边界用例（G-20 production 判据①）。
//
// 四类各 ≥1：异常输入 / 并发 / 权限不足 / 资源耗尽（分别见 input_test.go /
// concurrency_test.go / permission_test.go / resource_test.go）。
//
// 环境约束：本机不保证有 redis-server，因此需要执行面的用例统一打本进程内的最小
// RESP 服务端（内存 KV + 互斥锁，见 kvServer）：走真实 TCP 与真实 RESP 字节，
// 不跳过协议层，也不假装连过真实 Redis。真实 Redis 的全流程验收仍由 tests/e2e 负责。
package boundary_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pengtang1024/kore-plugin-redis/internal/redisop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// serverError 是 RESP 错误行（"-ERR ..."），用于模拟服务端拒绝。
type serverError string

// Error 实现 error 接口。
func (e serverError) Error() string { return string(e) }

// kvServer 是进程内的最小 RESP 服务端：内存 KV（按库隔离）+ 互斥锁。
//
// 它只实现边界用例需要的命令，但每条命令都按 Redis 的单命令原子语义串行化，
// 所以「并发 rename 只有一个赢家」这类断言是可判定的，不是掷骰子。
type kvServer struct {
	listener net.Listener
	mu       sync.Mutex
	dbs      map[int]map[string]string
	commands [][]string
	denyDB   bool // SELECT 返回 NOPERM（模拟 Redis 6 ACL 拒绝访问库）
	wg       sync.WaitGroup
}

// kvOption 在启动前调整服务端行为。
type kvOption func(*kvServer)

// withDenyDB 让 SELECT 返回 NOPERM（权限用例）。
func withDenyDB() kvOption { return func(s *kvServer) { s.denyDB = true } }

// startKVServer 启动边界用 RESP 服务端并注册清理。
func startKVServer(t *testing.T, opts ...kvOption) *kvServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动边界用 RESP 服务端失败: %v", err)
	}
	server := &kvServer{listener: listener, dbs: map[int]map[string]string{0: {}}}
	for _, opt := range opts {
		opt(server)
	}
	server.wg.Add(1)
	go server.accept()
	t.Cleanup(server.Close)
	return server
}

// Close 关闭监听并等待所有连接退出。
func (s *kvServer) Close() {
	_ = s.listener.Close()
	s.wg.Wait()
}

// port 返回监听端口。
func (s *kvServer) port() int {
	address, ok := s.listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0
	}
	return address.Port
}

// instance 返回指向本服务端的实例配置。
func (s *kvServer) instance(db int) redisop.InstanceConfig {
	return redisop.InstanceConfig{Name: "default", Host: "127.0.0.1", Port: s.port(), DB: db}
}

// instances 返回单实例列表（默认库 0）。
func (s *kvServer) instances() []redisop.InstanceConfig {
	return []redisop.InstanceConfig{s.instance(0)}
}

// keyCount 返回指定库的键数量。
func (s *kvServer) keyCount(db int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.dbs[db])
}

// hasKey 报告指定库是否存在该键。
func (s *kvServer) hasKey(db int, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.dbs[db][key]
	return ok
}

// commandLog 返回已收到命令的快照（每条命令是参数切片）。
func (s *kvServer) commandLog() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]string, len(s.commands))
	for index, command := range s.commands {
		out[index] = append([]string(nil), command...)
	}
	return out
}

// receivedSingleArg 报告服务端是否收到过 command，且该命令恰好只有一个参数等于 arg。
// 用于证明含空格/控制字符/RESP 帧片段的 key 是作为单个 bulk 参数传输的。
func (s *kvServer) receivedSingleArg(command, arg string) bool {
	for _, entry := range s.commandLog() {
		if len(entry) == 2 && strings.EqualFold(entry[0], command) && entry[1] == arg {
			return true
		}
	}
	return false
}

// accept 循环接收连接，每条连接一个会话 goroutine。
func (s *kvServer) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serve(conn)
		}()
	}
}

// serve 读取命令、执行并写回回复；db 是连接级状态（SELECT 切换）。
func (s *kvServer) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	db := 0
	for {
		args, err := readCommand(reader)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.commands = append(s.commands, args)
		s.mu.Unlock()
		reply, nextDB := s.handle(db, args)
		db = nextDB
		if err := writeReply(writer, reply); err != nil {
			return
		}
		if err := writer.Flush(); err != nil {
			return
		}
	}
}

// dbMap 返回指定库的键表，必要时创建；调用方必须持有 s.mu。
func (s *kvServer) dbMap(db int) map[string]string {
	keys, ok := s.dbs[db]
	if !ok {
		keys = map[string]string{}
		s.dbs[db] = keys
	}
	return keys
}

// handle 执行一条命令并返回 RESP 回复与连接当前库号。
func (s *kvServer) handle(db int, args []string) (any, int) {
	command := strings.ToUpper(args[0])
	switch command {
	case "PING":
		return "PONG", db
	case "AUTH":
		return "OK", db
	case "INFO":
		return "redis_version:7.2.4\r\nuptime_in_seconds:42\r\n", db
	case "SELECT":
		if len(args) != 2 {
			return serverError("ERR wrong number of arguments for 'select'"), db
		}
		if s.denyDB {
			return serverError("NOPERM this user has no permissions to access one of the keys used as arguments"), db
		}
		target, err := strconv.Atoi(args[1])
		if err != nil || target < 0 {
			return serverError("ERR invalid DB index"), db
		}
		return "OK", target
	case "TYPE":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.dbMap(db)[args[1]]; ok {
			return "string", db
		}
		return "none", db
	case "TTL":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.dbMap(db)[args[1]]; !ok {
			return int64(-2), db
		}
		return int64(-1), db
	case "GET":
		s.mu.Lock()
		defer s.mu.Unlock()
		value, ok := s.dbMap(db)[args[1]]
		if !ok {
			return nil, db
		}
		return value, db
	case "SET":
		if len(args) < 3 {
			return serverError("ERR wrong number of arguments for 'set'"), db
		}
		s.mu.Lock()
		s.dbMap(db)[args[1]] = args[2]
		s.mu.Unlock()
		return "OK", db
	case "DEL":
		s.mu.Lock()
		defer s.mu.Unlock()
		keys := s.dbMap(db)
		deleted := int64(0)
		for _, key := range args[1:] {
			if _, ok := keys[key]; ok {
				delete(keys, key)
				deleted++
			}
		}
		return deleted, db
	case "MEMORY":
		if len(args) < 3 || !strings.EqualFold(args[1], "USAGE") {
			return serverError("ERR unknown subcommand"), db
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		value, ok := s.dbMap(db)[args[2]]
		if !ok {
			return nil, db
		}
		return int64(len(value)), db
	case "RENAME", "RENAMENX":
		return s.rename(db, command, args)
	case "MOVE":
		return s.move(db, args)
	case "COPY":
		return s.copyKey(db, args)
	case "EXPIRE", "PERSIST", "BGSAVE":
		return int64(1), db
	default:
		return serverError("ERR unknown command '" + args[0] + "'"), db
	}
}

// rename 实现 RENAME / RENAMENX：源不存在报错，RENAMENX 在目标存在时返回 0。
func (s *kvServer) rename(db int, command string, args []string) (any, int) {
	if len(args) != 3 {
		return serverError("ERR wrong number of arguments for '" + strings.ToLower(command) + "'"), db
	}
	src, dst := args[1], args[2]
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := s.dbMap(db)
	value, ok := keys[src]
	if !ok {
		return serverError("ERR no such key"), db
	}
	if _, exists := keys[dst]; exists && command == "RENAMENX" {
		return int64(0), db
	}
	keys[dst] = value
	delete(keys, src)
	if command == "RENAMENX" {
		return int64(1), db
	}
	return "OK", db
}

// move 实现 MOVE：源不存在或目标库已有同名键时返回 0（不覆盖）。
func (s *kvServer) move(db int, args []string) (any, int) {
	if len(args) != 3 {
		return serverError("ERR wrong number of arguments for 'move'"), db
	}
	target, err := strconv.Atoi(args[2])
	if err != nil || target < 0 {
		return serverError("ERR DB index is out of range"), db
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	source := s.dbMap(db)
	value, ok := source[args[1]]
	if !ok {
		return int64(0), db
	}
	dest := s.dbMap(target)
	if _, exists := dest[args[1]]; exists {
		return int64(0), db
	}
	dest[args[1]] = value
	delete(source, args[1])
	return int64(1), db
}

// copyKey 实现 COPY key dest [DB n] [REPLACE]：默认不覆盖，源不存在返回 0。
func (s *kvServer) copyKey(db int, args []string) (any, int) {
	if len(args) < 3 {
		return serverError("ERR wrong number of arguments for 'copy'"), db
	}
	key, dest := args[1], args[2]
	destDB := db
	replace := false
	for index := 3; index < len(args); index++ {
		switch strings.ToUpper(args[index]) {
		case "DB":
			if index+1 >= len(args) {
				return serverError("ERR syntax error"), db
			}
			parsed, err := strconv.Atoi(args[index+1])
			if err != nil || parsed < 0 {
				return serverError("ERR DB index is out of range"), db
			}
			destDB = parsed
			index++
		case "REPLACE":
			replace = true
		default:
			return serverError("ERR syntax error"), db
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.dbMap(db)[key]
	if !ok {
		return int64(0), db
	}
	destKeys := s.dbMap(destDB)
	if _, exists := destKeys[dest]; exists && !replace {
		return int64(0), db
	}
	destKeys[dest] = value
	return int64(1), db
}

// readCommand 解析客户端发来的 RESP 命令数组。
func readCommand(reader *bufio.Reader) ([]string, error) {
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
		if err != nil || length < 0 || length > 8<<20 {
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

// writeReply 把回复编码为 RESP 字节。
func writeReply(writer *bufio.Writer, reply any) error {
	switch value := reply.(type) {
	case nil:
		_, err := writer.WriteString("$-1\r\n")
		return err
	case serverError:
		_, err := writer.WriteString("-" + string(value) + "\r\n")
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
		return writeReply(writer, items)
	case []any:
		if _, err := fmt.Fprintf(writer, "*%d\r\n", len(value)); err != nil {
			return err
		}
		for _, item := range value {
			if err := writeReply(writer, item); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("不支持的测试回复类型 %T", reply)
	}
}

// invoke 执行一次 redis 方法（真实 RESP 客户端，nil 时由 Call 构造）。
func invoke(instances []redisop.InstanceConfig, method string, params json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	return redisop.Call(method, params, instances, redisop.NewClient())
}

// mustInvoke 执行方法并在失败时终止用例。
func mustInvoke(t *testing.T, instances []redisop.InstanceConfig, method string, params json.RawMessage) json.RawMessage {
	t.Helper()
	raw, perr := invoke(instances, method, params)
	if perr != nil {
		t.Fatalf("%s 意外失败: %s: %s", method, perr.Code, perr.Message)
	}
	return raw
}

// jsonObject 把 map 编码为参数对象；测试数据含控制字符时也必须走 JSON 编码。
func jsonObject(t *testing.T, value map[string]any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("构造参数失败: %v", err)
	}
	return data
}
