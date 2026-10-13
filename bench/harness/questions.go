package harness

import (
	"embed"
	"encoding/json"
	"fmt"
)

//go:embed questions/*.json
var questionFiles embed.FS

// Question is one benchmark question with its reference query.
type Question struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // lookup, join, enum, aggregate, large, misspell
	Question string `json:"question"`
	SQL      string `json:"sql"`
	Ordered  bool   `json:"ordered,omitempty"`
	// Mongo is the twin of SQL on a document dataset:
	// {"collection": "...", "pipeline": [...]} in relaxed Extended JSON. Its
	// result must equal SQL's on the Postgres dataset the documents came from.
	Mongo json.RawMessage `json:"mongo,omitempty"`
}

// LoadQuestions returns the questions for a dataset.
func LoadQuestions(dataset string) ([]Question, error) {
	b, err := questionFiles.ReadFile("questions/" + dataset + ".json")
	if err != nil {
		return nil, fmt.Errorf("no questions for dataset %q", dataset)
	}
	var qs []Question
	if err := json.Unmarshal(b, &qs); err != nil {
		return nil, fmt.Errorf("questions/%s.json: %w", dataset, err)
	}
	return qs, nil
}

// SystemPrompt is the same for every server and question.
const SystemPrompt = "You are a careful data analyst. Answer the user's question by exploring and querying the database " +
	"with the available tools. When you know the answer, call submit_answer exactly once with the result rows " +
	"(one array per row, columns in the order the question asks for). Do not guess values you have not queried."

// PromptFor is the user message for a question.
func PromptFor(q Question) string { return q.Question }
