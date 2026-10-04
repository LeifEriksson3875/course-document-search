package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var courseID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func serve(g *gateway) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /courses", func(w http.ResponseWriter, r *http.Request) {
		var input course
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input) != nil || !courseID.MatchString(input.ID) || input.PDF == "" || input.Deadline.IsZero() {
			http.Error(w, "course_id, pdf and deadline are required", http.StatusBadRequest)
			return
		}
		result, err := g.index(r.Context(), input)
		if err != nil {
			respondError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, result)
	})
	mux.HandleFunc("POST /courses/{id}/questions", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var input struct {
			Question string `json:"question"`
		}
		if !courseID.MatchString(id) || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input) != nil || strings.TrimSpace(input.Question) == "" {
			http.Error(w, "course id and question are required", http.StatusBadRequest)
			return
		}
		result, err := g.ask(r.Context(), id, input.Question, time.Now())
		if err != nil {
			respondError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func respondError(w http.ResponseWriter, err error) {
	var rejection rejected
	if errors.As(err, &rejection) && rejection.Status >= 400 && rejection.Status < 500 {
		writeJSON(w, rejection.Status, map[string]string{"error": rejection.Code, "message": rejection.Message})
		return
	}
	log.Printf("request failed: %v", err)
	http.Error(w, "upstream request failed", http.StatusBadGateway)
}

func main() {
	key := os.Getenv("INFRAI_API_KEY")
	if key == "" {
		log.Fatal("set INFRAI_API_KEY")
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Fatal(http.ListenAndServe(addr, serve(newGateway(key))))
}
