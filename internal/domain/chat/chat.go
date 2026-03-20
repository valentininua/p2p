package chat

import "time"

const (
	GlobalRoomName  = "global-chat"
	PresenceTimeout = 3 * time.Minute
	PingInterval    = 60 * time.Second
)

type MessageType string

const (
	MessageTypeChat  MessageType = "chat"
	MessageTypeJoin  MessageType = "join"
	MessageTypeLeave MessageType = "leave"
	MessageTypePing  MessageType = "ping"
)

type Message struct {
	Type      MessageType `json:"type"`
	From      string      `json:"from"`
	Nickname  string      `json:"nickname"`
	Text      string      `json:"text"`
	Encrypted string      `json:"enc,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
	Room      string      `json:"room"`
}

func NewMessage(messageType MessageType, from, nickname, room, text string, timestamp time.Time) Message {
	return Message{
		Type:      messageType,
		From:      from,
		Nickname:  nickname,
		Text:      text,
		Timestamp: timestamp,
		Room:      room,
	}
}

type Member struct {
	Nickname string
	LastSeen time.Time
}

func NewMember(nickname string, seenAt time.Time) *Member {
	return &Member{
		Nickname: nickname,
		LastSeen: seenAt,
	}
}

func (m *Member) IsOnline(now time.Time) bool {
	return m != nil && now.Sub(m.LastSeen) < PresenceTimeout
}

func CountOnline(members map[string]*Member, now time.Time) int {
	count := 0
	for _, member := range members {
		if member != nil && member.IsOnline(now) {
			count++
		}
	}

	return count
}
