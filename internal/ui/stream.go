// stream.go holds the three streaming shapes. A stream is a chain of commands
// rather than a loop: each receive issues the next, so Update is never blocked
// on a server that has gone quiet.

package ui

import (
	"context"
	"errors"
	"io"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/render"
	"github.com/alonshuld/grpctui/internal/ui/panels"
)

// sendOnStream puts one request message on the stream, opening it first if it
// is not already running.
//
// That is the same keystroke meaning two things by context, and deliberately:
// ctrl+s is "send what the form says", and on a client-streaming call that is a
// thing you do repeatedly before ending the request stream with ctrl+e.
func (m *Model) sendOnStream(req panels.SendRequestMsg, md grpcclient.Metadata) tea.Cmd {
	// A method the client does not stream into carries exactly one request, so
	// there is no second message to send on its stream: ctrl+s starts a new one,
	// the same way a second ctrl+s replaces a unary call in flight.
	//
	// A client-streaming stream whose sending half the user has already closed
	// is deliberately not restarted. They ended the request stream and are
	// waiting for the answer to it; throwing that away and starting again is the
	// one thing they cannot have meant.
	if m.stream == nil || !req.Method.ClientStreaming {
		return m.startStream(req, md)
	}

	m.sendQueue = append(m.sendQueue, req.Request)
	return m.nextSend()
}

// startStream opens a streaming call, tagged with a sequence number from the
// same counter unary calls use — only one call, of either shape, is ever in
// flight.
func (m *Model) startStream(req panels.SendRequestMsg, md grpcclient.Metadata) tea.Cmd {
	m.abortCall()
	m.dropStream()
	m.callSeq++

	// No timeout: see [DefaultCallTimeout]. The stream ends when the server
	// finishes, when the user presses esc, or when the process does.
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelCall = cancel
	m.streamStart = m.now()

	// The first request message goes out as soon as the stream is open. A
	// method the client does not stream into has exactly one, so its sending
	// half is closed behind it — leaving it open would keep a finished request
	// waiting on a user who has nothing left to say.
	m.sendQueue = []proto.Message{req.Request}
	m.closeAfterQueue = !req.Method.ClientStreaming

	// Header names, never their values.
	m.logger.Info("opening stream",
		zap.String("target", m.client.Target()),
		zap.String("method", req.Method.FullName),
		zap.String("kind", string(req.Method.Kind())),
		zap.Strings("headers", md.Keys()),
	)

	tick := m.response.SetStreaming(req.Method)
	m.layout()
	return tea.Batch(tick, openStream(ctx, m.client, req.Method, md, m.callSeq))
}

// openStream opens the call off the Update goroutine.
func openStream(ctx context.Context, client Streamer, method grpcclient.Method, md grpcclient.Metadata, seq int) tea.Cmd {
	return func() tea.Msg {
		stream, err := client.InvokeStream(ctx, method, md)
		return streamOpenedMsg{seq: seq, stream: stream, err: err}
	}
}

// streamOpened starts the two halves of the stream running: the queue that
// feeds it, and the chain of receives that drains it.
func (m Model) streamOpened(msg streamOpenedMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.callSeq {
		// A stream the user has already moved on from. It is closed here rather
		// than left to the garbage collector: an abandoned gRPC stream holds its
		// call open until something cancels it.
		if msg.stream != nil {
			_ = msg.stream.Close()
		}
		m.logger.Debug("dropping a stale stream", zap.Int("seq", msg.seq))
		return m, nil
	}

	if msg.err != nil {
		m.cancelCall = nil
		m.sendQueue, m.closeAfterQueue = nil, false
		st, hasStatus := grpcclient.StatusOf(msg.err)
		m.response.FinishStream(msg.err.Error(), st, hasStatus, m.elapsed())
		m.layout()
		m.logger.Error("could not open the stream", zap.Error(msg.err))
		return m, nil
	}

	m.stream = msg.stream
	return m, tea.Batch(m.nextSend(), receive(m.stream, m.callSeq, m.elapsed, m.annotator()))
}

// nextSend issues the next thing the send queue is waiting to do: one message,
// or — once the queue has drained — the close the user asked for.
//
// Only one send runs at a time. gRPC allows a single sender per stream, and the
// order messages arrive in is part of what a client-streaming call means, so
// two of them may not race even when the transport would tolerate it.
func (m *Model) nextSend() tea.Cmd {
	if m.sending || m.stream == nil {
		return nil
	}

	if len(m.sendQueue) > 0 {
		req := m.sendQueue[0]
		m.sendQueue = m.sendQueue[1:]
		m.sending = true
		return send(m.stream, req, m.callSeq, m.elapsed)
	}

	if m.closeAfterQueue {
		m.closeAfterQueue = false
		m.sending = true
		return closeSending(m.stream, m.callSeq, m.elapsed)
	}
	return nil
}

