package gemini

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jaypaulb/AI-personas/canvusapi"
	"github.com/jaypaulb/AI-personas/internal/atom"
	"github.com/jaypaulb/AI-personas/internal/canvus"
	"github.com/jaypaulb/AI-personas/internal/timing"
)

// MinRequiredAnswers is the minimum number of answers required for partial success
const MinRequiredAnswers = 1

// DefaultQuestionTimeout is the default timeout for waiting for question text (5 minutes)
const DefaultQuestionTimeout = 5 * time.Minute

// TimeoutHelperColor is the amber color for timeout helper notes
const TimeoutHelperColor = "#ff9800ff"

// getQuestionTimeout returns the configured question timeout from env var or default
func getQuestionTimeout() time.Duration {
	timeoutStr := os.Getenv("QUESTION_TIMEOUT")
	if timeoutStr == "" {
		return DefaultQuestionTimeout
	}
	// Try parsing as seconds first
	if seconds, err := strconv.Atoi(timeoutStr); err == nil {
		return time.Duration(seconds) * time.Second
	}
	// Try parsing as duration string (e.g., "5m", "300s")
	if duration, err := time.ParseDuration(timeoutStr); err == nil {
		return duration
	}
	log.Printf("[getQuestionTimeout] Invalid QUESTION_TIMEOUT value '%s', using default %v", timeoutStr, DefaultQuestionTimeout)
	return DefaultQuestionTimeout
}

// QuestionWorkflow manages the Q&A workflow state
type QuestionWorkflow struct {
	// State - owned by this organism
	answeredNotes  sync.Map // noteID -> true
	processingList sync.Map // qnoteID -> true
	waitChans      sync.Map // noteID -> chan struct{}
	helperNotes    sync.Map // qnoteID -> helperNoteID
}

// NewQuestionWorkflow creates a new QuestionWorkflow instance
func NewQuestionWorkflow() *QuestionWorkflow {
	return &QuestionWorkflow{}
}

// IsProcessing checks if the Qnote is already being processed
func (qw *QuestionWorkflow) IsProcessing(qnoteID string) bool {
	_, already := qw.processingList.LoadOrStore(qnoteID, true)
	return already
}

// MarkProcessingComplete removes the Qnote from the processing list
func (qw *QuestionWorkflow) MarkProcessingComplete(qnoteID string) {
	qw.processingList.Delete(qnoteID)
}

// MarkAnswered marks a note as answered
func (qw *QuestionWorkflow) MarkAnswered(noteID string) {
	qw.answeredNotes.Store(noteID, true)
}

// IsAnswered checks if a note has been answered
func (qw *QuestionWorkflow) IsAnswered(noteID string) bool {
	_, ok := qw.answeredNotes.Load(noteID)
	return ok
}

// StoreHelperNote stores the helper note ID for a Qnote
func (qw *QuestionWorkflow) StoreHelperNote(qnoteID, helperID string) {
	qw.helperNotes.Store(qnoteID, helperID)
}

// GetHelperNote gets the helper note ID for a Qnote
func (qw *QuestionWorkflow) GetHelperNote(qnoteID string) (string, bool) {
	val, ok := qw.helperNotes.Load(qnoteID)
	if !ok {
		return "", false
	}
	return val.(string), true
}

// DeleteHelperNote removes the helper note tracking for a Qnote
func (qw *QuestionWorkflow) DeleteHelperNote(qnoteID string) {
	qw.helperNotes.Delete(qnoteID)
}

// --- Global instance for backward compatibility ---
var globalQuestionWorkflow = NewQuestionWorkflow()

// GetGlobalQuestionWorkflow returns the global QuestionWorkflow instance
func GetGlobalQuestionWorkflow() *QuestionWorkflow {
	return globalQuestionWorkflow
}

// --- Backward compatibility wrappers using global state ---
// These will be deprecated once all callers are updated

var answeredNotes = &globalQuestionWorkflow.answeredNotes
var qnoteProcessingList = &globalQuestionWorkflow.processingList
var qnoteWaitChans = &globalQuestionWorkflow.waitChans
var qnoteHelperNotes = &globalQuestionWorkflow.helperNotes

// IsQnoteProcessing checks if the Qnote is already being processed.
func IsQnoteProcessing(qnoteID string) bool {
	if _, already := qnoteProcessingList.LoadOrStore(qnoteID, true); already {
		return true
	}
	return false
}

// CheckPersonasPresent checks for the presence of all 4 persona notes for the Qnote.
// Note: This function calls GetWidgets - use CheckPersonasPresentWithCache for better performance.
func CheckPersonasPresent(qnoteID string, client *canvusapi.Client) bool {
	return CheckPersonasPresentWithCache(qnoteID, client, nil)
}

// CheckPersonasPresentWithCache checks for the presence of persona notes for the Qnote.
// Updated to support partial success - returns true if at least MinRequiredPersonas are present.
// If cachedWidgets is provided, it will be used instead of fetching widgets again.
// Returns: bool (personas present), []map[string]interface{} (widgets for reuse)
func CheckPersonasPresentWithCache(qnoteID string, client *canvusapi.Client, cachedWidgets []map[string]interface{}) bool {
	var widgets []map[string]interface{}
	var err error

	if cachedWidgets != nil {
		widgets = cachedWidgets
		if timing.IsDebugEnabled() {
			log.Printf("[CheckPersonasPresent] Using cached widgets (%d widgets)", len(widgets))
		}
	} else {
		getWidgetsTimer := timing.Start("check_personas_present_get_widgets")
		widgets, err = client.GetWidgets(false)
		if err != nil {
			getWidgetsTimer.StopAndLog(false)
			return false
		}
		getWidgetsTimer.StopAndLog(true)
	}

	personaCount := 0
	for _, w := range widgets {
		typeStr, _ := w["widget_type"].(string)
		title, _ := w["title"].(string)
		if typeStr == "Note" && strings.HasPrefix(strings.TrimSpace(title), "Persona ") {
			// Skip failed persona indicators
			if !strings.Contains(title, "FAILED") {
				personaCount++
			}
		}
	}
	// Support partial success - require at least MinRequiredPersonas (but prefer 4)
	if personaCount >= 4 {
		log.Printf("[personas-check] All 4 persona notes present for Qnote %s.", qnoteID)
		return true
	}
	if personaCount >= MinRequiredPersonas {
		log.Printf("[personas-check] Partial personas present (%d/%d) for Qnote %s. Proceeding with available personas.", personaCount, 4, qnoteID)
		return true
	}
	return false
}

// CheckQuestionPresent checks if the Qnote contains a question.
func CheckQuestionPresent(qnoteID string, client *canvusapi.Client) bool {
	qWidget, err := client.GetNote(qnoteID, false)
	if err != nil {
		return false
	}
	currText, _ := qWidget["text"].(string)
	trimmedText := strings.TrimSpace(currText)
	if strings.HasSuffix(trimmedText, "?") {
		return true
	}
	return false
}

