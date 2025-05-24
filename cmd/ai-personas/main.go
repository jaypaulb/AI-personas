package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jaypaulb/AI-personas/canvusapi"
	"github.com/jaypaulb/AI-personas/internal/canvus"
	"github.com/jaypaulb/AI-personas/internal/gemini"
	"github.com/jaypaulb/AI-personas/internal/logutil"
	"github.com/joho/godotenv"
)

var debugMode = false // Set to true for verbose debug logging
var noteMonitors = make(map[string]bool)
var chatTokenLimit = 256

// Helper to mask API keys for logging
func maskKey(key string) string {
	if len(key) <= 4 {
		return key
	}
	return strings.Repeat("*", len(key)-4) + key[len(key)-4:]
}

func housekeepingCheckAPIKeys() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 1. Gemini
	geminiKey := os.Getenv("GEMINI_API_KEY")
	gClient, err := gemini.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("Gemini API key check failed (key: %s): %w", maskKey(geminiKey), err)
	}
	// Make a real Gemini API call to catch expired/invalid keys
	// Use a trivial prompt to minimize cost
	_, err = gClient.GeneratePersonas(ctx, "health check")
	if err != nil {
		return fmt.Errorf("Gemini API key health check failed (key: %s): %w", maskKey(geminiKey), err)
	}
	// 2. OpenAI
	openaiKey := os.Getenv("OPENAI_API_KEY")
	if openaiKey == "" {
		return errors.New("OPENAI_API_KEY not set in environment")
	}
	openaiReq, _ := http.NewRequestWithContext(ctx, "GET", "https://api.openai.com/v1/models", nil)
	openaiReq.Header.Set("Authorization", "Bearer "+openaiKey)
	resp, err := http.DefaultClient.Do(openaiReq)
	if err != nil || resp.StatusCode != 200 {
		return fmt.Errorf("OpenAI API key check failed (key: %s): %v (status %d)", maskKey(openaiKey), err, resp.StatusCode)
	}
	resp.Body.Close()
	// 3. MCS (Canvus)
	mcsKey := os.Getenv("CANVUS_API_KEY")
	client, err := canvusapi.NewClientFromEnv()
	if err != nil {
		return fmt.Errorf("MCS API key check failed (key: %s): %w", maskKey(mcsKey), err)
	}
	_, err = client.GetCanvasInfo()
	if err != nil {
		return fmt.Errorf("MCS API key check failed (key: %s): %w", maskKey(mcsKey), err)
	}
	return nil
}

