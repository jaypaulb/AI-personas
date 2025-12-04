package atom

import (
	"fmt"
	"regexp"

	"github.com/jaypaulb/AI-personas/internal/types"
)

// FormatPersonaNote formats a persona for display in a Canvus note
func FormatPersonaNote(p types.Persona) string {
	return fmt.Sprintf(
		"🧑 Name: %s\n\n💼 Role: %s\n\n📝 Description: %s\n\n🏫 Background: %s\n\n🎯 Goals: %s\n\n🎂 Age: %s\n\n⚧ Sex: %s\n\n🌍 Race: %s",
		p.Name, p.Role, p.Description, p.Background, string(p.Goals), string(p.Age), p.Sex, p.Race,
	)
}

// ParsePersonaNote parses a persona note text into a Persona struct
func ParsePersonaNote(text string) types.Persona {
	p := types.Persona{}
	// Use regex to extract fields
	re := regexp.MustCompile(`(?m)^🧑 Name: (.*)[\s\S]*^💼 Role: (.*)[\s\S]*^📝 Description: (.*)[\s\S]*^🏫 Background: (.*)[\s\S]*^🎯 Goals: (.*)[\s\S]*^🎂 Age: (.*)[\s\S]*^⚧ Sex: (.*)[\s\S]*^🌍 Race: (.*)$`)
	matches := re.FindStringSubmatch(text)
	if len(matches) == 9 {
		p.Name = matches[1]
		p.Role = matches[2]
		p.Description = matches[3]
		p.Background = matches[4]
		p.Goals = types.GoalsString(matches[5])
		p.Age = types.AgeString(matches[6])
		p.Sex = matches[7]
		p.Race = matches[8]
	}
	return p
}

// GenerateSystemPrompt returns a detailed system prompt for a persona in a focus group
func GenerateSystemPrompt(persona types.Persona, businessContext string) string {
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
