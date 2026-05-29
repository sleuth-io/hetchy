package sxsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	skillsNewBotIDQuery = `query HetchyBotID($slug: String!) {
  bot(slug: $slug) {
    id
  }
}`
	skillsNewDeleteBotMutation = `mutation HetchyDeleteBot($id: ID!) {
  deleteBot(id: $id) {
    errors {
      field
      messages
    }
  }
}`
)

type skillsNewGraphQLError struct {
	Message string `json:"message"`
}

type skillsNewMutationError struct {
	Field    string   `json:"field"`
	Messages []string `json:"messages"`
}

func deleteSkillsNewBot(ctx context.Context, serverURL, authToken string, slugCandidates []string) error {
	for _, slug := range slugCandidates {
		id, err := skillsNewBotID(ctx, serverURL, authToken, slug)
		if err != nil {
			return err
		}
		if id == "" {
			continue
		}
		return deleteSkillsNewBotID(ctx, serverURL, authToken, id)
	}
	return nil
}

func skillsNewBotID(ctx context.Context, serverURL, authToken, slug string) (string, error) {
	var out struct {
		Bot *struct {
			ID string `json:"id"`
		} `json:"bot"`
	}
	if err := skillsNewGraphQL(ctx, serverURL, authToken, "HetchyBotID", skillsNewBotIDQuery, map[string]any{"slug": slug}, &out); err != nil {
		return "", err
	}
	if out.Bot == nil {
		return "", nil
	}
	return strings.TrimSpace(out.Bot.ID), nil
}

func deleteSkillsNewBotID(ctx context.Context, serverURL, authToken, id string) error {
	var out struct {
		DeleteBot *struct {
			Errors []skillsNewMutationError `json:"errors"`
		} `json:"deleteBot"`
	}
	if err := skillsNewGraphQL(ctx, serverURL, authToken, "HetchyDeleteBot", skillsNewDeleteBotMutation, map[string]any{"id": id}, &out); err != nil {
		return err
	}
	if out.DeleteBot == nil {
		return nil
	}
	return skillsNewMutationErrors(out.DeleteBot.Errors)
}

func skillsNewGraphQL(ctx context.Context, serverURL, authToken, operationName, query string, variables map[string]any, data any) error {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	authToken = strings.TrimSpace(authToken)
	if serverURL == "" || authToken == "" {
		return ErrNotConfigured
	}
	body, err := json.Marshal(map[string]any{
		"operationName": operationName,
		"query":         query,
		"variables":     variables,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/graphql", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var envelope struct {
		Data   json.RawMessage         `json:"data"`
		Errors []skillsNewGraphQLError `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("skills.new graphql status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		}
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("skills.new graphql status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if len(envelope.Errors) > 0 {
		return errors.New(skillsNewGraphQLErrors(envelope.Errors))
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}
	return json.Unmarshal(envelope.Data, data)
}

func skillsNewGraphQLErrors(errs []skillsNewGraphQLError) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		if msg := strings.TrimSpace(err.Message); msg != "" {
			parts = append(parts, msg)
		}
	}
	if len(parts) == 0 {
		return "skills.new graphql error"
	}
	return strings.Join(parts, "; ")
}

func skillsNewMutationErrors(errs []skillsNewMutationError) error {
	parts := []string{}
	for _, err := range errs {
		msg := strings.Join(err.Messages, ", ")
		if err.Field != "" && msg != "" {
			parts = append(parts, err.Field+": "+msg)
			continue
		}
		if msg != "" {
			parts = append(parts, msg)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return errors.New(strings.Join(parts, "; "))
}
