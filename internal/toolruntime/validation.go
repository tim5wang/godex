package toolruntime

import "sort"

func validateToolInputSchema(tool string, args map[string]interface{}, schema map[string]interface{}) error {
	required := requiredToolFields(schema["required"])
	if len(required) == 0 {
		return nil
	}
	missing := make([]string, 0)
	for _, name := range required {
		value, ok := args[name]
		if !ok || value == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return ErrToolInvalidInput{Tool: tool, Missing: missing}
}

func requiredToolFields(value interface{}) []string {
	var required []string
	switch typed := value.(type) {
	case []string:
		required = append(required, typed...)
	case []interface{}:
		for _, item := range typed {
			if name, ok := item.(string); ok {
				required = append(required, name)
			}
		}
	}
	sort.Strings(required)
	return required
}