// endSending closes the stream's sending half once whatever is queued has gone
// out. With no stream open the key does nothing, and says so in the log rather
// than on screen: it is not an error, just a key pressed at the wrong moment.
func (m *Model) endSending() tea.Cmd {
	if m.stream == nil {
		m.logger.Debug("no stream to end")
		return nil
	}
	m.closeAfterQueue = true
	return m.nextSend()
}

// send puts one request message on the stream, rendering it for the log on the
// same goroutine so that the panel receives text and never a protobuf message.
func send(stream grpcclient.Stream, req proto.Message, seq int, at func() time.Duration) tea.Cmd {
	return func() tea.Msg {
		body, format, err := protoschema.MarshalRequest(req)
		if err != nil {
			// The message is going out regardless — it is valid protobuf, or the
			// form would not have built it. Only the rendering failed, and saying
			// so in the log beats dropping the line.
			body, format = "(the request could not be rendered: "+err.Error()+")", protoschema.FormatText
		}

		if err := stream.Send(req); err != nil {
			return streamSentMsg{seq: seq, err: err, at: at()}
		}
		return streamSentMsg{seq: seq, body: body, format: format, at: at()}
	}
}

// closeSending closes the sending half off the Update goroutine.
func closeSending(stream grpcclient.Stream, seq int, at func() time.Duration) tea.Cmd {
	return func() tea.Msg {
		err := stream.CloseSend()
		return streamSentMsg{seq: seq, closedSend: true, err: err, at: at()}
	}
}

// receive reads one message off the stream. Each result issues the next
// receive, which is how a chain of tea.Cmds drains a stream without ever
// blocking Update.
func receive(stream grpcclient.Stream, seq int, at func() time.Duration, gloss func(proto.Message, string) []render.Annotation) tea.Cmd {
	return func() tea.Msg {
		msg, err := stream.Recv()
		switch {
		case errors.Is(err, io.EOF):
			return streamRecvMsg{seq: seq, done: true, at: at()}
		case err != nil:
			st, hasStatus := grpcclient.StatusOf(err)
			return streamRecvMsg{seq: seq, err: err, status: st, hasStatus: hasStatus, at: at()}
		}

		body, format, err := protoschema.Marshal(msg)
		if err != nil {
			return streamRecvMsg{seq: seq, err: err, at: at()}
		}
		return streamRecvMsg{seq: seq, body: body, format: format, notes: annotate(gloss, msg, body), at: at()}
	}
}

// streamSent records a request message having gone out, and starts whatever the
// queue holds next.
func (m Model) streamSent(msg streamSentMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.callSeq {
		m.logger.Debug("dropping a stale stream send", zap.Int("seq", msg.seq))
		return m, nil
	}
	m.sending = false

	switch {
	case errors.Is(msg.err, io.EOF):
		// The stream is already over and the reason belongs to the receiving
		// half, which is about to report it. Anything said here would be a
		// second, less informative version of the same event.
		m.sendQueue, m.closeAfterQueue = nil, false
		return m, nil

	case msg.err != nil:
		m.sendQueue, m.closeAfterQueue = nil, false
		m.response.AppendNote(msg.err.Error(), msg.at)
		m.logger.Warn("could not send on the stream", zap.Error(msg.err))

	case msg.closedSend:
		m.response.AppendNote("sending closed", msg.at)

	default:
		m.response.AppendSent(msg.body, msg.format, msg.at)
	}

	m.layout()
	return m, m.nextSend()
}

// streamReceived appends one response message to the log and asks for the next,
// or closes the log when the stream has ended.
func (m Model) streamReceived(msg streamRecvMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.callSeq {
		m.logger.Debug("dropping a stale stream message", zap.Int("seq", msg.seq))
		return m, nil
	}

	if msg.done || msg.err != nil {
		return m.finishStream(msg)
	}

	m.response.AppendReceived(msg.body, msg.format, msg.at, msg.notes)
	m.layout()
	return m, receive(m.stream, m.callSeq, m.elapsed, m.annotator())
}

// finishStream closes the stream out, leaving everything it carried on screen.
func (m Model) finishStream(msg streamRecvMsg) (tea.Model, tea.Cmd) {
	message := ""
	if msg.err != nil {
		message = msg.err.Error()
	}
	m.response.FinishStream(message, msg.status, msg.hasStatus, msg.at)

	m.cancelCall = nil
	m.dropStream()
	m.layout()

	m.logger.Debug("stream ended",
		zap.Bool("ok", msg.done),
		zap.Duration("took", msg.at),
	)
	return m, nil
}

// dropStream lets go of the stream, along with anything queued to go out on it.
func (m *Model) dropStream() {
	if m.stream != nil {
		_ = m.stream.Close()
		m.stream = nil
	}
	m.sendQueue = nil
	m.sending = false
	m.closeAfterQueue = false
}

// elapsed reports how long the current stream has been running. It is passed
// into the stream's commands as a function, so that each one timestamps itself
// when it actually happens rather than when it was created.
func (m Model) elapsed() time.Duration { return m.now().Sub(m.streamStart) }
