package main

import (
	"context"
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
	log.Printf("[startup] Looking for .env at: %s", absEnvPath)
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
	var imagePatched, notePatched bool
	var noteTextExtracted string
	var wg sync.WaitGroup
	go func() {
		<-sigs
		log.Println("Shutting down...")
		cancel()
	}()

	go eventMonitor.SubscribeAndDetectTriggers(ctx, triggers)

	for {
		log.Printf("[main] Waiting for triggers...")
		select {
		case trig := <-triggers:
			log.Printf("[main] Received trigger: %+v", trig)
			switch trig.Type {
			case canvus.TriggerCreatePersonasNote:
				log.Printf("\n\nTrigger - Create_Personas Note detected. Proceeding with Persona Creation.\n\n")
				err := gemini.CreatePersonas(ctx, client)
				if err != nil {
					log.Printf("[error] CreatePersonas failed: %v\n", err)
				} else {
					// Delete the Create_Personas note after successful persona creation
					if err := client.DeleteNote(trig.Widget.ID); err != nil {
						log.Printf("[error] Failed to delete Create_Personas note: %v\n", err)
					} else {
						log.Printf("[action] Deleted Create_Personas note (ID: %s) after persona creation.", trig.Widget.ID)
					}
				}
			case canvus.TriggerNewAIQuestion:
				log.Printf("[main] TriggerNewAIQuestion for noteID=%s", trig.Widget.ID)
				if !noteMonitors[trig.Widget.ID] {
					noteMonitors[trig.Widget.ID] = true
					log.Printf("[main] Launching HandleAIQuestion goroutine for noteID=%s", trig.Widget.ID)
					go gemini.HandleAIQuestion(ctx, client, trig.Widget, chatTokenLimit)
				}
			}
			if imagePatched && notePatched && noteTextExtracted != "" {
				log.Printf("[main] Note text extracted after '?': %s\n", noteTextExtracted)
				wg.Wait()
				return
			}
		case <-ctx.Done():
			wg.Wait()
			log.Printf("[main] Context cancelled. Exiting.\n")
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
			log.Printf("[ensureGridSpace] 3x3 grid around question note is clear.")
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
	log.Printf("[ensureGridSpace] Could not fit 3x3 grid after move/scale attempts. User intervention required.")
	return fmt.Errorf("blocked: cannot fit 3x3 grid around question note after move/scale attempts")
}
