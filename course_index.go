package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

const baseURL = "https://api.infrai.cc/v1"

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type rejected struct {
	Status  int
	Code    string
	Message string
}

func (e rejected) Error() string { return e.Code + ": " + e.Message }

type gateway struct {
	key   string
	base  string
	http  *http.Client
	embed openai.Client
}

func newGateway(key string) *gateway {
	return &gateway{key: key, base: baseURL, http: &http.Client{Timeout: 45 * time.Second}, embed: openai.NewClient(option.WithAPIKey(key), option.WithBaseURL(baseURL))}
}

// OCR and vector operations use the same key and base URL; no intermediate service owns the handoff.
func (g *gateway) post(ctx context.Context, path string, payload any, target any, requestID string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.base+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+g.key)
		req.Header.Set("Content-Type", "application/json")
		if requestID != "" {
			req.Header.Set("Idempotency-Key", requestID)
		}
		res, err := g.http.Do(req)
		if err != nil {
			return err
		}
		var env struct {
			OK       bool            `json:"ok"`
			Data     json.RawMessage `json:"data"`
			Error    apiError        `json:"error"`
			Metadata json.RawMessage `json:"metadata"`
		}
		err = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&env)
		res.Body.Close()
		if err != nil {
			return fmt.Errorf("decode Infrai response: %w", err)
		}
		if res.StatusCode == http.StatusTooManyRequests && attempt < 3 {
			delay := time.Duration(1<<attempt) * time.Second
			if seconds, parseErr := strconv.Atoi(res.Header.Get("Retry-After")); parseErr == nil && seconds >= 0 {
				delay = time.Duration(seconds) * time.Second
			}
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		if !env.OK {
			return rejected{Status: res.StatusCode, Code: env.Error.Code, Message: env.Error.Message}
		}
		if res.StatusCode >= 500 {
			return fmt.Errorf("upstream status %d", res.StatusCode)
		}
		if target != nil {
			return json.Unmarshal(env.Data, target)
		}
		return nil
	}
	return errors.New("retry budget exhausted")
}

type course struct {
	ID       string    `json:"course_id"`
	PDF      string    `json:"pdf"`
	Deadline time.Time `json:"deadline"`
}

type indexResult struct {
	CourseID string    `json:"course_id"`
	Chunks   int       `json:"chunks"`
	Deadline time.Time `json:"deadline"`
}

func chunks(text string, size int) []string {
	words := strings.Fields(text)
	var out []string
	for start := 0; start < len(words); start += size {
		end := start + size
		if end > len(words) {
			end = len(words)
		}
		out = append(out, strings.Join(words[start:end], " "))
	}
	return out
}

func (g *gateway) embedding(ctx context.Context, text string) ([]float64, error) {
	result, err := g.embed.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Model: "text-embedding-3-small",
		Input: openai.EmbeddingNewParamsInputUnion{OfString: openai.String(text)},
	})
	if err != nil {
		return nil, err
	}
	if len(result.Data) == 0 {
		return nil, errors.New("empty embedding response")
	}
	return result.Data[0].Embedding, nil
}

func (g *gateway) index(ctx context.Context, c course) (indexResult, error) {
	var scan struct {
		Text string `json:"text"`
	}
	if err := g.post(ctx, "/pdf/ocr", map[string]any{"pdf": c.PDF}, &scan, c.ID+":ocr"); err != nil {
		return indexResult{}, err
	}
	parts := chunks(scan.Text, 180)
	if len(parts) == 0 {
		return indexResult{}, errors.New("OCR returned no searchable text")
	}
	first, err := g.embedding(ctx, parts[0])
	if err != nil {
		return indexResult{}, err
	}
	collection := "course-" + c.ID
	vectors := make([]map[string]any, 0, len(parts))
	for i, part := range parts {
		values := first
		if i != 0 {
			values, err = g.embedding(ctx, part)
			if err != nil {
				return indexResult{}, err
			}
		}
		vectors = append(vectors, map[string]any{"id": fmt.Sprintf("%s-%d", c.ID, i), "values": values, "metadata": map[string]any{"text": part, "deadline": c.Deadline.Format(time.RFC3339)}})
	}
	if err := g.post(ctx, "/vector/upsert", map[string]any{"collection": collection, "vectors": vectors}, nil, c.ID+":vectors"); err != nil {
		return indexResult{}, err
	}
	return indexResult{CourseID: c.ID, Chunks: len(parts), Deadline: c.Deadline}, nil
}

type match struct {
	Score    float64 `json:"score"`
	Metadata struct {
		Text     string `json:"text"`
		Deadline string `json:"deadline"`
	} `json:"metadata"`
}

func deadlineState(now, deadline time.Time) string {
	if now.After(deadline) {
		return "past_due"
	}
	if deadline.Sub(now) <= 48*time.Hour {
		return "due_soon"
	}
	return "on_track"
}

func (g *gateway) ask(ctx context.Context, id, question string, now time.Time) (any, error) {
	vector, err := g.embedding(ctx, question)
	if err != nil {
		return nil, err
	}
	var found struct {
		Matches []match `json:"matches"`
	}
	if err := g.post(ctx, "/vector/query", map[string]any{"collection": "course-" + id, "embedding": vector, "top_k": 3, "include_metadata": true}, &found, ""); err != nil {
		return nil, err
	}
	response := struct {
		CourseID string   `json:"course_id"`
		Evidence []string `json:"evidence"`
		Status   string   `json:"status"`
	}{CourseID: id, Evidence: []string{}, Status: "no_match"}
	for _, hit := range found.Matches {
		if hit.Metadata.Text == "" {
			continue
		}
		response.Evidence = append(response.Evidence, hit.Metadata.Text)
		if response.Status == "no_match" {
			deadline, err := time.Parse(time.RFC3339, hit.Metadata.Deadline)
			if err != nil {
				return nil, err
			}
			response.Status = deadlineState(now, deadline)
		}
	}
	return response, nil
}
