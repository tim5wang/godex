package flow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

const maxJSONSchemaNestingDepth = 32

// ValidateInputValues checks inputs against the variables declared by a Flow.
// Flows without input declarations retain their legacy open-input behavior.
func ValidateInputValues(def *Definition, values map[string]any) error {
	if def == nil || len(def.Inputs) == 0 {
		return nil
	}
	return ValidateVariableValues(def.Inputs, values, "inputs", true)
}

// ValidateVariableValues validates declared object fields. When
// rejectUnknown is true, fields not present in defs are rejected.
func ValidateVariableValues(defs []VarDef, values map[string]any, path string, rejectUnknown bool) error {
	declared := make(map[string]VarDef, len(defs))
	for _, def := range defs {
		name := strings.TrimSpace(def.Name)
		if name != "" {
			declared[name] = def
		}
	}
	if rejectUnknown {
		for name := range values {
			if _, ok := declared[name]; !ok {
				return fmt.Errorf("%s.%s is not declared", path, name)
			}
		}
	}
	for _, def := range defs {
		name := strings.TrimSpace(def.Name)
		value, exists := values[name]
		if !exists {
			if def.Required {
				return fmt.Errorf("%s.%s is required", path, name)
			}
			continue
		}
		if err := validateVariableValue(def, value, path+"."+name); err != nil {
			return err
		}
	}
	return nil
}

// ValidateJSONSchemaValue validates a value against the JSON-Schema subset
// supported for Flow variables and function-node contracts.
func ValidateJSONSchemaValue(value any, schema json.RawMessage, path string) error {
	decoded, err := decodeSchemaDefinition(schema, path+".schema")
	if err != nil || decoded == nil {
		return err
	}
	return validateSchemaValue(value, decoded, path, 0)
}

func decodeSchemaDefinition(raw json.RawMessage, path string) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return nil, fmt.Errorf("%s schema is invalid JSON: %w", path, err)
	}
	if decoded == nil {
		return nil, fmt.Errorf("%s schema must be an object", path)
	}
	if err := validateSchemaDefinition(decoded, path); err != nil {
		return nil, err
	}
	return decoded, nil
}

func validateVariableValue(def VarDef, value any, path string) error {
	typ := strings.ToLower(strings.TrimSpace(def.Type))
	if typ != "" && typ != "any" && !valueHasJSONType(value, typ) {
		return fmt.Errorf("%s must be %s, got %s", path, typ, jsonTypeName(value))
	}
	return ValidateJSONSchemaValue(value, def.Schema, path)
}

func validateSchemaDefinition(schema map[string]any, path string) error {
	return validateSchemaDefinitionAtDepth(schema, path, 0)
}

func validateSchemaDefinitionAtDepth(schema map[string]any, path string, depth int) error {
	if depth > maxJSONSchemaNestingDepth {
		return fmt.Errorf("%s exceeds the maximum schema nesting depth", path)
	}
	if typ, ok := schema["type"]; ok {
		typeName, ok := typ.(string)
		if !ok || !supportedJSONType(strings.ToLower(typeName)) {
			return fmt.Errorf("%s.type must be one of object, array, string, number, integer, boolean, null", path)
		}
	}
	if required, ok := schema["required"]; ok {
		items, ok := required.([]any)
		if !ok {
			return fmt.Errorf("%s.required must be an array of strings", path)
		}
		for i, item := range items {
			if _, ok := item.(string); !ok {
				return fmt.Errorf("%s.required[%d] must be a string", path, i)
			}
		}
	}
	if properties, ok := schema["properties"]; ok {
		props, ok := properties.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.properties must be an object", path)
		}
		for name, child := range props {
			childSchema, ok := child.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.properties.%s must be an object", path, name)
			}
			if err := validateSchemaDefinitionAtDepth(childSchema, path+".properties."+name, depth+1); err != nil {
				return err
			}
		}
	}
	if items, ok := schema["items"]; ok {
		itemSchema, ok := items.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.items must be an object", path)
		}
		if err := validateSchemaDefinitionAtDepth(itemSchema, path+".items", depth+1); err != nil {
			return err
		}
	}
	if additional, ok := schema["additionalProperties"]; ok {
		if _, boolOK := additional.(bool); !boolOK {
			if child, mapOK := additional.(map[string]any); !mapOK {
				return fmt.Errorf("%s.additionalProperties must be a boolean or schema object", path)
			} else if err := validateSchemaDefinitionAtDepth(child, path+".additionalProperties", depth+1); err != nil {
				return err
			}
		}
	}
	if enum, ok := schema["enum"]; ok {
		if _, ok := enum.([]any); !ok {
			return fmt.Errorf("%s.enum must be an array", path)
		}
	}
	// This validator intentionally implements a small, explicit subset rather
	// than silently claiming full JSON Schema support.
	for key := range schema {
		switch key {
		case "type", "description", "properties", "required", "items", "enum", "additionalProperties":
		default:
			return fmt.Errorf("%s.%s is not supported by the Flow schema validator", path, key)
		}
	}
	return nil
}

