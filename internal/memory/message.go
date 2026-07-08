package memory

import "time"

// Message is a single chat turn stored in the working-memory active buffer.
type Message struct {
	// Role identifies the speaker (e.g. "user", "assistant", "system").
	Role string `json:"role"`
	// Content is the message text.
	Content string `json:"content"`
	// Timestamp records when the message was appended. Append fills this in
	// with time.Now().UTC() when left zero.
	Timestamp time.Time `json:"timestamp,omitempty"`
}
