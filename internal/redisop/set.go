package redisop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// setTypes 是 redis.set 支持的类型枚举；空串表示沿用键既有类型。
var setTypes = map[string]struct{}{
	"string": {}, "hash": {}, "list": {}, "set": {}, "zset": {},
}

// callSet 处理 redis.set：严格解码参数 → 解析实例 → 校验键/类型/TTL → 执行面写入。
// 这是「写」方法，命令由 Client.Set 按类型拼成固定形状（SET/HSET/RPUSH/SADD/ZADD），
// 不存在把用户输入当命令名的路径。
func callSet(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p setParams
	if perr := decodeStrictParams(raw, &p); perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(instances, p.Instance)
	if perr != nil {
		return nil, perr
	}
	if perr := validateKey(p.Key); perr != nil {
		return nil, perr
	}
	if p.Type != "" {
		if _, ok := setTypes[p.Type]; !ok {
			return nil, invalid("type=" + p.Type + " 非法：只允许 string / hash / list / set / zset，或省略以沿用键既有类型")
		}
	}
	ttl, perr := validateWriteTTL(p.TTLs, "ttl_s")
	if perr != nil {
		return nil, perr
	}
	if len(strings.TrimSpace(string(p.Value))) == 0 || string(p.Value) == "null" {
		return nil, invalid("缺少 value：string 用字符串、hash 用 [{field,value}]、list/set 用字符串数组、zset 用 [{member,score}]")
	}
	// 类型显式给出时先在本地校验值形状/上限：错误不依赖是否连得上 Redis。
	// 类型省略时必须先问服务端 TYPE，解析顺延到 Client.Set。
	if p.Type != "" {
		if _, opErr := parseSetPayload(p.Type, p.Value); opErr != nil {
			return nil, toProtocolError(opErr, "redis.set")
		}
	}
	result, err := exec.Set(instance, SetRequest{Key: p.Key, Type: p.Type, TTLs: ttl, Value: p.Value})
	if err != nil {
		return nil, classify(err)
	}
	return encode(result)
}

// validateWriteTTL 校验 redis.set 的 ttl_s：省略=不改 TTL；>0=同时设置过期；
// 0 与负数都拒绝——「去 TTL」语义只有 redis.persist 一个入口，避免 0 的二义性。
func validateWriteTTL(ttl *int64, field string) (int64, *pluginapi.Error) {
	if ttl == nil {
		return 0, nil
	}
	value := *ttl
	if value == 0 {
		return 0, invalid(field + "=0 无意义：不改 TTL 请省略该字段；去掉 TTL 请用 redis.persist")
	}
	if value < 0 {
		return 0, invalid(fmt.Sprintf("%s=%d 非法：TTL 必须为正整数；去掉 TTL 请用 redis.persist", field, value))
	}
	if value > maxTTLSeconds {
		return 0, invalid(fmt.Sprintf("%s=%d 超过上限 %d 秒（10 年）", field, value, maxTTLSeconds))
	}
	return value, nil
}

