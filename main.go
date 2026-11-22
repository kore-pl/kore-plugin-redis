// Package main 是 kore 的 redis 插件：Redis 管理的方法面骨架。
//
// redis 只做协议握手 + 参数校验 + 结构化结果，不真连 Redis、
// 不内置 redis-server 二进制（内核零依赖 / G-05 无中间件 / 提案 §2.1 批 2）。
// 本插件管理的是"用户自己的 Redis 实例"，连接方式与凭据由使用者提供，
// 骨架口径下仅返回结构化占位，不发起任何网络 I/O。
//
// 协议面（kore.plugin.v1）：UDS → kore.hello 握手 → 会话循环读 request、
// 按 method 分发并回 response；stdout 留给协议，日志一律写 stderr。
package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
)

// main 按退出码区分失败：0 优雅退出、1 启动或协议失败。
func main() {
	logger := newLogger(os.Getenv("KORE_LOG_LEVEL"))
	if err := run(logger); err != nil {
		logger.Error("插件退出", "err", err)
		os.Exit(1)
	}
}

// run 依次完成建连、hello 握手与会话循环；收到 kore.shutdown 后返回 nil。
func run(logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	conn, err := net.Dial("unix", cfg.socket)
	if err != nil {
		return fmt.Errorf("连接主壳 socket %s: %w", cfg.socket, err)
	}
	defer func() { _ = conn.Close() }()
	if err := handshake(conn, cfg); err != nil {
		return err
	}
	logger.Info("hello 完成", "plugin_id", cfg.id, "version", cfg.version)
	return serve(conn, cfg, logger)
}
