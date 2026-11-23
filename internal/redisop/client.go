package redisop

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// Client 是 RESP 执行面：每次方法调用建立短连接，不引入连接池与后台 goroutine。
type Client struct {
	connectTimeout time.Duration
	commandTimeout time.Duration
}

// NewClient 用默认超时构造执行器。
func NewClient() *Client {
	return &Client{connectTimeout: connectTimeout, commandTimeout: commandTimeout}
}

// Instances 并行为每个实例做 PING + INFO server 探测；单实例失败只写该实例的
// error 字段，不影响其它实例。固定 4 个 worker，避免实例多时 goroutine 膨胀。
func (c *Client) Instances(instances []InstanceConfig) ([]InstanceStatus, error) {
	if len(instances) == 0 {
		return []InstanceStatus{}, nil
	}
	statuses := make([]InstanceStatus, len(instances))
	workers := 4
	if len(instances) < workers {
		workers = len(instances)
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for index := range jobs {
				statuses[index] = c.probe(instances[index])
			}
		}()
	}
	for index := range instances {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	return statuses, nil
}

// probe 对单实例执行 PING + INFO server，返回可达性、版本与运行时长。
func (c *Client) probe(instance InstanceConfig) InstanceStatus {
	status := InstanceStatus{
		Name:             instance.Name,
		Host:             instance.Host,
		Port:             instance.Port,
		CredentialSource: credentialSource(instance),
	}
	conn, err := c.dial(instance)
	if err != nil {
		status.Error = errorText(err)
		return status
	}
	defer func() { _ = conn.Close() }()

	reply, err := conn.do("PING")
	if err != nil {
		status.Error = errorText(err)
		return status
	}
	if text, ok := asString(reply); !ok || text != "PONG" {
		status.Error = "PING 返回非 PONG"
		return status
	}
	status.Reachable = true

	raw, err := conn.do("INFO", "server")
	if err != nil {
		status.Error = "PING 可达，但 INFO server 失败: " + errorText(err)
		return status
	}
	infoText, ok := asString(raw)
	if !ok {
		status.Error = "INFO server 返回非字符串"
		return status
	}
	fields := parseInfoFields(infoText)
	status.Version = fields["redis_version"]
	status.UptimeS = safeAtoi(fields["uptime_in_seconds"])
	if status.Version == "" {
		status.Error = "INFO server 未返回 redis_version"
	}
	return status
}

// Info 读取并解析 INFO 全量输出为显式结构。
func (c *Client) Info(instance InstanceConfig) (InfoResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return InfoResult{}, err
	}
	defer func() { _ = conn.Close() }()

	reply, err := conn.do("INFO")
	if err != nil {
		return InfoResult{}, upstreamError(err, "INFO 失败")
	}
	raw, ok := asString(reply)
	if !ok {
		return InfoResult{}, opErrorf(pluginapi.E_UPSTREAM, "INFO 返回非字符串")
	}
	result := buildInfoResult(instance.Name, raw)
	if result.Server.Version == "" {
		return InfoResult{}, opErrorf(pluginapi.E_UPSTREAM, "INFO 未返回 redis_version")
	}
	return result, nil
}

// Keys 用 SCAN 游标遍历键空间（不使用阻塞式 KEYS），再对命中键批量取 TYPE/TTL。
func (c *Client) Keys(instance InstanceConfig, pattern string, limit int) (KeysResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return KeysResult{}, err
	}
	defer func() { _ = conn.Close() }()

	countHint := limit
	if countHint < 100 {
		countHint = 100
	}
	if countHint > 1000 {
		countHint = 1000
	}

	cursor := "0"
	seen := make(map[string]struct{}, limit)
	keys := make([]string, 0, limit)
	truncated := false
	for {
		reply, err := conn.do("SCAN", cursor, "MATCH", pattern, "COUNT", fmt.Sprintf("%d", countHint))
		if err != nil {
			return KeysResult{}, upstreamError(err, "SCAN 失败")
		}
		batchCursor, batch, err := parseScanReply(reply)
		if err != nil {
			return KeysResult{}, err
		}
		stopped := false
		for index, key := range batch {
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
			if len(keys) >= limit {
				// 同一批里还有未处理的键，或游标未归零，都说明还有剩余。
				truncated = index < len(batch)-1 || batchCursor != "0"
				stopped = true
				break
			}
		}
		cursor = batchCursor
		if stopped || cursor == "0" {
			break
		}
	}

	entries, err := c.keyEntries(conn, keys)
	if err != nil {
		return KeysResult{}, err
	}
	return KeysResult{Keys: entries, Scanned: len(entries), Truncated: truncated}, nil
}

