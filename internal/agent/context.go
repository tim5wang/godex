package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/background"
	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/modelcontext"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/tools"
)

type BuildContextResult struct {
	System                string
	Messages              []protocol.Message
	ToolSchemas           []protocol.ToolSchema
	TokenEstimate         int
	TokenBreakdown        tools.ContextTokenBreakdown
	CompressionReasons    []string
	Compacted             bool
	CompactionBefore      int
	CompactionAfter       int
	PreCompactionTotal    int
	PostCompactionTotal   int
	CompactionMode        string
	CompactionLatencyMS   int64
	LargestContextSources []tools.ContextSourcePressure
	AckRuntime            func()
	HistoryRecall         *tools.HistoryRecallEvaluation
}

func (a *Agent) buildContext(ctx context.Context) (*BuildContextResult, error) {
	history, version := a.messageState()
	history = dedupeRepeatedLargeToolResultSummaries(history)
	query := latestPersistentUserText(history)
	a.activateImplicitBundlesForQuery(query)
	agentProfile := agentProfileFromContext(ctx)

	system, err := a.buildRuntimeSystemPrompt(agentProfile)
	if err != nil {
		return nil, err
	}
	memoryMessages, memoryLayers, err := a.collectMemoryMessages(history)
	if err != nil {
		return nil, err
	}
	promptStateSections, err := a.buildDynamicRuntimePromptSections(agentProfile)
	if err != nil {
		return nil, err
	}
	promptStateMessages := runtimePromptMessages(promptStateSections)
	runtimeMessages, ackRuntime := a.collectRuntimeMessages()
	if ledger := strings.TrimSpace(tools.SessionContextFromContext(ctx).ProjectLedger); ledger != "" {
		runtimeMessages = append([]protocol.Message{protocol.NewEphemeralTextMessage(protocol.KindBackground, formatProjectLedgerRuntimeMessage(ledger))}, runtimeMessages...)
	}
	allRuntimeMessages := append(protocol.CloneMessages(promptStateMessages), runtimeMessages...)

	triggerTokens := a.compactionTriggerTokens()
	preliminary := estimateContextBudget(system, history, memoryMessages, allRuntimeMessages, a.toolHandler.ActiveSchemas(), triggerTokens)
	compactedHistory, compacted, compactionDiag, err := a.maybeAutoCompact(ctx, history, version, system, preliminary)
	if err != nil {
		return nil, err
	}
	combined := append(protocol.CloneMessages(compactedHistory), memoryMessages...)
	combined = append(combined, promptStateMessages...)
	combined = append(combined, runtimeMessages...)
	postCompactEstimate := estimateContextBudget(system, compactedHistory, memoryMessages, allRuntimeMessages, a.toolHandler.ActiveSchemas(), triggerTokens)
	historyRecall := a.evaluateHistoryRecall(ctx, query, compactedHistory, memoryLayers, compacted)
	toolSchemas := a.activeToolSchemas(agentProfile)
	estimate := estimateContextBudget(system, compactedHistory, memoryMessages, allRuntimeMessages, toolSchemas, triggerTokens)
	reasons := estimate.Reasons
	compactionBefore := 0
	compactionAfter := 0
	if compacted {
		compactionBefore = preliminary.Breakdown.Total
		compactionAfter = postCompactEstimate.Breakdown.Total
		if len(preliminary.Reasons) > 0 {
			reasons = preliminary.Reasons
		}
	}

	return &BuildContextResult{
		System:                system,
		Messages:              combined,
		ToolSchemas:           toolSchemas,
		TokenEstimate:         estimate.Breakdown.Total,
		TokenBreakdown:        estimate.Breakdown,
		CompressionReasons:    reasons,
		Compacted:             compacted,
		CompactionBefore:      compactionBefore,
		CompactionAfter:       compactionAfter,
		PreCompactionTotal:    preliminary.Breakdown.Total,
		PostCompactionTotal:   postCompactEstimate.Breakdown.Total,
		CompactionMode:        compactionDiag.Mode,
		CompactionLatencyMS:   compactionDiag.LatencyMS,
		LargestContextSources: largestContextSources(estimate.Breakdown),
		AckRuntime:            ackRuntime,
		HistoryRecall:         historyRecall,
	}, nil
}

