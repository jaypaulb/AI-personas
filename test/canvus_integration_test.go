package test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jaypaulb/AI-personas/canvusapi"
	"github.com/jaypaulb/AI-personas/internal/canvus"
	"github.com/joho/godotenv"
)

func createTempPNG(filename string) (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	buf := new(bytes.Buffer)
	if err := png.Encode(buf, img); err != nil {
		return "", err
	}
	tmpfile, err := ioutil.TempFile("", filename)
	if err != nil {
		return "", err
	}
	if _, err := tmpfile.Write(buf.Bytes()); err != nil {
		tmpfile.Close()
		return "", err
	}
	tmpfile.Close()
	return tmpfile.Name(), nil
}

// Custom event monitor for debug logging
func subscribeAndDetectTriggersDebug(em *canvus.EventMonitor, ctx context.Context, triggers chan<- canvus.EventTrigger, t *testing.T) {
	client := em.Client
	server := strings.TrimRight(client.Server, "/")
	url := fmt.Sprintf("%s/api/v1/canvases/%s/widgets?subscribe", server, client.CanvasID)
	t.Logf("[debug] Subscribing to widgets at URL: %s", url)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		t.Fatalf("[debug] Failed to create request: %v", err)
	}
	req.Header.Set("Private-Token", client.ApiKey)

	resp, err := client.HTTP.Do(req)
	if err != nil {
		t.Fatalf("[debug] Failed to connect to stream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := ioutil.ReadAll(resp.Body)
		t.Fatalf("[debug] Unexpected status code: %d\nResponse body: %s", resp.StatusCode, string(body))
	}

	r := bufio.NewReader(resp.Body)
	for {
		select {
		case <-ctx.Done():
			t.Logf("[debug] Event monitor stopped.")
			return
		default:
			line, err := r.ReadBytes('\n')
			if err != nil {
				if err.Error() == "EOF" {
					t.Logf("[debug] EOF on event stream, sleeping...")
					time.Sleep(1 * time.Second)
					continue
				}
				t.Logf("[debug] Error reading event stream: %v", err)
				return
			}
			trimmed := strings.TrimSpace(string(line))
			if trimmed == "" || trimmed == "\r" {
				continue // keep-alive or empty
			}
			var events []map[string]interface{}
			if err := json.Unmarshal(line, &events); err != nil {
				t.Logf("[debug] Skipping malformed line: %s", string(line))
				continue
			}
			t.Logf("[event] %s", string(line)) // Log the raw event line
			for _, raw := range events {
				widType, _ := raw["widget_type"].(string)
				id, _ := raw["id"].(string)
				title, _ := raw["title"].(string)
				text, _ := raw["text"].(string)

				widget := canvus.WidgetEvent{
					ID:    id,
					Type:  widType,
					Title: title,
					Text:  text,
					Data:  raw,
				}

				// Detect BAC_Complete.png image creation
				if widType == "Image" {
					if strings.EqualFold(title, "BAC_Complete.png") {
						t.Logf("[trigger] BAC_Complete.png detected: %+v", widget)
						triggers <- canvus.EventTrigger{Type: canvus.TriggerBACCompleteImage, Widget: widget}
						// Test: update title, wait, then delete
						go func(id string) {
							time.Sleep(2 * time.Second)
							update := map[string]interface{}{"title": "UPDATED_BAC_Complete.png"}
							resp, err := client.UpdateImage(id, update)
							respJSON, _ := json.MarshalIndent(resp, "", "  ")
							t.Logf("[action] UpdateImage response:\n%s\nerr: %v\n", string(respJSON), err)
							time.Sleep(2 * time.Second)
							err = client.DeleteImage(id)
							t.Logf("[action] DeleteImage err: %v\n", err)
						}(id)
						continue
					}
				}

				// Detect New_AI_Question note creation
				if widType == "Note" && strings.EqualFold(title, "New_AI_Question") {
					t.Logf("[trigger] New_AI_Question detected: %+v", widget)
					triggers <- canvus.EventTrigger{Type: canvus.TriggerNewAIQuestion, Widget: widget}
					// Patch title to append _Monitoring
					go func(id string) {
						time.Sleep(2 * time.Second)
						update := map[string]interface{}{"title": "New_AI_Question_Monitoring"}
						resp, err := client.UpdateNote(id, update)
						respJSON, _ := json.MarshalIndent(resp, "", "  ")
						t.Logf("[action] UpdateNote (to _Monitoring) response:\n%s\nerr: %v\n", string(respJSON), err)
						// Start a new goroutine to monitor for text ending with '?'
						go func(noteID string) {
							ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
							defer cancel2()
							server := strings.TrimRight(client.Server, "/")
							url := fmt.Sprintf("%s/api/v1/canvases/%s/notes/%s?subscribe", server, client.CanvasID, noteID)
							t.Logf("[note-monitor] Starting note monitor for ID: %s at URL: %s\n", noteID, url)
							req, err := http.NewRequestWithContext(ctx2, "GET", url, nil)
							if err != nil {
								t.Logf("[note-monitor] Failed to create request: %v\n", err)
								return
							}
							req.Header.Set("Private-Token", client.ApiKey)
							resp, err := client.HTTP.Do(req)
							if err != nil {
								t.Logf("[note-monitor] Failed to connect to stream: %v\n", err)
								return
							}
							defer resp.Body.Close()
							r := bufio.NewReader(resp.Body)
							patched := false
							for {
								select {
								case <-ctx2.Done():
									t.Logf("[note-monitor] Context done for note monitor\n")
									return
								default:
									line, err := r.ReadBytes('\n')
									if err != nil {
										if err.Error() == "EOF" {
											t.Logf("[note-monitor] EOF on note event stream, sleeping...\n")
											time.Sleep(1 * time.Second)
											continue
										}
										t.Logf("[note-monitor] Error reading note event stream: %v\n", err)
										return
									}
									t.Logf("[note-monitor] Raw event: %s\n", string(line))
									var raw map[string]interface{}
									if err := json.Unmarshal(line, &raw); err != nil {
										t.Logf("[note-monitor] Skipping malformed line: %s\n", string(line))
										continue
									}
									text, _ := raw["text"].(string)
									t.Logf("[note-monitor] Note event received: text=%q\n", text)
									if !patched {
										// Wait 2s, then patch text to add ?
										patched = true
										time.Sleep(2 * time.Second)
										newText := strings.TrimSpace(text)
										if !strings.HasSuffix(newText, "?") {
											newText = newText + "?"
										}
										updateText := map[string]interface{}{"text": newText}
										t.Logf("[note-monitor] Patching note text. Before: %q, After: %q\n", text, newText)
										resp, err := client.UpdateNote(noteID, updateText)
										respJSON, _ := json.MarshalIndent(resp, "", "  ")
										t.Logf("[note-monitor] UpdateNote (add '?') response:\n%s\nerr: %v\n", string(respJSON), err)
										// Immediately fetch the note to confirm update
										fetched, fetchErr := client.GetNote(noteID, false)
										if fetchErr != nil {
											t.Logf("[note-monitor] Error fetching note after patch: %v\n", fetchErr)
										} else {
											fetchedText, _ := fetched["text"].(string)
											t.Logf("[note-monitor] Note text after patch (fetched): %q\n", fetchedText)
										}
									}
									if strings.HasSuffix(strings.TrimSpace(text), "?") {
										t.Logf("[note-monitor] Note text now ends with '?', extracting and deleting\n")
										err := client.DeleteNote(noteID)
										t.Logf("[action] DeleteNote err: %v\n", err)
										return
									}
								}
							}
						}(id)
					}(id)
					continue
				}
			}
		}
	}
}