// BuildConnectorPayload creates a Canvus connector payload between two widgets
func BuildConnectorPayload(srcID, dstID string) map[string]interface{} {
	return atom.BuildConnectorPayload(srcID, dstID)
}

// EnsureHelperNoteForQuestion always creates or updates the helper note and connector, sets Qnote to amber, then calls MonitorQuestionNote
// Note: This function calls GetWidgets - use EnsureHelperNoteForQuestionWithCache for better performance.
func EnsureHelperNoteForQuestion(qnoteID string, client *canvusapi.Client) {
	EnsureHelperNoteForQuestionWithCache(qnoteID, client, nil)
}

// EnsureHelperNoteForQuestionWithCache creates or updates the helper note and connector, sets Qnote to amber.
// If cachedWidgets is provided, it will be used instead of fetching widgets again.
func EnsureHelperNoteForQuestionWithCache(qnoteID string, client *canvusapi.Client, cachedWidgets []map[string]interface{}) {
	qWidget, err := client.GetNote(qnoteID, false)
	if err != nil {
		return
	}
	qLoc, _ := qWidget["location"].(map[string]interface{})
	qx := qLoc["x"].(float64)
	qy := qLoc["y"].(float64)
	qSize, _ := qWidget["size"].(map[string]interface{})
	qw := qSize["width"].(float64)
	qh := qSize["height"].(float64)
	helperTitle := "Helper: Please enter a question for this note"

	var widgets []map[string]interface{}
	if cachedWidgets != nil {
		widgets = cachedWidgets
		if timing.IsDebugEnabled() {
			log.Printf("[EnsureHelperNoteForQuestion] Using cached widgets (%d widgets)", len(widgets))
		}
	} else {
		getWidgetsTimer := timing.Start("ensure_helper_note_get_widgets")
		widgets, err = client.GetWidgets(false)
		if err != nil {
			getWidgetsTimer.StopAndLog(false)
			return
		}
		getWidgetsTimer.StopAndLog(true)
	}

	var helperID string
	found := false
	for _, w := range widgets {
		typeStr, _ := w["widget_type"].(string)
		title, _ := w["title"].(string)
		if typeStr == "Note" && title == helperTitle {
			helperID, _ = w["id"].(string)
			found = true
			break
		}
	}
	if !found {
		helperX := qx - 1.2*qw
		helperY := qy - 0.33*qh
		noteMeta := map[string]interface{}{
			"title":            helperTitle,
			"text":             "Please enter a question in the main note to begin the Q&A process.",
			"location":         map[string]interface{}{"x": helperX, "y": helperY},
			"size":             map[string]interface{}{"width": qw, "height": qh * 0.7},
			"background_color": "#e0e0e0",
		}
		helperNote, err := client.CreateNote(noteMeta)
		if err != nil {
			return
		}
		helperID, _ = helperNote["id"].(string)
		connMeta := BuildConnectorPayload(helperID, qnoteID)
		if _, err := client.CreateConnector(connMeta); err != nil {
			log.Printf("[warn] CreateConnector failed for helper note: %v", err)
		}
		log.Printf("[helper-note] Created helper note and connector for Qnote %s.", qnoteID)
	}
	// Track the helper note ID for this Qnote
	qnoteHelperNotes.Store(qnoteID, helperID)
	updateResp, err := client.UpdateNote(qnoteID, map[string]interface{}{"background_color": "#ffe4b3"})
	if err != nil {
		log.Printf("[warn] UpdateNote failed setting amber color for Qnote %s: %v", qnoteID, err)
	}
	exactAmber, _ := updateResp["background_color"].(string)
	log.Printf("[monitor] Qnote color set to: %q for noteID: %s", exactAmber, qnoteID)
}

// OnQuestionDetected updates helper note and Qnote when a question is detected, then calls AnswerQuestion.
// Note: This function calls GetWidgets - use OnQuestionDetectedWithCache for better performance.
func OnQuestionDetected(qnoteID string, client *canvusapi.Client, chatTokenLimit int) {
	OnQuestionDetectedWithCache(qnoteID, client, chatTokenLimit, nil)
}

// OnQuestionDetectedWithCache updates helper note and Qnote when a question is detected, then calls AnswerQuestion.
// If cachedWidgets is provided, it will be used instead of fetching widgets again.
func OnQuestionDetectedWithCache(qnoteID string, client *canvusapi.Client, chatTokenLimit int, cachedWidgets []map[string]interface{}) {
	// Update helper note to 'Processing Question'
	helperTitle := "Helper: Please enter a question for this note"

	var widgets []map[string]interface{}
	var err error
	if cachedWidgets != nil {
		widgets = cachedWidgets
		if timing.IsDebugEnabled() {
			log.Printf("[OnQuestionDetected] Using cached widgets (%d widgets)", len(widgets))
		}
	} else {
		getWidgetsTimer := timing.Start("on_question_detected_get_widgets")
		widgets, err = client.GetWidgets(false)
		getWidgetsTimer.StopAndLog(err == nil)
	}

	if err == nil && widgets != nil {
		for _, w := range widgets {
			typeStr, _ := w["widget_type"].(string)
			title, _ := w["title"].(string)
			if typeStr == "Note" && title == helperTitle {
				noteID2, _ := w["id"].(string)
				update := map[string]interface{}{
					"text": "Processing Question...",
				}
				if _, err := client.UpdateNote(noteID2, update); err != nil {
					log.Printf("[warn] UpdateNote failed for helper note %s: %v", noteID2, err)
				}
			}
		}
	}
	// Set Qnote to amber
	updateQ := map[string]interface{}{
		"background_color": "#ffe4b3",
	}
	if _, err := client.UpdateNote(qnoteID, updateQ); err != nil {
		log.Printf("[warn] UpdateNote failed setting amber color for Qnote %s: %v", qnoteID, err)
	}
	// Call AnswerQuestion with cached widgets
	AnswerQuestionWithCache(qnoteID, client, chatTokenLimit, widgets)
}

// getAnswerGenerationMessage returns the appropriate wait message based on the model type
func getAnswerGenerationMessage() string {
	return atom.GetAnswerGenerationMessage()
}

// AnswerQuestion handles persona answers, meta-answers, note creation, and connectors.
func AnswerQuestion(qnoteID string, client *canvusapi.Client, chatTokenLimit int) {
	AnswerQuestionWithCache(qnoteID, client, chatTokenLimit, nil)
}