func agentProfileFromContext(ctx context.Context) string {
	return config.NormalizeAgentProfile(tools.SessionContextFromContext(ctx).AgentProfile)
}

func (a *Agent) activeToolSchemas(agentProfile string) []protocol.ToolSchema {
	schemas := a.toolHandler.ActiveSchemas()
	if len(schemas) == 0 {
		return nil
	}

	blocked := make(map[string]struct{}, 16)
	if !a.hasActiveSkills() {
		blocked["expand_skill"] = struct{}{}
		blocked["unload_skill"] = struct{}{}
	}
	hasMemoryCandidates := a.hasMemoryCandidates()
	if !hasMemoryCandidates {
		blocked["accept_memory_candidate"] = struct{}{}
		blocked["dismiss_memory_candidate"] = struct{}{}
	}
	if config.NormalizeAgentProfile(agentProfile) == config.AgentProfileCoding {
		applyCodingProfileToolFilter(blocked, hasMemoryCandidates)
	}
	if len(blocked) == 0 {
		return schemas
	}

	filtered := make([]protocol.ToolSchema, 0, len(schemas))
	for _, schema := range schemas {
		if _, ok := blocked[schema.Name]; ok {
			continue
		}
		filtered = append(filtered, schema)
	}
	return filtered
}

func applyCodingProfileToolFilter(blocked map[string]struct{}, hasMemoryCandidates bool) {
	if !hasMemoryCandidates {
		blocked["accept_memory_candidate"] = struct{}{}
		blocked["dismiss_memory_candidate"] = struct{}{}
	}
}

func (a *Agent) hasActiveSkills() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.activeSkills) > 0
}

func (a *Agent) hasMemoryCandidates() bool {
	if a.memoryMgr == nil {
		return false
	}
	count, err := a.memoryMgr.CandidateCount()
	if err != nil {
		return true
	}
	return count > 0
}

func (a *Agent) activateImplicitBundlesForQuery(query string) []string {
	bundles := implicitBundlesForQuery(query)
	if len(bundles) == 0 || a == nil || a.toolHandler == nil {
		return nil
	}
	return a.toolHandler.ActivateBundles(bundles...)
}

func implicitBundlesForQuery(query string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	var bundles []string
	if looksLikeExplicitWebQuery(query) || looksLikeCurrentInformationQuery(query) {
		bundles = append(bundles, bundleWeb)
	}
	return bundles
}

func looksLikeExplicitWebQuery(query string) bool {
	return containsAny(query,
		"网络搜索", "网页搜索", "上网搜索", "网上搜索", "上网查", "网上查", "联网搜索", "联网查",
		"查一下网页", "查网页", "搜索网页", "访问网页", "打开网页", "抓取网页", "读取网页",
		"web search", "search web", "search the web", "internet search", "online search",
		"brave search", "fetch url", "fetch the url",
	) || containsAnyASCIIWord(query, "google", "bing", "duckduckgo")
}

func looksLikeCurrentInformationQuery(query string) bool {
	if containsAny(query,
		"天气", "天气预报", "气温", "降雨", "下雨", "空气质量", "aqi", "台风",
		"weather", "forecast", "temperature", "rain", "air quality", "typhoon",
	) {
		return true
	}
	hasRecencyCue := containsAny(query,
		"今天", "今日", "现在", "当前", "实时", "最新", "最近", "刚刚", "明天",
		"today", "now", "current", "latest", "recent", "tomorrow",
	)
	if !hasRecencyCue {
		return false
	}
	return containsAny(query,
		"新闻", "股价", "股票", "汇率", "价格", "票房", "赛程", "比分", "赛事", "航班", "路况", "状态", "版本",
		"news", "stock", "price", "exchange rate", "currency", "score", "schedule", "flight", "traffic", "status", "version", "release",
	)
}

