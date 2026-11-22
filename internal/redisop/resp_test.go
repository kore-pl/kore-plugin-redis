package redisop

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

// TestEncodeCommand 校验 RESP 命令编码逐字节正确（数组 + bulk 长度 + CRLF）。
func TestEncodeCommand(t *testing.T) {
	got := string(encodeCommand("SET", "foo", "bar"))
	want := "*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nbar\r\n"
	if got != want {
		t.Fatalf("encodeCommand=%q，期望 %q", got, want)
	}
	empty := string(encodeCommand("PING"))
	if empty != "*1\r\n$4\r\nPING\r\n" {
		t.Fatalf("单参数编码=%q", empty)
	}
}

// TestReadReplyTypes 覆盖 simple/error/int/bulk/array/nil 六类回复。
func TestReadReplyTypes(t *testing.T) {
	cases := []struct {
		payload string
		want    any
		wantErr bool
	}{
		{"+OK\r\n", "OK", false},
		{"-ERR boom\r\n", nil, true},
		{":42\r\n", int64(42), false},
		{"$3\r\nbar\r\n", "bar", false},
		{"$-1\r\n", nil, false},
		{"$0\r\n\r\n", "", false},
		{"*2\r\n$3\r\nfoo\r\n:7\r\n", []any{"foo", int64(7)}, false},
		{"*-1\r\n", nil, false},
		{"*0\r\n", []any{}, false},
	}
	for _, c := range cases {
		conn := newRespConn(nopConn{})
		conn.br = bufio.NewReader(strings.NewReader(c.payload))
		got, err := conn.readReply()
		if c.wantErr {
			if err == nil {
				t.Fatalf("%q 期望错误，得到 %v", c.payload, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q 解析失败: %v", c.payload, err)
		}
		if !equalReply(got, c.want) {
			t.Fatalf("%q 解析=%#v，期望 %#v", c.payload, got, c.want)
		}
	}
}

// TestReadReplyMalformed 校验非法帧不会静默通过。
func TestReadReplyMalformed(t *testing.T) {
	cases := []string{
		"?oops\r\n",
		"$abc\r\n",
		"$3\r\nab\r\n",
		"+OK\n",
		":not-int\r\n",
		"*2\r\n$3\r\nfoo\r\n",
	}
	for _, payload := range cases {
		conn := newRespConn(nopConn{})
		conn.br = bufio.NewReader(strings.NewReader(payload))
		if _, err := conn.readReply(); err == nil {
			t.Fatalf("%q 应解析失败", payload)
		}
	}
}

// TestReadReplyLimits bulk/数组长度超限时拒绝，避免恶意服务撑爆内存。
func TestReadReplyLimits(t *testing.T) {
	conn := newRespConn(nopConn{})
	conn.br = bufio.NewReader(strings.NewReader("$999999999\r\n"))
	if _, err := conn.readReply(); err == nil {
		t.Fatal("超大 bulk 长度应被拒绝")
	}
	conn.br = bufio.NewReader(strings.NewReader("*999999999\r\n"))
	if _, err := conn.readReply(); err == nil {
		t.Fatal("超大数组长度应被拒绝")
	}
}

// TestParseInfoFields INFO 解析忽略段标题与空行。
func TestParseInfoFields(t *testing.T) {
	raw := "# Server\r\nredis_version:7.2.4\r\nuptime_in_seconds:12\r\n\n"
	fields := parseInfoFields(raw)
	if fields["redis_version"] != "7.2.4" || fields["uptime_in_seconds"] != "12" {
		t.Fatalf("INFO 字段解析异常: %#v", fields)
	}
	if _, exists := fields["# Server"]; exists {
		t.Fatal("段标题不应进入字段表")
	}
}

// TestParseScanReply 校验 SCAN 回复形状与非法形状拒绝。
func TestParseScanReply(t *testing.T) {
	cursor, keys, err := parseScanReply([]any{"17", []any{"a", "b"}})
	if err != nil || cursor != "17" || len(keys) != 2 {
		t.Fatalf("SCAN 解析=(%q,%v,%v)", cursor, keys, err)
	}
	if _, _, err := parseScanReply([]any{"17"}); err == nil {
		t.Fatal("SCAN 回复缺键列表应报错")
	}
	if _, _, err := parseScanReply("nope"); err == nil {
		t.Fatal("SCAN 回复非数组应报错")
	}
}

// nopConn 是 readReply 单测用的空连接占位（不参与任何读写）。
type nopConn struct{}

func (nopConn) Read([]byte) (int, error)         { return 0, nil }
func (nopConn) Write(p []byte) (int, error)      { return len(p), nil }
func (nopConn) Close() error                     { return nil }
func (nopConn) LocalAddr() net.Addr              { return netAddr{} }
func (nopConn) RemoteAddr() net.Addr             { return netAddr{} }
func (nopConn) SetDeadline(time.Time) error      { return nil }
func (nopConn) SetReadDeadline(time.Time) error  { return nil }
func (nopConn) SetWriteDeadline(time.Time) error { return nil }

// netAddr 是 net.Addr 的最小实现。
type netAddr struct{}

func (netAddr) Network() string { return "nop" }
func (netAddr) String() string  { return "nop" }

// equalReply 比较 RESP 取值，区分 nil 与空数组。
func equalReply(got, want any) bool {
	switch wantValue := want.(type) {
	case nil:
		return got == nil
	case []any:
		gotValue, ok := got.([]any)
		if !ok || len(gotValue) != len(wantValue) {
			return false
		}
		for index := range wantValue {
			if !equalReply(gotValue[index], wantValue[index]) {
				return false
			}
		}
		return true
	default:
		return got == want
	}
}
