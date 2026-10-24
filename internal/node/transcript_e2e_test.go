package node

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// segmentAdapter mimics ACP's live shape: text is flushed as a complete
// message before a tool call, then the next text segment follows it. The node
// must preserve that order in its transcript cache.
type segmentAdapter struct{}

func (segmentAdapter) Start(ctx context.Context, req adapters.StartRequest, sink adapters.EventSink) (adapters.StartResult, error) {
	go func() {
		sink.TextDelta(ctx, req.SessionID, "checking the file")
		sink.Message(ctx, req.SessionID, protocol.Message{Role: protocol.RoleAssistant, Content: "checking the file"})
		if ts, ok := sink.(adapters.ToolCallSink); ok {
			ts.ToolCall(ctx, req.SessionID, adapters.ToolCall{ID: "call-1", Title: "Read README.md", Kind: "read", Status: "pending"})
			ts.ToolCall(ctx, req.SessionID, adapters.ToolCall{ID: "call-1", Status: "completed"})
		}
		sink.TextDelta(ctx, req.SessionID, "it is small")
		sink.Message(ctx, req.SessionID, protocol.Message{Role: protocol.RoleAssistant, Content: "it is small"})
		sink.Done(ctx, req.SessionID)
	}()
	return adapters.StartResult{ExternalSessionID: "agent-ext-1"}, nil
}

func (segmentAdapter) SendMessage(context.Context, string, string) error { return nil }
func (segmentAdapter) Cancel(context.Context, string) error              { return nil }

// TestLiveTranscriptPreservesTextToolOrder drives the real node daemon against
// a scripted hub and asserts the transcript response keeps the segment/tool
// order (text → tool → text) and that tool updates replace one row.
func TestLiveTranscriptPreservesTextToolOrder(t *testing.T) {
	t.Setenv("FLEETDECK_EVENT_BUFFER_DIR", "")

	var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	done := make(chan struct{})
	transcript := make(chan protocol.SessionTranscriptResponse, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/node", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()

		var reg protocol.Envelope
		if err := c.ReadJSON(&reg); err != nil {
			return
		}
		_ = c.WriteJSON(protocol.NewEnvelope(protocol.MsgRegisterResponse, protocol.RegisterResponse{NodeID: "n1", Token: "tok"}))

		user := protocol.Message{ID: "user-1", Role: protocol.RoleUser, Content: "do it"}
		if err := c.WriteJSON(protocol.NewEnvelope(protocol.MsgTaskStart, protocol.TaskStartCommand{
			TaskID: "t1", SessionID: "s1", AgentType: "segment", Prompt: "do it", WorkspacePath: "/tmp/ws",
			UserMessage: &user,
		})); err != nil {
			return
		}

		sentTranscript := false
		for {
			var env protocol.Envelope
			if err := c.ReadJSON(&env); err != nil {
				return
			}
			switch env.Type {
			case protocol.MsgEvent:
				var we struct {
					Type string          `json:"type"`
					Data json.RawMessage `json:"data"`
				}
				if env.Decode(&we) != nil || we.Type != protocol.EvSessionStateChanged {
					continue
				}
				var sc protocol.SessionStateChanged
				if json.Unmarshal(we.Data, &sc) != nil || sc.Status != protocol.SessionDone {
					continue
				}
				if err := c.WriteJSON(protocol.NewEnvelope(protocol.MsgSessionTranscript, protocol.SessionTranscriptRequest{ReqID: "tr-1", SessionID: "s1"})); err != nil {
					return
				}
				sentTranscript = true
			case protocol.MsgSessionTranscriptResp:
				var resp protocol.SessionTranscriptResponse
				if env.Decode(&resp) != nil {
					return
				}
				if sentTranscript {
					select {
					case transcript <- resp:
					default:
					}
					close(done)
					return
				}
			}
		}
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	reg := adapters.NewRegistry()
	reg.Register("segment", func() adapters.Adapter { return segmentAdapter{} })
	n := New(Config{
		Name:      "transcript-e2e",
		HubURL:    "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/node",
		TokenFile: t.TempDir() + "/token",
		MachineID: "machine-transcript-e2e",
	}, reg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = n.runOnce(ctx) }()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the transcript response")
	}

	resp := <-transcript
	if len(resp.Messages) != 4 {
		t.Fatalf("transcript = %+v, want user/text/tool/text", resp.Messages)
	}
	if resp.Messages[0].ID != "user-1" || resp.Messages[0].Role != protocol.RoleUser {
		t.Fatalf("messages[0] = %+v, want the hub's prompt row", resp.Messages[0])
	}
	if resp.Messages[1].Role != protocol.RoleAssistant || resp.Messages[1].Content != "checking the file" {
		t.Fatalf("messages[1] = %+v, want the text before the tool", resp.Messages[1])
	}
	tool := resp.Messages[2]
	if tool.Role != protocol.RoleTool || tool.Tool == nil || tool.Tool.Title != "Read README.md" || tool.Tool.Status != "completed" {
		t.Fatalf("messages[2] = %+v, want the updated tool row", tool)
	}
	if resp.Messages[3].Role != protocol.RoleAssistant || resp.Messages[3].Content != "it is small" {
		t.Fatalf("messages[3] = %+v, want the text after the tool", resp.Messages[3])
	}
}
