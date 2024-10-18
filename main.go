package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/gorilla/schema"
	"github.com/sashabaranov/go-openai"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

var TEMPLATE_PARAMS = url.Values{
	"responseID":   []string{"[%RID%]"},
	"panelistID":   []string{"[%PID%]"},
	"supplierID":   []string{"[%SID%]"},
	"age":          []string{"[%AGE%]"},
	"gender":       []string{"[%GENDER%]"},
	"hispanic":     []string{"[%HISPANIC%]"},
	"ethnicity":    []string{"[%ETHNICITY%]"},
	"standardVote": []string{"[%STANDARD_VOTE%]"},
	"zip":          []string{"[%ZIP%]"},
}

var (
	innovationFirstStmt     *sql.Stmt
	surveyInsertStmt        *sql.Stmt
	chatTimeQueryStmt       *sql.Stmt
	responseQueryStmt       *sql.Stmt
	responseUpdateStmt      *sql.Stmt
	chatHistoryStmt         *sql.Stmt
	insertChatStmt          *sql.Stmt
	responseInsertStmt      *sql.Stmt
	lucidResponseInsertStmt *sql.Stmt
	chatCountStmt           *sql.Stmt
	markIncomplete          *sql.Stmt
)

var (
	TEMPLATE_LINK              *url.URL
	SURVEY_ENDPOINT            *url.URL
	PROJECT_ENDPOINT           *url.URL
	QUALIFICATION_ENDPOINT     *url.URL
	EXCHANGE_TEMPLATE_ENDPOINT *url.URL
	COMPLETE_URL               *url.URL
	SURVEYOR_CLIENT_ID         = 9676
	BLOCKED_VENDOR_TEMPLATE_ID = 1839
)

type Qualification struct {
	Name       string
	QuestionID int
	PreCodes   []string
}

type QuestionRow struct {
	QuestionID    uuid.UUID `db:"id"`
	ResponseID    uuid.UUID `db:"response_id"`
	UserMsg       string    `db:"user_msg"`
	SafetyMsg     string    `db:"safety_msg"`
	InnovationMsg string    `db:"innovation_msg"`
	CreateTime    time.Time `db:"create_time"`
}

type SurveyResponse struct {
	ID                *uuid.UUID `db:"id" schema:"id"`
	SurveyID          *uuid.UUID `db:"survey_id" schema:"survey_id"`
	ResponseID        string     `db:"response_id" schema:"response_id"`
	StartTime         *time.Time `db:"start_time" schema:"start_time"`
	WhichLLM          string     `db:"which_llm" schema:"which_llm"`
	AISpeed           string     `db:"ai_speed" schema:"ai_speed"`
	MuskOpinion       string     `db:"musk_opinion" schema:"musk_opinion"`
	PattersonOpinion  string     `db:"patterson_opinion" schema:"patterson_opinion"`
	KensingtonOpinion string     `db:"kensington_opinion" schema:"kensington_opinion"`
	Potholes          string     `db:"potholes" schema:"potholes"`
}

func (sq *SurveyResponse) Scan(row *sql.Row) error {
	return row.Scan(
		&sq.ID,
		&sq.SurveyID,
		&sq.ResponseID,
		&sq.StartTime,
		&sq.WhichLLM,
		&sq.AISpeed,
		&sq.MuskOpinion,
		&sq.PattersonOpinion,
		&sq.KensingtonOpinion,
		&sq.Potholes,
	)
}

func (sq *SurveyResponse) Update() error {
	_, err := responseUpdateStmt.Exec(
		sq.WhichLLM,
		sq.AISpeed,
		sq.MuskOpinion,
		sq.PattersonOpinion,
		sq.KensingtonOpinion,
		sq.Potholes,
		sq.ID,
	)
	return err
}

func (sq *SurveyResponse) NoneNull() bool {
	return (sq.SurveyID != nil &&
		sq.StartTime != nil &&
		sq.WhichLLM != "" &&
		sq.AISpeed != "" &&
		sq.MuskOpinion != "" &&
		sq.PattersonOpinion != "" &&
		sq.KensingtonOpinion != "" &&
		sq.Potholes != "")
}

func buildExpandedURL(baseURL string, params url.Values) string {
	queryParts := make([]string, 0, len(params))
	for key, values := range params {
		for _, value := range values {
			queryParts = append(queryParts, key+"="+value)
		}
	}
	queryString := strings.Join(queryParts, "&")
	return baseURL + "?" + queryString
}

