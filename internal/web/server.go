package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Showmax/go-fqdn"
	"github.com/jaypaulb/AI-personas/canvusapi"
	"github.com/skip2/go-qrcode"
)

// Version can be set at build time via -ldflags
var Version = "dev"

// startTime records when the server started for uptime calculation
var startTime time.Time

func init() {
	startTime = time.Now()
}

// HealthStatus represents the overall health status of the service
type HealthStatus string

const (
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusDegraded  HealthStatus = "degraded"
	HealthStatusUnhealthy HealthStatus = "unhealthy"
)

// HealthResponse is the JSON response for the /health endpoint
type HealthResponse struct {
	Status  HealthStatus `json:"status"`
	Uptime  string       `json:"uptime"`
	Version string       `json:"version"`
	Details struct {
		CanvusAPI bool `json:"canvus_api"`
	} `json:"details"`
}

// ServerConfig holds configuration for the web server
type ServerConfig struct {
	Port         string
	PublicWebURL string
	QRCodePath   string
}

// DefaultServerConfig returns configuration from environment
func DefaultServerConfig() ServerConfig {
	port := os.Getenv("PORT")
	if port == "" {
		port = os.Getenv("WEB_PORT")
	}
	if port == "" {
		port = "8080"
	}

	return ServerConfig{
		Port:         port,
		PublicWebURL: os.Getenv("PUBLIC_WEB_URL"),
		QRCodePath:   "qr_remote.png",
	}
}

// Server manages the web server and QR code functionality
type Server struct {
	Client *canvusapi.Client
	Config ServerConfig
}

// NewServer creates a new web server instance
func NewServer(client *canvusapi.Client) *Server {
	return NewServerWithConfig(client, DefaultServerConfig())
}

// NewServerWithConfig creates a new web server with custom configuration
func NewServerWithConfig(client *canvusapi.Client, config ServerConfig) *Server {
	return &Server{
		Client: client,
		Config: config,
	}
}

// GetWebURL returns the public web URL for the server
func (s *Server) GetWebURL() string {
	if s.Config.PublicWebURL != "" {
		return s.Config.PublicWebURL
	}

	fqdnHost, err := fqdn.FqdnHostname()
	if err != nil || fqdnHost == "" {
		fqdnHost, _ = os.Hostname()
	}
	return "http://" + fqdnHost + ":" + s.Config.Port + "/"
}

// Start starts the web server and QR code watcher
func (s *Server) Start() {
	webURL := s.GetWebURL()
	s.startQRCodeWatcher(webURL)

	fqdnHost, _ := fqdn.FqdnHostname()
	log.Printf("[web] Starting web server on :%s (FQDN: %s)", s.Config.Port, fqdnHost)

	http.HandleFunc("/", s.handleRoot)
	http.HandleFunc("/health", s.handleHealth)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	go func() {
		log.Printf("[web] Listening on :%s (FQDN: %s)", s.Config.Port, fqdnHost)
		http.ListenAndServe(":"+s.Config.Port, nil)
	}()
}

// handleHealth handles the /health endpoint for service health checks
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	uptime := time.Since(startTime)

	// Check Canvus API availability with a simple GetWidgets call
	canvusOK := true
	_, err := s.Client.GetWidgets(false)
	if err != nil {
		canvusOK = false
		log.Printf("[web][health] Canvus API check failed: %v", err)
	}

	// Determine overall health status
	status := HealthStatusHealthy
	if !canvusOK {
		status = HealthStatusUnhealthy
	}

	response := HealthResponse{
		Status:  status,
		Uptime:  formatUptime(uptime),
		Version: Version,
	}
	response.Details.CanvusAPI = canvusOK

	w.Header().Set("Content-Type", "application/json")
	if status == HealthStatusUnhealthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[web][error] Failed to encode health response: %v", err)
	}
}

