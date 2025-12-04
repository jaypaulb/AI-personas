package gemini

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jaypaulb/AI-personas/canvusapi"
	"github.com/jaypaulb/AI-personas/internal/atom"
	"github.com/jaypaulb/AI-personas/internal/molecule"
	"github.com/jaypaulb/AI-personas/internal/timing"
)

// FailedPersonaColor is the red background color for failed persona indicators
const FailedPersonaColor = "#f44336ff"

// MinRequiredPersonas is the minimum number of personas required for partial success
const MinRequiredPersonas = 1

// PersonaWorkflow manages the persona creation workflow state
type PersonaWorkflow struct {
	// State - owned by this organism
	personaNoteIDs sync.Map // qnoteID -> []string (persona note IDs)
}

// NewPersonaWorkflow creates a new PersonaWorkflow instance
func NewPersonaWorkflow() *PersonaWorkflow {
	return &PersonaWorkflow{}
}

// StorePersonaNoteIDs stores the persona note IDs for a QnoteID
func (pw *PersonaWorkflow) StorePersonaNoteIDs(qnoteID string, noteIDs []string) {
	pw.personaNoteIDs.Store(qnoteID, noteIDs)
}

// GetPersonaNoteIDs retrieves the persona note IDs for a QnoteID
func (pw *PersonaWorkflow) GetPersonaNoteIDs(qnoteID string) ([]string, bool) {
	val, ok := pw.personaNoteIDs.Load(qnoteID)
	if !ok {
		return nil, false
	}
	ids, ok := val.([]string)
	return ids, ok
}

// HasPersonaNoteIDs checks if persona note IDs exist for a QnoteID
func (pw *PersonaWorkflow) HasPersonaNoteIDs(qnoteID string) bool {
	_, ok := pw.personaNoteIDs.Load(qnoteID)
	return ok
}

// --- Global instance for backward compatibility ---
var globalPersonaWorkflow = NewPersonaWorkflow()

// GetGlobalPersonaWorkflow returns the global PersonaWorkflow instance
func GetGlobalPersonaWorkflow() *PersonaWorkflow {
	return globalPersonaWorkflow
}

// PersonaNoteIDs is the backward-compatible global reference
// Deprecated: Use PersonaWorkflow methods instead
var PersonaNoteIDs = &globalPersonaWorkflow.personaNoteIDs

// ParsePersonaNote parses a persona note text into a Persona struct
func ParsePersonaNote(text string) Persona {
	return atom.ParsePersonaNote(text)
}

// FetchPersonasFromNotes fetches persona notes by IDs and parses them
// Updated to support partial success - returns available personas even if some are missing
func FetchPersonasFromNotes(qnoteID string, client *canvusapi.Client) ([]Persona, error) {
	idsAny, ok := PersonaNoteIDs.Load(qnoteID)
	if !ok {
		return nil, fmt.Errorf("no persona note IDs for Qnote %s", qnoteID)
	}
	ids, ok := idsAny.([]string)
	if !ok || len(ids) == 0 {
		return nil, fmt.Errorf("invalid persona note IDs for Qnote %s", qnoteID)
	}
	personas := make([]Persona, 0, len(ids))
	var fetchErrors []string
	for _, id := range ids {
		if id == "" {
			continue // Skip empty IDs (failed personas)
		}
		note, err := client.GetNote(id, false)
		if err != nil {
			fetchErrors = append(fetchErrors, fmt.Sprintf("note %s: %v", id, err))
			continue
		}
		text, _ := note["text"].(string)
		personas = append(personas, ParsePersonaNote(text))
	}
	if len(personas) == 0 {
		return nil, fmt.Errorf("failed to fetch any persona notes for Qnote %s: %v", qnoteID, fetchErrors)
	}
	if len(fetchErrors) > 0 {
		log.Printf("[FetchPersonasFromNotes] Partial success: fetched %d/%d personas. Errors: %v", len(personas), len(ids), fetchErrors)
	}
	return personas, nil
}

