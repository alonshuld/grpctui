# Demo recordings

The `.tape` files here record grpctui with
[VHS](https://github.com/charmbracelet/vhs), so a demo that goes out of date is
re-recorded rather than re-staged. The GIFs beside them are committed, because
the README embeds them and a page whose demo is a broken image is worse than a
page with no demo; `make demos` overwrites them in place:

```bash
go install github.com/charmbracelet/vhs@latest   # needs ffmpeg and ttyd
go install ./cmd/grpctui                         # the tapes type `grpctui`, so it has to be on PATH
go run ./cmd/demoserver &                        # the target they record against
make demos                                       # writes docs/demos/*.gif
```

Each tape covers one of the workflows worth showing someone who has never run
grpctui:

| Tape | Workflow |
| --- | --- |
| `unary.tape` | Discover a server, fill in a request, send it, read the response |
| `streaming.tape` | Watch a server-streaming call arrive live, then end it |
| `collections.tape` | Save a request, find it again in the browser, replay it |

## What they record against

Every tape drives a real grpctui against a real server — there is no scripted
output, which is the point of recording rather than drawing.
[cmd/demoserver](../../cmd/demoserver) is that server: the made-up API in
[internal/demoapi](../../internal/demoapi), the gRPC health service, and
reflection, on `localhost:50051`. Its replies are canned, but nothing else about
it is: the descriptors are real, the streams are real, and grpctui discovers it
the same way it discovers anything.

Another target works too, as long as it serves reflection:

```bash
GRPCTUI_DEMO_TARGET=localhost:9090 make demos
```

What it will not do is choose the same methods. The tapes walk the tree by
cursor movement rather than by name, so their `Down` counts assume the demo
server's tree — six services in the order reflection sorts them, with
`demo.v1.UserService.GetUser` eight rows below the top and
`demo.v1.OrderService.WatchOrders` four. Point them elsewhere and the counts
want redoing. The timings assume a server that answers promptly; a slow one
wants the `Sleep` lines raised.

Two properties of the demo server exist for the tapes specifically.
`WatchOrders` never finishes on its own — it cycles its events for as long as
anybody is listening, so the streaming tape can make the point that a stream
has no timeout and `esc` is what ends it. And a reply's timestamps are filled in
when it is sent rather than written into the source, so the gloss beside one
reads "3 minutes ago" in a recording made today and in one made next year.

Each tape starts grpctui with `-config ''` and `-history-file ''`, and the
collections tape points `-collections` at a `mktemp -d`. A recording must not
add entries to whoever made it, and one that picked up local settings — a theme,
a profile, last week's history — would not be reproducible.

## Editing a tape

`vhs` types keystrokes into a real terminal, so a tape is the same keys a person
would press — see [the keybinding reference](../keybindings.md). Three rules
keep the recordings watchable, and correct:

- Give discovery and each call a `Sleep` long enough to have finished. A GIF
  that cuts away mid-spinner looks like a tool that did not work.
- Type at `50ms` and move the cursor at `300ms`. Real speed is unreadable, and
  anything slower is tedious.
- Write a modified key as `Ctrl+S`, not `Ctrl+s`: vhs rejects the lowercase
  form, and `vhs validate docs/demos/*.tape` is the cheap way to find out
  before a recording runs.
