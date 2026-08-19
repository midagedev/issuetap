package api

import (
	"encoding/json"
	"net/http"
)

// jiraError is the Cloud/DC error envelope gadak parses
// (errorMessages + errors).
type jiraError struct {
	ErrorMessages []string          `json:"errorMessages"`
	Errors        map[string]string `json:"errors"`
	Issuetap      *issuetapMeta     `json:"issuetap,omitempty"`
}

type issuetapMeta struct {
	Code   string `json:"code"`
	Method string `json:"method"`
	Path   string `json:"path"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeWikiError is the Confluence v1 envelope used by getPage / CQL
// (statusCode + message), not the Jira errorMessages shape.
func writeWikiError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"statusCode": status,
		"message":    message,
	})
}

func writeJiraError(w http.ResponseWriter, status int, messages ...string) {
	if len(messages) == 0 {
		messages = []string{http.StatusText(status)}
	}
	writeJSON(w, status, jiraError{
		ErrorMessages: messages,
		Errors:        map[string]string{},
	})
}

// writeJiraFieldErrors is Jira's per-field 400 (errors.<id> = reason).
func writeJiraFieldErrors(w http.ResponseWriter, fields map[string]string) {
	writeJSON(w, http.StatusBadRequest, jiraError{
		ErrorMessages: []string{},
		Errors:        fields,
	})
}

// writeUnsupported is the honest coverage-gap response. A 404 would look
// like "your client is broken"; a plausible lie would look like "the
// endpoint works". Consumers key on issuetap.code == "unsupported_endpoint".
func writeUnsupported(w http.ResponseWriter, method, path string) {
	writeJSON(w, http.StatusNotImplemented, jiraError{
		ErrorMessages: []string{"issuetap does not implement " + method + " " + path},
		Errors:        map[string]string{"endpoint": "unsupported_endpoint"},
		Issuetap:      &issuetapMeta{Code: "unsupported_endpoint", Method: method, Path: path},
	})
}

func writeAuth(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="issuetap"`)
	writeJiraError(w, http.StatusUnauthorized, "Client must be authenticated to access this resource.")
}
