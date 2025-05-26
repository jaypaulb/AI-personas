package gemini

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jaypaulb/AI-personas/canvusapi"
	"github.com/jaypaulb/AI-personas/internal/canvus"
)

var answeredNotes sync.Map // noteID -> true

// Processing list for Qnotes (thread-safe)
var qnoteProcessingList sync.Map // qnoteID -> true

// Wait channels for Qnotes (thread-safe)
var qnoteWaitChans sync.Map // noteID -> chan struct{}

// TODO: Update logging functions to be less verbose and more configurable for production use.

// IsQnoteProcessing checks if the Qnote is already being processed.
func IsQnoteProcessing(qnoteID string) bool {
	log.Printf("[step] IsQnoteProcessing called for noteID: %s", qnoteID)
	if _, already := qnoteProcessingList.LoadOrStore(qnoteID, true); already {
		log.Printf("[qnote-processing] Skipping note %s: already being processed.", qnoteID)
		return true
	}
	log.Printf("[qnote-processing] Added note %s to processing list.", qnoteID)
	return false
}

// CheckPersonasPresent checks for the presence of all 4 persona notes for the Qnote.
func CheckPersonasPresent(qnoteID string, client *canvusapi.Client) bool {
	log.Printf("[step] CheckPersonasPresent called for noteID: %s", qnoteID)
	widgets, err := client.GetWidgets(false)
	if err != nil {
		log.Printf("[personas-check] Failed to fetch widgets: %v", err)
		return false
	}
	personaCount := 0
	for _, w := range widgets {
		typeStr, _ := w["widget_type"].(string)
		title, _ := w["title"].(string)
		if typeStr == "Note" && strings.HasPrefix(strings.TrimSpace(title), "Persona ") {
			personaCount++
		}
	}
	if personaCount >= 4 {
		log.Printf("[personas-check] All 4 persona notes present for Qnote %s.", qnoteID)
		return true
	}
	log.Printf("[personas-check] Missing persona notes for Qnote %s. Found %d/4.", qnoteID, personaCount)
	return false
}

// CheckQuestionPresent checks if the Qnote contains a question.
func CheckQuestionPresent(qnoteID string, client *canvusapi.Client) bool {
	log.Printf("[step] CheckQuestionPresent called for noteID: %s", qnoteID)
	qWidget, err := client.GetNote(qnoteID, false)
	if err != nil {
		log.Printf("[question-check] Failed to fetch Qnote %s: %v", qnoteID, err)
		return false
	}
	currText, _ := qWidget["text"].(string)
	trimmedText := strings.TrimSpace(currText)
	log.Printf("[question-check] Qnote %s text: %q", qnoteID, trimmedText)
	if strings.HasSuffix(trimmedText, "?") {
		log.Printf("[question-check] Qnote %s contains a question.", qnoteID)
		return true
	}
	log.Printf("[question-check] Qnote %s does not contain a question.", qnoteID)
	return false
}

// Helper to build a Canvus connector payload
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

// EnsureHelperNoteForQuestion always creates or updates the helper note and connector, sets Qnote to amber, then calls MonitorQuestionNote
func EnsureHelperNoteForQuestion(qnoteID string, client *canvusapi.Client) {
	log.Printf("[step] EnsureHelperNoteForQuestion called for noteID: %s", qnoteID)
	qWidget, err := client.GetNote(qnoteID, false)
	if err != nil {
		log.Printf("[helper-note] Failed to fetch Qnote %s: %v", qnoteID, err)
		return
	}
	qLoc, _ := qWidget["location"].(map[string]interface{})
	qx := qLoc["x"].(float64)
	qy := qLoc["y"].(float64)
	qSize, _ := qWidget["size"].(map[string]interface{})
	qw := qSize["width"].(float64)
	qh := qSize["height"].(float64)
	helperTitle := "Helper: Please enter a question for this note"
	widgets, err := client.GetWidgets(false)
	if err != nil {
		log.Printf("[helper-note] Failed to fetch widgets: %v", err)
		return
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
			log.Printf("[helper-note] Failed to create helper note: %v", err)
			return
		}
		helperID, _ = helperNote["id"].(string)
		connMeta := BuildConnectorPayload(helperID, qnoteID)
		_, _ = client.CreateConnector(connMeta)
		log.Printf("[helper-note] Created helper note and connector for Qnote %s.", qnoteID)
	} else {
		log.Printf("[helper-note] Helper note already present for Qnote %s.", qnoteID)
	}
	// Always update Qnote to amber and capture the exact color value returned
	updateResp, _ := client.UpdateNote(qnoteID, map[string]interface{}{"background_color": "#ffe4b3"})
	exactAmber, _ := updateResp["background_color"].(string)
	log.Printf("[monitor] Qnote color set to: %q for noteID: %s", exactAmber, qnoteID)
}

