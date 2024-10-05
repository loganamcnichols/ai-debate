package main

import (
	"context"
	"database/sql"
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

	claude "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

type QuestionRow struct {
	QuestionID    int
	ResponseID    uuid.UUID
	UserMsg       string
	CautionMsg    string
	InnovationMsg string
}

type SortedResponses struct {
	UserMsg         string
	FirstResponse   string
	SecondResponse  string
	InnovationFirst bool
}

var client *claude.Client
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

func writeStreamWithInterval(w io.Writer, stream *ssestream.Stream[claude.MessageStreamEvent], eventName string, interval time.Duration) (string, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return "", fmt.Errorf("writer does not implement http.Flusher")
	}

	var fullContent string
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	done := make(chan bool)
	go func() {
		for stream.Next() {
			event := stream.Current()
			switch delta := event.Delta.(type) {
			case claude.ContentBlockDeltaEventDelta:
				if delta.Text != "" {
					fullContent += delta.Text
				}
			}
		}
		done <- true
	}()

	for {
		select {
		case <-ticker.C:
			if fullContent != "" {
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, convertToParagraphs(fullContent))
				flusher.Flush()
			}
		case <-done:
			// Final write
			if fullContent != "" {
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, convertToParagraphs(fullContent))
				flusher.Flush()
			}
			return fullContent, nil
		}
	}
}

func streamResponse(w http.ResponseWriter, r *http.Request) {
	log.Println("stream called")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Ensure the writer supports flushing
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}
	idParam := r.URL.Query().Get("response-id")

	log.Printf("Got responseID %s in stream", idParam)

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

	log.Printf("got response %t from innovate first query", innovateFirst)

	rows, err := chatHistoryStmt.Query(responseID)
	if err != nil {
		log.Printf("unable to execute query chatHistoryStmt: %v\n", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var messages []claude.MessageParam
	var questionID int
	var formattedQuestion, formattedTransition, firstAnswer, secondAnswer string

	for rows.Next() {
		log.Printf("processing row")
		var nextRow QuestionRow
		rows.Scan(&questionID, &nextRow.ResponseID, &nextRow.UserMsg, &nextRow.CautionMsg, &nextRow.InnovationMsg)
		if innovateFirst {
			formattedQuestion = fmt.Sprintf("The user asks '%s'. The innovation team will respond first.", nextRow.UserMsg)
			formattedTransition = "Now the caution team will have an opportunity for rebuttle."
			firstAnswer = nextRow.InnovationMsg
			secondAnswer = nextRow.CautionMsg
		} else {
			formattedQuestion = fmt.Sprintf("The user asks '%s'. The caution team will respond first.", nextRow.UserMsg)
			formattedTransition = "Now the innovation team will have an opportunity for rebuttle."
			firstAnswer = nextRow.CautionMsg
			secondAnswer = nextRow.InnovationMsg
		}
		if firstAnswer == "" || secondAnswer == "" {
			break
		}

		messages = append(messages, claude.NewUserMessage(claude.NewTextBlock(formattedQuestion)))
		messages = append(messages, claude.NewAssistantMessage(claude.NewTextBlock(firstAnswer)))
		messages = append(messages, claude.NewUserMessage(claude.NewTextBlock(formattedTransition)))
		messages = append(messages, claude.NewAssistantMessage(claude.NewTextBlock(secondAnswer)))

		innovateFirst = !innovateFirst
	}

	log.Printf("innovate first came out %t", innovateFirst)

	var firstPromptFile string
	var secondPromptFile string
	if innovateFirst {
		firstPromptFile = "PRO_INNOVATION_PROMPT.txt"
		secondPromptFile = "PRO_CAUTION_PROMPT.txt"
	} else {
		firstPromptFile = "PRO_CAUTION_PROMPT.txt"
		secondPromptFile = "PRO_INNOVATION_PROMPT.txt"
	}

	firstSystemPrompt, err := os.ReadFile(firstPromptFile)
	if err != nil {
		log.Printf("unable to read innovation prompt: %v", err)
	}
	secondSystemPrompt, err := os.ReadFile(secondPromptFile)
	if err != nil {
		log.Printf("unable to read caution prompt: %v", err)
	}

	messages = append(messages, claude.NewUserMessage(claude.NewTextBlock(formattedQuestion)))

	if innovateFirst {
		fmt.Fprint(w, "event: innovation-first\ndata: true\n\n")
		flusher.Flush()
	} else {
		fmt.Fprintf(w, "event: innovation-first\ndata: false\n\n")
		flusher.Flush()
	}

	stream := client.Messages.NewStreaming(context.TODO(), claude.MessageNewParams{
		Model:     claude.F(claude.ModelClaude_3_5_Sonnet_20240620),
		MaxTokens: claude.Int(1024),
		System:    claude.F([]claude.TextBlockParam{claude.NewTextBlock(string(firstSystemPrompt))}),
		Messages:  claude.F(messages),
	})

	firstAnswer, err = writeStreamWithInterval(w, stream, "first-response", 200*time.Millisecond)
	if err != nil {
		log.Printf("error executing updateChatStmt: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	log.Printf("processed first answer: %s", firstAnswer)

	time.Sleep(200 * time.Millisecond)
	messages = append(messages, claude.NewAssistantMessage(claude.NewTextBlock(firstAnswer)))
	messages = append(messages, claude.NewUserMessage(claude.NewTextBlock(formattedTransition)))

	stream = client.Messages.NewStreaming(context.TODO(), claude.MessageNewParams{
		Model:     claude.F(claude.ModelClaude_3_5_Sonnet_20240620),
		MaxTokens: claude.Int(1024),
		System:    claude.F([]claude.TextBlockParam{claude.NewTextBlock(string(secondSystemPrompt))}),
		Messages:  claude.F(messages),
	})
	secondAnswer, err = writeStreamWithInterval(w, stream, "second-response", 200*time.Millisecond)
	if err != nil {
		log.Printf("error executing updateChatStmt: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	log.Printf("Processed second answer %s:", secondAnswer)
	if innovateFirst {
		_, err := updateChatStmt.Exec(firstAnswer, secondAnswer, questionID)
		log.Println("update complete")
		if err != nil {
			log.Printf("error executing updateChatStmt: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	} else {
		_, err := updateChatStmt.Exec(secondAnswer, firstAnswer, questionID)
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
		res.Scan(&nextRow.QuestionID, &nextRow.ResponseID, &nextRow.UserMsg,
			&nextRow.CautionMsg, &nextRow.InnovationMsg)
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
	// Open a file for writing logs
	var err error
	logFile, err := os.OpenFile("app.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
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

	chatHistoryStmt, err = db.Prepare(`SELECT * FROM chat WHERE response_id = $1;`)
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

	client = claude.NewClient()

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
