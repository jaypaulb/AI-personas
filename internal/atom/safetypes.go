package atom

// SafeFloat64 safely extracts a float64 value from a map[string]interface{}.
// Returns the value and true if the key exists and is a float64, otherwise returns 0.0 and false.
func SafeFloat64(m map[string]interface{}, key string) (float64, bool) {
	if m == nil {
		return 0.0, false
	}
	val, ok := m[key]
	if !ok {
		return 0.0, false
	}
	f, ok := val.(float64)
	return f, ok
}

// SafeString safely extracts a string value from a map[string]interface{}.
// Returns the value and true if the key exists and is a string, otherwise returns "" and false.
func SafeString(m map[string]interface{}, key string) (string, bool) {
	if m == nil {
		return "", false
	}
	val, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := val.(string)
	return s, ok
}

// SafeMap safely extracts a map[string]interface{} value from a map[string]interface{}.
// Returns the value and true if the key exists and is a map, otherwise returns nil and false.
func SafeMap(m map[string]interface{}, key string) (map[string]interface{}, bool) {
	if m == nil {
		return nil, false
	}
	val, ok := m[key]
	if !ok {
		return nil, false
	}
	nested, ok := val.(map[string]interface{})
	return nested, ok
}
