package redisop

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callSlowlog 处理 redis.slowlog：解析实例/上限参数并返回慢查询记录。
func callSlowlog(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p slowlogParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(instances, p.Instance)
	if perr != nil {
		return nil, perr
	}
	limit := p.Limit
	if limit <= 0 {
		limit = defaultSlowlogLimit
	}
	if limit > maxSlowlogLimit {
		return nil, invalid("limit 超过上限 " + strconv.Itoa(maxSlowlogLimit))
	}
	result, err := exec.Slowlog(instance, limit)
	if err != nil {
		return nil, classify(err)
	}
	if result.Entries == nil {
		result.Entries = []SlowlogEntry{}
	}
	result.Count = len(result.Entries)
	return encode(result)
}

// Slowlog 读取 SLOWLOG GET <limit>；逐条解析，形状异常的条目跳过而不失败整次调用。
func (c *Client) Slowlog(instance InstanceConfig, limit int) (SlowlogResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return SlowlogResult{}, err
	}
	defer func() { _ = conn.Close() }()

	reply, err := conn.do("SLOWLOG", "GET", strconv.Itoa(limit))
	if err != nil {
		return SlowlogResult{}, upstreamError(err, "SLOWLOG GET 失败")
	}
	items, ok := asArray(reply)
	if !ok {
		return SlowlogResult{}, opErrorf(pluginapi.E_UPSTREAM, "SLOWLOG GET 返回非数组")
	}
	entries := make([]SlowlogEntry, 0, len(items))
	for _, item := range items {
		entry, err := parseSlowlogEntry(item)
		if err != nil {
			continue // 单条形状异常不拖垮整次查询
		}
		entries = append(entries, entry)
	}
	return SlowlogResult{Entries: entries, Count: len(entries)}, nil
}

// parseSlowlogEntry 解析单条慢查询记录。Redis 4+ 的条目是
// [id, unix_ts, duration_us, argv[], 客户端地址, 客户端名, …]，7.x 尾部字段更多；
// 这里只取前 4 个必需元素，其余忽略，保持对外形状稳定。
func parseSlowlogEntry(item any) (SlowlogEntry, error) {
	fields, ok := asArray(item)
	if !ok || len(fields) < 4 {
		return SlowlogEntry{}, opErrorf(pluginapi.E_UPSTREAM, "SLOWLOG 条目形状非法")
	}
	id, ok1 := asInt(fields[0])
	ts, ok2 := asInt(fields[1])
	duration, ok3 := asInt(fields[2])
	argv, ok4 := asArray(fields[3])
	if !ok1 || !ok2 || !ok3 || !ok4 || len(argv) == 0 {
		return SlowlogEntry{}, opErrorf(pluginapi.E_UPSTREAM, "SLOWLOG 条目字段非法")
	}
	command, _ := asString(argv[0])
	if command == "" {
		return SlowlogEntry{}, opErrorf(pluginapi.E_UPSTREAM, "SLOWLOG 条目命令为空")
	}
	entry := SlowlogEntry{ID: id, TS: ts, DurationUS: duration, Command: command}
	summary := make([]string, 0, len(argv)-1)
	for _, arg := range argv[1:] {
		text, _ := asString(arg)
		summary = append(summary, truncateSlowlogArg(text))
	}
	if len(summary) > maxSlowlogArgs {
		summary = append(summary[:maxSlowlogArgs], "…(+"+strconv.Itoa(len(summary)-maxSlowlogArgs)+")")
	}
	entry.ArgsSummary = strings.Join(summary, " ")
	return entry, nil
}

// truncateSlowlogArg 把单个参数截断到 maxSlowlogArgRunes 字符并加省略号，防大值刷屏。
func truncateSlowlogArg(arg string) string {
	runes := []rune(arg)
	if len(runes) <= maxSlowlogArgRunes {
		return arg
	}
	return string(runes[:maxSlowlogArgRunes]) + "…"
}