// formatUptime formats a duration into a human-readable string
func formatUptime(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm %ds", days, hours, minutes, seconds)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

// handleRoot handles the root endpoint for question submission
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html")
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
		s.handleQuestionSubmission(w, r)
		return
	}

	w.WriteHeader(405)
}

// handleQuestionSubmission processes submitted questions
func (s *Server) handleQuestionSubmission(w http.ResponseWriter, r *http.Request) {
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

	// Find the Remote anchor zone
	widgets, err := s.Client.GetWidgets(false)
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

	// Calculate note position
	anchorLoc, _ := remoteAnchor["location"].(map[string]interface{})
	anchorSize, _ := remoteAnchor["size"].(map[string]interface{})
	ax := anchorLoc["x"].(float64)
	ay := anchorLoc["y"].(float64)
	aw := anchorSize["width"].(float64)
	ah := anchorSize["height"].(float64)

	noteX, noteY, noteW, noteH, scale, err := s.findFreeSegment(widgets, ax, ay, aw, ah)
	if err != nil {
		w.WriteHeader(409)
		w.Write([]byte(err.Error()))
		return
	}

	noteMeta := map[string]interface{}{
		"title":            "New_AI_Question",
		"text":             question,
		"location":         map[string]interface{}{"x": noteX - noteW*scale/2, "y": noteY - noteH*scale/2},
		"size":             map[string]interface{}{"width": noteW, "height": noteH},
		"scale":            scale,
		"background_color": "#FFFFFFFF",
	}

	_, err = s.Client.CreateNote(noteMeta)
	if err != nil {
		w.WriteHeader(500)
		w.Write([]byte("Failed to create note: " + err.Error()))
		return
	}

	w.Write([]byte("Question submitted!"))
}

// findFreeSegment finds a free segment in the Remote anchor grid
func (s *Server) findFreeSegment(widgets []map[string]interface{}, ax, ay, aw, ah float64) (noteX, noteY, noteW, noteH, scale float64, err error) {
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
		return 0, 0, 0, 0, 0, fmt.Errorf("Anchor is full: no free segments available")
	}

	// Center of the segment
	noteX = ax + float64(segCol)*segW + segW/2
	noteY = ay + float64(segRow)*segH + segH/2
	// Note size is 2/3 of the segment size
	noteW = segW * (2.0 / 3.0)
	noteH = segH * (2.0 / 3.0)
	// Scale so that the note appears the same size onscreen
	scale = 1.5 / 3.5

	return noteX, noteY, noteW, noteH, scale, nil
}

// startQRCodeWatcher starts the QR code creation and monitoring goroutine
func (s *Server) startQRCodeWatcher(webURL string) {
	go func() {
		ctx := context.Background()
		var qrID string

		for {
			// Create QR code if we don't have one
			if qrID == "" {
				var err error
				qrID, err = s.createAndPlaceQRCode(webURL)
				if err != nil {
					log.Printf("[web][error] Could not create initial QR code: %v", err)
					time.Sleep(5 * time.Second)
					continue
				}
				log.Printf("[web] QR code created (ID: %s), starting subscription...", qrID)
				time.Sleep(2 * time.Second)
			}

			// Subscribe to the QR code widget stream
			stream, err := s.Client.SubscribeToImage(ctx, qrID)
			if err != nil {
				log.Printf("[web][error] Failed to subscribe to QR code widget (ID: %s): %v", qrID, err)
				qrID = ""
				time.Sleep(5 * time.Second)
				continue
			}

			log.Printf("[web] Subscribed to QR code widget (ID: %s)", qrID)

			deleted := s.watchQRCodeStream(stream, qrID)
			if deleted {
				qrID = ""
			} else if qrID != "" {
				log.Printf("[web] QR code subscription ended, will resubscribe (ID: %s)", qrID)
				time.Sleep(2 * time.Second)
			}
		}
	}()
}

