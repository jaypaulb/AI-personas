package main

import (
	"context"
	"errors"
	"fmt"
	"io"
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

	"github.com/Showmax/go-fqdn"
	"github.com/jaypaulb/AI-personas/canvusapi"
	"github.com/jaypaulb/AI-personas/internal/canvus"
	"github.com/jaypaulb/AI-personas/internal/gemini"
	"github.com/joho/godotenv"
	"github.com/skip2/go-qrcode"
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

// Helper to create QR code and return widget ID
func createAndPlaceQRCode(client *canvusapi.Client, webURL, qrPath string) (string, error) {
	log.Printf("[web] Generating QR code for URL: %s", webURL)
	err := qrcode.WriteFile(webURL, qrcode.Medium, 256, qrPath)
	if err != nil {
		log.Printf("[web][error] Failed to generate QR code: %v", err)
		return "", err
	}
	log.Printf("[web] QR code generated at %s", qrPath)

	widgets, err := client.GetWidgets(false)
	if err != nil {
		log.Printf("[web][error] Failed to fetch widgets for QR cleanup: %v", err)
		return "", err
	}
	for _, w := range widgets {
		if w["widget_type"] == "Image" && w["title"] == "Remote QR" {
			if id, ok := w["id"].(string); ok {
				if delErr := client.DeleteImage(id); delErr != nil {
					log.Printf("[web][error] Failed to delete old QR image (ID: %s): %v", id, delErr)
				} else {
					log.Printf("[web] Deleted old QR image (ID: %s)", id)
				}
			}
		}
	}

	// Find the Remote anchor zone
	var remoteAnchor map[string]interface{}
	for _, w := range widgets {
		typeStr, _ := w["widget_type"].(string)
		anchorName, _ := w["anchor_name"].(string)
		if typeStr == "Anchor" && strings.EqualFold(strings.TrimSpace(anchorName), "Remote") {
			remoteAnchor = w
			break
		}
	}
	if remoteAnchor != nil {
		anchorLoc, _ := remoteAnchor["location"].(map[string]interface{})
		anchorSize, _ := remoteAnchor["size"].(map[string]interface{})
		ax := anchorLoc["x"].(float64)
		ay := anchorLoc["y"].(float64)
		aw := anchorSize["width"].(float64)
		ah := anchorSize["height"].(float64)
		// QR code: 5% of zone area, top-left
		qrW := aw / 20.0
		qrH := ah / 20.0
		qrX := ax
		qrY := ay
		imgMeta := map[string]interface{}{
			"title":    "Remote QR",
			"location": map[string]interface{}{"x": qrX, "y": qrY},
			"size":     map[string]interface{}{"width": qrW, "height": qrH},
		}
		log.Printf("[web] Uploading QR code image to Remote anchor at (x=%.3f, y=%.3f, w=%.3f, h=%.3f)", qrX, qrY, qrW, qrH)
		imgWidget, err := client.CreateImage(qrPath, imgMeta)
		if err != nil {
			log.Printf("[web][error] Failed to upload QR code image: %v", err)
			return "", err
		}
		log.Printf("[web] QR code image uploaded to Remote anchor.")
		log.Printf("[web][QRCODE] Remote access URL established: %s", webURL)
		if id, ok := imgWidget["id"].(string); ok {
			return id, nil
		}
		return "", fmt.Errorf("QR code image widget ID not found")
	} else {
		log.Printf("[web][warn] Remote anchor not found; QR code not uploaded.")
		return "", fmt.Errorf("Remote anchor not found")
	}
}

func startQRCodeWatcher(client *canvusapi.Client, webURL, qrPath string) {
	go func() {
		for {
			qrID, err := createAndPlaceQRCode(client, webURL, qrPath)
			if err != nil {
				log.Printf("[web][error] Could not create initial QR code: %v", err)
				return
			}
			log.Printf("[web] Subscribing to QR code widget ID: %s", qrID)
			// Subscribe to the QR code widget events
			for {
				widget, err := client.GetWidget(qrID, true)
				if err != nil {
					log.Printf("[web][error] Subscription to QR code widget failed: %v", err)
					break // Try to recreate QR code
				}
				// If widget is nil or deleted, break and recreate
				if widget == nil {
					log.Printf("[web] QR code widget deleted (ID: %s), recreating...", qrID)
					break
				}
				// If widget has a "deleted" flag or similar, break and recreate
				// (Assume stream closes on delete)
			}
			// Loop to recreate QR code
		}
	}()
}

func startWebServer(client *canvusapi.Client) {
	port := os.Getenv("PORT")
	if port == "" {
		port = os.Getenv("WEB_PORT")
	}
	if port == "" {
		port = "8080"
	}
	fqdn, err := fqdn.FqdnHostname()
	if err != nil || fqdn == "" {
		fqdn, _ = os.Hostname()
	}
	webURL := "http://" + fqdn + ":" + port + "/"
	qrPath := "qr_remote.png"

	startQRCodeWatcher(client, webURL, qrPath)

	log.Printf("[web] Starting web server on :%s (FQDN: %s)", port, fqdn)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html")
			// Serve static/question.html
			f, err := os.Open("static/question.html")
			if err != nil {
				w.WriteHeader(500)
				w.Write([]byte("Question page not found. Please contact admin."))
				return
			}
			defer f.Close()
			io.Copy(w, f)
			return
		}
		if r.Method == http.MethodPost {
			err := r.ParseForm()
			if err != nil {
				w.WriteHeader(400)
				w.Write([]byte("Invalid form"))
				return
			}
			question := r.FormValue("question")
			if question == "" {
				w.WriteHeader(400)
				w.Write([]byte("Question required"))
				return
			}

			// Ensure the question ends with a '?'
			question = strings.TrimSpace(question)
			if !strings.HasSuffix(question, "?") {
				question = question + "?"
			}

			// Find the Remote anchor zone again (in case widgets changed)
			widgets, err := client.GetWidgets(false)
			if err != nil {
				w.WriteHeader(500)
				w.Write([]byte("Failed to fetch widgets"))
				return
			}
			var remoteAnchor map[string]interface{}
			for _, wgt := range widgets {
				typeStr, _ := wgt["widget_type"].(string)
				anchorName, _ := wgt["anchor_name"].(string)
				if typeStr == "Anchor" && strings.EqualFold(strings.TrimSpace(anchorName), "Remote") {
					remoteAnchor = wgt
					break
				}
			}
			if remoteAnchor == nil {
				w.WriteHeader(500)
				w.Write([]byte("Remote anchor not found"))
				return
			}
			anchorLoc, _ := remoteAnchor["location"].(map[string]interface{})
			anchorSize, _ := remoteAnchor["size"].(map[string]interface{})
			ax := anchorLoc["x"].(float64)
			ay := anchorLoc["y"].(float64)
			aw := anchorSize["width"].(float64)
			ah := anchorSize["height"].(float64)

			cols, rows := 5, 4
			segW := aw / float64(cols)
			segH := ah / float64(rows)

			// Build a 5x4 grid of segments (segment 0 is for QR code)
			used := make([]bool, cols*rows)
			for _, wgt := range widgets {
				if wgt["widget_type"] != "Note" && wgt["widget_type"] != "Image" {
					continue
				}
				loc, lok := wgt["location"].(map[string]interface{})
				size, sok := wgt["size"].(map[string]interface{})
				if !lok || !sok {
					continue
				}
				wx, _ := loc["x"].(float64)
				wy, _ := loc["y"].(float64)
				ww, _ := size["width"].(float64)
				wh, _ := size["height"].(float64)
				for row := 0; row < rows; row++ {
					for col := 0; col < cols; col++ {
						segX := ax + float64(col)*segW
						segY := ay + float64(row)*segH
						// Check for overlap (simple AABB)
						if wx < segX+segW && wx+ww > segX && wy < segY+segH && wy+wh > segY {
							used[row*cols+col] = true
						}
					}
				}
			}
			// Segment 0 (row 0, col 0) is reserved for QR code
			used[0] = true
			segmentFound := false
			var segCol, segRow int
			for i := 1; i < cols*rows; i++ {
				if !used[i] {
					segCol = i % cols
					segRow = i / cols
					segmentFound = true
					break
				}
			}
			if !segmentFound {
				w.WriteHeader(409)
				w.Write([]byte("No free segments available in anchor zone"))
				return
			}
			// Center of the segment
			noteX := ax + float64(segCol)*segW + segW/2
			noteY := ay + float64(segRow)*segH + segH/2
			// Note size is 2/3 of the segment size
			noteW := segW * (2.0 / 3.0)
			noteH := segH * (2.0 / 3.0)
			// Scale so that the note appears the same size onscreen (scale = 1.5x previous value)
			scale := 1.5 / 3.5
			noteMeta := map[string]interface{}{
				"title":            "New_AI_Question",
				"text":             question,
				"location":         map[string]interface{}{"x": noteX - noteW*scale/2, "y": noteY - noteH*scale/2},
				"size":             map[string]interface{}{"width": noteW, "height": noteH},
				"scale":            scale,
				"background_color": "#FFFFFFFF",
			}
			_, err = client.CreateNote(noteMeta)
			if err != nil {
				w.WriteHeader(500)
				w.Write([]byte("Failed to create note: " + err.Error()))
				return
			}

			w.Write([]byte("Question submitted!"))
			return
		}
		w.WriteHeader(405)
	})
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	go func() {
		log.Printf("[web] Listening on :%s (FQDN: %s)", port, fqdn)
		http.ListenAndServe(":"+port, nil)
	}()
}

func main() {
	cwd, _ := os.Getwd()
	absEnvPath := filepath.Join(cwd, ".env")
	log.Printf("[startup] Looking for .env at: %s", absEnvPath)
	if envMap, err := godotenv.Read(absEnvPath); err == nil {
		for k, v := range envMap {
			os.Setenv(k, v)
		}
		log.Printf("[startup] .env loaded from: %s", absEnvPath)
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
	startWebServer(client)

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
			log.Printf("[main] Received trigger: {Type:%d Widget:{ID:%s Type:%s Title:%s}}", trig.Type, trig.Widget.ID, trig.Widget.Type, trig.Widget.Title)
			switch trig.Type {
			case canvus.TriggerCreatePersonasNote:
				log.Printf("\n\nTrigger - Create_Personas Note detected. Proceeding with Persona Creation.\n\n")
				err := gemini.CreatePersonas(ctx, trig.Widget.ID, client)
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
