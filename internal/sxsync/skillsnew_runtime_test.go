package sxsync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCreateSkillsNewBotRuntimeToken(t *testing.T) {
	expiresAt := "2026-05-27T12:00:00Z"
	var operations []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer mgmt-token" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var req struct {
			OperationName string         `json:"operationName"`
			Variables     map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		operations = append(operations, req.OperationName)
		w.Header().Set("Content-Type", "application/json")
		switch req.OperationName {
		case "ListBots":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"bots": []any{
					map[string]any{"id": "bot-1", "name": "Reviewer", "slug": "reviewer", "description": "Reviews pull requests."},
				},
			}})
		case "CreateBotRuntimeToken":
			if got, _ := req.Variables["botId"].(string); got != "bot-1" {
				t.Errorf("botId = %q, want bot-1", got)
			}
			if got, _ := req.Variables["label"].(string); got != "Hetchy runtime: Reviewer" {
				t.Errorf("label = %q, want runtime label", got)
			}
			if got, _ := req.Variables["ttlSeconds"].(float64); int(got) != skillsNewBotRuntimeTokenTTLSeconds {
				t.Errorf("ttlSeconds = %v, want %d", got, skillsNewBotRuntimeTokenTTLSeconds)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"createBotRuntimeToken": map[string]any{
					"botKey":    "runtime-token",
					"expiresAt": expiresAt,
				},
			}})
		default:
			t.Fatalf("unexpected operation %q", req.OperationName)
		}
	}))
	defer srv.Close()

	got, err := createSkillsNewBotRuntimeToken(context.Background(), srv.URL, "mgmt-token", "reviewer", "Hetchy runtime: Reviewer", skillsNewBotRuntimeTokenTTLSeconds)
	if err != nil {
		t.Fatalf("createSkillsNewBotRuntimeToken: %v", err)
	}
	if got.Token != "runtime-token" {
		t.Fatalf("token = %q, want runtime-token", got.Token)
	}
	wantExpiresAt, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ExpiresAt.Equal(wantExpiresAt) {
		t.Fatalf("expiresAt = %s, want %s", got.ExpiresAt, wantExpiresAt)
	}
	if len(operations) != 2 || operations[0] != "ListBots" || operations[1] != "CreateBotRuntimeToken" {
		t.Fatalf("operations = %v, want ListBots then CreateBotRuntimeToken", operations)
	}
}
