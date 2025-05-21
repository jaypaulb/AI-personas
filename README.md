# AI Personas

A GoLang application that integrates with the Canvus API (MCS) and Google Gemini GenAI to automate persona-driven focus group analysis and Q&A workflows on a business canvas.

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