func containsAny(query string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(query, needle) {
			return true
		}
	}
	return false
}

func containsAnyASCIIWord(query string, words ...string) bool {
	for _, word := range words {
		word = strings.ToLower(strings.TrimSpace(word))
		if word != "" && containsASCIIWord(query, word) {
			return true
		}
	}
	return false
}

func containsASCIIWord(query, word string) bool {
	if query == "" || word == "" {
		return false
	}
	start := 0
	for {
		index := strings.Index(query[start:], word)
		if index < 0 {
			return false
		}
		index += start
		beforeOK := index == 0 || !isASCIIAlphaNum(query[index-1])
		afterIndex := index + len(word)
		afterOK := afterIndex >= len(query) || !isASCIIAlphaNum(query[afterIndex])
		if beforeOK && afterOK {
			return true
		}
		start = index + 1
	}
}

func isASCIIAlphaNum(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9')
}

func (a *Agent) evaluateHistoryRecall(ctx context.Context, query string, history []protocol.Message, layers memory.ContextLayers, compacted bool) *tools.HistoryRecallEvaluation {
	if strings.TrimSpace(query) == "" || a.historySearch == nil {
		if state := historyRecallTurnStateFromContext(ctx); state != nil {
			state.setAutomaticExposure(false)
		}
		return nil
	}

	policy := historySearchPolicyFromConfig(a.cfg.Tools.History)

	transcriptRefs := a.TranscriptRefs()
	historyWasCompacted := compacted || hasSummaryMessage(history)
	historyWasCleared := len(transcriptRefs) > 0 && !hasSummaryMessage(history)
	currentContextSufficient := !historyWasCompacted && !historyWasCleared && len(history) > 0
	runtimeCtx := tools.SessionContextFromContext(ctx)
	alreadyUsed := 0
	if state := historyRecallTurnStateFromContext(ctx); state != nil {
		alreadyUsed = state.automaticUses()
		defer state.setAutomaticExposure(false)
	}

	eval := tools.EvaluateHistoryRecall(policy, tools.HistoryRecallEvaluationInput{
		Query:                    query,
		SessionSource:            runtimeCtx.Source,
		CurrentContextSufficient: currentContextSufficient,
		StrongMemoryHit:          len(layers.Relevant) > 0,
		AlreadyUsedThisTurn:      alreadyUsed,
		HistoryWasCleared:        historyWasCleared,
		HistoryWasCompacted:      historyWasCompacted,
	})
	if state := historyRecallTurnStateFromContext(ctx); state != nil {
		state.setAutomaticExposure(eval.Automatic)
	}
	return &eval
}

func hasSummaryMessage(messages []protocol.Message) bool {
	for _, msg := range messages {
		if msg.Metadata != nil && msg.Metadata.Kind == protocol.KindSummary {
			return true
		}
	}
	return false
}

