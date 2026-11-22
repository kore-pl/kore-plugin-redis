package redisop

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// redisError 是 Redis 返回的 "-ERR ..." 错误行；实现 error 便于统一冒泡。
type redisError string

// Error 实现 error 接口。
func (e redisError) Error() string { return string(e) }

// encodeCommand 把一条命令编码为 RESP 数组帧：*N\r\n$len\r\narg\r\n...。
// 参数按字节走协议，不经过任何 shell/argv，因此密码不会出现在进程参数里。
func encodeCommand(args ...string) []byte {
	buffer := make([]byte, 0, 32)
	buffer = append(buffer, '*')
	buffer = strconv.AppendInt(buffer, int64(len(args)), 10)
	buffer = append(buffer, '\r', '\n')
	for _, arg := range args {
		buffer = append(buffer, '$')
		buffer = strconv.AppendInt(buffer, int64(len(arg)), 10)
		buffer = append(buffer, '\r', '\n')
		buffer = append(buffer, arg...)
		buffer = append(buffer, '\r', '\n')
	}
	return buffer
}

// respConn 是一条 Redis 连接的 RESP 读写器。
type respConn struct {
	conn net.Conn
	br   *bufio.Reader
	bw   *bufio.Writer
}

// newRespConn 包装底层连接。
func newRespConn(conn net.Conn) *respConn {
	return &respConn{
		conn: conn,
		br:   bufio.NewReaderSize(conn, 32*1024),
		bw:   bufio.NewWriterSize(conn, 32*1024),
	}
}

// Close 关闭连接。
func (c *respConn) Close() error { return c.conn.Close() }

// send 写入一条命令但不刷新，供 pipeline 批量发送。
func (c *respConn) send(args ...string) error {
	if _, err := c.bw.Write(encodeCommand(args...)); err != nil {
		return err
	}
	return nil
}

// flush 把已写入的命令一次性发给服务端。
func (c *respConn) flush() error { return c.bw.Flush() }

// readReply 读取一条完整 RESP 回复。
func (c *respConn) readReply() (any, error) {
	line, err := c.readLine()
	if err != nil {
		return nil, err
	}
	if line == "" {
		return nil, errors.New("RESP 回复为空行")
	}
	switch line[0] {
	case '+':
		return line[1:], nil
	case '-':
		return nil, redisError(line[1:])
	case ':':
		value, err := strconv.ParseInt(line[1:], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("解析 RESP 整数失败: %v", err)
		}
		return value, nil
	case '$':
		return c.readBulk(line[1:])
	case '*':
		return c.readArray(line[1:])
	default:
		return nil, fmt.Errorf("未知 RESP 类型前缀 %q", line[0])
	}
}

// readLine 读取一行（以 CRLF 结尾），并限制最大长度。
func (c *respConn) readLine() (string, error) {
	line, err := c.br.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(line) > maxBulkBytes {
		return "", fmt.Errorf("RESP 行超过上限 %d 字节", maxBulkBytes)
	}
	if !strings.HasSuffix(line, "\r\n") {
		return "", errors.New("RESP 行未以 CRLF 结尾")
	}
	return line[:len(line)-2], nil
}

// readBulk 读取 bulk string；-1 表示 nil。
func (c *respConn) readBulk(lengthText string) (any, error) {
	length, err := strconv.ParseInt(lengthText, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("解析 RESP bulk 长度失败: %v", err)
	}
	if length == -1 {
		return nil, nil
	}
	if length < 0 || length > maxBulkBytes {
		return nil, fmt.Errorf("RESP bulk 长度 %d 超出上限 %d", length, maxBulkBytes)
	}
	buffer := make([]byte, length+2)
	if _, err := io.ReadFull(c.br, buffer); err != nil {
		return nil, fmt.Errorf("读取 RESP bulk 失败: %w", err)
	}
	if buffer[length] != '\r' || buffer[length+1] != '\n' {
		return nil, errors.New("RESP bulk 未以 CRLF 结尾")
	}
	return string(buffer[:length]), nil
}

// readArray 读取数组；-1 表示 nil，元素可为任意 RESP 回复。
func (c *respConn) readArray(countText string) (any, error) {
	count, err := strconv.ParseInt(countText, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("解析 RESP 数组长度失败: %v", err)
	}
	if count == -1 {
		return nil, nil
	}
	if count < 0 || count > maxArrayItems {
		return nil, fmt.Errorf("RESP 数组长度 %d 超出上限 %d", count, maxArrayItems)
	}
	items := make([]any, 0, count)
	for i := int64(0); i < count; i++ {
		item, err := c.readReply()
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// do 发送单条命令并读取回复。
func (c *respConn) do(args ...string) (any, error) {
	if err := c.send(args...); err != nil {
		return nil, err
	}
	if err := c.flush(); err != nil {
		return nil, err
	}
	return c.readReply()
}

// asString 把字符串类回复（simple/bulk）转成 string。
func asString(reply any) (string, bool) {
	value, ok := reply.(string)
	return value, ok
}

// asInt 把整数回复转成 int64。
func asInt(reply any) (int64, bool) {
	value, ok := reply.(int64)
	return value, ok
}

// asArray 把数组回复转成 []any。
func asArray(reply any) ([]any, bool) {
	value, ok := reply.([]any)
	return value, ok
}

// safeAtoi 把 INFO 字段安全转成 int64；缺失或非法时返回 0，不阻断整次调用。
func safeAtoi(text string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil {
		return 0
	}
	return value
}

// dial 建立 TCP 连接并设置命令级截止时间；密码只通过 AUTH 命令传输。
func (c *Client) dial(instance InstanceConfig) (*respConn, error) {
	password, err := instancePassword(instance)
	if err != nil {
		return nil, err
	}
	addr := instance.Addr()
	raw, err := net.DialTimeout("tcp", addr, c.connectTimeout)
	if err != nil {
		return nil, opErrorf(pluginapi.E_UPSTREAM, "连接 Redis %s 失败: %s", addr, sanitizeSecrets(err.Error(), password))
	}
	if err := raw.SetDeadline(time.Now().Add(c.commandTimeout)); err != nil {
		_ = raw.Close()
		return nil, opErrorf(pluginapi.E_INTERNAL, "设置 Redis 连接超时失败: %v", err)
	}
	conn := newRespConn(raw)
	if password != "" {
		reply, err := conn.do("AUTH", password)
		if err != nil {
			_ = conn.Close()
			return nil, opErrorf(pluginapi.E_UPSTREAM, "Redis 认证失败: %s", sanitizeSecrets(err.Error(), password))
		}
		if text, ok := asString(reply); !ok || text != "OK" {
			_ = conn.Close()
			return nil, opErrorf(pluginapi.E_UPSTREAM, "Redis 认证返回非 OK: %s", sanitizeSecrets(fmt.Sprint(reply), password))
		}
	}
	return conn, nil
}
