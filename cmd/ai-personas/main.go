package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/jaypaulb/AI-personas/canvusapi"
	"github.com/jaypaulb/AI-personas/internal/canvus"
	"github.com/jaypaulb/AI-personas/internal/gemini"
	"github.com/jaypaulb/AI-personas/internal/startup"
	"github.com/jaypaulb/AI-personas/internal/web"
	"github.com/joho/godotenv"
)

// GracefulShutdownTimeout is the maximum time to wait for goroutines to complete
const GracefulShutdownTimeout = 30 * time.Second

// Configuration loaded from environment
var (
	debugMode      = false
	chatTokenLimit = 256
	noteMonitors   sync.Map // Thread-safe map for concurrent access: noteID -> bool
)

// workflowWG tracks active workflow goroutines for graceful shutdown
var workflowWG sync.WaitGroup

func main() {
	// Load environment configuration
	loadEnv()

	// Validate all API keys at startup
	if err := startup.ValidateAPIKeys(30 * time.Second); err != nil {
		log.Fatalf("[startup] API key validation failed: %v", err)
	}

	// Initialize Canvus client
	client, err := canvusapi.NewClientFromEnv()
	if err != nil {
		log.Fatalf("Failed to initialize Canvus client: %v", err)
	}

	// Start web server
	webServer := web.NewServer(client)
	webServer.Start()

	// Start event monitoring
	eventMonitor := canvus.NewEventMonitor(client)
	ctx, cancel := context.WithCancel(context.Background())

	triggers := make(chan canvus.EventTrigger, 10)

	// Handle graceful shutdown
	setupShutdownHandler(cancel)

	// Start event subscription
	workflowWG.Add(1)
	go func() {
		defer workflowWG.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[error] SubscribeAndDetectTriggers panic recovered: %v\n%s", r, debug.Stack())
			}
		}()
		eventMonitor.SubscribeAndDetectTriggers(ctx, triggers)
	}()

	// Main event loop
	runEventLoop(ctx, client, triggers)

	// Wait for graceful shutdown
	waitForShutdown()
}

// loadEnv loads configuration from .env file and environment
func loadEnv() {
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
}

// setupShutdownHandler configures graceful shutdown on SIGINT/SIGTERM
func setupShutdownHandler(cancel context.CancelFunc) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		log.Println("[shutdown] Received shutdown signal, initiating graceful shutdown...")
		cancel()
	}()
}

// waitForShutdown waits for all goroutines to complete with a timeout
func waitForShutdown() {
	log.Printf("[shutdown] Waiting for active workflows to complete (timeout: %v)...", GracefulShutdownTimeout)

	done := make(chan struct{})
	go func() {
		workflowWG.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("[shutdown] All workflows completed gracefully.")
	case <-time.After(GracefulShutdownTimeout):
		log.Printf("[shutdown] Timeout after %v, forcing exit. Some workflows may not have completed.", GracefulShutdownTimeout)
	}

	log.Println("[shutdown] Exiting.")
}

// runEventLoop processes events from the trigger channel
func runEventLoop(ctx context.Context, client *canvusapi.Client, triggers <-chan canvus.EventTrigger) {
	for {
		log.Printf("[main] Waiting for triggers...")
		select {
		case trig := <-triggers:
			handleTrigger(ctx, client, trig)
		case <-ctx.Done():
			log.Printf("[main] Context cancelled. Exiting event loop.")
			return
		}
	}
}

// handleTrigger dispatches trigger events to appropriate handlers
func handleTrigger(ctx context.Context, client *canvusapi.Client, trig canvus.EventTrigger) {
	log.Printf("[main] Received trigger: {Type:%d Widget:{ID:%s Type:%s Title:%s}}",
		trig.Type, trig.Widget.ID, trig.Widget.Type, trig.Widget.Title)

	switch trig.Type {
	case canvus.TriggerCreatePersonasNote:
		handleCreatePersonas(ctx, client, trig)

	case canvus.TriggerNewAIQuestion:
		handleNewAIQuestion(ctx, client, trig)

	case canvus.TriggerConnectorCreated:
		handleConnectorCreated(ctx, client, trig)
	}
}

// handleCreatePersonas handles persona creation triggers
func handleCreatePersonas(ctx context.Context, client *canvusapi.Client, trig canvus.EventTrigger) {
	log.Printf("\n\nTrigger - Create_Personas Note detected. Proceeding with Persona Creation.\n\n")
	err := gemini.CreatePersonas(ctx, trig.Widget.ID, client)
	if err != nil {
		log.Printf("[error] CreatePersonas failed: %v\n", err)
		return
	}

	// Delete the Create_Personas note after successful persona creation
	if err := client.DeleteNote(trig.Widget.ID); err != nil {
		log.Printf("[error] Failed to delete Create_Personas note: %v\n", err)
	} else {
		log.Printf("[action] Deleted Create_Personas note (ID: %s) after persona creation.", trig.Widget.ID)
	}
}

// handleNewAIQuestion handles new AI question triggers
func handleNewAIQuestion(ctx context.Context, client *canvusapi.Client, trig canvus.EventTrigger) {
	log.Printf("[main] TriggerNewAIQuestion for noteID=%s", trig.Widget.ID)
	// Thread-safe check and store using sync.Map
	if _, loaded := noteMonitors.LoadOrStore(trig.Widget.ID, true); !loaded {
		log.Printf("[main] Launching HandleAIQuestion goroutine for noteID=%s", trig.Widget.ID)
		workflowWG.Add(1)
		go func(noteID string) {
			defer workflowWG.Done()
			defer noteMonitors.Delete(noteID) // Cleanup after workflow completion
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[error] handleNewAIQuestion goroutine panic recovered for noteID=%s: %v\n%s", noteID, r, debug.Stack())
				}
			}()
			gemini.HandleAIQuestion(ctx, client, trig.Widget, chatTokenLimit)
		}(trig.Widget.ID)
	}
}

// handleConnectorCreated handles connector creation triggers
func handleConnectorCreated(ctx context.Context, client *canvusapi.Client, trig canvus.EventTrigger) {
	log.Printf("[main] TriggerConnectorCreated for connectorID=%s", trig.Widget.ID)
	workflowWG.Add(1)
	go func() {
		defer workflowWG.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[error] handleConnectorCreated goroutine panic recovered for connectorID=%s: %v\n%s", trig.Widget.ID, r, debug.Stack())
			}
		}()
		gemini.HandleFollowupConnector(ctx, client, trig.Widget, chatTokenLimit)
	}()
}
