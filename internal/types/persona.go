package types

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AgeString handles age as either a string or a number in JSON
type AgeString string

func (a *AgeString) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*a = AgeString(s)
		return nil
	}
	var n float64
	if err := json.Unmarshal(data, &n); err == nil {
		*a = AgeString(fmt.Sprintf("%d", int(n)))
		return nil
	}
	return fmt.Errorf("AgeString: cannot unmarshal %s", string(data))
}

// GoalsString handles goals as either a string or an array of strings
type GoalsString string

func (g *GoalsString) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as string first
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*g = GoalsString(s)
		return nil
	}
	// Try to unmarshal as array of strings
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		// Join array elements with newlines
		*g = GoalsString(strings.Join(arr, "\n"))
		return nil
	}
	return fmt.Errorf("GoalsString: cannot unmarshal %s", string(data))
}

// Persona represents a customer persona for focus group simulation
type Persona struct {
	Name        string      `json:"name"`
	Role        string      `json:"role"`
	Description string      `json:"description"`
	Background  string      `json:"background"`
	Goals       GoalsString `json:"goals"`
	Age         AgeString   `json:"age"`
	Sex         string      `json:"sex"`
	Race        string      `json:"race"`
}
