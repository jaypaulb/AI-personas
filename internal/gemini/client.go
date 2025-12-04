package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jaypaulb/AI-personas/internal/atom"
	"github.com/jaypaulb/AI-personas/internal/timing"
	"github.com/jaypaulb/AI-personas/internal/types"
	"github.com/joho/godotenv"
	"google.golang.org/genai"
)

// HTTP client timeout for OpenAI API calls
const openAIHTTPTimeout = 30 * time.Second

// Gemini API retry configuration
const (
	geminiMaxRetries     = 5
	geminiInitialBackoff = 1 * time.Second
	geminiMaxBackoff     = 32 * time.Second
)

// OpenAI API retry configuration
const (
	openAIMaxRetries     = 5
	openAIInitialBackoff = 1 * time.Second
	openAIMaxBackoff     = 32 * time.Second
)

// httpClientWithTimeout returns an HTTP client with configured timeout
var httpClientWithTimeout = &http.Client{Timeout: openAIHTTPTimeout}

// Persona is an alias to types.Persona for backward compatibility within this package
type Persona = types.Persona

type Client struct {
	genai *genai.Client
}

func NewClient(ctx context.Context) (*Client, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY not set in environment")
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, err
	}
	return &Client{genai: client}, nil
}

// isGeminiRateLimitError checks if an error from Gemini indicates rate limiting
func isGeminiRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// Check for common rate limit indicators in Gemini error messages
	return strings.Contains(errStr, "429") ||
		strings.Contains(errStr, "RESOURCE_EXHAUSTED") ||
		strings.Contains(errStr, "rate limit") ||
		strings.Contains(errStr, "quota exceeded") ||
		strings.Contains(errStr, "Too Many Requests")
}

// isGeminiRetryableError checks if an error from Gemini is transient and should be retried
func isGeminiRetryableError(err error) bool {
	if err == nil {
		return false
	}
	// Rate limit errors are retryable
	if isGeminiRateLimitError(err) {
		return true
	}
	errStr := err.Error()
	// Check for server errors (5xx)
	return strings.Contains(errStr, "500") ||
		strings.Contains(errStr, "502") ||
		strings.Contains(errStr, "503") ||
		strings.Contains(errStr, "504") ||
		strings.Contains(errStr, "INTERNAL") ||
		strings.Contains(errStr, "UNAVAILABLE")
}

// GeneratePersonas calls Gemini to generate 4 personas as a JSON array
func (c *Client) GeneratePersonas(ctx context.Context, businessContext string) ([]Persona, error) {
	prompt := `Given the following business model context, generate exactly 4 diverse personas as a JSON array. These personas should represent POTENTIAL CLIENTS from 4 DIFFERENT MARKET SECTORS who would be interested in the products/services described. They should NOT be employees of the company, but rather external customers, buyers, or decision-makers from different industries or market segments.

Each persona should have the following fields: name, role, description, background, goals, age, sex, race. The "goals" field should be an array of strings representing their key objectives related to the business context.

Respond ONLY with the JSON array, no extra text.

Business Context:
` + businessContext

	// Get model from environment variable, with sensible default
	model := os.Getenv("GEMINI_MODEL_PERSONAS")
	if model == "" {
		model = "gemini-2.5-flash" // Default to flash for persona generation
	}

	// Read temperature from env
	temp := 0.7
	if v := os.Getenv("LLM_TEMP"); v != "" {
		if f, err := strconv.ParseFloat(v, 32); err == nil {
			temp = f
		}
	}
	config := &genai.GenerateContentConfig{
		Temperature: genai.Ptr(float32(temp)),
	}

	// Start timing the Gemini API call
	timer := timing.Start("gemini_generate_personas")
	promptLen := len(prompt)

	var resp *genai.GenerateContentResponse
	var lastErr error

	// Retry loop with exponential backoff for rate limits
	for attempt := 1; attempt <= geminiMaxRetries; attempt++ {
		resp, lastErr = c.genai.Models.GenerateContent(ctx, model, []*genai.Content{{Parts: []*genai.Part{{Text: prompt}}}}, config)

		if lastErr != nil {
			// Fallback to gemini-2.5-flash-lite if model not found (only on first attempt)
			if attempt == 1 && (strings.Contains(lastErr.Error(), "not found") || strings.Contains(lastErr.Error(), "NOT_FOUND")) {
				log.Printf("[GeneratePersonas] Model %s not found, trying fallback gemini-2.5-flash-lite", model)
				model = "gemini-2.5-flash-lite"
				resp, lastErr = c.genai.Models.GenerateContent(ctx, model, []*genai.Content{{Parts: []*genai.Part{{Text: prompt}}}}, config)
			}
		}

		if lastErr == nil {
			// Success
			break
		}

		// Check if error is retryable
		if !isGeminiRetryableError(lastErr) {
			log.Printf("[GeneratePersonas] Non-retryable error: %v", lastErr)
			break
		}

		if attempt == geminiMaxRetries {
			log.Printf("[GeneratePersonas] All %d attempts failed, last error: %v", geminiMaxRetries, lastErr)
			break
		}

		// Calculate backoff with jitter
		backoff := atom.CalculateBackoff(attempt, geminiInitialBackoff, geminiMaxBackoff, 0.1)
		log.Printf("[GeneratePersonas] Attempt %d/%d failed (%v), retrying in %v", attempt, geminiMaxRetries, lastErr, backoff)
		time.Sleep(backoff)
	}

	if lastErr != nil {
		timing.LogOperationWithDetails(timer.Name(), timer.Duration(), false, fmt.Sprintf("model=%s prompt_len=%d", model, promptLen))
		timer.Stop()
		return nil, lastErr
	}

	timing.LogOperationWithDetails(timer.Name(), timer.Duration(), true, fmt.Sprintf("model=%s prompt_len=%d", model, promptLen))
	timer.Stop()

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("no response from Gemini")
	}
	jsonText := resp.Candidates[0].Content.Parts[0].Text

	// Strip Markdown code block if present
	jsonText = atom.StripMarkdownCodeBlock(jsonText)

	var personas []Persona
	if err := json.Unmarshal([]byte(jsonText), &personas); err != nil {
		return nil, fmt.Errorf("failed to parse Gemini JSON: %w\nRaw: %s", err, jsonText)
	}
	return personas, nil
}

