package panels

import (
	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	"google.golang.org/protobuf/proto"

	"github.com/alonshuld/grpctui/internal/grpcclient"
	"github.com/alonshuld/grpctui/internal/protoschema"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// SendRequestMsg asks the root model to invoke a method. [Form.Submit] returns
// one only once every value in the form has converted cleanly into a request
// message, so the root never has to think about validation; sending it as a
// message is how anything else — v0.6's history, say — asks for the same call.
type SendRequestMsg struct {
	Service grpcclient.Service
	Method  grpcclient.Method

	// Request is what goes on the wire: every {{variable}} reference expanded.
	Request proto.Message

	// Template is the same request with its references left as they were
	// written, and Values the ones a protobuf message cannot hold — see
	// [protoschema.Form.Template]. Together they are what a history entry or a
	// collection records, so that a saved request stays portable between
	// environments and a captured token never reaches a file.
	//
	// Template is nil when it could not be built, which leaves the recorder to
	// fall back to Request; a request that went out is worth recording
	// imperfectly rather than not at all.
	Template proto.Message
	Values   map[string]string
}

// Layout constants for a field row.
const (
	maxNameWidth   = 28
	maxTypeWidth   = 22
	minValueWidth  = 8
	rowPrefixWidth = 2
	indentWidth    = 2

	// boolTrue and boolFalse are the two values a bool field stores once it has
	// been toggled. A toggled-off bool holds "false" rather than "", so that an
	// `optional bool` can be sent as an explicit false; an untouched one holds
	// "" and is left out of the request entirely. For a plain proto3 bool the
	// distinction costs nothing — setting one to false puts nothing on the wire.
	boolTrue  = "true"
	boolFalse = "false"

	// unsetLabel and emptyLabel mark the two states a field with explicit
	// presence can be in that an ordinary field cannot: never filled in, and
	// filled in with nothing. Only such a field ever shows either.
	unsetLabel = "unset"
	emptyLabel = `""`
)

// Glyphs for the two things a row can be beyond a plain field: something that
// holds other rows, and one of a set to pick between.
const (
	glyphExpanded  = "▾"
	glyphCollapsed = "▸"
	glyphOn        = "●"
	glyphOff       = "○"
)

// Form is the request form for the selected method: one row per field of the
// method's input message, filled in and sent with ctrl+s.
//
// Since v0.3 the rows are a tree rather than a list — a nested message expands
// to its fields, a repeated field to its items, an enum to its values, a oneof
// to its variants — but the panel walks a generic [protoschema.Node] and never
// asks what protobuf kind is underneath.
//
// It has two modes. Browsing moves the cursor between rows with j/k, the same
// as everywhere else in grpctui; editing hands every key to the field's text
// input, which is why q stops meaning "quit" while it lasts. The root model
// asks [Form.Editing] before claiming a key for itself.
type Form struct {
	keys   keys.KeyMap
	styles styles.Styles

	service grpcclient.Service
	method  grpcclient.Method
	schema  protoschema.Form

	// resolver expands the {{variable}} references in the form's values. It is
	// held here as well as on the schema because [Form.SetMethod] builds a fresh
	// tree, and a form that quietly stopped resolving on the second method
	// selected would be worse than one that never did.
	resolver protoschema.Resolver

	// rows is the flattened, visible tree: what the panel draws and what the
	// cursor indexes. It is rebuilt whenever the tree's shape changes.
	rows []*protoschema.Node

	// input edits whichever row the cursor is on. One input is enough because
	// only one row is ever edited at a time, and the tree grows and shrinks
	// under it — a per-row input would have to be created and destroyed with
	// every item added.
	input textinput.Model

	selected bool
	editing  bool
	cursor   int
	offset   int

	// fieldErrs holds the last build's complaints, keyed by [protoschema.Node]
	// path; notice holds a problem with the form as a whole.
	fieldErrs map[string]string
	notice    string

	// lines is the rendered panel, cached. One keystroke asks for it four to six
	// times over — the scroll clamp, the root model's layout, this panel's own
	// View — and building it is O(rows) of styled string assembly, which is the
	// difference between a responsive form and a sluggish one on the large
	// schemas v1.0 targets. Every mutation refreshes it; [Form.render] rebuilds
	// on the fly when it is nil, so a Form that has never been touched is still
	// correct.
	lines []line

	width   int
	height  int
	focused bool
}

// NewForm builds an empty request form.
func NewForm(km keys.KeyMap, st styles.Styles) Form {
	return Form{keys: km, styles: st, input: newFieldInput(st)}
}

// SetMethod rebuilds the form for a method, discarding whatever was typed into
// the previous one.
func (f *Form) SetMethod(svc grpcclient.Service, m grpcclient.Method) {
	f.service = svc
	f.method = m
	f.schema = protoschema.NewForm(m.InputDescriptor())
	f.schema.SetResolver(f.resolver)
	f.selected = true
	f.editing = false
	f.cursor = 0
	f.offset = 0
	f.fieldErrs = nil
	f.notice = ""
	f.input.Blur()

	f.rows = f.schema.Rows()
	f.resizeInput()
	f.moveTo(0)
}

// Load fills the form in from a message, which is how a request comes back out
// of history or a collection.
//
// It is called after [Form.SetMethod], on the fresh tree that built: loading
// into a form somebody has already typed into would merge two requests into one
// nobody asked for. Nothing is expanded by it — a nested message that came back
// filled says "set" and a list says how many items it holds, exactly as they do
// when the user fills them in and collapses them.
func (f *Form) Load(msg proto.Message) {
	if !f.selected || msg == nil {
		return
	}

	f.schema.Load(msg)
	f.rows = f.schema.Rows()
	f.moveTo(0)
}

// LoadValues puts a saved request's {{variable}} references back on the rows
// they were typed into, after [Form.Load] has rebuilt the shape around them.
func (f *Form) LoadValues(values map[string]string) error {
	if !f.selected || len(values) == 0 {
		return nil
	}

	err := f.schema.LoadValues(values)
	f.rows = f.schema.Rows()
	f.moveTo(f.cursor)
	return err
}

// SetResolver installs the expansion applied to every value on its way to the
// wire. Switching environment replaces it, which is what makes the same form
// mean a different request.
func (f *Form) SetResolver(r protoschema.Resolver) {
	f.resolver = r
	f.schema.SetResolver(r)

	// A row the previous environment could not resolve may resolve now, and the
	// other way round, so last send's per-row complaints are no longer about
	// anything. The next send says what is wrong under the new bindings.
	f.fieldErrs = nil
	f.refresh()
}

// SetNotice puts a line at the foot of the panel. It is the same line a failed
// build writes to, and it is cleared by the next send.
func (f *Form) SetNotice(text string) {
	f.notice = text
	f.refresh()
}

// newFieldInput builds the text input rows are edited through.
func newFieldInput(st styles.Styles) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.TextStyle = st.FieldValue
	ti.PlaceholderStyle = st.FieldDisabled

	// A blinking cursor would make every frame of the UI time-dependent, which
	// costs a redraw a second for no information and makes golden files racy.
	ti.Cursor.SetMode(cursor.CursorStatic)
	return ti
}

