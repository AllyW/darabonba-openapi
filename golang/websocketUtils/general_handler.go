package websocketutils

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/alibabacloud-go/tea/dara"
)

type GeneralMessageFormat string

const (
	GeneralMessageFormatText   GeneralMessageFormat = "text"
	GeneralMessageFormatBinary GeneralMessageFormat = "binary"
)

type GeneralMessage struct {
	Body   interface{}          `json:"body,omitempty"`
	Format GeneralMessageFormat `json:"format,omitempty"`
}

func (m *GeneralMessage) ToJSON() ([]byte, error) {
	return json.Marshal(m)
}

type GeneralMessageHandler func(session *dara.WebSocketSessionInfo, message *GeneralMessage) error

type GeneralWebSocketHandler struct {
	dara.AbstractWebSocketHandler
	messageHandler GeneralMessageHandler
}

func NewGeneralWebSocketHandler(messageHandler GeneralMessageHandler) *GeneralWebSocketHandler {
	return &GeneralWebSocketHandler{
		messageHandler: messageHandler,
	}
}

// General protocol: simple pass-through, just return the payload
func (h *GeneralWebSocketHandler) Encode(message *dara.WebSocketMessage) ([]byte, error) {
	if message == nil {
		return nil, fmt.Errorf("message cannot be nil")
	}
	// For General protocol, just return the payload
	return message.Payload, nil
}

// General protocol doesn't have special framing, just parse the payload
func (h *GeneralWebSocketHandler) Decode(data []byte, messageType dara.WebSocketMessageType) (*dara.WebSocketMessage, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("data cannot be empty")
	}

	message := &dara.WebSocketMessage{
		Type:      messageType,
		Timestamp: time.Now(),
		Headers:   make(map[string]string),
		Payload:   data,
	}

	var jsonData map[string]interface{}
	if err := json.Unmarshal(data, &jsonData); err == nil {
		if headers, ok := jsonData["headers"].(map[string]interface{}); ok {
			for k, v := range headers {
				message.Headers[k] = fmt.Sprintf("%v", v)
			}
		}
		if msgID, ok := jsonData["id"].(string); ok {
			message.MessageID = msgID
		}
		if corrID, ok := jsonData["correlation-id"].(string); ok {
			message.CorrelationID = corrID
		}
	}

	return message, nil
}

func (h *GeneralWebSocketHandler) HandleRawMessage(session *dara.WebSocketSessionInfo, message *dara.WebSocketMessage) error {
	generalMsg, err := ParseGeneralMessage(message)
	if err != nil {
		return err
	}

	if h.messageHandler != nil {
		return h.messageHandler(session, generalMsg)
	}

	return nil
}

func ParseGeneralMessage(message *dara.WebSocketMessage) (*GeneralMessage, error) {
	if message.Type == dara.WebSocketMessageTypeBinary {
		return &GeneralMessage{
			Body:   message.Payload,
			Format: GeneralMessageFormatBinary,
		}, nil
	}

	var body interface{}
	if err := json.Unmarshal(message.Payload, &body); err != nil {
		// If JSON parsing fails, return the original bytes
		return &GeneralMessage{
			Body:   message.Payload,
			Format: GeneralMessageFormatText,
		}, nil
	}

	return &GeneralMessage{
		Body:   body,
		Format: GeneralMessageFormatText,
	}, nil
}
