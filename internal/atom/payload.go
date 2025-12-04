package atom

import (
	"os"
	"strings"
)

// BuildConnectorPayload creates a Canvus connector payload between two widgets
func BuildConnectorPayload(srcID, dstID string) map[string]interface{} {
	return map[string]interface{}{
		"src": map[string]interface{}{
			"id":            srcID,
			"auto_location": true,
			"tip":           "none",
		},
		"dst": map[string]interface{}{
			"id":            dstID,
			"auto_location": true,
			"tip":           "solid-equilateral-triangle",
		},
		"line_color":  "#e7e7f2ff",
		"line_width":  5,
		"state":       "normal",
		"type":        "curve",
		"widget_type": "Connector",
	}
}

// GetAnswerGenerationMessage returns the appropriate wait message based on the model type
func GetAnswerGenerationMessage() string {
	model := os.Getenv("GEMINI_MODEL_CHAT")
	if model == "" {
		model = "gemini-2.5-flash" // Default
	}
	modelLower := strings.ToLower(model)

	if strings.Contains(modelLower, "flash-lite") {
		return "Generating answers, please wait... This can take up to 30 seconds."
	} else if strings.Contains(modelLower, "flash") {
		return "Generating answers, please wait... This can take up to 60 seconds."
	} else if strings.Contains(modelLower, "pro") {
		return "Generating answers, please wait... This could take a few minutes as the model thinks about its answers."
	}
	// Default message if model type can't be determined
	return "Generating answers, please wait..."
}
