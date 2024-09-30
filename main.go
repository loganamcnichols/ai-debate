package main

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	claude "github.com/anthropics/anthropic-sdk-go"
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

var client *claude.Client
var tmpls *template.Template
var db *sql.DB

func submitQuestion(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	idParam := params.Get("response-id")
	orientation := params.Get("orientation")
	responseID, err := uuid.Parse(idParam)
	if err != nil {
		log.Printf("unable to parse uuid: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	userMsg := r.FormValue("user-msg")
	if userMsg == "" {
		log.Printf("received empty user msg")
		tmpls.ExecuteTemplate(w, "err-user-input.html", "Message was empty")
		return
	}

	query := `INSERT INTO chat (response_id, user_msg) 
	                    VALUES ($1, $2) RETURNING id;`

	_, err = db.Exec(query, responseID, userMsg)
	if err != nil {
		log.Printf("error executing query %s, %v\n", params, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if orientation == "left" {
		tmpls.ExecuteTemplate(w, "question-submit-left.html", responseID)
	} else {
		tmpls.ExecuteTemplate(w, "question-submit-right.html", responseID)
	}
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
	responseID, err := uuid.Parse(idParam)
	if err != nil {
		log.Printf("unable to parse resopnse-id %s: %v\n", idParam, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	query := `SELECT first_move_innovation FROM response WHERE id = $1;`
	var innovateFirst bool
	err = db.QueryRow(query, responseID).Scan(&innovateFirst)
	if err != nil {
		log.Printf("failed to execute query %s: %v\n", query, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	query = `SELECT * FROM chat WHERE response_id = $1;`
	rows, err := db.Query(query, responseID)
	if err != nil {
		log.Printf("unable to execute query %s: %v\n", query, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var messages []claude.MessageParam
	var questionID int
	var formattedQuestion, formattedTransition, firstAnswer, secondAnswer string
	var nextRow QuestionRow

	for rows.Next() {
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
		if nextRow.CautionMsg == "" || nextRow.InnovationMsg == "" {
			break
		}

		messages = append(messages, claude.NewUserMessage(claude.NewTextBlock(formattedQuestion)))
		messages = append(messages, claude.NewAssistantMessage(claude.NewTextBlock(firstAnswer)))
		messages = append(messages, claude.NewUserMessage(claude.NewTextBlock(formattedTransition)))
		messages = append(messages, claude.NewAssistantMessage(claude.NewTextBlock(secondAnswer)))

		innovateFirst = !innovateFirst
	}

	var firstSystemPrompt []byte
	var secondSystemPrompt []byte
	if innovateFirst {
		firstSystemPrompt, err = os.ReadFile("PRO_INNOVATION_PROMPT.txt")
		if err != nil {
			log.Printf("unable to read innovation prompt: %v", err)
		}
		secondSystemPrompt, err = os.ReadFile("PRO_CAUTION_PROMPT.txt")
		if err != nil {
			log.Printf("unable to read caution prompt: %v", err)
		}
	} else {
		firstSystemPrompt, err = os.ReadFile("PRO_CAUTION_PROMPT.txt")
		if err != nil {
			log.Printf("unable to read caution prompt: %v", err)
		}
		secondSystemPrompt, err = os.ReadFile("PRO_INNOVATION_PROMPT.txt")
		if err != nil {
			log.Printf("unable to read innovation prompt: %v", err)
		}
	}

	messages = append(messages, claude.NewUserMessage(claude.NewTextBlock(formattedQuestion)))

	var firstMessageContent string
	stream := client.Messages.NewStreaming(context.TODO(), claude.MessageNewParams{
		Model:     claude.F(claude.ModelClaude_3_5_Sonnet_20240620),
		MaxTokens: claude.Int(1024),
		System:    claude.F([]claude.TextBlockParam{claude.NewTextBlock(string(firstSystemPrompt))}),
		Messages:  claude.F(messages),
	})
	for stream.Next() {
		event := stream.Current()
		switch delta := event.Delta.(type) {
		case claude.ContentBlockDeltaEventDelta:
			if delta.Text != "" {
				firstMessageContent += delta.Text
				fmt.Fprintf(w, "event: first-response\ndata: %s\n\n", convertToParagraphs(firstMessageContent))
				flusher.Flush()
			}
		}
	}

	messages = append(messages, claude.NewAssistantMessage(claude.NewTextBlock(firstMessageContent)))
	messages = append(messages, claude.NewUserMessage(claude.NewTextBlock(formattedTransition)))

	var secondMessageContent string
	stream = client.Messages.NewStreaming(context.TODO(), claude.MessageNewParams{
		Model:     claude.F(claude.ModelClaude_3_5_Sonnet_20240620),
		MaxTokens: claude.Int(1024),
		System:    claude.F([]claude.TextBlockParam{claude.NewTextBlock(string(secondSystemPrompt))}),
		Messages:  claude.F(messages),
	})
	for stream.Next() {
		event := stream.Current()
		switch delta := event.Delta.(type) {
		case claude.ContentBlockDeltaEventDelta:
			if delta.Text != "" {
				secondMessageContent += delta.Text
				fmt.Fprintf(w, "event: second-response\ndata: %s\n\n", convertToParagraphs(secondMessageContent))
				flusher.Flush()
			}
		}
	}
	fmt.Fprint(w, "event: success\ndata: success\n\n")

	fmt.Fprint(w, "event: close\ndata: Closing connection\n\n")

	if innovateFirst {
		update := `UPDATE chat SET innovation_msg = $1 caution_msg = $2 WHERE id = $3;`
		_, err = db.Exec(update, firstMessageContent, secondMessageContent, questionID)
		if err != nil {
			log.Printf("error executing update %s: %v", update, err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	} else {
		update := `UPDATE chat SET innovation_msg = $1, caution_msg = $2 WHERE id = $3`
		_, err := db.Exec(update, secondMessageContent, firstMessageContent, questionID)
		if err != nil {
			log.Printf("error executing update %s: %v\n", update, err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}
}

func createResponse() (uuid.UUID, error) {
	randomValue := rand.Float64()
	var query string
	if randomValue > 0.5 {
		query = "INSERT INTO response (first_move_innovation) VALUES (TRUE) RETURNING id"
	} else {
		query = "INSERT INTO response (first_move_innovation) VALUES (FALSE) RETURNING id"
	}
	var responseID uuid.UUID
	err := db.QueryRow(query).Scan(&responseID)
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

	query := "SELECT first_move_innovation FROM response WHERE id = $1"
	var innovationFirst bool
	err = db.QueryRow(query, responseID).Scan(&innovationFirst)
	if err != nil {
		log.Printf("unable to execute query %s: %v\n", query, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	query = "SELECT * FROM chat WHERE response_id = $1"
	res, err := db.Query(query, responseID)
	if err != nil {
		log.Printf("error executing query %s: %v\n", query, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	var questionRows []QuestionRow
	for res.Next() {
		var nextRow QuestionRow
		res.Scan(&nextRow.QuestionID, &nextRow.ResponseID, &nextRow.UserMsg,
			&nextRow.CautionMsg, &nextRow.InnovationMsg)
		questionRows = append(questionRows, nextRow)
	}

	var data = struct {
		QuestionRows    []QuestionRow
		InnovationFirst bool
		ResponseID      string
	}{
		QuestionRows:    questionRows,
		InnovationFirst: innovationFirst,
		ResponseID:      responseID.String(),
	}

	err = tmpls.ExecuteTemplate(w, "index.html", data)
	if err != nil {
		log.Printf("error executing template: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func main() {
	var err error
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

	client = claude.NewClient()

	r := mux.NewRouter()

	// Define your routes
	r.HandleFunc("/", handleIndex)
	r.HandleFunc("/submit-question", submitQuestion)
	r.HandleFunc("/chat-response", streamResponse)

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