// parseScanReply 解析 SCAN 的 [cursor, [keys...]] 回复。
func parseScanReply(reply any) (string, []string, error) {
	items, ok := asArray(reply)
	if !ok || len(items) != 2 {
		return "", nil, opErrorf(pluginapi.E_UPSTREAM, "SCAN 回复形状非法")
	}
	cursor, ok := asString(items[0])
	if !ok {
		return "", nil, opErrorf(pluginapi.E_UPSTREAM, "SCAN 游标不是字符串")
	}
	rawKeys, ok := asArray(items[1])
	if !ok {
		return "", nil, opErrorf(pluginapi.E_UPSTREAM, "SCAN 键列表形状非法")
	}
	keys := make([]string, 0, len(rawKeys))
	for _, item := range rawKeys {
		key, ok := asString(item)
		if !ok {
			return "", nil, opErrorf(pluginapi.E_UPSTREAM, "SCAN 键不是字符串")
		}
		keys = append(keys, key)
	}
	return cursor, keys, nil
}

// keyEntries 用 pipeline 批量取 TYPE/TTL，避免逐键两次往返。
func (c *Client) keyEntries(conn *respConn, keys []string) ([]KeyEntry, error) {
	entries := make([]KeyEntry, 0, len(keys))
	const chunk = 100
	for start := 0; start < len(keys); start += chunk {
		end := start + chunk
		if end > len(keys) {
			end = len(keys)
		}
		for _, key := range keys[start:end] {
			if err := conn.send("TYPE", key); err != nil {
				return nil, upstreamError(err, "TYPE 发送失败")
			}
			if err := conn.send("TTL", key); err != nil {
				return nil, upstreamError(err, "TTL 发送失败")
			}
		}
		if err := conn.flush(); err != nil {
			return nil, upstreamError(err, "TYPE/TTL 发送失败")
		}
		for _, key := range keys[start:end] {
			typeReply, err := conn.readReply()
			if err != nil {
				return nil, upstreamError(err, "TYPE 读取失败")
			}
			ttlReply, err := conn.readReply()
			if err != nil {
				return nil, upstreamError(err, "TTL 读取失败")
			}
			keyType, _ := asString(typeReply)
			ttl, _ := asInt(ttlReply)
			entries = append(entries, KeyEntry{Key: key, Type: keyType, TTLS: ttl})
		}
	}
	return entries, nil
}

// Get 按 TYPE 分派读取键值，返回类型、TTL 与 JSON 载荷。
func (c *Client) Get(instance InstanceConfig, key string) (GetResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return GetResult{}, err
	}
	defer func() { _ = conn.Close() }()

	typeReply, err := conn.do("TYPE", key)
	if err != nil {
		return GetResult{}, upstreamError(err, "TYPE 失败")
	}
	keyType, _ := asString(typeReply)
	if keyType == "" {
		return GetResult{}, opErrorf(pluginapi.E_UPSTREAM, "TYPE 返回空类型")
	}
	if keyType == "none" {
		return GetResult{}, opErrorf(pluginapi.E_NOTFOUND, "key 不存在: %s", key)
	}
	ttl := int64(-2)
	if ttlReply, err := conn.do("TTL", key); err == nil {
		if value, ok := asInt(ttlReply); ok {
			ttl = value
		}
	}
	result := GetResult{Key: key, Type: keyType, TTLS: ttl}
	value, truncated, err := c.readValue(conn, key, keyType)
	if err != nil {
		return GetResult{}, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return GetResult{}, opErrorf(pluginapi.E_INTERNAL, "序列化 %s 的取值失败: %v", keyType, err)
	}
	result.Value = data
	result.Truncated = truncated
	return result, nil
}

