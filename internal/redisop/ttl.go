package redisop

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callTTL 处理 redis.ttl：只读键的类型与剩余 TTL（-1 永久、0 即将过期、>0 剩余秒）。
func callTTL(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p ttlParams
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
	result, err := exec.TTL(instance, p.Key)
	if err != nil {
		return nil, classify(err)
	}
	return encode(result)
}

// callExpire 处理 redis.expire：只接受正整数 TTL（去 TTL 只能用 redis.persist）。
func callExpire(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p expireParams
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
	if p.TTLs == nil {
		return nil, invalid("缺少必填参数 ttl_s（正整数秒）；去掉 TTL 请用 redis.persist")
	}
	ttl, perr := validatePositiveTTL(*p.TTLs)
	if perr != nil {
		return nil, perr
	}
	result, err := exec.Expire(instance, p.Key, ttl)
	if err != nil {
		return nil, classify(err)
	}
	return encode(result)
}

// callPersist 处理 redis.persist：去掉键的过期时间，使键永久保留。
func callPersist(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p persistParams
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
	result, err := exec.Persist(instance, p.Key)
	if err != nil {
		return nil, classify(err)
	}
	return encode(result)
}

// validatePositiveTTL 校验 redis.expire 的 ttl_s：0 与负数都拒绝并给出替代做法。
func validatePositiveTTL(ttl int64) (int64, *pluginapi.Error) {
	if ttl <= 0 {
		return 0, invalid(fmt.Sprintf(
			"ttl_s=%d 非法：EXPIRE 只接受正整数秒；永不过期请用 redis.persist，查询剩余时间请用 redis.ttl", ttl))
	}
	if ttl > maxTTLSeconds {
		return 0, invalid(fmt.Sprintf("ttl_s=%d 超过上限 %d 秒（10 年）", ttl, maxTTLSeconds))
	}
	return ttl, nil
}

// TTL 读取键的 TYPE 与 TTL：-2（键不存在）映射 E_NOTFOUND，-1 标记永久。
func (c *Client) TTL(instance InstanceConfig, key string) (TTLResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return TTLResult{}, err
	}
	defer func() { _ = conn.Close() }()

	for _, args := range [][]string{{"TYPE", key}, {"TTL", key}} {
		if err := conn.send(args...); err != nil {
			return TTLResult{}, upstreamError(err, args[0]+" 发送失败")
		}
	}
	if err := conn.flush(); err != nil {
		return TTLResult{}, upstreamError(err, "TYPE/TTL 发送失败")
	}
	typeReply, err := conn.readReply()
	if err != nil {
		return TTLResult{}, upstreamError(err, "TYPE 读取失败")
	}
	keyType, _ := asString(typeReply)
	if keyType == "" {
		return TTLResult{}, opErrorf(pluginapi.E_UPSTREAM, "TYPE 返回空类型")
	}
	if keyType == "none" {
		return TTLResult{}, opErrorf(pluginapi.E_NOTFOUND, "key 不存在: %s", key)
	}
	ttlReply, err := conn.readReply()
	if err != nil {
		return TTLResult{}, upstreamError(err, "TTL 读取失败")
	}
	ttl, ok := asInt(ttlReply)
	if !ok {
		return TTLResult{}, opErrorf(pluginapi.E_UPSTREAM, "TTL 返回非整数")
	}
	if ttl == -2 {
		// TYPE 与 TTL 之间键被删除（并发竞态）：按不存在返回，不编造 TTL。
		return TTLResult{}, opErrorf(pluginapi.E_NOTFOUND, "key 不存在: %s", key)
	}
	return TTLResult{
		Key:        key,
		Type:       keyType,
		TTLs:       ttl,
		Persistent: ttl == -1,
		ExpiresAt:  ttlExpiresAt(ttl),
	}, nil
}

// Expire 设置键的过期时间；键不存在（EXPIRE 返回 0）映射 E_NOTFOUND。
func (c *Client) Expire(instance InstanceConfig, key string, ttlS int64) (ExpireResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return ExpireResult{}, err
	}
	defer func() { _ = conn.Close() }()

	reply, err := conn.do("EXPIRE", key, strconv.FormatInt(ttlS, 10))
	if err != nil {
		return ExpireResult{}, upstreamError(err, "EXPIRE 失败")
	}
	set, ok := asInt(reply)
	if !ok {
		return ExpireResult{}, opErrorf(pluginapi.E_UPSTREAM, "EXPIRE 返回非整数")
	}
	if set != 1 {
		return ExpireResult{}, opErrorf(pluginapi.E_NOTFOUND, "key 不存在: %s", key)
	}
	ttl, err := readTTL(conn, key)
	if err != nil {
		return ExpireResult{}, err
	}
	if ttl < 0 {
		return ExpireResult{}, opErrorf(pluginapi.E_UPSTREAM, "EXPIRE 成功后 TTL=%d，不符合预期", ttl)
	}
	return ExpireResult{Key: key, TTLs: ttl}, nil
}

// Persist 去掉键的过期时间；键不存在映射 E_NOTFOUND，本来就没有 TTL 时 Persisted=false。
func (c *Client) Persist(instance InstanceConfig, key string) (PersistResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return PersistResult{}, err
	}
	defer func() { _ = conn.Close() }()

	reply, err := conn.do("PERSIST", key)
	if err != nil {
		return PersistResult{}, upstreamError(err, "PERSIST 失败")
	}
	removed, ok := asInt(reply)
	if !ok {
		return PersistResult{}, opErrorf(pluginapi.E_UPSTREAM, "PERSIST 返回非整数")
	}
	// PERSIST=0 有两义：键不存在、或本来就没有 TTL；用 TYPE 区分，避免把不存在的键说成「已永久」。
	typeReply, err := conn.do("TYPE", key)
	if err != nil {
		return PersistResult{}, upstreamError(err, "TYPE 失败")
	}
	keyType, _ := asString(typeReply)
	if keyType == "none" {
		return PersistResult{}, opErrorf(pluginapi.E_NOTFOUND, "key 不存在: %s", key)
	}
	ttl, err := readTTL(conn, key)
	if err != nil {
		return PersistResult{}, err
	}
	return PersistResult{Key: key, Persisted: removed == 1, TTLs: ttl}, nil
}
