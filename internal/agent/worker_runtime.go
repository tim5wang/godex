package agent

import "github.com/tim5wang/godex/internal/workerruntime"

func (a *Agent) WorkerRuntime() workerruntime.Runtime {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.workerRuntime == nil {
		a.workerRuntime = localGoDexWorkerRuntime{agent: a}
	}
	return a.workerRuntime
}
