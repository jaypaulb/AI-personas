package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

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
	resp, err := c.genai.Models.GenerateContent(ctx, model, []*genai.Content{{Parts: []*genai.Part{{Text: prompt}}}}, nil)
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

// AnswerQuestion answers a question as a persona (placeholder)
func (c *Client) AnswerQuestion(ctx context.Context, persona string, question string) (string, error) {
	// TODO: Implement Q&A using Gemini
	return "", nil
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
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("Failed to create OpenAI request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OpenAI HTTP request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("OpenAI API error: %s", string(respBody))
	}
	var parsed struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("Failed to parse OpenAI response: %w", err)
	}
	if len(parsed.Data) == 0 || parsed.Data[0].URL == "" {
		return nil, fmt.Errorf("No image URL returned from OpenAI")
	}
	imgResp, err := http.Get(parsed.Data[0].URL)
	if err != nil {
		return nil, fmt.Errorf("Failed to download image: %w", err)
	}
	defer imgResp.Body.Close()
	imgBytes, err := io.ReadAll(imgResp.Body)
	if err != nil {
		return nil, fmt.Errorf("Failed to read image data: %w", err)
	}
	return imgBytes, nil
}

func (c *Client) GenaiClient() *genai.Client {
	return c.genai
}
