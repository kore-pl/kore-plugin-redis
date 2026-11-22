// Package main 是 kore 的 redis 插件：真实连接并管理用户已有的 Redis 实例。
//
// redis 通过最小 RESP 客户端（标准库 net + 自实现编解码）完成 PING/INFO/SCAN/
// TYPE/TTL/GET/HGETALL/LRANGE/SMEMBERS/ZRANGE/DEL，不引入 Redis 客户端库、
// 不内置 redis-server（内核零依赖 / G-05 无中间件）。密码只从环境变量读取，
// 经 AUTH 命令走协议传输，不进 argv、不落配置文件、不回传响应。
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
	return serve(conn, &cfg, logger)
}
