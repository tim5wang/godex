package localbash

import (
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// spinningReader 模拟一个反复返回 (0, nil) 的 io.Reader。
// Stop() 之前不返回任何 io.EOF / error;Stop() 后下一次
// Read 立即返回 io.EOF,模拟 pipe 被关闭的真实瞬间。
//
// 真实场景:exec.Cmd 子进程被 ctx cancel 杀掉,stdout/stderr
// pipe 在很短的窗口期内既无数据又未关闭;Go runtime 给
// Read 反复返回 (0, nil)。当前 copyTo 没有抗空转,会
// 永远自旋到 pipe 真的关闭为止。CPU 100% 飘动 —— 上一轮
// 复测确认 50ms 内 6.5M 次 Read。
type spinningReader struct {
	calls   atomic.Int64
	stopped atomic.Bool
}

func (s *spinningReader) Read(p []byte) (int, error) {
	s.calls.Add(1)
	if s.stopped.Load() {
		return 0, io.EOF
	}
	// 真实生产场景:Read 在 pipe buffer 空时立即返回 (0, nil)。
	// 不阻塞、不 sleep,copyTo 立刻再次 Read。
	return 0, nil
}

func (s *spinningReader) Stop() { s.stopped.Store(true) }

// TestCopyToDoesNotBusySpinOnSpuriousEmptyReads 验证 copyTo
// 在 reader 反复返回 (0, nil) 时,不会因此让 CPU 100% 自旋。
// 当前 copyTo 没有抗空转,这一测试在修复前会因 stub.calls
// 在 50ms 内飙升到百万级而被 fail。
//
// 阈值选择:有抗空转(>5ms sleep)时,50ms 内 Read 调用应 < 1e4;
// 无抗空转时会 > 1e6。取 1e5 作为边界,留两个数量级 buffer。
func TestCopyToDoesNotBusySpinOnSpuriousEmptyReads(t *testing.T) {
	t.Parallel()

	stub := &spinningReader{}
	var dst strings.Builder
	var mu sync.Mutex

	done := make(chan struct{})
	go func() {
		copyTo(&dst, &mu, stub)
		close(done)
	}()

	// 让 copyTo 在 reader 上自旋 50ms
	time.Sleep(50 * time.Millisecond)
	calls := stub.calls.Load()
	t.Logf("copyTo made %d reads in 50ms", calls)

	stub.Stop() // 模拟 pipe 关闭

	select {
	case <-done:
		// copyTo 已经退出
	case <-time.After(2 * time.Second):
		t.Fatalf("copyTo did not exit within 2s after reader EOF; calls=%d", calls)
	}

	// 关键断言:有抗空转时 Read 调用应 < 1e5。修复前会 > 1e6。
	if calls > 100_000 {
		t.Fatalf("copyTo busy-spinning: %d reads in 50ms exceeds 1e5 threshold (no anti-spin backoff)", calls)
	}
}

// TestRunFinalizesOnContextCancel 验证 Run 在 ctx 取消后必须
// 发出 final chunk 并 close channel。
func TestRunFinalizesOnContextCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	ch := Run(ctx, t.TempDir(), "sleep 30")

	deadline := time.After(3 * time.Second)
	sawFinal := false
	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				if !sawFinal {
					t.Fatalf("channel closed without a final chunk")
				}
				return
			}
			if chunk.Final {
				sawFinal = true
			}
		case <-deadline:
			t.Fatalf("Run did not finalize within 3s of ctx cancel; sawFinal=%v", sawFinal)
		}
	}
}

// TestRunCompletesShortCommand 正常路径 smoke。
func TestRunCompletesShortCommand(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := Run(ctx, t.TempDir(), "echo hello")
	var last OutputChunk
	for chunk := range ch {
		last = chunk
	}
	if !last.Final {
		t.Fatalf("expected last chunk to be final, got %#v", last)
	}
	if last.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d (err=%v)", last.ExitCode, last.Err)
	}
	if !strings.Contains(last.Output, "hello") {
		t.Fatalf("expected output to contain 'hello', got %q", last.Output)
	}
}
