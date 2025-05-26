package canvus

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jaypaulb/AI-personas/canvusapi"
)

var debugMode = false

func init() {
	if v := os.Getenv("DEBUG"); v == "1" {
		debugMode = true
	}
}

// WidgetEvent represents a widget event from the Canvus API
// (expand as needed for event details)
type WidgetEvent struct {
	ID    string
	Type  string
	Title string
	Text  string
	Data  map[string]interface{}
}

type TriggerType int

const (
	TriggerNone TriggerType = iota
	TriggerBACCompleteImage
	TriggerNewAIQuestion
	TriggerCreatePersonasNote
	TriggerQnoteQuestionDetected
)

// EventTrigger represents a detected trigger event
// (expand as needed)
type EventTrigger struct {
	Type   TriggerType
	Widget WidgetEvent
}

// EventMonitor handles widget event subscription and trigger detection
type EventMonitor struct {
	Client *canvusapi.Client
}

// NewEventMonitor creates a new EventMonitor
func NewEventMonitor(client *canvusapi.Client) *EventMonitor {
	return &EventMonitor{Client: client}
}

// Handler registry for Qnote question detection
var qnoteQuestionHandlers sync.Map // noteID -> struct{color string; handler func(WidgetEvent)}

// RegisterQnoteQuestionHandlerWithColor allows registration of a callback and expected color for a Qnote
func RegisterQnoteQuestionHandlerWithColor(noteID string, color string, handler func(WidgetEvent)) {
	qnoteQuestionHandlers.Store(noteID, struct {
		color   string
		handler func(WidgetEvent)
	}{color, handler})
}

// Helper to check if a string is a question (imported from gemini/aiquestion.go)
func IsQuestion(text string) bool {
	questionWords := []string{"what", "why", "how", "when", "where", "who", "which", "is", "are", "do", "does", "can", "could", "would", "should"}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "?") {
		return true
	}
	for _, w := range questionWords {
		if strings.HasPrefix(lower, w+" ") || strings.Contains(lower, w+" ") {
			return true
		}
	}
	return false
}

// Debounce state for Qnote question detection
var (
	qnoteDebounceTimers   sync.Map // noteID -> *time.Timer
	qnoteLatestEvents     sync.Map // noteID -> WidgetEvent
	qnoteDebounceDuration = 1 * time.Second
)

// SubscribeAndDetectTriggers subscribes to widget events and sends triggers to the channel
func (em *EventMonitor) SubscribeAndDetectTriggers(ctx context.Context, triggers chan<- EventTrigger) {
	stream, err := em.Client.SubscribeToWidgets(ctx)
	if err != nil {
		log.Printf("Failed to subscribe to widgets: %v", err)
		return
	}
	defer stream.Close()

	r := bufio.NewReader(stream)
	for {
		select {
		case <-ctx.Done():
			log.Println("Event monitor stopped.")
			return
		default:
			line, err := r.ReadBytes('\n')
			if err != nil {
				if err == io.EOF {
					time.Sleep(1 * time.Second)
					continue
				}
				log.Printf("Error reading widget event stream: %v", err)
				return
			}
			trimmed := strings.TrimSpace(string(line))
			if trimmed == "" || trimmed == "\r" {
				continue // skip keep-alive or empty lines
			}
			var events []map[string]interface{}
			if err := json.Unmarshal(line, &events); err != nil {
				log.Printf("[event] Skipping malformed line: %s", string(line))
				continue // skip malformed lines
			}
			for _, raw := range events {
				widType, _ := raw["widget_type"].(string)
				id, _ := raw["id"].(string)
				title, _ := raw["title"].(string)
				text, _ := raw["text"].(string)

				widget := WidgetEvent{
					ID:    id,
					Type:  widType,
					Title: title,
					Text:  text,
					Data:  raw,
				}

				// Flexible BAC_Complete image trigger (case-insensitive, ignores .png)
				imageTitle := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(title), ".png"))
				if widType == "Image" && imageTitle == "bac_complete" {
					triggers <- EventTrigger{Type: TriggerBACCompleteImage, Widget: widget}
					continue
				}

				// Detect New_AI_Question note creation
				if widType == "Note" && strings.EqualFold(title, "New_AI_Question") {
					bg, _ := raw["background_color"].(string)
					bgLower := strings.ToLower(strings.TrimSpace(bg))
					if bgLower == "#ffffffff" || bgLower == "#ffffff" {
						triggers <- EventTrigger{Type: TriggerNewAIQuestion, Widget: widget}
					}
					continue
				}

				// Detect Create_Personas note
				if widType == "Note" && strings.TrimSpace(title) == "Create_Personas" {
					triggers <- EventTrigger{Type: TriggerCreatePersonasNote, Widget: widget}
					continue
				}

				// Only process and log Qnote events for New_AI_Question notes
				if widType == "Note" && id != "" && strings.EqualFold(title, "New_AI_Question") {
					bg, _ := raw["background_color"].(string)
					bgLower := strings.ToLower(strings.TrimSpace(bg))
					if handlerRaw, ok := qnoteQuestionHandlers.Load(id); ok {
						entry := handlerRaw.(struct {
							color   string
							handler func(WidgetEvent)
						})
						expectedColor := strings.ToLower(strings.TrimSpace(entry.color))
						colorMatch := bgLower == expectedColor
						if colorMatch {
							// Debounce logic: store latest event and reset timer
							qnoteLatestEvents.Store(id, widget)
							if timerRaw, loaded := qnoteDebounceTimers.LoadOrStore(id, nil); loaded && timerRaw != nil {
								timerRaw.(*time.Timer).Stop()
							}
							timer := time.AfterFunc(qnoteDebounceDuration, func() {
								// On debounce expiry, check if latest event is a question
								val, ok := qnoteLatestEvents.Load(id)
								if !ok {
									return
								}
								latestWidget := val.(WidgetEvent)
								isQ := IsQuestion(latestWidget.Text)
								if isQ {
									triggers <- EventTrigger{Type: TriggerQnoteQuestionDetected, Widget: latestWidget}
									entry.handler(latestWidget)
								}
							})
							qnoteDebounceTimers.Store(id, timer)
						}
					}
				}
			}
		}
	}
}