// watchQRCodeStream monitors the QR code widget stream for deletion
func (s *Server) watchQRCodeStream(stream io.ReadCloser, qrID string) bool {
	defer stream.Close()
	r := bufio.NewReader(stream)
	deleted := false

	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			if err == io.EOF && !deleted {
				log.Printf("[web] QR code subscription stream ended unexpectedly (ID: %s)", qrID)
			}
			break
		}

		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" || trimmed == "\r" {
			continue
		}

		// Try parsing as a single widget event
		var widgetEvent map[string]interface{}
		if err := json.Unmarshal(line, &widgetEvent); err == nil {
			if id, ok := widgetEvent["id"].(string); ok && id == qrID {
				if state, ok := widgetEvent["state"].(string); ok && state == "deleted" {
					log.Printf("[web] QR code widget deleted (ID: %s), will recreate...", qrID)
					deleted = true
					break
				}
			}
			continue
		}

		// Try parsing as an array of events
		var events []map[string]interface{}
		if err := json.Unmarshal(line, &events); err == nil {
			for _, ev := range events {
				if id, ok := ev["id"].(string); ok && id == qrID {
					if state, ok := ev["state"].(string); ok && state == "deleted" {
						log.Printf("[web] QR code widget deleted (ID: %s), will recreate...", qrID)
						deleted = true
						break
					}
				}
			}
			if deleted {
				break
			}
		}
	}

	return deleted
}

// createAndPlaceQRCode creates and places a QR code on the canvas
func (s *Server) createAndPlaceQRCode(webURL string) (string, error) {
	log.Printf("[web] Generating QR code for URL: %s", webURL)
	err := qrcode.WriteFile(webURL, qrcode.Medium, 256, s.Config.QRCodePath)
	if err != nil {
		log.Printf("[web][error] Failed to generate QR code: %v", err)
		return "", err
	}
	log.Printf("[web] QR code generated at %s", s.Config.QRCodePath)

	// Delete any existing QR code
	widgets, err := s.Client.GetWidgets(false)
	if err != nil {
		log.Printf("[web][error] Failed to fetch widgets for QR cleanup: %v", err)
		return "", err
	}

	for _, w := range widgets {
		if w["widget_type"] == "Image" && w["title"] == "Remote QR" {
			if id, ok := w["id"].(string); ok {
				if delErr := s.Client.DeleteImage(id); delErr != nil {
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

	if remoteAnchor == nil {
		log.Printf("[web][warn] Remote anchor not found; QR code not uploaded.")
		return "", fmt.Errorf("Remote anchor not found")
	}

	// Calculate QR code position and size
	anchorLoc, _ := remoteAnchor["location"].(map[string]interface{})
	anchorSize, _ := remoteAnchor["size"].(map[string]interface{})
	ax := anchorLoc["x"].(float64)
	ay := anchorLoc["y"].(float64)
	aw := anchorSize["width"].(float64)
	ah := anchorSize["height"].(float64)

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
	imgWidget, err := s.Client.CreateImage(s.Config.QRCodePath, imgMeta)
	if err != nil {
		log.Printf("[web][error] Failed to upload QR code image: %v", err)
		return "", err
	}

	log.Printf("[web] QR code image uploaded to Remote anchor.")
	log.Printf("[web][QRCODE] Remote access URL established: %s", webURL)

	// Extract and verify ID
	extractedID := ""
	if id, ok := imgWidget["id"].(string); ok {
		extractedID = id
	}

	// Verify by fetching widgets
	widgets, err = s.Client.GetWidgets(false)
	if err != nil {
		if extractedID != "" {
			return extractedID, nil
		}
		return "", fmt.Errorf("QR code image widget ID not found and cannot verify")
	}

	for _, w := range widgets {
		if w["widget_type"] == "Image" && w["title"] == "Remote QR" {
			if actualID, ok := w["id"].(string); ok {
				return actualID, nil
			}
		}
	}

	if extractedID != "" {
		return extractedID, nil
	}
	return "", fmt.Errorf("QR code image widget ID not found")
}
