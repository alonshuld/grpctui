# A first call: discover a server, fill in a request, send it, read the answer.
#
#     make demos          # or: vhs docs/demos/unary.tape
#
# See README.md in this directory for what it records against.

Output docs/demos/unary.gif

Require grpctui

Set Shell bash
Set FontSize 15
Set Width 1280
Set Height 720
Set Padding 12
Set TypingSpeed 50ms

# Discovery: the whole API surface, with nothing configured.
Type "grpctui -config '' -history-file '' ${GRPCTUI_DEMO_TARGET:-localhost:50051}"
Enter
Sleep 3s

# Walk the tree and open a method. The form is generated from its input message.
Down@300ms 1
Sleep 1s
Enter
Sleep 1500ms

# Into the form, edit the first field.
Tab
Sleep 800ms
Enter
Sleep 500ms
Type "grpc.health.v1.Health"
Sleep 800ms
Escape
Sleep 1s

# Send, and read the response.
Ctrl+S
Sleep 3s

# The status code and the timing sit with the body.
Sleep 2s
Type "q"
Sleep 1s
