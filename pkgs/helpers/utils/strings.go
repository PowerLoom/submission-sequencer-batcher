package utils

import "strings"

func ExtractField(str, fieldName string) string {
	start := strings.Index(str, fieldName+":")
	if start == -1 {
		return ""
	}
	start += len(fieldName) + 2 // +2 for the colon and the opening quote
	end := strings.Index(str[start:], "\"")
	if end == -1 {
		return ""
	}
	return str[start : start+end]
}

func AppendToLogEntry(logEntry map[string]interface{}, key string, value interface{}) {
	if existingValue, ok := logEntry[key]; ok {
		switch v := existingValue.(type) {
		case []interface{}:
			logEntry[key] = append(v, value)
		default:
			logEntry[key] = []interface{}{v, value}
		}
	} else {
		logEntry[key] = []interface{}{value}
	}
}
