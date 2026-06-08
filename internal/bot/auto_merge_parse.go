package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

func parseAutoMergeAssessmentFromBlocks(bs []blocks.Block) (*autoMergeAssessment, error) {
	for i := len(bs) - 1; i >= 0; i-- {
		text := bs[i].Body
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
	var a autoMergeAssessment
	if err := json.Unmarshal([]byte(obj), &a); err != nil {
		return nil, fmt.Errorf("malformed auto merge assessment JSON: %w", err)
	}
	if err := validateAutoMergeAssessment(&a); err != nil {
		return nil, err
	}
	return &a, nil
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
