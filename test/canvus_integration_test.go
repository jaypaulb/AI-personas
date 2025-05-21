package test

import (
	"context"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jaypaulb/AI-personas/canvusapi"
	"github.com/jaypaulb/AI-personas/internal/canvus"
)

func TestCanvusEventMonitor_Integration(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Logf("Current working directory: %s", cwd)

	envPath := filepath.Join(cwd, ".env")
	if _, err := os.Stat(envPath); err == nil {
		content, err := ioutil.ReadFile(envPath)
		if err == nil {
			t.Logf(".env file contents:\n%s", string(content))
		} else {
			t.Logf("Could not read .env file: %v", err)
		}
	} else {
		t.Logf(".env file not found at: %s", envPath)
	}

	t.Logf("CANVUS_API_KEY: %q", os.Getenv("CANVUS_API_KEY"))
	t.Logf("CANVUS_SERVER: %q", os.Getenv("CANVUS_SERVER"))
	t.Logf("CANVAS_ID: %q", os.Getenv("CANVAS_ID"))

	if os.Getenv("CANVUS_API_KEY") == "" || os.Getenv("CANVUS_SERVER") == "" || os.Getenv("CANVAS_ID") == "" {
		t.Skip("Skipping integration test: CANVUS_API_KEY, CANVUS_SERVER, or CANVAS_ID not set")
	}

	client, err := canvusapi.NewClientFromEnv()
	if err != nil {
		t.Fatalf("Failed to create Canvus client: %v", err)
	}

	eventMonitor := canvus.NewEventMonitor(client)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	triggers := make(chan canvus.EventTrigger, 10)
	go eventMonitor.SubscribeAndDetectTriggers(ctx, triggers)

	t.Log("Waiting for triggers. Please create BAC_Complete.png or New_AI_Question note in the test canvas.")
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
