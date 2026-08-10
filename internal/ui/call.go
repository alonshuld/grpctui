// call.go holds a unary call, from the keystroke that starts one to the
// response that ends it, plus the previous-body cache a diff reads.

package ui

import (
	"context"
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/render"
	"github.com/alonshuld/grpctui/internal/ui/panels"
)

// send builds a request from the form and starts the call, or leaves the form
// showing why it could not.
//
// A call already in flight is replaced rather than protected: [Model.startCall]
// cancels it and the sequence number drops its answer, which is what the user
// pressing send again is asking for. Swallowing the keystroke instead would
// look like the key had stopped working.
func (m *Model) send() tea.Cmd {
	// A malformed header is refused here rather than at the transport layer, so
	// that the complaint lands on the row that caused it. The cursor is moved
	// onto that row and the panel given focus, because a panel capped at eight
	// lines can have the offending header scrolled out of sight.
	if err := m.metadata.Validate(); err != nil {
		m.metadata.FocusFirstInvalid()
		m.focus = focusMetadata
		m.syncFocus()
		m.layout()
		m.logger.Debug("refused to send with invalid headers", zap.Error(err))
		return nil
	}

	req, ok := m.request.Submit()
	if !ok {
		// The form grew an error row, so the split between it and the response
		// panel has to be recomputed.
		m.layout()
		return nil
	}
	return m.dispatch(req)
}

// dispatch sends one request, by whichever shape its method has, and records it
// in history on the way past.
//
// It is the single door every send goes through, which is why the history entry
// is written here rather than in each of the three places that put a message on
// the wire — including the one a [panels.LoadRequestMsg] arrives by, where
// there is no form submission to hang it off.
func (m *Model) dispatch(req panels.SendRequestMsg) tea.Cmd {
	// The headers are resolved here rather than in each of the three senders,
	// for the same reason the history entry is written here: there is one door,
	// and a reference nobody bound must refuse the call whichever way in it came.
	m.metadata.SetNotice("")

	md, err := m.headers()
	if err != nil {
		m.metadata.SetNotice(err.Error())
		m.focus = focusMetadata
		m.syncFocus()
		m.layout()
		m.logger.Debug("refused to send with unresolved headers", zap.Error(err))
		return nil
	}

	record := m.record(req, md)

	if req.Method.Kind() == grpcclient.KindUnary {
		return tea.Batch(record, m.startCall(req, md))
	}
	return tea.Batch(record, m.sendOnStream(req, md))
}

// headers are the metadata the next call carries, with every {{variable}}
// reference expanded.
//
// A reference nothing binds is refused rather than sent as written: an
// `authorization: Bearer {{token}}` that goes out literally comes back
// Unauthenticated, which is the most misleading answer the server could
// possibly give. Disabled headers are skipped — one parked precisely because it
// referred to something is not a reason to refuse the call.
func (m Model) headers() (grpcclient.Metadata, error) {
	md := m.metadata.Headers()
	set := m.bindings()

	var errs []error
	for i, h := range md {
		if h.Disabled {
			continue
		}
		value, err := set.Resolve(h.Value)
		if err != nil {
			errs = append(errs, fmt.Errorf("header %q: %w", h.Key, err))
			continue
		}
		md[i].Value = value
	}
	return md, errors.Join(errs...)
}

// annotator is the glossing function the call commands carry.
//
// It is a closure rather than the registry itself because the commands run off
// the Update goroutine, where reading the model would be a race. Capturing the
// registry and the clock — both values — is what lets a command annotate a
// response without touching anything the UI still owns.
func (m Model) annotator() func(proto.Message, string) []render.Annotation {
	registry, now := m.renderers, m.now
	if registry.Empty() {
		return nil
	}
	return func(msg proto.Message, body string) []render.Annotation {
		return registry.Annotate(msg, body, now())
	}
}

// annotate applies an annotator that may be nil, which is what a session with
// every renderer switched off has.
func annotate(f func(proto.Message, string) []render.Annotation, msg proto.Message, body string) []render.Annotation {
	if f == nil {
		return nil
	}
	return f(msg, body)
}

