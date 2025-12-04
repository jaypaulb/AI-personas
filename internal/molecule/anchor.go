package molecule

// BoundingBox represents a rectangular bounding box
type BoundingBox struct {
	MinX, MinY, MaxX, MaxY float64
}

// Width returns the width of the bounding box
func (bb BoundingBox) Width() float64 {
	return bb.MaxX - bb.MinX
}

// Height returns the height of the bounding box
func (bb BoundingBox) Height() float64 {
	return bb.MaxY - bb.MinY
}

// CalculateBoundingBox calculates a bounding box that encompasses all given widgets
// widgets should be a slice of widget maps, targetIDs specifies which widget IDs to include
func CalculateBoundingBox(widgets []map[string]interface{}, targetIDs []string) (BoundingBox, int) {
	bb := BoundingBox{
		MinX: 1e9,
		MinY: 1e9,
		MaxX: -1e9,
		MaxY: -1e9,
	}

	targetIDSet := make(map[string]bool)
	for _, id := range targetIDs {
		targetIDSet[id] = true
	}

	noteCount := 0
	for _, w := range widgets {
		id, _ := w["id"].(string)
		if !targetIDSet[id] {
			continue
		}

		loc, _ := w["location"].(map[string]interface{})
		size, _ := w["size"].(map[string]interface{})
		if loc == nil || size == nil {
			continue
		}

		x, _ := loc["x"].(float64)
		y, _ := loc["y"].(float64)
		width, _ := size["width"].(float64)
		height, _ := size["height"].(float64)

		if x < bb.MinX {
			bb.MinX = x
		}
		if y < bb.MinY {
			bb.MinY = y
		}
		if x+width > bb.MaxX {
			bb.MaxX = x + width
		}
		if y+height > bb.MaxY {
			bb.MaxY = y + height
		}
		noteCount++
	}

	return bb, noteCount
}

// BuildAnchorPayload creates an anchor payload for grouping notes
func BuildAnchorPayload(anchorName string, bb BoundingBox, noteIDs []string) map[string]interface{} {
	return map[string]interface{}{
		"anchor_name": anchorName,
		"location":    map[string]interface{}{"x": bb.MinX, "y": bb.MinY},
		"size":        map[string]interface{}{"width": bb.Width(), "height": bb.Height()},
		"notes":       noteIDs,
	}
}

// ExtractWidgetLocation extracts location and size from a widget map
func ExtractWidgetLocation(widget map[string]interface{}) (x, y, w, h float64, ok bool) {
	loc, locOK := widget["location"].(map[string]interface{})
	size, sizeOK := widget["size"].(map[string]interface{})
	if !locOK || !sizeOK {
		return 0, 0, 0, 0, false
	}

	x, _ = loc["x"].(float64)
	y, _ = loc["y"].(float64)
	w, _ = size["width"].(float64)
	h, _ = size["height"].(float64)

	return x, y, w, h, true
}

// ExtractWidgetScale extracts the scale from a widget, defaulting to 1.0
func ExtractWidgetScale(widget map[string]interface{}) float64 {
	if scale, ok := widget["scale"].(float64); ok {
		return scale
	}
	if size, ok := widget["size"].(map[string]interface{}); ok {
		if scale, ok := size["scale"].(float64); ok {
			return scale
		}
	}
	return 1.0
}