// OnQuestionDetected updates helper note and Qnote when a question is detected, then calls AnswerQuestion.
func OnQuestionDetected(qnoteID string, client *canvusapi.Client, chatTokenLimit int) {
	log.Printf("[step] OnQuestionDetected called for noteID: %s", qnoteID)
	// Update helper note to 'Processing Question'
	helperTitle := "Helper: Please enter a question for this note"
	widgets, err := client.GetWidgets(false)
	if err == nil {
		for _, w := range widgets {
			typeStr, _ := w["widget_type"].(string)
			title, _ := w["title"].(string)
			if typeStr == "Note" && title == helperTitle {
				noteID2, _ := w["id"].(string)
				update := map[string]interface{}{
					"text": "Processing Question...",
				}
				_, _ = client.UpdateNote(noteID2, update)
			}
		}
	}
	// Set Qnote to amber
	updateQ := map[string]interface{}{
		"background_color": "#ffe4b3",
	}
	_, _ = client.UpdateNote(qnoteID, updateQ)
	// Call AnswerQuestion
	AnswerQuestion(qnoteID, client, chatTokenLimit)
}

// AnswerQuestion handles persona answers, meta-answers, note creation, and connectors.
func AnswerQuestion(qnoteID string, client *canvusapi.Client, chatTokenLimit int) {
	log.Printf("[step] AnswerQuestion called for noteID: %s", qnoteID)
	ctx := context.Background()
	defer func() {
		qnoteProcessingList.Delete(qnoteID)
	}()
	// Fetch Qnote and personas
	qWidget, _ := client.GetNote(qnoteID, false)
	currText, _ := qWidget["text"].(string)
	personas := []Persona{}
	geminiClient, err := NewClient(ctx)
	if err != nil {
		log.Printf("[error] NewClient failed: %v", err)
		return
	}
	personas, err = geminiClient.GeneratePersonas(ctx, "Q&A context")
	if err != nil {
		log.Printf("[error] GeneratePersonas failed: %v", err)
		return
	}
	colors := []string{"#2196f3ff", "#4caf50ff", "#ff9800ff", "#9c27b0ff"}
	qLoc, _ := qWidget["location"].(map[string]interface{})
	qSize, _ := qWidget["size"].(map[string]interface{})
	qx := qLoc["x"].(float64)
	qy := qLoc["y"].(float64)
	qw := qSize["width"].(float64)
	qh := qSize["height"].(float64)
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
	spacing := qw / 5.0
	// Layout: center (Q), top (A1), right (A2), bottom (A3), left (A4), then diagonals for meta
	answerPositions := [][2]int{{0, -1}, {1, 0}, {0, 1}, {-1, 0}} // top, right, bottom, left
	metaPositions := [][2]int{{1, -1}, {1, 1}, {-1, 1}, {-1, -1}} // top-right, bottom-right, bottom-left, top-left
	answerNoteIDs := make([]string, 4)
	metaNoteIDs := make([]string, 4)
	// 1. Generate persona answers in parallel
	var ansWg sync.WaitGroup
	ansWg.Add(4)
	answers := make([]string, 4)
	for i, p := range personas {
		go func(i int, p Persona) {
			defer ansWg.Done()
			answer, _ := geminiClient.AnswerQuestion(ctx, p, question, sessionManager, "")
			if len(answer) > chatTokenLimit {
				succinctPrompt := "Please rephrase your answer in a much more succinct, short, and verbal way. Limit your response to " + fmt.Sprintf("%d", chatTokenLimit) + " characters."
				answer, _ = geminiClient.AnswerQuestion(ctx, p, succinctPrompt, sessionManager, "")
			}
			answers[i] = answer
		}(i, p)
	}
	ansWg.Wait()
	// 2. Create answer notes (sequential, after all answers are ready)
	for i, p := range personas {
		pos := answerPositions[i]
		ansX := qx + float64(pos[0])*(qw+spacing)
		ansY := qy + float64(pos[1])*(qh+spacing)
		noteMeta := map[string]interface{}{
			"title":            p.Name + " Answer",
			"text":             answers[i],
			"location":         map[string]interface{}{"x": ansX, "y": ansY},
			"size":             map[string]interface{}{"width": qw, "height": qh},
			"background_color": colors[i%len(colors)],
			"scale":            scale,
		}
		ansNote, err := client.CreateNote(noteMeta)
		log.Printf("[debug] Created answer note for persona %s: %+v", p.Name, ansNote)
		if err != nil {
			log.Printf("[error] Failed to create answer note for persona %s: %v", p.Name, err)
			answerNoteIDs[i] = ""
			continue
		}
		ansNoteID, ok := ansNote["id"].(string)
		if !ok || ansNoteID == "" {
			log.Printf("[error] Answer note for persona %s missing ID", p.Name)
			answerNoteIDs[i] = ""
			continue
		}
		answerNoteIDs[i] = ansNoteID
	}
	// 3. Generate meta-answers in parallel
	var metaWg sync.WaitGroup
	metaWg.Add(4)
	metaAnswers := make([]string, 4)
	for i, p := range personas {
		go func(i int, p Persona) {
			defer metaWg.Done()
			others := []string{}
			for j, ans := range answers {
				if i != j {
					others = append(others, fmt.Sprintf("%s said: %s", personas[j].Name, ans))
				}
			}
			metaPrompt := fmt.Sprintf("Thank you %s for the interesting answer. Does what you heard from the others change what you think in any way? You heard: %s", p.Name, strings.Join(others, "; "))
			metaAnswer, _ := geminiClient.AnswerQuestion(ctx, p, metaPrompt, sessionManager, "")
			if len(metaAnswer) > chatTokenLimit {
				succinctPrompt := "Please rephrase your answer in a much more succinct, short, and verbal way. Limit your response to " + fmt.Sprintf("%d", chatTokenLimit) + " characters."
				metaAnswer, _ = geminiClient.AnswerQuestion(ctx, p, succinctPrompt, sessionManager, "")
			}
			metaAnswers[i] = metaAnswer
		}(i, p)
	}
	metaWg.Wait()
	// 4. Create meta answer notes (sequential, after all meta-answers are ready)
	for i, p := range personas {
		metaPos := metaPositions[i]
		metaX := qx + float64(metaPos[0])*(qw+spacing)
		metaY := qy + float64(metaPos[1])*(qh+spacing)
		metaMeta := map[string]interface{}{
			"title":            p.Name + " Meta Answer",
			"text":             metaAnswers[i],
			"location":         map[string]interface{}{"x": metaX, "y": metaY},
			"size":             map[string]interface{}{"width": qw, "height": qh},
			"background_color": colors[i%len(colors)],
			"scale":            scale,
		}
		metaNote, err := client.CreateNote(metaMeta)
		log.Printf("[debug] Created meta note for persona %s: %+v", p.Name, metaNote)
		if err != nil {
			log.Printf("[error] Failed to create meta note for persona %s: %v", p.Name, err)
			metaNoteIDs[i] = ""
			continue
		}
		metaNoteID, ok := metaNote["id"].(string)
		if !ok || metaNoteID == "" {
			log.Printf("[error] Meta note for persona %s missing ID", p.Name)
			metaNoteIDs[i] = ""
			continue
		}
		metaNoteIDs[i] = metaNoteID
	}
	// 5. Create connectors: question -> answer, answer -> meta answer (matching layout)
	for i := 0; i < 4; i++ {
		if answerNoteIDs[i] == "" {
			log.Printf("[error] Skipping connector creation for Answer %d: missing answer note ID", i+1)
			continue
		}
		connMeta1 := BuildConnectorPayload(qnoteID, answerNoteIDs[i])
		log.Printf("[debug] Creating connector payload: %+v", connMeta1)
		if _, err := client.CreateConnector(connMeta1); err != nil {
			log.Printf("[error] Failed to create connector Qnote->Answer: %v", err)
		} else {
			log.Printf("[connector] Created connector from Qnote %s to Answer %s", qnoteID, answerNoteIDs[i])
		}
		if metaNoteIDs[i] == "" {
			log.Printf("[error] Skipping connector creation for Meta %d: missing meta note ID", i+1)
			continue
		}
		connMeta2 := BuildConnectorPayload(answerNoteIDs[i], metaNoteIDs[i])
		log.Printf("[debug] Creating connector payload: %+v", connMeta2)
		if _, err := client.CreateConnector(connMeta2); err != nil {
			log.Printf("[error] Failed to create connector Answer->Meta: %v", err)
		} else {
			log.Printf("[connector] Created connector from Answer %s to Meta %s", answerNoteIDs[i], metaNoteIDs[i])
		}
	}
	// After all, set question note color to pastel green and restore only the original question
	origQ := currText
	if idx := strings.Index(origQ, "-->"); idx != -1 {
		origQ = origQ[idx+3:]
	}
	origQ = strings.TrimSpace(strings.Split(origQ, "Please wait")[0])
	client.UpdateNote(qnoteID, map[string]interface{}{"background_color": "#ccffcc", "text": origQ})
	answeredNotes.Store(qnoteID, true)
	// Delete the helper note associated with this Qnote
	helperTitle := "Helper: Please enter a question for this note"
	widgets, err := client.GetWidgets(false)
	if err == nil {
		for _, w := range widgets {
			typeStr, _ := w["widget_type"].(string)
			title, _ := w["title"].(string)
			if typeStr == "Note" && title == helperTitle {
				noteID2, _ := w["id"].(string)
				_ = client.DeleteNote(noteID2)
				log.Printf("[helper-note] Deleted helper note %s for Qnote %s.", noteID2, qnoteID)
			}
		}
	}
	log.Printf("[step] AnswerQuestion completed for noteID: %s", qnoteID)
}