func historySearchPolicyFromConfig(cfg config.HistorySearchConfig) tools.HistorySearchPolicy {
	defaults := tools.DefaultHistorySearchPolicy()
	if !cfg.Enabled &&
		!cfg.Auto.Enabled &&
		cfg.Auto.MaxPerTurn == 0 &&
		strings.TrimSpace(cfg.Auto.DefaultScope) == "" &&
		!cfg.Auto.AllowArchiveOnClear &&
		!cfg.Auto.AllowArchiveOnCompact &&
		!cfg.Auto.AllowAllArchivesAutomatic &&
		cfg.Auto.MinScore == 0 &&
		len(cfg.Cues.Explicit) == 0 &&
		len(cfg.Cues.Implicit) == 0 &&
		len(cfg.Blocks.SessionSources) == 0 {
		return defaults
	}
	autoConfigured := cfg.Auto.MaxPerTurn > 0 ||
		strings.TrimSpace(cfg.Auto.DefaultScope) != "" ||
		cfg.Auto.AllowArchiveOnClear ||
		cfg.Auto.AllowArchiveOnCompact ||
		cfg.Auto.AllowAllArchivesAutomatic ||
		cfg.Auto.MinScore > 0
	policy := tools.HistorySearchPolicy{
		Enabled: cfg.Enabled,
		Auto: tools.HistorySearchAutoPolicy{
			Enabled:                   cfg.Auto.Enabled,
			MaxPerTurn:                cfg.Auto.MaxPerTurn,
			DefaultScope:              strings.TrimSpace(cfg.Auto.DefaultScope),
			AllowArchiveOnClear:       cfg.Auto.AllowArchiveOnClear,
			AllowArchiveOnCompact:     cfg.Auto.AllowArchiveOnCompact,
			AllowAllArchivesAutomatic: cfg.Auto.AllowAllArchivesAutomatic,
			MinScore:                  cfg.Auto.MinScore,
		},
		Cues: tools.HistorySearchCuePolicy{
			Explicit: append([]string{}, cfg.Cues.Explicit...),
			Implicit: append([]string{}, cfg.Cues.Implicit...),
		},
		Blocks: tools.HistorySearchBlockPolicy{
			SessionSources: append([]string{}, cfg.Blocks.SessionSources...),
		},
	}
	if !cfg.Enabled {
		policy.Enabled = defaults.Enabled
	}
	if !cfg.Auto.Enabled && autoConfigured {
		policy.Auto.Enabled = defaults.Auto.Enabled
	}
	return policy
}

func (a *Agent) buildSystemPrompt() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.prompts.Clone().Build()
}

func (a *Agent) buildRuntimeSystemPrompt(agentProfile ...string) (string, error) {
	profile := config.AgentProfileGeneral
	if len(agentProfile) > 0 {
		profile = config.NormalizeAgentProfile(agentProfile[0])
	}
	dynamic, err := a.buildDynamicSystemPrompt(profile)
	if err != nil {
		return "", err
	}
	return strings.Join(filterNonEmpty(
		a.buildSystemPrompt(),
		conciseDefaultResponsePrompt,
		dynamic,
	), "\n\n"), nil
}

const conciseDefaultResponsePrompt = "Default response style: be concise unless the user asks for a detailed report. Do not restate stable context, raw tool output, or lengthy process notes unless they directly change the answer."

func (a *Agent) collectRuntimeMessages() ([]protocol.Message, func()) {
	messages := make([]protocol.Message, 0, 2)
	var inboxPreview []message.Message
	var backgroundPreview []background.Notification

	if notifs := a.bgMgr.PeekNotifications(); len(notifs) > 0 {
		backgroundPreview = notifs
		messages = append(messages, protocol.NewEphemeralTextMessage(protocol.KindBackground, formatBackgroundNotifications(notifs, a.now())))
	}

	if inbox := a.msgBus.PeekInbox(a.cfg.LeadName); len(inbox) > 0 {
		inboxPreview = inbox
		messages = append(messages, protocol.NewEphemeralTextMessage(protocol.KindInbox, message.FormatInboxMessages(inbox)))
	}

	ack := func() {
		if len(backgroundPreview) > 0 {
			a.bgMgr.AckNotifications(backgroundPreview)
		}
		if len(inboxPreview) > 0 {
			a.msgBus.AckInbox(a.cfg.LeadName, inboxPreview)
		}
	}

	return messages, ack
}

func formatProjectLedgerRuntimeMessage(ledger string) string {
	return "Long-task project ledger (ephemeral session state; prefer this over guessing older project status):\n" + strings.TrimSpace(ledger)
}

func (a *Agent) collectMemoryMessages(history []protocol.Message) ([]protocol.Message, memory.ContextLayers, error) {
	query := latestPersistentUserText(history)
	layers, err := a.memoryMgr.BuildContextLayers(query)
	if err != nil {
		return nil, memory.ContextLayers{}, err
	}
	if len(layers.Identity) == 0 && len(layers.Core) == 0 && len(layers.Relevant) == 0 {
		return nil, layers, nil
	}

	return []protocol.Message{
		protocol.NewEphemeralTextMessage(protocol.KindMemory, formatMemoryLayers(layers)),
	}, layers, nil
}

