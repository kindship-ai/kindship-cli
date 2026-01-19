package validator

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xeipuuv/gojsonschema"
)

// ValidateInputs validates inputs against input_schema
func ValidateInputs(inputs map[string]interface{}, schema map[string]interface{}) error {
	if schema == nil || len(schema) == 0 {
		return nil // No schema = no validation
	}

	schemaLoader := gojsonschema.NewGoLoader(schema)
	dataLoader := gojsonschema.NewGoLoader(inputs)

	result, err := gojsonschema.Validate(schemaLoader, dataLoader)
	if err != nil {
		return fmt.Errorf("validation error: %w", err)
	}

	if !result.Valid() {
		errors := make([]string, 0, len(result.Errors()))
		for _, err := range result.Errors() {
			errors = append(errors, err.String())
		}
		return fmt.Errorf("input validation failed: %s", strings.Join(errors, "; "))
	}

	return nil
}

// ValidateOutputs validates outputs against output_schema
func ValidateOutputs(outputs map[string]interface{}, schema map[string]interface{}) error {
	if schema == nil || len(schema) == 0 {
		return nil // No schema = no validation
	}

	schemaLoader := gojsonschema.NewGoLoader(schema)
	dataLoader := gojsonschema.NewGoLoader(outputs)

	result, err := gojsonschema.Validate(schemaLoader, dataLoader)
	if err != nil {
		return fmt.Errorf("validation error: %w", err)
	}

	if !result.Valid() {
		errors := make([]string, 0, len(result.Errors()))
		for _, err := range result.Errors() {
			errors = append(errors, err.String())
		}
		return fmt.Errorf("output validation failed: %s", strings.Join(errors, "; "))
	}

	return nil
}

// GetInputLabels returns the list of labels from an inputs map
func GetInputLabels(inputs map[string]interface{}) []string {
	labels := make([]string, 0, len(inputs))
	for label := range inputs {
		labels = append(labels, label)
	}
	return labels
}

// FormatInputsForDisplay formats inputs for logging/display
func FormatInputsForDisplay(inputs map[string]interface{}) string {
	if len(inputs) == 0 {
		return "No inputs"
	}

	parts := make([]string, 0, len(inputs))
	for label, value := range inputs {
		jsonBytes, err := json.Marshal(value)
		if err != nil {
			parts = append(parts, fmt.Sprintf("%s: [error marshaling]", label))
			continue
		}
		// Truncate long values
		jsonStr := string(jsonBytes)
		if len(jsonStr) > 100 {
			jsonStr = jsonStr[:100] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s: %s", label, jsonStr))
	}
	return strings.Join(parts, "\n  ")
}

// ExtractJSONFromOutput attempts to extract a JSON object from stdout
// It looks for JSON blocks in markdown code fences or raw JSON objects
func ExtractJSONFromOutput(stdout string) (map[string]interface{}, error) {
	stdout = strings.TrimSpace(stdout)

	// Try to find JSON in markdown code fence
	jsonStart := strings.Index(stdout, "```json")
	if jsonStart != -1 {
		jsonStart += 7 // Skip past ```json
		jsonEnd := strings.Index(stdout[jsonStart:], "```")
		if jsonEnd != -1 {
			stdout = strings.TrimSpace(stdout[jsonStart : jsonStart+jsonEnd])
		}
	} else {
		// Try generic code fence
		codeStart := strings.Index(stdout, "```")
		if codeStart != -1 {
			codeStart += 3
			// Skip language identifier if present
			newline := strings.Index(stdout[codeStart:], "\n")
			if newline != -1 {
				codeStart += newline + 1
			}
			codeEnd := strings.Index(stdout[codeStart:], "```")
			if codeEnd != -1 {
				stdout = strings.TrimSpace(stdout[codeStart : codeStart+codeEnd])
			}
		}
	}

	// Try to find a JSON object (starts with { ends with })
	braceStart := strings.Index(stdout, "{")
	if braceStart == -1 {
		return nil, fmt.Errorf("no JSON object found in output")
	}

	// Find matching closing brace
	braceCount := 0
	braceEnd := -1
	for i := braceStart; i < len(stdout); i++ {
		if stdout[i] == '{' {
			braceCount++
		} else if stdout[i] == '}' {
			braceCount--
			if braceCount == 0 {
				braceEnd = i + 1
				break
			}
		}
	}

	if braceEnd == -1 {
		return nil, fmt.Errorf("no matching closing brace found")
	}

	jsonStr := stdout[braceStart:braceEnd]

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	return result, nil
}