// readValue 按 Redis 类型读取值；集合类最多取 maxValueEntries 条并标记截断。
func (c *Client) readValue(conn *respConn, key, keyType string) (any, bool, error) {
	switch keyType {
	case "string":
		reply, err := conn.do("GET", key)
		if err != nil {
			return nil, false, upstreamError(err, "GET 失败")
		}
		if reply == nil {
			return nil, false, nil
		}
		text, ok := asString(reply)
		if !ok {
			return nil, false, opErrorf(pluginapi.E_UPSTREAM, "GET 返回非字符串")
		}
		return text, false, nil
	case "hash":
		reply, err := conn.do("HGETALL", key)
		if err != nil {
			return nil, false, upstreamError(err, "HGETALL 失败")
		}
		items, ok := asArray(reply)
		if !ok {
			return nil, false, opErrorf(pluginapi.E_UPSTREAM, "HGETALL 返回非数组")
		}
		pairs := len(items) / 2
		truncated := pairs > maxValueEntries
		if truncated {
			pairs = maxValueEntries
		}
		hash := make(map[string]string, pairs)
		for index := 0; index < pairs; index++ {
			field, fieldOK := asString(items[index*2])
			value, valueOK := asString(items[index*2+1])
			if !fieldOK || !valueOK {
				return nil, false, opErrorf(pluginapi.E_UPSTREAM, "HGETALL 字段不是字符串")
			}
			hash[field] = value
		}
		return hash, truncated, nil
	case "list":
		reply, err := conn.do("LRANGE", key, "0", fmt.Sprintf("%d", maxValueEntries))
		if err != nil {
			return nil, false, upstreamError(err, "LRANGE 失败")
		}
		values, truncated, err := stringArray(reply)
		if err != nil {
			return nil, false, err
		}
		return values, truncated, nil
	case "set":
		reply, err := conn.do("SMEMBERS", key)
		if err != nil {
			return nil, false, upstreamError(err, "SMEMBERS 失败")
		}
		values, truncated, err := stringArray(reply)
		if err != nil {
			return nil, false, err
		}
		return values, truncated, nil
	case "zset":
		reply, err := conn.do("ZRANGE", key, "0", fmt.Sprintf("%d", maxValueEntries), "WITHSCORES")
		if err != nil {
			return nil, false, upstreamError(err, "ZRANGE 失败")
		}
		items, ok := asArray(reply)
		if !ok {
			return nil, false, opErrorf(pluginapi.E_UPSTREAM, "ZRANGE 返回非数组")
		}
		pairs := len(items) / 2
		truncated := pairs > maxValueEntries
		if truncated {
			pairs = maxValueEntries
		}
		entries := make([]ZSetEntry, 0, pairs)
		for index := 0; index < pairs; index++ {
			member, memberOK := asString(items[index*2])
			score, scoreOK := asString(items[index*2+1])
			if !memberOK || !scoreOK {
				return nil, false, opErrorf(pluginapi.E_UPSTREAM, "ZRANGE 成员不是字符串")
			}
			entries = append(entries, ZSetEntry{Member: member, Score: score})
		}
		return entries, truncated, nil
	default:
		return nil, false, opErrorf(pluginapi.E_UNSUPPORTED, "暂不支持的类型: %s", keyType)
	}
}

// stringArray 把数组回复转成字符串切片，超过上限时截断。
func stringArray(reply any) ([]string, bool, error) {
	items, ok := asArray(reply)
	if !ok {
		return nil, false, opErrorf(pluginapi.E_UPSTREAM, "返回非数组")
	}
	truncated := len(items) > maxValueEntries
	if truncated {
		items = items[:maxValueEntries]
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := asString(item)
		if !ok {
			return nil, false, opErrorf(pluginapi.E_UPSTREAM, "数组元素不是字符串")
		}
		values = append(values, text)
	}
	return values, truncated, nil
}

// Delete 用一条 DEL 命令删除多个键，返回实际删除数量。
func (c *Client) Delete(instance InstanceConfig, keys []string) (DeleteResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return DeleteResult{}, err
	}
	defer func() { _ = conn.Close() }()

	args := make([]string, 0, len(keys)+1)
	args = append(args, "DEL")
	args = append(args, keys...)
	reply, err := conn.do(args...)
	if err != nil {
		return DeleteResult{}, upstreamError(err, "DEL 失败")
	}
	deleted, ok := asInt(reply)
	if !ok {
		return DeleteResult{}, opErrorf(pluginapi.E_UPSTREAM, "DEL 返回非整数")
	}
	return DeleteResult{Deleted: deleted, Keys: keys}, nil
}

// upstreamError 把底层错误映射为 E_UPSTREAM，并统一加上操作名。
func upstreamError(err error, action string) *OpError {
	var op *OpError
	if errors.As(err, &op) {
		return op
	}
	return opErrorf(pluginapi.E_UPSTREAM, "%s: %s", action, err.Error())
}

// parseInfoFields 把 INFO 的 key:value 行解析成映射，忽略 # 段标题与空行。
func parseInfoFields(raw string) map[string]string {
	fields := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields[key] = value
	}
	return fields
}

// 编译期断言：Client 实现 Executor。
var _ Executor = (*Client)(nil)
