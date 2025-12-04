package types

// TriggerType represents the type of event trigger detected
type TriggerType int

const (
	TriggerNone TriggerType = iota
	TriggerBACCompleteImage
	TriggerNewAIQuestion
	TriggerCreatePersonasNote
	TriggerQnoteQuestionDetected
	TriggerConnectorCreated
)

// WidgetEvent represents a widget event from the Canvus API
type WidgetEvent struct {
	ID    string
	Type  string
	Title string
	Text  string
	Data  map[string]interface{}
}

// EventTrigger represents a detected trigger event
type EventTrigger struct {
	Type   TriggerType
	Widget WidgetEvent
}
