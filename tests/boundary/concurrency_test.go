// 并发（判据① 第二类）：真并发 + 真 TCP + 真 RESP，断言无数据竞争
// （由 `go test -race` 判定）且并发下的单命令原子语义不被客户端放大成错误结果。
//
// 覆盖两类：并发读写同一 key；并发 rename / move / copy 同一目标。
package boundary_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/pengtang1024/kore-plugin-redis/internal/redisop"
)

// TestBoundaryConcurrencySameKeyReadWrite 让 6 个写者与 6 个读者同时操作同一 key：
// 读到的值只能是种子或某次写入值（不能是空/半截），写者不得报错，
// 最终服务端该 key 只有一个值。竞争由 -race 在客户端与服务端两侧同时检测。
func TestBoundaryConcurrencySameKeyReadWrite(t *testing.T) {
	t.Parallel()
	server := startKVServer(t)
	instances := server.instances()
	const writers, readers, rounds = 6, 6, 20

	mustInvoke(t, instances, "redis.set", jsonObject(t, map[string]any{
		"key": "hot-key", "type": "string", "value": "seed",
	}))
	readParams := jsonObject(t, map[string]any{"key": "hot-key"})

	errCh := make(chan string, (writers+readers)*rounds)
	var wg sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for round := 0; round < rounds; round++ {
				value := fmt.Sprintf("w%d-r%d", id, round)
				params := json.RawMessage(fmt.Sprintf(
					`{"key":"hot-key","type":"string","value":%q}`, value))
				if _, perr := invoke(instances, "redis.set", params); perr != nil {
					errCh <- fmt.Sprintf("写 %s 失败: %s: %s", value, perr.Code, perr.Message)
				}
			}
		}(writer)
	}
	for reader := 0; reader < readers; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < rounds; round++ {
				raw, perr := invoke(instances, "redis.get", readParams)
				if perr != nil {
					errCh <- fmt.Sprintf("读失败: %s: %s", perr.Code, perr.Message)
					continue
				}
				var result redisop.GetResult
				if err := json.Unmarshal(raw, &result); err != nil {
					errCh <- "解析 get 结果失败: " + err.Error()
					continue
				}
				var value string
				if err := json.Unmarshal(result.Value, &value); err != nil {
					errCh <- "值不是字符串: " + string(result.Value)
					continue
				}
				if value != "seed" && !strings.HasPrefix(value, "w") {
					errCh <- "读到非法值: " + value
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for message := range errCh {
		t.Errorf("并发读写同一 key：%s", message)
	}

	raw := mustInvoke(t, instances, "redis.get", readParams)
	var final redisop.GetResult
	if err := json.Unmarshal(raw, &final); err != nil {
		t.Fatalf("解析最终值失败: %v", err)
	}
	var finalValue string
	if err := json.Unmarshal(final.Value, &finalValue); err != nil || !strings.HasPrefix(finalValue, "w") {
		t.Fatalf("最终值应是某次写入值（w*），实际 %s", final.Value)
	}
	if count := server.keyCount(0); count != 1 {
		t.Fatalf("并发写同一 key 后服务端应只有 1 个键，实际 %d", count)
	}
}

// TestBoundaryConcurrencyRenameSingleWinner 让 8 个源键并发 RENAMENX 到同一目标：
// 恰好 1 个赢家（renamed=true），其余 7 个必须是 renamed=false（不覆盖），
// 不能被客户端并发放大成"多个赢家"或丢键。
func TestBoundaryConcurrencyRenameSingleWinner(t *testing.T) {
	t.Parallel()
	server := startKVServer(t)
	instances := server.instances()
	const contenders = 8

	for index := 0; index < contenders; index++ {
		mustInvoke(t, instances, "redis.set", jsonObject(t, map[string]any{
			"key": fmt.Sprintf("src-%d", index), "type": "string", "value": fmt.Sprintf("v%d", index),
		}))
	}

	type outcome struct {
		renamed bool
		err     string
	}
	results := make(chan outcome, contenders)
	var wg sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			params := json.RawMessage(fmt.Sprintf(`{"key":"src-%d","new_key":"target"}`, id))
			raw, perr := invoke(instances, "redis.rename", params)
			if perr != nil {
				results <- outcome{err: perr.Code + ": " + perr.Message}
				return
			}
			var result redisop.RenameResult
			if err := json.Unmarshal(raw, &result); err != nil {
				results <- outcome{err: "解析 rename 结果失败: " + err.Error()}
				return
			}
			results <- outcome{renamed: result.Renamed}
		}(index)
	}
	wg.Wait()
	close(results)

	winners := 0
	for result := range results {
		if result.err != "" {
			t.Fatalf("并发 rename 失败: %s", result.err)
		}
		if result.renamed {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("并发 RENAMENX 应恰好 1 个赢家，实际 %d", winners)
	}
	if !server.hasKey(0, "target") {
		t.Fatalf("赢家键 target 应存在")
	}
	if count := server.keyCount(0); count != contenders {
		t.Fatalf("7 个源键 + 1 个 target 应共 %d 个键，实际 %d", contenders, count)
	}
}