// startCall issues one unary call, tagged with a sequence number so that a
// result the user has moved on from can be discarded.
func (m *Model) startCall(req panels.SendRequestMsg, md grpcclient.Metadata) tea.Cmd {
	m.abortCall()
	m.dropStream()
	m.callSeq++

	ctx, cancel := context.WithTimeout(m.ctx, m.callTimeout)
	m.cancelCall = cancel

	// Header names, never their values: this log outlives the session and the
	// values are where the bearer token is.
	m.logger.Info("calling method",
		zap.String("target", m.client.Target()),
		zap.String("method", req.Method.FullName),
		zap.Strings("headers", md.Keys()),
	)

	tick := m.response.SetInFlight(req.Method)
	m.layout()
	return tea.Batch(tick, invoke(ctx, cancel, m.client, req, md, m.callSeq, m.annotator()))
}

// invoke runs the call off the Update goroutine, decoding the response there
// too so that the panel receives text and never a protobuf message.
//
// The raw encoding is produced here as well, for the same reason: it is work
// per response, and Update is not the place for any of it. A message that will
// not re-encode simply has no raw view — the call itself succeeded, and
// reporting it as failed over a rendering would be absurd.
func invoke(ctx context.Context, cancel context.CancelFunc, client Invoker, req panels.SendRequestMsg, md grpcclient.Metadata, seq int, gloss func(proto.Message, string) []render.Annotation) tea.Cmd {
	method := req.Method.FullName
	return func() tea.Msg {
		defer cancel()

		resp, err := client.InvokeUnary(ctx, req.Method, req.Request, md)
		if err != nil {
			st, ok := grpcclient.StatusOf(err)
			return callFinishedMsg{seq: seq, method: method, err: err, status: st, hasStatus: ok}
		}

		body, format, err := protoschema.Marshal(resp.Message)
		if err != nil {
			return callFinishedMsg{seq: seq, method: method, err: err, duration: resp.Duration}
		}

		wire, _ := protoschema.Wire(resp.Message)
		return callFinishedMsg{
			seq:      seq,
			method:   method,
			body:     body,
			format:   format,
			wire:     wire,
			notes:    annotate(gloss, resp.Message, body),
			duration: resp.Duration,
			timing:   resp.Timing,
		}
	}
}

func (m Model) finishCall(msg callFinishedMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.callSeq {
		m.logger.Debug("dropping stale call result", zap.Int("seq", msg.seq))
		return m, nil
	}
	m.cancelCall = nil

	if msg.err != nil {
		m.response.SetFailure(msg.err.Error(), msg.status, msg.hasStatus, msg.duration)
		m.layout()
		return m, nil
	}

	// The previous body is read before the new one replaces it, which is the
	// whole of the diff view: "the same method, last time".
	m.response.SetSuccess(panels.Result{
		Body:     msg.body,
		Format:   msg.format,
		Wire:     msg.wire,
		Previous: m.responses[msg.method],
		Took:     msg.duration,
		Timing:   msg.timing,
		Notes:    msg.notes,
	})
	m.remember(msg.method, msg.body)

	m.layout()
	return m, nil
}

// maxDiffMethods is how many methods keep a previous response for the diff
// view. It is a cache rather than a record: a method evicted from it reports
// its next response as a first one, which is the same thing it would have said
// before it was ever called.
const maxDiffMethods = 24

// remember keeps a response for the next call to the same method to be
// compared against.
func (m *Model) remember(method, body string) {
	if m.responses == nil {
		m.responses = make(map[string]string)
	}

	if _, held := m.responses[method]; !held && len(m.responses) >= maxDiffMethods {
		// Which one goes is arbitrary — map order — and that is fine: every
		// entry is equally a convenience, and choosing properly would mean
		// keeping an access order for a cache of two dozen strings.
		for evict := range m.responses {
			delete(m.responses, evict)
			break
		}
	}
	m.responses[method] = body
}

// abortCall cancels the call in flight, if any. The cancellation surfaces as an
// ordinary failed call, so the panel needs no separate "cancelled" state.
func (m *Model) abortCall() {
	if m.cancelCall == nil {
		return
	}
	m.cancelCall()
	m.cancelCall = nil
}

// retireCall abandons the call in flight, if any: it is cancelled and its
// sequence number bumped, so the answer already on its way is dropped rather
// than shown. A stream is dropped with it, queued messages and all.
//
// That is the difference from [Model.abortCall], which the user presses esc for
// and which is meant to put "Canceled" on screen. Here there is nothing left to
// put it under.
func (m *Model) retireCall() {
	if m.cancelCall == nil {
		return
	}
	retired := m.callSeq
	m.abortCall()
	m.dropStream()
	m.callSeq++
	m.logger.Debug("abandoned an in-flight call", zap.Int("seq", retired))
}
