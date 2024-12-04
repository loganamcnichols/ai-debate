package realtime

type ConversationContent struct {
	Type  string `json:"type"`
	Audio string `json:"audio"`
	Text  string `json:"text"`
}

type ConversationItem struct {
	Type    string                `json:"type"`
	Role    string                `json:"role"`
	Content []ConversationContent `json:"content"`
}

type Voice string

const (
	ALLOY  Voice = "alloy"
	ASH    Voice = "ash"
	BALLAD Voice = "ballad"
	CORAL  Voice = "coral"
	ECHO   Voice = "echo"
	SHIMER Voice = "shimmer"
	VERSE  Voice = "verse"
)

type TranscriptionSettings struct {
	Model string `json:"model"`
}

type Session struct {
	Modalities              []string               `json:"modalities"`
	Instructions            string                 `json:"instructions"`
	Voice                   Voice                  `json:"voice"`
	TurnDetection           interface{}            `json:"turn_detection"`
	InputAudioTranscription *TranscriptionSettings `json:"input_audio_transcription"`
}

type ResponseContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ResponseOutput struct {
	Content []ResponseContent `json:"content"`
}

type Response struct {
	ID     string           `json:"id"`
	Output []ResponseOutput `json:"output"`
}

type ClientEventType string

const (
	SESSION_UPDATE             ClientEventType = "session.update"
	INPUT_AUDIO_BUFFER_APPEND  ClientEventType = "input_audio_buffer.append"
	INPUT_AUDIO_BUFFER_COMMIT  ClientEventType = "input_audio_buffer.commit"
	INPUT_AUDIO_BUFFER_CLEAR   ClientEventType = "input_audio_buffer.clear"
	CONVERSATION_ITEM_CREATE   ClientEventType = "conversation.item.create"
	CONVERSATION_ITEM_TRUNCATE ClientEventType = "conversation.item.truncate"
	CONVERSATION_ITEM_DELETE   ClientEventType = "conversation.item.delete"
	RESPONSE_CREATE            ClientEventType = "response.create"
	RESPONSE_CANCEL            ClientEventType = "response.cancel"
)

type ClientEvent struct {
	Type ClientEventType `json:"type"`
}

// ** session **
// session.update
type SessionUpdate struct {
	Type    ClientEventType `json:"type"`
	Session Session         `json:"session"`
}

// ** input audio buffer **
// input_audio_buffer.append
type InputAudioBufferAppend struct {
	Type  ClientEventType `json:"type"`
	Audio string          `json:"audio"`
}

// input_audio_buffer.commit
type InputAudioBufferCommit struct {
	Type ClientEventType `json:"type"`
}

// input_audio_buffer.clear
type InputAudioBufferClear struct {
	Type ClientEventType `json:"type"`
}

// ** conversation **
// * Item *
// conversation.item.create
type ConversationItemCreate struct {
	Type ClientEventType  `json:"type"`
	Item ConversationItem `json:"item"`
}

// conversation.item.truncate
type ConversationItemTruncate struct {
	Type         ClientEventType `json:"type"`
	ItemID       string          `json:"item_id"`
	ContentIndex int             `json:"content_index"`
	AudioEndMS   uint32          `json:"audio_end_ms"`
}

// conversation.item.delete
type ConversationItemDelete struct {
	Type ClientEventType `json:"type"`
}

// ** Response **
// https://platform.openai.com/docs/api-reference/realtime-client-events/response/create
type ResponseCreate struct {
	Type     ClientEventType `json:"type"`
	Response struct {
		Instructions string `json:"instructions"`
	} `json:"response"`
}

// response.cancel
type ResponseCancel struct {
	Type ClientEventType `json:"type"`
}

type ServerEventType string

const (
	ERROR                                                 ServerEventType = "error"
	SESSION_CREATED                                       ServerEventType = "session.created"
	SESSION_UPDATED                                       ServerEventType = "session.updated"
	CONVERSATION_CREATED                                  ServerEventType = "conversation.created"
	CONVERSATION_ITEM_CREATED                             ServerEventType = "conversation.item.created"
	CONVERSATION_ITEM_TRUNCATED                           ServerEventType = "conversation.item.truncated"
	CONVERSATION_ITEM_DELETED                             ServerEventType = "conversation.item.deleted"
	CONVERSATION_ITEM_INPUT_AUDIO_TRANSCRIPTION_COMPLETED ServerEventType = "conversation.item.input_audio_transcription.completed"
	CONVERSATION_ITEM_INPUT_AUDIO_TRANSCRIPTION_FAILED    ServerEventType = "conversation.item.input_audio_transcription.failed"
	INPUT_AUDIO_BUFFER_COMMITED                           ServerEventType = "input_audio_buffer.committed"
	INPUT_AUDIO_BUFFER_CLEARED                            ServerEventType = "input_audio_buffer.cleared"
	INPUT_AUDIO_BUFFER_SPEECH_STARTED                     ServerEventType = "input_audio_buffer.speech_started"
	INPUT_AUDIO_BUFFER_SPEECH_STOPPED                     ServerEventType = "input_audio_buffer.speech_stopped"
	RESPONSE_CREATED                                      ServerEventType = "response.created"
	RESPONSE_DONE                                         ServerEventType = "response.done"
	RESPONSE_OUTPUT_ITEM_ADDED                            ServerEventType = "response.output_item.added"
	RESPONSE_OUTPUT_ITEM_DONE                             ServerEventType = "response.output_item.done"
	RESPONSE_CONTENT_PART_ADDED                           ServerEventType = "response.content_part.added"
	RESPONSE_CONTENT_PART_DONE                            ServerEventType = "response.content_part.done"
	RESPONSE_TEXT_DELTA                                   ServerEventType = "response.text.delta"
	RESPONSE_TEXT_DONE                                    ServerEventType = "response.text.done"
	RESPONSE_AUDIO_TRANSCRIPT_DELTA                       ServerEventType = "response.audio_transcript.delta"
	RESPONSE_AUDIO_TRANSCRIPT_DONE                        ServerEventType = "response.audio_transcript.done"
	RESPONSE_AUDIO_DELTA                                  ServerEventType = "response.audio.delta"
	RESPONSE_AUDIO_DONE                                   ServerEventType = "response.audio.done"
	RESPONSE_FUNCTION_CALL_ARGUMENTS_DELTA                ServerEventType = "response.function_call_arguments.delta"
	RESPONSE_FUNCTION_CALL_ARGUMENTS_DONE                 ServerEventType = "response.function_call_arguments.done"
	RATE_LIMITS_UPDATED                                   ServerEventType = "rate_limits.updated"
)