// TestBoundaryConcurrencyMoveCopySingleWinner 覆盖并发 MOVE 同一 key 与并发 COPY
// 到同一目标：每次都恰好 1 个成功，其余返回 moved/copied=false，键不丢不重。
func TestBoundaryConcurrencyMoveCopySingleWinner(t *testing.T) {
	t.Parallel()
	server := startKVServer(t)
	instances := server.instances()
	const contenders = 8

	mustInvoke(t, instances, "redis.set", jsonObject(t, map[string]any{
		"key": "moving", "type": "string", "value": "v",
	}))
	moveParams := json.RawMessage(`{"key":"moving","db":1}`)
	moveResults := make(chan bool, contenders)
	var wg sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, perr := invoke(instances, "redis.move", moveParams)
			if perr != nil {
				moveResults <- false
				t.Errorf("并发 move 失败: %s: %s", perr.Code, perr.Message)
				return
			}
			var result redisop.MoveResult
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Errorf("解析 move 结果失败: %v", err)
				moveResults <- false
				return
			}
			moveResults <- result.Moved
		}()
	}
	wg.Wait()
	close(moveResults)
	moved := 0
	for result := range moveResults {
		if result {
			moved++
		}
	}
	if moved != 1 {
		t.Fatalf("并发 MOVE 同一 key 应恰好 1 次 moved=true，实际 %d", moved)
	}
	if !server.hasKey(1, "moving") || server.hasKey(0, "moving") {
		t.Fatalf("MOVE 后键应在 db1 且不在 db0")
	}

	for index := 0; index < contenders; index++ {
		mustInvoke(t, instances, "redis.set", jsonObject(t, map[string]any{
			"key": fmt.Sprintf("copy-src-%d", index), "type": "string", "value": fmt.Sprintf("v%d", index),
		}))
	}
	copied := make(chan bool, contenders)
	for index := 0; index < contenders; index++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			params := json.RawMessage(fmt.Sprintf(`{"key":"copy-src-%d","dest":"copy-dest"}`, id))
			raw, perr := invoke(instances, "redis.copy", params)
			if perr != nil {
				copied <- false
				t.Errorf("并发 copy 失败: %s: %s", perr.Code, perr.Message)
				return
			}
			var result redisop.CopyResult
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Errorf("解析 copy 结果失败: %v", err)
				copied <- false
				return
			}
			copied <- result.Copied
		}(index)
	}
	wg.Wait()
	close(copied)
	copyWinners := 0
	for result := range copied {
		if result {
			copyWinners++
		}
	}
	if copyWinners != 1 {
		t.Fatalf("并发 COPY 到同一目标应恰好 1 次 copied=true，实际 %d", copyWinners)
	}
	if !server.hasKey(0, "copy-dest") {
		t.Fatalf("成功复制后目标键 copy-dest 应存在")
	}
	if count := server.keyCount(0); count != contenders+1 {
		t.Fatalf("8 个源键 + 1 个目标键应共 %d 个键，实际 %d", contenders+1, count)
	}
}
