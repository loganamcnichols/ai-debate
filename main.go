package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/sashabaranov/go-openai"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

type QuestionRow struct {
	QuestionID    int       `db:"id"`
	ResponseID    uuid.UUID `db:"response_id"`
	UserMsg       string    `db:"user_msg"`
	CautionMsg    string    `db:"caution_msg"`
	InnovationMsg string    `db:"innovation_msg"`
	CreateTime    time.Time `db:"create_time"`
}

func (q *QuestionRow) Scan(rows *sql.Rows) error {
	return rows.Scan(
		&q.QuestionID,
		&q.ResponseID,
		&q.UserMsg,
		&q.CautionMsg,
		&q.InnovationMsg,
		&q.CreateTime,
	)
}

type SortedResponses struct {
	UserMsg         string
	FirstResponse   string
	SecondResponse  string
	InnovationFirst bool
}

// var client *claude.Client
var client *openai.Client
var tmpls *template.Template
var db *sql.DB

var (
	submitStmt          *sql.Stmt
	innovationFirstStmt *sql.Stmt
	chatHistoryStmt     *sql.Stmt
	updateChatStmt      *sql.Stmt
	responseInsertStmt  *sql.Stmt
	// Add more as needed
)

func promptSuggest(w http.ResponseWriter, r *http.Request) {
	prompts := []string{
		"If AI keeps improving at its current speed what will happen?",
		"Do you think the current level of AI safety is enough?",
		"What has been the impact of laws about AI?",
		"What would happen if we slowed down AI?",
	}
	choice := prompts[rand.Intn(len(prompts))]
	err := tmpls.ExecuteTemplate(w, "question-suggestion.html", choice)
	if err != nil {
		log.Printf("failed to execute template 'question-suggestion': %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func submitQuestion(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	idParam := params.Get("response-id")
	responseID, err := uuid.Parse(idParam)
	if err != nil {
		log.Printf("unable to parse uuid: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	userMsg := r.FormValue("user-msg")
	if userMsg == "" {
		log.Printf("received empty user msg")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	_, err = submitStmt.Exec(responseID, userMsg)
	if err != nil {
		log.Printf("error executing submitStmt, %v\n", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var firstResponse string
	var secondResponse string

	tmpls.ExecuteTemplate(w, "question-submit.html", struct {
		FirstResponse  string
		SecondResponse string
		UserMsg        string
		ResponseID     string
	}{
		FirstResponse:  firstResponse,
		SecondResponse: secondResponse,
		UserMsg:        userMsg,
		ResponseID:     responseID.String(),
	})
}

func convertToParagraphs(text string) string {
	// Split the text into lines
	lines := strings.Split(text, "\n")

	// Wrap each non-empty line with <p> tags
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			lines[i] = "<p>" + strings.TrimSpace(line) + "</p>"
		}
	}

	// Join the lines back together without any line breaks
	return strings.Join(lines, "")
}

func formatFirstMessage(firstMessage string, firstBot string) string {
	return fmt.Sprintf("Our next question is: \"%s\". %s will response first.", firstMessage, firstBot)
}

func formatSecondMessage(secondBot string) string {
	return fmt.Sprintf("Now, %s will respond.", secondBot)
}

func streamOpenaiResponse(w http.ResponseWriter, stream *openai.ChatCompletionStream, eventName string) (text string, err error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	throttle := time.NewTicker(20 * time.Millisecond)
	defer throttle.Stop()

	for {
		select {
		case <-throttle.C:
			var res openai.ChatCompletionStreamResponse
			res, err = stream.Recv()
			if errors.Is(err, io.EOF) {
				err = nil
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, convertToParagraphs(text))
				flusher.Flush()
				return
			}
			if err != nil {
				return
			}
			text += res.Choices[0].Delta.Content
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, convertToParagraphs(text))
			flusher.Flush()
		}
	}
}

func streamResponse(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Ensure the writer supports flushing
	idParam := r.URL.Query().Get("response-id")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	responseID, err := uuid.Parse(idParam)
	if err != nil {
		log.Printf("unable to parse resopnse-id %s: %v\n", idParam, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var innovateFirst bool
	err = innovationFirstStmt.QueryRow(responseID).Scan(&innovateFirst)
	if err != nil {
		log.Printf("failed to execute innovationFirstStmt: %v\n", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	rows, err := chatHistoryStmt.Query(responseID)
	if err != nil {
		log.Printf("unable to execute query chatHistoryStmt: %v\n", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	if !rows.Next() {
		log.Println(fmt.Errorf("no rows found during scan, this shouldn't be possible"))
		return
	}

	messages := []openai.ChatCompletionMessage{{}}
	var nextRow QuestionRow
	var firstAnswer, secondAnswer string
	for {
		if err := nextRow.Scan(rows); err != nil {
			log.Printf("error in during scan: %v", err)
			return
		}

		if innovateFirst {
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    "user",
				Content: formatFirstMessage(nextRow.UserMsg, "Innovate Bot"),
			})
		} else {
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    "user",
				Content: formatFirstMessage(nextRow.UserMsg, "Caution Bot"),
			})
		}

		if !rows.Next() {
			break
		}

		if innovateFirst {
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    "assistant",
				Name:    "InnovateBot",
				Content: formatFirstMessage(nextRow.InnovationMsg, "Innovate Bot"),
			})

			messages = append(messages, openai.ChatCompletionMessage{
				Role:    "user",
				Content: formatSecondMessage("Caution Bot"),
			})

			messages = append(messages, openai.ChatCompletionMessage{
				Role:    "assistant",
				Name:    "CautionBot",
				Content: nextRow.CautionMsg,
			})
		} else {
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    "assistant",
				Name:    "CautionBot",
				Content: nextRow.CautionMsg,
			})

			messages = append(messages, openai.ChatCompletionMessage{
				Role:    "user",
				Content: formatSecondMessage("Innovate Bot"),
			})

			messages = append(messages, openai.ChatCompletionMessage{
				Role:    "assistant",
				Name:    "InnovateBot",
				Content: nextRow.InnovationMsg,
			})
		}

		innovateFirst = !innovateFirst
	}

	if rows.Err() != nil {
		log.Printf("error iterating on rows: %v", rows.Err())
	}

	var firstPromptFile string
	var secondPromptFile string

	var firstBotName string
	var secondBotName string

	if innovateFirst {
		firstPromptFile = "PRO_INNOVATION_PROMPT.txt"
		secondPromptFile = "PRO_CAUTION_PROMPT.txt"

		firstBotName = "InnovateBot"
		secondBotName = "CautionBot"

		fmt.Fprint(w, "event: innovation-first\ndata: true\n\n")

		flusher.Flush()

	} else {
		firstPromptFile = "PRO_CAUTION_PROMPT.txt"
		secondPromptFile = "PRO_INNOVATION_PROMPT.txt"

		firstBotName = "CautionBot"
		secondBotName = "InnovateBot"

		fmt.Fprintf(w, "event: innovation-first\ndata: false\n\n")
		flusher.Flush()
	}

	firstSystemPrompt, err := os.ReadFile(firstPromptFile)
	if err != nil {
		log.Printf("unable to read innovation prompt: %v", err)
	}
	secondSystemPrompt, err := os.ReadFile(secondPromptFile)
	if err != nil {
		log.Printf("unable to read caution prompt: %v", err)
	}

	messages[0] = openai.ChatCompletionMessage{
		Role:    "system",
		Content: string(firstSystemPrompt),
	}

	req := openai.ChatCompletionRequest{
		Model:    openai.GPT4oMini20240718,
		Messages: messages,
		Stream:   true,
	}
	stream1, err := client.CreateChatCompletionStream(context.Background(), req)
	if err != nil {
		log.Printf("ChatCompletionStream error: %v\n", err)
		return
	}

	defer stream1.Close()

	firstAnswer, err = streamOpenaiResponse(w, stream1, "first-response")
	if err != nil {
		log.Printf("error streaming openai response: %v\n", err)
		return
	}

	messages = append(messages, openai.ChatCompletionMessage{
		Role:    "assistant",
		Name:    firstBotName,
		Content: firstAnswer,
	})

	messages = append(messages, openai.ChatCompletionMessage{
		Role:    "user",
		Content: formatSecondMessage(secondBotName),
	})

	messages[0] = openai.ChatCompletionMessage{
		Role:    "system",
		Content: string(secondSystemPrompt),
	}

	req = openai.ChatCompletionRequest{
		Model:    openai.GPT4oMini20240718,
		Messages: messages,
		Stream:   true,
	}

	stream2, err := client.CreateChatCompletionStream(context.Background(), req)
	if err != nil {
		log.Printf("ChatCompletionStream error: %v\n", err)
		return
	}

	defer stream2.Close()

	secondAnswer, err = streamOpenaiResponse(w, stream2, "second-response")
	if err != nil {
		log.Printf("error streaming openai response: %v\n", err)
		return
	}

	if innovateFirst {
		_, err := updateChatStmt.Exec(firstAnswer, secondAnswer, nextRow.QuestionID)
		log.Println("update complete")
		if err != nil {
			log.Printf("error executing updateChatStmt: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	} else {
		_, err := updateChatStmt.Exec(secondAnswer, firstAnswer, nextRow.QuestionID)
		log.Println("update complete")
		if err != nil {
			log.Printf("error executing updateChatStmt: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}

	fmt.Fprint(w, "event: close\ndata: Closing connection\n\n")
	flusher.Flush()
}

func createResponse() (uuid.UUID, error) {
	randomValue := rand.Float64()
	var query string
	var responseID uuid.UUID
	err := responseInsertStmt.QueryRow(randomValue > 0.5).Scan(&responseID)
	if err != nil {
		return responseID, fmt.Errorf("error executing query %s: %v", query, err)
	}
	return responseID, nil
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	var responseID uuid.UUID
	cookie, err := r.Cookie("response-id")
	if err == http.ErrNoCookie {
		responseID, err = createResponse()
		if err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
		http.SetCookie(w, &http.Cookie{
			Name:  "response-id",
			Value: responseID.String(),
			Path:  "/",
		})
	} else {
		responseID, err = uuid.Parse(cookie.Value)
		if err != nil {
			log.Printf("unable to parse cookie: %v\n", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}

	var innovationFirst bool
	err = innovationFirstStmt.QueryRow(responseID).Scan(&innovationFirst)
	if err != nil {
		log.Printf("unable to execute query innovation first stmt: %v\n", err)
		responseID, err = createResponse()
		if err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
		http.SetCookie(w, &http.Cookie{
			Name:  "response-id",
			Value: responseID.String(),
			Path:  "/",
		})
		query := "SELECT first_move_innovation FROM response WHERE id = $1"
		var innovationFirst bool
		err = db.QueryRow(query, responseID).Scan(&innovationFirst)
		if err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}

	res, err := chatHistoryStmt.Query(responseID)
	if err != nil {
		log.Printf("error executing query chatHistoryStmt: %v\n", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer res.Close()
	var questionsOut []SortedResponses
	initInnvationFirst := innovationFirst
	for res.Next() {
		var nextRow QuestionRow
		if err := nextRow.Scan(res); err != nil {
			log.Printf("error scanning row: %v\n", err)
			return
		}
		if innovationFirst {
			questionsOut = append(questionsOut, SortedResponses{
				UserMsg:         nextRow.UserMsg,
				FirstResponse:   nextRow.InnovationMsg,
				SecondResponse:  nextRow.CautionMsg,
				InnovationFirst: innovationFirst,
			})
		} else {
			questionsOut = append(questionsOut, SortedResponses{
				UserMsg:         nextRow.UserMsg,
				FirstResponse:   nextRow.CautionMsg,
				SecondResponse:  nextRow.InnovationMsg,
				InnovationFirst: innovationFirst,
			})
		}
		innovationFirst = !innovationFirst
	}

	var data = struct {
		QuestionRows    []SortedResponses
		InnovationFirst bool
		ResponseID      string
	}{
		QuestionRows:    questionsOut,
		InnovationFirst: initInnvationFirst,
		ResponseID:      responseID.String(),
	}

	err = tmpls.ExecuteTemplate(w, "index.html", data)
	if err != nil {
		log.Printf("error executing template: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func main() {
	// Get current timestamp
	timestamp := time.Now().Format("2006-01-02_15-04-05")

	// Create log filename with timestamp
	logFileName := fmt.Sprintf("app_%s.log", timestamp)

	// Open (or create) the log file
	logFile, err := os.OpenFile(logFileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatal("Failed to open log file:", err)
	}
	defer logFile.Close()

	// Create a multi writer for both file and console output
	multiWriter := io.MultiWriter(os.Stdout, logFile)

	// Set the log output to use the multi writer
	log.SetOutput(multiWriter)
	funcMap := template.FuncMap{
		"mod": func(i, j int) int {
			return i % j
		},
	}
	tmpl := template.New("").Funcs(funcMap)
	tmpls, err = tmpl.ParseGlob("web/templates/*")
	if err != nil {
		log.Fatalf("Failed to parse templates: %v\n", err)
	}

	err = godotenv.Load()
	if err != nil {
		log.Fatalf("failed to load .env: %v\n", err)
	}

	connStr := os.Getenv("POSTGRESQL_CONN_STR")
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Fatalf("unable to connect to database: %v\n", err)
	}
	err = db.Ping()
	if err != nil {
		log.Fatalf("unable to ping database %v\n", err)
	}

	defer db.Close()
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)

	submitStmt, err = db.Prepare(`INSERT INTO chat (response_id, user_msg) VALUES ($1, $2) RETURNING id;`)
	if err != nil {
		log.Fatalf("Failed to prepare submitStmt: %v", err)
	}

	innovationFirstStmt, err = db.Prepare(`SELECT first_move_innovation FROM response WHERE id = $1;`)
	if err != nil {
		log.Fatalf("Failed to prepare innovationFirstStmt: %v", err)
	}

	chatHistoryStmt, err = db.Prepare(`SELECT * FROM chat WHERE response_id = $1 ORDER BY created_time;`)
	if err != nil {
		log.Fatalf("Failed to prepare chatHistoryStmt: %v", err)
	}

	updateChatStmt, err = db.Prepare(`UPDATE chat SET innovation_msg = $1, caution_msg = $2 WHERE id = $3;`)
	if err != nil {
		log.Fatalf("Failed to prepare updateChatStmt: %v", err)
	}

	responseInsertStmt, err = db.Prepare(`INSERT INTO response (first_move_innovation) VALUES ($1) RETURNING id`)
	if err != nil {
		log.Fatalf("Failed to prepare responseInsertStmt %v", err)
	}

	client = openai.NewClient(os.Getenv("OPENAI_API_KEY"))

	r := mux.NewRouter()

	// Define your routes
	r.HandleFunc("/", handleIndex)
	r.HandleFunc("/submit-question", submitQuestion)
	r.HandleFunc("/chat-response", streamResponse)
	r.HandleFunc("/prompt-suggestion", promptSuggest)

	// Serve static files
	r.PathPrefix("/").Handler(http.FileServer(http.Dir("web")))

	// Use the router as the HTTP handler
	http.Handle("/", r)

	fmt.Println("Server is running on http://localhost:8080")
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		fmt.Println("Error starting server:", err)
	}
}
