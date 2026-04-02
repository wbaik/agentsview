package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wesm/agentsview/internal/importer"
)

// parseSSEDone extracts the "done" event data from an SSE
// response body and decodes it into the target.
func parseSSEDone(t *testing.T, body string, v any) {
	t.Helper()
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			data := line[len("data: "):]
			// The last data line before EOF with event:done
			// is the final stats. Try to decode each data
			// line; keep the last successful decode.
			if err := json.Unmarshal(
				[]byte(data), v,
			); err == nil {
				continue
			}
		}
	}
	// Re-parse: find event:done specifically.
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if line == "event: done" && i+1 < len(lines) {
			data := strings.TrimPrefix(lines[i+1], "data: ")
			if err := json.Unmarshal(
				[]byte(data), v,
			); err != nil {
				t.Fatalf(
					"decoding SSE done event: %v\nbody: %s",
					err, body,
				)
			}
			return
		}
	}
	t.Fatalf("no 'done' event in SSE response:\n%s", body)
}

func TestHandleImportClaudeAI(t *testing.T) {
	srv := testServer(t, 5*time.Second)

	conversations := `[
      {
        "uuid": "api-test-001",
        "name": "API Test",
        "summary": "",
        "created_at": "2026-03-01T10:00:00.000000Z",
        "updated_at": "2026-03-01T10:05:00.000000Z",
        "account": {"uuid": "acct-1"},
        "chat_messages": [
          {
            "uuid": "m1",
            "text": "Test message",
            "content": [{"type":"text","text":"Test message"}],
            "sender": "human",
            "created_at": "2026-03-01T10:00:00.000000Z",
            "updated_at": "2026-03-01T10:00:00.000000Z",
            "attachments": [],
            "files": []
          }
        ]
      }
    ]`

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "conversations.json")
	if err != nil {
		t.Fatalf("creating form file: %v", err)
	}
	_, _ = part.Write([]byte(conversations))
	writer.Close()

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/import/claude-ai",
		&body,
	)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"status = %d, want 200: %s",
			rec.Code, rec.Body.String(),
		)
	}

	var stats importer.ImportStats
	ct := rec.Header().Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		parseSSEDone(t, rec.Body.String(), &stats)
	} else if err := json.NewDecoder(rec.Body).Decode(&stats); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if stats.Imported != 1 {
		t.Errorf("imported = %d, want 1", stats.Imported)
	}
	if stats.Updated != 0 {
		t.Errorf("updated = %d, want 0", stats.Updated)
	}
}

func TestHandleImportChatGPT_RequiresZip(t *testing.T) {
	srv := testServer(t, 5*time.Second)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "data.json")
	if err != nil {
		t.Fatalf("creating form file: %v", err)
	}
	_, _ = part.Write([]byte("[]"))
	writer.Close()

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/import/chatgpt",
		&body,
	)
	req.Header.Set(
		"Content-Type", writer.FormDataContentType(),
	)

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf(
			"status = %d, want 400: %s",
			rec.Code, rec.Body.String(),
		)
	}
}

func TestHandleImportClaudeAI_NoFile(t *testing.T) {
	srv := testServer(t, 5*time.Second)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	writer.Close()

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/import/claude-ai",
		&body,
	)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf(
			"status = %d, want 400: %s",
			rec.Code, rec.Body.String(),
		)
	}
}