func estimateMessages(messages []protocol.Message) int {
	total := 0
	for _, msg := range messages {
		total += estimateMessage(msg)
	}
	return total
}

type contextBudgetEstimate struct {
	Breakdown                     tools.ContextTokenBreakdown
	Reasons                       []string
	LargeToolResultReferenceCount int
	ToolResultReferences          []tools.ToolResultReference
}

const maxContextToolResultReferences = 8

func estimateContextBudget(system string, history, memoryMessages, runtimeMessages []protocol.Message, toolSchemas []protocol.ToolSchema, threshold int) contextBudgetEstimate {
	var estimate contextBudgetEstimate
	estimate.Breakdown.System = compress.CountTokens(system)

	historyBase, historyToolResults, historyAttachments, historyRefs, historyRefCount := estimateMessageSet(history)
	memoryBase, memoryToolResults, memoryAttachments, memoryRefs, memoryRefCount := estimateMessageSet(memoryMessages)
	runtimeBase, runtimeToolResults, runtimeAttachments, runtimeRefs, runtimeRefCount := estimateMessageSet(runtimeMessages)

	estimate.Breakdown.History = historyBase
	estimate.Breakdown.Memory = memoryBase
	estimate.Breakdown.Runtime = runtimeBase
	estimate.Breakdown.ToolResults = historyToolResults + memoryToolResults + runtimeToolResults
	estimate.Breakdown.Attachments = historyAttachments + memoryAttachments + runtimeAttachments
	estimate.Breakdown.ToolSchemas = estimateToolSchemas(toolSchemas)
	estimate.Breakdown.Total = estimate.Breakdown.System +
		estimate.Breakdown.History +
		estimate.Breakdown.Memory +
		estimate.Breakdown.Runtime +
		estimate.Breakdown.ToolSchemas +
		estimate.Breakdown.Attachments +
		estimate.Breakdown.ToolResults

	estimate.ToolResultReferences = appendLimitedToolResultRefs(estimate.ToolResultReferences, historyRefs...)
	estimate.ToolResultReferences = appendLimitedToolResultRefs(estimate.ToolResultReferences, memoryRefs...)
	estimate.ToolResultReferences = appendLimitedToolResultRefs(estimate.ToolResultReferences, runtimeRefs...)
	estimate.LargeToolResultReferenceCount = historyRefCount + memoryRefCount + runtimeRefCount
	estimate.Reasons = compressionReasons(estimate.Breakdown, threshold)
	return estimate
}

func prefixCacheInspection(system string, toolSchemas []protocol.ToolSchema, history []protocol.Message, dynamicSections []runtimePromptSection, dynamicMessages []protocol.Message) tools.PrefixCacheInspection {
	return tools.PrefixCacheInspection{
		SystemHash:           sha256Hex([]byte(system)),
		ToolSchemasHash:      sha256Canonical(toolSchemas),
		StablePrefixHash:     sha256Canonical(stablePrefixCacheInput{System: system, ToolSchemas: toolSchemas, History: history}),
		StableSystemTokens:   compress.CountTokens(system),
		DynamicRuntimeTokens: estimateMessages(dynamicMessages),
		DynamicSectionTokens: runtimePromptSectionTokenMap(dynamicSections),
	}
}

type stablePrefixCacheInput struct {
	System      string                `json:"system"`
	ToolSchemas []protocol.ToolSchema `json:"tool_schemas"`
	History     []protocol.Message    `json:"history"`
}