// Set 按类型写入键值：类型为空时沿用键既有类型（键不存在则要求显式类型）。
// 现有类型与请求类型不一致时拒绝，避免 WRONGTYPE 在写入后才暴露。
func (c *Client) Set(instance InstanceConfig, request SetRequest) (SetResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return SetResult{}, err
	}
	defer func() { _ = conn.Close() }()

	keyType, opErr := resolveSetType(conn, request.Key, request.Type)
	if opErr != nil {
		return SetResult{}, opErr
	}
	payload, opErr := parseSetPayload(keyType, request.Value)
	if opErr != nil {
		return SetResult{}, opErr
	}
	args, opErr := buildSetCommand(request.Key, payload, request.TTLs)
	if opErr != nil {
		return SetResult{}, opErr
	}
	reply, err := conn.do(args...)
	if err != nil {
		return SetResult{}, upstreamError(err, strings.ToUpper(args[0])+" 失败")
	}
	written := int64(1)
	if keyType == "string" {
		if text, ok := asString(reply); !ok || text != "OK" {
			return SetResult{}, opErrorf(pluginapi.E_UPSTREAM, "SET 返回非 OK: %v", reply)
		}
	} else {
		// HSET/SADD/ZADD 返回新增元素数、RPUSH 返回写入后长度：如实透传。
		count, ok := asInt(reply)
		if !ok {
			return SetResult{}, opErrorf(pluginapi.E_UPSTREAM, "%s 返回非整数", strings.ToUpper(args[0]))
		}
		written = count
		if request.TTLs > 0 {
			expireReply, err := conn.do("EXPIRE", request.Key, strconv.FormatInt(request.TTLs, 10))
			if err != nil {
				return SetResult{}, upstreamError(err, "EXPIRE 失败")
			}
			if value, ok := asInt(expireReply); !ok || value != 1 {
				return SetResult{}, opErrorf(pluginapi.E_UPSTREAM, "EXPIRE 未生效（键可能在写入后立即过期）")
			}
		}
	}
	ttl, err := readTTL(conn, request.Key)
	if err != nil {
		return SetResult{}, err
	}
	return SetResult{Key: request.Key, Type: keyType, Written: written, TTLs: ttl}, nil
}

// resolveSetType 决定本次写入类型：显式类型须与既有类型一致；省略类型时沿用既有类型。
func resolveSetType(conn *respConn, key, requested string) (string, error) {
	reply, err := conn.do("TYPE", key)
	if err != nil {
		return "", upstreamError(err, "TYPE 失败")
	}
	existing, _ := asString(reply)
	if existing == "" {
		return "", opErrorf(pluginapi.E_UPSTREAM, "TYPE 返回空类型")
	}
	if requested == "" {
		if existing == "none" {
			return "", opErrorf(pluginapi.E_INVALID,
				"键 %q 不存在且未指定 type：请显式指定 string / hash / list / set / zset", key)
		}
		if _, ok := setTypes[existing]; !ok {
			return "", opErrorf(pluginapi.E_UNSUPPORTED, "键 %q 的类型 %s 不支持写入", key, existing)
		}
		return existing, nil
	}
	if existing != "none" && existing != requested {
		return "", opErrorf(pluginapi.E_INVALID,
			"键 %q 现有类型 %s 与请求类型 %s 不一致：请改用匹配类型，或先删除该键", key, existing, requested)
	}
	return requested, nil
}

// SetPayload 是 redis.set 解析后的写入载荷：字段由类型定形，不出现 any / map[string]any。
type SetPayload struct {
	Type    string
	Text    string
	Hash    []HashField
	List    []string
	Members []string
	ZSet    []ZSetWrite
}

// zsetWriteRaw 是 zset 写入的原始条目：score 接受 JSON 字符串或数字，解析期临时承载。
type zsetWriteRaw struct {
	Member string          `json:"member"`
	Score  json.RawMessage `json:"score"`
}

