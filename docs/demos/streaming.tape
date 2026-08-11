# Streaming: open a server-streaming call and watch it arrive, then end it.
#
#     make demos          # or: vhs docs/demos/streaming.tape
#
# demo.v1.OrderService.WatchOrders is a watch rather than a list: it keeps
# sending for as long as somebody is listening, which is the point being made
# here. See README.md in this directory.

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

# WatchOrders is the streaming method; the tree tags it «stream.
Down@300ms 4
Sleep 1200ms
Enter
Sleep 1500ms

Tab
Sleep 1s

# A stream has no timeout: it stays open, appending as the server speaks.
Ctrl+S
Sleep 7s

# esc is what ends a watch.
Escape
Sleep 2s
Type "q"
Sleep 1s
