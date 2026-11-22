package redisop

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// OpError 是执行面带协议错误码的错误，classify 优先用它，避免二次加工时丢码。
type OpError struct {
	Code    string
	Message string
}

// Error 实现 error 接口。
func (e *OpError) Error() string { return e.Code + ": " + e.Message }

// opErrorf 构造带协议错误码的执行面错误。
func opErrorf(code, format string, args ...any) *OpError {
	return &OpError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// timeoutError 统一超时错误文案（含实际生效超时）。
func timeoutError(d time.Duration) *OpError {
	return opErrorf(pluginapi.E_UPSTREAM, "操作超时（%s），已断开 Redis 连接", d)
}

// errorText 把执行面错误压成可放进 per-instance error 字段的文本（不含密码）。
func errorText(err error) string {
	var op *OpError
	if errors.As(err, &op) {
		return op.Message
	}
	return err.Error()
}

// sanitizeSecrets 把已知密码从错误文本中抹掉并截断，避免上游回显导致泄漏。
func sanitizeSecrets(message string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "***")
		}
	}
	if runes := []rune(message); len(runes) > maxErrorRunes {
		message = string(runes[:maxErrorRunes]) + "…"
	}
	return message
}