func TestCanvusEventMonitor_Integration(t *testing.T) {
	cwd, _ := os.Getwd()
	absEnvPath := filepath.Join(cwd, ".env")
	parentEnvPath := filepath.Join(cwd, "..", ".env")

	loaded := false
	t.Logf("[startup] Looking for .env at: %s", absEnvPath)
	if err := godotenv.Load(absEnvPath); err == nil {
		t.Logf("Loaded .env from: %s", absEnvPath)
		loaded = true
	}
	if !loaded {
		t.Logf("[startup] Looking for .env at: %s", parentEnvPath)
		if err := godotenv.Load(parentEnvPath); err == nil {
			t.Logf("Loaded .env from: %s", parentEnvPath)
			loaded = true
		}
	}
	if !loaded {
		t.Logf("Could not find .env in either %s or %s", absEnvPath, parentEnvPath)
	}

	// Log all env vars for debugging
	for _, k := range []string{"CANVUS_API_KEY", "CANVUS_SERVER", "CANVAS_ID"} {
		t.Logf("[env] %s = %q", k, os.Getenv(k))
	}

	client, err := canvusapi.NewClientFromEnv()
	if err != nil {
		t.Fatalf("Failed to create Canvus client: %v", err)
	}

	eventMonitor := canvus.NewEventMonitor(client)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	triggers := make(chan canvus.EventTrigger, 10)
	// 1. Start listening for widget events BEFORE creating widgets
	go eventMonitor.SubscribeAndDetectTriggers(ctx, triggers)
	// Ensure subscription is established before creating widgets
	time.Sleep(2 * time.Second)

	// 2. Create BAC_Complete.png image widget at (0,0)
	imgPath, err := createTempPNG("BAC_Complete.png")
	if err != nil {
		t.Fatalf("Failed to create temp PNG: %v", err)
	}
	imgMeta := map[string]interface{}{
		"filename": "BAC_Complete.png",
		"title":    "BAC_Complete.png",
		"location": map[string]interface{}{"x": 0, "y": 0},
	}
	t.Logf("[api] Creating image widget: %+v\n", imgMeta)
	_, err = client.CreateImage(imgPath, imgMeta)
	if err != nil {
		t.Fatalf("[api] Failed to create BAC_Complete.png image widget: %v", err)
	}

	// 3. Create New_AI_Question note widget at (0,0) WITHOUT a '?'
	noteMeta := map[string]interface{}{
		"title":    "New_AI_Question",
		"text":     "What is the AI's opinion.", // no ?
		"location": map[string]interface{}{"x": 0, "y": 0},
	}
	t.Logf("[api] Creating note widget: %+v\n", noteMeta)
	_, err = client.CreateNote(noteMeta)
	if err != nil {
		t.Fatalf("[api] Failed to create New_AI_Question note widget: %v", err)
	}

	var imageID, noteID string
	var noteTextExtracted string
	var noteMonitorStarted bool
	var imagePatched, notePatched bool
	var wg sync.WaitGroup
	for {
		select {
		case trig := <-triggers:
			// Only use IDs from trigger events
			if trig.Widget.Type == "Image" && trig.Widget.Title == "BAC_Complete.png" && !imagePatched {
				imageID = trig.Widget.ID
				t.Logf("[trigger] BAC_Complete.png detected, ID: %s\n", imageID)
				// Patch image title to prevent retriggering
				update := map[string]interface{}{"title": "BAC_Complete_Monitoring.png"}
				resp, err := client.UpdateImage(imageID, update)
				respJSON, _ := json.MarshalIndent(resp, "", "  ")
				t.Logf("[action] UpdateImage (to _Monitoring) response:\n%s\nerr: %v\n", string(respJSON), err)
				imagePatched = true
			}
			if trig.Widget.Type == "Note" && trig.Widget.Title == "New_AI_Question" && !notePatched {
				noteID = trig.Widget.ID
				t.Logf("[trigger] New_AI_Question detected, ID: %s\n", noteID)
				// Patch note title to prevent retriggering
				update := map[string]interface{}{"title": "New_AI_Question_Monitoring"}
				resp, err := client.UpdateNote(noteID, update)
				respJSON, _ := json.MarshalIndent(resp, "", "  ")
				t.Logf("[action] UpdateNote (to _Monitoring) response:\n%s\nerr: %v\n", string(respJSON), err)
				notePatched = true
			}
			// Start note monitor only after note is patched and not already started
			if notePatched && !noteMonitorStarted && noteID != "" {
				noteMonitorStarted = true
				wg.Add(1)
				go func(noteID string, noteTextExtracted *string, imageID *string) {
					defer wg.Done()
					ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
					defer cancel2()
					server := strings.TrimRight(client.Server, "/")
					url := fmt.Sprintf("%s/api/v1/canvases/%s/notes/%s?subscribe", server, client.CanvasID, noteID)
					t.Logf("[note-monitor] Starting note monitor for ID: %s at URL: %s\n", noteID, url)
					req, err := http.NewRequestWithContext(ctx2, "GET", url, nil)
					if err != nil {
						t.Logf("[note-monitor] Failed to create request: %v\n", err)
						return
					}
					req.Header.Set("Private-Token", client.ApiKey)
					resp, err := client.HTTP.Do(req)
					if err != nil {
						t.Logf("[note-monitor] Failed to connect to stream: %v\n", err)
						return
					}
					defer resp.Body.Close()
					r := bufio.NewReader(resp.Body)
					patched := false
					for {
						select {
						case <-ctx2.Done():
							t.Logf("[note-monitor] Context done for note monitor\n")
							return
						default:
							line, err := r.ReadBytes('\n')
							if err != nil {
								if err.Error() == "EOF" {
									t.Logf("[note-monitor] EOF on note event stream, sleeping...\n")
									time.Sleep(1 * time.Second)
									continue
								}
								t.Logf("[note-monitor] Error reading note event stream: %v\n", err)
								return
							}
							t.Logf("[note-monitor] Raw event: %s\n", string(line))
							var raw map[string]interface{}
							if err := json.Unmarshal(line, &raw); err != nil {
								t.Logf("[note-monitor] Skipping malformed line: %s\n", string(line))
								continue
							}
							text, _ := raw["text"].(string)
							t.Logf("[note-monitor] Note event received: text=%q\n", text)
							if !patched {
								// Wait 2s, then patch text to add ?
								patched = true
								time.Sleep(2 * time.Second)
								newText := strings.TrimSpace(text)
								if !strings.HasSuffix(newText, "?") {
									newText = newText + "?"
								}
								updateText := map[string]interface{}{"text": newText}
								t.Logf("[note-monitor] Patching note text. Before: %q, After: %q\n", text, newText)
								resp, err := client.UpdateNote(noteID, updateText)
								respJSON, _ := json.MarshalIndent(resp, "", "  ")
								t.Logf("[note-monitor] UpdateNote (add '?') response:\n%s\nerr: %v\n", string(respJSON), err)
								// Immediately fetch the note to confirm update
								fetched, fetchErr := client.GetNote(noteID, false)
								if fetchErr != nil {
									t.Logf("[note-monitor] Error fetching note after patch: %v\n", fetchErr)
								} else {
									fetchedText, _ := fetched["text"].(string)
									t.Logf("[note-monitor] Note text after patch (fetched): %q\n", fetchedText)
								}
							}
							if strings.HasSuffix(strings.TrimSpace(text), "?") {
								t.Logf("[note-monitor] Note text now ends with '?', extracting and deleting\n")
								*noteTextExtracted = text
								err := client.DeleteNote(noteID)
								t.Logf("[action] DeleteNote err: %v\n", err)
								return
							}
						}
					}
				}(noteID, &noteTextExtracted, &imageID)
			}
			if imagePatched && notePatched && noteTextExtracted != "" {
				t.Logf("[test] Note text extracted after '?': %s\n", noteTextExtracted)
				wg.Wait()
				return // Success: all triggers and workflow completed
			}
		case <-ctx.Done():
			wg.Wait()
			if imageID == "" {
				t.Error("Did not detect BAC_Complete.png trigger within timeout")
			}
			if noteID == "" {
				t.Error("Did not detect New_AI_Question trigger within timeout")
			}
			if noteTextExtracted == "" {
				t.Error("Did not extract note text ending with '?' within timeout")
			}
			// Cleanup: attempt to delete any created note and image to avoid duplicates on next run
			if noteID != "" {
				t.Logf("[cleanup] Attempting to delete note with ID: %s\n", noteID)
				err := client.DeleteNote(noteID)
				if err != nil {
					t.Logf("[cleanup] DeleteNote error: %v\n", err)
				}
				// Verify deletion
				_, getErr := client.GetNote(noteID, false)
				if getErr == nil {
					t.Logf("[cleanup] Note still exists after delete, retrying...\n")
					err2 := client.DeleteNote(noteID)
					t.Logf("[cleanup] Retry DeleteNote err: %v\n", err2)
				} else {
					t.Logf("[cleanup] Note confirmed deleted or not found.\n")
				}
			}
			if imageID != "" {
				t.Logf("[cleanup] Attempting to delete image with ID: %s\n", imageID)
				err := client.DeleteImage(imageID)
				if err != nil {
					t.Logf("[cleanup] DeleteImage error: %v\n", err)
				}
				// Verify deletion
				_, getErr := client.GetImage(imageID, false)
				if getErr == nil {
					t.Logf("[cleanup] Image still exists after delete, retrying...\n")
					err2 := client.DeleteImage(imageID)
					t.Logf("[cleanup] Retry DeleteImage err: %v\n", err2)
				} else {
					t.Logf("[cleanup] Image confirmed deleted or not found.\n")
				}
			}
			return
		}
	}
}