// FormatPersonaNote formats a persona for a Canvus note
func FormatPersonaNote(p Persona) string {
	return atom.FormatPersonaNote(p)
}

// PersonaSession holds a chat session and persona info
// for multi-turn LLM conversations.
type PersonaSession struct {
	Persona *Persona
	Chat    *genai.Chat
}

// SessionManager manages chat sessions for each persona.
type SessionManager struct {
	sessions map[string]*PersonaSession
	client   *genai.Client
	mu       sync.Mutex // Add mutex for concurrent access
}

// NewSessionManager creates a new session manager.
func NewSessionManager(client *genai.Client) *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*PersonaSession),
		client:   client,
	}
}

// GenerateSystemPrompt returns a detailed system prompt for a persona
func GenerateSystemPrompt(persona Persona, businessContext string) string {
	return atom.GenerateSystemPrompt(persona, businessContext)
}

// GetOrCreateSession returns the session for a persona, creating it if needed.
func (sm *SessionManager) GetOrCreateSession(ctx context.Context, persona Persona, businessContext string) (*PersonaSession, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sess, ok := sm.sessions[persona.Name]; ok {
		return sess, nil
	}

	// Start timing session creation
	timer := timing.Start("gemini_create_session")

	// Read temperature from env
	temp := 0.7
	if v := os.Getenv("LLM_TEMP"); v != "" {
		if f, err := strconv.ParseFloat(v, 32); err == nil {
			temp = f
		}
	}
	config := &genai.GenerateContentConfig{
		Temperature: genai.Ptr(float32(temp)),
	}
	// Get model from environment variable for chat sessions
	model := os.Getenv("GEMINI_MODEL_CHAT")
	if model == "" {
		model = "gemini-2.5-flash" // Default to flash for chat sessions
	}

	var chat *genai.Chat
	var lastErr error

	// Retry loop with exponential backoff for rate limits
	for attempt := 1; attempt <= geminiMaxRetries; attempt++ {
		chat, lastErr = sm.client.Chats.Create(ctx, model, config, nil)

		if lastErr != nil {
			// Fallback to gemini-2.5-flash-lite if model not found (only on first attempt)
			if attempt == 1 && (strings.Contains(lastErr.Error(), "not found") || strings.Contains(lastErr.Error(), "NOT_FOUND")) {
				log.Printf("[GetOrCreateSession] Model %s not found, trying fallback gemini-2.5-flash-lite", model)
				model = "gemini-2.5-flash-lite"
				chat, lastErr = sm.client.Chats.Create(ctx, model, config, nil)
			}
		}

		if lastErr == nil {
			// Success
			break
		}

		// Check if error is retryable
		if !isGeminiRetryableError(lastErr) {
			log.Printf("[GetOrCreateSession] Non-retryable error: %v", lastErr)
			break
		}

		if attempt == geminiMaxRetries {
			log.Printf("[GetOrCreateSession] All %d attempts failed, last error: %v", geminiMaxRetries, lastErr)
			break
		}

		// Calculate backoff with jitter
		backoff := atom.CalculateBackoff(attempt, geminiInitialBackoff, geminiMaxBackoff, 0.1)
		log.Printf("[GetOrCreateSession] Attempt %d/%d failed (%v), retrying in %v", attempt, geminiMaxRetries, lastErr, backoff)
		time.Sleep(backoff)
	}

	if lastErr != nil {
		timing.LogOperationWithDetails(timer.Name(), timer.Duration(), false, fmt.Sprintf("model=%s persona=%s", model, persona.Name))
		timer.Stop()
		return nil, lastErr
	}

	// Inject system prompt as first message
	systemPrompt := GenerateSystemPrompt(persona, businessContext)
	promptLen := len(systemPrompt)
	_, _ = chat.Send(ctx, &genai.Part{Text: systemPrompt})

	timing.LogOperationWithDetails(timer.Name(), timer.Duration(), true, fmt.Sprintf("model=%s persona=%s prompt_len=%d", model, persona.Name, promptLen))
	timer.Stop()

	sess := &PersonaSession{
		Persona: &persona,
		Chat:    chat,
	}
	sm.sessions[persona.Name] = sess
	return sess, nil
}

