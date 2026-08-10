# Streaming: open a server-streaming call and watch it arrive, then end it.
#
#     make demos          # or: vhs docs/demos/streaming.tape
#
# The target needs a server-streaming method. grpc.health.v1.Health.Watch is
# one, and is registered wherever reflection is — see README.md here.

Output docs/demos/streaming.gif

Require grpctui

Set Shell bash
Set FontSize 15
Set Width 1280
Set Height 720
Set Padding 12
Set TypingSpeed 50ms

Type "grpctui -config '' -history-file '' ${GRPCTUI_DEMO_TARGET:-localhost:50051}"
Enter
Sleep 3s

# Watch is the streaming method; the tree tags it «stream.
Down@300ms 2
Sleep 1200ms
Enter
Sleep 1500ms

Tab
Sleep 800ms
Enter
Sleep 400ms
Type "grpc.health.v1.Health"
Sleep 600ms
Escape
Sleep 800ms

# A stream has no timeout: it stays open, appending as the server speaks.
Ctrl+S
Sleep 6s

# esc is what ends a watch.
Escape
Sleep 2s
Type "q"
Sleep 1s
