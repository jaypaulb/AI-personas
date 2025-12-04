package molecule

// AnswerGridPositions returns the relative grid offsets for 4 answer notes
// arranged around a center question note (top, right, bottom, left)
func AnswerGridPositions() [][2]int {
	return [][2]int{{0, -1}, {1, 0}, {0, 1}, {-1, 0}}
}

// MetaAnswerGridPositions returns the relative grid offsets for 4 meta-answer notes
// arranged at the diagonals (top-right, bottom-right, bottom-left, top-left)
func MetaAnswerGridPositions() [][2]int {
	return [][2]int{{1, -1}, {1, 1}, {-1, 1}, {-1, -1}}
}

// CalculateGridPosition calculates the absolute position for a note in a grid
// given the center position, grid offset, note dimensions, scale, and spacing
func CalculateGridPosition(centerX, centerY float64, gridOffset [2]int, noteW, noteH, scale, spacing float64) (x, y float64) {
	x = centerX + float64(gridOffset[0])*((noteW*scale)+spacing)
	y = centerY + float64(gridOffset[1])*((noteH*scale)+spacing)
	return x, y
}

// CalculateSpacing calculates the spacing between grid cells based on note width and scale
func CalculateSpacing(noteW, scale float64) float64 {
	return (noteW * scale) / 5.0
}

// PersonaColumnLayout calculates position for a persona note in a column layout
// index is 0-3, anchor dimensions define the container
func PersonaColumnLayout(index int, anchorX, anchorY, anchorW, anchorH float64) (x, imgY, noteY, colW, imgH, noteH float64) {
	border := 0.02
	colWidth := 0.23
	gap := 0.01
	imageH := 0.10
	noteHeight := 0.40

	colW = anchorW * colWidth
	imgH = anchorH * imageH
	noteH = noteHeight * anchorH

	x = anchorX + anchorW*border + float64(index)*(anchorW*colWidth+anchorW*gap)
	imgY = anchorY + anchorH*border
	noteY = anchorY + (anchorH * 0.34)

	return x, imgY, noteY, colW, imgH, noteH
}

// PersonaColors returns the standard color palette for persona notes
func PersonaColors() []string {
	return []string{"#2196f3ff", "#4caf50ff", "#ff9800ff", "#9c27b0ff"}
}

// HelperNotePosition calculates position for a helper note relative to a question note
func HelperNotePosition(qX, qY, qW, qH float64) (helperX, helperY, helperW, helperH float64) {
	helperX = qX - 1.2*qW
	helperY = qY - 0.33*qH
	helperW = qW
	helperH = qH * 0.7
	return
}
