# Demo recordings

The `.tape` files here record grpctui with
[VHS](https://github.com/charmbracelet/vhs), so a demo that goes out of date is
re-recorded rather than re-staged. The GIFs themselves are not committed —
`make demos` writes them beside the tapes, and `.gitignore` keeps them out:

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
output, which is the point of recording rather than drawing. Point them at any
target that serves reflection:

```bash
GRPCTUI_DEMO_TARGET=localhost:50051 make demos
```

The default is `localhost:50051`. The methods the tapes select are chosen by
cursor movement rather than by name, so they work against whatever service the
target happens to expose — but the timings assume a server that answers
promptly, and a slow one wants the `Sleep` lines raised.

For the streaming tape the target needs at least one server-streaming method.
The gRPC health service has one (`grpc.health.v1.Health.Watch`) and is
registered on most servers that have reflection, which is why the tape reaches
for it.

## Editing a tape

`vhs` types keystrokes into a real terminal, so a tape is the same keys a person
would press — see [the keybinding reference](../keybindings.md). Two rules keep
the recordings watchable:

- Give discovery and each call a `Sleep` long enough to have finished. A GIF
  that cuts away mid-spinner looks like a tool that did not work.
- Type at `50ms` and move the cursor at `300ms`. Real speed is unreadable, and
  anything slower is tedious.
