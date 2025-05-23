package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jaypaulb/AI-personas/canvusapi"
	"github.com/jaypaulb/AI-personas/internal/canvus"
	"github.com/jaypaulb/AI-personas/internal/gemini"
	"github.com/joho/godotenv"
)

func main() {
	cwd, _ := os.Getwd()
	absEnvPath := filepath.Join(cwd, ".env")
	log.Printf("[startup] Looking for .env at: %s", absEnvPath)
	_ = godotenv.Load(absEnvPath)

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
	var noteMonitorStarted bool
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
		select {
		case trig := <-triggers:
			// Flexible BAC_Complete image trigger (case-insensitive, ignores .png)
			imageTitle := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(trig.Widget.Title), ".png"))
			if trig.Widget.Type == "Image" && imageTitle == "bac_complete" && !imagePatched {
				imageID = trig.Widget.ID
				log.Printf("\n\nImage Trigger - BAC Completed @ Loc Proceeding with Data Extraction\n\n")
				// Add a short delay to avoid race condition with backend
				time.Sleep(2 * time.Second)
				update := map[string]interface{}{"title": "BAC_Complete_Monitoring"}
				log.Printf("[debug] UpdateImage payload: %v", update)
				resp, err := client.UpdateImage(imageID, update)
				respJSON, _ := json.MarshalIndent(resp, "", "  ")
				log.Printf("[action] UpdateImage (to _Monitoring) response:\n%s\nerr: %v\n", string(respJSON), err)
				// Fetch the image after update
				fetched, fetchErr := client.GetImage(imageID, false)
				if fetchErr != nil {
					log.Printf("[debug] Error fetching image after update: %v", fetchErr)
				} else {
					title, _ := fetched["title"].(string)
					log.Printf("[debug] Image title after update (fetched): %q", title)
				}
				imagePatched = true
				// Start a goroutine to reset imagePatched after 60 seconds
				go func() {
					time.Sleep(60 * time.Second)
					imagePatched = false
					log.Printf("[info] imagePatched reset after timeout, ready for next BAC_Complete trigger\n")
				}()

				// Step 1: Fetch all widgets
				widgets, err := client.GetWidgets(false)
				if err != nil {
					log.Printf("[error] Failed to fetch widgets: %v\n", err)
					continue
				}

				// Step 2: Filter business notes
				requiredTitles := []string{
					"KEY PARTNERS",
					"KEY ACTIVITIES",
					"VALUE PROPOSITIONS",
					"CUSTOMER RELATIONSHIPS",
					"CUSTOMER SEGMENTS",
					"KEY RESOURCES",
					"CHANNELS",
					"COST STRUCTURE",
					"REVENUE STREAMS",
				}
				titleMap := make(map[string]bool)
				for _, t := range requiredTitles {
					titleMap[t] = false
				}
				var businessNotes []map[string]interface{}
				var personasAnchor map[string]interface{}
				for _, w := range widgets {
					typeStr, _ := w["widget_type"].(string)
					title, _ := w["title"].(string)
					titleUpper := strings.ToUpper(strings.TrimSpace(title))
					if typeStr == "Note" && titleMap[titleUpper] == false {
						for _, req := range requiredTitles {
							if titleUpper == req {
								businessNotes = append(businessNotes, w)
								titleMap[req] = true
								log.Printf("Extracted data from Note - %s\n", req)
							}
						}
					}
					if typeStr == "Anchor" {
						anchorName, _ := w["anchor_name"].(string)
						if strings.EqualFold(strings.TrimSpace(anchorName), "Personas") {
							personasAnchor = w
						}
					}
				}

				missing := false
				for _, req := range requiredTitles {
					if !titleMap[req] {
						log.Printf("Note - %s not found Aborting\n", req)
						missing = true
					}
				}
				if missing {
					log.Printf("\nAborting extraction due to missing notes.\n")
					continue
				}

				if personasAnchor == nil {
					log.Printf("Note - Personas anchor not found Aborting\n")
					continue
				}

				log.Printf("Successfully extracted all data - parsing and compiling report for AI\n")
				// Step 3: Structure and log the data
				structured := struct {
					BusinessNotes  []string               `json:"business_notes"`
					PersonasAnchor map[string]interface{} `json:"personas_anchor"`
				}{
					BusinessNotes:  []string{},
					PersonasAnchor: personasAnchor,
				}
				for _, n := range businessNotes {
					title, _ := n["title"].(string)
					text, _ := n["text"].(string)
					structured.BusinessNotes = append(structured.BusinessNotes, fmt.Sprintf("%s: %s", title, text))
				}
				jsonOut, _ := json.MarshalIndent(structured, "", "  ")
				log.Printf("\nAI Report = %s\n\n", string(jsonOut))

				// --- Gemini persona generation ---
				businessContext := strings.Join(structured.BusinessNotes, "\n\n")
				ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel2()
				geminiClient, err := gemini.NewClient(ctx2)
				if err != nil {
					log.Printf("[error] Failed to create Gemini client: %v\n", err)
					continue
				}
				personas, err := geminiClient.GeneratePersonas(ctx2, businessContext)
				if err != nil {
					log.Printf("[error] Gemini persona generation failed: %v\n", err)
					continue
				}
				// Color palette
				colors := []string{"#2196f3ff", "#4caf50ff", "#ff9800ff", "#9c27b0ff"}
				// Layout calculation
				anchor := structured.PersonasAnchor
				anchorLoc, _ := anchor["location"].(map[string]interface{})
				anchorSize, _ := anchor["size"].(map[string]interface{})
				ax := anchorLoc["x"].(float64)
				ay := anchorLoc["y"].(float64)
				aw := anchorSize["width"].(float64)
				ah := anchorSize["height"].(float64)
				border := 0.02
				colW := 0.23
				gap := 0.01
				imgH := 0.10
				var imgWg sync.WaitGroup
				for i, p := range personas {
					color := colors[i%len(colors)]
					formatted := gemini.FormatPersonaNote(p)
					// Calculate position
					x := ax + aw*border + float64(i)*(aw*colW+aw*gap)
					imgY := ay + ah*border
					imgW := aw * colW
					imgHpx := ah * imgH
					noteH := ah * 0.33 // fixed fraction of anchor height
					// Place note at the bottom of the anchor area, with a border
					noteY := ay + (ah * 0.34)
					noteMeta := map[string]interface{}{
						"title":            p.Name + " Persona",
						"text":             formatted,
						"location":         map[string]interface{}{"x": x, "y": noteY},
						"size":             map[string]interface{}{"width": imgW, "height": noteH * ah},
						"background_color": color,
					}
					noteWidget, err := client.CreateNote(noteMeta)
					if err != nil {
						log.Printf("[warn] Failed to create persona note: %v\n", err)
					} else {
						noteWidgetID, _ := noteWidget["id"].(string)
						log.Printf("[action] Persona note created: %s (ID: %s)", p.Name, noteWidgetID)
					}
					// Start image generation/upload in a goroutine
					imgWg.Add(1)
					go func(p gemini.Persona, x, imgY, imgW, imgHpx float64) {
						defer imgWg.Done()
						log.Printf("[debug] Calling OpenAI DALL·E for persona: %s", p.Name)
						imgBytes, err := gemini.GeneratePersonaImageOpenAI(p)
						log.Printf("[debug] OpenAI DALL·E call returned for persona: %s, err: %v", p.Name, err)
						imgPath := ""
						if err != nil {
							log.Printf("[warn] Persona image not generated: %v\n", err)
							return
						}
						tmpfile, err := os.CreateTemp("", "persona_*.png")
						if err != nil {
							log.Printf("[warn] Could not create temp file for persona image: %v\n", err)
							return
						}
						imgPath = tmpfile.Name()
						if _, err := tmpfile.Write(imgBytes); err != nil {
							log.Printf("[warn] Could not write persona image to temp file: %v\n", err)
							tmpfile.Close()
							os.Remove(imgPath)
							return
						}
						tmpfile.Close()
						imgMeta := map[string]interface{}{
							"title":    p.Name + " Headshot",
							"location": map[string]interface{}{"x": x, "y": imgY},
							"size":     map[string]interface{}{"width": imgW, "height": imgHpx},
						}
						imgWidget, err := client.CreateImage(imgPath, imgMeta)
						if err != nil {
							log.Printf("[warn] Failed to upload persona image: %v\n", err)
						} else {
							imgWidgetID, _ := imgWidget["id"].(string)
							log.Printf("[action] Persona image uploaded: %s (ID: %s)", p.Name, imgWidgetID)
						}
						os.Remove(imgPath)
					}(p, x, imgY, imgW, imgHpx)
				}
				imgWg.Wait()
				// --- end Gemini persona generation ---
			}
			if trig.Widget.Type == "Note" && trig.Widget.Title == "New_AI_Question" && !notePatched {
				noteID = trig.Widget.ID
				log.Printf("[trigger] New_AI_Question detected, ID: %s\n", noteID)
				update := map[string]interface{}{"title": "New_AI_Question_Monitoring"}
				resp, err := client.UpdateNote(noteID, update)
				respJSON, _ := json.MarshalIndent(resp, "", "  ")
				log.Printf("[action] UpdateNote (to _Monitoring) response:\n%s\nerr: %v\n", string(respJSON), err)
				notePatched = true
				// TODO: Future process for New_AI_Question trigger (e.g., Q&A workflow)
			}
			if notePatched && !noteMonitorStarted && noteID != "" {
				noteMonitorStarted = true
				wg.Add(1)
				go func(noteID string, noteTextExtracted *string, imageID *string) {
					defer wg.Done()
					ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
					defer cancel2()
					server := strings.TrimRight(client.Server, "/")
					url := fmt.Sprintf("%s/api/v1/canvases/%s/notes/%s?subscribe", server, client.CanvasID, noteID)
					log.Printf("[note-monitor] Starting note monitor for ID: %s at URL: %s\n", noteID, url)
					req, err := http.NewRequestWithContext(ctx2, "GET", url, nil)
					if err != nil {
						log.Printf("[note-monitor] Failed to create request: %v\n", err)
						return
					}
					req.Header.Set("Private-Token", client.ApiKey)
					resp, err := client.HTTP.Do(req)
					if err != nil {
						log.Printf("[note-monitor] Failed to connect to stream: %v\n", err)
						return
					}
					defer resp.Body.Close()
					r := bufio.NewReader(resp.Body)
					patched := false
					for {
						select {
						case <-ctx2.Done():
							log.Printf("[note-monitor] Context done for note monitor\n")
							return
						default:
							line, err := r.ReadBytes('\n')
							if err != nil {
								if err.Error() == "EOF" {
									log.Printf("[note-monitor] EOF on note event stream, sleeping...\n")
									time.Sleep(1 * time.Second)
									continue
								}
								log.Printf("[note-monitor] Error reading note event stream: %v\n", err)
								return
							}
							log.Printf("[note-monitor] Raw event: %s\n", string(line))
							var raw map[string]interface{}
							if err := json.Unmarshal(line, &raw); err != nil {
								log.Printf("[note-monitor] Skipping malformed line: %s\n", string(line))
								continue
							}
							text, _ := raw["text"].(string)
							log.Printf("[note-monitor] Note event received: text=%q\n", text)
							if !patched {
								patched = true
								time.Sleep(2 * time.Second)
								newText := strings.TrimSpace(text)
								if !strings.HasSuffix(newText, "?") {
									newText = newText + "?"
								}
								updateText := map[string]interface{}{"text": newText}
								log.Printf("[note-monitor] Patching note text. Before: %q, After: %q\n", text, newText)
								resp, err := client.UpdateNote(noteID, updateText)
								respJSON, _ := json.MarshalIndent(resp, "", "  ")
								log.Printf("[note-monitor] UpdateNote (add '?') response:\n%s\nerr: %v\n", string(respJSON), err)
								fetched, fetchErr := client.GetNote(noteID, false)
								if fetchErr != nil {
									log.Printf("[note-monitor] Error fetching note after patch: %v\n", fetchErr)
								} else {
									fetchedText, _ := fetched["text"].(string)
									log.Printf("[note-monitor] Note text after patch (fetched): %q\n", fetchedText)
								}
							}
							if strings.HasSuffix(strings.TrimSpace(text), "?") {
								log.Printf("[note-monitor] Note text now ends with '?', extracting.\n")
								*noteTextExtracted = text
								// TODO: Future process for note text ending with '?', e.g., trigger persona Q&A
								return
							}
						}
					}
				}(noteID, &noteTextExtracted, &imageID)
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