type ChatMessage struct {
	QuestionID uuid.UUID
	Role       string
	Content    string
}

type QuantityType string

const (
	PRESCREENS QuantityType = "prescreens"
	COMPLETES  QuantityType = "completes"
)

type SurveyRequest struct {
	BusinessUnitID int          `json:"business_unit_id"`
	Locale         string       `json:"locale"`
	Name           string       `json:"name"`
	ProjectID      int          `json:"project_id"`
	CollectsPII    bool         `json:"collects_pii"`
	LiveURL        string       `json:"live_url"`
	Quantity       int          `json:"quantity"`
	QuantityType   QuantityType `json:"quantity_calc_type"`
	Status         string       `json:"status"`
	TestURL        string       `json:"test_url"`
	SurveyCPIUSD   float32      `json:"survey_cpi_usd"`
	StudyType      string       `json:"study_type"`
	Industry       string       `json:"industry"`
	CompletionRate float32      `json:"expected_incidence_rate"`
	SurveyMinutes  int          `json:"expected_completion_loi"`
}

func newSurveyRequest(name string, projectID int, prescreens int, chatTime int) *SurveyRequest {
	base := *TEMPLATE_LINK
	full := buildExpandedURL(base.String(), TEMPLATE_PARAMS)
	return &SurveyRequest{
		BusinessUnitID: 3175,
		Locale:         "eng_us",
		Name:           name,
		ProjectID:      projectID,
		CollectsPII:    false,
		LiveURL:        full,
		Quantity:       prescreens,
		QuantityType:   PRESCREENS,
		Status:         "awarded",
		TestURL:        full,
		SurveyCPIUSD:   0.5,
		StudyType:      "adhoc",
		Industry:       "other",
		CompletionRate: 1.0,
		SurveyMinutes:  chatTime + 5, // 5 minutes for answering the actual questions
	}
}

// ConvertString converts a string to a UUID
func ConvertString(value string) reflect.Value {
	id, err := uuid.Parse(value)
	if err != nil {
		return reflect.Value{}
	}
	return reflect.ValueOf(id)
}

func range18Plus() []string {
	var ages []string
	for i := 18; i < 100; i++ {
		ages = append(ages, fmt.Sprint(i))
	}
	return ages
}

func addQualifications(lucidID int) error {
	client := http.Client{}
	qualifations := []Qualification{
		{
			Name:       "AGE",
			QuestionID: 42,
			PreCodes:   range18Plus(),
		},
		{
			Name:       "GENDER",
			QuestionID: 43,
			PreCodes:   []string{},
		},
		{
			Name:       "HISPANIC",
			QuestionID: 47,
			PreCodes:   []string{},
		},
		{
			Name:       "ETHNICITY",
			QuestionID: 113,
			PreCodes:   []string{},
		},
		{
			Name:       "STANDARD_VOTE",
			QuestionID: 634,
			PreCodes:   []string{},
		},
		{
			Name:       "ZIP",
			QuestionID: 45,
			PreCodes:   []string{},
		},
	}
	for _, qual := range qualifations {
		data, err := json.Marshal(qual)
		if err != nil {
			return err
		}
		qualificationsEndpoint := *QUALIFICATION_ENDPOINT.JoinPath(fmt.Sprint(lucidID))
		req, err := http.NewRequest("POST", qualificationsEndpoint.String(), bytes.NewBuffer(data))
		if err != nil {
			return fmt.Errorf("unable to create qualification request: %v", err)
		}
		addLucidHeaders(req)

		_, err = client.Do(req)
		if err != nil {
			return fmt.Errorf("unable to add qualification: %v", err)
		}
	}
	return nil
}