// parseSetPayload 按类型严格解析 value，并执行元素数/单值字节/总字节上限校验。
func parseSetPayload(keyType string, raw json.RawMessage) (SetPayload, error) {
	payload := SetPayload{Type: keyType}
	switch keyType {
	case "string":
		var text string
		if err := strictJSON(raw, &text); err != nil {
			return SetPayload{}, opErrorf(pluginapi.E_INVALID, "string 类型的 value 必须是 JSON 字符串: %v", err)
		}
		if len(text) > maxWriteValueBytes {
			return SetPayload{}, opErrorf(pluginapi.E_INVALID,
				"value 超过单值上限 %d 字节", maxWriteValueBytes)
		}
		payload.Text = text
	case "hash":
		var fields []HashField
		if err := strictJSON(raw, &fields); err != nil {
			return SetPayload{}, opErrorf(pluginapi.E_INVALID,
				"hash 类型的 value 必须是 [{field,value}] 数组: %v", err)
		}
		if err := checkWriteItems(len(fields)); err != nil {
			return SetPayload{}, err
		}
		total := 0
		for index, field := range fields {
			if strings.TrimSpace(field.Field) == "" {
				return SetPayload{}, opErrorf(pluginapi.E_INVALID, "第 %d 个 hash 字段的 field 为空", index+1)
			}
			if err := checkWriteText(field.Field, "hash 字段名"); err != nil {
				return SetPayload{}, err
			}
			if err := checkWriteText(field.Value, "hash 字段值"); err != nil {
				return SetPayload{}, err
			}
			total += len(field.Field) + len(field.Value)
		}
		if err := checkWriteTotal(total); err != nil {
			return SetPayload{}, err
		}
		payload.Hash = fields
	case "list", "set":
		var values []string
		if err := strictJSON(raw, &values); err != nil {
			return SetPayload{}, opErrorf(pluginapi.E_INVALID,
				"%s 类型的 value 必须是字符串数组: %v", keyType, err)
		}
		if err := checkWriteItems(len(values)); err != nil {
			return SetPayload{}, err
		}
		total := 0
		for index, value := range values {
			if err := checkWriteText(value, fmt.Sprintf("第 %d 个元素", index+1)); err != nil {
				return SetPayload{}, err
			}
			total += len(value)
		}
		if err := checkWriteTotal(total); err != nil {
			return SetPayload{}, err
		}
		if keyType == "list" {
			payload.List = values
		} else {
			payload.Members = values
		}
	case "zset":
		var entries []zsetWriteRaw
		if err := strictJSON(raw, &entries); err != nil {
			return SetPayload{}, opErrorf(pluginapi.E_INVALID,
				"zset 类型的 value 必须是 [{member,score}] 数组: %v", err)
		}
		if err := checkWriteItems(len(entries)); err != nil {
			return SetPayload{}, err
		}
		total := 0
		parsed := make([]ZSetWrite, 0, len(entries))
		for index, entry := range entries {
			if strings.TrimSpace(entry.Member) == "" {
				return SetPayload{}, opErrorf(pluginapi.E_INVALID, "第 %d 个 zset 成员的 member 为空", index+1)
			}
			if err := checkWriteText(entry.Member, "zset 成员"); err != nil {
				return SetPayload{}, err
			}
			score, err := parseZSetScore(entry.Score)
			if err != nil {
				return SetPayload{}, opErrorf(pluginapi.E_INVALID, "第 %d 个 zset 成员的 score 非法: %v", index+1, err)
			}
			total += len(entry.Member) + len(score)
			parsed = append(parsed, ZSetWrite{Member: entry.Member, Score: score})
		}
		if err := checkWriteTotal(total); err != nil {
			return SetPayload{}, err
		}
		payload.ZSet = parsed
	default:
		return SetPayload{}, opErrorf(pluginapi.E_UNSUPPORTED, "键类型 %s 不支持写入", keyType)
	}
	return payload, nil
}

// parseZSetScore 接受 JSON 字符串或数字，返回 Redis 可用的分数文本（如 "1.5"、"inf"）。
func parseZSetScore(raw json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", fmt.Errorf("score 不得为空")
	}
	if strings.HasPrefix(trimmed, `"`) {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", err
		}
		if _, err := strconv.ParseFloat(text, 64); err != nil {
			return "", fmt.Errorf("%q 不是数值", text)
		}
		return text, nil
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return "", fmt.Errorf("%q 不是数值", trimmed)
	}
	return strconv.FormatFloat(value, 'g', -1, 64), nil
}

