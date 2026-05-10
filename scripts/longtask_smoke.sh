#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

echo "[longtask-smoke] backend restart/replay/approval recovery"
go test ./internal/services/backend -run 'Test(OpenSessionMarksInterruptedTurnAfterRestart|ResumeTurnAsyncContinuesInterruptedCheckpoint|PendingPermissionsPersistAcrossServiceRestart|SubscribeReplayReplaysActiveTurnEvents)' -count=1

echo "[longtask-smoke] durable subagents"
go test ./internal/agent -run 'TestDurableSubagent(CompletesAndPersistsResult|EmitsAndPersistsProgress|LoadMarksRunningJobInterrupted|ResumeInterruptedJob|UsesIsolatedWorktreeAndMerge|MergeDetectsMainWorkspaceConflict)' -count=1

echo "[longtask-smoke] end-to-end development loop"
go test ./internal/agent -run 'TestLongTaskDevelopmentSmokeFixesFixtureRepo' -count=1

echo "[longtask-smoke] output budget and approval restore"
go test ./internal/tools -run 'Test(BashToolSpillsLargeOutput|BrowserSessionApprovalPersistsAcrossRestore)' -count=1

echo "[longtask-smoke] channel approval/restart flow"
go test ./internal/runtime/channels -run 'TestManager(RouteInboundApproveCommandResumesPendingTurn|ReconcileRollsBackFailedRestartWithoutStoppingOtherChannels)' -count=1

echo "[longtask-smoke] history search scale/indexing"
go test ./internal/services/historysearch -run 'TestServiceSearchHistory(AllArchivesCreatesSQLiteSidecar|SidecarRefreshesChangedTranscript|TruncatesAndSortsByRecency)' -count=1

echo "[longtask-smoke] done"
