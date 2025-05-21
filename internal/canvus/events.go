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
			var raw map[string]interface{}
			if err := json.Unmarshal(line, &raw); err != nil {
				continue // skip malformed lines
			}
			widType, _ := raw["type"].(string)
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

			// Detect BAC_Complete.png image creation
			if widType == "image" {
				filename, _ := raw["filename"].(string)
				if strings.EqualFold(filename, "BAC_Complete.png") {
					triggers <- EventTrigger{Type: TriggerBACCompleteImage, Widget: widget}
					continue
				}
			}

			// Detect New_AI_Question note creation
			if widType == "note" && strings.EqualFold(title, "New_AI_Question") {
				triggers <- EventTrigger{Type: TriggerNewAIQuestion, Widget: widget}
				continue
			}
		}
	}
}
