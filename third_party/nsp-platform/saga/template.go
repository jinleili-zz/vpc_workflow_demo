// File: template.go
// Package saga - Template rendering for SAGA transactions

package saga

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// TemplateContext holds the data context for template rendering.
type TemplateContext struct {
	// Transaction contains the global transaction data.
	Transaction *Transaction
	// Steps contains all steps with their action responses.
	Steps []*Step
	// CurrentStep is the step being rendered for.
	CurrentStep *Step
}

// templateVarRegex matches template variables like {action_response.field} or {step[0].action_response.field}
var templateVarRegex = regexp.MustCompile(`\{([^}]+)\}`)

// RenderTemplate renders a template string with the given context data.
// Supported template syntax:
//   - {action_response.field_name} - from current step's action_response
//   - {step[0].action_response.field} - from specified step's action_response
//   - {transaction.payload.field} - from global transaction payload
//
// Returns an error if a template variable cannot be resolved.
func RenderTemplate(tpl string, data map[string]any) (string, error) {
	if tpl == "" {
		return "", nil
	}

	result := templateVarRegex.ReplaceAllStringFunc(tpl, func(match string) string {
		// Extract the variable path (without braces)
		path := match[1 : len(match)-1]

		value, err := resolveTemplatePath(path, data)
		if err != nil {
			// Return the original match to indicate failure
			// The caller will check if any matches remain
			return match
		}

		return value
	})

	// Check if any unresolved variables remain
	if templateVarRegex.MatchString(result) {
		return "", fmt.Errorf("unresolved template variables in: %s", result)
	}

	return result, nil
}

// RenderPayload renders template variables in a payload map.
// Returns a new map with all string values rendered.
func RenderPayload(payload map[string]any, data map[string]any) (map[string]any, error) {
	if payload == nil {
		return nil, nil
	}

	result := make(map[string]any)
	for key, value := range payload {
		rendered, err := renderValue(value, data)
		if err != nil {
			return nil, fmt.Errorf("failed to render key %s: %w", key, err)
		}
		result[key] = rendered
	}

	return result, nil
}

// renderValue recursively renders template variables in a value.
func renderValue(value any, data map[string]any) (any, error) {
	switch v := value.(type) {
	case string:
		return RenderTemplate(v, data)
	case map[string]any:
		return RenderPayload(v, data)
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			rendered, err := renderValue(item, data)
			if err != nil {
				return nil, err
			}
			result[i] = rendered
		}
		return result, nil
	default:
		// Non-string values are returned as-is
		return value, nil
	}
}

// resolveTemplatePath resolves a dot-notation path against the data context.
func resolveTemplatePath(path string, data map[string]any) (string, error) {
	parts := parsePathParts(path)
	if len(parts) == 0 {
		return "", fmt.Errorf("empty path")
	}

	var current any = data
	for i, part := range parts {
		switch c := current.(type) {
		case map[string]any:
			var ok bool
			// Check for array index notation
			if idx, arrayPart, hasIndex := parseArrayIndex(part); hasIndex {
				// First access the map key
				current, ok = c[arrayPart]
				if !ok {
					return "", fmt.Errorf("key not found: %s", arrayPart)
				}
				// Then access the array index
				arr, isArr := current.([]any)
				if !isArr {
					return "", fmt.Errorf("expected array at %s", arrayPart)
				}
				if idx < 0 || idx >= len(arr) {
					return "", fmt.Errorf("array index out of bounds: %d", idx)
				}
				current = arr[idx]
			} else {
				current, ok = c[part]
				if !ok {
					return "", fmt.Errorf("key not found: %s (path: %s, step: %d)", part, path, i)
				}
			}
		case []any:
			// This shouldn't happen if parseArrayIndex is working correctly
			return "", fmt.Errorf("unexpected array at path step %d", i)
		default:
			return "", fmt.Errorf("cannot traverse into type %T at path step %d", current, i)
		}
	}

	return valueToString(current)
}

// parsePathParts splits a path into parts, handling dots and array notation.
func parsePathParts(path string) []string {
	var parts []string
	var current strings.Builder

	for i := 0; i < len(path); i++ {
		c := path[i]
		if c == '.' {
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		} else if c == '[' {
			// Keep array notation with the current part
			if current.Len() > 0 {
				// Find matching ]
				end := strings.IndexByte(path[i:], ']')
				if end == -1 {
					// Malformed, just add the rest
					current.WriteString(path[i:])
					break
				}
				current.WriteString(path[i : i+end+1])
				i += end
			} else {
				// Array index at start of path (e.g., step[0])
				// Find matching ]
				end := strings.IndexByte(path[i:], ']')
				if end == -1 {
					current.WriteString(path[i:])
					break
				}
				current.WriteString(path[i : i+end+1])
				i += end
			}
		} else {
			current.WriteByte(c)
		}
	}

	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}

// parseArrayIndex extracts array index from a path part like "items[0]" or "step[2]".
// Returns (index, basePart, hasIndex).
func parseArrayIndex(part string) (int, string, bool) {
	start := strings.IndexByte(part, '[')
	if start == -1 {
		return 0, part, false
	}

	end := strings.IndexByte(part, ']')
	if end == -1 || end <= start+1 {
		return 0, part, false
	}

	basePart := part[:start]
	indexStr := part[start+1 : end]

	idx, err := strconv.Atoi(indexStr)
	if err != nil {
		return 0, part, false
	}

	return idx, basePart, true
}

// valueToString converts a value to its string representation.
func valueToString(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case float64:
		// Check if it's an integer
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10), nil
		}
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case bool:
		return strconv.FormatBool(v), nil
	case nil:
		return "", nil
	case map[string]any, []any:
		// For complex types, return JSON
		bytes, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(bytes), nil
	default:
		return fmt.Sprintf("%v", v), nil
	}
}

// BuildTemplateData builds the data context for template rendering.
func BuildTemplateData(tx *Transaction, steps []*Step, currentStep *Step) map[string]any {
	data := make(map[string]any)

	// Add transaction payload
	if tx != nil && tx.Payload != nil {
		data["transaction"] = map[string]any{
			"payload": tx.Payload,
			"id":      tx.ID,
		}
	} else {
		data["transaction"] = map[string]any{
			"payload": map[string]any{},
		}
	}

	// Add steps data
	stepsData := make([]any, len(steps))
	for i, step := range steps {
		stepData := map[string]any{
			"index":           step.Index,
			"name":            step.Name,
			"status":          string(step.Status),
			"action_response": step.ActionResponse,
		}
		stepsData[i] = stepData
	}
	data["step"] = stepsData

	// Add current step's action_response at top level for convenience
	if currentStep != nil && currentStep.ActionResponse != nil {
		data["action_response"] = currentStep.ActionResponse
	} else {
		data["action_response"] = map[string]any{}
	}

	return data
}

// RenderURL renders template variables in a URL string.
func RenderURL(url string, data map[string]any) (string, error) {
	return RenderTemplate(url, data)
}

// RenderPayloadJSON renders template variables in a JSON payload and returns the result.
func RenderPayloadJSON(payload map[string]any, data map[string]any) ([]byte, error) {
	if payload == nil {
		return nil, nil
	}

	rendered, err := RenderPayload(payload, data)
	if err != nil {
		return nil, err
	}

	return json.Marshal(rendered)
}
