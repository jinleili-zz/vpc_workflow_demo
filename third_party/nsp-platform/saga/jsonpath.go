// File: jsonpath.go
// Package saga - Simple JSONPath implementation for polling result extraction

package saga

import (
	"fmt"
	"strconv"
	"strings"
)

// ExtractByPath extracts a value from a map using a simple JSONPath expression.
// Supported syntax:
//   - $.status             - top-level field
//   - $.result.code        - nested field
//   - $.items[0].status    - array index access
//
// Returns the extracted value as a string, or an error if the path cannot be resolved.
func ExtractByPath(data map[string]any, path string) (string, error) {
	if data == nil {
		return "", fmt.Errorf("data is nil")
	}

	if path == "" {
		return "", fmt.Errorf("path is empty")
	}

	// Remove leading $. if present
	if strings.HasPrefix(path, "$.") {
		path = path[2:]
	} else if strings.HasPrefix(path, "$") {
		path = path[1:]
		if strings.HasPrefix(path, ".") {
			path = path[1:]
		}
	}

	if path == "" {
		return "", fmt.Errorf("path is empty after removing root")
	}

	// Parse and navigate the path
	parts := parseJSONPathParts(path)
	if len(parts) == 0 {
		return "", fmt.Errorf("no path parts found")
	}

	var current any = data
	for i, part := range parts {
		switch c := current.(type) {
		case map[string]any:
			// Check for array index in this part
			if idx, basePart, hasIndex := parseJSONPathArrayIndex(part); hasIndex {
				// Access the map key first
				val, ok := c[basePart]
				if !ok {
					return "", fmt.Errorf("key '%s' not found at path position %d", basePart, i)
				}
				// Then access the array index
				arr, isArr := val.([]any)
				if !isArr {
					return "", fmt.Errorf("expected array at '%s', got %T", basePart, val)
				}
				if idx < 0 || idx >= len(arr) {
					return "", fmt.Errorf("array index %d out of bounds (length %d)", idx, len(arr))
				}
				current = arr[idx]
			} else {
				val, ok := c[part]
				if !ok {
					return "", fmt.Errorf("key '%s' not found at path position %d", part, i)
				}
				current = val
			}

		case []any:
			// Direct array access (e.g., $[0])
			idx, err := strconv.Atoi(part)
			if err != nil {
				return "", fmt.Errorf("expected array index, got '%s'", part)
			}
			if idx < 0 || idx >= len(c) {
				return "", fmt.Errorf("array index %d out of bounds (length %d)", idx, len(c))
			}
			current = c[idx]

		default:
			return "", fmt.Errorf("cannot navigate into %T at path position %d", current, i)
		}
	}

	return jsonValueToString(current)
}

// parseJSONPathParts splits a JSONPath into individual parts.
// Handles dots and array notation.
func parseJSONPathParts(path string) []string {
	var parts []string
	var current strings.Builder

	i := 0
	for i < len(path) {
		c := path[i]

		if c == '.' {
			// Dot separator
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
			i++
		} else if c == '[' {
			// Array notation
			// Find the closing bracket
			end := strings.IndexByte(path[i:], ']')
			if end == -1 {
				// Malformed, add the rest
				current.WriteString(path[i:])
				break
			}

			if current.Len() > 0 {
				// Append array index to current part (e.g., "items[0]")
				current.WriteString(path[i : i+end+1])
			} else {
				// Standalone array index (e.g., "[0]" at start)
				// Extract just the index
				indexStr := path[i+1 : i+end]
				parts = append(parts, indexStr)
			}
			i += end + 1
		} else {
			current.WriteByte(c)
			i++
		}
	}

	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}

// parseJSONPathArrayIndex extracts the array index from a path part like "items[0]".
// Returns (index, basePart, hasIndex).
func parseJSONPathArrayIndex(part string) (int, string, bool) {
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

// jsonValueToString converts a JSON value to its string representation.
func jsonValueToString(value any) (string, error) {
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
	default:
		return fmt.Sprintf("%v", v), nil
	}
}

// MatchPollResult checks if the poll response matches the expected success or failure value.
// Returns:
//   - (true, nil) if matches successPath/successValue
//   - (false, nil) if matches failurePath/failureValue
//   - (false, error) if no match or error occurred
func MatchPollResult(response map[string]any, step *Step) (success bool, failure bool, err error) {
	// Check success condition
	if step.PollSuccessPath != "" && step.PollSuccessValue != "" {
		value, extractErr := ExtractByPath(response, step.PollSuccessPath)
		if extractErr == nil && value == step.PollSuccessValue {
			return true, false, nil
		}
	}

	// Check failure condition
	if step.PollFailurePath != "" && step.PollFailureValue != "" {
		value, extractErr := ExtractByPath(response, step.PollFailurePath)
		if extractErr == nil && value == step.PollFailureValue {
			return false, true, nil
		}
	}

	// Neither matched - still processing
	return false, false, nil
}