// AnswerQuestionWithCache handles persona answers, meta-answers, note creation, and connectors.
// If cachedWidgets is provided, it will be used where possible instead of fetching widgets again.
// Supports partial success - continues with minimum 1 answer if some fail.
func AnswerQuestionWithCache(qnoteID string, client *canvusapi.Client, chatTokenLimit int, cachedWidgets []map[string]interface{}) {
	// Start end-to-end workflow timing
	workflowTimer := timing.Start("answer_question_workflow")
	defer func() {
		workflowTimer.StopAndLog(true)
	}()

	ctx := context.Background()
	defer func() {
		qnoteProcessingList.Delete(qnoteID)
	}()
	qWidget, _ := client.GetNote(qnoteID, false)
	currText, _ := qWidget["text"].(string)

	// Get the appropriate wait message based on model type
	waitMessage := getAnswerGenerationMessage()

	// Create or update helper note to show "Generating answers, please wait..."
	helperTitle := "Helper: Please enter a question for this note"
	qLoc, _ := qWidget["location"].(map[string]interface{})
	qSize, _ := qWidget["size"].(map[string]interface{})
	qx := qLoc["x"].(float64)
	qy := qLoc["y"].(float64)
	qw := qSize["width"].(float64)
	qh := qSize["height"].(float64)

	var helperID string
	var widgets []map[string]interface{}
	var err error

	// Use cached widgets if available, otherwise fetch
	if cachedWidgets != nil {
		widgets = cachedWidgets
		if timing.IsDebugEnabled() {
			log.Printf("[AnswerQuestion] Using cached widgets (%d widgets)", len(widgets))
		}
	} else {
		getWidgetsTimer := timing.Start("answer_question_get_widgets_helper")
		widgets, err = client.GetWidgets(false)
		getWidgetsTimer.StopAndLog(err == nil)
	}

	if err == nil && widgets != nil {
		for _, w := range widgets {
			typeStr, _ := w["widget_type"].(string)
			title, _ := w["title"].(string)
			if typeStr == "Note" && title == helperTitle {
				helperID, _ = w["id"].(string)
				// Update existing helper note
				update := map[string]interface{}{
					"text": waitMessage,
				}
				if _, err := client.UpdateNote(helperID, update); err != nil {
					log.Printf("[warn] UpdateNote failed for helper note %s: %v", helperID, err)
				}
				qnoteHelperNotes.Store(qnoteID, helperID)
				break
			}
		}
	}
	// If helper note doesn't exist, create it
	if helperID == "" {
		helperX := qx - 1.2*qw
		helperY := qy - 0.33*qh
		noteMeta := map[string]interface{}{
			"title":            helperTitle,
			"text":             waitMessage,
			"location":         map[string]interface{}{"x": helperX, "y": helperY},
			"size":             map[string]interface{}{"width": qw, "height": qh * 0.7},
			"background_color": "#e0e0e0",
		}
		helperNote, err := client.CreateNote(noteMeta)
		if err == nil {
			helperID, _ = helperNote["id"].(string)
			connMeta := BuildConnectorPayload(helperID, qnoteID)
			if _, err := client.CreateConnector(connMeta); err != nil {
				log.Printf("[warn] CreateConnector failed for helper note: %v", err)
			}
			qnoteHelperNotes.Store(qnoteID, helperID)
			log.Printf("[helper-note] Created answer generation helper note for Qnote %s.", qnoteID)
		}
	}

	geminiClient, err := NewClient(ctx)
	if err != nil {
		return
	}
	// Ensure personas exist and get their IDs (pass cached widgets)
	if _, ok := PersonaNoteIDs.Load(qnoteID); !ok {
		err = CreatePersonasWithCache(ctx, qnoteID, client, widgets)
		if err != nil {
			log.Printf("[AnswerQuestion] CreatePersonas failed: %v", err)
			return
		}
	}
	personas, err := FetchPersonasFromNotes(qnoteID, client)
	if err != nil || len(personas) < MinRequiredPersonas {
		// Try to recreate personas if not enough are available (pass cached widgets)
		err = CreatePersonasWithCache(ctx, qnoteID, client, widgets)
		if err != nil {
			log.Printf("[AnswerQuestion] CreatePersonas failed: %v", err)
			return
		}
		personas, err = FetchPersonasFromNotes(qnoteID, client)
		if err != nil || len(personas) < MinRequiredPersonas {
			log.Printf("[AnswerQuestion] Could not fetch minimum required personas (%d) after CreatePersonas: %v", MinRequiredPersonas, err)
			return
		}
	}

	numPersonas := len(personas)
	log.Printf("[AnswerQuestion] Working with %d personas", numPersonas)

	colors := []string{"#2196f3ff", "#4caf50ff", "#ff9800ff", "#9c27b0ff"}
	// qLoc, qSize, qx, qy, qw, qh already extracted above for helper note
	scale := 1.0
	if s, ok := qWidget["scale"].(float64); ok {
		scale = s
	} else if s, ok := qSize["scale"].(float64); ok {
		scale = s
	}
	sessionManager := NewSessionManager(geminiClient.GenaiClient())
	// --- Persona Q&A Workflow ---
	question := currText
	if idx := strings.Index(question, "-->"); idx != -1 {
		question = question[idx+3:]
	}
	question = strings.TrimSpace(strings.Split(question, "Please wait")[0])

	// Get business context (pass cached widgets to avoid redundant fetch)
	businessContextStr, _, err := getBusinessContextWithCache(ctx, qnoteID, client, widgets)
	if err != nil {
		log.Printf("[AnswerQuestion] Failed to get business context: %v", err)
		return // Or handle this error appropriately
	}

	spacing := (qw * scale) / 5.0
	log.Printf("[AnswerQuestion] Spacing set to %.4f units (qw=%.4f * scale=%.4f / 5.0)", spacing, qw, scale)
	// Layout: center (Q), top (A1), right (A2), bottom (A3), left (A4), then diagonals for meta
	answerPositions := [][2]int{{0, -1}, {1, 0}, {0, 1}, {-1, 0}} // top, right, bottom, left
	metaPositions := [][2]int{{1, -1}, {1, 1}, {-1, 1}, {-1, -1}} // top-right, bottom-right, bottom-left, top-left
	answerNoteIDs := make([]string, numPersonas)
	metaNoteIDs := make([]string, numPersonas)

	// 1. Generate persona answers in parallel (all Gemini API calls simultaneously)
	answerGenTimer := timing.Start("answer_question_persona_answers")
	startTime := time.Now()
	log.Printf("[AnswerQuestion] Starting parallel generation of %d persona answers...", numPersonas)
	var ansWg sync.WaitGroup
	ansWg.Add(numPersonas)
	answers := make([]string, numPersonas)
	answerErrors := make([]error, numPersonas)
	var answerErrorsMu sync.Mutex
	for i, p := range personas {
		go func(i int, p Persona) {
			defer ansWg.Done()
			answer, err := geminiClient.AnswerQuestion(ctx, p, question, sessionManager, businessContextStr)
			if err != nil {
				answerErrorsMu.Lock()
				answerErrors[i] = fmt.Errorf("persona %s: %w", p.Name, err)
				answerErrorsMu.Unlock()
				log.Printf("[AnswerQuestion] ERROR: Failed to generate answer for persona %s: %v", p.Name, err)
				return
			}
			if len(answer) > chatTokenLimit {
				succinctPrompt := "Please rephrase your answer in a much more succinct, short, and verbal way. Limit your response to " + fmt.Sprintf("%d", chatTokenLimit) + " characters."
				answer, err = geminiClient.AnswerQuestion(ctx, p, succinctPrompt, sessionManager, businessContextStr)
				if err != nil {
					answerErrorsMu.Lock()
					answerErrors[i] = fmt.Errorf("persona %s (succinct): %w", p.Name, err)
					answerErrorsMu.Unlock()
					log.Printf("[AnswerQuestion] ERROR: Failed to generate succinct answer for persona %s: %v", p.Name, err)
					return
				}
			}
			answers[i] = answer
		}(i, p)
	}
	ansWg.Wait()
	personaAnswerDuration := time.Since(startTime)
	answerGenTimer.StopAndLog(true)
	log.Printf("[AnswerQuestion] Completed parallel generation of %d persona answers in %.2f seconds", numPersonas, personaAnswerDuration.Seconds())

	// Count successful answers
	successfulAnswers := 0
	for i := 0; i < numPersonas; i++ {
		if answers[i] != "" && answerErrors[i] == nil {
			successfulAnswers++
		}
	}

	// Check for minimum required answers
	if successfulAnswers < MinRequiredAnswers {
		log.Printf("[AnswerQuestion] ERROR: Failed to generate minimum required answers. Got %d/%d (minimum: %d)", successfulAnswers, numPersonas, MinRequiredAnswers)
		// Log all errors
		for i, err := range answerErrors {
			if err != nil {
				log.Printf("[AnswerQuestion] Answer error %d: %v", i+1, err)
			}
		}
		return
	}

	if successfulAnswers < numPersonas {
		log.Printf("[AnswerQuestion] WARN: Partial success - generated %d/%d answers. Proceeding with available answers.", successfulAnswers, numPersonas)
	}

	// 2. Create answer notes in parallel (all note creations simultaneously)
	answerNoteTimer := timing.Start("answer_question_create_answer_notes")
	var ansNoteWg sync.WaitGroup
	ansNoteWg.Add(numPersonas)
	for i, p := range personas {
		go func(i int, p Persona) {
			defer ansNoteWg.Done()
			// Skip if answer generation failed
			if answers[i] == "" || answerErrors[i] != nil {
				log.Printf("[AnswerQuestion] Skipping note creation for persona %s - no answer generated", p.Name)
				answerNoteIDs[i] = ""
				return
			}
			pos := answerPositions[i%len(answerPositions)]
			ansX := qx + float64(pos[0])*((qw*scale)+spacing)
			ansY := qy + float64(pos[1])*((qh*scale)+spacing)
			noteMeta := map[string]interface{}{
				"title":            p.Name + " Answer",
				"text":             answers[i],
				"location":         map[string]interface{}{"x": ansX, "y": ansY},
				"size":             map[string]interface{}{"width": qw, "height": qh},
				"background_color": colors[i%len(colors)],
				"scale":            scale,
			}
			singleNoteTimer := timing.Start(fmt.Sprintf("answer_question_create_answer_note_%d", i+1))
			ansNote, err := client.CreateNote(noteMeta)
			if err != nil {
				singleNoteTimer.StopAndLog(false)
				log.Printf("[AnswerQuestion] ERROR: Failed to create answer note for persona %s: %v", p.Name, err)
				answerNoteIDs[i] = ""
				return
			}
			ansNoteID, ok := ansNote["id"].(string)
			if !ok || ansNoteID == "" {
				singleNoteTimer.StopAndLog(false)
				log.Printf("[AnswerQuestion] ERROR: Created answer note for persona %s but got empty ID", p.Name)
				answerNoteIDs[i] = ""
				return
			}
			singleNoteTimer.StopAndLog(true)
			answerNoteIDs[i] = ansNoteID
		}(i, p)
	}
	ansNoteWg.Wait()
	answerNoteTimer.StopAndLog(true)

	// 3. Generate meta-answers in parallel (all Gemini API calls simultaneously)
	metaGenTimer := timing.Start("answer_question_meta_answers")
	metaStartTime := time.Now()
	log.Printf("[AnswerQuestion] Starting parallel generation of %d meta-answers...", numPersonas)
	var metaWg sync.WaitGroup
	metaWg.Add(numPersonas)
	metaAnswers := make([]string, numPersonas)
	metaErrors := make([]error, numPersonas)
	var metaErrorsMu sync.Mutex
	for i, p := range personas {
		go func(i int, p Persona) {
			defer metaWg.Done()
			// Skip if original answer failed
			if answers[i] == "" || answerErrors[i] != nil {
				return
			}
			others := []string{}
			for j, ans := range answers {
				if i != j && ans != "" && answerErrors[j] == nil {
					others = append(others, fmt.Sprintf("%s said: %s", personas[j].Name, ans))
				}
			}
			if len(others) == 0 {
				// No other answers to react to
				metaAnswers[i] = "No other responses to react to."
				return
			}
			metaPrompt := fmt.Sprintf("Thank you %s for the interesting answer. Does what you heard from the others change what you think in any way? You heard: %s", p.Name, strings.Join(others, "; "))
			metaAnswer, err := geminiClient.AnswerQuestion(ctx, p, metaPrompt, sessionManager, businessContextStr)
			if err != nil {
				metaErrorsMu.Lock()
				metaErrors[i] = fmt.Errorf("persona %s meta: %w", p.Name, err)
				metaErrorsMu.Unlock()
				log.Printf("[AnswerQuestion] ERROR: Failed to generate meta-answer for persona %s: %v", p.Name, err)
				return
			}
			if len(metaAnswer) > chatTokenLimit {
				succinctPrompt := "Please rephrase your answer in a much more succinct, short, and verbal way. Limit your response to " + fmt.Sprintf("%d", chatTokenLimit) + " characters."
				metaAnswer, err = geminiClient.AnswerQuestion(ctx, p, succinctPrompt, sessionManager, businessContextStr)
				if err != nil {
					metaErrorsMu.Lock()
					metaErrors[i] = fmt.Errorf("persona %s meta (succinct): %w", p.Name, err)
					metaErrorsMu.Unlock()
					log.Printf("[AnswerQuestion] ERROR: Failed to generate succinct meta-answer for persona %s: %v", p.Name, err)
					return
				}
			}
			metaAnswers[i] = metaAnswer
		}(i, p)
	}
	metaWg.Wait()
	metaAnswerDuration := time.Since(metaStartTime)
	metaGenTimer.StopAndLog(true)
	log.Printf("[AnswerQuestion] Completed parallel generation of meta-answers in %.2f seconds", metaAnswerDuration.Seconds())

	// Log meta-answer partial success
	successfulMeta := 0
	for i := 0; i < numPersonas; i++ {
		if metaAnswers[i] != "" && metaErrors[i] == nil {
			successfulMeta++
		}
	}
	if successfulMeta < numPersonas {
		log.Printf("[AnswerQuestion] WARN: Generated %d/%d meta-answers", successfulMeta, numPersonas)
	}

	// 4. Create meta answer notes in parallel (all note creations simultaneously)
	metaNoteTimer := timing.Start("answer_question_create_meta_notes")
	var metaNoteWg sync.WaitGroup
	metaNoteWg.Add(numPersonas)
	for i, p := range personas {
		go func(i int, p Persona) {
			defer metaNoteWg.Done()
			// Skip if meta-answer generation failed or original answer failed
			if metaAnswers[i] == "" || metaErrors[i] != nil || answerNoteIDs[i] == "" {
				metaNoteIDs[i] = ""
				return
			}
			metaPos := metaPositions[i%len(metaPositions)]
			metaX := qx + float64(metaPos[0])*((qw*scale)+spacing)
			metaY := qy + float64(metaPos[1])*((qh*scale)+spacing)
			metaMeta := map[string]interface{}{
				"title":            p.Name + " Meta Answer",
				"text":             metaAnswers[i],
				"location":         map[string]interface{}{"x": metaX, "y": metaY},
				"size":             map[string]interface{}{"width": qw, "height": qh},
				"background_color": colors[i%len(colors)],
				"scale":            scale,
			}
			singleMetaNoteTimer := timing.Start(fmt.Sprintf("answer_question_create_meta_note_%d", i+1))
			metaNote, err := client.CreateNote(metaMeta)
			if err != nil {
				singleMetaNoteTimer.StopAndLog(false)
				log.Printf("[AnswerQuestion] ERROR: Failed to create meta note for persona %s: %v", p.Name, err)
				metaNoteIDs[i] = ""
				return
			}
			metaNoteID, ok := metaNote["id"].(string)
			if !ok || metaNoteID == "" {
				singleMetaNoteTimer.StopAndLog(false)
				log.Printf("[AnswerQuestion] ERROR: Created meta note for persona %s but got empty ID", p.Name)
				metaNoteIDs[i] = ""
				return
			}
			singleMetaNoteTimer.StopAndLog(true)
			metaNoteIDs[i] = metaNoteID
		}(i, p)
	}
	metaNoteWg.Wait()
	metaNoteTimer.StopAndLog(true)

	// 5. Create connectors in parallel: question -> answer, answer -> meta answer (matching layout)
	connectorTimer := timing.Start("answer_question_create_connectors")
	var connWg sync.WaitGroup
	connectorCount := 0
	var connCountMu sync.Mutex
	for i := 0; i < numPersonas; i++ {
		if answerNoteIDs[i] == "" {
			continue
		}
		connWg.Add(1)
		go func(i int) {
			defer connWg.Done()
			connMeta1 := BuildConnectorPayload(qnoteID, answerNoteIDs[i])
			if _, err := client.CreateConnector(connMeta1); err != nil {
				log.Printf("[AnswerQuestion] ERROR: Failed to create connector from question to answer %d: %v", i+1, err)
				return
			}
			connCountMu.Lock()
			connectorCount++
			connCountMu.Unlock()
			if metaNoteIDs[i] == "" {
				return
			}
			connMeta2 := BuildConnectorPayload(answerNoteIDs[i], metaNoteIDs[i])
			if _, err := client.CreateConnector(connMeta2); err != nil {
				log.Printf("[AnswerQuestion] ERROR: Failed to create connector from answer to meta-answer %d: %v", i+1, err)
				return
			}
			connCountMu.Lock()
			connectorCount++
			connCountMu.Unlock()
		}(i)
	}
	connWg.Wait()
	timing.LogOperationWithDetails(connectorTimer.Name(), connectorTimer.Duration(), true, fmt.Sprintf("connectors_created=%d", connectorCount))
	connectorTimer.Stop()

	// --- Create anchor for answer/meta notes ---
	allNoteIDs := []string{}
	for _, id := range answerNoteIDs {
		if id != "" {
			allNoteIDs = append(allNoteIDs, id)
		}
	}
	for _, id := range metaNoteIDs {
		if id != "" {
			allNoteIDs = append(allNoteIDs, id)
		}
	}
	if len(allNoteIDs) > 0 {
		anchorTimer := timing.Start("answer_question_create_anchor")

		// Note: This GetWidgets call needs fresh data to get the newly created notes' positions
		// Cannot use cached widgets here as they were fetched before note creation
		getWidgetsAnchorTimer := timing.Start("answer_question_get_widgets_anchor")
		freshWidgets, err := client.GetWidgets(false)
		getWidgetsAnchorTimer.StopAndLog(err == nil)

		if err == nil {
			minX, minY := 1e9, 1e9
			maxX, maxY := -1e9, -1e9
			noteCount := 0
			for _, w := range freshWidgets {
				id, _ := w["id"].(string)
				for _, targetID := range allNoteIDs {
					if id == targetID {
						loc, _ := w["location"].(map[string]interface{})
						size, _ := w["size"].(map[string]interface{})
						x, _ := loc["x"].(float64)
						y, _ := loc["y"].(float64)
						w_, _ := size["width"].(float64)
						h_, _ := size["height"].(float64)
						if x < minX {
							minX = x
						}
						if y < minY {
							minY = y
						}
						if x+w_ > maxX {
							maxX = x + w_
						}
						if y+h_ > maxY {
							maxY = y + h_
						}
						noteCount++
						break
					}
				}
			}
			if noteCount > 0 {
				anchorPayload := map[string]interface{}{
					"anchor_name": question + " (Script Made)",
					"location":    map[string]interface{}{"x": minX, "y": minY},
					"size":        map[string]interface{}{"width": maxX - minX, "height": maxY - minY},
					"notes":       allNoteIDs,
				}
				if anchorResp, err := client.CreateAnchor(anchorPayload); err == nil {
					log.Printf("[anchor] Created anchor for Qnote %s: %v", qnoteID, anchorResp)
					anchorTimer.StopAndLog(true)
				} else {
					log.Printf("[anchor] Failed to create anchor for Qnote %s: %v", qnoteID, err)
					anchorTimer.StopAndLog(false)
				}
			} else {
				anchorTimer.StopAndLog(false)
			}
		} else {
			anchorTimer.StopAndLog(false)
		}
	}
	// After all, set question note color to pastel green and restore only the original question
	origQ := currText
	if idx := strings.Index(origQ, "-->"); idx != -1 {
		origQ = origQ[idx+3:]
	}
	origQ = strings.TrimSpace(strings.Split(origQ, "Please wait")[0])
	if _, err := client.UpdateNote(qnoteID, map[string]interface{}{"background_color": "#ccffcc", "text": origQ}); err != nil {
		log.Printf("[warn] UpdateNote failed setting green color for Qnote %s: %v", qnoteID, err)
	}
	answeredNotes.Store(qnoteID, true)
	// Delete the helper note associated with this Qnote (by tracked ID)
	if val, ok := qnoteHelperNotes.Load(qnoteID); ok {
		helperID := val.(string)
		if err := client.DeleteNote(helperID); err != nil {
			log.Printf("[warn] DeleteNote failed for helper note %s: %v", helperID, err)
		}
		log.Printf("[helper-note] Deleted helper note %s for Qnote %s.", helperID, qnoteID)
		qnoteHelperNotes.Delete(qnoteID)
	}
	log.Printf("[step] AnswerQuestion completed for noteID: %s (answers: %d/%d, meta: %d/%d)", qnoteID, successfulAnswers, numPersonas, successfulMeta, numPersonas)
}

