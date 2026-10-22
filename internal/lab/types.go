package lab

import "time"

const Namespace = "/lab"

var Rooms = []string{"general", "developers", "experiments"}

type Identity struct {
	VisitorID   string `json:"visitorId"`
	DisplayName string `json:"displayName"`
	NameStyle   string `json:"nameStyle"`
	AvatarColor string `json:"avatarColor"`
	PublicID    string `json:"publicId"`
}
type Sender struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Color string `json:"color,omitempty"`
}
type Payload struct {
	Text string `json:"text"`
}
type Event struct {
	ID        string    `json:"id"`
	Sequence  uint64    `json:"sequence"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Scope     string    `json:"scope"`
	RoomID    string    `json:"roomId,omitempty"`
	ReplyTo   string    `json:"replyTo,omitempty"`
	Sender    Sender    `json:"sender"`
	Payload   Payload   `json:"payload"`
}
type Counts struct {
	Total    int `json:"total"`
	Browser  int `json:"browser"`
	GoClient int `json:"goClient"`
}
type Participant struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Connections int    `json:"connections"`
}
type Status struct {
	SchemaVersion       int            `json:"schemaVersion"`
	BootID              string         `json:"bootId"`
	Status              string         `json:"status"`
	GeneratedAt         time.Time      `json:"generatedAt"`
	Connections         Counts         `json:"connections"`
	PresenceRevision    uint64         `json:"presenceRevision"`
	LastHumanActivityAt *time.Time     `json:"lastHumanActivityAt"`
	LastEventAt         *time.Time     `json:"lastEventAt"`
	BotReady            bool           `json:"botReady"`
	Rooms               map[string]int `json:"rooms"`
	SubmissionsEnabled  bool           `json:"submissionsEnabled"`
}
type Presence struct {
	Status
	Participants []Participant `json:"participants"`
}
type Snapshot struct {
	BootID    string            `json:"bootId"`
	Identity  Identity          `json:"identity"`
	Presence  Presence          `json:"presence"`
	Rooms     []string          `json:"rooms"`
	RoomID    string            `json:"roomId"`
	Positions map[string]uint64 `json:"positions"`
	History   []Event           `json:"history"`
	Truncated bool              `json:"truncated"`
}
type Request struct {
	NameStyle       string            `json:"nameStyle,omitempty"`
	VisitorID       string            `json:"visitorId,omitempty"`
	ClientMessageID string            `json:"clientMessageId,omitempty"`
	Text            string            `json:"text,omitempty"`
	Scope           string            `json:"scope,omitempty"`
	RoomID          string            `json:"roomId,omitempty"`
	BootID          string            `json:"bootId,omitempty"`
	Positions       map[string]uint64 `json:"positions,omitempty"`
	CorrelationID   string            `json:"correlationId,omitempty"`
	ReplyTo         string            `json:"replyTo,omitempty"`
}
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Ack struct {
	OK    bool     `json:"ok"`
	Data  any      `json:"data,omitempty"`
	Error *Failure `json:"error,omitempty"`
}

func success(data any) Ack             { return Ack{OK: true, Data: data} }
func failure(code, message string) Ack { return Ack{Error: &Failure{code, message}} }

type Receipt struct {
	EventID  string `json:"eventId"`
	Sequence uint64 `json:"sequence"`
}
type Echo struct {
	CorrelationID string `json:"correlationId"`
	Text          string `json:"text"`
	Error         string `json:"error,omitempty"`
}
