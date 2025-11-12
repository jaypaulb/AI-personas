package gemini

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jaypaulb/AI-personas/canvusapi"
)

// PersonaNoteIDs stores persona note IDs per QnoteID
var PersonaNoteIDs sync.Map // map[qnoteID][]string

// ParsePersonaNote parses a persona note text into a Persona struct
func ParsePersonaNote(text string) Persona {
	p := Persona{}
	// Use regex to extract fields
	re := regexp.MustCompile(`(?m)^🧑 Name: (.*)[\s\S]*^💼 Role: (.*)[\s\S]*^📝 Description: (.*)[\s\S]*^🏫 Background: (.*)[\s\S]*^🎯 Goals: (.*)[\s\S]*^🎂 Age: (.*)[\s\S]*^⚧ Sex: (.*)[\s\S]*^🌍 Race: (.*)$`)
	matches := re.FindStringSubmatch(text)
	if len(matches) == 9 {
		p.Name = matches[1]
		p.Role = matches[2]
		p.Description = matches[3]
		p.Background = matches[4]
		p.Goals = GoalsString(matches[5])
		p.Age = AgeString(matches[6])
		p.Sex = matches[7]
		p.Race = matches[8]
	}
	return p
}

// FetchPersonasFromNotes fetches persona notes by IDs and parses them
func FetchPersonasFromNotes(qnoteID string, client *canvusapi.Client) ([]Persona, error) {
	idsAny, ok := PersonaNoteIDs.Load(qnoteID)
	if !ok {
		return nil, fmt.Errorf("no persona note IDs for Qnote %s", qnoteID)
	}
	ids, ok := idsAny.([]string)
	if !ok || len(ids) != 4 {
		return nil, fmt.Errorf("invalid persona note IDs for Qnote %s", qnoteID)
	}
	personas := make([]Persona, 0, 4)
	for _, id := range ids {
		note, err := client.GetNote(id, false)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch persona note %s: %w", id, err)
		}
		text, _ := note["text"].(string)
		personas = append(personas, ParsePersonaNote(text))
	}
	return personas, nil
}

