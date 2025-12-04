package startup

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jaypaulb/AI-personas/canvusapi"
	"github.com/jaypaulb/AI-personas/internal/atom"
	"github.com/jaypaulb/AI-personas/internal/gemini"
)

// ValidateAPIKeys checks that all required API keys are valid and functional
func ValidateAPIKeys(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 1. Gemini
	if err := validateGeminiKey(ctx); err != nil {
		return err
	}

	// 2. OpenAI
	if err := validateOpenAIKey(ctx); err != nil {
		return err
	}

	// 3. Canvus (MCS)
	if err := validateCanvusKey(); err != nil {
		return err
	}

	return nil
}

func validateGeminiKey(ctx context.Context) error {
	geminiKey := os.Getenv("GEMINI_API_KEY")
	log.Printf("[startup] GEMINI_API_KEY: %s", atom.MaskKey(geminiKey))

	gClient, err := gemini.NewClient(ctx)
	if err != nil || gClient == nil {
		return fmt.Errorf("Gemini API key check failed (key: %s): %w", atom.MaskKey(geminiKey), err)
	}
	if gClient.GenaiClient() == nil {
		return fmt.Errorf("Gemini API client internal field is nil (key: %s)", atom.MaskKey(geminiKey))
	}
	log.Printf("[startup] Skipping personas health check: no noteID available for CreatePersonas.")
	return nil
}

func validateOpenAIKey(ctx context.Context) error {
	openaiKey := os.Getenv("OPENAI_API_KEY")
	if openaiKey == "" {
		return errors.New("OPENAI_API_KEY not set in environment")
	}

	openaiReq, _ := http.NewRequestWithContext(ctx, "GET", "https://api.openai.com/v1/models", nil)
	openaiReq.Header.Set("Authorization", "Bearer "+openaiKey)

	resp, err := http.DefaultClient.Do(openaiReq)
	if err != nil {
		return fmt.Errorf("OpenAI API key check failed (key: %s): %v", atom.MaskKey(openaiKey), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("OpenAI API key check failed (key: %s): status %d", atom.MaskKey(openaiKey), resp.StatusCode)
	}
	return nil
}

func validateCanvusKey() error {
	mcsKey := os.Getenv("CANVUS_API_KEY")
	client, err := canvusapi.NewClientFromEnv()
	if err != nil {
		return fmt.Errorf("MCS API key check failed (key: %s): %w", atom.MaskKey(mcsKey), err)
	}

	_, err = client.GetCanvasInfo()
	if err != nil {
		return fmt.Errorf("MCS API key check failed (key: %s): %w", atom.MaskKey(mcsKey), err)
	}
	return nil
}