// buildSetCommand 按类型拼装固定形状的写入命令；ttl_s>0 时 string 用 SET ... EX（原子），
// 集合类型由 Set 在写入后追加 EXPIRE。
func buildSetCommand(key string, payload SetPayload, ttl int64) ([]string, error) {
	switch payload.Type {
	case "string":
		args := []string{"SET", key, payload.Text}
		if ttl > 0 {
			args = append(args, "EX", strconv.FormatInt(ttl, 10))
		}
		return args, nil
	case "hash":
		if len(payload.Hash) == 0 {
			return nil, opErrorf(pluginapi.E_INVALID, "hash 写入至少需要一个 {field,value}")
		}
		args := make([]string, 0, 2+2*len(payload.Hash))
		args = append(args, "HSET", key)
		for _, field := range payload.Hash {
			args = append(args, field.Field, field.Value)
		}
		return args, nil
	case "list":
		if len(payload.List) == 0 {
			return nil, opErrorf(pluginapi.E_INVALID, "list 写入至少需要一个元素")
		}
		return append([]string{"RPUSH", key}, payload.List...), nil
	case "set":
		if len(payload.Members) == 0 {
			return nil, opErrorf(pluginapi.E_INVALID, "set 写入至少需要一个成员")
		}
		return append([]string{"SADD", key}, payload.Members...), nil
	case "zset":
		if len(payload.ZSet) == 0 {
			return nil, opErrorf(pluginapi.E_INVALID, "zset 写入至少需要一个 {member,score}")
		}
		args := make([]string, 0, 2+2*len(payload.ZSet))
		args = append(args, "ZADD", key)
		for _, entry := range payload.ZSet {
			args = append(args, entry.Score, entry.Member)
		}
		return args, nil
	default:
		return nil, opErrorf(pluginapi.E_UNSUPPORTED, "键类型 %s 不支持写入", payload.Type)
	}
}

// checkWriteItems 校验单次写入的元素个数上限。
func checkWriteItems(count int) error {
	if count == 0 {
		return opErrorf(pluginapi.E_INVALID, "value 不得为空")
	}
	if count > maxWriteItems {
		return opErrorf(pluginapi.E_INVALID, "value 元素数 %d 超过上限 %d", count, maxWriteItems)
	}
	return nil
}

// checkWriteText 校验单个值的字节上限与 NUL。
func checkWriteText(text, what string) error {
	if strings.ContainsRune(text, 0) {
		return opErrorf(pluginapi.E_INVALID, "%s含 NUL 字节", what)
	}
	if len(text) > maxWriteValueBytes {
		return opErrorf(pluginapi.E_INVALID, "%s超过单值上限 %d 字节", what, maxWriteValueBytes)
	}
	return nil
}

// checkWriteTotal 校验单次写入载荷的总字节上限。
func checkWriteTotal(total int) error {
	if total > maxWriteTotalBytes {
		return opErrorf(pluginapi.E_INVALID, "value 总字节 %d 超过上限 %d", total, maxWriteTotalBytes)
	}
	return nil
}

// strictJSON 解码单段 JSON 并拒绝尾随内容。
func strictJSON(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return fmt.Errorf("JSON 之后存在多余内容")
	}
	return nil
}

// readTTL 读取键的 TTL（-2 缺键也原样返回，由调用方决定语义）。
func readTTL(conn *respConn, key string) (int64, error) {
	reply, err := conn.do("TTL", key)
	if err != nil {
		return 0, upstreamError(err, "TTL 失败")
	}
	ttl, ok := asInt(reply)
	if !ok {
		return 0, opErrorf(pluginapi.E_UPSTREAM, "TTL 返回非整数")
	}
	return ttl, nil
}

// ttlExpiresAt 把 TTL 换算成绝对过期时间（Unix 秒）；-1/0 返回 0（无绝对时间或即将过期）。
func ttlExpiresAt(ttl int64) int64 {
	if ttl <= 0 {
		return 0
	}
	return time.Now().Unix() + ttl
}

// toProtocolError 把执行面的 *OpError 转成协议错误；非 OpError 归 E_INTERNAL。
func toProtocolError(err error, method string) *pluginapi.Error {
	var opErr *OpError
	if errors.As(err, &opErr) {
		return &pluginapi.Error{Code: opErr.Code, Message: opErr.Message}
	}
	return internalErr("序列化 " + method + " 参数失败: " + err.Error())
}