// AnswerQuestion answers a question as a persona, maintaining chat history.
func (c *Client) AnswerQuestion(ctx context.Context, persona Persona, question string, sm *SessionManager, businessContext string) (string, error) {
	sess, err := sm.GetOrCreateSession(ctx, persona, businessContext)
	if err != nil {
		return "", err
	}

	// Start timing the answer generation
	timer := timing.Start("gemini_answer_question")
	promptLen := len(question)

	var resp *genai.GenerateContentResponse
	var lastErr error

	// Retry loop with exponential backoff for rate limits
	for attempt := 1; attempt <= geminiMaxRetries; attempt++ {
		resp, lastErr = sess.Chat.Send(ctx, &genai.Part{Text: question})

		if lastErr == nil {
			// Success
			break
		}

		// Check if error is retryable
		if !isGeminiRetryableError(lastErr) {
			log.Printf("[AnswerQuestion] Non-retryable error for persona %s: %v", persona.Name, lastErr)
			break
		}

		if attempt == geminiMaxRetries {
			log.Printf("[AnswerQuestion] All %d attempts failed for persona %s, last error: %v", geminiMaxRetries, persona.Name, lastErr)
			break
		}

		// Calculate backoff with jitter
		backoff := atom.CalculateBackoff(attempt, geminiInitialBackoff, geminiMaxBackoff, 0.1)
		log.Printf("[AnswerQuestion] Attempt %d/%d failed for persona %s (%v), retrying in %v", attempt, geminiMaxRetries, persona.Name, lastErr, backoff)
		time.Sleep(backoff)
	}

	if lastErr != nil {
		timing.LogOperationWithDetails(timer.Name(), timer.Duration(), false, fmt.Sprintf("persona=%s prompt_len=%d", persona.Name, promptLen))
		timer.Stop()
		return "", lastErr
	}

	timing.LogOperationWithDetails(timer.Name(), timer.Duration(), true, fmt.Sprintf("persona=%s prompt_len=%d", persona.Name, promptLen))
	timer.Stop()

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no response from Gemini")
	}
	return resp.Candidates[0].Content.Parts[0].Text, nil
}

