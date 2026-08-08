# Collections: save a request, find it again, replay it.
#
#     make demos          # or: vhs docs/demos/collections.tape
#
# This one writes to the collections directory, so it is pointed at a throwaway
# one — a demo must not add entries to whoever is recording it.

Output docs/demos/collections.gif

Require grpctui

Set Shell bash
Set FontSize 15
Set Width 1280
Set Height 720
Set Padding 12
Set TypingSpeed 50ms

Hide
Type "export GRPCTUI_DEMO_DIR=$(mktemp -d)"
Enter
Show

Type "grpctui -collections $GRPCTUI_DEMO_DIR ${GRPCTUI_DEMO_TARGET:-localhost:50051}"
Enter
Sleep 3s

Down@300ms 2
Enter
Sleep 1200ms

Tab
Sleep 600ms
Enter
Sleep 400ms
Type "grpc.health.v1.Health"
Sleep 600ms
Escape
Sleep 800ms

# S saves the request as it stands, under collection/name.
Type "S"
Sleep 1s
Type "smoke/health"
Sleep 1s
Enter
Sleep 2s

# ctrl+r browses history and every collection at once; / searches all of it.
Ctrl+r
Sleep 1500ms
Type "/"
Sleep 600ms
Type "health"
Sleep 1500ms
Escape
Sleep 800ms

# enter recalls it into the form — ctrl+s would send it outright.
Enter
Sleep 2s
Ctrl+s
Sleep 3s

Type "q"
Sleep 1s

Hide
Type "rm -rf $GRPCTUI_DEMO_DIR"
Enter
Show