func main() {
	cwd, _ := os.Getwd()
	absEnvPath := filepath.Join(cwd, ".env")
	logutil.Debugf("[startup] Looking for .env at: %s", absEnvPath)
	if envMap, err := godotenv.Read(absEnvPath); err == nil {
		for k, v := range envMap {
			os.Setenv(k, v)
		}
	}

	if os.Getenv("DEBUG") == "1" {
		debugMode = true
	}

	if v := os.Getenv("CHAT_TOKEN_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			chatTokenLimit = n
		}
	}

	if err := housekeepingCheckAPIKeys(); err != nil {
		log.Fatalf("[startup] API key validation failed: %v", err)
	}

	client, err := canvusapi.NewClientFromEnv()
	if err != nil {
		log.Fatalf("Failed to initialize Canvus client: %v", err)
	}

	eventMonitor := canvus.NewEventMonitor(client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	triggers := make(chan canvus.EventTrigger, 10)

	// Handle graceful shutdown
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	var imageID, noteID string
	var imagePatched, notePatched bool
	var noteTextExtracted string
	var wg sync.WaitGroup
	var businessContext string
	go func() {
		<-sigs
		log.Println("Shutting down...")
		cancel()
	}()

	go eventMonitor.SubscribeAndDetectTriggers(ctx, triggers)

	for {
		select {
		case trig := <-triggers:
			// Flexible BAC_Complete image trigger (case-insensitive, ignores .png)
			imageTitle := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(trig.Widget.Title), ".png"))
			if trig.Widget.Type == "Image" && imageTitle == "bac_complete" && !imagePatched {
				imageID = trig.Widget.ID
				logutil.Debugf("\n\nImage Trigger - BAC Completed @ Loc Proceeding with Data Extraction\n\n")
				// Add a short delay to avoid race condition with backend
				time.Sleep(2 * time.Second)
				update := map[string]interface{}{"title": "BAC_Complete_Monitoring"}
				logutil.Debugf("[debug] UpdateImage payload: %v", update)
				resp, err := client.UpdateImage(imageID, update)
				respJSON, _ := json.MarshalIndent(resp, "", "  ")
				logutil.Debugf("[action] UpdateImage (to _Monitoring) response:\n%s\nerr: %v\n", string(respJSON), err)
				// Fetch the image after update
				fetched, fetchErr := client.GetImage(imageID, false)
				if fetchErr != nil {
					logutil.Debugf("[debug] Error fetching image after update: %v", fetchErr)
				} else {
					title, _ := fetched["title"].(string)
					logutil.Debugf("[debug] Image title after update (fetched): %q", title)
				}
				imagePatched = true
				// Start a goroutine to reset imagePatched after 60 seconds
				go func() {
					time.Sleep(60 * time.Second)
					imagePatched = false
					logutil.Infof("[info] imagePatched reset after timeout, ready for next BAC_Complete trigger\n")
				}()

				// Call the new helper function
				err = gemini.CreatePersonas(ctx, client)
				if err != nil {
					logutil.Errorf("[error] CreatePersonas failed: %v\n", err)
				}
			}
			if trig.Widget.Type == "Note" && trig.Widget.Title == "New_AI_Question" && !noteMonitors[trig.Widget.ID] {
				noteID = trig.Widget.ID
				logutil.Debugf("[trigger] New_AI_Question detected, ID: %s\n", noteID)
				// Prepend monitoring message and set pastel red
				origText := trig.Widget.Text
				newText := "I'm Monitoring this note please add your question below -->\n" + origText
				update := map[string]interface{}{
					"text":             newText,
					"background_color": "#ffcccc",
				}
				logutil.Infof("[action] Attempting to update note for monitoring: payload=%v", update)
				resp, err := client.UpdateNote(noteID, update)
				respJSON, _ := json.MarshalIndent(resp, "", "  ")
				if err != nil {
					logutil.Errorf("[action] UpdateNote (to monitoring) error: %v", err)
				} else {
					logutil.Infof("[action] UpdateNote (to monitoring) response: %s", string(respJSON))
				}
				notePatched = true
				noteMonitors[noteID] = true
				// Gather context for Q&A workflow
				ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel2()
				geminiClient, err := gemini.NewClient(ctx2)
				if err != nil {
					logutil.Errorf("[error] Failed to create Gemini client: %v\n", err)
					continue
				}
				personas, err := geminiClient.GeneratePersonas(ctx2, "Q&A context")
				if err != nil {
					logutil.Errorf("[error] Gemini persona generation failed: %v\n", err)
					continue
				}
				colors := []string{"#2196f3ff", "#4caf50ff", "#ff9800ff", "#9c27b0ff"}
				qWidget, _ := client.GetNote(noteID, false)
				// Fetch Qnote (question note) location and size
				qLoc, _ := qWidget["location"].(map[string]interface{})
				qSize, _ := qWidget["size"].(map[string]interface{})
				qx := qLoc["x"].(float64)
				qy := qLoc["y"].(float64)
				qw := qSize["width"].(float64)
				qh := qSize["height"].(float64)
				sessionManager := gemini.NewSessionManager(geminiClient.GenaiClient())
				// Start the note monitor goroutine with all required context
				wg.Add(1)
				go func(noteID string, noteTextExtracted *string, imageID *string, personas []gemini.Persona, colors []string, qx, qy, qw, qh float64, geminiClient *gemini.Client, sessionManager *gemini.SessionManager, businessContext string) {
					defer wg.Done()
					ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
					defer cancel2()
					server := strings.TrimRight(client.Server, "/")
					url := fmt.Sprintf("%s/api/v1/canvases/%s/notes/%s?subscribe", server, client.CanvasID, noteID)
					logutil.Infof("[note-monitor] Starting note monitor for ID: %s at URL: %s\n", noteID, url)
					req, err := http.NewRequestWithContext(ctx2, "GET", url, nil)
					if err != nil {
						logutil.Warnf("[note-monitor] Failed to create request: %v\n", err)
						return
					}
					req.Header.Set("Private-Token", client.ApiKey)
					resp, err := client.HTTP.Do(req)
					if err != nil {
						logutil.Warnf("[note-monitor] Failed to connect to stream: %v\n", err)
						return
					}
					defer resp.Body.Close()
					r := bufio.NewReader(resp.Body)
					var lastText string
					var timer *time.Timer
					for {
						select {
						case <-ctx2.Done():
							logutil.Infof("[note-monitor] Context done for note monitor\n")
							return
						default:
							line, err := r.ReadBytes('\n')
							if err != nil {
								if err.Error() == "EOF" {
									logutil.Infof("[note-monitor] EOF on note event stream, sleeping...\n")
									time.Sleep(1 * time.Second)
									continue
								}
								logutil.Errorf("[note-monitor] Error reading note event stream: %v\n", err)
								return
							}
							var raw map[string]interface{}
							if err := json.Unmarshal(line, &raw); err != nil {
								continue
							}
							text, _ := raw["text"].(string)
							trimmedText := strings.TrimSpace(text)
							if strings.HasSuffix(trimmedText, "?") {
								if lastText != trimmedText {
									lastText = trimmedText
									if timer != nil {
										timer.Stop()
									}
									timer = time.AfterFunc(3*time.Second, func() {
										logutil.Infof("[note-monitor] 3 seconds of inactivity after '?' detected. Proceeding to _Answering.\n")
										// Append answering message and set pastel amber
										fetched, err := client.GetNote(noteID, false)
										var currText string
										if err == nil {
											currText, _ = fetched["text"].(string)
										}
										newText := currText + "\nPlease wait - answering."
										updateText := map[string]interface{}{
											"text":             newText,
											"background_color": "#ffe4b3",
										}
										logutil.Infof("[note-monitor] Attempting to update note for answering: payload=%v", updateText)
										resp, err := client.UpdateNote(noteID, updateText)
										respJSON, _ := json.MarshalIndent(resp, "", "  ")
										if err != nil {
											logutil.Errorf("[note-monitor] UpdateNote (to answering) error: %v", err)
										} else {
											logutil.Infof("[note-monitor] UpdateNote (to answering) response: %s", string(respJSON))
										}
										// --- Persona Q&A Workflow ---
										// 1. Extract the user's question (strip monitoring/answering messages)
										question := currText
										if idx := strings.Index(question, "-->"); idx != -1 {
											question = question[idx+3:]
										}
										question = strings.TrimSpace(strings.Split(question, "Please wait")[0])
										// Define grid positions for 4 personas: top, right, bottom, left (answers); top-right, bottom-right, bottom-left, top-left (meta answers)
										gridPositions := [][2]float64{
											{0, -1},  // top (Persona 1 Answer)
											{1, 0},   // right (Persona 2 Answer)
											{0, 1},   // bottom (Persona 3 Answer)
											{-1, 0},  // left (Persona 4 Answer)
											{1, -1},  // top-right (Persona 2 Meta)
											{1, 1},   // bottom-right (Persona 3 Meta)
											{-1, 1},  // bottom-left (Persona 4 Meta)
											{-1, -1}, // top-left (Persona 1 Meta)
										}
										answerNoteIDs := make([]string, 4)
										metaNoteIDs := make([]string, 4)
										// 1. Create answer notes in cardinal directions
										for i, p := range personas {
											pos := gridPositions[i]
											ansX := qx + pos[0]*qw
											ansY := qy + pos[1]*qh
											answer, _ := geminiClient.AnswerQuestion(ctx, p, question, sessionManager, businessContext)
											if len(answer) > chatTokenLimit {
												succinctPrompt := "Please rephrase your answer in a much more succinct, short, and verbal way. Limit your response to " + fmt.Sprintf("%d", chatTokenLimit) + " characters."
												answer, _ = geminiClient.AnswerQuestion(ctx, p, succinctPrompt, sessionManager, businessContext)
											}
											noteMeta := map[string]interface{}{
												"title":            p.Name + " Answer",
												"text":             answer,
												"location":         map[string]interface{}{"x": ansX, "y": ansY},
												"size":             map[string]interface{}{"width": qw, "height": qh},
												"background_color": colors[i%len(colors)],
											}
											ansNote, _ := client.CreateNote(noteMeta)
											ansNoteID, _ := ansNote["id"].(string)
											answerNoteIDs[i] = ansNoteID
										}
										// 2. Create meta answer notes in corners, after all answers are created
										for i, p := range personas {
											// Gather other answers
											others := []string{}
											for j, ans := range answerNoteIDs {
												if i != j {
													ansNote, _ := client.GetNote(ans, false)
													ansText, _ := ansNote["text"].(string)
													others = append(others, fmt.Sprintf("%s said: %s", personas[j].Name, ansText))
												}
											}
											metaPrompt := fmt.Sprintf("Thank you %s for the interesting answer. Does what you heard from the others change what you think in any way? You heard: %s", p.Name, strings.Join(others, "; "))
											metaAnswer, _ := geminiClient.AnswerQuestion(ctx, p, metaPrompt, sessionManager, businessContext)
											if len(metaAnswer) > chatTokenLimit {
												succinctPrompt := "Please rephrase your answer in a much more succinct, short, and verbal way. Limit your response to " + fmt.Sprintf("%d", chatTokenLimit) + " characters."
												metaAnswer, _ = geminiClient.AnswerQuestion(ctx, p, succinctPrompt, sessionManager, businessContext)
											}
											metaPos := gridPositions[4+i]
											metaX := qx + metaPos[0]*qw
											metaY := qy + metaPos[1]*qh
											metaMeta := map[string]interface{}{
												"title":            p.Name + " Meta Answer",
												"text":             metaAnswer,
												"location":         map[string]interface{}{"x": metaX, "y": metaY},
												"size":             map[string]interface{}{"width": qw, "height": qh},
												"background_color": colors[i%len(colors)],
											}
											metaNote, _ := client.CreateNote(metaMeta)
											metaNoteID, _ := metaNote["id"].(string)
											metaNoteIDs[i] = metaNoteID
										}
										// 3. Create connectors: question -> answer, answer -> meta answer
										for i := range personas {
											connMeta1 := map[string]interface{}{
												"from_id": noteID,
												"to_id":   answerNoteIDs[i],
											}
											client.CreateConnector(connMeta1)
											connMeta2 := map[string]interface{}{
												"from_id": answerNoteIDs[i],
												"to_id":   metaNoteIDs[i],
											}
											client.CreateConnector(connMeta2)
										}
										// After all, set question note color to pastel green
										client.UpdateNote(noteID, map[string]interface{}{"background_color": "#ccffcc"})
										// Ensure space for 3x3 grid of notes around the question note
										if err := ensureGridSpace(client, noteID); err != nil {
											logutil.Warnf("[note-monitor] Could not ensure grid space: %v\n", err)
										}
										// Stop monitoring this note
										delete(noteMonitors, noteID)
										logutil.Infof("[note-monitor] Stopped monitoring note ID: %s after switching to answering.", noteID)
										// Exit the goroutine
										return
									})
								} else {
									// If text is the same, reset the timer
									if timer != nil {
										timer.Reset(3 * time.Second)
									}
								}
							}
						}
					}
				}(noteID, &noteTextExtracted, &imageID, personas, colors, qx, qy, qw, qh, geminiClient, sessionManager, businessContext)
			}
			if imagePatched && notePatched && noteTextExtracted != "" {
				logutil.Infof("[main] Note text extracted after '?': %s\n", noteTextExtracted)
				wg.Wait()
				return
			}
		case <-ctx.Done():
			wg.Wait()
			logutil.Infof("[main] Context cancelled. Exiting.\n")
			return
		}
	}
}