// GeneratePersonaImage calls Imagen 3 to generate an avatar image for a persona
// NOTE: This model may incur costs depending on your API tier.
func (c *Client) GeneratePersonaImage(ctx context.Context, persona Persona) ([]byte, error) {
	prompt := fmt.Sprintf(
		"Generate a realistic professional headshot photo of a person for a business persona profile. Name: %s. Role: %s. Description: %s. Background: %s. Goals: %s. The image should be a portrait, neutral background, natural lighting, and suitable for a business context.",
		persona.Name, persona.Role, persona.Description, persona.Background, persona.Goals,
	)
	model := "models/imagen-3.0-generate-002"
	config := &genai.GenerateContentConfig{
		ResponseModalities: []string{"TEXT", "IMAGE"},
	}

	var resp *genai.GenerateContentResponse
	var lastErr error

	// Retry loop with exponential backoff for rate limits
	for attempt := 1; attempt <= geminiMaxRetries; attempt++ {
		resp, lastErr = c.genai.Models.GenerateContent(
			ctx,
			model,
			[]*genai.Content{{Parts: []*genai.Part{{Text: prompt}}}},
			config,
		)

		if lastErr == nil {
			// Success
			break
		}

		// Check if error is retryable
		if !isGeminiRetryableError(lastErr) {
			log.Printf("[GeneratePersonaImage] Non-retryable error: %v", lastErr)
			break
		}

		if attempt == geminiMaxRetries {
			log.Printf("[GeneratePersonaImage] All %d attempts failed, last error: %v", geminiMaxRetries, lastErr)
			break
		}

		// Calculate backoff with jitter
		backoff := atom.CalculateBackoff(attempt, geminiInitialBackoff, geminiMaxBackoff, 0.1)
		log.Printf("[GeneratePersonaImage] Attempt %d/%d failed (%v), retrying in %v", attempt, geminiMaxRetries, lastErr, backoff)
		time.Sleep(backoff)
	}

	if lastErr != nil {
		return nil, fmt.Errorf("Imagen image generation failed: %w", lastErr)
	}

	for _, part := range resp.Candidates[0].Content.Parts {
		if part.InlineData != nil && len(part.InlineData.Data) > 0 {
			return part.InlineData.Data, nil
		}
	}
	return nil, fmt.Errorf("No image data returned from Imagen.")
}

