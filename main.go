package main

import (
	"bytes"
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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/sashabaranov/go-openai"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

type QuestionRow struct {
	QuestionID    uuid.UUID `db:"id"`
	ResponseID    uuid.UUID `db:"response_id"`
	UserMsg       string    `db:"user_msg"`
	CautionMsg    string    `db:"caution_msg"`
	InnovationMsg string    `db:"innovation_msg"`
	CreateTime    time.Time `db:"create_time"`
}

type ChatMessage struct {
	QuestionID uuid.UUID
	Role       string
	Content    string
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
var chatMap = sync.Map{}
var suggestionMap = sync.Map{}

var prompts = []string{
	"If AI keeps improving at its current speed what will happen?",
	"Do you think the current level of AI safety is enough?",
	"What has been the impact of laws about AI?",
	"What would happen if we slowed down AI?",
}

var (
	submitStmt          *sql.Stmt
	innovationFirstStmt *sql.Stmt
	chatHistoryStmt     *sql.Stmt
	insertChatStmt      *sql.Stmt
	responseInsertStmt  *sql.Stmt
	chatCountStmt       *sql.Stmt
	// Add more as needed
)

func promptSuggest(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	idParam := params.Get("response-id")
	responseID, err := uuid.Parse(idParam)
	if err != nil {
		log.Printf("unable to parse uuid: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	var availablePrompts []string
	if data, ok := suggestionMap.Load(responseID); ok {
		availablePrompts = data.([]string)
	} else {
		availablePrompts = promptCopy()
		suggestionMap.Store(responseID, promptCopy())
	}
	if len(availablePrompts) == 0 {
		w.Header().Set("HX-Reswap", "outerHTML")
		fmt.Fprint(w, "<p>Try asking a question of your own.</p>")
		return
	}
	choiceIdx := rand.Intn(len(availablePrompts))
	choice := availablePrompts[choiceIdx]
	err = tmpls.ExecuteTemplate(w, "question-suggestion.html", struct {
		ChoiceIdx int
		Choice    string
	}{
		ChoiceIdx: choiceIdx,
		Choice:    choice,
	})
	if err != nil {
		log.Printf("failed to execute template 'question-suggestion': %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func suggestionSubmit(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	idParam := params.Get("response-id")
	responseID, err := uuid.Parse(idParam)
	if err != nil {
		log.Printf("unable to parse uuid: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	suggestionIdx, err := strconv.Atoi(r.FormValue("suggestion-idx"))
	if err != nil {
		log.Printf("unable to parse uuid: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	var availablePrompts []string
	if data, ok := suggestionMap.Load(responseID); ok {
		availablePrompts = data.([]string)
	} else {
		availablePrompts = promptCopy()
		suggestionMap.Store(responseID, availablePrompts)
	}

	if suggestionIdx >= len(availablePrompts) {
		log.Printf("unable to parse uuid: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	err = tmpls.ExecuteTemplate(w, "inactive-form", responseID)
	if err != nil {
		log.Printf("error executing template 'inactive-form': %v\n", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	userMsg := availablePrompts[suggestionIdx]

	availablePrompts = append(availablePrompts[:suggestionIdx], availablePrompts[suggestionIdx+1:]...)
	suggestionMap.Store(responseID, availablePrompts)

	var userChannel chan string
	if data, ok := chatMap.Load(responseID); ok {
		chanP := data.(*chan string)
		userChannel = *chanP
	} else {
		log.Printf("recreating channel")
		userChannel = make(chan string, 1)
		chatMap.Store(responseID, &userChannel)
	}

	userChannel <- userMsg

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
		w.Header().Set("HX-Reswap", "none")
		return
	}

	err = tmpls.ExecuteTemplate(w, "inactive-form", responseID)
	if err != nil {
		log.Printf("unable to execute template 'inactive-form': %v\n", err)
		return
	}

	var userChannel chan string
	if data, ok := chatMap.Load(responseID); ok {
		chanP := data.(*chan string)
		userChannel = *chanP
	} else {
		userChannel = make(chan string, 1)
		chatMap.Store(responseID, &userChannel)
	}

	userChannel <- userMsg
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

func streamOpenaiResponse(w http.ResponseWriter, stream *openai.ChatCompletionStream, msg ChatMessage) (text string, err error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	msgTmpl := "event: %s-%s\ndata: %s\n\n"

	throttle := time.NewTicker(20 * time.Millisecond)
	defer throttle.Stop()

	for range throttle.C {
		var res openai.ChatCompletionStreamResponse
		res, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			err = nil
			fmt.Fprintf(w, msgTmpl, msg.QuestionID.String(), msg.Role, convertToParagraphs(text))
			flusher.Flush()
			return
		}
		if err != nil {
			return
		}
		text += res.Choices[0].Delta.Content
		fmt.Fprintf(w, msgTmpl, msg.QuestionID.String(), msg.Role, convertToParagraphs(text))
		flusher.Flush()
	}
	return "", nil
}

func postTemplate(w http.ResponseWriter, eventName string, tmplName string, data interface{}) error {
	var buf bytes.Buffer

	// Execute the template and write the result to the buffer
	err := tmpls.ExecuteTemplate(&buf, tmplName, data)
	if err != nil {
		return err
	}

	// Convert buffer to string and replace newlines with spaces
	output := strings.ReplaceAll(buf.String(), "\n", " ")

	// Trim any leading or trailing spaces that might have been introduced
	output = strings.TrimSpace(output)

	// Write the SSE event
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, output)

	// Flush the writer to ensure the event is sent immediately
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

func streamIntroMsgs(w http.ResponseWriter) error {
	err := postTemplate(w, "into-msg", "intro-msg-1", nil)
	if err != nil {
		return fmt.Errorf("unable to parse template 'intro-msg-1': %v", err)
	}
	time.Sleep(1 * time.Second)
	err = postTemplate(w, "intro-msg", "intro-msg-2", nil)
	if err != nil {
		return fmt.Errorf("unable to parse template 'intro-msg-2': %v", err)
	}
	time.Sleep(1 * time.Second)
	err = postTemplate(w, "intro-msg", "intro-msg-3", nil)
	if err != nil {
		return fmt.Errorf("unable to parse template 'intro-msg-3': %v", err)
	}
	return nil
}

func formatTemplateMessages(responseID uuid.UUID, innovateFirst bool) ([]ChatMessage, error) {
	messages := []ChatMessage{}
	rows, err := chatHistoryStmt.Query(responseID)
	if err != nil {
		return messages, fmt.Errorf("failed to execute chatHistoryStmt: %v", err)
	}
	defer rows.Close()
	var nextRow QuestionRow
	for rows.Next() {
		if err := nextRow.Scan(rows); err != nil {
			return messages, err
		}
		if innovateFirst {
			messages = append(messages, []ChatMessage{
				{
					QuestionID: nextRow.QuestionID,
					Role:       "user",
					Content:    nextRow.UserMsg,
				},
				{
					QuestionID: nextRow.QuestionID,
					Role:       "InnovateBot",
					Content:    nextRow.InnovationMsg,
				},
				{
					QuestionID: nextRow.QuestionID,
					Role:       "CautionBot",
					Content:    nextRow.CautionMsg,
				},
			}...)
		} else {
			messages = append(messages, []ChatMessage{
				{
					QuestionID: nextRow.QuestionID,
					Role:       "user",
					Content:    nextRow.UserMsg,
				},
				{
					QuestionID: nextRow.QuestionID,
					Role:       "CautionBot",
					Content:    nextRow.CautionMsg,
				},
				{
					QuestionID: nextRow.QuestionID,
					Role:       "InnovateBot",
					Content:    nextRow.InnovationMsg,
				},
			}...)
		}
		innovateFirst = !innovateFirst
	}
	return messages, nil
}

func formatMessages(responseID uuid.UUID) ([]openai.ChatCompletionMessage, bool, error) {
	messages := []openai.ChatCompletionMessage{{}}
	var innovateFirst bool
	err := innovationFirstStmt.QueryRow(responseID).Scan(&innovateFirst)
	if err != nil {
		return messages, false, fmt.Errorf("failed to execute innovationFirstStmt: %v", err)
	}

	rows, err := chatHistoryStmt.Query(responseID)
	if err != nil {
		return messages, false, fmt.Errorf("failed to execute chatHistoryStmt: %v", err)
	}
	defer rows.Close()
	var nextRow QuestionRow
	for rows.Next() {
		if err := nextRow.Scan(rows); err != nil {
			return messages, false, err
		}
		if innovateFirst {
			messages = append(messages, []openai.ChatCompletionMessage{
				{
					Role:    "user",
					Content: formatFirstMessage(nextRow.UserMsg, "Innovate Bot"),
				},
				{
					Role:    "assistant",
					Name:    "InnovateBot",
					Content: formatFirstMessage(nextRow.InnovationMsg, "Innovate Bot"),
				},
				{
					Role:    "user",
					Content: formatSecondMessage("Caution Bot"),
				},
				{
					Role:    "assistant",
					Name:    "CautionBot",
					Content: nextRow.CautionMsg,
				}}...)
		} else {
			messages = append(messages, []openai.ChatCompletionMessage{
				{
					Role:    "user",
					Content: formatFirstMessage(nextRow.UserMsg, "Caution Bot"),
				},
				{
					Role:    "assistant",
					Name:    "CautionBot",
					Content: nextRow.CautionMsg,
				},
				{
					Role:    "user",
					Content: formatSecondMessage("Innovate Bot"),
				},
				{
					Role:    "assistant",
					Name:    "InnovateBot",
					Content: nextRow.InnovationMsg,
				}}...)
		}
		innovateFirst = !innovateFirst
	}
	if rows.Err() != nil {
		return messages, false, err
	}

	return messages, innovateFirst, nil
}

func processStreamError(w http.ResponseWriter, responseID uuid.UUID, questionID uuid.UUID, userInput string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "event: %s-%s-delete\ndata: <p></p>\n\n", questionID.String(), "user")
	flusher.Flush()
	fmt.Fprintf(w, "event: %s-%s-delete\ndata: <p></p>\n\n", questionID.String(), "InnovateBot")
	flusher.Flush()
	fmt.Fprintf(w, "event: %s-%s-delete\ndata: <p></p>\n\n", questionID.String(), "CautionBot")
	flusher.Flush()
	postTemplate(w, "active-form", "form-error.html", struct {
		ResponseID string
		UserInput  string
	}{
		ResponseID: responseID.String(),
		UserInput:  userInput,
	})
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

	var userChannel chan string
	if data, ok := chatMap.Load(responseID); ok {
		chanP := data.(*chan string)
		userChannel = *chanP
	} else {
		userChannel = make(chan string, 1)
		chatMap.Store(responseID, &userChannel)
	}

	timer := time.NewTimer(1 * time.Minute)

	// Close the channel after time
	ctx := r.Context()
	go func() {
		select {
		case <-timer.C:
			fmt.Fprint(w, "event: inactive\ndata: \n\n")
			chatMap.Delete(responseID)
			close(userChannel)
		case <-ctx.Done():
			chatMap.Delete(responseID)
			close(userChannel)
		}
	}()

	var chatHistoryLen int
	err = chatCountStmt.QueryRow(responseID).Scan(&chatHistoryLen)
	if err != nil {
		log.Printf("failed to execute query for count")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if chatHistoryLen == 0 {
		if err = streamIntroMsgs(w); err != nil {
			log.Printf("failed to load intro msgs: %v\n", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		go func() { userChannel <- "opening argument" }()

	}

	for userMsg := range userChannel {
		timer.Reset(20 * time.Minute)
		messages, innovateNext, err := formatMessages(responseID)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}

		var firstPromptFile string
		var secondPromptFile string

		var firstBotName string
		var secondBotName string

		if innovateNext {
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

		questionID := uuid.New()

		userChatMessage := ChatMessage{
			QuestionID: questionID,
			Role:       "user",
			Content:    userMsg,
		}

		firstRespMessage := ChatMessage{
			QuestionID: questionID,
			Role:       firstBotName,
		}

		secondRespMessage := ChatMessage{
			QuestionID: questionID,
			Role:       secondBotName,
		}

		err = postTemplate(w, "chat-msg", "chat-msg", userChatMessage)
		if err != nil {
			log.Printf("failed to post chat-message template: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		err = postTemplate(w, "chat-msg", "chat-msg", firstRespMessage)
		if err != nil {
			log.Printf("failed to post chat-message template: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		err = postTemplate(w, "chat-msg", "chat-msg", secondRespMessage)
		if err != nil {
			log.Printf("failed to post chat-message template: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		messages = append(messages, openai.ChatCompletionMessage{
			Role:    "user",
			Content: formatFirstMessage(userMsg, firstBotName),
		})

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
			return
		}
		defer stream1.Close()

		firstAnswer, err := streamOpenaiResponse(w, stream1, firstRespMessage)
		if err != nil {
			processStreamError(w, responseID, questionID, userMsg)
			continue
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
			processStreamError(w, responseID, questionID, userMsg)
			continue
		}

		defer stream2.Close()

		secondAnswer, err := streamOpenaiResponse(w, stream2, secondRespMessage)
		if err != nil {
			log.Printf("error streaming openai response: %v\n", err)
			processStreamError(w, responseID, questionID, userMsg)
		}

		err = postTemplate(w, "active-form", "active-form", responseID.String())
		if err != nil {
			log.Printf("unable to execute template 'active-form': %v\n", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		if innovateNext {
			_, err := insertChatStmt.Exec(questionID, responseID, userMsg, secondAnswer, firstAnswer)
			if err != nil {
				log.Printf("error executing insertChatStmt: %v", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
		} else {
			_, err := insertChatStmt.Exec(questionID, responseID, userMsg, firstAnswer, secondAnswer)
			log.Println("update complete")
			if err != nil {
				log.Printf("error executing updateChatStmt: %v", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
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

func promptCopy() []string {
	dst := make([]string, len(prompts))
	copy(dst, prompts)
	return dst
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	var responseID uuid.UUID
	cookie, err := r.Cookie("response-id")
	if err == http.ErrNoCookie {
		responseID, err = createResponse()
		if err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
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

	messages, err := formatTemplateMessages(responseID, innovationFirst)
	if err != nil {
		log.Printf("unable to format template messages: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	var data = struct {
		QuestionRows []ChatMessage
		ResponseID   string
	}{
		QuestionRows: messages,
		ResponseID:   responseID.String(),
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

	insertChatStmt, err = db.Prepare(`INSERT INTO chat (id, response_id, user_msg, caution_msg, innovation_msg)
																							VALUES ($1, $2, $3, $4, $5);`)
	if err != nil {
		log.Fatalf("Failed to prepare updateChatStmt: %v", err)
	}

	responseInsertStmt, err = db.Prepare(`INSERT INTO response (first_move_innovation) VALUES ($1) RETURNING id`)
	if err != nil {
		log.Fatalf("Failed to prepare responseInsertStmt %v", err)
	}

	chatCountStmt, err = db.Prepare(`SELECT COUNT(*) FROM chat WHERE response_id = $1`)
	if err != nil {
		log.Fatalf("Failed to prepare chatCountStmt: %v", err)
	}

	client = openai.NewClient(os.Getenv("OPENAI_API_KEY"))

	r := mux.NewRouter()

	// Define your routes
	r.HandleFunc("/", handleIndex)
	r.HandleFunc("/submit-question", submitQuestion)
	r.HandleFunc("/submit-suggestion", suggestionSubmit)
	r.HandleFunc("/chat", streamResponse)
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