// CleanupAfterAnswer deletes helper notes, stops monitors, and removes from processing list.
func CleanupAfterAnswer(qnoteID string, client *canvusapi.Client) {
	log.Printf("[step] CleanupAfterAnswer called for noteID: %s", qnoteID)
	// Only delete the helper note associated with this Qnote (by tracked ID)
	if val, ok := qnoteHelperNotes.Load(qnoteID); ok {
		helperID := val.(string)
		if err := client.DeleteNote(helperID); err != nil {
			log.Printf("[warn] DeleteNote failed for helper note %s: %v", helperID, err)
		}
		log.Printf("[helper-note] Deleted helper note %s for Qnote %s.", helperID, qnoteID)
		qnoteHelperNotes.Delete(qnoteID)
	}
	qnoteProcessingList.Delete(qnoteID)
}

// EnsureHelperNoteForPersonas creates a persona waiting helper note.
// Note: This function calls GetWidgets - use EnsureHelperNoteForPersonasWithCache for better performance.
func EnsureHelperNoteForPersonas(qnoteID string, client *canvusapi.Client) {
	EnsureHelperNoteForPersonasWithCache(qnoteID, client, nil)
}

// EnsureHelperNoteForPersonasWithCache creates a persona waiting helper note.
// If cachedWidgets is provided, it will be used instead of fetching widgets again.
func EnsureHelperNoteForPersonasWithCache(qnoteID string, client *canvusapi.Client, cachedWidgets []map[string]interface{}) {
	qWidget, err := client.GetNote(qnoteID, false)
	if err != nil {
		return
	}
	qLoc, _ := qWidget["location"].(map[string]interface{})
	qx := qLoc["x"].(float64)
	qy := qLoc["y"].(float64)
	qSize, _ := qWidget["size"].(map[string]interface{})
	qw := qSize["width"].(float64)
	qh := qSize["height"].(float64)
	helperTitle := "Helper: Generating personas, please wait..."

	var widgets []map[string]interface{}
	if cachedWidgets != nil {
		widgets = cachedWidgets
		if timing.IsDebugEnabled() {
			log.Printf("[EnsureHelperNoteForPersonas] Using cached widgets (%d widgets)", len(widgets))
		}
	} else {
		getWidgetsTimer := timing.Start("ensure_helper_note_personas_get_widgets")
		widgets, err = client.GetWidgets(false)
		if err != nil {
			getWidgetsTimer.StopAndLog(false)
			return
		}
		getWidgetsTimer.StopAndLog(true)
	}

	var helperID string
	found := false
	for _, w := range widgets {
		typeStr, _ := w["widget_type"].(string)
		title, _ := w["title"].(string)
		if typeStr == "Note" && title == helperTitle {
			helperID, _ = w["id"].(string)
			found = true
			break
		}
	}
	if !found {
		helperX := qx - 1.2*qw
		helperY := qy - 1.1*qh // Place it above the Qnote
		noteMeta := map[string]interface{}{
			"title":            helperTitle,
			"text":             "Personas are being generated. Please wait before proceeding.",
			"location":         map[string]interface{}{"x": helperX, "y": helperY},
			"size":             map[string]interface{}{"width": qw, "height": qh * 0.7},
			"background_color": "#e0e0e0",
		}
		helperNote, err := client.CreateNote(noteMeta)
		if err != nil {
			return
		}
		helperID, _ = helperNote["id"].(string)
		connMeta := BuildConnectorPayload(helperID, qnoteID)
		if _, err := client.CreateConnector(connMeta); err != nil {
			log.Printf("[warn] CreateConnector failed for persona helper note: %v", err)
		}
		log.Printf("[helper-note] Created persona waiting helper note and connector for Qnote %s.", qnoteID)
	}
	// Track the helper note ID for this Qnote
	qnoteHelperNotes.Store(qnoteID, helperID)
	updateResp, err := client.UpdateNote(qnoteID, map[string]interface{}{"background_color": "#ffe4b3"})
	if err != nil {
		log.Printf("[warn] UpdateNote failed setting amber color for Qnote %s: %v", qnoteID, err)
	}
	exactAmber, _ := updateResp["background_color"].(string)
	log.Printf("[monitor] Qnote color set to: %q for noteID: %s", exactAmber, qnoteID)
}

