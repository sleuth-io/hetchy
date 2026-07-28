package bot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sleuth-io/hetchy/internal/blocks"
)

func parseAutoMergeAssessmentFromBlocks(bs []blocks.Block) (*autoMergeAssessment, error) {
	for _, b := range slices.Backward(bs) {
		text := b.Body
		if text == "" {
			continue
		}
		if !strings.Contains(text, autoMergeAssessmentMarker) {
			continue
		}
		return parseAutoMergeAssessmentText(text)
	}
	return nil, errors.New("missing auto merge assessment")
}

func parseAutoMergeAssessmentText(text string) (*autoMergeAssessment, error) {
	idx := strings.LastIndex(text, autoMergeAssessmentMarker)
	if idx < 0 {
		return nil, errors.New("missing auto merge assessment marker")
	}
	obj, err := extractFirstJSONObject(text[idx+len(autoMergeAssessmentMarker):])
	if err != nil {
		return nil, fmt.Errorf("malformed auto merge assessment: %w", err)
	}
	a, err := decodeAutoMergeAssessmentJSON([]byte(obj))
	if err != nil {
		return nil, fmt.Errorf("malformed auto merge assessment JSON: %w", err)
	}
	if err := validateAutoMergeAssessment(a); err != nil {
		return nil, err
	}
	return a, nil
}

func extractFirstJSONObject(text string) (string, error) {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return "", errors.New("JSON object not found")
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		ch := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1], nil
			}
		}
	}
	return "", errors.New("JSON object is incomplete")
}

type rawAutoMergeAssessment struct {
	Recommendation            string                    `json:"recommendation"`
	Risk                      string                    `json:"risk"`
	Confidence                string                    `json:"confidence"`
	Summary                   string                    `json:"summary"`
	RiskFactors               json.RawMessage           `json:"risk_factors"`
	TestsSeenPassing          json.RawMessage           `json:"tests_seen_passing"`
	ReviewIterations          json.RawMessage           `json:"review_iterations"`
	RemainingIssues           []autoMergeRemainingIssue `json:"remaining_issues"`
	DangerousChangeCategories json.RawMessage           `json:"dangerous_change_categories"`
	HeadSHA                   string                    `json:"head_sha"`
}

func decodeAutoMergeAssessmentJSON(raw []byte) (*autoMergeAssessment, error) {
	var parsed rawAutoMergeAssessment
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	riskFactors, err := decodeAutoMergeStringList("risk_factors", parsed.RiskFactors, false)
	if err != nil {
		return nil, err
	}
	testsSeen, err := decodeAutoMergeStringList("tests_seen_passing", parsed.TestsSeenPassing, false)
	if err != nil {
		return nil, err
	}
	reviewIterations, err := decodeAutoMergeStringList("review_iterations", parsed.ReviewIterations, true)
	if err != nil {
		return nil, err
	}
	dangerousCategories, err := decodeAutoMergeStringList("dangerous_change_categories", parsed.DangerousChangeCategories, false)
	if err != nil {
		return nil, err
	}
	return &autoMergeAssessment{
		Recommendation:            parsed.Recommendation,
		Risk:                      parsed.Risk,
		Confidence:                parsed.Confidence,
		Summary:                   parsed.Summary,
		RiskFactors:               riskFactors,
		TestsSeenPassing:          testsSeen,
		ReviewIterations:          reviewIterations,
		RemainingIssues:           parsed.RemainingIssues,
		DangerousChangeCategories: dangerousCategories,
		HeadSHA:                   parsed.HeadSHA,
	}, nil
}

func decodeAutoMergeStringList(name string, raw json.RawMessage, allowScalar bool) ([]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var values []json.RawMessage
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("%s must be an array of strings: %w", name, err)
		}
		out := make([]string, 0, len(values))
		for _, value := range values {
			s, ok, err := decodeAutoMergeStringListValue(name, value, allowScalar)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, s)
			}
		}
		return out, nil
	}
	s, ok, err := decodeAutoMergeStringListValue(name, raw, allowScalar)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return []string{s}, nil
}

func decodeAutoMergeStringListValue(name string, raw json.RawMessage, allowScalar bool) (string, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", false, fmt.Errorf("%s must contain strings: %w", name, err)
		}
		s = strings.TrimSpace(s)
		return s, s != "", nil
	}
	if !allowScalar {
		return "", false, fmt.Errorf("%s must be a string or array of strings", name)
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return "", false, fmt.Errorf("%s scalar value is invalid: %w", name, err)
	}
	switch v.(type) {
	case json.Number:
		return "", false, nil
	case bool:
		return "", false, nil
	default:
		return "", false, fmt.Errorf("%s must contain strings", name)
	}
}
