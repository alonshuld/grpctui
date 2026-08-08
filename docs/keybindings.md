<!--
Generated from internal/ui/keys. Do not edit by hand: run

    go test ./internal/ui/keys -update
-->

# Keybindings

Every key grpctui binds, what the config file calls it, and what it does.
`grpctui keys` prints the same table with your own remapping applied, which is
the one to trust if you have edited any of them.

The `?` bar inside grpctui is the short version — the keys worth learning
first. This is all of them.

## Moving about

| Key | Action | What it does |
| --- | --- | --- |
| `up, k` | `up` | move the cursor up a row |
| `down, j` | `down` | move the cursor down a row |
| `pgup, ctrl+u` | `page-up` | scroll up a screenful |
| `pgdown, ctrl+d` | `page-down` | scroll down a screenful |
| `home, g` | `top` | jump to the first row |
| `end, G` | `bottom` | jump to the last row |
| `shift+left, H` | `scroll-left` | pan left, for a line wider than the panel |
| `shift+right, L` | `scroll-right` | pan right, for a line wider than the panel |
| `tab` | `next-panel` | move focus to the next panel |
| `shift+tab` | `prev-panel` | move focus to the previous panel |

## Filling in a request

| Key | Action | What it does |
| --- | --- | --- |
| `enter` | `select` | choose a method, or start editing a field |
| `right, l` | `expand` | open a nested message or a list |
| `left, h` | `collapse` | close it again |
| `space` | `toggle` | flip a bool, or pick the oneof variant under the cursor |
| `a, +` | `add` | append an item to a repeated field or a map |
| `d, -` | `remove` | delete the item under the cursor |

## Sending

| Key | Action | What it does |
| --- | --- | --- |
| `ctrl+s` | `send` | put the request on the wire |
| `ctrl+e` | `end-stream` | close the sending half and wait for the answer |
| `esc` | `cancel` | abandon the call, or leave the field being edited |
| `r` | `retry` | send the last request again |

## Reading a response

| Key | Action | What it does |
| --- | --- | --- |
| `w` | `raw-view` | swap the response between decoded JSON and its bytes |
| `D` | `diff` | compare this response with the last from the same method |
| `ctrl+p` | `capture` | bind a value out of the response to a variable |
| `X` | `export` | render the request as a grpcurl command |

## Saved requests

| Key | Action | What it does |
| --- | --- | --- |
| `[` | `history-prev` | step back through the requests already sent |
| `]` | `history-next` | step forward again |
| `ctrl+r` | `requests` | browse history and collections, searchable |
| `S` | `save` | put the request in the form into a collection |
| `/` | `filter` | type a query in the request browser |

## The session

| Key | Action | What it does |
| --- | --- | --- |
| `p` | `profiles` | switch connection profile |
| `e` | `environments` | switch environment, changing what {{name}} means |
| `v` | `variables` | list what the environment binds, and edit it |
| `t` | `traffic` | show what the passive proxy has seen (needs --proxy) |
| `T` | `themes` | switch palette, previewed as you move |
| `?` | `help` | expand the help bar |
| `q` | `quit` | leave grpctui |
| `ctrl+c` | `force-quit` | leave, even from inside a field being edited |

## Remapping

The middle column is the action's name. Bind one to different keys under
`keys:` in `~/.config/grpctui/config.yaml`, as a comma-separated list.
An empty value unbinds the action, which is how you get a key back that grpctui
has claimed and you want for something else:

```yaml
keys:
  send: ctrl+enter
  requests: ctrl+r,f3
  traffic: ""
```

Keys are spelled the way bubbletea spells them: `enter`, `esc`,
`tab`, `space`, `up`, `pgdown`, `ctrl+s`,
`shift+tab`, or a bare character.

Two actions sharing a key is refused at startup when at least one of them was
remapped — grpctui's own defaults overlap in places where the two can never both
be live, and refusing those would be refusing the built-in keymap. What is worth
catching is binding `send` to `q` and losing `quit` without
being told.

Every override is checked before any is reported, and nothing is applied unless
everything can be: a half-remapped keyboard is worse than an unremapped one,
because the half that worked hides the half that did not.

See also [docs/formats.md](formats.md) for the rest of the config file.