// createTimeoutHelperNote creates a helper note informing the user that the question wait timed out
func createTimeoutHelperNote(client *canvusapi.Client, qnoteID string, timeout time.Duration) {
	qWidget, err := client.GetNote(qnoteID, false)
	if err != nil {
		log.Printf("[createTimeoutHelperNote] Failed to get Qnote %s: %v", qnoteID, err)
		return
	}
	qLoc, _ := qWidget["location"].(map[string]interface{})
	qx := qLoc["x"].(float64)
	qy := qLoc["y"].(float64)
	qSize, _ := qWidget["size"].(map[string]interface{})
	qw := qSize["width"].(float64)
	qh := qSize["height"].(float64)

	helperX := qx - 1.2*qw
	helperY := qy - 0.33*qh
	noteMeta := map[string]interface{}{
		"title":            "Question Wait Timed Out",
		"text":             fmt.Sprintf("The system waited %v for a question to be entered, but none was detected.\n\nPlease enter your question (ending with ?) in the note, then create a new 'New_AI_Question' trigger note to restart the Q&A process.", timeout),
		"location":         map[string]interface{}{"x": helperX, "y": helperY},
		"size":             map[string]interface{}{"width": qw, "height": qh * 0.7},
		"background_color": TimeoutHelperColor,
	}
	helperNote, err := client.CreateNote(noteMeta)
	if err != nil {
		log.Printf("[createTimeoutHelperNote] Failed to create timeout helper note: %v", err)
		return
	}
	helperID, _ := helperNote["id"].(string)
	connMeta := BuildConnectorPayload(helperID, qnoteID)
	if _, err := client.CreateConnector(connMeta); err != nil {
		log.Printf("[warn] CreateConnector failed for timeout helper note: %v", err)
	}
	log.Printf("[createTimeoutHelperNote] Created timeout helper note %s for Qnote %s", helperID, qnoteID)
}

