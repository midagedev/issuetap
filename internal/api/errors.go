package api

import (
	"encoding/json"
	"net/http"

	"github.com/midagedev/issuetap/internal/store"
)

// jiraError is the Cloud/DC error envelope gadak parses
// (errorMessages + errors).
type jiraError struct {
	ErrorMessages []string          `json:"errorMessages"`
	Errors        map[string]string `json:"errors"`
	Issuetap      *issuetapMeta     `json:"issuetap,omitempty"`
}

type issuetapMeta struct {
	Code    string `json:"code"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Suggest string `json:"suggest,omitempty"`
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
// When Inventory has exactly one conservative sibling, the message names
// it and issuetap.suggest carries the same "METHOD /path" string; otherwise
// the envelope is the historical one-line form.
func writeUnsupported(w http.ResponseWriter, method, path string) {
	msg := "issuetap does not implement " + method + " " + path
	meta := &issuetapMeta{Code: "unsupported_endpoint", Method: method, Path: path}
	if sug := SuggestImplemented(method, path); sug != "" {
		msg += "; did you mean " + sug + "?"
		meta.Suggest = sug
	}
	writeJSON(w, http.StatusNotImplemented, jiraError{
		ErrorMessages: []string{msg},
		Errors:        map[string]string{"endpoint": "unsupported_endpoint"},
		Issuetap:      meta,
	})
}

func writeAuth(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="issuetap"`)
	writeJiraError(w, http.StatusUnauthorized, "Client must be authenticated to access this resource.")
}

// writeJiraWriteError is the fallback for store write errors after
// not-found / field-error have been checked. Durable persist failures
// are 500; other rejections stay 400.
func writeJiraWriteError(w http.ResponseWriter, err error) {
	if store.IsPersist(err) {
		writeJiraError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJiraError(w, http.StatusBadRequest, err.Error())
}

// writeWikiWriteError is the Confluence equivalent of writeJiraWriteError.
func writeWikiWriteError(w http.ResponseWriter, err error) {
	if store.IsPersist(err) {
		writeWikiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeWikiError(w, http.StatusBadRequest, err.Error())
}

// writeJSONWriteError is the lab /api envelope for store write errors.
func writeJSONWriteError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if store.IsPersist(err) {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, map[string]any{"error": err.Error()})
}