// CreatePersonas extracts business notes, generates personas, and creates persona notes and images on the canvas.
// Returns error if any required step fails.
func CreatePersonas(ctx context.Context, qnoteID string, client *canvusapi.Client) error {
	// Call the cached version with nil to trigger a fresh fetch
	return CreatePersonasWithCache(ctx, qnoteID, client, nil)
}

// createFailedPersonaNote creates a red indicator note for a persona that failed to generate
func createFailedPersonaNote(client *canvusapi.Client, personaIndex int, reason string, x, y, width, height float64) string {
	noteMeta := map[string]interface{}{
		"title":            fmt.Sprintf("Persona %d: FAILED", personaIndex+1),
		"text":             fmt.Sprintf("Failed to create persona %d.\n\nReason: %s\n\nThis persona will be skipped in Q&A sessions.", personaIndex+1, reason),
		"location":         map[string]interface{}{"x": x, "y": y},
		"size":             map[string]interface{}{"width": width, "height": height},
		"background_color": FailedPersonaColor,
	}
	noteWidget, err := client.CreateNote(noteMeta)
	if err != nil {
		log.Printf("[createFailedPersonaNote] Failed to create failure indicator note for persona %d: %v", personaIndex+1, err)
		return ""
	}
	noteID, _ := noteWidget["id"].(string)
	log.Printf("[createFailedPersonaNote] Created failure indicator for persona %d (ID: %s)", personaIndex+1, noteID)
	return noteID
}