// WaitForQuestionText waits for a question to be entered in the note, with timeout.
// Returns true if question was detected, false if timed out.
func WaitForQuestionText(ctx context.Context, noteID string, client *canvusapi.Client) bool {
	timeout := getQuestionTimeout()
	log.Printf("[WaitForQuestionText] Starting to wait for question in note %s (timeout: %v)", noteID, timeout)

	// Create a context with timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ch := make(chan struct{})
	go func() {
		for {
			select {
			case <-timeoutCtx.Done():
				return
			default:
				qWidget, err := client.GetNote(noteID, false)
				if err != nil {
					time.Sleep(1 * time.Second)
					continue
				}
				currText, _ := qWidget["text"].(string)
				if strings.HasSuffix(strings.TrimSpace(currText), "?") {
					log.Printf("[WaitForQuestionText] Detected question in note %s: %q", noteID, currText)
					close(ch)
					return
				}
				time.Sleep(500 * time.Millisecond)
			}
		}
	}()

	select {
	case <-ch:
		return true
	case <-timeoutCtx.Done():
		log.Printf("[WaitForQuestionText] Timeout waiting for question in note %s after %v", noteID, timeout)
		return false
	}
}

// HandleAIQuestion encapsulates the Q&A workflow for a New_AI_Question trigger.
// Optimized with widget caching to minimize redundant GetWidgets calls.
func HandleAIQuestion(ctx context.Context, client *canvusapi.Client, trig canvus.WidgetEvent, chatTokenLimit int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[error] HandleAIQuestion panic recovered: %v\n%s", r, debug.Stack())
			return
		}
	}()
	log.Printf("[trigger] HandleAIQuestion called: noteID=%s", trig.ID)
	noteID := trig.ID
	if IsQnoteProcessing(noteID) {
		return
	}

	// Fetch widgets once at the start of the workflow for caching
	getWidgetsTimer := timing.Start("handle_ai_question_get_widgets_initial")
	widgets, err := client.GetWidgets(false)
	if err != nil {
		getWidgetsTimer.StopAndLog(false)
		log.Printf("[HandleAIQuestion] Failed to fetch initial widgets: %v", err)
		return
	}
	getWidgetsTimer.StopAndLog(true)
	log.Printf("[HandleAIQuestion] Fetched %d widgets for caching", len(widgets))

	if !CheckPersonasPresentWithCache(noteID, client, widgets) {
		EnsureHelperNoteForPersonasWithCache(noteID, client, widgets)
		err := CreatePersonasWithCache(ctx, noteID, client, widgets)
		if err != nil {
			// Remove the helper note if persona generation failed
			if val, ok := qnoteHelperNotes.Load(noteID); ok {
				helperID := val.(string)
				if err := client.DeleteNote(helperID); err != nil {
					log.Printf("[warn] DeleteNote failed for persona helper note %s: %v", helperID, err)
				}
				log.Printf("[helper-note] Deleted persona waiting helper note %s for Qnote %s.", helperID, noteID)
				qnoteHelperNotes.Delete(noteID)
			}
			return
		}
		// Refresh widgets after persona creation for subsequent checks
		widgets, err = client.GetWidgets(false)
		if err != nil {
			log.Printf("[HandleAIQuestion] Failed to refresh widgets after persona creation: %v", err)
			return
		}
		if !CheckPersonasPresentWithCache(noteID, client, widgets) {
			if val, ok := qnoteHelperNotes.Load(noteID); ok {
				helperID := val.(string)
				if err := client.DeleteNote(helperID); err != nil {
					log.Printf("[warn] DeleteNote failed for persona helper note %s: %v", helperID, err)
				}
				log.Printf("[helper-note] Deleted persona waiting helper note %s for Qnote %s.", helperID, noteID)
				qnoteHelperNotes.Delete(noteID)
			}
			return
		}
		// Remove the helper note after personas are created
		if val, ok := qnoteHelperNotes.Load(noteID); ok {
			helperID := val.(string)
			if err := client.DeleteNote(helperID); err != nil {
				log.Printf("[warn] DeleteNote failed for persona helper note %s: %v", helperID, err)
			}
			log.Printf("[helper-note] Deleted persona waiting helper note %s for Qnote %s.", helperID, noteID)
			qnoteHelperNotes.Delete(noteID)
		}
	}
	if !CheckQuestionPresent(noteID, client) {
		EnsureHelperNoteForQuestionWithCache(noteID, client, widgets)

		// Use the new WaitForQuestionText with timeout
		questionDetected := WaitForQuestionText(ctx, noteID, client)

		if !questionDetected {
			// Timeout occurred - create timeout helper note and cleanup
			createTimeoutHelperNote(client, noteID, getQuestionTimeout())

			// Remove the question helper note
			if val, ok := qnoteHelperNotes.Load(noteID); ok {
				helperID := val.(string)
				if err := client.DeleteNote(helperID); err != nil {
					log.Printf("[warn] DeleteNote failed for question helper note %s: %v", helperID, err)
				}
				log.Printf("[helper-note] Deleted question helper note %s for Qnote %s (timeout).", helperID, noteID)
				qnoteHelperNotes.Delete(noteID)
			}

			// Remove from processing list
			qnoteProcessingList.Delete(noteID)
			log.Printf("[HandleAIQuestion] Aborted for noteID %s due to question timeout", noteID)
			return
		}

		log.Printf("[step] Resuming HandleAIQuestion for noteID: %s after question detected", noteID)
		// Refresh widgets after waiting for question (state may have changed)
		widgets, _ = client.GetWidgets(false)
	}
	OnQuestionDetectedWithCache(noteID, client, chatTokenLimit, widgets)
	log.Printf("[step] HandleAIQuestion completed for noteID: %s", noteID)
	return
}

