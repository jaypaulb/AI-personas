package gemini

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jaypaulb/AI-personas/canvusapi"
)

// CreatePersonas extracts business notes, generates personas, and creates persona notes and images on the canvas.
// Returns error if any required step fails.
func CreatePersonas(ctx context.Context, qnoteID string, client *canvusapi.Client) error {
	// Step 1: Fetch all widgets
	widgets, err := client.GetWidgets(false)
	if err != nil {
		return fmt.Errorf("[CreatePersonas] Failed to fetch widgets: %w", err)
	}

	// Step 2: Filter business notes
	requiredTitles := []string{
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
		if typeStr == "Note" && titleMap[titleUpper] == false {
			for _, req := range requiredTitles {
				if titleUpper == req {
					businessNotes = append(businessNotes, w)
					titleMap[req] = true
					fmt.Printf("Extracted data from Note - %s\n", req)
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

	missing := false
	for _, req := range requiredTitles {
		if !titleMap[req] {
			fmt.Printf("Note - %s not found Aborting\n", req)
			missing = true
		}
	}
	if missing {
		return fmt.Errorf("[CreatePersonas] Aborting extraction due to missing notes.")
	}

	if personasAnchor == nil {
		return fmt.Errorf("[CreatePersonas] Personas anchor not found. Aborting.")
	}

	fmt.Printf("Successfully extracted all data - parsing and compiling report for AI\n")
	// Step 3: Structure and log the data
	structured := struct {
		BusinessNotes  []string               `json:"business_notes"`
		PersonasAnchor map[string]interface{} `json:"personas_anchor"`
	}{
		BusinessNotes:  []string{},
		PersonasAnchor: personasAnchor,
	}
	for _, n := range businessNotes {
		title, _ := n["title"].(string)
		text, _ := n["text"].(string)
		structured.BusinessNotes = append(structured.BusinessNotes, fmt.Sprintf("%s: %s", title, text))
	}
	businessContext := strings.Join(structured.BusinessNotes, "\n\n")

	// --- Persona existence check ---
	existingPersonas := make(map[int]map[string]interface{}) // index -> widget
	personaTitles := make([]string, 4)
	for i := 0; i < 4; i++ {
		personaTitles[i] = ""
	}

	// First, try to match existing notes to personas by index
	for _, w := range widgets {
		typeStr, _ := w["widget_type"].(string)
		title, _ := w["title"].(string)
		for i := 0; i < 4; i++ {
			prefix := fmt.Sprintf("Persona %d: ", i+1)
			if typeStr == "Note" && strings.HasPrefix(strings.TrimSpace(title), prefix) {
				existingPersonas[i] = w
				personaTitles[i] = title // Save the actual title for later use
			}
		}
	}

	if len(existingPersonas) == 4 {
		fmt.Printf("All 4 persona notes already exist. Using existing data.\n")
		for i := 0; i < 4; i++ {
			w := existingPersonas[i]
			title, _ := w["title"].(string)
			text, _ := w["text"].(string)
			fmt.Printf("Existing %s: %s\n", title, text)
		}
		return nil
	}

	// --- Gemini persona generation for missing personas ---
	ctx2, cancel2 := context.WithTimeout(ctx, 60*time.Second)
	defer cancel2()
	geminiClient, err := NewClient(ctx2)
	if err != nil {
		return fmt.Errorf("[CreatePersonas] Failed to create Gemini client: %w", err)
	}
	personas, err := geminiClient.GeneratePersonas(ctx2, businessContext)
	if err != nil {
		return fmt.Errorf("[CreatePersonas] Gemini persona generation failed: %w", err)
	}
	// Color palette
	colors := []string{"#2196f3ff", "#4caf50ff", "#ff9800ff", "#9c27b0ff"}
	// Layout calculation
	anchor := structured.PersonasAnchor
	anchorLoc, _ := anchor["location"].(map[string]interface{})
	anchorSize, _ := anchor["size"].(map[string]interface{})
	ax := anchorLoc["x"].(float64)
	ay := anchorLoc["y"].(float64)
	aw := anchorSize["width"].(float64)
	ah := anchorSize["height"].(float64)
	border := 0.02
	colW := 0.23
	gap := 0.01
	imgH := 0.10
	var imgWg sync.WaitGroup
	for i := 0; i < 4; i++ {
		if _, exists := existingPersonas[i]; exists {
			continue // Skip existing
		}
		p := personas[i]
		color := colors[i%len(colors)]
		formatted := FormatPersonaNote(p)
		// Calculate position
		x := ax + aw*border + float64(i)*(aw*colW+aw*gap)
		imgY := ay + ah*border
		imgW := aw * colW
		imgHpx := ah * imgH
		noteH := 0.40 // fixed fraction of anchor height
		// Place note at the bottom of the anchor area, with a border
		noteY := ay + (ah * 0.34)

		title := fmt.Sprintf("Persona %d: %s", i+1, p.Name)
		personaTitles[i] = title

		noteMeta := map[string]interface{}{
			"title":            title,
			"text":             formatted,
			"location":         map[string]interface{}{"x": x, "y": noteY},
			"size":             map[string]interface{}{"width": imgW, "height": noteH * ah},
			"background_color": color,
		}
		noteWidget, err := client.CreateNote(noteMeta)
		if err != nil {
			fmt.Printf("[CreatePersonas] Failed to create persona note: %v\n", err)
		} else {
			noteWidgetID, _ := noteWidget["id"].(string)
			fmt.Printf("[CreatePersonas] Persona note created: %s (ID: %s)\n", title, noteWidgetID)
		}
		// Start image generation/upload in a goroutine
		imgWg.Add(1)
		go func(p Persona, x, imgY, imgW, imgHpx float64, idx int, title string) {
			defer imgWg.Done()
			fmt.Printf("[CreatePersonas] Calling OpenAI DALL·E for persona: %s\n", title)
			imgBytes, err := GeneratePersonaImageOpenAI(p)
			fmt.Printf("[CreatePersonas] OpenAI DALL·E call returned for persona: %s, err: %v\n", title, err)
			imgPath := ""
			if err != nil {
				fmt.Printf("[CreatePersonas] Persona image not generated: %v\n", err)
				return
			}
			tmpfile, err := os.CreateTemp("", "persona_*.png")
			if err != nil {
				fmt.Printf("[CreatePersonas] Could not create temp file for persona image: %v\n", err)
				return
			}
			imgPath = tmpfile.Name()
			if _, err := tmpfile.Write(imgBytes); err != nil {
				fmt.Printf("[CreatePersonas] Could not write persona image to temp file: %v\n", err)
				tmpfile.Close()
				os.Remove(imgPath)
				return
			}
			tmpfile.Close()
			imgMeta := map[string]interface{}{
				"title":    title + " Headshot",
				"location": map[string]interface{}{"x": x, "y": imgY},
				"size":     map[string]interface{}{"width": imgW, "height": imgHpx},
			}
			imgWidget, err := client.CreateImage(imgPath, imgMeta)
			if err != nil {
				fmt.Printf("[CreatePersonas] Failed to upload persona image: %v\n", err)
			} else {
				imgWidgetID, _ := imgWidget["id"].(string)
				fmt.Printf("[CreatePersonas] Persona image uploaded: %s (ID: %s)\n", title+" Headshot", imgWidgetID)
			}
			os.Remove(imgPath)
		}(p, x, imgY, imgW, imgHpx, i, title)
	}
	fmt.Printf("[CreatePersonas] Persona image generation running in background.\n")
	// --- end Gemini persona generation ---
	return nil
}
