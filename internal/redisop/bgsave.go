package redisop

import (
	"encoding/json"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callBgsave 处理 redis.bgsave：只发送 BGSAVE（后台异步落盘），不提供阻塞式 SAVE。
func callBgsave(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p infoParams
	if perr := decodeStrictParams(raw, &p); perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(instances, p.Instance)
	if perr != nil {
		return nil, perr
	}
	result, err := exec.Bgsave(instance)
	if err != nil {
		return nil, classify(err)
	}
	return encode(result)
}

// Bgsave 触发后台 RDB 落盘；已有落盘任务在进行时给出可操作提示（稍后重试）。
func (c *Client) Bgsave(instance InstanceConfig) (BgsaveResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return BgsaveResult{}, err
	}
	defer func() { _ = conn.Close() }()

	reply, err := conn.do("BGSAVE")
	if err != nil {
		message := errorText(err)
		if strings.Contains(strings.ToLower(message), "progress") {
			return BgsaveResult{}, opErrorf(pluginapi.E_UPSTREAM,
				"已有落盘任务正在进行中（%s）：请稍后用 redis.persistence 查看 rdb_bgsave_in_progress，完成后再试", message)
		}
		return BgsaveResult{}, upstreamError(err, "BGSAVE 失败")
	}
	message, _ := asString(reply)
	if message == "" {
		return BgsaveResult{}, opErrorf(pluginapi.E_UPSTREAM, "BGSAVE 返回空状态")
	}
	return BgsaveResult{Started: true, Message: message}, nil
}
