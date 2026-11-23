package redisop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callKeyInfo 处理 redis.keyInfo：单键元信息（类型/编码/TTL/内存），不读值。
func callKeyInfo(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p getParams // keyInfo 与 get 入参同形（instance + key）
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(instances, p.Instance)
	if perr != nil {
		return nil, perr
	}
	if perr := validateKey(p.Key); perr != nil {
		return nil, perr
	}
	result, err := exec.KeyInfo(instance, p.Key)
	if err != nil {
		return nil, classify(err)
	}
	return encode(result)
}

// KeyInfo 用一条 pipeline 取 TYPE/TTL/MEMORY USAGE/OBJECT ENCODING，全程 O(1)，
// 大键（MEMORY USAGE 大）同样快；不使用 DEBUG 系列命令。
// MEMORY USAGE / OBJECT ENCODING 需要 Redis >= 4.0：命令报错时降级为 0 / 空串。
func (c *Client) KeyInfo(instance InstanceConfig, key string) (KeyInfoResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return KeyInfoResult{}, err
	}
	defer func() { _ = conn.Close() }()

	for _, args := range [][]string{
		{"TYPE", key}, {"TTL", key}, {"MEMORY", "USAGE", key}, {"OBJECT", "ENCODING", key},
	} {
		if err := conn.send(args...); err != nil {
			return KeyInfoResult{}, upstreamError(err, args[0]+" 发送失败")
		}
	}
	if err := conn.flush(); err != nil {
		return KeyInfoResult{}, upstreamError(err, "keyInfo 发送失败")
	}
	typeReply, err := conn.readReply()
	if err != nil {
		return KeyInfoResult{}, upstreamError(err, "TYPE 读取失败")
	}
	keyType, _ := asString(typeReply)
	if keyType == "" {
		return KeyInfoResult{}, opErrorf(pluginapi.E_UPSTREAM, "TYPE 返回空类型")
	}
	if keyType == "none" {
		return KeyInfoResult{}, opErrorf(pluginapi.E_NOTFOUND, "key 不存在: %s", key)
	}
	ttl := int64(-2)
	ttlReply, err := conn.readReply()
	if err != nil {
		return KeyInfoResult{}, upstreamError(err, "TTL 读取失败")
	}
	if value, ok := asInt(ttlReply); ok {
		ttl = value
	}
	memory := int64(0)
	memoryReply, err := conn.readReply()
	if err != nil {
		if _, isRedisErr := err.(redisError); !isRedisErr {
			return KeyInfoResult{}, upstreamError(err, "MEMORY USAGE 读取失败")
		} // Redis < 4.0：MEMORY USAGE 不存在，降级 0
	} else if value, ok := asInt(memoryReply); ok {
		memory = value
	}
	encoding := ""
	encodingReply, err := conn.readReply()
	if err != nil {
		if _, isRedisErr := err.(redisError); !isRedisErr {
			return KeyInfoResult{}, upstreamError(err, "OBJECT ENCODING 读取失败")
		} // 老版本不支持，降级空串
	} else if text, ok := asString(encodingReply); ok {
		encoding = text
	}
	return KeyInfoResult{Key: key, Type: keyType, Encoding: encoding, TTLS: ttl, MemoryBytes: memory}, nil
}
