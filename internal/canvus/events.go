package canvus

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"strings"
	"time"

	"github.com/jaypaulb/AI-personas/canvusapi"
)

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
			log.Printf("[event] %s", string(line)) // Log the raw event line
			for _, raw := range events {
				widType, _ := raw["widget_type"].(string)
				id, _ := raw["id"].(string)
				title, _ := raw["title"].(string)
				text, _ := raw["text"].(string)

				log.Printf("[debug] Checking event: widget_type=%q, title=%q", widType, title)

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
					jsonRaw, _ := json.MarshalIndent(raw, "", "  ")
					log.Printf("[trigger] BAC_Complete image detected:\n%s\n", string(jsonRaw))
					triggers <- EventTrigger{Type: TriggerBACCompleteImage, Widget: widget}
					continue
				}

				// Detect New_AI_Question note creation
				if widType == "Note" && strings.EqualFold(title, "New_AI_Question") {
					jsonRaw, _ := json.MarshalIndent(raw, "", "  ")
					log.Printf("[trigger] New_AI_Question detected:\n%s\n", string(jsonRaw))
					triggers <- EventTrigger{Type: TriggerNewAIQuestion, Widget: widget}
					continue
				}
			}
		}
	}
}