// GeneratePersonaImageOpenAI generates a persona image using OpenAI DALL-E
// Uses exponential backoff with jitter for retries on rate limits and server errors
func GeneratePersonaImageOpenAI(persona Persona) ([]byte, error) {
	_ = godotenv.Load("../.env") // Try parent dir for test, fallback to cwd
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY not set in environment or .env")
	}
	prompt := fmt.Sprintf("Business Appropriate Headshot of %s, a %s. %s, %s, %s. The headshot should be tightly cropped, centered on the face, with the full head visible and minimal chest.", persona.Name, persona.Role, persona.Age, persona.Sex, persona.Race)

	// Start timing the total DALL-E operation
	totalTimer := timing.Start("openai_dalle_total")

	url := "https://api.openai.com/v1/images/generations"
	body := map[string]interface{}{
		"prompt": prompt,
		"n":      1,
		"size":   "512x512",
	}
	jsonBody, _ := json.Marshal(body)
	var lastErr error

	for attempt := 1; attempt <= openAIMaxRetries; attempt++ {
		// Start timing this API call attempt
		apiTimer := timing.Start(fmt.Sprintf("openai_dalle_api_attempt_%d", attempt))

		req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
		if err != nil {
			apiTimer.StopAndLog(false)
			return nil, fmt.Errorf("Failed to create OpenAI request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClientWithTimeout.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("OpenAI HTTP request failed: %w", err)
			timing.LogOperationWithDetails(apiTimer.Name(), apiTimer.Duration(), false, fmt.Sprintf("error=http_request_failed attempt=%d", attempt))
			apiTimer.Stop()
			if attempt < openAIMaxRetries {
				backoff := atom.CalculateBackoff(attempt, openAIInitialBackoff, openAIMaxBackoff, 0.1)
				log.Printf("[OpenAI DALL-E] Attempt %d/%d: HTTP error, retrying in %v", attempt, openAIMaxRetries, backoff)
				time.Sleep(backoff)
				continue
			}
			break
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// Handle rate limiting (429)
		if resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("OpenAI API rate limit exceeded: %s", string(respBody))
			timing.LogOperationWithDetails(apiTimer.Name(), apiTimer.Duration(), false, fmt.Sprintf("status_code=%d attempt=%d", resp.StatusCode, attempt))
			apiTimer.Stop()
			if attempt < openAIMaxRetries {
				// Check for Retry-After header
				retryAfter := atom.ParseRetryAfter(resp)
				var backoff time.Duration
				if retryAfter > 0 {
					backoff = retryAfter
					log.Printf("[OpenAI DALL-E] Attempt %d/%d: Rate limited, Retry-After header suggests %v", attempt, openAIMaxRetries, backoff)
				} else {
					backoff = atom.CalculateBackoff(attempt, openAIInitialBackoff, openAIMaxBackoff, 0.1)
					log.Printf("[OpenAI DALL-E] Attempt %d/%d: Rate limited, retrying in %v", attempt, openAIMaxRetries, backoff)
				}
				time.Sleep(backoff)
				continue
			}
			break
		}

		// Handle server errors (5xx)
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			lastErr = fmt.Errorf("OpenAI API server error: %s", string(respBody))
			timing.LogOperationWithDetails(apiTimer.Name(), apiTimer.Duration(), false, fmt.Sprintf("status_code=%d attempt=%d", resp.StatusCode, attempt))
			apiTimer.Stop()
			if attempt < openAIMaxRetries {
				backoff := atom.CalculateBackoff(attempt, openAIInitialBackoff, openAIMaxBackoff, 0.1)
				log.Printf("[OpenAI DALL-E] Attempt %d/%d: Server error %d, retrying in %v", attempt, openAIMaxRetries, resp.StatusCode, backoff)
				time.Sleep(backoff)
				continue
			}
			break
		}

		// Handle other non-success status codes
		if resp.StatusCode != 200 {
			lastErr = fmt.Errorf("OpenAI API error: %s", string(respBody))
			timing.LogOperationWithDetails(apiTimer.Name(), apiTimer.Duration(), false, fmt.Sprintf("status_code=%d attempt=%d", resp.StatusCode, attempt))
			apiTimer.Stop()
			// Only retry on explicit 'server_error' type in response body
			if bytes.Contains(respBody, []byte("server_error")) && attempt < openAIMaxRetries {
				backoff := atom.CalculateBackoff(attempt, openAIInitialBackoff, openAIMaxBackoff, 0.1)
				log.Printf("[OpenAI DALL-E] Attempt %d/%d: server_error in response, retrying in %v", attempt, openAIMaxRetries, backoff)
				time.Sleep(backoff)
				continue
			}
			// Non-retryable error
			break
		}

		timing.LogOperationWithDetails(apiTimer.Name(), apiTimer.Duration(), true, fmt.Sprintf("status_code=%d attempt=%d", resp.StatusCode, attempt))
		apiTimer.Stop()

		var parsed struct {
			Data []struct {
				URL string `json:"url"`
			} `json:"data"`
		}
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			lastErr = fmt.Errorf("Failed to parse OpenAI response: %w", err)
			break
		}
		if len(parsed.Data) == 0 || parsed.Data[0].URL == "" {
			lastErr = fmt.Errorf("No image URL returned from OpenAI")
			break
		}

		// Start timing image download
		downloadTimer := timing.Start("openai_dalle_image_download")

		imgResp, err := httpClientWithTimeout.Get(parsed.Data[0].URL)
		if err != nil {
			timing.LogOperationWithDetails(downloadTimer.Name(), downloadTimer.Duration(), false, "error=download_failed")
			downloadTimer.Stop()
			lastErr = fmt.Errorf("Failed to download image: %w", err)
			break
		}
		defer imgResp.Body.Close()
		imgBytes, err := io.ReadAll(imgResp.Body)
		if err != nil {
			timing.LogOperationWithDetails(downloadTimer.Name(), downloadTimer.Duration(), false, "error=read_failed")
			downloadTimer.Stop()
			lastErr = fmt.Errorf("Failed to read image data: %w", err)
			break
		}

		timing.LogOperationWithDetails(downloadTimer.Name(), downloadTimer.Duration(), true, fmt.Sprintf("size_bytes=%d", len(imgBytes)))
		downloadTimer.Stop()

		timing.LogOperationWithDetails(totalTimer.Name(), totalTimer.Duration(), true, fmt.Sprintf("attempts=%d", attempt))
		totalTimer.Stop()

		return imgBytes, nil
	}

	timing.LogOperationWithDetails(totalTimer.Name(), totalTimer.Duration(), false, fmt.Sprintf("error=max_retries_exceeded attempts=%d", openAIMaxRetries))
	totalTimer.Stop()

	return nil, lastErr
}

func (c *Client) GenaiClient() *genai.Client {
	return c.genai
}
