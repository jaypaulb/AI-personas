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
	"github.com/jaypaulb/AI-personas/internal/atom"
	"github.com/jaypaulb/AI-personas/internal/types"
)

// SSE reconnection constants
const (
	initialBackoff = 1 * time.Second
	maxBackoff     = 30 * time.Second
	maxRetries     = 10
)

// Type aliases for backward compatibility within this package
type WidgetEvent = types.WidgetEvent
type TriggerType = types.TriggerType
type EventTrigger = types.EventTrigger

// Re-export trigger constants for backward compatibility
const (
	TriggerNone                  = types.TriggerNone
	TriggerBACCompleteImage      = types.TriggerBACCompleteImage
	TriggerNewAIQuestion         = types.TriggerNewAIQuestion
	TriggerCreatePersonasNote    = types.TriggerCreatePersonasNote
	TriggerQnoteQuestionDetected = types.TriggerQnoteQuestionDetected
	TriggerConnectorCreated      = types.TriggerConnectorCreated
)

// QuestionHandlerEntry holds a handler and expected color for Qnote detection
type QuestionHandlerEntry struct {
	Color   string
	Handler func(WidgetEvent)
}

// EventMonitorConfig holds configuration for the EventMonitor
type EventMonitorConfig struct {
	DebugMode        bool
	DebounceDuration time.Duration
}

// DefaultEventMonitorConfig returns the default configuration
func DefaultEventMonitorConfig() EventMonitorConfig {
	debugMode := os.Getenv("DEBUG") == "1"
	return EventMonitorConfig{
		DebugMode:        debugMode,
		DebounceDuration: 1 * time.Second,
	}
}

// EventMonitor handles widget event subscription and trigger detection
type EventMonitor struct {
	Client *canvusapi.Client
	Config EventMonitorConfig

	// State - owned by this organism
	questionHandlers sync.Map // noteID -> QuestionHandlerEntry
	debounceTimers   sync.Map // noteID -> *time.Timer
	latestEvents     sync.Map // noteID -> WidgetEvent
	mu               sync.Mutex
}

// NewEventMonitor creates a new EventMonitor with default configuration
func NewEventMonitor(client *canvusapi.Client) *EventMonitor {
	return NewEventMonitorWithConfig(client, DefaultEventMonitorConfig())
}

// NewEventMonitorWithConfig creates a new EventMonitor with custom configuration
func NewEventMonitorWithConfig(client *canvusapi.Client, config EventMonitorConfig) *EventMonitor {
	return &EventMonitor{
		Client: client,
		Config: config,
	}
}

// RegisterQuestionHandler registers a callback and expected color for a Qnote
func (em *EventMonitor) RegisterQuestionHandler(noteID string, color string, handler func(WidgetEvent)) {
	em.questionHandlers.Store(noteID, QuestionHandlerEntry{
		Color:   color,
		Handler: handler,
	})
}

// UnregisterQuestionHandler removes a registered handler
func (em *EventMonitor) UnregisterQuestionHandler(noteID string) {
	em.questionHandlers.Delete(noteID)
	// Also clean up any associated timers
	if timerRaw, ok := em.debounceTimers.Load(noteID); ok {
		if timer, ok := timerRaw.(*time.Timer); ok && timer != nil {
			timer.Stop()
		}
		em.debounceTimers.Delete(noteID)
	}
	em.latestEvents.Delete(noteID)
}

// IsQuestion checks if text appears to be a question
func IsQuestion(text string) bool {
	return atom.IsQuestion(text)
}

