package toolruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/platform/tooling"
)

// ToolSpec describes one tool's public declaration and normalization rules.
type ToolSpec struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
	Aliases     map[string]string      `json:"aliases,omitempty"`
}

// ToolCall is the normalized input envelope passed through the tool runtime.
type ToolCall struct {
	Name            string                    `json:"name"`
	RawInput        map[string]interface{}    `json:"raw_input,omitempty"`
	NormalizedInput map[string]interface{}    `json:"normalized_input,omitempty"`
	SessionContext  automation.SessionContext `json:"session_context,omitempty"`
	DecodedInput    any                       `json:"-"`
}

// ToolResult is the structured result emitted by one tool execution.
type ToolResult struct {
	Text          string                 `json:"text,omitempty"`
	Structured    any                    `json:"structured,omitempty"`
	ArtifactPaths []string               `json:"artifact_paths,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

// OutputString serializes the user/model-visible portion of the result.
func (r ToolResult) OutputString() (string, error) {
	return serializeToolResult(r)
}

// BeforeInterceptor runs before the tool body. Returning a non-nil ToolResult
// short-circuits execution and skips the tool body.
type BeforeInterceptor func(context.Context, *ToolCall) (*ToolResult, error)

// AfterInterceptor can replace the current result and/or error after tool execution.
type AfterInterceptor func(context.Context, *ToolCall, ToolResult, error) (ToolResult, error)

// Tool defines the sealed typed-runtime tool contract.
type Tool interface {
	Name() string
	Description() string
	Spec() ToolSpec
	Execute(ctx context.Context, args map[string]interface{}) (string, error)
	prepare(raw map[string]interface{}, sessionCtx automation.SessionContext) (ToolCall, error)
	refresh(call *ToolCall) error
	invoke(ctx context.Context, call *ToolCall) (ToolResult, error)
}

type typedTool[T any] struct {
	spec ToolSpec
	run  func(context.Context, T) (ToolResult, error)
}

// NewTypedTool creates a new typed tool backed by the shared runtime.
func NewTypedTool[T any](spec ToolSpec, run func(context.Context, T) (ToolResult, error)) Tool {
	return &typedTool[T]{
		spec: normalizeToolSpec(spec),
		run:  run,
	}
}

// SpecFromDefinition converts a tooling definition into a runtime spec.
func SpecFromDefinition(def tooling.Definition, aliases map[string]string) ToolSpec {
	return ToolSpec{
		Name:        def.Name,
		Description: def.Description,
		InputSchema: cloneStringAnyMap(def.InputSchema),
		Aliases:     cloneStringStringMap(aliases),
	}
}

// NewToolSpec builds a runtime spec from raw schema data.
func NewToolSpec(name, description string, inputSchema map[string]interface{}, aliases map[string]string) ToolSpec {
	return ToolSpec{
		Name:        strings.TrimSpace(name),
		Description: strings.TrimSpace(description),
		InputSchema: cloneStringAnyMap(inputSchema),
		Aliases:     cloneStringStringMap(aliases),
	}
}

func (s ToolSpec) Schema() map[string]interface{} {
	return map[string]interface{}{
		"name":         s.Name,
		"description":  s.Description,
		"input_schema": cloneStringAnyMap(s.InputSchema),
	}
}

func (s ToolSpec) ToolSchema() protocol.ToolSchema {
	return protocol.ToolSchema{
		Name:        s.Name,
		Description: s.Description,
		InputSchema: cloneStringAnyMap(s.InputSchema),
	}
}

func (t *typedTool[T]) Name() string {
	return t.spec.Name
}

func (t *typedTool[T]) Description() string {
	return t.spec.Description
}

func (t *typedTool[T]) Spec() ToolSpec {
	return ToolSpec{
		Name:        t.spec.Name,
		Description: t.spec.Description,
		InputSchema: cloneStringAnyMap(t.spec.InputSchema),
		Aliases:     cloneStringStringMap(t.spec.Aliases),
	}
}

func (t *typedTool[T]) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	return executeToolRuntime(ctx, t, args, nil, nil)
}

func (t *typedTool[T]) prepare(raw map[string]interface{}, sessionCtx automation.SessionContext) (ToolCall, error) {
	rawInput := cloneStringAnyMap(raw)
	normalized := cloneStringAnyMap(raw)
	applyAliases(normalized, t.spec.Aliases)
	coerced, err := coerceObject(normalized, t.spec.InputSchema)
	if err != nil {
		return ToolCall{}, err
	}
	call := ToolCall{
		Name:            t.spec.Name,
		RawInput:        rawInput,
		NormalizedInput: coerced,
		SessionContext:  sessionCtx.Clone(),
	}
	if err := t.refresh(&call); err != nil {
		return ToolCall{}, err
	}
	return call, nil
}

func (t *typedTool[T]) refresh(call *ToolCall) error {
	coerced, err := coerceObject(call.NormalizedInput, t.spec.InputSchema)
	if err != nil {
		return err
	}
	call.NormalizedInput = coerced
	decoded, err := decodeTypedInput[T](call.NormalizedInput)
	if err != nil {
		return err
	}
	call.DecodedInput = decoded
	return nil
}

func (t *typedTool[T]) invoke(ctx context.Context, call *ToolCall) (ToolResult, error) {
	decoded, err := decodeTypedInput[T](call.NormalizedInput)
	if err != nil {
		return ToolResult{}, err
	}
	call.DecodedInput = decoded
	return t.run(ctx, decoded)
}

func executeToolRuntime(ctx context.Context, tool Tool, args map[string]interface{}, before []BeforeInterceptor, after []AfterInterceptor) (string, error) {
	result, err := executeToolRuntimeResult(ctx, tool, args, before, after)
	if err != nil {
		return "", err
	}
	return serializeToolResult(result)
}

func executeToolRuntimeResult(ctx context.Context, tool Tool, args map[string]interface{}, before []BeforeInterceptor, after []AfterInterceptor) (ToolResult, error) {
	call, err := tool.prepare(args, SessionContextFromContext(ctx))
	if err != nil {
		return ToolResult{}, err
	}

	var (
		result         ToolResult
		execErr        error
		shortCircuited bool
	)

	for _, interceptor := range before {
		if interceptor == nil {
			continue
		}
		override, err := interceptor(ctx, &call)
		if err != nil {
			execErr = err
			break
		}
		if override != nil {
			result = cloneToolResult(*override)
			shortCircuited = true
			break
		}
		if err := tool.refresh(&call); err != nil {
			execErr = err
			break
		}
	}

	if execErr == nil && !shortCircuited {
		result, execErr = tool.invoke(ctx, &call)
	}

	for _, interceptor := range after {
		if interceptor == nil {
			continue
		}
		result, execErr = interceptor(ctx, &call, result, execErr)
	}

	if execErr != nil {
		return cloneToolResult(result), execErr
	}
	return cloneToolResult(result), nil
}

func serializeToolResult(result ToolResult) (string, error) {
	if result.Text != "" {
		return result.Text, nil
	}
	if result.Structured != nil {
		data, err := json.Marshal(result.Structured)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return "OK", nil
}

func decodeTypedInput[T any](input map[string]interface{}) (T, error) {
	var target T
	data, err := json.Marshal(input)
	if err != nil {
		return target, err
	}
	if err := json.Unmarshal(data, &target); err != nil {
		return target, err
	}
	return target, nil
}

func normalizeToolSpec(spec ToolSpec) ToolSpec {
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Description = strings.TrimSpace(spec.Description)
	spec.InputSchema = cloneStringAnyMap(spec.InputSchema)
	spec.Aliases = cloneStringStringMap(spec.Aliases)
	return spec
}

func applyAliases(args map[string]interface{}, aliases map[string]string) {
	for from, to := range aliases {
		from = strings.TrimSpace(from)
		to = strings.TrimSpace(to)
		if from == "" || to == "" || from == to {
			continue
		}
		value, ok := args[from]
		if !ok {
			continue
		}
		if _, exists := args[to]; !exists {
			args[to] = value
		}
		delete(args, from)
	}
}

func coerceObject(args map[string]interface{}, schema map[string]interface{}) (map[string]interface{}, error) {
	props := schemaProperties(schema)
	if len(props) == 0 {
		return cloneStringAnyMap(args), nil
	}
	out := cloneStringAnyMap(args)
	for key, value := range out {
		propSchema, ok := props[key]
		if !ok {
			continue
		}
		coerced, err := coerceValue(value, propSchema, key)
		if err != nil {
			return nil, err
		}
		out[key] = coerced
	}
	return out, nil
}

func coerceValue(value any, schema map[string]interface{}, path string) (any, error) {
	switch schemaType(schema) {
	case "integer":
		return coerceInteger(value, path)
	case "number":
		return coerceNumber(value, path)
	case "boolean":
		return coerceBoolean(value, path)
	case "array":
		return coerceArray(value, schema, path)
	case "object":
		return coerceMap(value, schema, path)
	default:
		return value, nil
	}
}

func coerceInteger(value any, path string) (any, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int8:
		return int(typed), nil
	case int16:
		return int(typed), nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case float32:
		return int(typed), nil
	case float64:
		return int(typed), nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return nil, fmt.Errorf("%s must be an integer: %w", path, err)
		}
		return int(parsed), nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return nil, fmt.Errorf("%s must be an integer: %w", path, err)
		}
		return parsed, nil
	default:
		return value, nil
	}
}

func coerceNumber(value any, path string) (any, error) {
	switch typed := value.(type) {
	case int:
		return float64(typed), nil
	case int8:
		return float64(typed), nil
	case int16:
		return float64(typed), nil
	case int32:
		return float64(typed), nil
	case int64:
		return float64(typed), nil
	case float32:
		return float64(typed), nil
	case float64:
		return typed, nil
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return nil, fmt.Errorf("%s must be a number: %w", path, err)
		}
		return parsed, nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return nil, fmt.Errorf("%s must be a number: %w", path, err)
		}
		return parsed, nil
	default:
		return value, nil
	}
}

func coerceBoolean(value any, path string) (any, error) {
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes":
			return true, nil
		case "false", "0", "no":
			return false, nil
		default:
			return nil, fmt.Errorf("%s must be a boolean", path)
		}
	default:
		return value, nil
	}
}

func coerceArray(value any, schema map[string]interface{}, path string) (any, error) {
	itemSchema := schemaMap(schema["items"])
	items := make([]interface{}, 0)
	switch typed := value.(type) {
	case string:
		if err := json.Unmarshal([]byte(strings.TrimSpace(typed)), &items); err != nil {
			return nil, fmt.Errorf("%s must be a JSON array: %w", path, err)
		}
	case []interface{}:
		items = append(items, typed...)
	case []string:
		for _, item := range typed {
			items = append(items, item)
		}
	case []int:
		for _, item := range typed {
			items = append(items, item)
		}
	default:
		return value, nil
	}
	if len(itemSchema) == 0 {
		return items, nil
	}
	for i, item := range items {
		coerced, err := coerceValue(item, itemSchema, fmt.Sprintf("%s[%d]", path, i))
		if err != nil {
			return nil, err
		}
		items[i] = coerced
	}
	return items, nil
}

func coerceMap(value any, schema map[string]interface{}, path string) (any, error) {
	var object map[string]interface{}
	switch typed := value.(type) {
	case map[string]interface{}:
		object = cloneStringAnyMap(typed)
	case map[string]string:
		object = make(map[string]interface{}, len(typed))
		for key, item := range typed {
			object[key] = item
		}
	case string:
		if err := json.Unmarshal([]byte(strings.TrimSpace(typed)), &object); err != nil {
			return nil, fmt.Errorf("%s must be a JSON object: %w", path, err)
		}
	default:
		return value, nil
	}

	props := schemaProperties(schema)
	for key, item := range object {
		propSchema, ok := props[key]
		if !ok {
			continue
		}
		coerced, err := coerceValue(item, propSchema, joinSchemaPath(path, key))
		if err != nil {
			return nil, err
		}
		object[key] = coerced
	}
	return object, nil
}

func schemaProperties(schema map[string]interface{}) map[string]map[string]interface{} {
	raw, ok := schema["properties"]
	if !ok || raw == nil {
		return nil
	}
	propsMap, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	props := make(map[string]map[string]interface{}, len(propsMap))
	for key, item := range propsMap {
		props[key] = schemaMap(item)
	}
	return props
}

func schemaType(schema map[string]interface{}) string {
	if value, ok := schema["type"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func schemaMap(value any) map[string]interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return typed
	case map[string]string:
		result := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			result[key] = item
		}
		return result
	default:
		return nil
	}
}

func cloneToolResult(result ToolResult) ToolResult {
	return ToolResult{
		Text:          result.Text,
		Structured:    cloneJSONValue(result.Structured),
		ArtifactPaths: append([]string{}, result.ArtifactPaths...),
		Metadata:      cloneStringAnyMap(result.Metadata),
	}
}

func cloneStringAnyMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		out[key] = cloneJSONValue(value)
	}
	return out
}

func cloneStringStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneStringAnyMap(typed)
	case map[string]string:
		out := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneJSONValue(item))
		}
		return out
	case []string:
		out := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	case []int:
		out := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	default:
		return typed
	}
}

func joinSchemaPath(base, field string) string {
	if base == "" {
		return field
	}
	return base + "." + field
}
