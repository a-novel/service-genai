package servicegenai

import (
	"encoding/json"
	"errors"
	"strings"
)

var (
	// ErrResponsesOutputEmpty is returned when no Responses API result was stored.
	ErrResponsesOutputEmpty = errors.New("responses output is empty")
	// ErrResponsesOutputMalformed is returned when the stored result is not valid JSON.
	ErrResponsesOutputMalformed = errors.New("responses output is malformed")
	// ErrResponsesOutputTextMissing is returned when the result contains no generated text.
	ErrResponsesOutputTextMissing = errors.New("responses output contains no text")
	// ErrResponsesRefused is returned when the result contains a model refusal.
	ErrResponsesRefused = errors.New("responses output was refused")
)

// ExtractResponsesOutputText returns the generated text stored in an OpenAI Responses API result.
// A refusal takes precedence over text, and its explanation is never returned.
func ExtractResponsesOutputText(output []byte) (string, error) {
	if len(output) == 0 {
		return "", ErrResponsesOutputEmpty
	}

	var response struct {
		// OpenAI owns this snake_case field.
		//nolint:tagliatelle
		OutputText string `json:"output_text"`
		Output     []struct {
			Content []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		} `json:"output"`
	}

	err := json.Unmarshal(output, &response)
	if err != nil {
		return "", ErrResponsesOutputMalformed
	}

	var text strings.Builder

	for _, item := range response.Output {
		for _, content := range item.Content {
			switch content.Type {
			case "output_text":
				text.WriteString(content.Text)
			case "refusal":
				return "", ErrResponsesRefused
			}
		}
	}

	if response.OutputText != "" {
		return response.OutputText, nil
	}

	if text.Len() == 0 {
		return "", ErrResponsesOutputTextMissing
	}

	return text.String(), nil
}
