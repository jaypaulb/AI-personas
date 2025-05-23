# AI-Personas

This project implements a robust persona-driven Q&A workflow for Canvus canvases, integrating Google Gemini GenAI and OpenAI. Key features:

- **Persona System Prompt:** Each persona LLM session is initialized with a detailed system prompt including business context and conversational instructions.
- **Color Mapping:** All notes from a persona use a consistent color.
- **Grid Layout:** Persona answers and meta answers are placed in a 3x3 grid around the user question, matching the intended UI.
- **Connector Logic:** Connectors are created after all notes, linking question → answer and answer → meta answer.
- **Overlap Handling:** The system automatically moves or scales notes to avoid overlap. If space cannot be found, the user is prompted to move the note.
- **Succinct LLM Responses:** If a response is too long, the LLM is re-prompted for a more succinct, verbal answer.
- **Robustness:** The workflow is robust to grid/canvas issues and prevents negative WaitGroup panics.

## Usage

1. Set up your `.env` with the required API keys.
2. Run the main application. The system will monitor for new questions and generate persona-driven answers and meta answers in a visually organized, robust manner.

See `task_list` for completed features and next steps.

## Features
- Monitors a Canvus canvas for widget triggers (image/note creation)
- Extracts business model data from notes
- Uses Gemini GenAI to generate diverse personas and simulate focus group sessions
- Persists persona sessions for ongoing Q&A
- Visualizes persona responses and meta-responses on the canvas

## Setup
1. Clone the repository:
   ```
   git clone https://github.com/jaypaulb/AI-personas.git
   cd AI-personas
   ```
2. Copy and edit the `.env` file with your Canvus API and canvas details.
3. Build the project:
   ```
   go build ./cmd/...
   ```
4. Run the application:
   ```
   go run ./cmd/...
   ```

## Configuration
Set the following environment variables in your `.env` file:
- `CANVUS_API_KEY` - Private token for MCS authentication
- `CANVUS_SERVER` - MCS server URL
- `CANVAS_ID` - Target canvas ID

## Contributing
Pull requests are welcome. For major changes, please open an issue first to discuss what you would like to change.

## License
MIT 