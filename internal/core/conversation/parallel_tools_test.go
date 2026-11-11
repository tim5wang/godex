package conversation

import (
	"context"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

func TestParallelReadsOverlapButDoNotCrossWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	blocks := []protocol.Block{
		protocol.ToolUseBlock("a", "read_file", map[string]interface{}{"path": "a"}),
		protocol.ToolUseBlock("b", "read_file", map[string]interface{}{"path": "b"}),
		protocol.ToolUseBlock("write", "write_file", nil),
		protocol.ToolUseBlock("c", "read_file", map[string]interface{}{"path": "c"}),
		protocol.ToolUseBlock("d", "read_file", map[string]interface{}{"path": "d"}),
	}
	var active, peak, entered atomic.Int32
	var wrote atomic.Bool
	release := make(chan struct{})
	var prepared, finished []string
	result, executed, err := ExecuteToolUsesWithOptions(ctx, blocks,
		func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error) {
			if active.Load() != 0 {
				t.Error("write overlapped a read")
			}
			wrote.Store(true)
			return ToolExecutionResult{Output: "write"}, nil
		}, nil, func(tool ExecutedTool) { finished = append(finished, tool.ID) }, nil,
		ExecuteToolOptions{
			MaxParallel: 2,
			CanParallel: func(block protocol.Block) bool { return block.Name == "read_file" },
			Prepare: func(_ context.Context, _ string, input map[string]interface{}) (PreparedTool, error) {
				path := input["path"].(string)
				prepared = append(prepared, path)
				if (path == "c" || path == "d") && !wrote.Load() {
					t.Error("read was prepared across a write barrier")
				}
				return func(ctx context.Context) (ToolExecutionResult, error) {
					n := active.Add(1)
					defer active.Add(-1)
					for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
					}
					if entered.Add(1) == 2 {
						close(release)
					}
					select {
					case <-release:
					case <-ctx.Done():
						return ToolExecutionResult{}, ctx.Err()
					}
					return ToolExecutionResult{Output: path}, nil
				}, nil
			},
		})
	if err != nil || len(executed) != 5 || len(result.Content) != 5 {
		t.Fatalf("batch failed: %v, %+v", err, executed)
	}
	if peak.Load() != 2 {
		t.Fatalf("expected two overlapping reads, got %d", peak.Load())
	}
	if !reflect.DeepEqual(prepared, []string{"a", "b", "c", "d"}) || !reflect.DeepEqual(finished, []string{"a", "b", "write", "c", "d"}) {
		t.Fatalf("order changed: prepared=%v finished=%v", prepared, finished)
	}
}

func TestParallelReadApprovalStopsPreparationAndExecutionOfLaterCalls(t *testing.T) {
	var blocks []protocol.Block
	for i := 0; i < 4; i++ {
		blocks = append(blocks, protocol.ToolUseBlock(fmt.Sprint(i), "read_file", map[string]interface{}{"index": i}))
	}
	var prepared []int
	var executions atomic.Int32
	result, executed, err := ExecuteToolUsesWithOptions(context.Background(), blocks, nil, nil, nil, nil, ExecuteToolOptions{
		MaxParallel: 4,
		CanParallel: func(protocol.Block) bool { return true },
		Prepare: func(_ context.Context, _ string, input map[string]interface{}) (PreparedTool, error) {
			i := input["index"].(int)
			prepared = append(prepared, i)
			if i == 1 {
				return nil, pendingStopError{}
			}
			return func(context.Context) (ToolExecutionResult, error) {
				executions.Add(1)
				return ToolExecutionResult{Output: "ok"}, nil
			}, nil
		},
	})
	if err == nil || len(executed) != 2 || executions.Load() != 1 || !reflect.DeepEqual(prepared, []int{0, 1}) {
		t.Fatalf("approval barrier was crossed: err=%v prepared=%v executed=%d calls=%d", err, prepared, len(executed), executions.Load())
	}
	if len(result.Content) != 4 || result.Content[2].Content == "" || result.Content[3].Content == "" {
		t.Fatal("unexecuted siblings need explicit skipped results")
	}
}

func BenchmarkReadBatch(b *testing.B) {
	for _, parallel := range []int{1, 4} {
		b.Run(fmt.Sprintf("parallel_%d", parallel), func(b *testing.B) {
			var blocks []protocol.Block
			for i := 0; i < 8; i++ {
				blocks = append(blocks, protocol.ToolUseBlock(fmt.Sprint(i), "read_file", nil))
			}
			run := func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error) {
				time.Sleep(20 * time.Millisecond)
				return ToolExecutionResult{Output: "read"}, nil
			}
			options := ExecuteToolOptions{MaxParallel: parallel, CanParallel: func(protocol.Block) bool { return true }, Prepare: func(context.Context, string, map[string]interface{}) (PreparedTool, error) {
				return func(ctx context.Context) (ToolExecutionResult, error) { return run(ctx, "read_file", nil) }, nil
			}}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := ExecuteToolUsesWithOptions(context.Background(), blocks, run, nil, nil, nil, options); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
