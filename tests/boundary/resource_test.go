//go:build linux

// 资源耗尽（判据① 第四类）：必须真实注入，三条路径都由内核返回错误，不用"假装 ENOSPC"的桩。
//  1. /dev/full：内核 write(2) 返回 ENOSPC（与插件写配置同一系统调用语义）；
//  2. RLIMIT_FSIZE（等价 ulimit -f，子进程内注入）：写超配额 → EFBIG，
//     验证 plugincfg.Store.Write 的原子写不破坏旧配置、不留临时文件；
//  3. RLIMIT_NOFILE（等价 ulimit -n，子进程内注入）：fd 耗尽 → EMFILE，
//     验证写入路径给出明确失败而不是静默成功。
//
// 为什么放子进程：降 rlimit 会污染同进程的其它用例；子进程隔离后限额只在被测窗口内生效。
package boundary_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"

	"github.com/pengtang1024/kore-plugin-redis/internal/plugincfg"
)

const (
	// resourceChildMode / resourceChildDir 是父用例传给子进程的注入参数。
	resourceChildMode = "KORE_BOUNDARY_RESOURCE_MODE"
	resourceChildDir  = "KORE_BOUNDARY_RESOURCE_DIR"
	// resourceOldConfig 是注入前写入的合法旧配置，用于断言失败后不被破坏。
	resourceOldConfig = `{"host":"127.0.0.1","port":6379}`
)

// TestBoundaryResourceENOSPCDevFull 用 /dev/full 触发内核 ENOSPC：写入必须失败，
// 且 errno 必须是 ENOSPC（不能被误判成 EIO/EPERM）。
//
// 说明：/dev/full 是字符设备，不能作为配置目录，所以这条验证的是与 plugincfg
// 写配置同一 write(2) 语义下的 ENOSPC 判定；真正经过 Store.Write 的配额注入
// 由下面两条 RLIMIT 用例覆盖。
func TestBoundaryResourceENOSPCDevFull(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("/dev/full 不可用，跳过内核 ENOSPC 注入")
	}
	file, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("/dev/full 不可打开（%v），跳过", err)
	}
	defer func() { _ = file.Close() }()

	written, err := file.Write([]byte(resourceOldConfig))
	if err == nil {
		t.Fatalf("写 /dev/full 应返回 ENOSPC，实际写入 %d 字节且无错误", written)
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("写 /dev/full 应返回 ENOSPC，实际 %v", err)
	}
}

// TestBoundaryResourceFSIZERlimit 在子进程内注入 RLIMIT_FSIZE（= ulimit -f），
// 要求：Store.Write 返回含 EFBIG 的错误、旧配置完整、目录里不残留临时文件。
func TestBoundaryResourceFSIZERlimit(t *testing.T) {
	t.Parallel()
	dir := prepareResourceDir(t)
	output := runResourceChild(t, "fsize", dir)
	if !strings.Contains(output, "EFBIG=true") {
		t.Fatalf("RLIMIT_FSIZE 注入后写入应返回 EFBIG，子进程输出: %s", output)
	}
	if !strings.Contains(output, "old_intact=true") {
		t.Fatalf("写入失败后旧配置必须保持完整，子进程输出: %s", output)
	}
	assertNoTempLeftover(t, dir)
}

// TestBoundaryResourceNofileRlimit 在子进程内注入 RLIMIT_NOFILE（= ulimit -n）
// 并耗尽 fd，要求：Store.Write 返回含 EMFILE 的错误、旧配置完整、不留临时文件。
func TestBoundaryResourceNofileRlimit(t *testing.T) {
	t.Parallel()
	dir := prepareResourceDir(t)
	output := runResourceChild(t, "nofile", dir)
	if !strings.Contains(output, "EMFILE=true") {
		t.Fatalf("RLIMIT_NOFILE 耗尽后写入应返回 EMFILE，子进程输出: %s", output)
	}
	if !strings.Contains(output, "old_intact=true") {
		t.Fatalf("fd 耗尽时旧配置必须保持完整，子进程输出: %s", output)
	}
	assertNoTempLeftover(t, dir)
}