// Clear returns the panel to its empty state.
func (f *Form) Clear() {
	*f = Form{
		keys:   f.keys,
		styles: f.styles,
		input:  newFieldInput(f.styles),
		// The resolver belongs to the session rather than to the method, so
		// clearing the panel — which switching connection does — must not be a
		// way to stop {{variable}} references working.
		resolver: f.resolver,
		width:    f.width,
		height:   f.height,
		focused:  f.focused,
	}
	f.refresh()
}

// SetSize sets the panel's inner content area.
func (f *Form) SetSize(width, height int) {
	f.width = width
	f.height = height
	f.resizeInput()
	f.clampOffset()
}

// Focus gives the panel focus.
func (f *Form) Focus() {
	f.focused = true
	f.refresh()
}

// Blur removes focus from the panel, ending any edit in progress.
func (f *Form) Blur() {
	f.StopEditing()
	f.focused = false
	f.refresh()
}

// Focused reports whether the panel has focus.
func (f Form) Focused() bool { return f.focused }

// Editing reports whether a field is being typed into. While it is, the root
// model must leave every key alone but ctrl+c, ctrl+s and the panel switches.
func (f Form) Editing() bool { return f.editing }

// StopEditing ends any edit in progress, checking what was typed so that a typo
// is reported where it was made rather than when the call is sent.
func (f *Form) StopEditing() {
	if !f.editing {
		return
	}
	f.editing = false
	f.input.Blur()

	if node, ok := f.current(); ok {
		f.setFieldError(node)
	}
	f.refresh()
}

// Method returns the method the form was built for, if any.
func (f Form) Method() (grpcclient.Method, bool) { return f.method, f.selected }

// ContentHeight reports how many lines the form wants, so the root model can
// give the response panel everything the form does not need.
func (f Form) ContentHeight() int { return len(f.render()) }
