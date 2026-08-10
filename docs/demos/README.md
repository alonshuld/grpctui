# Demo recordings

The `.tape` files here record grpctui with
[VHS](https://github.com/charmbracelet/vhs), so a demo that goes out of date is
re-recorded rather than re-staged. The GIFs beside them are committed, because
the README embeds them and a page whose demo is a broken image is worse than a
page with no demo; `make demos` overwrites them in place:

```bash
go install github.com/charmbracelet/vhs@latest   # needs ffmpeg and ttyd
go install ./cmd/grpctui                         # the tapes type `grpctui`, so it has to be on PATH
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
output, which is the point of recording rather than drawing. Point them at a
target that serves reflection:

```bash
GRPCTUI_DEMO_TARGET=localhost:50051 make demos
```

The default is `localhost:50051`, and what the tapes expect there is a server
serving **the gRPC health service and reflection, and nothing else** — which is
a dozen lines of `health.NewServer()` and `reflection.Register()` around a
listener. Health is what they reach for because it is registered wherever
reflection is, and because it has one unary method (`Check`) and one
server-streaming one (`Watch`), which is every shape these three tapes need.

The methods are selected by cursor movement rather than by name, so a target
exposing more than that shows a different tree and the `Down` counts have to be
retuned — they currently assume `grpc.health.v1.Health` first, with `Check` one
row below it and `Watch` two. The timings assume a server that answers
promptly; a slow one wants the `Sleep` lines raised.

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
