package redisop

import (
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// validatePrefix 校验键扫描前缀：拒绝 NUL、控制字符、通配符与超长前缀。
// 这是纯字符串校验，不访问任何 Redis 实例。
func validatePrefix(raw string) (string, *pluginapi.Error) {
	if raw == "" {
		return "", invalid("key 前缀不能为空")
	}
	if len(raw) > maxPrefixLen {
		return "", invalid("key 前缀超过长度上限")
	}
	if strings.ContainsRune(raw, 0) {
		return "", invalid("key 前缀含 NUL 字节")
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "", invalid("key 前缀含控制字符")
		}
	}
	// 骨架口径下不接受通配符，避免把 glob 语义误当作前缀匹配。
	if strings.ContainsAny(raw, "*?[]") {
		return "", invalid("key 前缀含通配符，骨架只接受字面前缀")
	}
	return raw, nil
}