type ServerEvent struct {
	Type ServerEventType `json:"type"`
}

// error

type Error struct {
	Type  string `json:"type"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Param   string `json:"param"`
	}
}

// session.created
type SessionCreated struct {
	Type ServerEventType `json:"type"`
}

// session.updated
type SessionUpdated struct {
	Type ServerEventType `json:"type"`
}

// ** conversation **
// conversation.created
type ConversationCreated struct {
	Type ServerEventType `json:"type"`
}

// * item *
// conversation.item.created
type ConversationItemCreated struct {
	EventID        string           `json:"event_id"`
	Type           ServerEventType  `json:"type"`
	PreviousItemID string           `json:"previous_item_id"`
	Item           ConversationItem `json:"item"`
}

// conversation.item.truncated
type ConversationItemTruncated struct {
	Type ServerEventType `json:"type"`
}

// conversation.item.deleted
type ConversationItemDeleted struct {
	Type ServerEventType `json:"type"`
}

// * input audio transcription *

// conversation.item.input_audio_transcription.completed
type ConversationItemInputAudioTranscriptionCompleted struct {
	Type ServerEventType `json:"type"`
}

// conversation.item.input_audio_transcription.failed
type ConversationItemInputAudioTranscriptionFailed struct {
	Type ServerEventType `json:"type"`
}

// ** input audio buffer **
// input_audio_buffer.committed
type InputAudioBufferCommited struct {
	Type ServerEventType `json:"type"`
}

// input_audio_buffer.cleared
type InputAudioBufferCleared struct {
	Type ServerEventType `json:"type"`
}

// input_audio_buffer.speech_started
type InputAudioBufferSpeechStarted struct {
	Type ServerEventType `json:"type"`
}

// input_audio_buffer.speech_stopped
type InputAudioBufferSpeechStopped struct {
	Type ServerEventType `json:"type"`
}

// ** response **

// response.created
type ResponseCreated struct {
	Type     ServerEventType `json:"type"`
	Response Response        `json:"response"`
}

// response.done
type ResponseDone struct {
	Type     ServerEventType `json:"type"`
	Response Response        `json:"response"`
}

// * output item *

// response.output_item.added
type ResponseOutputItemAdded struct {
	Type ServerEventType `json:"type"`
}

// response.output_item.done
type ResponseOutputItemDone struct {
	Type ServerEventType `json:"type"`
}

// * content part *
// response.content_part.added
type ResponseContentPartAdded struct {
	Type ServerEventType `json:"type"`
}

// response.content_part.done
type ResponseContentPartDone struct {
	Type ServerEventType `json:"type"`
}

// * text *
// response.text.delta
type ResponseTextDelta struct {
	Type ServerEventType `json:"type"`
}

// response.text.done
type ResponseTextDone struct {
	Type ServerEventType `json:"type"`
}

// * audio transcript
// response.audio_transcript.delta
type ResponseAudioTranscriptDelta struct {
	Type ServerEventType `json:"type"`
}

// response.audio_transcript.done
type ResponseAudoTranscriptDone struct {
	Type ServerEventType `json:"type"`
}

// * audio *
// response.audio.delta
type ResponseAudioDelta struct {
	EventID string          `json:"event_id"`
	Type    ServerEventType `json:"type"`
	ItemID  string          `json:"item_id"`
	Delta   string          `json:"delta"`
}

// response.audio.done
type ResponseAudioDone struct {
	Type       ServerEventType `json:"type"`
	Transcript string          `json:"transcript"`
}

// * function call arguments

// response.function_call_argument.delta
type ResponseFunctionCallArgumentsDelta struct {
	Type ServerEventType `json:"type"`
}

// response.function_call_arguments.done
type ResponseFunctionCallArgumentsDone struct {
	Type ServerEventType `json:"type"`
}

// ** rate limits **
// rate_limits.updated
type RateLimitsUpdated struct {
	Type ServerEventType `json:"type"`
}