// SubscribeAndDetectTriggers subscribes to widget events and sends triggers to the channel
// Implements reconnection with exponential backoff on connection failures
func (em *EventMonitor) SubscribeAndDetectTriggers(ctx context.Context, triggers chan<- EventTrigger) {
	backoff := initialBackoff
	retryCount := 0

	for {
		select {
		case <-ctx.Done():
			log.Println("[events] Context cancelled, stopping event monitor.")
			return
		default:
		}

		stream, err := em.Client.SubscribeToWidgets(ctx)
		if err != nil {
			retryCount++
			if retryCount > maxRetries {
				log.Printf("[events] Failed to subscribe to widgets after %d attempts, giving up: %v", maxRetries, err)
				return
			}
			log.Printf("[events] Failed to subscribe to widgets (attempt %d/%d): %v. Retrying in %v...", retryCount, maxRetries, err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			// Exponential backoff
			backoff = backoff * 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		// Reset backoff and retry count on successful connection
		log.Printf("[events] Successfully connected to widget stream")
		backoff = initialBackoff
		retryCount = 0

		// Process the stream
		disconnected := em.processStream(ctx, stream, triggers)
		stream.Close()

		if !disconnected {
			// Clean exit requested by context
			return
		}

		// Stream disconnected, attempt reconnection
		log.Printf("[events] Stream disconnected, attempting to reconnect in %v...", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// processStream reads events from the stream and returns true if disconnected (should reconnect)
func (em *EventMonitor) processStream(ctx context.Context, stream io.ReadCloser, triggers chan<- EventTrigger) bool {
	r := bufio.NewReader(stream)
	for {
		select {
		case <-ctx.Done():
			log.Println("[events] Event monitor stopped.")
			return false
		default:
			line, err := r.ReadBytes('\n')
			if err != nil {
				if err == io.EOF {
					// EOF on SSE stream means server closed connection
					log.Printf("[events] Stream EOF received, will attempt reconnection")
					return true
				}
				// Other errors also trigger reconnection
				log.Printf("[events] Error reading widget event stream: %v", err)
				return true
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
				em.processWidgetEvent(raw, triggers)
			}
		}
	}
}

// processWidgetEvent processes a single widget event and emits triggers as needed
func (em *EventMonitor) processWidgetEvent(raw map[string]interface{}, triggers chan<- EventTrigger) {
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
		return
	}

	// Detect New_AI_Question note creation
	if widType == "Note" && strings.EqualFold(title, "New_AI_Question") {
		bg, _ := raw["background_color"].(string)
		bgLower := strings.ToLower(strings.TrimSpace(bg))
		if bgLower == "#ffffffff" || bgLower == "#ffffff" {
			triggers <- EventTrigger{Type: TriggerNewAIQuestion, Widget: widget}
		}
		return
	}

	// Detect Create_Personas note
	if widType == "Note" && strings.TrimSpace(title) == "Create_Personas" {
		triggers <- EventTrigger{Type: TriggerCreatePersonasNote, Widget: widget}
		return
	}

	// Detect Connector creation
	if widType == "Connector" {
		triggers <- EventTrigger{Type: TriggerConnectorCreated, Widget: widget}
		return
	}

	// Handle Qnote question detection with debouncing
	em.handleQnoteQuestionDetection(widget, raw, triggers)
}

// handleQnoteQuestionDetection handles the debounced question detection for Qnotes
func (em *EventMonitor) handleQnoteQuestionDetection(widget WidgetEvent, raw map[string]interface{}, triggers chan<- EventTrigger) {
	if widget.Type != "Note" || widget.ID == "" || !strings.EqualFold(widget.Title, "New_AI_Question") {
		return
	}

	bg, _ := raw["background_color"].(string)
	bgLower := strings.ToLower(strings.TrimSpace(bg))

	handlerRaw, ok := em.questionHandlers.Load(widget.ID)
	if !ok {
		return
	}

	entry := handlerRaw.(QuestionHandlerEntry)
	expectedColor := strings.ToLower(strings.TrimSpace(entry.Color))
	if bgLower != expectedColor {
		return
	}

	// Debounce logic: store latest event and reset timer
	em.latestEvents.Store(widget.ID, widget)

	if timerRaw, loaded := em.debounceTimers.LoadOrStore(widget.ID, nil); loaded && timerRaw != nil {
		timerRaw.(*time.Timer).Stop()
	}

	timer := time.AfterFunc(em.Config.DebounceDuration, func() {
		// On debounce expiry, check if latest event is a question
		val, ok := em.latestEvents.Load(widget.ID)
		if !ok {
			return
		}
		latestWidget := val.(WidgetEvent)
		if IsQuestion(latestWidget.Text) {
			triggers <- EventTrigger{Type: TriggerQnoteQuestionDetected, Widget: latestWidget}
			entry.Handler(latestWidget)
		}
	})
	em.debounceTimers.Store(widget.ID, timer)
}

// --- Backward compatibility functions ---
// These maintain compatibility with code using the old global function API

var globalEventMonitor *EventMonitor
var globalEventMonitorOnce sync.Once

func getGlobalEventMonitor() *EventMonitor {
	globalEventMonitorOnce.Do(func() {
		// This will be replaced when proper dependency injection is set up
		globalEventMonitor = &EventMonitor{
			Config: DefaultEventMonitorConfig(),
		}
	})
	return globalEventMonitor
}

// RegisterQnoteQuestionHandlerWithColor is a backward-compatible global function
// Deprecated: Use EventMonitor.RegisterQuestionHandler instead
func RegisterQnoteQuestionHandlerWithColor(noteID string, color string, handler func(WidgetEvent)) {
	getGlobalEventMonitor().RegisterQuestionHandler(noteID, color, handler)
}