// CreatePersonasWithCache extracts business notes, generates personas, and creates persona notes and images on the canvas.
// If cachedWidgets is provided, it will be used instead of fetching widgets again.
// Supports partial success - continues with minimum 1 persona if some fail.
// Returns error if any required step fails.
func CreatePersonasWithCache(ctx context.Context, qnoteID string, client *canvusapi.Client, cachedWidgets []map[string]interface{}) error {
	// Start end-to-end workflow timing
	workflowTimer := timing.Start("create_personas_workflow")
	defer func() {
		workflowTimer.StopAndLog(true)
	}()

	log.Printf("[CreatePersonas] Starting persona creation for Qnote %s", qnoteID)

	// Step 1: Fetch all widgets (or use cache)
	var widgets []map[string]interface{}
	var err error
	if cachedWidgets != nil {
		widgets = cachedWidgets
		log.Printf("[CreatePersonas] Using cached widgets (%d widgets)", len(widgets))
	} else {
		getWidgetsTimer := timing.Start("create_personas_get_widgets")
		widgets, err = client.GetWidgets(false)
		if err != nil {
			getWidgetsTimer.StopAndLog(false)
			log.Printf("[CreatePersonas] ERROR: Failed to fetch widgets: %v", err)
			return fmt.Errorf("[CreatePersonas] Failed to fetch widgets: %w", err)
		}
		getWidgetsTimer.StopAndLog(true)
		log.Printf("[CreatePersonas] Fetched %d widgets", len(widgets))
	}

	// Use the helper to get business context and anchor (pass cached widgets to avoid redundant fetch)
	businessContextTimer := timing.Start("create_personas_get_business_context")
	businessContext, personasAnchor, missingNotes, err := getBusinessContextWithCacheAndMissing(ctx, qnoteID, client, widgets)
	if err != nil {
		businessContextTimer.StopAndLog(false)
		// If there are missing notes, create a helper note on the canvas
		if len(missingNotes) > 0 {
			molecule.CreateMissingNotesHelper(client, missingNotes, personasAnchor)
		}
		log.Printf("[CreatePersonas] ERROR: Failed to get business context or anchor: %v", err)
		return fmt.Errorf("[CreatePersonas] Failed to get business context or anchor: %w", err)
	}
	businessContextTimer.StopAndLog(true)
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

	// Note: GeneratePersonas is already instrumented in client.go
	personas, err := geminiClient.GeneratePersonas(ctx2, businessContext)
	if err != nil {
		log.Printf("[CreatePersonas] ERROR: Gemini persona generation failed: %v", err)
		return fmt.Errorf("[CreatePersonas] Gemini persona generation failed: %w", err)
	}
	log.Printf("[CreatePersonas] Successfully generated %d personas from Gemini", len(personas))

	// Color palette
	colors := []string{"#2196f3ff", "#4caf50ff", "#ff9800ff", "#9c27b0ff"}

	// Layout calculation with safe type assertions
	anchor := personasAnchor
	anchorLoc, locOK := atom.SafeMap(anchor, "location")
	anchorSize, sizeOK := atom.SafeMap(anchor, "size")
	if !locOK || !sizeOK {
		log.Printf("[CreatePersonas] ERROR: Personas anchor missing location or size")
		return fmt.Errorf("[CreatePersonas] personas anchor missing location or size")
	}

	ax, axOK := atom.SafeFloat64(anchorLoc, "x")
	ay, ayOK := atom.SafeFloat64(anchorLoc, "y")
	aw, awOK := atom.SafeFloat64(anchorSize, "width")
	ah, ahOK := atom.SafeFloat64(anchorSize, "height")
	if !axOK || !ayOK || !awOK || !ahOK {
		log.Printf("[CreatePersonas] ERROR: Personas anchor has invalid location/size values")
		return fmt.Errorf("[CreatePersonas] personas anchor has invalid location/size values")
	}

	border := 0.02
	colW := 0.23
	gap := 0.01
	imgH := 0.10
	var imgWg sync.WaitGroup
	personaIDs := make([]string, 4)      // Fixed size array to maintain positions
	var createErrors []error
	var createErrorsMu sync.Mutex
	successCount := 0
	var successCountMu sync.Mutex

	// Track total note creation time
	noteCreationTimer := timing.Start("create_personas_all_notes")

	for i := 0; i < 4; i++ {
		if w, exists := existingPersonas[i]; exists {
			id, _ := w["id"].(string)
			personaIDs[i] = id
			successCountMu.Lock()
			successCount++
			successCountMu.Unlock()
			log.Printf("[CreatePersonas] Using existing persona %d (ID: %s)", i+1, id)
			continue // Skip existing
		}

		// Handle case where we have fewer personas generated than needed
		if i >= len(personas) {
			log.Printf("[CreatePersonas] WARN: No persona data for index %d (only %d personas generated)", i+1, len(personas))
			// Calculate position for failure note
			x := ax + aw*border + float64(i)*(aw*colW+aw*gap)
			noteY := ay + (ah * 0.34)
			imgW := aw * colW
			noteH := 0.40 * ah
			failedID := createFailedPersonaNote(client, i, "Gemini did not generate enough personas", x, noteY, imgW, noteH)
			personaIDs[i] = failedID // Store even failed IDs for tracking
			createErrorsMu.Lock()
			createErrors = append(createErrors, fmt.Errorf("persona %d: no data from Gemini", i+1))
			createErrorsMu.Unlock()
			continue
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

		// Time each note creation individually
		singleNoteTimer := timing.Start(fmt.Sprintf("create_personas_note_%d", i+1))
		noteWidget, err := client.CreateNote(noteMeta)
		noteCreated := false
		if err != nil {
			singleNoteTimer.StopAndLog(false)
			log.Printf("[CreatePersonas] ERROR: Failed to create persona note %d (%s): %v", i+1, title, err)
			// Create failure indicator note
			failedID := createFailedPersonaNote(client, i, err.Error(), x, noteY, imgW, noteH*ah)
			personaIDs[i] = failedID
			createErrorsMu.Lock()
			createErrors = append(createErrors, fmt.Errorf("persona %d (%s): %w", i+1, title, err))
			createErrorsMu.Unlock()
		} else {
			noteWidgetID, _ := noteWidget["id"].(string)
			if noteWidgetID == "" {
				singleNoteTimer.StopAndLog(false)
				log.Printf("[CreatePersonas] ERROR: Created persona note %d but got empty ID", i+1)
				createErrorsMu.Lock()
				createErrors = append(createErrors, fmt.Errorf("persona %d (%s): created but got empty ID", i+1, title))
				createErrorsMu.Unlock()
			} else {
				singleNoteTimer.StopAndLog(true)
				personaIDs[i] = noteWidgetID
				noteCreated = true
				successCountMu.Lock()
				successCount++
				successCountMu.Unlock()
				log.Printf("[CreatePersonas] Successfully created persona note %d: %s (ID: %s)", i+1, title, noteWidgetID)
			}
		}

		// Start image generation/upload in a goroutine (only if note was created successfully)
		if noteCreated {
			imgWg.Add(1)
			go func(p Persona, x, imgY, imgW, imgHpx float64, idx int, title string) {
				defer imgWg.Done()

				// Time the entire image goroutine operation
				goroutineTimer := timing.Start(fmt.Sprintf("create_personas_image_goroutine_%d", idx+1))

				log.Printf("[CreatePersonas] Calling OpenAI DALL-E for persona: %s", title)

				// Note: GeneratePersonaImageOpenAI is already instrumented in client.go
				// It tracks: openai_dalle_total, openai_dalle_api_attempt_N, openai_dalle_image_download
				imgBytes, err := GeneratePersonaImageOpenAI(p)
				if err != nil {
					timing.LogOperationWithDetails(goroutineTimer.Name(), goroutineTimer.Duration(), false, fmt.Sprintf("error=dalle_generation persona=%s", title))
					goroutineTimer.Stop()
					log.Printf("[CreatePersonas] Persona image not generated for %s: %v", title, err)
					return
				}

				tmpfile, err := os.CreateTemp("", "persona_*.png")
				if err != nil {
					timing.LogOperationWithDetails(goroutineTimer.Name(), goroutineTimer.Duration(), false, fmt.Sprintf("error=temp_file persona=%s", title))
					goroutineTimer.Stop()
					log.Printf("[CreatePersonas] Could not create temp file for persona image %s: %v", title, err)
					return
				}
				imgPath := tmpfile.Name()
				if _, err := tmpfile.Write(imgBytes); err != nil {
					timing.LogOperationWithDetails(goroutineTimer.Name(), goroutineTimer.Duration(), false, fmt.Sprintf("error=write_temp persona=%s", title))
					goroutineTimer.Stop()
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

				// Time the Canvus image upload separately
				uploadTimer := timing.Start(fmt.Sprintf("create_personas_image_upload_%d", idx+1))
				imgWidget, err := client.CreateImage(imgPath, imgMeta)
				if err != nil {
					uploadTimer.StopAndLog(false)
					timing.LogOperationWithDetails(goroutineTimer.Name(), goroutineTimer.Duration(), false, fmt.Sprintf("error=upload persona=%s", title))
					goroutineTimer.Stop()
					log.Printf("[CreatePersonas] Failed to upload persona image for %s: %v", title, err)
				} else {
					uploadTimer.StopAndLog(true)
					imgWidgetID, _ := imgWidget["id"].(string)
					timing.LogOperationWithDetails(goroutineTimer.Name(), goroutineTimer.Duration(), true, fmt.Sprintf("persona=%s image_id=%s", title, imgWidgetID))
					goroutineTimer.Stop()
					log.Printf("[CreatePersonas] Persona image uploaded: %s (ID: %s)", title+" Headshot", imgWidgetID)
				}
				os.Remove(imgPath)
			}(p, x, imgY, imgW, imgHpx, i, title)
		}
	}

	noteCreationTimer.StopAndLog(true)
	log.Printf("[CreatePersonas] Persona image generation running in background for %d personas", successCount)
	// --- end Gemini persona generation ---

	// Check for partial success - need at least MinRequiredPersonas
	if successCount < MinRequiredPersonas {
		errMsg := fmt.Sprintf("Failed to create minimum required personas. Created %d/%d (minimum: %d). Errors: %v", successCount, 4, MinRequiredPersonas, createErrors)
		log.Printf("[CreatePersonas] ERROR: %s", errMsg)
		return fmt.Errorf("[CreatePersonas] %s", errMsg)
	}

	// Log partial success if not all personas were created
	if successCount < 4 {
		log.Printf("[CreatePersonas] WARN: Partial success - created %d/4 personas. Proceeding with available personas. Errors: %v", successCount, createErrors)
	}

	// Filter out empty IDs for storage (keep only valid persona IDs)
	validIDs := make([]string, 0, 4)
	for _, id := range personaIDs {
		if id != "" && !strings.Contains(id, "FAILED") { // Skip failed indicator notes
			validIDs = append(validIDs, id)
		}
	}

	// Store persona note IDs for this Qnote (may be less than 4 in partial success case)
	PersonaNoteIDs.Store(qnoteID, validIDs)
	log.Printf("[CreatePersonas] Successfully created and stored %d persona IDs for Qnote %s", len(validIDs), qnoteID)
	return nil
}

// getBusinessContext extracts business notes and the personas anchor from the canvas.
// Deprecated: Use getBusinessContextWithCache for better performance.
func getBusinessContext(ctx context.Context, qnoteID string, client *canvusapi.Client) (string, map[string]interface{}, error) {
	return getBusinessContextWithCache(ctx, qnoteID, client, nil)
}

// getBusinessContextWithCache extracts business notes and the personas anchor from the canvas.
// If cachedWidgets is provided, it will be used instead of fetching widgets again.
func getBusinessContextWithCache(ctx context.Context, qnoteID string, client *canvusapi.Client, cachedWidgets []map[string]interface{}) (string, map[string]interface{}, error) {
	businessContext, personasAnchor, _, err := getBusinessContextWithCacheAndMissing(ctx, qnoteID, client, cachedWidgets)
	return businessContext, personasAnchor, err
}

// getBusinessContextWithCacheAndMissing extracts business notes and the personas anchor from the canvas.
// Returns the missing notes list for error feedback purposes.
// If cachedWidgets is provided, it will be used instead of fetching widgets again.
func getBusinessContextWithCacheAndMissing(ctx context.Context, qnoteID string, client *canvusapi.Client, cachedWidgets []map[string]interface{}) (string, map[string]interface{}, []string, error) {
	var widgets []map[string]interface{}
	var err error

	if cachedWidgets != nil {
		widgets = cachedWidgets
		// Log that we're using cached widgets (DEBUG only via timing package pattern)
		if timing.IsDebugEnabled() {
			log.Printf("[getBusinessContext] Using cached widgets (%d widgets)", len(widgets))
		}
	} else {
		// Fetch widgets if no cache provided
		getWidgetsTimer := timing.Start("get_business_context_get_widgets")
		widgets, err = client.GetWidgets(false)
		if err != nil {
			getWidgetsTimer.StopAndLog(false)
			return "", nil, nil, fmt.Errorf("Failed to fetch widgets: %w", err)
		}
		getWidgetsTimer.StopAndLog(true)
	}

	businessContext, personasAnchor, missingNotes, err := molecule.ExtractBusinessContext(widgets)
	if err != nil {
		if len(missingNotes) > 0 {
			log.Printf("[getBusinessContext] Missing required notes: %v", missingNotes)
			return "", personasAnchor, missingNotes, fmt.Errorf("Aborting extraction due to missing notes.")
		}
		return "", nil, nil, err
	}

	return businessContext, personasAnchor, nil, nil
}
