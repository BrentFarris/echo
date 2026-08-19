package plugins

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"strings"
)

func ValidateJSONSchemaDefinition(schema map[string]any) error {
	return validateSchemaDefinition(schema, "$", 0)
}

func validateSchemaDefinition(schema map[string]any, path string, depth int) error {
	if depth > 32 {
		return fmt.Errorf("%s exceeds schema nesting limit", path)
	}
	allowedKeywords := map[string]bool{
		"type": true, "title": true, "description": true, "default": true,
		"properties": true, "required": true, "additionalProperties": true,
		"items": true, "oneOf": true, "enum": true, "pattern": true,
		"minimum": true, "maximum": true, "minLength": true, "maxLength": true,
		"minItems": true, "maxItems": true,
	}
	for keyword := range schema {
		if !allowedKeywords[keyword] {
			return fmt.Errorf("%s uses unsupported schema keyword %q", path, keyword)
		}
	}
	typeName, hasType := schema["type"].(string)
	if rawType, exists := schema["type"]; exists && (!hasType || !map[string]bool{"object": true, "array": true, "string": true, "number": true, "integer": true, "boolean": true, "null": true, "any": true}[typeName]) {
		return fmt.Errorf("%s has unsupported type %v", path, rawType)
	}
	if raw, exists := schema["properties"]; exists {
		properties, ok := raw.(map[string]any)
		if !ok || typeName != "object" {
			return fmt.Errorf("%s properties require an object schema", path)
		}
		for name, definition := range properties {
			if name == "" || len(name) > 128 {
				return fmt.Errorf("%s has an invalid property name", path)
			}
			child, ok := definition.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.%s schema must be an object", path, name)
			}
			if err := validateSchemaDefinition(child, path+"."+name, depth+1); err != nil {
				return err
			}
		}
	}
	if raw, exists := schema["required"]; exists {
		values, ok := raw.([]any)
		if !ok || typeName != "object" {
			return fmt.Errorf("%s required must be an array on an object schema", path)
		}
		seen := map[string]bool{}
		for _, value := range values {
			name, ok := value.(string)
			if !ok || name == "" || seen[name] {
				return fmt.Errorf("%s required contains an invalid or duplicate name", path)
			}
			seen[name] = true
		}
	}
	if raw, exists := schema["additionalProperties"]; exists {
		if _, ok := raw.(bool); !ok || typeName != "object" {
			return fmt.Errorf("%s additionalProperties must be a boolean on an object schema", path)
		}
	}
	if raw, exists := schema["items"]; exists {
		items, ok := raw.(map[string]any)
		if !ok || typeName != "array" {
			return fmt.Errorf("%s items require an array schema", path)
		}
		if err := validateSchemaDefinition(items, path+"[]", depth+1); err != nil {
			return err
		}
	}
	if raw, exists := schema["oneOf"]; exists {
		values, ok := raw.([]any)
		if !ok || len(values) < 1 || len(values) > 16 {
			return fmt.Errorf("%s oneOf must contain 1 to 16 schemas", path)
		}
		for index, value := range values {
			child, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.oneOf[%d] must be a schema object", path, index)
			}
			if err := validateSchemaDefinition(child, fmt.Sprintf("%s.oneOf[%d]", path, index), depth+1); err != nil {
				return err
			}
		}
	}
	if raw, exists := schema["enum"]; exists {
		values, ok := raw.([]any)
		if !ok || len(values) == 0 || len(values) > 256 {
			return fmt.Errorf("%s enum must contain 1 to 256 values", path)
		}
	}
	if raw, exists := schema["pattern"]; exists {
		pattern, ok := raw.(string)
		if !ok || typeName != "string" {
			return fmt.Errorf("%s pattern requires a string schema", path)
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("%s pattern is invalid", path)
		}
	}
	for _, keyword := range []string{"minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems"} {
		if raw, exists := schema[keyword]; exists {
			if _, ok := schemaNumber(raw); !ok {
				return fmt.Errorf("%s %s must be numeric", path, keyword)
			}
		}
	}
	return nil
}

// ValidateJSONSchema implements the bounded JSON-Schema subset exposed by the
// v1 manifest. It intentionally rejects malformed schema nodes and validates
// the common object/array/scalar, required, enum, oneOf, and additionalProperties
// rules needed by model-facing tools.
func ValidateJSONSchema(schema map[string]any, value any) error {
	return validateSchemaValue(schema, value, "$", 0)
}