// prepareResourceDir 建状态目录并写入一份合法旧配置。
func prepareResourceDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	store := plugincfg.NewStore(dir)
	if err := store.Write(json.RawMessage(resourceOldConfig)); err != nil {
		t.Fatalf("准备旧配置失败: %v", err)
	}
	return dir
}

// assertNoTempLeftover 断言目录里只剩 config.json：原子写失败既不能破坏旧文件，
// 也不能留下 .config-*.tmp 垃圾。
func assertNoTempLeftover(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取状态目录失败: %v", err)
	}
	if len(entries) == 1 && entries[0].Name() == plugincfg.ConfigFileName {
		return
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	t.Fatalf("状态目录应只剩 %s，实际 %v", plugincfg.ConfigFileName, names)
}

// runResourceChild 以 -test.run 重跑同一测试二进制，在子进程内注入 rlimit 后执行真实写入。
func runResourceChild(t *testing.T, mode, dir string) string {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestBoundaryResourceChild$")
	command.Env = append(os.Environ(),
		resourceChildMode+"="+mode,
		resourceChildDir+"="+dir,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("资源耗尽子进程失败: %v\n%s", err, output)
	}
	return string(output)
}

// TestBoundaryResourceChild 是资源耗尽注入的子进程入口，父用例不会执行其主体。
// 子进程把观测打成一行 KORE_BOUNDARY_RESULT 供父用例断言；观测失败以退出码 2 上报。
func TestBoundaryResourceChild(t *testing.T) {
	mode := os.Getenv(resourceChildMode)
	if mode == "" {
		t.Skip("子进程专用：由 FSIZE / NOFILE 用例以 -test.run 调用")
	}
	dir := os.Getenv(resourceChildDir)
	if dir == "" {
		resourceChildFail("缺少环境变量 %s", resourceChildDir)
	}
	store := plugincfg.NewStore(dir)
	var err error
	switch mode {
	case "fsize":
		// 4096 字节配额远小于下面的写入载荷；忽略 SIGXFSZ 让 write 返回 EFBIG 而不是杀进程。
		signal.Ignore(syscall.SIGXFSZ)
		if rlimitErr := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 4096, Max: 4096}); rlimitErr != nil {
			resourceChildFail("设置 RLIMIT_FSIZE 失败: %v", rlimitErr)
		}
		payload := fmt.Sprintf(`{"padding":%q}`, strings.Repeat("x", 8192))
		err = store.Write(json.RawMessage(payload))
	case "nofile":
		if rlimitErr := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: 64, Max: 64}); rlimitErr != nil {
			resourceChildFail("设置 RLIMIT_NOFILE 失败: %v", rlimitErr)
		}
		held := make([]*os.File, 0, 256)
		for len(held) < 4096 {
			file, openErr := os.Open(os.DevNull)
			if openErr != nil {
				break
			}
			held = append(held, file)
		}
		if len(held) == 0 {
			resourceChildFail("未能耗尽文件描述符")
		}
		err = store.Write(json.RawMessage(`{"host":"10.0.0.1"}`))
		for _, file := range held {
			_ = file.Close()
		}
	default:
		resourceChildFail("未知注入模式 %q", mode)
	}

	oldIntact := false
	if raw, exists, readErr := store.Read(); readErr == nil && exists && string(raw) == resourceOldConfig {
		oldIntact = true
	}
	fmt.Printf("KORE_BOUNDARY_RESULT mode=%s err=%v EFBIG=%t EMFILE=%t old_intact=%t\n",
		mode, err, errors.Is(err, syscall.EFBIG), errors.Is(err, syscall.EMFILE), oldIntact)
	if err == nil {
		resourceChildFail("注入 %s 后写入应失败，实际成功", mode)
	}
}

// resourceChildFail 打印失败原因并以退出码 2 结束子进程。
func resourceChildFail(format string, args ...any) {
	fmt.Printf("KORE_BOUNDARY_CHILD_FAIL "+format+"\n", args...)
	os.Exit(2)
}