func sha256Canonical(value interface{}) string {
	data, err := json.Marshal(value)
	if err != nil {
		return sha256Hex([]byte(fmt.Sprintf("%#v", value)))
	}
	return sha256Hex(data)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

func estimateMessageSet(messages []protocol.Message) (baseTokens, toolResultTokens, attachmentTokens int, refs []tools.ToolResultReference, refCount int) {
	for _, msg := range messages {
		base, toolResults, attachments, msgRefs, msgRefCount := estimateMessageParts(msg)
		baseTokens += base
		toolResultTokens += toolResults
		attachmentTokens += attachments
		refs = appendLimitedToolResultRefs(refs, msgRefs...)
		refCount += msgRefCount
	}
	return baseTokens, toolResultTokens, attachmentTokens, refs, refCount
}

func estimateMessageParts(msg protocol.Message) (baseTokens, toolResultTokens, attachmentTokens int, refs []tools.ToolResultReference, refCount int) {
	for _, block := range msg.Content {
		switch block.Type {
		case protocol.BlockText:
			baseTokens += compress.CountTokens(block.Text)
		case protocol.BlockToolUse:
			baseTokens += compress.CountTokens(block.ID)
			baseTokens += compress.CountTokens(block.Name)
			baseTokens += compress.CountTokens(fmt.Sprintf("%v", block.Input))
		case protocol.BlockToolResult:
			toolResultTokens += compress.CountTokens(block.ToolUseID)
			toolResultTokens += compress.CountTokens(block.Content)
			if ref, ok := parseToolResultReference(block); ok {
				refs = appendLimitedToolResultRefs(refs, ref)
				refCount++
			}
		}
	}
	if msg.Metadata != nil {
		baseTokens += compress.CountTokens(string(msg.Metadata.Kind))
		baseTokens += compress.CountTokens(msg.Metadata.Transcript)
		attachmentTokens += estimateMetadataAttachments(msg.Metadata)
	}
	return baseTokens, toolResultTokens, attachmentTokens, refs, refCount
}

func estimateToolSchemas(schemas []protocol.ToolSchema) int {
	total := 0
	for _, schema := range schemas {
		data, err := json.Marshal(schema)
		if err != nil {
			total += compress.CountTokens(schema.Name)
			total += compress.CountTokens(schema.Description)
			total += compress.CountTokens(fmt.Sprintf("%v", schema.InputSchema))
			continue
		}
		total += compress.CountTokens(string(data))
	}
	return total
}

func estimateMetadataAttachments(metadata *protocol.Metadata) int {
	if metadata == nil {
		return 0
	}
	total := 0
	for _, attachment := range metadata.Attachments {
		total += estimateAttachment(attachment)
	}
	for _, part := range metadata.Parts {
		total += compress.CountTokens(part.Type)
		total += compress.CountTokens(part.Text)
		total += compress.CountTokens(part.MIMEType)
		if part.Attachment != nil {
			total += estimateAttachment(*part.Attachment)
		}
	}
	return total
}

func estimateAttachment(attachment protocol.Attachment) int {
	total := 0
	total += compress.CountTokens(attachment.ID)
	total += compress.CountTokens(attachment.Name)
	total += compress.CountTokens(attachment.MIMEType)
	total += compress.CountTokens(attachment.Path)
	total += compress.CountTokens(attachment.URL)
	if attachment.SizeBytes > 0 {
		total += compress.CountTokens(fmt.Sprintf("%d", attachment.SizeBytes))
	}
	return total
}

func compressionReasons(breakdown tools.ContextTokenBreakdown, threshold int) []string {
	if threshold <= 0 {
		return nil
	}
	reasons := make([]string, 0, 3)
	if breakdown.History > threshold {
		reasons = append(reasons, "history_over_threshold", "history_compactable")
	}
	if breakdown.Total > threshold {
		reasons = append(reasons, "total_over_threshold")
		if historyHasCompactableContent(breakdown.History, threshold) && !containsCompressionReason(reasons, "history_compactable") {
			reasons = append(reasons, "history_compactable")
		}
	}
	return reasons
}

func tokenBreakdownMap(breakdown tools.ContextTokenBreakdown) map[string]int {
	return map[string]int{
		"system":       breakdown.System,
		"history":      breakdown.History,
		"memory":       breakdown.Memory,
		"runtime":      breakdown.Runtime,
		"tool_schemas": breakdown.ToolSchemas,
		"attachments":  breakdown.Attachments,
		"tool_results": breakdown.ToolResults,
		"total":        breakdown.Total,
	}
}

func recentPersistentUserMessages(messages []protocol.Message, limit int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, limit)
	for i := len(messages) - 1; i >= 0 && len(out) < limit; i-- {
		msg := messages[i]
		if msg.Role != protocol.RoleUser {
			continue
		}
		if msg.Metadata != nil && msg.Metadata.Ephemeral {
			continue
		}
		text := strings.TrimSpace(protocol.MessageText(msg))
		if text == "" {
			continue
		}
		out = append(out, text)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func shouldAutoCompact(estimate contextBudgetEstimate, threshold int) bool {
	if threshold <= 0 {
		return false
	}
	if estimate.Breakdown.History > threshold {
		return true
	}
	return estimate.Breakdown.Total > threshold && historyHasCompactableContent(estimate.Breakdown.History, threshold)
}

func historyHasCompactableContent(historyTokens, threshold int) bool {
	if threshold <= 0 || historyTokens <= 0 {
		return false
	}
	if historyTokens > threshold/2 {
		return true
	}
	floor := threshold / 4
	if floor > 12000 {
		floor = 12000
	}
	if floor < 256 {
		floor = 256
	}
	return historyTokens >= floor
}

func appendLimitedToolResultRefs(existing []tools.ToolResultReference, incoming ...tools.ToolResultReference) []tools.ToolResultReference {
	for _, ref := range incoming {
		if len(existing) >= maxContextToolResultReferences {
			break
		}
		if strings.TrimSpace(ref.ToolUseID) == "" && strings.TrimSpace(ref.ArtifactPath) == "" && strings.TrimSpace(ref.SHA256) == "" {
			continue
		}
		existing = append(existing, ref)
	}
	return existing
}

func parseToolResultReference(block protocol.Block) (tools.ToolResultReference, bool) {
	var payload struct {
		Status       string `json:"status"`
		ToolName     string `json:"tool_name"`
		ToolUseID    string `json:"tool_use_id"`
		Bytes        int    `json:"bytes"`
		SHA256       string `json:"sha256"`
		ArtifactPath string `json:"artifact_path"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(block.Content)), &payload); err != nil {
		return tools.ToolResultReference{}, false
	}
	if payload.Status != "tool_result_truncated" && payload.ArtifactPath == "" && payload.SHA256 == "" {
		return tools.ToolResultReference{}, false
	}
	ref := tools.ToolResultReference{
		ToolName:     strings.TrimSpace(payload.ToolName),
		ToolUseID:    strings.TrimSpace(payload.ToolUseID),
		Bytes:        payload.Bytes,
		SHA256:       strings.TrimSpace(payload.SHA256),
		ArtifactPath: strings.TrimSpace(payload.ArtifactPath),
	}
	if ref.ToolUseID == "" {
		ref.ToolUseID = strings.TrimSpace(block.ToolUseID)
	}
	return ref, true
}

func dedupeRepeatedLargeToolResultSummaries(messages []protocol.Message) []protocol.Message {
	if len(messages) == 0 {
		return messages
	}
	seen := make(map[string]struct{})
	var out []protocol.Message
	for msgIdx, msg := range messages {
		for blockIdx, block := range msg.Content {
			if block.Type != protocol.BlockToolResult {
				continue
			}
			ref, ok := parseToolResultReference(block)
			if !ok {
				continue
			}
			key := toolResultReferenceKey(ref)
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				if out == nil {
					out = protocol.CloneMessages(messages)
				}
				out[msgIdx].Content[blockIdx].Content = modelcontext.SummaryJSON(modelcontext.LargeToolResultSummary{
					Status:       "tool_result_duplicate",
					ToolName:     ref.ToolName,
					ToolUseID:    ref.ToolUseID,
					Bytes:        ref.Bytes,
					SHA256:       ref.SHA256,
					ArtifactPath: ref.ArtifactPath,
					Note:         "Duplicate large tool result summary; use the earlier matching artifact reference for the full output.",
				})
				continue
			}
			seen[key] = struct{}{}
		}
	}
	if out != nil {
		return out
	}
	return messages
}

func toolResultReferenceKey(ref tools.ToolResultReference) string {
	if sha := strings.TrimSpace(ref.SHA256); sha != "" {
		return "sha256:" + sha
	}
	if path := strings.TrimSpace(ref.ArtifactPath); path != "" {
		return "artifact:" + path
	}
	return ""
}

func estimateMessage(msg protocol.Message) int {
	total := 0
	for _, block := range msg.Content {
		switch block.Type {
		case protocol.BlockText:
			total += compress.CountTokens(block.Text)
		case protocol.BlockToolUse:
			total += compress.CountTokens(block.ID)
			total += compress.CountTokens(block.Name)
			total += compress.CountTokens(fmt.Sprintf("%v", block.Input))
		case protocol.BlockToolResult:
			total += compress.CountTokens(block.ToolUseID)
			total += compress.CountTokens(block.Content)
		}
	}
	if msg.Metadata != nil {
		total += compress.CountTokens(string(msg.Metadata.Kind))
		total += compress.CountTokens(msg.Metadata.Transcript)
	}
	return total
}

func containsCompressionReason(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func formatBackgroundNotifications(notifs []background.Notification, now time.Time) string {
	envelopes := make([]message.Envelope, 0, len(notifs))
	for _, notif := range notifs {
		content := fmt.Sprintf("%s: %s", notif.Status, notif.Result)
		if notif.Error != "" {
			content = fmt.Sprintf("%s (error: %s)", content, notif.Error)
		}
		envelopes = append(envelopes, message.NewRuntimeEnvelope(
			message.SourceBackground,
			"",
			notif.TaskID,
			content,
			now,
			map[string]string{"task_id": notif.TaskID, "status": notif.Status, "error": notif.Error},
		))
	}
	return message.FormatEnvelopes("Background task updates", envelopes)
}

func latestPersistentUserText(messages []protocol.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != protocol.RoleUser {
			continue
		}
		if msg.Metadata != nil && msg.Metadata.Ephemeral {
			continue
		}
		text := strings.TrimSpace(protocol.MessageText(msg))
		if text != "" {
			return text
		}
	}
	return ""
}

func formatMemoryLayers(layers memory.ContextLayers) string {
	var builder strings.Builder

	builder.WriteString("Memory context:\n")
	if len(layers.Identity) > 0 {
		builder.WriteString("L0 identity:\n")
		appendMemorySection(&builder, layers.Identity, false)
	}
	if len(layers.Core) > 0 {
		if len(layers.Identity) > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString("Core project memory:\n")
		appendMemorySection(&builder, layers.Core, false)
	}
	if len(layers.Relevant) > 0 {
		if len(layers.Identity) > 0 || len(layers.Core) > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString("Relevant recall for the current request:\n")
		appendMemorySection(&builder, layers.Relevant, true)
	}
	return strings.TrimRight(builder.String(), "\n")
}

const maxRelevantMemoryContentRunes = 800

func appendMemorySection(builder *strings.Builder, memories []memory.RelevantMemory, includeContent bool) {
	for _, mem := range memories {
		builder.WriteString(fmt.Sprintf("- %s [%s] (%s): %s\n", mem.Title, mem.Type, mem.File, mem.Summary))
		if content := strings.TrimSpace(mem.Content); includeContent && content != "" {
			builder.WriteString("  ")
			builder.WriteString(strings.ReplaceAll(truncateRunes(content, maxRelevantMemoryContentRunes), "\n", "\n  "))
			builder.WriteString("\n")
		}
	}
}

func truncateRunes(input string, limit int) string {
	if limit <= 0 {
		return input
	}
	runes := []rune(input)
	if len(runes) <= limit {
		return input
	}
	return string(runes[:limit]) + "..."
}
