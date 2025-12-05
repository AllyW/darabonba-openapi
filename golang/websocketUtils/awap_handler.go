package websocketutils

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alibabacloud-go/tea/dara"
)

type AwapMessageType string

type AwapMessageFormat string

const (
	AwapMessageFormatText   AwapMessageFormat = "text"
	AwapMessageFormatBinary AwapMessageFormat = "binary"
)

type AwapMessage struct {
	Type    AwapMessageType        `json:"type"`
	ID      string                 `json:"id"`
	Headers map[string]string      `json:"headers,omitempty"`
	Payload interface{}            `json:"payload,omitempty"`
	Format  AwapMessageFormat      `json:"format,omitempty"`
	Status  int                    `json:"status,omitempty"`
	Error   string                 `json:"error,omitempty"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

func (m *AwapMessage) ToJSON() ([]byte, error) {
	return json.Marshal(m)
}

func (m *AwapMessage) WithHeader(key, value string) *AwapMessage {
	if m.Headers == nil {
		m.Headers = make(map[string]string)
	}
	m.Headers[key] = value
	return m
}

func (m *AwapMessage) WithFormat(format AwapMessageFormat) *AwapMessage {
	m.Format = format
	return m
}

// AwapMessageHandler is the callback function type for handling AWAP messages
type AwapMessageHandler func(session *dara.WebSocketSessionInfo, message *AwapMessage) error

type AwapWebSocketHandler struct {
	dara.AbstractWebSocketHandler
	messageHandler    AwapMessageHandler
	pendingRequests   map[string]chan *AwapMessage
	pendingRequestsMu sync.RWMutex
}

func NewAwapWebSocketHandler(messageHandler AwapMessageHandler) *AwapWebSocketHandler {
	return &AwapWebSocketHandler{
		messageHandler:  messageHandler,
		pendingRequests: make(map[string]chan *AwapMessage),
	}
}

// Encode implements WebSocketHandler.Encode for AWAP protocol
// AWAP format: headers (text lines) + "\n\n" + payload (JSON)
func (h *AwapWebSocketHandler) Encode(message *dara.WebSocketMessage) ([]byte, error) {
	if message == nil {
		return nil, fmt.Errorf("message cannot be nil")
	}

	var builder strings.Builder

	// Add headers
	if message.Headers != nil {
		for key, value := range message.Headers {
			builder.WriteString(key)
			builder.WriteString(":")
			builder.WriteString(value)
			builder.WriteString("\n")
		}
	}

	// Add MessageID as "id" header
	if message.MessageID != "" {
		builder.WriteString("id:")
		builder.WriteString(message.MessageID)
		builder.WriteString("\n")
	}

	// Add CorrelationID as "ack-id" header if present
	if message.CorrelationID != "" {
		builder.WriteString("ack-id:")
		builder.WriteString(message.CorrelationID)
		builder.WriteString("\n")
	}

	// Add separator between headers and payload
	builder.WriteString("\n")

	// Build complete message
	headerBytes := []byte(builder.String())
	result := make([]byte, len(headerBytes)+len(message.Payload))
	copy(result, headerBytes)
	copy(result[len(headerBytes):], message.Payload)

	return result, nil
}

func (h *AwapWebSocketHandler) Decode(data []byte, messageType dara.WebSocketMessageType) (*dara.WebSocketMessage, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("data cannot be empty")
	}

	// Find \n\n separator
	headerEndIndex := -1
	for i := 0; i < len(data)-1; i++ {
		if data[i] == '\n' && data[i+1] == '\n' {
			headerEndIndex = i
			break
		}
	}

	if headerEndIndex == -1 {
		return nil, fmt.Errorf("failed to parse AWAP message: no \\n\\n separator found")
	}

	headerBytes := data[:headerEndIndex]
	payloadBytes := data[headerEndIndex+2:] // Skip \n\n

	message := &dara.WebSocketMessage{
		Type:      messageType,
		Headers:   make(map[string]string),
		Payload:   payloadBytes,
		Timestamp: time.Now(),
	}

	// Parse header lines (key:value format)
	headerStr := string(headerBytes)
	headerLines := strings.Split(headerStr, "\n")

	for _, line := range headerLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		colonIndex := strings.Index(line, ":")
		if colonIndex <= 0 {
			continue
		}

		key := strings.TrimSpace(line[:colonIndex])
		value := strings.TrimSpace(line[colonIndex+1:])

		// Extract special fields
		switch key {
		case "id":
			message.MessageID = value
		case "ack-id":
			message.CorrelationID = value
		}

		message.Headers[key] = value
	}

	return message, nil
}

func (h *AwapWebSocketHandler) HandleRawMessage(session *dara.WebSocketSessionInfo, message *dara.WebSocketMessage) error {
	awapMsg, err := ParseAwapMessage(message)
	if err != nil {
		return fmt.Errorf("failed to parse AWAP message: %w", err)
	}

	var ackID string
	if awapMsg.Headers != nil {
		ackID = awapMsg.Headers["ack-id"]
	}

	if ackID != "" {
		if h.completeAwapRequest(ackID, awapMsg) {
			return nil // Request completed, no need to call user handler
		}
	}

	if h.messageHandler != nil {
		return h.messageHandler(session, awapMsg)
	}

	return nil
}

func (h *AwapWebSocketHandler) SendAwapRequestWithResponse(
	client dara.WebSocketClient,
	msgID string,
	messageText string,
	timeout time.Duration,
) (*AwapMessage, error) {
	if msgID == "" {
		return nil, errors.New("message ID cannot be empty for request-response pattern")
	}

	if messageText == "" {
		return nil, errors.New("message text cannot be empty for request-response pattern")
	}

	responseChan := make(chan *AwapMessage, 1)

	h.pendingRequestsMu.Lock()
	h.pendingRequests[msgID] = responseChan
	h.pendingRequestsMu.Unlock()

	defer func() {
		h.pendingRequestsMu.Lock()
		delete(h.pendingRequests, msgID)
		h.pendingRequestsMu.Unlock()
		close(responseChan)
	}()

	err := client.SendText(messageText)
	if err != nil {
		return nil, fmt.Errorf("failed to send message: %w", err)
	}

	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	select {
	case response := <-responseChan:
		return response, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("request timeout after %v waiting for response to message ID: %s", timeout, msgID)
	}
}

func (h *AwapWebSocketHandler) completeAwapRequest(messageID string, response *AwapMessage) bool {
	h.pendingRequestsMu.RLock()
	responseChan, exists := h.pendingRequests[messageID]
	h.pendingRequestsMu.RUnlock()

	if !exists || responseChan == nil {
		return false
	}

	select {
	case responseChan <- response:
		return true
	default:
		// Channel is full or closed
		return false
	}
}

func ParseAwapMessage(message *dara.WebSocketMessage) (*AwapMessage, error) {
	data := message.Payload

	headerEndIndex := -1
	for i := 0; i < len(data)-1; i++ {
		if data[i] == '\n' && data[i+1] == '\n' {
			headerEndIndex = i
			break
		}
	}

	awapMsg := &AwapMessage{
		Headers: make(map[string]string),
	}

	if headerEndIndex == -1 {
		return nil, fmt.Errorf("failed to parse AWAP message: no \\n\\n separator found")
	}

	headerBytes := data[:headerEndIndex]
	payloadBytes := data[headerEndIndex+2:] // Skip \n\n

	headerStr := string(headerBytes)
	headerLines := strings.Split(headerStr, "\n")
	for _, line := range headerLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		colonIndex := strings.Index(line, ":")
		if colonIndex > 0 {
			key := strings.TrimSpace(line[:colonIndex])
			value := strings.TrimSpace(line[colonIndex+1:])
			// Extract AWAP-required fields
			switch key {
			case "type":
				awapMsg.Type = AwapMessageType(value)
			case "id":
				awapMsg.ID = value
			case "status":
				if status, err := strconv.Atoi(value); err == nil {
					awapMsg.Status = status
				}
			case "error":
				awapMsg.Error = value
			default:
				// Only non-AWAP-required fields go into Headers map
				awapMsg.Headers[key] = value
			}
		}
	}

	if len(payloadBytes) > 0 {
		var payload interface{}
		if err := json.Unmarshal(payloadBytes, &payload); err != nil {
			awapMsg.Payload = payloadBytes
		} else {
			awapMsg.Payload = payload
		}
	}

	if message.Type == dara.WebSocketMessageTypeBinary {
		awapMsg.Format = AwapMessageFormatBinary
	} else {
		awapMsg.Format = AwapMessageFormatText
	}

	return awapMsg, nil
}