func (q *QuestionRow) Scan(rows *sql.Rows) error {
	return rows.Scan(
		&q.QuestionID,
		&q.ResponseID,
		&q.UserMsg,
		&q.SafetyMsg,
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

type TimeoutMap[K, V any] struct {
	Map     sync.Map
	Timeout time.Duration
}

func (timeoutMap *TimeoutMap[K, V]) Store(key K, value V) {
	timeoutMap.Map.Store(key, value)
	timer := time.NewTimer(timeoutMap.Timeout)
	go func() {
		for range timer.C {
			timeoutMap.Map.Delete(key)
		}
	}()
}

func (timeoutMap *TimeoutMap[K, V]) Delete(key K) {
	timeoutMap.Map.Delete(key)
}

func (timeoutMap *TimeoutMap[K, V]) Load(key K) (V, bool) {
	var val V
	if data, ok := timeoutMap.Map.Load(key); ok {
		val, ok := data.(V)
		return val, ok
	}
	return val, false
}

type ChannelMap[K, V any] struct {
	Map     sync.Map
	Timeout time.Duration
}

func (channelMap *ChannelMap[K, V]) Store(key K, value chan V) {
	channelMap.Map.Store(key, value)
	timer := time.NewTimer(channelMap.Timeout)
	go func() {
		for range timer.C {
			channelMap.Map.Delete(key)
		}
	}()
}

func (channelMap *ChannelMap[K, V]) Delete(key K) {
	if data, ok := channelMap.Load(key); ok {
		channelMap.Map.Delete(key)
		close(data)
	}
}

func (channelMap *ChannelMap[K, V]) Load(key K) (chan V, bool) {
	if data, ok := channelMap.Map.Load(key); ok {
		if val, ok := data.(chan V); ok {
			return val, true
		}
	}
	return nil, false
}

// var client *claude.Client
var client *openai.Client
var tmpls *template.Template
var db *sql.DB
var chatMap = ChannelMap[uuid.UUID, string]{
	Timeout: 30 * time.Minute,
	Map:     sync.Map{},
}

var suggestionMap = TimeoutMap[uuid.UUID, []string]{
	Timeout: 30 * time.Minute,
	Map:     sync.Map{},
}

var prompts = []string{
	"If AI keeps improving at its current speed what will happen?",
	"Do you think the current level of AI safety is enough?",
	"What has been the impact of laws about AI?",
	"What would happen if we slowed down AI?",
}

func promptSuggest(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	idParam := params.Get("response-id")
	responseID, err := uuid.Parse(idParam)
	if err != nil {
		log.Printf("unable to parse uuid: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	availablePrompts, ok := suggestionMap.Load(responseID)
	if !ok {
		availablePrompts = promptCopy()
		suggestionMap.Store(responseID, availablePrompts)
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
	availablePrompts, ok := suggestionMap.Load(responseID)
	if !ok {
		log.Printf("not ok")
		availablePrompts = promptCopy()
		suggestionMap.Store(responseID, availablePrompts)
	}

	if suggestionIdx >= len(availablePrompts) {
		log.Printf("unable to parse uuid: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	userMsg := availablePrompts[suggestionIdx]

	availablePrompts = append(availablePrompts[:suggestionIdx], availablePrompts[suggestionIdx+1:]...)
	suggestionMap.Store(responseID, availablePrompts)

	userChannel, ok := chatMap.Load(responseID)
	if !ok {
		return
	}

	userChannel <- userMsg

	err = tmpls.ExecuteTemplate(w, "inactive-form", responseID)
	if err != nil {
		log.Printf("error executing template 'inactive-form': %v\n", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
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
		w.Header().Set("HX-Reswap", "none")
		return
	}

	var userChannel chan string
	userChannel, ok := chatMap.Load(responseID)
	if !ok {
		return
	}
	userChannel <- userMsg

	err = tmpls.ExecuteTemplate(w, "inactive-form", responseID)
	if err != nil {
		log.Printf("unable to execute template 'inactive-form': %v\n", err)
		return
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
	err := postTemplate(w, "intro-msg", "intro-msg-1", nil)
	if err != nil {
		return fmt.Errorf("unable to parse template 'intro-msg-1': %v", err)
	}
	time.Sleep(2 * time.Second)
	err = postTemplate(w, "intro-msg", "intro-msg-2", nil)
	if err != nil {
		return fmt.Errorf("unable to parse template 'intro-msg-2': %v", err)
	}
	time.Sleep(2 * time.Second)
	err = postTemplate(w, "intro-msg", "intro-msg-3", nil)
	if err != nil {
		return fmt.Errorf("unable to parse template 'intro-msg-3': %v", err)
	}
	time.Sleep(2 * time.Second)
	return nil
}

// func formatTemplateMessages(responseID uuid.UUID, innovateFirst bool) ([]ChatMessage, error) {
// 	messages := []ChatMessage{}
// 	rows, err := chatHistoryStmt.Query(responseID)
// 	if err != nil {
// 		return messages, fmt.Errorf("failed to execute chatHistoryStmt: %v", err)
// 	}
// 	defer rows.Close()
// 	var nextRow QuestionRow
// 	for rows.Next() {
// 		if err := nextRow.Scan(rows); err != nil {
// 			return messages, err
// 		}
// 		if innovateFirst {
// 			messages = append(messages, []ChatMessage{
// 				{
// 					QuestionID: nextRow.QuestionID,
// 					Role:       "user",
// 					Content:    nextRow.UserMsg,
// 				},
// 				{
// 					QuestionID: nextRow.QuestionID,
// 					Role:       "InnovateBot",
// 					Content:    nextRow.InnovationMsg,
// 				},
// 				{
// 					QuestionID: nextRow.QuestionID,
// 					Role:       "SafetyBot",
// 					Content:    nextRow.SafetyMsg,
// 				},
// 			}...)
// 		} else {
// 			messages = append(messages, []ChatMessage{
// 				{
// 					QuestionID: nextRow.QuestionID,
// 					Role:       "user",
// 					Content:    nextRow.UserMsg,
// 				},
// 				{
// 					QuestionID: nextRow.QuestionID,
// 					Role:       "SafetyBot",
// 					Content:    nextRow.SafetyMsg,
// 				},
// 				{
// 					QuestionID: nextRow.QuestionID,
// 					Role:       "InnovateBot",
// 					Content:    nextRow.InnovationMsg,
// 				},
// 			}...)
// 		}
// 		innovateFirst = !innovateFirst
// 	}
// 	return messages, nil
// }

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
					Content: formatSecondMessage("Safety Bot"),
				},
				{
					Role:    "assistant",
					Name:    "SafetyBot",
					Content: nextRow.SafetyMsg,
				}}...)
		} else {
			messages = append(messages, []openai.ChatCompletionMessage{
				{
					Role:    "user",
					Content: formatFirstMessage(nextRow.UserMsg, "Safety Bot"),
				},
				{
					Role:    "assistant",
					Name:    "SafetyBot",
					Content: nextRow.SafetyMsg,
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
	fmt.Fprintf(w, "event: %s-%s-delete\ndata: <p></p>\n\n", questionID.String(), "SafetyBot")
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
	now := time.Now()
	customFormat := now.Format("2006-01-02 15:04:05.000")
	log.Println(customFormat)

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

	var chatHistoryLen int
	err = chatCountStmt.QueryRow(responseID).Scan(&chatHistoryLen)
	if err != nil {
		log.Printf("failed to execute query for count")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	chatMap.Delete(responseID)
	userChannel := make(chan string, 1)
	if chatHistoryLen == 0 {
		userChannel <- "Opening argument"
		if err = streamIntroMsgs(w); err != nil {
			log.Printf("failed to load intro msgs: %v\n", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}
	chatMap.Store(responseID, userChannel)

	inactiveTimer := time.NewTimer(3 * time.Minute)
	keepAliveTicker := time.NewTicker(20 * time.Second)

	go func() {
		for {
			select {
			case <-r.Context().Done():
				chatMap.Delete(responseID)
				return
			case <-keepAliveTicker.C:
				log.Printf("hit keep alive ticker")
				fmt.Fprintf(w, "event: keep-alive\ndata: \n\n")
				flusher.Flush()
			case <-inactiveTimer.C:
				fmt.Fprintf(w, "event: inactive\ndata: \n\n")
				flusher.Flush()

				_, err := markIncomplete.Exec(responseID)
				if err != nil {
					log.Printf("failed to execute markIncomplete stmt %v\n", err)
				}
			}
		}
	}()

	for userMsg := range userChannel {
		print("in user msg loop")
		inactiveTimer.Reset(3 * time.Minute)
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
			secondPromptFile = "PRO_SAFETY_PROMPT.txt"

			firstBotName = "InnovateBot"
			secondBotName = "SafetyBot"

			fmt.Fprint(w, "event: innovation-first\ndata: true\n\n")

			flusher.Flush()

		} else {
			firstPromptFile = "PRO_SAFETY_PROMPT.txt"
			secondPromptFile = "PRO_INNOVATION_PROMPT.txt"

			firstBotName = "SafetyBot"
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
			log.Printf("unable to read safety prompt: %v", err)
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
			if err != nil {
				log.Printf("error executing updateChatStmt: %v", err)
				return
			}
		}
	}
}

func promptCopy() []string {
	dst := make([]string, len(prompts))
	copy(dst, prompts)
	return dst
}

func createHmacSHA1(message, secret string) []byte {
	key := []byte(secret)
	h := hmac.New(sha1.New, key)
	h.Write([]byte(message))
	return h.Sum(nil)
}

func generateHash(url, key string) string {
	rawHash := createHmacSHA1(url, key)
	base64Hash := base64.StdEncoding.EncodeToString(rawHash)

	// Replace '+' with '-', '/' with '_', and remove '='
	hash := strings.NewReplacer("+", "-", "/", "_").Replace(base64Hash)
	hash = strings.TrimRight(hash, "=")

	return hash
}

func completeSurvey(w http.ResponseWriter, survey SurveyResponse, responseID string) {
	if survey.SurveyID == nil {
		err := tmpls.ExecuteTemplate(w, "non-lucid-complete.html", nil)
		if err != nil {
			log.Printf("unable to execute non-lucid complete: %v\n", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
	} else {
		completeURL := *COMPLETE_URL
		params := completeURL.Query()
		params.Add("RIS", "10")
		params.Add("RID", responseID)
		completeURL.RawQuery = params.Encode()
		hash := generateHash(completeURL.String(), os.Getenv("COMPLETE_KEY"))
		params.Add("hash", hash)
		completeURL.RawQuery = params.Encode()
		w.Header().Set("HX-Redirect", completeURL.String())
	}
}

func handleSurvey(w http.ResponseWriter, r *http.Request) {
	var pageCount = 2
	var survey SurveyResponse
	decoder := schema.NewDecoder()
	decoder.IgnoreUnknownKeys(true)
	decoder.RegisterConverter(uuid.UUID{}, ConvertString)
	ID, err := uuid.Parse(r.URL.Query().Get("response-id"))
	if err != nil {
		log.Printf("unable to parse reponse id %s: %v\n", r.URL.Query().Get("response-id"), err)
		http.Error(w, "invalid response-id", http.StatusBadRequest)
		return
	}
	err = r.ParseForm()
	if err != nil {
		log.Printf("error parsing form: %v\n", err)
	}
	navigate := r.FormValue("navigate")
	page, err := strconv.Atoi(r.FormValue("page"))
	if err != nil {
		log.Printf("unable to convert page %s: %v", r.FormValue("page"), err)
		http.Error(w, "expected page parameter as an int", http.StatusBadRequest)
		return
	}
	err = survey.Scan(responseQueryStmt.QueryRow(ID))
	if err != nil {
		log.Printf("error scanning survey questions: %v\n", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if r.Method == "GET" {
		tmplName := fmt.Sprintf("survey-page-%d", page)
		err = tmpls.ExecuteTemplate(w, tmplName, survey)
		if err != nil {
			log.Printf("error execution template %s: %v", tmplName, err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	} else if r.Method == "POST" {
		var missing bool
		for _, vals := range r.PostForm {
			if vals[len(vals)-1] == "missing" {
				missing = true
			}
		}
		if navigate == "next" && !missing {
			page += 1
		} else if navigate == "previous" {
			page -= 1
		}

		err = decoder.Decode(&survey, r.PostForm)
		if err != nil {
			log.Printf("unable to decode form: %v\n", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		err = survey.Update()
		if err != nil {
			log.Printf("failed to update survey: %v\n", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		if page > pageCount {
			completeSurvey(w, survey, survey.ResponseID)
			return
		}
		tmplName := fmt.Sprintf("survey-page-%d", page)
		err = tmpls.ExecuteTemplate(w, tmplName, survey)
		if err != nil {
			log.Printf("error execution template %s: %v", tmplName, err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}
}

func createProject(name string) (int, error) {
	projectData := struct {
		Name     string `json:"name"`
		ClientID int    `json:"client_id"`
	}{
		Name:     name,
		ClientID: SURVEYOR_CLIENT_ID,
	}
	data, err := json.Marshal(projectData)
	if err != nil {
		return 0, fmt.Errorf("unable to marshal %v: %v", projectData, err)
	}
	req, err := http.NewRequest("POST", PROJECT_ENDPOINT.String(), bytes.NewBuffer(data))
	if err != nil {
		return 0, fmt.Errorf("unable to make request for %s with %v: %v", PROJECT_ENDPOINT, data, err)
	}

	addLucidHeaders(req)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("error sending request: %v", err)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("unable to read response body: %v", err)
	}

	var respTmpl struct {
		ID int `json:"id"`
	}
	err = json.Unmarshal(body, &respTmpl)
	if err != nil {
		return 0, fmt.Errorf("unable to unmarshal %s: %v", body, err)
	}

	return respTmpl.ID, nil
}

func addLucidHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", os.Getenv("LUCIDHQ_API_KEY"))
}

func createSurvey(name string, projectID int, prescreens int, chatTime int) (*int, error) {
	requestParams := newSurveyRequest(name, projectID, prescreens, chatTime)
	data, err := json.Marshal(requestParams)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal SurveyRequest: %v", err)
	}
	req, err := http.NewRequest("POST", SURVEY_ENDPOINT.String(), bytes.NewBuffer(data))
	if err != nil {
		return nil, fmt.Errorf("unable to create new survey request: %v", err)
	}

	addLucidHeaders(req)

	client := http.Client{}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to create new survey: %v", err)
	}
	var respTmpl struct {
		ID  int       `json:"id"`
		SID uuid.UUID `json:"sid"`
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("unable to read response boody: %v", err)
	}

	err = json.Unmarshal(body, &respTmpl)

	return &respTmpl.ID, err
}

func applyBlockedVendorTemplate(surveyID int) error {
	exchangeTemplateEndpoint := EXCHANGE_TEMPLATE_ENDPOINT.JoinPath(fmt.Sprint(surveyID), fmt.Sprint(BLOCKED_VENDOR_TEMPLATE_ID))
	req, err := http.NewRequest("POST", exchangeTemplateEndpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("unable to create request to %s: %v", EXCHANGE_TEMPLATE_ENDPOINT, err)
	}
	addLucidHeaders(req)

	client := http.Client{}
	_, err = client.Do(req)
	if err != nil {
		return fmt.Errorf("unable to complete block bendor template post: %v", err)
	}
	return nil
}

func setToLive(surveyID int) error {
	surveyEndpoint := *SURVEY_ENDPOINT.JoinPath(fmt.Sprint(surveyID))
	data, err := json.Marshal(struct {
		Status string `json:"status"`
	}{Status: "live"})
	if err != nil {
		return fmt.Errorf("unable to marshal json for set to live: %v")
	}
	req, err := http.NewRequest("PATCH", surveyEndpoint.String(), bytes.NewBuffer(data))
	if err != nil {
		return fmt.Errorf("unable to create request to set to live: %v", err)
	}
	addLucidHeaders(req)
	client := http.Client{}
	_, err = client.Do(req)
	if err != nil {
		return fmt.Errorf("unable to ")
	}
	return nil
}

func handleSurveyDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != os.Getenv("LUCIDHQ_API_KEY") {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	surveyID, err := uuid.NewUUID()
	if err != nil {
		log.Printf("unable to create new uuid: %v\n", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	chatTimeParam := r.FormValue("chatTime")
	prescreenParam := r.FormValue("prescreens")
	lucidLaunchParam := r.FormValue("lucidLaunch")

	chatTime, chatTimeErr := strconv.Atoi(chatTimeParam)
	prescreens, prescreensErr := strconv.Atoi(prescreenParam)
	lucidLaunch := lucidLaunchParam == "true"

	if chatTimeErr != nil {
		log.Printf("received invalid chatTime parameter %s: %v", chatTimeParam, chatTimeErr)
		http.Error(w, "recieved invalid chatTime parameter", http.StatusBadRequest)
		return
	}
	if prescreensErr != nil {
		log.Printf("received invalid prescreen parameter %s: %v", prescreenParam, prescreensErr)
		http.Error(w, "recieved invalid chatTime parameter", http.StatusBadRequest)
		return
	}

	surveyName := fmt.Sprintf("AI Debate %s", time.Now().Format(time.DateTime))
	var lucidID *int
	if lucidLaunch {
		projectID, err := createProject(surveyName)
		if err != nil {
			log.Println(err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		lucidID, err = createSurvey(surveyName, projectID, prescreens, chatTime)
		if err != nil {
			log.Println(err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		err = addQualifications(*lucidID)
		if err != nil {
			log.Println(err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		err = applyBlockedVendorTemplate(*lucidID)
		if err != nil {
			log.Println(err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		err = setToLive(*lucidID)
		if err != nil {
			log.Println(err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}

	_, err = surveyInsertStmt.Exec(surveyID, lucidID, chatTime)
	if err != nil {
		log.Printf("unable to execute surveyInsertStmt: %v\n", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "https://ai-debate.org/%s", surveyID.String())
}

func handleLucidIndex(w http.ResponseWriter, r *http.Request) {
	log.Printf("Received request params: %s", r.URL.RawQuery)
	var err error
	vars := mux.Vars(r)
	params := r.URL.Query()
	var responseID, panelistID, supplierID string
	var surveyID *uuid.UUID
	responseID = params.Get("responseID")
	panelistID = params.Get("panelistID")
	supplierID = params.Get("supplierID")
	parsedUUID, err := uuid.Parse(vars["surveyID"])
	if err != nil {
		log.Printf("error parsing surveyID %s: %v\n", vars["surveyID"], err)
	} else {
		surveyID = &parsedUUID
	}

	ageParam := params.Get("age")
	var age int
	if ageParam == "" {
		age = 0
	} else {
		age, err = strconv.Atoi(ageParam)
		if err != nil {
			log.Printf("error parsing page param %s: %v", ageParam, err)
			http.Error(w, "error parsing age param: %v\n", http.StatusBadRequest)
			return
		}
	}
	zip := params.Get("zip")
	gender := params.Get("gender")
	hispanic := params.Get("hispanic")
	ethnicity := params.Get("ethnicity")
	standardVote := params.Get("standardVote")

	if ageParam == "" ||
		zip == "" ||
		gender == "" ||
		hispanic == "" ||
		ethnicity == "" ||
		standardVote == "" {
		log.Println("one of the params is missing")
	}

	innovateFirst := (rand.Float32() > 0.5)

	var chatTime int
	err = chatTimeQueryStmt.QueryRow(&surveyID).Scan(&chatTime)
	if err != nil {
		log.Printf("error executing chatTime query: %v\n", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var id uuid.UUID
	err = lucidResponseInsertStmt.QueryRow(responseID, surveyID, panelistID, supplierID, age, zip, gender, hispanic, ethnicity, standardVote, innovateFirst).Scan(&id)
	if err != nil {
		log.Printf("error executing lucidResponseInsertStmt: %v\n", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var data = struct {
		QuestionRows []ChatMessage
		ResponseID   string
		ChatTime     int
	}{
		QuestionRows: []ChatMessage{},
		ResponseID:   id.String(),
		ChatTime:     chatTime,
	}

	err = tmpls.ExecuteTemplate(w, "index.html", data)
	if err != nil {
		log.Printf("error executing template: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	var err error
	var responseID *uuid.UUID
	innovateFirst := (rand.Float32() > 0.5)
	err = responseInsertStmt.QueryRow(innovateFirst).Scan(&responseID)
	if err != nil {
		log.Printf("unable to create a new uuid: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var data = struct {
		QuestionRows []ChatMessage
		ResponseID   string
		ChatTime     int
	}{
		QuestionRows: []ChatMessage{},
		ResponseID:   responseID.String(),
		ChatTime:     10,
	}

	err = tmpls.ExecuteTemplate(w, "index.html", data)
	if err != nil {
		log.Printf("error executing template: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func initURLs() {
	var err error
	TEMPLATE_LINK, err = url.Parse("https://ai-debate.org")
	if err != nil {
		log.Printf("unable to parse 'https://ai-debate.org'")
	}
	SURVEY_ENDPOINT, err = url.Parse("https://api.samplicio.us/demand/v2-beta/surveys")
	if err != nil {
		log.Printf("unable to parse 'https://api.samplicio.us/demand/v2-beta/surveys'")
	}
	PROJECT_ENDPOINT, err = url.Parse("https://api.samplicio.us/demand/v2-beta/projects")
	if err != nil {
		log.Printf("unable to parse 'https://api.samplicio.us/demand/v2-beta/projects'")
	}
	QUALIFICATION_ENDPOINT, err = url.Parse("https://api.samplicio.us/Demand/v1/SurveyQualifications/Create")
	if err != nil {
		log.Printf("unable to parse 'https://api.samplicio.us/Demand/v1/SurveyQualification/Create'")
	}
	EXCHANGE_TEMPLATE_ENDPOINT, err = url.Parse("https://api.samplicio.us/ExchangeTemplates/ApplyToSurvey")
	if err != nil {
		log.Printf("unable to parse 'https://api.samplicio.us/ExchangeTemplates/ApplyToSurvey'")
	}
	COMPLETE_URL, err = url.Parse("https://www.samplicio.us/router/ClientCallBack.aspx")
	if err != nil {
		log.Printf("unable to parse 'https://www.samplicio.us/router/ClientCallBack.aspx'")
	}
}

func main() {
	// Get current timestamp
	timestamp := time.Now().Format("2006-01-02_15-04-05")

	initURLs()

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

	innovationFirstStmt, err = db.Prepare(`SELECT innovate_first FROM response WHERE id = $1;`)
	if err != nil {
		log.Fatalf("Failed to prepare innovationFirstStmt: %v", err)
	}

	surveyInsertStmt, err = db.Prepare(`INSERT INTO survey (id, lucid_id, chat_time) VALUES ($1, $2, $3);`)
	if err != nil {
		log.Fatalf("Failed to prepare surveyInsertStmt: %v\n", err)
	}

	chatTimeQueryStmt, err = db.Prepare("SELECT chat_time FROM survey WHERE id = $1;")
	if err != nil {
		log.Fatalf("Failed to prepare chatTimeQueryStmt: %v\n", err)
	}

	responseQueryStmt, err = db.Prepare(`SELECT
																			 id,
																			 survey_id,
																			 response_id,
																			 start_time,
	                                     which_llm, 
	                                     ai_speed, 
	                                     musk_opinion, 
	                                     patterson_opinion, 
																			 kensington_opinion, 
																			 potholes
																FROM response WHERE id = $1`)
	if err != nil {
		log.Fatalf("Failed to prepare startTimeStmt: %v\n", err)
	}

	responseUpdateStmt, err = db.Prepare(`
	UPDATE response
	SET which_llm = $1,
			ai_speed = $2,
			musk_opinion = $3,
			patterson_opinion = $4,
			kensington_opinion = $5,
			potholes = $6
	WHERE id = $7`)
	if err != nil {
		log.Fatalf("Failed to prepare responseUpdateStmt: %v\n", err)
	}

	chatHistoryStmt, err = db.Prepare(`SELECT * FROM chat WHERE response_id = $1 ORDER BY created_time;`)
	if err != nil {
		log.Fatalf("Failed to prepare chatHistoryStmt: %v", err)
	}

	insertChatStmt, err = db.Prepare(`INSERT INTO chat (id, response_id, user_msg, safety_msg, innovation_msg)
																							VALUES ($1, $2, $3, $4, $5);`)
	if err != nil {
		log.Fatalf("Failed to prepare updateChatStmt: %v", err)
	}

	responseInsertStmt, err = db.Prepare(`INSERT INTO response (innovate_first) VALUES ($1) RETURNING id`)
	if err != nil {
		log.Fatalf("Failed to prepare responseInsertStmt %v", err)
	}

	lucidResponseInsertStmt, err = db.Prepare(`INSERT INTO response (response_id, survey_id, panelist_id, supplier_id, age, zip, gender, hispanic, ethnicity, standard_vote, innovate_first)
	                                                          VALUES ($1, $2,       $3,         $4,         $5,  $6,  $7,     $8,       $9,        $10,           $11)
																														RETURNING id`)
	if err != nil {
		log.Fatalf("Failed to prepare lucidResponseInsertStmt: %v", err)
	}

	chatCountStmt, err = db.Prepare(`SELECT COUNT(*) FROM chat WHERE response_id = $1`)
	if err != nil {
		log.Fatalf("Failed to prepare chatCountStmt: %v", err)
	}

	markIncomplete, err = db.Prepare(`UPDATE response SET completed = FALSE WHERE id = $1`)
	if err != nil {
		log.Fatalf("Failed to prepare markIncomplete stmt %v", err)
	}

	client = openai.NewClient(os.Getenv("OPENAI_API_KEY"))

	r := mux.NewRouter()

	r.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))
	r.HandleFunc("/submit-question", submitQuestion)
	r.HandleFunc("/submit-suggestion", suggestionSubmit)
	r.HandleFunc("/chat", streamResponse)
	r.HandleFunc("/prompt-suggestion", promptSuggest)
	r.HandleFunc("/deploy", handleSurveyDeploy)
	r.HandleFunc("/survey", handleSurvey)
	r.HandleFunc("/", handleIndex)
	r.HandleFunc("/{surveyID:[a-zA-Z0-9-]+}", handleLucidIndex)

	fmt.Println("Server is running on http://localhost:8080")
	err = http.ListenAndServe(":8080", r)
	if err != nil {
		fmt.Println("Error starting server:", err)
	}
}