// CleanupAfterAnswer deletes helper notes, stops monitors, and removes from processing list.
func CleanupAfterAnswer(qnoteID string, client *canvusapi.Client) {
	log.Printf("[step] CleanupAfterAnswer called for noteID: %s", qnoteID)
	// Remove from processing list only; do not reset color
	qnoteProcessingList.Delete(qnoteID)
}

// HandleAIQuestion encapsulates the Q&A workflow for a New_AI_Question trigger.
func HandleAIQuestion(ctx context.Context, client *canvusapi.Client, trig canvus.WidgetEvent, chatTokenLimit int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[panic] HandleAIQuestion panic: %v", r)
		}
	}()
	log.Printf("[trigger] HandleAIQuestion called: noteID=%s, trigger=%+v, chatTokenLimit=%d", trig.ID, trig, chatTokenLimit)
	noteID := trig.ID
	if IsQnoteProcessing(noteID) {
		log.Printf("[step] Exiting HandleAIQuestion after IsQnoteProcessing for noteID: %s", noteID)
		return
	}
	if !CheckPersonasPresent(noteID, client) {
		log.Printf("[step] Personas missing, attempting to create personas for noteID: %s", noteID)
		err := CreatePersonas(ctx, client)
		if err != nil {
			log.Printf("[error] CreatePersonas failed: %v", err)
			return
		}
		// Re-check after creation
		if !CheckPersonasPresent(noteID, client) {
			log.Printf("[error] Personas still missing after CreatePersonas for noteID: %s", noteID)
			return
		}
	}
	if !CheckQuestionPresent(noteID, client) {
		log.Printf("[step] Pausing HandleAIQuestion after CheckQuestionPresent for noteID: %s, waiting for question event", noteID)
		EnsureHelperNoteForQuestion(noteID, client)
		ch := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			log.Printf("[monitor] Starting per-Qnote event subscription for noteID: %s", noteID)
			for {
				select {
				case <-ctx.Done():
					return
				default:
					qWidget, err := client.GetNote(noteID, false)
					if err != nil {
						log.Printf("[monitor] Failed to fetch Qnote %s: %v", noteID, err)
						time.Sleep(1 * time.Second)
						continue
					}
					currText, _ := qWidget["text"].(string)
					if strings.HasSuffix(strings.TrimSpace(currText), "?") {
						log.Printf("[monitor] Detected question in Qnote %s: %q", noteID, currText)
						close(ch)
						return
					}
					time.Sleep(500 * time.Millisecond)
				}
			}
		}()
		<-ch // Block until signaled by per-Qnote monitor
		log.Printf("[step] Resuming HandleAIQuestion for noteID: %s after question detected", noteID)
	}
	OnQuestionDetected(noteID, client, chatTokenLimit)
	log.Printf("[step] HandleAIQuestion completed for noteID: %s", noteID)
	return
}
