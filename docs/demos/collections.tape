# Collections: save a request, find it again, replay it.
#
#     make demos          # or: vhs docs/demos/collections.tape
#
# This one writes to the collections directory, so it is pointed at a throwaway
# one — a demo must not add entries to whoever is recording it. Every tape here
# starts grpctui with `-config ''` and `-history-file ''` for the same reason,
# and because a recording that picks up local settings is not reproducible.

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

Type "grpctui -config '' -history-file '' -collections $GRPCTUI_DEMO_DIR ${GRPCTUI_DEMO_TARGET:-localhost:50051}"
Enter
Sleep 3s

Down@300ms 8
Enter
Sleep 1200ms

Tab
Sleep 600ms
Enter
Sleep 400ms
Type "usr_8f21c4"
Sleep 600ms
Escape
Sleep 800ms

# S saves the request as it stands, under collection/name. The prompt opens
# seeded with a suggestion, so clear it before typing one of our own.
Type "S"
Sleep 1s
Ctrl+U
Sleep 300ms
Type "smoke/get-user"
Sleep 1s
Enter
Sleep 2s

# ctrl+r browses history and every collection at once; / searches all of it.
Ctrl+R
Sleep 1500ms
Type "/"
Sleep 600ms
Type "user"
Sleep 1500ms
# enter settles on the filter, keeping the narrowed list and the cursor on the
# first match. Escape here would abandon the search and put the cursor back at
# the top of the whole list, and the recall below would load something else.
Enter
Sleep 800ms

# enter recalls it into the form — ctrl+s would send it outright.
Enter
Sleep 2s
Ctrl+S
Sleep 3s

Type "q"
Sleep 1s

Hide
Type "rm -rf $GRPCTUI_DEMO_DIR"
Enter
Show
