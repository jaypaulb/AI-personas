package test

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io/ioutil"
	"os"
	"path/filepath"
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

func TestCanvusEventMonitor_Integration(t *testing.T) {
	cwd, _ := os.Getwd()
	_ = godotenv.Load(filepath.Join(cwd, ".env"))

	client, err := canvusapi.NewClientFromEnv()
	if err != nil {
		t.Fatalf("Failed to create Canvus client: %v", err)
	}

	eventMonitor := canvus.NewEventMonitor(client)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	triggers := make(chan canvus.EventTrigger, 10)
	go eventMonitor.SubscribeAndDetectTriggers(ctx, triggers)

	// 1. Create BAC_Complete.png image widget
	imgPath, err := createTempPNG("BAC_Complete.png")
	if err != nil {
		t.Fatalf("Failed to create temp PNG: %v", err)
	}
	defer os.Remove(imgPath)
	imgMeta := map[string]interface{}{"filename": "BAC_Complete.png", "title": "BAC_Complete.png"}
	_, err = client.CreateImage(imgPath, imgMeta)
	if err != nil {
		t.Fatalf("Failed to create BAC_Complete.png image widget: %v", err)
	}

	// 2. Create New_AI_Question note widget
	noteMeta := map[string]interface{}{"title": "New_AI_Question", "text": "What is the AI's opinion?"}
	_, err = client.CreateNote(noteMeta)
	if err != nil {
		t.Fatalf("Failed to create New_AI_Question note widget: %v", err)
	}

	// 3. Wait for triggers
	found := map[canvus.TriggerType]bool{}
	for {
		select {
		case trig := <-triggers:
			t.Logf("Trigger detected: %+v", trig)
			found[trig.Type] = true
			if found[canvus.TriggerBACCompleteImage] && found[canvus.TriggerNewAIQuestion] {
				return // Success: both triggers detected
			}
		case <-ctx.Done():
			if !found[canvus.TriggerBACCompleteImage] {
				t.Error("Did not detect BAC_Complete.png trigger within timeout")
			}
			if !found[canvus.TriggerNewAIQuestion] {
				t.Error("Did not detect New_AI_Question trigger within timeout")
			}
			return
		}
	}
}
