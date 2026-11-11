package conversation

import (
	"context"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

type PreparedTool func(context.Context) (ToolExecutionResult, error)

type preparedToolOutcome struct {
	result     ToolExecutionResult
	err        error
	durationMS int64
}

// Only contiguous host-declared reads reach this path. All preflight checks
// finish in model order before bodies run. A pending approval is a barrier:
// later calls are neither prepared nor executed.
func prepareParallelReads(ctx context.Context, blocks []protocol.Block, options ExecuteToolOptions, onStarted func(protocol.Block)) []preparedToolOutcome {
	bodies := make([]PreparedTool, 0, len(blocks))
	outcomes := make([]preparedToolOutcome, 0, len(blocks))
	started := make([]time.Time, 0, len(blocks))
	contexts := make([]context.Context, 0, len(blocks))
	cancels := make([]context.CancelFunc, 0, len(blocks))
	for _, block := range blocks {
		started = append(started, time.Now())
		if onStarted != nil {
			onStarted(block)
		}
		prepareCtx := ctx
		cancel := func() {}
		if options.Timeout > 0 {
			prepareCtx, cancel = context.WithTimeout(ctx, options.Timeout)
		}
		body, err := options.Prepare(prepareCtx, block.Name, block.Input)
		contexts = append(contexts, prepareCtx)
		cancels = append(cancels, cancel)
		bodies = append(bodies, body)
		outcomes = append(outcomes, preparedToolOutcome{err: err})
		if shouldStopAfterTool(err) || ctx.Err() != nil {
			break
		}
	}
	var wg sync.WaitGroup
	for i, body := range bodies {
		wg.Add(1)
		go func(i int, body PreparedTool) {
			defer wg.Done()
			defer cancels[i]()
			if err := contexts[i].Err(); err != nil {
				outcomes[i].err = err
				outcomes[i].result.TimedOut = err == context.DeadlineExceeded
				outcomes[i].durationMS = time.Since(started[i]).Milliseconds()
				return
			}
			if body != nil {
				block := blocks[i]
				outcomes[i].result, outcomes[i].err = executeToolWithTimeout(contexts[i], options.Timeout, block.ID, block.Name, block.Input,
					func(runCtx context.Context, _ string, _ map[string]interface{}) (ToolExecutionResult, error) {
						return body(runCtx)
					}, options.StuckWarningAfter, options.OnStuck)
			}
			outcomes[i].durationMS = time.Since(started[i]).Milliseconds()
		}(i, body)
	}
	wg.Wait()
	return outcomes
}
