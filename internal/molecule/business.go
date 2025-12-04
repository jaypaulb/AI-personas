package molecule

import (
	"fmt"
	"log"
	"strings"

	"github.com/jaypaulb/AI-personas/canvusapi"
)

// RequiredBusinessNoteTitles returns the list of required business note titles
// for extracting business context from a Business Model Canvas
func RequiredBusinessNoteTitles() []string {
	return []string{
		"KEY PARTNERS",
		"KEY ACTIVITIES",
		"VALUE PROPOSITIONS",
		"CUSTOMER RELATIONSHIPS",
		"CUSTOMER SEGMENTS",
		"KEY RESOURCES",
		"CHANNELS",
		"COST STRUCTURE",
		"REVENUE STREAMS",
	}
}

// MissingNotesHelperColor is the red background color for missing notes feedback
const MissingNotesHelperColor = "#f44336ff"

// CreateMissingNotesHelper creates a helper note on the canvas listing which required
// business notes are missing. Returns the helper note ID if created, or empty string on error.
func CreateMissingNotesHelper(client *canvusapi.Client, missingNotes []string, personasAnchor map[string]interface{}) string {
	if len(missingNotes) == 0 {
		return ""
	}

	// Build the help text
	var sb strings.Builder
	sb.WriteString("The following required Business Model Canvas notes are missing:\n\n")
	for i, note := range missingNotes {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, note))
	}
	sb.WriteString("\nPlease add these notes to the canvas with the exact titles listed above, then try again.")

	// Determine position for the helper note
	// If we have a personas anchor, place it nearby; otherwise use default position
	var x, y, width, height float64 = 0, 0, 400, 300

	if personasAnchor != nil {
		if loc, ok := personasAnchor["location"].(map[string]interface{}); ok {
			if px, ok := loc["x"].(float64); ok {
				x = px - 450 // Place to the left of personas anchor
			}
			if py, ok := loc["y"].(float64); ok {
				y = py
			}
		}
		if size, ok := personasAnchor["size"].(map[string]interface{}); ok {
			if w, ok := size["width"].(float64); ok {
				width = w * 0.5
				if width < 300 {
					width = 300
				}
			}
			if h, ok := size["height"].(float64); ok {
				height = h * 0.3
				if height < 200 {
					height = 200
				}
			}
		}
	}

	noteMeta := map[string]interface{}{
		"title":            "Missing Required Notes",
		"text":             sb.String(),
		"location":         map[string]interface{}{"x": x, "y": y},
		"size":             map[string]interface{}{"width": width, "height": height},
		"background_color": MissingNotesHelperColor,
	}

	helperNote, err := client.CreateNote(noteMeta)
	if err != nil {
		log.Printf("[CreateMissingNotesHelper] Failed to create helper note: %v", err)
		return ""
	}

	helperID, _ := helperNote["id"].(string)
	log.Printf("[CreateMissingNotesHelper] Created missing notes helper (ID: %s) listing %d missing notes", helperID, len(missingNotes))
	return helperID
}

// ExtractBusinessContext extracts business context and personas anchor from widgets
// Returns: businessContext string, personasAnchor widget, missing note titles, error
func ExtractBusinessContext(widgets []map[string]interface{}) (string, map[string]interface{}, []string, error) {
	requiredTitles := RequiredBusinessNoteTitles()
	titleMap := make(map[string]bool)
	for _, t := range requiredTitles {
		titleMap[t] = false
	}

	var businessNotes []map[string]interface{}
	var personasAnchor map[string]interface{}

	for _, w := range widgets {
		typeStr, _ := w["widget_type"].(string)
		title, _ := w["title"].(string)
		titleUpper := strings.ToUpper(strings.TrimSpace(title))

		if typeStr == "Note" && !titleMap[titleUpper] {
			for _, req := range requiredTitles {
				if titleUpper == req {
					businessNotes = append(businessNotes, w)
					titleMap[req] = true
				}
			}
		}

		if typeStr == "Anchor" {
			anchorName, _ := w["anchor_name"].(string)
			if strings.EqualFold(strings.TrimSpace(anchorName), "Personas") {
				personasAnchor = w
			}
		}
	}

	// Check for missing notes
	var missingNotes []string
	for _, req := range requiredTitles {
		if !titleMap[req] {
			missingNotes = append(missingNotes, req)
		}
	}

	if len(missingNotes) > 0 {
		log.Printf("[ExtractBusinessContext] Missing required notes: %v", missingNotes)
		return "", personasAnchor, missingNotes, fmt.Errorf("missing required notes: %v", missingNotes)
	}

	if personasAnchor == nil {
		return "", nil, nil, fmt.Errorf("Personas anchor not found")
	}

	// Build business context string
	var contextParts []string
	for _, n := range businessNotes {
		title, _ := n["title"].(string)
		text, _ := n["text"].(string)
		contextParts = append(contextParts, fmt.Sprintf("%s: %s", title, text))
	}
	businessContext := strings.Join(contextParts, "\n\n")

	const minBusinessContextLength = 100
	if len(strings.TrimSpace(businessContext)) < minBusinessContextLength {
		log.Printf("[ExtractBusinessContext] Warning: Business context appears too short (%d characters)", len(strings.TrimSpace(businessContext)))
	}

	log.Printf("[ExtractBusinessContext] Successfully extracted %d business notes", len(businessNotes))
	return businessContext, personasAnchor, nil, nil
}
