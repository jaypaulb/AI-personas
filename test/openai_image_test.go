package test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"testing"

	"github.com/joho/godotenv"
)

func TestOpenAIImageGeneration(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Logf("Current working directory: %s", cwd)
	if _, err := os.Stat("../.env"); err != nil {
		t.Logf(".env file not found in parent dir: %v", err)
	} else {
		t.Logf(".env file found in parent dir")
	}
	_ = godotenv.Load("../.env")
	// Debug: print .env contents
	if envBytes, err := ioutil.ReadFile("../.env"); err == nil {
		t.Logf(".env contents:\n%s", string(envBytes))
	} else {
		t.Logf("Could not read .env: %v", err)
	}
	apiKey := os.Getenv("OPENAI_API_KEY")
	// Debug: print what we see
	if apiKey == "" {
		t.Logf("os.Getenv(OPENAI_API_KEY) is empty!")
	} else {
		t.Logf("os.Getenv(OPENAI_API_KEY) is: %s", apiKey[:5]+"... (redacted)")
	}
	if apiKey == "" {
		t.Fatal("OPENAI_API_KEY not set in environment or .env")
	}
	prompt := "A photorealistic professional headshot of John Smith, VP of Sales & Marketing, Sports Team, responsible for driving revenue and enhancing fan engagement. Neutral background, natural lighting, business attire."
	url := "https://api.openai.com/v1/images/generations"
	body := map[string]interface{}{
		"prompt": prompt,
		"n":      1,
		"size":   "512x512",
	}
	jsonBody, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(context.Background(), "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	respBody, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	t.Logf("HTTP status: %s", resp.Status)
	t.Logf("HTTP response: %s", string(respBody))
	if resp.StatusCode != 200 {
		t.Fatalf("OpenAI API error: %s", string(respBody))
	}
	var parsed struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		t.Fatalf("Failed to parse OpenAI response: %v", err)
	}
	if len(parsed.Data) == 0 || parsed.Data[0].URL == "" {
		t.Fatalf("No image URL returned from OpenAI")
	}
	imgResp, err := http.Get(parsed.Data[0].URL)
	if err != nil {
		t.Fatalf("Failed to download image: %v", err)
	}
	defer imgResp.Body.Close()
	imgBytes, err := io.ReadAll(imgResp.Body)
	if err != nil {
		t.Fatalf("Failed to read image data: %v", err)
	}
	if err := ioutil.WriteFile("test_persona_openai.png", imgBytes, 0644); err != nil {
		t.Fatalf("Failed to save image: %v", err)
	}
	t.Logf("Saved image to test_persona_openai.png")
}