func validateSchemaValue(schema map[string]any, value any, path string, depth int) error {
	if depth > 32 {
		return fmt.Errorf("%s exceeds schema nesting limit", path)
	}
	if oneOf, ok := schema["oneOf"].([]any); ok {
		matches := 0
		for _, candidate := range oneOf {
			candidateSchema, ok := candidate.(map[string]any)
			if ok && validateSchemaValue(candidateSchema, value, path, depth+1) == nil {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s must match exactly one allowed schema", path)
		}
	}
	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			if reflect.DeepEqual(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s is not an allowed value", path)
		}
	}
	typeName, _ := schema["type"].(string)
	switch typeName {
	case "", "any":
		return nil
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		required := map[string]bool{}
		if values, ok := schema["required"].([]any); ok {
			for _, item := range values {
				if name, ok := item.(string); ok {
					required[name] = true
				}
			}
		}
		for name := range required {
			if _, ok := object[name]; !ok {
				return fmt.Errorf("%s.%s is required", path, name)
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		additional, hasAdditional := schema["additionalProperties"].(bool)
		for name, field := range object {
			definition, declared := properties[name]
			if !declared {
				if hasAdditional && !additional {
					return fmt.Errorf("%s.%s is not allowed", path, name)
				}
				continue
			}
			fieldSchema, ok := definition.(map[string]any)
			if !ok {
				return fmt.Errorf("schema for %s.%s is invalid", path, name)
			}
			if err := validateSchemaValue(fieldSchema, field, path+"."+name, depth+1); err != nil {
				return err
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		if minimum, ok := schemaNumber(schema["minItems"]); ok && float64(len(array)) < minimum {
			return fmt.Errorf("%s has too few items", path)
		}
		if maximum, ok := schemaNumber(schema["maxItems"]); ok && float64(len(array)) > maximum {
			return fmt.Errorf("%s has too many items", path)
		}
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for index, item := range array {
				if err := validateSchemaValue(itemSchema, item, fmt.Sprintf("%s[%d]", path, index), depth+1); err != nil {
					return err
				}
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		if minimum, ok := schemaNumber(schema["minLength"]); ok && float64(len([]rune(text))) < minimum {
			return fmt.Errorf("%s is too short", path)
		}
		if maximum, ok := schemaNumber(schema["maxLength"]); ok && float64(len([]rune(text))) > maximum {
			return fmt.Errorf("%s is too long", path)
		}
		if pattern, ok := schema["pattern"].(string); ok {
			matched, _ := regexp.MatchString(pattern, text)
			if !matched {
				return fmt.Errorf("%s does not match its required pattern", path)
			}
		}
	case "number", "integer":
		number, ok := schemaNumber(value)
		if !ok || typeName == "integer" && math.Trunc(number) != number {
			return fmt.Errorf("%s must be a %s", path, typeName)
		}
		if minimum, ok := schemaNumber(schema["minimum"]); ok && number < minimum {
			return fmt.Errorf("%s is below its minimum", path)
		}
		if maximum, ok := schemaNumber(schema["maximum"]); ok && number > maximum {
			return fmt.Errorf("%s is above its maximum", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be true or false", path)
		}
	case "null":
		if value != nil {
			return fmt.Errorf("%s must be null", path)
		}
	default:
		return fmt.Errorf("%s uses unsupported schema type %q", path, typeName)
	}
	return nil
}

func DecodeAndValidateArguments(schema map[string]any, raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("plugin tool arguments exceed the size limit")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode plugin tool arguments: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("plugin tool arguments contain trailing JSON")
	}
	value = normalizeJSONNumbers(value)
	if err := ValidateJSONSchema(schema, value); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("plugin tool arguments must be an object")
	}
	return object, nil
}

func normalizeJSONNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return float64(integer)
		}
		number, _ := typed.Float64()
		return number
	case []any:
		for index := range typed {
			typed[index] = normalizeJSONNumbers(typed[index])
		}
	case map[string]any:
		for key := range typed {
			typed[key] = normalizeJSONNumbers(typed[key])
		}
	}
	return value
}

func schemaNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case json.Number:
		value, err := number.Float64()
		return value, err == nil
	default:
		return 0, false
	}
}
