package test

import (
	"context"
	"os"
	"testing"

	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/jaypaulb/AI-personas/internal/gemini"
	"google.golang.org/genai"
)

func TestGeminiAIImageGeneration(t *testing.T) {
	ctx := context.Background()
	client, err := gemini.NewClient(ctx)
	if err != nil {
		t.Fatalf("Failed to create Gemini client: %v", err)
	}
	persona := gemini.Persona{
		Name:        "John Smith",
		Role:        "VP of Sales & Marketing, Sports Team",
		Description: "Responsible for driving revenue and enhancing fan engagement for a major professional sports team.",
		Background:  "Extensive experience in sports marketing and sales, with a strong understanding of fan engagement strategies. Focuses on creating a unique and compelling fan experience.",
		Goals:       "Increase fan attendance, merchandise sales, and overall revenue. Seeking interactive displays for fan engagement and creating new revenue streams through sponsorships.",
	}

	modelsToTry := []string{"models/gemini-1.5-flash", "models/gemini-2.0-flash", "models/imagen-3.0-generate-002"}
	var imgBytes []byte
	var lastErr error
	for _, model := range modelsToTry {
		t.Logf("Trying model: %s", model)
		imgBytes, lastErr = tryGeneratePersonaImageWithModel(ctx, client, persona, model)
		if lastErr == nil && len(imgBytes) > 0 {
			t.Logf("Image generated successfully with model: %s", model)
			os.WriteFile("test_persona_image.png", imgBytes, 0644)
			return
		}
		t.Logf("Model %s failed: %v", model, lastErr)
	}
	t.Fatalf("All models failed to generate image: %v", lastErr)
}

func tryGeneratePersonaImageWithModel(ctx context.Context, client *gemini.Client, persona gemini.Persona, model string) ([]byte, error) {
	prompt := "Generate a realistic professional headshot photo of a person for a business persona profile. Name: " + persona.Name + ". Role: " + persona.Role + ". Description: " + persona.Description + ". Background: " + persona.Background + ". Goals: " + persona.Goals + ". The image should be a portrait, neutral background, natural lighting, and suitable for a business context."
	config := &genai.GenerateContentConfig{
		ResponseModalities: []string{"TEXT", "IMAGE"},
	}
	resp, err := client.GenaiClient().Models.GenerateContent(
		ctx,
		model,
		[]*genai.Content{{Parts: []*genai.Part{{Text: prompt}}}},
		config,
	)
	if err != nil {
		return nil, err
	}
	for _, part := range resp.Candidates[0].Content.Parts {
		if part.InlineData != nil && len(part.InlineData.Data) > 0 {
			return part.InlineData.Data, nil
		}
	}
	return nil, nil
}

func TestDirectHTTPImageGeneration(t *testing.T) {
	apiKey := "AIzaSyCpw5ugw6agpGndkJ_dYDQjX4WxV0JQanw"
	personaPrompt := "Generate a realistic professional headshot photo of a person for a business persona profile. Name: John Smith. Role: VP of Sales & Marketing, Sports Team. Description: Responsible for driving revenue and enhancing fan engagement for a major professional sports team. Background: Extensive experience in sports marketing and sales, with a strong understanding of fan engagement strategies. Focuses on creating a unique and compelling fan experience. Goals: Increase fan attendance, merchandise sales, and overall revenue. Seeking interactive displays for fan engagement and creating new revenue streams through sponsorships. The image should be a portrait, neutral background, natural lighting, and suitable for a business context."
	modelsToTry := []string{
		"gemini-2.0-flash-preview-image-generation",
		"imagen-3.0-generate-002",
	}
	for _, model := range modelsToTry {
		t.Logf("Trying direct HTTP for model: %s (generation_config/response_moalities)", model)
		url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1/models/%s:generateContent?key=%s", model, apiKey)
		body := map[string]interface{}{
			"contents": []map[string]interface{}{
				{"parts": []map[string]interface{}{{"text": personaPrompt}}},
			},
			"generation_config": map[string]interface{}{
				"response_moalities": []string{"TEXT", "IMAGE"},
			},
		}
		jsonBody, _ := json.Marshal(body)
		resp, err := http.Post(url, "application/json", bytes.NewReader(jsonBody))
		if err != nil {
			t.Logf("HTTP request failed: %v", err)
			continue
		}
		respBody, _ := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		t.Logf("HTTP status: %s", resp.Status)
		t.Logf("HTTP response: %s", string(respBody))
		// Try fallback with response_modalities at top level
		t.Logf("Trying direct HTTP for model: %s (response_modalities top-level)", model)
		body2 := map[string]interface{}{
			"contents": []map[string]interface{}{
				{"parts": []map[string]interface{}{{"text": personaPrompt}}},
			},
			"response_modalities": []string{"TEXT", "IMAGE"},
		}
		jsonBody2, _ := json.Marshal(body2)
		resp2, err2 := http.Post(url, "application/json", bytes.NewReader(jsonBody2))
		if err2 != nil {
			t.Logf("HTTP request failed (fallback): %v", err2)
			continue
		}
		respBody2, _ := ioutil.ReadAll(resp2.Body)
		resp2.Body.Close()
		t.Logf("HTTP status (fallback): %s", resp2.Status)
		t.Logf("HTTP response (fallback): %s", string(respBody2))
	}
}