// CreatePersonas extracts business notes, generates personas, and creates persona notes and images on the canvas.
// Returns error if any required step fails.
func CreatePersonas(ctx context.Context, qnoteID string, client *canvusapi.Client) error {
	log.Printf("[CreatePersonas] Starting persona creation for Qnote %s", qnoteID)
	// Step 1: Fetch all widgets
	widgets, err := client.GetWidgets(false)
	if err != nil {
		log.Printf("[CreatePersonas] ERROR: Failed to fetch widgets: %v", err)
		return fmt.Errorf("[CreatePersonas] Failed to fetch widgets: %w", err)
	}
	log.Printf("[CreatePersonas] Fetched %d widgets", len(widgets))

	// Use the helper to get business context and anchor
	businessContext, personasAnchor, err := getBusinessContext(ctx, qnoteID, client)
	if err != nil {
		log.Printf("[CreatePersonas] ERROR: Failed to get business context or anchor: %v", err)
		return fmt.Errorf("[CreatePersonas] Failed to get business context or anchor: %w", err)
	}
	log.Printf("[CreatePersonas] Business context extracted (%d chars), personas anchor found", len(businessContext))

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
		log.Printf("[CreatePersonas] All 4 persona notes already exist. Using existing data.")
		personaIDs := make([]string, 4)
		for i := 0; i < 4; i++ {
			w := existingPersonas[i]
			text, _ := w["text"].(string)
			id, _ := w["id"].(string)
			if id == "" {
				log.Printf("[CreatePersonas] ERROR: Existing persona %d has empty ID", i+1)
				return fmt.Errorf("[CreatePersonas] existing persona %d has empty ID", i+1)
			}
			personaIDs[i] = id
			p := ParsePersonaNote(text)
			log.Printf("[CreatePersonas] Existing Persona %d: %s (ID: %s)", i+1, p.Name, id)
		}
		PersonaNoteIDs.Store(qnoteID, personaIDs)
		log.Printf("[CreatePersonas] Stored existing persona IDs for Qnote %s", qnoteID)
		return nil
	}

	// --- Gemini persona generation for missing personas ---
	log.Printf("[CreatePersonas] Generating personas using Gemini API...")
	ctx2, cancel2 := context.WithTimeout(ctx, 60*time.Second)
	defer cancel2()
	geminiClient, err := NewClient(ctx2)
	if err != nil {
		log.Printf("[CreatePersonas] ERROR: Failed to create Gemini client: %v", err)
		return fmt.Errorf("[CreatePersonas] Failed to create Gemini client: %w", err)
	}
	personas, err := geminiClient.GeneratePersonas(ctx2, businessContext)
	if err != nil {
		log.Printf("[CreatePersonas] ERROR: Gemini persona generation failed: %v", err)
		return fmt.Errorf("[CreatePersonas] Gemini persona generation failed: %w", err)
	}
	log.Printf("[CreatePersonas] Successfully generated %d personas from Gemini", len(personas))
	// Color palette
	colors := []string{"#2196f3ff", "#4caf50ff", "#ff9800ff", "#9c27b0ff"}
	// Layout calculation
	anchor := personasAnchor
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
	var personaIDs []string
	var createErrors []error
	for i := 0; i < 4; i++ {
		if w, exists := existingPersonas[i]; exists {
			id, _ := w["id"].(string)
			personaIDs = append(personaIDs, id)
			log.Printf("[CreatePersonas] Using existing persona %d (ID: %s)", i+1, id)
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
		noteCreated := false
		if err != nil {
			log.Printf("[CreatePersonas] ERROR: Failed to create persona note %d (%s): %v", i+1, title, err)
			createErrors = append(createErrors, fmt.Errorf("persona %d (%s): %w", i+1, title, err))
		} else {
			noteWidgetID, _ := noteWidget["id"].(string)
			if noteWidgetID == "" {
				log.Printf("[CreatePersonas] ERROR: Created persona note %d but got empty ID", i+1)
				createErrors = append(createErrors, fmt.Errorf("persona %d (%s): created but got empty ID", i+1, title))
			} else {
				personaIDs = append(personaIDs, noteWidgetID)
				noteCreated = true
				log.Printf("[CreatePersonas] Successfully created persona note %d: %s (ID: %s)", i+1, title, noteWidgetID)
			}
		}
		// Start image generation/upload in a goroutine (only if note was created successfully)
		if noteCreated {
			imgWg.Add(1)
			go func(p Persona, x, imgY, imgW, imgHpx float64, idx int, title string) {
				defer imgWg.Done()
				log.Printf("[CreatePersonas] Calling OpenAI DALL·E for persona: %s", title)
				imgBytes, err := GeneratePersonaImageOpenAI(p)
				if err != nil {
					log.Printf("[CreatePersonas] Persona image not generated for %s: %v", title, err)
					return
				}
				tmpfile, err := os.CreateTemp("", "persona_*.png")
				if err != nil {
					log.Printf("[CreatePersonas] Could not create temp file for persona image %s: %v", title, err)
					return
				}
				imgPath := tmpfile.Name()
				if _, err := tmpfile.Write(imgBytes); err != nil {
					log.Printf("[CreatePersonas] Could not write persona image to temp file %s: %v", title, err)
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
					log.Printf("[CreatePersonas] Failed to upload persona image for %s: %v", title, err)
				} else {
					imgWidgetID, _ := imgWidget["id"].(string)
					log.Printf("[CreatePersonas] Persona image uploaded: %s (ID: %s)", title+" Headshot", imgWidgetID)
				}
				os.Remove(imgPath)
			}(p, x, imgY, imgW, imgHpx, i, title)
		}
	}
	log.Printf("[CreatePersonas] Persona image generation running in background for %d personas", len(personaIDs))
	// --- end Gemini persona generation ---

	// Validate that we have exactly 4 persona IDs
	if len(personaIDs) != 4 {
		errMsg := fmt.Sprintf("Failed to create all 4 personas. Created %d/%d. Errors: %v", len(personaIDs), 4, createErrors)
		log.Printf("[CreatePersonas] ERROR: %s", errMsg)
		return fmt.Errorf("[CreatePersonas] %s", errMsg)
	}

	// Store persona note IDs for this Qnote
	PersonaNoteIDs.Store(qnoteID, personaIDs)
	log.Printf("[CreatePersonas] Successfully created and stored all 4 persona IDs for Qnote %s", qnoteID)
	return nil
}

// getBusinessContext extracts business notes and the personas anchor from the canvas.
func getBusinessContext(ctx context.Context, qnoteID string, client *canvusapi.Client) (string, map[string]interface{}, error) {
	widgets, err := client.GetWidgets(false)
	if err != nil {
		return "", nil, fmt.Errorf("Failed to fetch widgets: %w", err)
	}

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
	var missingNotes []string
	for _, req := range requiredTitles {
		if !titleMap[req] {
			missingNotes = append(missingNotes, req)
			missing = true
		}
	}
	if missing {
		log.Printf("[getBusinessContext] Missing required notes: %v", missingNotes)
		return "", nil, fmt.Errorf("Aborting extraction due to missing notes.")
	}

	if personasAnchor == nil {
		return "", nil, fmt.Errorf("Personas anchor not found. Aborting.")
	}

	log.Printf("[getBusinessContext] Successfully extracted all %d business notes", len(businessNotes))
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

	const minBusinessContextLength = 100 // Minimum useful length for business context
	if len(strings.TrimSpace(businessContext)) < minBusinessContextLength {
		log.Printf("[getBusinessContext] Warning: Business context appears too short (%d characters). Consider adding more details.", len(strings.TrimSpace(businessContext)))
	}

	return businessContext, personasAnchor, nil
}