func validateSchemaValue(value any, schema map[string]any, path string, depth int) error {
	if depth > maxJSONSchemaNestingDepth {
		return fmt.Errorf("%s exceeds the maximum schema nesting depth", path)
	}
	if typ, ok := schema["type"].(string); ok && !valueHasJSONType(value, strings.ToLower(typ)) {
		return fmt.Errorf("%s must be %s, got %s", path, typ, jsonTypeName(value))
	}
	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			if jsonValuesEqual(value, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s must match one of the declared enum values", path)
		}
	}
	switch current := value.(type) {
	case map[string]any:
		if required, ok := schema["required"].([]any); ok {
			for _, item := range required {
				name := item.(string)
				if _, exists := current[name]; !exists {
					return fmt.Errorf("%s.%s is required", path, name)
				}
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for name, child := range current {
			childSchema, declared := properties[name]
			if !declared {
				switch additional := schema["additionalProperties"].(type) {
				case bool:
					if !additional {
						return fmt.Errorf("%s.%s is not allowed", path, name)
					}
				case map[string]any:
					if err := validateSchemaValue(child, additional, path+"."+name, depth+1); err != nil {
						return err
					}
				}
				continue
			}
			if err := validateSchemaValue(child, childSchema.(map[string]any), path+"."+name, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for i, item := range current {
				if err := validateSchemaValue(item, itemSchema, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func valueHasJSONType(value any, typ string) bool {
	switch typ {
	case "any":
		return true
	case "null":
		return value == nil
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := numericValue(value)
		return ok
	case "integer":
		n, ok := numericValue(value)
		return ok && math.Trunc(n) == n
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	default:
		return false
	}
}

func numericValue(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := strconv.ParseFloat(string(n), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func jsonTypeName(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		if _, ok := numericValue(value); ok {
			return "number"
		}
		return fmt.Sprintf("%T", value)
	}
}

func supportedJSONType(typ string) bool {
	switch typ {
	case "object", "array", "string", "number", "integer", "boolean", "null", "any":
		return true
	default:
		return false
	}
}

func jsonValuesEqual(left, right any) bool {
	if l, ok := numericValue(left); ok {
		if r, ok := numericValue(right); ok {
			return l == r
		}
	}
	if reflect.DeepEqual(left, right) {
		return true
	}
	l, lerr := json.Marshal(left)
	r, rerr := json.Marshal(right)
	return lerr == nil && rerr == nil && bytes.Equal(l, r)
}

var outputSourcePattern = regexp.MustCompile(`^nodes\.([a-zA-Z_][a-zA-Z0-9_-]*)\.outputs\.([a-zA-Z_][a-zA-Z0-9_-]*)$`)

// ParseOutputSource parses a Flow-level output mapping expression.
func ParseOutputSource(source string) (nodeID, field string, ok bool) {
	match := outputSourcePattern.FindStringSubmatch(strings.TrimSpace(source))
	if len(match) != 3 {
		return "", "", false
	}
	return match[1], match[2], true
}