// ensureGridSpace checks for space around the question note for a 3x3 grid and moves/scales as needed.
func ensureGridSpace(client *canvusapi.Client, noteID string) error {
	const minSize = 0.02
	const moveStep = 500.0
	const scaleStep = 0.7
	maxAttempts := 10

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// 1. Fetch all widgets
		widgets, err := client.GetWidgets(false)
		if err != nil {
			return fmt.Errorf("failed to fetch widgets: %w", err)
		}

		// 2. Get canvas size
		var canvasWidth, canvasHeight float64
		for _, w := range widgets {
			if size, ok := w["canvas_size"].(map[string]interface{}); ok {
				canvasWidth, _ = size["width"].(float64)
				canvasHeight, _ = size["height"].(float64)
				break
			}
			if size, ok := w["size"].(map[string]interface{}); ok && canvasWidth == 0 && canvasHeight == 0 {
				canvasWidth, _ = size["width"].(float64)
				canvasHeight, _ = size["height"].(float64)
			}
		}
		if canvasWidth == 0 || canvasHeight == 0 {
			canvasWidth, canvasHeight = 1.0, 1.0
		}

		// 3. Get the question note's position and size
		var qx, qy, qw, qh float64
		for _, w := range widgets {
			if id, _ := w["id"].(string); id == noteID {
				if loc, ok := w["location"].(map[string]interface{}); ok {
					qx, _ = loc["x"].(float64)
					qy, _ = loc["y"].(float64)
				}
				if size, ok := w["size"].(map[string]interface{}); ok {
					qw, _ = size["width"].(float64)
					qh, _ = size["height"].(float64)
				}
				break
			}
		}
		if qw == 0 || qh == 0 {
			qw, qh = 0.1, 0.1
		}

		// 4. Compute grid positions
		grid := [][2]int{{0, 0}, {0, -1}, {1, 0}, {0, 1}, {-1, 0}, {1, -1}, {1, 1}, {-1, 1}, {-1, -1}}
		cellW, cellH := qw, qh
		positions := make([][2]float64, len(grid))
		for i, offset := range grid {
			positions[i][0] = qx + float64(offset[0])*cellW
			positions[i][1] = qy + float64(offset[1])*cellH
		}

		// 5. Check for overlap and edge proximity
		blocked := false
		for _, pos := range positions {
			gx, gy := pos[0], pos[1]
			if gx < 0 || gy < 0 || gx+cellW > canvasWidth || gy+cellH > canvasHeight {
				blocked = true
				break
			}
			for _, w := range widgets {
				if id, _ := w["id"].(string); id == noteID {
					continue
				}
				if w["widget_type"] != "Note" {
					continue
				}
				loc, lok := w["location"].(map[string]interface{})
				size, sok := w["size"].(map[string]interface{})
				if !lok || !sok {
					continue
				}
				wx, _ := loc["x"].(float64)
				wy, _ := loc["y"].(float64)
				ww, _ := size["width"].(float64)
				wh, _ := size["height"].(float64)
				if wx < gx+cellW && wx+ww > gx && wy < gy+cellH && wy+wh > gy {
					blocked = true
					break
				}
			}
			if blocked {
				break
			}
		}
		if !blocked {
			logutil.Infof("[ensureGridSpace] 3x3 grid around question note is clear.")
			return nil
		}

		// Try moving in all directions by moveStep
		directions := [][2]float64{
			{moveStep, 0}, {-moveStep, 0}, {0, moveStep}, {0, -moveStep},
			{moveStep, moveStep}, {moveStep, -moveStep}, {-moveStep, moveStep}, {-moveStep, -moveStep},
		}
		for _, dir := range directions {
			newQx, newQy := qx+dir[0], qy+dir[1]
			// Update note location
			update := map[string]interface{}{"location": map[string]interface{}{"x": newQx, "y": newQy}}
			_, err := client.UpdateNote(noteID, update)
			if err == nil {
				// Recurse to check if this move clears space
				if ensureGridSpace(client, noteID) == nil {
					return nil
				}
			}
		}
		// If still blocked, try scaling down
		if qw > minSize && qh > minSize {
			newQw, newQh := qw*scaleStep, qh*scaleStep
			update := map[string]interface{}{"size": map[string]interface{}{"width": newQw, "height": newQh}}
			_, err := client.UpdateNote(noteID, update)
			if err == nil {
				// Recurse to check if this scaling clears space
				if ensureGridSpace(client, noteID) == nil {
					return nil
				}
			}
		}
	}
	// If all attempts fail, update the note with a message for the user
	msg := "\nPlease move the note to provide clear space around it then delete this line."
	client.UpdateNote(noteID, map[string]interface{}{"text": msg})
	logutil.Warnf("[ensureGridSpace] Could not fit 3x3 grid after move/scale attempts. User intervention required.")
	return fmt.Errorf("blocked: cannot fit 3x3 grid around question note after move/scale attempts")
}
