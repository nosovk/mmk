package mattermost

import (
	"fmt"
	"net/http"
)

// APIError describes a non-successful response from the Mattermost API.
type APIError struct {
	StatusCode    int    `json:"-"`
	ID            string `json:"id"`
	Message       string `json:"message"`
	RequestID     string `json:"request_id"`
	DetailedError string `json:"detailed_error"`
}

func (e *APIError) Error() string {
	message := e.Message
	if message == "" {
		message = http.StatusText(e.StatusCode)
	}
	if message == "" {
		message = "Mattermost API request failed"
	}
	if e.ID != "" {
		return fmt.Sprintf("Mattermost API error %d (%s): %s", e.StatusCode, e.ID, message)
	}
	return fmt.Sprintf("Mattermost API error %d: %s", e.StatusCode, message)
}
