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
	"time"

	"github.com/joho/godotenv"
	"google.golang.org/genai"
)

type AgeString string

func (a *AgeString) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*a = AgeString(s)
		return nil
	}
	var n float64
	if err := json.Unmarshal(data, &n); err == nil {
		*a = AgeString(fmt.Sprintf("%d", int(n)))
		return nil
	}
	return fmt.Errorf("AgeString: cannot unmarshal %s", string(data))
}

type Persona struct {
	Name        string    `json:"name"`
	Role        string    `json:"role"`
	Description string    `json:"description"`
	Background  string    `json:"background"`
	Goals       string    `json:"goals"`
	Age         AgeString `json:"age"`
	Sex         string    `json:"sex"`
	Race        string    `json:"race"`
}

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

// GeneratePersonas calls Gemini to generate 4 personas as a JSON array
func (c *Client) GeneratePersonas(ctx context.Context, businessContext string) ([]Persona, error) {
	prompt := `Given the following business model context, generate exactly 4 diverse personas as a JSON array. Each persona should have the following fields: name, role, description, background, goals, age, sex, race. Respond ONLY with the JSON array, no extra text.

Business Context:
` + businessContext

	model := "gemini-1.5-flash" // or your preferred model

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

	resp, err := c.genai.Models.GenerateContent(ctx, model, []*genai.Content{{Parts: []*genai.Part{{Text: prompt}}}}, config)
	if err != nil {
		return nil, err
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("no response from Gemini")
	}
	jsonText := resp.Candidates[0].Content.Parts[0].Text

	// Strip Markdown code block if present
	jsonText = strings.TrimSpace(jsonText)
	if strings.HasPrefix(jsonText, "```") {
		jsonText = strings.TrimPrefix(jsonText, "```json")
		jsonText = strings.TrimPrefix(jsonText, "```")
		jsonText = strings.TrimSuffix(jsonText, "```")
		jsonText = strings.TrimSpace(jsonText)
	}

	var personas []Persona
	if err := json.Unmarshal([]byte(jsonText), &personas); err != nil {
		return nil, fmt.Errorf("failed to parse Gemini JSON: %w\nRaw: %s", err, jsonText)
	}
	return personas, nil
}

// FormatPersonaNote formats a persona for a Canvus note
func FormatPersonaNote(p Persona) string {
	return fmt.Sprintf(
		"🧑 Name: %s\n\n💼 Role: %s\n\n📝 Description: %s\n\n🏫 Background: %s\n\n🎯 Goals: %s\n\n🎂 Age: %s\n\n⚧ Sex: %s\n\n🌍 Race: %s",
		p.Name, p.Role, p.Description, p.Background, p.Goals, string(p.Age), p.Sex, p.Race,
	)
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
	return fmt.Sprintf(`Assume the role of the following persona for a business focus group. You are a client or potential client of the business. You are in a general purpose focus group for the business. Here is the business outline:

%s

Persona:
Name: %s
Role: %s
Description: %s
Background: %s
Goals: %s
Age: %s
Sex: %s
Race: %s

When asked a question or provided with some info, you must only respond as the persona assigned and in the voice of that persona. Your responses should be short and sweet and structured as if given verbally. You should not repeat the question or reiterate points from the question as this would not be natural for a conversational style interaction verbally. Do not start your answer by restating the question. Do not use phrases like 'As a persona...' or 'If I were...'. Just answer as if you are the person.`,
		businessContext,
		persona.Name,
		persona.Role,
		persona.Description,
		persona.Background,
		persona.Goals,
		persona.Age,
		persona.Sex,
		persona.Race,
	)
}

// GetOrCreateSession returns the session for a persona, creating it if needed.
func (sm *SessionManager) GetOrCreateSession(ctx context.Context, persona Persona, businessContext string) (*PersonaSession, error) {
	if sess, ok := sm.sessions[persona.Name]; ok {
		return sess, nil
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
	chat, err := sm.client.Chats.Create(ctx, "gemini-1.5-flash", config, nil)
	if err != nil {
		return nil, err
	}
	// Inject system prompt as first message
	systemPrompt := GenerateSystemPrompt(persona, businessContext)
	_, _ = chat.Send(ctx, &genai.Part{Text: systemPrompt})
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
	resp, err := sess.Chat.Send(ctx, &genai.Part{Text: question})
	if err != nil {
		return "", err
	}
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
	resp, err := c.genai.Models.GenerateContent(
		ctx,
		model,
		[]*genai.Content{{Parts: []*genai.Part{{Text: prompt}}}},
		config,
	)
	if err != nil {
		return nil, fmt.Errorf("Imagen image generation failed: %w", err)
	}
	for _, part := range resp.Candidates[0].Content.Parts {
		if part.InlineData != nil && len(part.InlineData.Data) > 0 {
			return part.InlineData.Data, nil
		}
	}
	return nil, fmt.Errorf("No image data returned from Imagen.")
}

// GeneratePersonaImageOpenAI generates a persona image using OpenAI DALL·E
func GeneratePersonaImageOpenAI(persona Persona) ([]byte, error) {
	_ = godotenv.Load("../.env") // Try parent dir for test, fallback to cwd
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY not set in environment or .env")
	}
	prompt := fmt.Sprintf("Business Appropriate Headshot of %s, a %s. %s, %s, %s. The headshot should be tightly cropped, centered on the face, with the full head visible and minimal chest.", persona.Name, persona.Role, persona.Age, persona.Sex, persona.Race)
	url := "https://api.openai.com/v1/images/generations"
	body := map[string]interface{}{
		"prompt": prompt,
		"n":      1,
		"size":   "512x512",
	}
	jsonBody, _ := json.Marshal(body)
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
		if err != nil {
			return nil, fmt.Errorf("Failed to create OpenAI request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("OpenAI HTTP request failed: %w", err)
			log.Printf("[warn] OpenAI image gen attempt %d failed: %v", attempt, lastErr)
			if attempt < 3 {
				time.Sleep(2 * time.Second)
				continue
			}
			break
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			lastErr = fmt.Errorf("OpenAI API server error: %s", string(respBody))
			log.Printf("[warn] OpenAI image gen attempt %d got server error: %s", attempt, string(respBody))
			if attempt < 3 {
				time.Sleep(2 * time.Second)
				continue
			}
			break
		}
		if resp.StatusCode != 200 {
			lastErr = fmt.Errorf("OpenAI API error: %s", string(respBody))
			// Only retry on explicit 'server_error' type
			if bytes.Contains(respBody, []byte("server_error")) && attempt < 3 {
				log.Printf("[warn] OpenAI image gen attempt %d got server_error, retrying", attempt)
				time.Sleep(2 * time.Second)
				continue
			}
			break
		}
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
		imgResp, err := http.Get(parsed.Data[0].URL)
		if err != nil {
			lastErr = fmt.Errorf("Failed to download image: %w", err)
			break
		}
		defer imgResp.Body.Close()
		imgBytes, err := io.ReadAll(imgResp.Body)
		if err != nil {
			lastErr = fmt.Errorf("Failed to read image data: %w", err)
			break
		}
		return imgBytes, nil
	}
	return nil, lastErr
}

func (c *Client) GenaiClient() *genai.Client {
	return c.genai
}
