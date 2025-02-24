package openaiclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
)

const (
	defaultChatModel = "gpt-3.5-turbo"
	defaultBaseURL   = "https://api.openai.com"
)

// ChatRequest is a request to create an embedding.
type ChatRequest struct {
	Model            string         `json:"model"`
	Messages         []*ChatMessage `json:"messages"`
	Temperature      float64        `json:"temperature,omitempty"`
	TopP             float64        `json:"top_p,omitempty"`
	MaxTokens        int            `json:"max_tokens,omitempty"`
	N                int            `json:"n,omitempty"`
	StopWords        []string       `json:"stop,omitempty"`
	Stream           bool           `json:"stream,omitempty"`
	FrequencyPenalty float64        `json:"frequency_penalty,omitempty"`
	PresencePenalty  float64        `json:"presence_penalty,omitempty"`

	// StreamingFunc is a function to be called for each chunk of a streaming response.
	// Return an error to stop streaming early.
	StreamingFunc func(ctx context.Context, chunk []byte) error `json:"-"`
}

// ChatMessage is a message in a chat request.
type ChatMessage struct {
	// The role of the author of this message. One of system, user, or assistant.
	Role string `json:"role"`
	// The content of the message.
	Content string `json:"content"`
	// The name of the author of this message. May contain a-z, A-Z, 0-9, and underscores,
	// with a maximum length of 64 characters.
	Name string `json:"name,omitempty"`
}

// ChatChoice is a choice in a chat response.
type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// ChatUsage is the usage of a chat completion request.
type ChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResponse is a response to a chat request.
type ChatResponse struct {
	ID      string        `json:"id,omitempty"`
	Created float64       `json:"created,omitempty"`
	Choices []*ChatChoice `json:"choices,omitempty"`
	Model   string        `json:"model,omitempty"`
	Object  string        `json:"object,omitempty"`
	Usage   struct {
		CompletionTokens float64 `json:"completion_tokens,omitempty"`
		PromptTokens     float64 `json:"prompt_tokens,omitempty"`
		TotalTokens      float64 `json:"total_tokens,omitempty"`
	} `json:"usage,omitempty"`
}

// StreamedChatRkesponsePayload is a chunk from the stream.
type StreamedChatRkesponsePayload struct {
	ID      string  `json:"id,omitempty"`
	Created float64 `json:"created,omitempty"`
	Model   string  `json:"model,omitempty"`
	Object  string  `json:"object,omitempty"`
	Choices []struct {
		Index float64 `json:"index,omitempty"`
		Delta struct {
			Role    string `json:"role,omitempty"`
			Content string `json:"content,omitempty"`
		} `json:"delta,omitempty"`
		FinishReason interface{} `json:"finish_reason,omitempty"`
	} `json:"choices,omitempty"`
}

func (c *Client) createChat(ctx context.Context, payload *ChatRequest) (*ChatResponse, error) {
	if payload.StreamingFunc != nil {
		payload.Stream = true
	}
	// Build request payload
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	// Build request
	body := bytes.NewReader(payloadBytes)
	if c.baseURL == "" {
		c.baseURL = defaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/v1/chat/completions", c.baseURL), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if c.organization != "" {
		req.Header.Set("OpenAI-Organization", c.organization)
	}

	// Send request
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()

	if r.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("API returned unexpected status code: %d", r.StatusCode)

		// No need to check the error here: if it fails, we'll just return the
		// status code.
		var errResp errorMessage
		if err := json.NewDecoder(r.Body).Decode(&errResp); err != nil {
			return nil, errors.New(msg) // nolint:goerr113
		}

		return nil, fmt.Errorf("%s: %s", msg, errResp.Error.Message) // nolint:goerr113
	}
	if payload.StreamingFunc != nil {
		return parseStreamingChatResponse(ctx, r, payload)
	}
	// Parse response
	var response ChatResponse
	return &response, json.NewDecoder(r.Body).Decode(&response)
}

func parseStreamingChatResponse(ctx context.Context, r *http.Response, payload *ChatRequest) (*ChatResponse, error) {
	scanner := bufio.NewScanner(r.Body)
	responseChan := make(chan StreamedChatRkesponsePayload)
	go func() {
		defer close(responseChan)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "data:") {
				log.Fatalf("unexpected line: %v", line)
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				return
			}
			var streamPayload StreamedChatRkesponsePayload
			err := json.NewDecoder(bytes.NewReader([]byte(data))).Decode(&streamPayload)
			if err != nil {
				log.Fatalf("failed to decode stream payload: %v", err)
			}
			responseChan <- streamPayload
		}
		if err := scanner.Err(); err != nil {
			log.Println("issue scanning response:", err)
		}
	}()
	// Parse response
	response := ChatResponse{
		Choices: []*ChatChoice{
			{},
		},
	}

	for streamResponse := range responseChan {
		if payload.StreamingFunc != nil {
			response.Choices[0].Message.Content += streamResponse.Choices[0].Delta.Content

			err := payload.StreamingFunc(ctx, []byte(streamResponse.Choices[0].Delta.Content))
			if err != nil {
				return nil, fmt.Errorf("streaming func returned an error: %w", err)
			}
		}
	}
	return &response, nil
}