// HandleFollowupConnector handles creation of a follow-up answer note when a connector is created from a persona answer note to a question note.
func HandleFollowupConnector(ctx context.Context, client *canvusapi.Client, connectorEvent canvus.WidgetEvent, chatTokenLimit int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[error] HandleFollowupConnector panic: %v\n%s", r, debug.Stack())
		}
	}()
	log.Printf("[HandleFollowupConnector] called: connectorID=%s", connectorEvent.ID)
	// Extract src and dst IDs from connector data
	src, srcOK := connectorEvent.Data["src"].(map[string]interface{})
	dst, dstOK := connectorEvent.Data["dst"].(map[string]interface{})
	if !srcOK || !dstOK {
		log.Printf("[HandleFollowupConnector] src/dst missing in connector data")
		return
	}
	srcID, srcIDOK := src["id"].(string)
	dstID, dstIDOK := dst["id"].(string)
	if !srcIDOK || !dstIDOK {
		log.Printf("[HandleFollowupConnector] srcID/dstID missing or not string")
		return
	}
	// Fetch src and dst widgets (not just notes)
	srcWidget, err := client.GetWidget(srcID, false)
	if err != nil {
		log.Printf("[HandleFollowupConnector] failed to fetch src widget: %v", err)
		return
	}
	dstWidget, err := client.GetWidget(dstID, false)
	if err != nil {
		log.Printf("[HandleFollowupConnector] failed to fetch dst widget: %v", err)
		return
	}
	srcType, _ := srcWidget["widget_type"].(string)
	dstType, _ := dstWidget["widget_type"].(string)
	if srcType != "Note" {
		log.Printf("[HandleFollowupConnector] src widget is not a Note (type=%s, id=%s)", srcType, srcID)
		return
	}
	if dstType != "Note" {
		log.Printf("[HandleFollowupConnector] dst widget is not a Note (type=%s, id=%s)", dstType, dstID)
		return
	}
	// Now fetch as notes
	srcNote := srcWidget
	dstNote := dstWidget
	// Check if src is a persona answer note (title ends with ' Answer' and color matches persona colors)
	title, _ := srcNote["title"].(string)
	bg, _ := srcNote["background_color"].(string)
	personaColors := map[string]bool{"#2196f3ff": true, "#4caf50ff": true, "#ff9800ff": true, "#9c27b0ff": true}
	if !strings.HasSuffix(title, " Answer") || !personaColors[strings.ToLower(bg)] {
		log.Printf("[HandleFollowupConnector] src note is not a persona answer note (title/bg)")
		return
	}
	// Check if dst is a note with a question
	dstText, _ := dstNote["text"].(string)
	if !strings.HasSuffix(strings.TrimSpace(dstText), "?") {
		log.Printf("[HandleFollowupConnector] dst note does not contain a question")
		return
	}
	// Improved persona name extraction
	personaName := title
	personaName = strings.TrimSuffix(personaName, " Followup Answer")
	personaName = strings.TrimSuffix(personaName, " Meta Answer")
	personaName = strings.TrimSuffix(personaName, " Answer")
	personaName = strings.TrimSpace(personaName)
	// Get locations and sizes
	srcLoc, _ := srcNote["location"].(map[string]interface{})
	srcX, _ := srcLoc["x"].(float64)
	srcY, _ := srcLoc["y"].(float64)
	dstLoc, _ := dstNote["location"].(map[string]interface{})
	dstX, _ := dstLoc["x"].(float64)
	dstY, _ := dstLoc["y"].(float64)
	dstSize, _ := dstNote["size"].(map[string]interface{})
	dstW, _ := dstSize["width"].(float64)
	dstH, _ := dstSize["height"].(float64)
	scale := 1.0
	if s, ok := dstNote["scale"].(float64); ok {
		scale = s
	}
	// Compute vector from dst to src
	dx := srcX - dstX
	dy := srcY - dstY
	// Place follow-up note at same distance from dst as src is from dst
	fupX := dstX + dx
	fupY := dstY + dy
	// Use same size as dst note
	fupW := dstW
	fupH := dstH
	// Helper: If dst note is blank or not a question, create helper note
	if strings.TrimSpace(dstText) == "" || !strings.HasSuffix(strings.TrimSpace(dstText), "?") {
		helperTitle := "Helper: Please enter a question for this note"
		noteMeta := map[string]interface{}{
			"title":            helperTitle,
			"text":             "Please enter a question in the note to enable follow-up.",
			"location":         map[string]interface{}{"x": dstX - 1.2*dstW, "y": dstY - 0.33*dstH},
			"size":             map[string]interface{}{"width": dstW, "height": dstH * 0.7},
			"background_color": "#e0e0e0",
		}
		if _, err := client.CreateNote(noteMeta); err != nil {
			log.Printf("[warn] CreateNote failed for followup helper: %v", err)
		}
		return
	}
	// Generate follow-up answer using the persona
	personas := []Persona{}
	geminiClient, err := NewClient(ctx)
	if err != nil {
		log.Printf("[HandleFollowupConnector] failed to create Gemini client: %v", err)
		return
	}
	err = CreatePersonas(ctx, dstID, client)
	if err != nil {
		log.Printf("[HandleFollowupConnector] CreatePersonas failed: %v", err)
		return
	}
	// Find the persona by name
	var persona Persona
	found := false
	for _, p := range personas {
		if p.Name == personaName {
			persona = p
			found = true
			break
		}
	}
	if !found {
		log.Printf("[HandleFollowupConnector] persona not found: %s", personaName)
		return
	}
	// Get business context for followup
	businessContextStr, _, err := getBusinessContext(ctx, dstID, client)
	if err != nil {
		log.Printf("[HandleFollowupConnector] Failed to get business context: %v", err)
		return // Or handle this error appropriately
	}

	sessionManager := NewSessionManager(geminiClient.GenaiClient())
	answer, _ := geminiClient.AnswerQuestion(ctx, persona, dstText, sessionManager, businessContextStr)
	if len(answer) > chatTokenLimit {
		succinctPrompt := "Please rephrase your answer in a much more succinct, short, and verbal way. Limit your response to " + fmt.Sprintf("%d", chatTokenLimit) + " characters."
		answer, _ = geminiClient.AnswerQuestion(ctx, persona, succinctPrompt, sessionManager, businessContextStr)
	}
	// Create follow-up answer note
	fupMeta := map[string]interface{}{
		"title":            persona.Name + " Followup Answer",
		"text":             answer,
		"location":         map[string]interface{}{"x": fupX, "y": fupY},
		"size":             map[string]interface{}{"width": fupW, "height": fupH},
		"background_color": bg,
		"scale":            scale,
	}
	fupNote, err := client.CreateNote(fupMeta)
	if err != nil {
		log.Printf("[HandleFollowupConnector] failed to create follow-up note: %v", err)
		return
	}
	fupNoteID, _ := fupNote["id"].(string)
	if fupNoteID == "" {
		log.Printf("[HandleFollowupConnector] follow-up note ID missing")
		return
	}
	// Create connector from dst to follow-up note, copying settings from original connector
	connMeta := connectorEvent.Data
	connMetaCpy := make(map[string]interface{})
	for k, v := range connMeta {
		connMetaCpy[k] = v
	}
	// Update src/dst for new connector
	connMetaCpy["src"] = map[string]interface{}{"id": dstID, "auto_location": true, "tip": "none"}
	connMetaCpy["dst"] = map[string]interface{}{"id": fupNoteID, "auto_location": true, "tip": "solid-equilateral-triangle"}
	connMetaCpy["widget_type"] = "Connector"
	if _, err := client.CreateConnector(connMetaCpy); err != nil {
		log.Printf("[warn] CreateConnector failed for follow-up: %v", err)
	}
	log.Printf("[HandleFollowupConnector] Follow-up answer note and connector created for persona %s", persona.Name)
}
