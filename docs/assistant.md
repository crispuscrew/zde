# zde - The Assistant

A design. Nothing here is built. Terms: [`glossary.md`](glossary.md).
Principles: [`vision.md`](vision.md), 2, 4, 6, 7 and 9. Layers:
[`delivery.md`](delivery.md). Phase: [`roadmap.md`](roadmap.md), 0.3.

The assistant is a model that can drive this desktop: it reads what is on the
machine and it performs a short list of things a keypress can undo. It is
reached over the network, from this machine or from one named address on the
local network, and a caller is authenticated by a certificate the owner
approved by reading its fingerprint off a screen. It is surfaced as MCP, so a
model calls it as tools.

Everything below is an argument about what that may reach and what it may not,
and the order is deliberate: the boundary question comes first, because if it
has no answer then nothing after it is worth writing down.

## 1. The boundary, before anything else

zded's socket check is one comparison. `allowPeer` (`internal/zded/server.go`)
reads `SO_PEERCRED` and refuses any peer whose uid is not zded's own. The
socket is 0600 in a 0700 directory, so the check is belt and braces with the
file mode, and both say the same thing: **any process running as the owner may
call any of zded's 54 methods.**

That is the whole of it, and it is what makes the obvious design cosmetic. Put
the TLS listener and the allowlist in a second binary running as the owner,
and the allowlist is a property of that process's address space. It stops the
assistant from calling `system.power` by accident. It stops nothing at all
about an assistant that has been taken over, because a process that has been
taken over does not consult its own allowlist: it opens the socket and asks
for `palette.run`. The separation would be a statement of intent enforced by
the thing whose intent is in question.

So the second binary is right, and it is not sufficient. Four ways to make it
sufficient were considered.

**A restricted peer identity inside zded, with no second uid.** zded learns to
recognise the assistant and applies a smaller table to it. There is nothing to
recognise it by. `SO_PEERCRED` carries uid, gid and pid; uid and gid are the
same as everything else in the session, and pid is a number whose meaning
requires reading `/proc/<pid>/exe`, which is a race against `exec` and a lie
about a process that has been injected into rather than replaced. A token in a
file does not help either: a token readable by a process running as the owner
is readable by every process running as the owner, which makes it a label and
not a credential. This option does not exist without the next one.

**A socket per capability, with different modes.** File modes discriminate by
uid and gid. With one uid, two sockets at 0600 owned by the owner are two doors
with one key. It buys a place to write the restriction down and no enforcement
whatever. Refused as a boundary on its own. Half of it survives into the
decision below, and only because the uid arrives with it: a second socket is
where the restricted table is answered, and it is a boundary because of who may
open it rather than because there are two of them.

**A separate uid, with an explicit grant in `allowPeer`.** This is the one that
buys something, and what it buys is worth being precise about. A compromised
assistant running as `zde-assist` cannot read `~/.local/state/zde/journal.jsonl`
or `history.json`, cannot read the desk manifests, cannot reach the session
bus - which is where notifications and MPRIS live and which is therefore where
half of the interesting text on this machine is - cannot ptrace zded, cannot
write `~/.config/niri/dynamic.kdl`, and cannot call the 46 existing methods
that are not on its table. That third-from-last one is not decorative:
`dynamic.kdl` is the one niri include zde writes at runtime, niri reloads the
tree when it is written, and a later `binds` block replaces the keys it names
again ([`roadmap.md`](roadmap.md), include precedence). A process that can
write that file can put a command on a key. So the uid is the difference
between "a bug in the assistant costs the nine things the assistant does" and
"a bug in the assistant costs the machine".

It is not free, and the cost is exactly the thing to be honest about: **zde's
model is one user's session, and this is the first place that stops being
true.** `XDG_RUNTIME_DIR` is 0700 and belongs to the owner, so the restricted
socket cannot live beside `zded.sock`; it needs a directory of its own outside
it. The unit cannot be a home-manager user unit, because home-manager writes
one user's units, so the assistant moves to layer 0 while every other zde
component sits in layer 1 ([`delivery.md`](delivery.md)). And it cannot be a
zinc container, because the grant it needs is a zde socket and principle 7 says
that socket is never mounted into one - the assistant is the exact case that
principle was written against, and a uid is the answer that needs no
reinterpretation of a frozen document.

**Accept it, and say so.** Defensible in exactly one shape and no other: a
listener bound to loopback and nothing else adds no authority to this machine.
Anything that can connect to 127.0.0.1 is already a process here; if it runs as
the owner it has `zded.sock` already and the assistant is a convenience, and if
it does not, the certificate is what stops it. The moment the listener answers
on a LAN address, a party who is not on this machine can address zde, and then
"the allowlist is as strong as one process" is a sentence about somebody else's
process.

### The decision

The restricted table lives in zded, on a socket of its own, answered only for a
uid that is not the owner's. The assistant holds the two TLS listeners
(section 7), the certificates and the grants, and holds no authority that zded
has not already agreed to hand that uid. Concretely:

- Layer 0 creates the user `zde-assist` and the group `zde-assist`, and one
  tmpfiles line: a directory `/run/zde`, owned by the owner, group
  `zde-assist`, mode 0750. Not under `XDG_RUNTIME_DIR`, which is the owner's
  alone and which the kernel clears between logins.
- zded binds `/run/zde/assist.sock` at 0660, group `zde-assist`, beside the
  socket it already binds. `Listen` already dials a stale socket before
  removing it, which matters more here than it does in the runtime directory:
  `/run` survives a logout and `XDG_RUNTIME_DIR` does not.
- `allowPeer` gains one branch. The owner's uid reaches the whole table on the
  main socket. The assistant's uid reaches a **second, enumerated dispatch
  table** on this one, and nothing else - not the same `Dispatch` with a filter
  in front of it. A filter is one forgotten `case` away from being wrong, and
  the switch it filters is 41 cases long and grows. A table that lists what it
  answers cannot silently gain a method, which is principle 9 applied to a
  code shape rather than to a feature.
- **The check goes at the top of `handle`'s read loop, not in `Dispatch`.**
  This is the part that survives a careful reading and is still wrong, so it is
  written down rather than left to whoever implements it. Three methods never
  reach `Dispatch` at all: `events` is answered at `server.go:581`, `ask.run`
  at `:598`, and `clip.history` **with exactly one argument** at `:612`, each
  with a `continue` before the `k.reply(s.Dispatch(req))` at `:629`. Two of
  those three are among the worst things on the socket - `ask.run` spawns a
  subprocess in a process group of its own, and `clip.history <id>` writes the
  session clipboard. And `Dispatch` *names* `events` and `ask.run` in its own
  switch, returning a sentence explaining that the connection handles them, so
  a table built in `Dispatch` looks complete and covers 51 of 54. The check
  belongs before the first `if`, and the reason belongs in a comment beside it.
- **The socket must be `chown`ed, not only `chmod`ed.** `Listen` does
  `os.Chmod(path, 0o600)` and nothing else (`server.go:399`), so a socket
  created by zded takes zded's egid, which is the owner's, and a 0660 mode
  reaches nobody new. Either `/run/zde` is setgid `zde-assist` so the socket
  inherits the group, or zded chowns the socket after binding. The setgid
  directory is the one that survives somebody editing `Listen` later.
- The list of methods reachable from that socket therefore lives in zded, in
  one place, and the assistant's tool surface is a subset of it. Two lists that
  must agree would drift; here the outer one refuses, so the inner one cannot
  exceed it however wrong it gets.
- **It is a table of method, arity and argument value, not of methods.** Six
  verbs mean two different things depending on how many arguments they are
  given. Three of them draw a surface in one arity and act in the other -
  `window.jump-to`, `system.power` and `clip.history` - because the surface and
  the choice are the same question asked twice. `attn.mode` reads at arity 0
  and writes at arity 1. `desk.snapshot` takes the focused desk or a named one.
  And `desk.apps` does the same, which is how its zero-argument form came to
  quote a niri string into a refusal (section 3). Two of the six are exposed
  here and both in exactly one arity: `attn.mode <mode>` and `desk.apps <desk>`.
  A table that named the method alone would hand over the other half by
  accident, which is precisely the class of mistake enumerating a table is for.
  The table goes one step further than arity for one entry: `attn.mode` accepts
  `work` and `focus` and refuses `quiet`, for the reason section 3 gives, and a
  value the table refuses is refused in zded rather than only in the assistant.

### What that still does not buy

The uid bounds a compromise to the restricted table. It does not bound it to
the read half of the table, because a compromised assistant connects on
whichever socket it likes and asks for whatever the table answers. **The
read/act split (section 5) is not enforced below the assistant and cannot be:
enforcing it would need two assistant processes under two uids, and that is a
second daemon, a second unit and a second approval flow for a distinction the
certificate already makes.** So the split defends against the loop - a model
reading text and acting on it - and against one stolen certificate. It does not
defend against a takeover of the process that holds both grants' listeners.
That is written down rather than hoped over.

The uid also does nothing about the owner's own processes, and never could.
They have `zded.sock`. The assistant is not a way to restrict the owner; it is
a way to give a network peer strictly less than the owner has.

**Nothing binds a LAN address before this lands.** A loopback-only build may
ship first and its honest label is "as trusted as zded". The LAN bind is
refused until the uid, the socket and the second table exist, on principle 9:
the component's whole premise is that a network peer reaches the desktop under
a restriction, and a restriction that lives only in the process facing the
network is a hope.

## 2. What the assistant is made of

One binary, `zde-assist`, matching `zde-keymap` in its naming and in being
built by the same derivation. It is both ends of the wire:

- `zde-assist serve` runs on the zde machine as `zde-assist`, holds the two TLS
  listeners, the approved-fingerprint file and the server key, and speaks to
  zded over `/run/zde/assist.sock` as an ordinary client. Two listeners because
  the read tools and the act tools are two MCP servers, for a reason that is
  the specification's rather than this project's (section 7).
- `zde-assist connect <host>:<port>` runs on the **client** machine as whoever
  runs the model, holds the client key, and is what an MCP client launches as
  its stdio server. It dials TLS and pipes the same newline-delimited JSON-RPC
  through. One invocation per server, so a client that wants both configures
  two. See section 7 for why this shape rather than HTTP.

`zde-assist serve` contains no path to `exec`. Not "does not currently call
it": no import of `os/exec` and no `syscall.Exec`, because the one property
worth being able to check by reading the import block is that the process
facing the network cannot start a program. Everything that starts a program
(`palette.run`, `ask.run`, a desk's launches) is refused or is zded's.

The owner's CLI verbs live in `zde` (layer 1) and talk to `zde-assist serve`
over a small unix socket of its own: `zde assist pair`, `zde assist list`,
`zde assist revoke`, `zde assist fingerprint`.

## 3. The tool surface

Nine tools. The rule they are chosen by is decision 1's: every one is a read,
or is something a keypress undoes. The name of a tool is the name of the zded
method underneath it wherever they are one thing, so that a line in the audit
log, a tool call and a socket method are the same word; where they differ, the
difference is itself the flag that something is not a straight pass-through.

### The rule that comes before the list

An earlier draft of this document sorted the *methods* into tainted and
untainted and thought that was the split. It was wrong, and wrong in the way
that matters: the taint is not in the method, it is **in the reply**, and an
act grant's replies were full of it. Four paths, all reachable on an act
grant's first call:

1. **`desk.switch` answers with workspace names.** `switchFrom` builds
   `focused` out of `n.String()` for each landing and returns it
   (`server.go:2073-2079`). A workspace name is `<desk>.<monitor>.<slot>`, and
   the slot comes from `slotFor` (`internal/desk/adopt.go:96-127`), which calls
   `Label(app_id)` (`internal/desk/label.go:13-43`). **That is the
   application's own string**, lowercased and reduced to `[a-z0-9-]`, and
   `partMax` is 64 (`internal/desk/name.go:26`). Sixty-four characters of
   kebab-case is a sentence. `vision.md` lines 112 to 118 say this outright:
   adoption "names a workspace from a string the app chose for itself".
2. **`window.jump-to <id>` answers with the workspace it landed on**
   (`jump.go:205-211`), which is the same string by the same route.
3. **Desk names are mintable.** niri's IPC socket authenticates nobody beyond
   the file mode, so anything that can reach `$NIRI_SOCKET` can rename a
   workspace and invent a desk name that arrives in `desk.list` and
   `status.OnDesk`. This one is weaker than the first two and the difference is
   worth keeping: a sandboxed app names its own `app_id` and so reaches path 1
   through the compositor, where reaching `$NIRI_SOCKET` takes a host process
   running as the owner, and such a process has `zded.sock` already. It is
   still a channel, and zde does not get to decide who else runs on the host.
4. **`desk.apps` at arity 0 quotes a niri string back in its refusal**:
   `fmt.Sprintf("%q is not a desk workspace, ...", focused)` at
   `server.go:1092-1111`. `%q` escapes the control characters and does nothing
   about the words.

So the rule, which is prior to the tool list and is what the restricted table
is really for:

> **Nothing the acting half of the restricted table answers contains a string
> any application chose.** Not in a result, and not in a refusal.

The reading half carries application text on purpose, in two of its five tools,
and that is precisely what makes it a separate credential rather than a
convenience. The rule is one-sided by design; section 5 states it as the
invariant it is.

That is not a new invention. It is the MCP tools page's own requirement -
servers **MUST** "sanitize tool outputs" - and it is the same move
`internal/zded/jump.go` already made for the window list, one layer further
out. What changes is where the property lives: in the restricted table's
answers, which is one place and is checkable by reading it, rather than in a
judgement about each method that has to be made again every time a method is
added. The next verb somebody puts on that table does not have to be audited
for taint by hand; it has to answer in the vocabulary the table already speaks.

**What it costs**, said plainly, because it is not free:

- `desk.switch` answers `{"ok": true, "monitors": 2}` and not where you landed.
  A client that wanted to say "you are now on vshop.DP-1.code" cannot.
- `desk.list` reads the names out of the manifests (`Desks.All`), not out of
  `m.DeskNames()`, and `desk.switch` accepts only a name that list would have
  returned. So **a desk that exists on the screen but was never written down is
  neither visible nor reachable.** That is a real loss of function - adoption
  and `desk.move-workspace-to` both make desks nobody declared - and the honest
  trade for it is that a name read back from niri is a name something else may
  have written. It is also what makes the private-desk omission real rather
  than cosmetic: a name that is not on the list is refused, so guessing it
  achieves nothing.
- `window.list` keeps titles and app ids, because it is a read-grant tool whose
  entire purpose is application text, and stripping it would leave a tool that
  answers nothing. It is the one place taint is expected, and it is why the
  read grant exists.
- One tool is dropped entirely (below).

MCP tool names permit letters, digits, underscore, hyphen and dot, and the
specification's own example of a valid one is `admin.tools.list` (revision
`2026-07-28`, Server, Tools, Tool Names), so `desk.switch` is a legal tool name
and is used unchanged.

### Reads that carry no application text

| Tool | Arguments | zded method | Answers |
|---|---|---|---|
| `status` | none | `status`, filtered | the counts and the fixed words only: desk count, queue depth, attn mode, listener count, and the four booleans (compositor reachable, shell listening, notifications taken, zinc on PATH). Plus `journalSkipped` and `unplaced`, which are numbers |
| `desk.list` | none | `Desks.All`, **not** `desk.list` | the desks the manifests declare, by name, minus the ones declared `private: true` |
| `desk.apps` | `desk` (**required**) | `desk.apps` at arity 1 only | what that desk declares: one `app@instance` address per app, and the slot each is pinned to, both read out of the manifest |

Four things about that table are the fix rather than the description.

**`status` loses three fields**, and they are the three that name something.
`onDesk` and `lastDesk` are desk names read back from the compositor, so they
are path 3 above; `badManifests` is a list of file names in the desks
directory, which the owner writes but which also names desks, including private
ones. What is left is numbers and a fixed vocabulary of mode words. An earlier
draft's row for this tool was also simply wrong about the shape: `Status` has
`journalSkipped`, `unplaced` and `notifications` too, which that row omitted.

**`desk.list` changes its source**, from `m.DeskNames()` to the manifests. That
is the cost written down two paragraphs ago, and it buys the whole untainted
half: manifests are files the owner writes and nothing else can, where the
compositor's map is whatever is named into niri.

**Private desks are omitted from all three.** `manifest.Dir.Save` chmods the
desks directory 0700 and says why in its own comment: it is "the file that says
which desk is the private one... and the point of a private desk is that it is
not advertised". Enumerating those names to a network client would undo that
with more effort than it took to do it. `desk.switch` refuses one by name for
the same reason, because an omission you can step around by guessing the name
is decoration. And it is the same rule `net.status` is refused under, so the
document is now consistent with itself: **where the owner is, and what they
keep private, are not things this component answers.**

**`desk.apps` loses its zero-argument form**, which is path 4. The argument is
required, so the refusal that quotes a niri string is unreachable.

### Reads that carry application text

| Tool | Arguments | zded method | Answers |
|---|---|---|---|
| `queue.list` | none | `queue.list` | what is waiting: id, text, desk, sender, urgent |
| `window.list` | none | `window.list` (**new**, section 4.4) | open windows: id, title, app id, workspace |

These two are the read grant, and they are the only two tools on either grant
that carry a string an application chose. That is not an exception to the rule
above, it is what the rule is protecting: a read grant is a credential for
reading application text, and the whole of section 5 is that such a credential
cannot also act.

The queue's `text` is a notification's summary, which is the sender's words;
its `from` is the sender's claim about itself, which nothing verifies
(principle 6, and `internal/attn`, `SelfFrom`, for the one name that is now out
of that claim's reach). A window title is set by the application. Both are
bounded in shape - a summary is put through `oneLine` at ingest, so it is one
printable line of at most 300 characters, and a window list is filtered through
`attn.Line` in `internal/zded/jump.go` - and neither is bounded in meaning.
**Bounded shape, unbounded meaning** is the whole reason section 5 exists.

### Acts

| Tool | Arguments | zded method | Answers | Undone by |
|---|---|---|---|---|
| `desk.switch` | `desk`, never a private one | `desk.switch` | `{ok, monitors}` and never a workspace name | switching back, one key |
| `attn.mode` | `mode`: `work` or `focus`, never `quiet` | `attn.mode <mode>` | `{ok}` | setting the mode back, one key |
| `queue.add` | `text` | `queue.add` | `{id}` | `zde queue done <id>`, one line |
| `app.start` | `desk`, `app` | `desk.start` (**new**, section 4.4) | `{started}` or a refusal in zde's words | closing the window, one key |

**`window.focus` is dropped**, and the reason is more interesting than the
tool. It answers with the workspace it landed on (path 2), which is one
argument for stripping the reply rather than removing the verb. But it also
runs a **full desk switch** on the way - `focusWindow` calls `switchFrom` at
`jump.go:183-189` when the window is on another desk - so it starts that desk's
apps and takes its mode loan while calling itself a focus change, which makes
its "invariant 6 says a focus change rearranges nothing" row in the earlier
draft simply false. And it takes a window id, which an act grant has no way to
obtain: `window.list` is a read-grant tool, so the only route to an id is to
walk small integers, which is an enumeration oracle answering "is there a
window with this id" and, before the reply was stripped, handing back an
app-chosen workspace name for each hit. **Under the split this tool cannot be
used correctly by the grant that holds it**, so removing it costs nothing that
was working. That is information about the split rather than about the tool,
and it is the clearest single demonstration that the split has teeth.

Four notes on the four that remain, because the reversibility claim is not
equally strong across them.

`desk.switch` **starts every app the target desk declares**, and this is the
least reversible thing the assistant can do, more so than `app.start`.
`switchFrom` calls `startApps` on a change of desk
([`model.md`](model.md), section 5), so one call can start N containers where N
is however many that manifest lists. Closing their windows is N keypresses and
stops none of the containers, which is zinc's `zcr stop`. The bound is the same
bound `app.start` has and it is the only one there is: the set of things
reachable is the set the owner wrote into manifests. A model that walks the
desk list has started everything on the machine, and nothing here prevents
that beyond the rate limit and the fact that somebody can see it happening.

`attn.mode` **will not take `quiet`**, and this is a change from the earlier
draft, which had it. Quiet is not a display mode in the way the tool's row
implied. `Arrived` only puts an arrival on the queue when `mode.Queues` is true
(`server.go:1552-1562`), so everything that arrives while quiet is on is
recorded in the history and **never queued**, and setting the mode back does
not queue what did not arrive. That makes it a forward-dated bulk `queue.done`,
which is refused three sections later as irreversible, and refusing the one
while allowing the other would be a rule with a hole cut in it. It also blinds
the component's own visibility: `maybePop` returns early when the mode does not
pop (`internal/zded/attn.go:87-92`), so an assistant that sets quiet has
switched off the cards that section 11 relies on to tell the person it is
acting. `work` and `focus` remain, and the refusal for `quiet` says this.

`app.start` is undone by closing the window, which is a keypress; what that
keypress does not do is stop the container, which is zinc's. So the honest
sentence is: the window is undone by a key, the container is not.

`queue.add` **gets a quota**, and needs one. At the act rate limit of one every
two seconds, `QueueMax` of 1000 is reached in about 33 minutes, and past it
`journal.ErrQueueFull` refuses the **newest** item - which is the right rule for
a flood and means every real notification from then on is recorded but never
queued. The undo is a thousand `queue done` calls typed by a person, because
`queue.done` is refused to the assistant. So: **at most 20 items added by the
assistant may be waiting at once**, counted per fingerprint, and the twenty-first
is refused in words naming the number. That is 2 per cent of `QueueMax`, it is
more reminders than anybody wants from a machine, and it bounds the journal
writes too: with twenty live the assistant's sustained write rate is however
fast the person finishes them, not 0.5 a second for ever.

### One schema, to make the shape concrete

The riskiest tool of the ten, written out, because the argument about
reversibility is easier to check against a schema than against a sentence:

```json
{
  "name": "desk.switch",
  "title": "Switch desk",
  "description":
    "Bring one desk up on every monitor it owns workspaces on. Starts every app that desk's manifest declares, once each, if they are not already running. Answers whether it worked and how many monitors moved, never where you landed. Undone by switching back.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "desk": {
        "type": "string",
        "description": "A desk name from desk.list. A desk declared private is refused.",
        "pattern": "^[a-z0-9][a-z0-9-]{0,63}$"
      }
    },
    "required": ["desk"],
    "additionalProperties": false
  },
  "outputSchema": {
    "type": "object",
    "properties": {
      "ok": { "type": "boolean" },
      "monitors": { "type": "integer", "minimum": 0 }
    },
    "required": ["ok", "monitors"],
    "additionalProperties": false
  },
  "annotations": {
    "readOnlyHint": false,
    "destructiveHint": false,
    "idempotentHint": true,
    "openWorldHint": false
  }
}
```

Four things about it are the design rather than the encoding. The description
says the launch out loud, because a model choosing this tool should be able to
see that switching a desk starts programs. The `outputSchema` is closed, with
`additionalProperties: false`, and that is the rule of this section made
mechanical: a reply that grew a `workspaces` field later would fail its own
schema rather than quietly carrying an application's words to a model. The
pattern is on the argument,
because `desk.ValidDesk` will refuse a bad one anyway and refusing it twice
costs nothing while refusing it once means the refusal arrives as a socket
error rather than as a schema violation the client can fix. And the annotations
are set honestly and enforce nothing (section 7). `idempotentHint` is true, and
it is worth saying what it is true of: the second call with the same desk lands
where the first one landed, because a switch records the workspace it entered.
It is not true that switching to the desk you are already standing on does
nothing - `rotate` in `internal/zded/server.go` says so, and refuses a rotation
of one for exactly that reason: a switch restores the desk's last-active
workspace, so it can scroll off the workspace you were on. A client that
retries after a timeout is safe; a client that calls it to mean "stay put" is
not. Nothing anywhere relies on a client believing either.

### The tool the owner asked for that does not exist

"What is playing" has no method behind it. The media target is in the action
map ([`model.md`](model.md), section 6) and on the bar (principle 4), and
neither is a socket method: media is 0.2's. So `media.status` is named here and
is not in the table, and it arrives with 0.2's media work rather than being
faked out of something else. A tool that answered "what is playing" by reading
window titles would be an application-text read wearing a media tool's name.

## 4. What is not exposed

zded answers 54 methods: 44 in `Dispatch` (41 `case` labels, three of which
carry two names) plus the ten in `bluetoothMethods`. Six of them are exposed -
`status`, `desk.apps`, `queue.list`, `desk.switch`, `attn.mode`, `queue.add` -
and two of the nine tools are built on methods that do not exist yet, while
`desk.list` the tool no longer uses `desk.list` the method. So 48 existing
methods are refused. Here is the line, and why it falls where it does.

### 4.1 The ten that draw a surface

`desk.switcher`, `window.jump-to` with no argument, `attn.center`,
`attn.reach`, `palette.list`, `net.connections`, `system.power` with no
argument, `clip.history` with no argument, `ask.oneshot` and `ask.panel` all
broadcast an event that asks the shell to draw something. Every one of those
surfaces except the notification popup takes an **exclusive keyboard grab while
it is up**, and niri hands that grab to the oldest of them - `shell/shell.qml`
line 558 for the general rule, and `shell/AttnPopup.qml` line 12 for the one
surface that is the exception and the paragraph explaining why it had to be.

So a tool built on any of them is a tool that takes the keyboard away from the
person sitting at the machine, from the network, at a moment nobody chose. That
is not a thing to rate-limit or to warn about; it is the failure principle 4
exists to make impossible, and there is no version of it that is acceptable.
Refused as a category, including the read-shaped ones: `window.jump-to` with no
argument returns exactly the list `window.list` will return and draws a picker
on the way past, which is why the list needs a verb of its own rather than a
flag.

### 4.2 The named ones

**`palette.run`.** The one general execution surface on the socket. It runs an
action from the compiled-in registry, and the argv is not the caller's, which
is the defence that makes it safe for a key and irrelevant here: the registry
contains a terminal, and a terminal is a shell. The refusal in
`internal/zded/palette.go` already says why a palette that took a method and
its arguments would be a way to ask zded for anything at all; a palette
reachable from the network is that sentence with a network on it. Refused, and
refused first.

**`system.power`.** Ends the session. Not reversible in any sense the word
carries: the windows close, the arrivals the queue never got are gone, anybody
else logged in loses their session. The menu arity also draws.

**`clip.history`.** With no argument it draws, and answers with a preview of
every entry - up to 50 rows of 200 runes. With one argument it **puts an entry
back on the clipboard**, which is "have this typed into whatever is focused
next" at one remove. Principle 5 is that the clipboard has history and secrets
never enter it; a history with a network endpoint on it is the first half of
that sentence being used against the second. Refused in both arities.
`clip.clear` with it: it destroys the history, and nothing undoes that.

**`attn.center`.** It draws, which is enough on its own, and there are two more
reasons and each of them would be enough as well. It answers with the whole
history: `attn.Record` values carrying `body`, which is up to 4,000 characters
of whatever an application sent, and the ceiling on the answer is
`PerSenderMax` times fourteen rings, which is 420 records and about 10 MB
(`internal/attn/history.go`). And a record's `Private` field is never
serialised and is not a filter on `Recent()`, so what arrives on a desk
declared `private: true` is in that answer like everything else - the flag
stops the disk write and the popup, which is the whole of what it was written
to do. A read tool that hands 10 MB of application-chosen prose, including the
part of it that was on a private desk, to a model over a network is not a
narrower version of anything in section 3. Refused, and not deferred: there is
no later phase in which this becomes reasonable. The queue is the read that
answers "what is waiting", at one bounded line per item and no bodies at all.

**`bluetooth.confirm`, and the other nine.** `confirm` answers the pairing
question. `internal/bt/agent.go` is the longest argument in this tree about why
that answer belongs to a person who read a passkey and compared it with what
the other device showed, and the services table names the one that matters:
`1124` is "a keyboard or mouse (it could type into anything you have open)".
Nothing on the network approves itself - that rule is the reason the assistant
has an approval flow at all (section 6), and it would be an odd design that
obeyed it for its own clients and handed the pairing agent to a model. The
other nine go too: the radio is a decision about physical proximity, and
`bluetooth.state` is a list of the devices around this machine, which is a
statement about where it is.

**`net.connect`.** Its second argument is a password. A method whose argument
list has a place for a secret is not a method to put behind a client that logs
its own tool calls, on a wire, on behalf of a model that may repeat what it was
given. `net.forget` and `net.disconnect` go with it, and for a second reason
worth its own clause: disconnecting the link is how the assistant makes itself
unreachable, and reconnecting needs the credential it must not hold. A verb
whose undo requires a secret the caller is refused is not reversible.

**`net.status` and `net.list`.** Reads, and refused anyway. The SSID you are on
and the SSIDs around you are a location: wifi geolocation is the ordinary way
to place a machine to within a street. "From paranoids to paranoids" is the
README's line, and a read tool that answers where the machine is would be the
component's first breach of it.

**`attn.invoke`.** Presses a button an application declared. The key is the
sender's own string and the effect is whatever the sender does with it, which
zde does not know and cannot bound. Neither reversible nor knowable.

**`desk.snapshot`.** Writes a manifest file into the desks directory. A write
to layer 2's configuration, over the network, is a persistence primitive: the
file it writes is one `desk.switch` away from starting what it names.
`desk.reconcile` goes with it - it renames workspaces, adopts unnamed ones, and
rewrites `dynamic.kdl` - and nothing about it is undone by a keypress.

**`ask.run`.** Spawns a subprocess in a process group of its own. It would also
be a model calling a model, which crosses a line drawn somewhere else: ask has
no history by default and the local tier exists so that a private question
stays on the machine (the ask paragraph in section 2 of
[`vision.md`](vision.md)). An assistant that can call the local tier puts that
tier's answers on a network connection, which is the one thing the local tier
was for.

**`events`.** Turning a connection into a listener. It is not a read of the
history, it is the history arriving unasked and continuously: `EventAttnPopup`
carries an `attn.Record` with the body in it, for every notification the
session receives. Refused.

### 4.3 The rest, by group

| Methods | Verdict | Why |
|---|---|---|
| `status` (filtered), `desk.apps <desk>`, `queue.list` | exposed | section 3 |
| `desk.switch`, `attn.mode work\|focus`, `queue.add` | exposed | section 3 |
| `desk.list` | refused | the tool of that name reads the manifests instead, because this one answers names read back from niri (section 3) |
| `window.jump-to <id>` | refused | it is a desk switch wearing a focus verb's name, and an act grant has no way to obtain a window id (section 3) |
| `desk.apps` with no argument | refused | it quotes a niri string into its refusal (section 3) |
| `attn.mode quiet`, `attn.mode` with no argument | refused | a forward-dated bulk `queue.done`, and a read `status` already answers |
| `desk.switcher`, `window.jump-to`, `attn.center`, `attn.reach`, `palette.list`, `net.connections`, `system.power`, `clip.history`, `ask.oneshot`, `ask.panel`, each with no argument | refused | draws a surface and takes the keyboard (4.1) |
| `palette.run`, `system.power <name>`, `clip.history <id>`, `clip.clear`, `net.connect`, `net.forget`, `net.disconnect`, `net.status`, `net.list`, `attn.invoke`, `desk.snapshot`, `desk.reconcile`, `ask.run`, `events` | refused | named in 4.2 |
| the ten `bluetooth.*` | refused | named in 4.2 |
| `nav.down`, `nav.up`, `workspace.next`, `workspace.prev`, `desk.next`, `desk.prev`, `desk.queue-jump`, `desk.regulars` | refused | relative motion (below) |
| `desk.move-window`, `desk.move-window-to`, `desk.move-workspace-to` | refused | rearrangement (below) |
| `queue.done` | refused | not reversible (below) |
| `desk.last` | refused | `desk.switch` says where; this says "back" |
| `attn.quiet` | refused | a toggle (below) |
| `shown` | refused | acknowledges a surface token; a caller that never drew anything has nothing to acknowledge |

All 54 are in that table, which is the point of writing it out: a method with
no row is a method nobody decided about, and the check is that the rows and
`Dispatch` plus `bluetoothMethods` name the same set. Six verbs appear twice
because their two arities land on different sides of the line (section 1).

**Relative motion, and what the rule actually buys.** `nav.*`,
`workspace.next/prev`, `desk.next/prev`, `queue-jump` and `regulars` are all
reversible and all harmless in isolation. They are refused because they say
"one more" rather than "which", and a model that has lost track of where it is
walks the desks pressing them. The absolute verb does everything they do: **a
machine gets the verb that names the destination, not the verb that counts.**

That is a claim about the shape of the interface and it must not be mistaken
for a boundary, because most of it is reconstructible. `desk.regulars` is
`desk.switch("regulars")`, and the regulars are a desk name like any other.
`desk.next` and `desk.prev` fall out of `desk.list` and one switch. `desk.last`
was reconstructible too, from `status.lastDesk`, until this revision took that
field out of the answer for a different reason - so it is now genuinely
unreachable, by accident rather than by this rule. What refusing them buys is
that a confused client cannot walk anywhere by repeating one call, and that the
audit line says which desk rather than "one along". It does not buy that any
desk is out of reach; only the private flag does that, and it does it in zded.

One of the five is genuinely blocked, and it is worth noticing what blocks it.
`desk.queue-jump` would be `queue.list` and then `desk.switch`, and no
credential can make both calls: the first is the read grant's, the second the
act grant's. **The split is what stops it, not the rule** - which is the
clearest evidence in this document that the split is load-bearing and the
relative-motion rule is housekeeping.

The same distinction applies to `attn.quiet`: it is refused as a toggle, since
a toggle called twice by a client that retried is a toggle that did nothing,
and an idempotent verb is the one to hand to something that may retry. But
`attn.mode quiet` is refused for a stronger reason and in zded (section 3), so
the toggle being gone is not what keeps quiet out of reach.

**Rearrangement.** Moving a window or a workspace to another desk changes what
is where. Invariant 6 is that focus changes never rearrange anything and layout
changes are explicit; a rearrangement performed from the network is the least
explicit thing on this list. `desk.move-workspace-to` additionally is how the
regulars band comes into being, which is a decision about how somebody works.

**`queue.done`.** Not reversible, and the reason is worth stating because it
looks like it should be. The id is spent, `history.Dismiss` marks the record,
and `Notifier.Dismissed` tells the sender it is gone. Re-adding the text makes
a different item with a different id and tells nobody. A queue is what somebody
still owes themselves, and finishing one on their behalf is deleting the record
of an obligation. `queue.add` is exposed because it is additive and the person
holds the undo.

### 4.4 The two methods zded does not have yet

Both are preconditions, and both are zded's rather than the assistant's.

**`window.list`.** No arguments, answers `[]Window`, draws nothing. It is the
first half of `jumpTo` with the broadcast removed, and zde wants it
independently: `zde window jump-to` already prints this list when no shell
answers, and the read and the draw being one verb is why an assistant cannot
have the read.

**`desk.start <desk> <app>`.** Starts one app that the named desk's manifest
declares, refusing an address the manifest does not name, and answering what it
started or why not. It reuses the `launch` field `startApps` already goes
through. It must be zded's for two reasons: the check "the manifest declares
it" belongs where the manifests are read, and the assistant must contain no
path to `exec` at all (section 2).

## 5. Two grants, and the attack that decides it

A certificate carries `read` or `act`. Never both, and the file's shape is what
enforces it: one line per fingerprint, and a fingerprint may appear once. The
same key cannot hold both grants, so a client that wants both needs two keys,
which is two approvals the owner performed while looking at two fingerprints.
A check that refused a certificate carrying two grants would be a check
somebody could forget; a file in which the thing cannot be written down is not.

The reason is a demonstrated one rather than a principle.

`attn.center` returns `attn.Record` values with `body` in them, up to 4,000
characters each. `clip.history` returns previews of what was copied. A window
title is whatever the application set. Two of those three are refused outright
in section 4, and the third is the reason this section exists, because a queue
row carries a notification's summary and a window row carries a title and both
have to be readable for the component to be worth anything. All three are text
an application chose,
and this month's audit (commit 2a51374, "a notification off the bus cannot say
it is zde, and foreign text cannot drive a terminal") found that the sender
column was the application's own claim with nothing checking it, so
`notify-send -a zde "..."` produced a card, a centre row and a queue line
identical in every field to the ones zde sends about itself. The fix reserved
one name and made the reservation spelling-insensitive; the general problem it
names is unchanged and is named in `internal/attn/notify.go`: the sender column
is unverified until attribution by channel lands (principle 6, and ask 4 in
section 4 of [`vision.md`](vision.md)).

Now put a model on the other end. An assistant that reads that text and can
also act is a machine in which **any application on the session bus can write a
sentence that becomes a system action, needing no network access of its own**.
The application does not have to break anything: it sends a notification whose
body is an instruction, the assistant reads it because reading the queue is
what it is for, and the model does the thing. The queue's own bound makes this
worse rather than better, because a summary is a printable single line of 300
characters, which is exactly the shape of an instruction.

**Text read from the machine must never authorise an action on the machine.**
That is the rule, and the grant split is how it is written down.

### The invariant, stated so it can be checked

An earlier draft wrote the split as a partition of the tool set: untainted
reads on both grants, tainted reads on the read grant, acts on the act grant.
That version was false, because it sorted methods when the thing that carries
text is a reply, and four of the act grant's replies carried application
strings (section 3). Sorting methods also puts the property somewhere nobody
can check: you verify it by re-reading every method's implementation, and you
re-verify it every time one changes.

So the invariant is a property of the act grant's **output**, and it is one
sentence:

> **Every reply on the act grant, result and refusal alike, is drawn from a
> closed vocabulary: numbers, booleans, and strings that came out of a file the
> owner wrote or out of zded's own source.**

That is checkable by reading the restricted table's answers, mechanised by
closed `outputSchema`s with `additionalProperties: false` (section 3), and it
does not have to be re-derived when a method is added. It is also, word for
word, the MCP tools page's "sanitize tool outputs" **MUST** applied to the one
thing on this machine that most needs it.

The read grant is the complement and is deliberately not sanitised:
`queue.list` and `window.list` exist to carry application text, and stripping
them would leave two tools that answer nothing.

The property the owner asked for survives the restatement exactly: **no
credential holds both application text and the ability to act.** What changed
is that it is now true.

What the split does not defend against, said once and plainly: a person holding
both certificates and running one client that uses them in turn, and a
compromise of `zde-assist serve`, which holds both listeners. Section 1 says
why the second cannot be fixed without a second daemon under a second uid.

And one channel the invariant does not close, because nothing here can: the
**shape** of an act grant's replies still carries a little. Whether
`desk.switch` says ok or refuses tells the caller whether a desk exists, and
`monitors` is a count of the machine's screens. A closed vocabulary bounds what
can be said to a few bits an answer, not to none. The claim is that no sentence
an application wrote reaches a model that can act, not that the act grant
learns nothing at all.

## 6. mTLS

### No CA

The obvious design has zde run a small CA and issue client certificates. It is
refused. A CA is a way to delegate the decision about who may connect, and
there is nobody here to delegate it to: the CA key would live on this machine,
so anything running as the owner could mint a client certificate, and the
approved list would be doing all the work anyway while the CA key sat there as
the thing worth stealing. A CA that adds a key to steal and moves no decision
is a mechanism with a cost and no benefit.

So: **self-signed on both sides, pinned by fingerprint.** The shape is
`authorized_keys` and the shape is the pairing agent's - the owner reads a
number off a screen and says yes to that number.

### Keys, and where they live

- The **server** key and certificate are generated by
  `zde-assist serve` on first start, at `/var/lib/zde-assist/server.key` and
  `server.crt`, 0600, owned by `zde-assist`. SANs: `localhost`, `127.0.0.1`,
  `::1`, and the one configured LAN address. `zde assist fingerprint` prints
  its SPKI SHA-256 for a client to pin, because a self-signed server
  certificate is otherwise unverifiable and a client that skips verification
  has no mTLS, only TLS-shaped bytes.
- The **client** key is generated by `zde-assist connect` on the client
  machine, and **zde never holds it**. There is no path in this design that
  transports a client private key: no enrolment endpoint, no export verb, no
  file that could hold one. What crosses the wire at handshake is a
  certificate, which is a public key with a signature on it.
- The **approved list** is `/var/lib/zde-assist/approved`, 0600, one line per
  grant:

```
# fingerprint (SPKI SHA-256)   grant  name           until     approved
sha256:3f9ac41e8b2d5a07f6c9...  read   laptop-claude  revoked   2026-08-15T14:02:11Z
sha256:c1d0779be4aa3f5218ee...  act    laptop-claude  session   2026-08-15T14:03:40Z
```

The pin is on the **SubjectPublicKeyInfo**, not on the whole certificate. A
client that re-issues itself a certificate with the same key keeps its grant,
which is what makes expiry a hygiene property rather than a security one, and
which is said out loud in the next paragraph rather than left to be discovered.

### What happens to a certificate

**Unknown.** The handshake completes and every call is refused, with a sentence
carrying the fingerprint so a person can compare it with what their client
prints. Nothing is drawn. An unknown certificate that could make zde put a
surface on somebody's screen would be a way for anyone who can reach the port
to interrupt whatever they were typing.

**Approved.** Its grant is read from the file at handshake and bound to the
connection. Every call on that connection is checked against it. The tool list
that connection sees contains only its grant's tools (section 7).

**Expired.** Refused, with the date in the refusal. Honestly labelled: because
the pin is on the key, a client can re-issue itself a fresh certificate for
free and keep its grant, so this buys a client that renews rather than one that
drifts. It is not a boundary and is not presented as one.

**Revoked.** There is no CRL and no OCSP, because there is no CA.
`zde assist revoke <fingerprint>` removes the line **and drops every live
connection holding it**. That second half is the whole verb: removing a line
from a file does nothing to a TLS session already up, and a revocation that
only applies to future handshakes is a promise about the future dressed as one
about the present.

And the limit of that verb, which is easy to miss: **revocation runs inside the
process it is revoking.** `zde assist revoke` edits a file and asks
`zde-assist serve` to drop a connection, and a `serve` that has been taken over
will happily not do either. The only revocation that survives a compromise of
the thing being revoked is one enforced from outside it: `systemctl stop
zde-assist`, which also takes the listener down, and removing `zde-assist` from
the socket's group, which takes its reach to zded down. Both are the owner's,
both are one line, and the CLI's refusal path should name them - a verb that
quietly means "if the daemon feels like it" is worse than no verb.

### How a certificate becomes approved

The bluetooth agent's **five** rules, reused rather than reinvented, because
they were argued once already in `internal/bt/agent.go`. An earlier draft took
four and dropped the one that costs something, which is the one that matters:

1. **Nothing proceeds without an explicit yes.** The fingerprint goes to the
   person; the person compares it with what their client shows; the answer
   comes back through zded and the shell.
2. **One question at a time.** A queue of approval dialogs is a stack to click
   through, and the second one is the one nobody reads.
3. **A question nobody answers is refused, not left standing.** Silence is a
   refusal, on the same clock the pairing agent uses.
4. **Pairing does not trust.** `agent.go` is explicit that trusted is "a
   separate property and a separate verb, because trusted means reconnect and
   use services without asking again - which is a decision a person makes about
   a device they own, not a side effect of having once agreed to pair it". The
   earlier draft here had only the trusted form: approve a fingerprint once and
   it connects for ever. So an approval is **`--until session` by default**,
   which is dropped when `zde-assist` restarts, and `--until revoked` is the
   standing form and is a second word the owner has to type. Agreeing once that
   a machine may talk to this desktop today is not agreeing that it may talk to
   it in March.
5. **An answer names the question it answers.** The yes is spent on the
   fingerprint the person read or on nothing at all.

Two rules are added. The first keeps rule 2 from being a denial-of-service
target: **the approval surface only opens inside a window the owner opened.**
`zde assist pair` opens it for 60 seconds; the first unknown fingerprint to
arrive in that window produces the question. Outside it, an unknown certificate
is refused in words and draws nothing. Without this, anyone who can reach the
port can make a dialog appear.

The second is about who chooses the grant. **The grant is an argument to
`zde assist pair`, not a field the stranger fills in.** `zde assist pair
--grant read` is the owner saying what is about to be handed over; a connecting
certificate that asks for `act` is not asking, it is describing itself, and the
surface must not show the stranger's word as though it were the decision. What
the surface shows is the fingerprint, the address, and **the grant the owner
typed**, and the yes is spent on that triple. Letting the peer name its own
authority is how "approve this client" quietly becomes "approve whatever this
client says it is".

The surface is the shell's, holds the keyboard like every other one, and shows
the fingerprint in groups a person can read aloud, the address the connection
came from, and which grant is being asked for. It is drawn from a zded event
like every other surface, so approving a client is one more thing the shell is
a thin adapter for and not a second place with logic in it (the shell paragraph
in section 2 of [`vision.md`](vision.md)).

## 7. MCP: what it gives, and what is added

Checked against revision `2026-07-28` of the specification rather than from
memory.

### What it gives

- **The tool shape.** `name`, `title`, `description`, `inputSchema`,
  `outputSchema`, `annotations`, and the two calls `tools/list` and
  `tools/call`. Every tool in section 3 is one of those with a JSON Schema on
  it.
- **A framing that fits a byte stream.** The stdio binding is
  "newline-delimited JSON-RPC", and the transports page says custom transports
  over a reliable bidirectional byte stream, naming Unix domain sockets and
  TCP, "**SHOULD** reuse the stdio framing rather than defining a new one: the
  stdio binding is just newline-delimited JSON-RPC over a byte stream, and only
  its process-lifecycle rules are specific to standard streams." So TLS with
  stdio framing over it is a custom transport the specification anticipates,
  not a deviation.
- **A place to put a refusal.** Tool execution errors are `isError: true` in
  the result with text a model can read, distinct from JSON-RPC protocol errors
  for unknown tools and malformed requests. That maps exactly onto the house
  style: a refusal says what was refused and why, in a sentence, and it goes
  where the model reads it rather than where only a log does.
- **The output sanitisation requirement**, which is this document's own
  problem stated back to it as a rule. Servers **MUST** "Validate all tool
  inputs", "Implement proper access controls", "Rate limit tool invocations"
  and "**Sanitize tool outputs**". Section 5's invariant is the fourth of those
  taken seriously, and sections 4 and 10 are the second and third.

### The one place this nearly went wrong: statelessness

An earlier draft had one server varying its `tools/list` by the client
certificate, and quoted the specification as permitting it. The quote was cut
one clause short. In full:

> This set **MAY** be empty and **MAY** change over time, but **MUST NOT** vary
> per-connection or as a side effect of other requests on the connection. The
> set **MAY** vary by the authorization presented on the request - for example,
> returning only the tools the caller's granted scopes permit - **since
> credentials are per-request input, not connection state.**

The final clause is the whole permission, and a TLS client certificate is
connection state. The base specification is blunter still: "MCP is a
**stateless protocol**... Servers **MUST NOT** rely on prior requests over the
same connection to establish context (e.g., capabilities, protocol version,
**client identity**)", and "an open connection, such as a STDIO process, is not
a conversation or session". A design that reads the peer certificate once and
serves a different tool list for the life of the connection is the thing those
sentences are written against.

The fix is not a caveat, it is the design being wrong by one layer. **Two
listeners, two ports, two MCP servers.** The read server's `tools/list` always
returns the same five tools; the act server's always returns the same four.
Neither varies by anything at all - not per connection, not per request, not by
authorization - so the `MUST NOT` is not merely satisfied, it is not engaged.
The certificate does not select a tool list; it selects **whether you may talk
to this server at all**, which is transport authentication and is what the auth
page leaves to implementations: "clients and servers **MAY** negotiate their
own custom authentication and authorization strategies."

It is also the truer model. These are two authorities, and calling them two
servers stops a good deal of hedging. A read certificate presented to the act
port is refused at that port, in words. And when the two ever become two
processes under two uids (section 1), nothing about the protocol changes.

One consequence to hand to whoever writes the client: a client aggregating both
servers meets two tool sets with no name collisions today, but the
specification notes that tool name uniqueness is scoped to a single server and
that aggregating clients **SHOULD** disambiguate, so `zde-assist connect`
should be run once per server rather than merged behind one name.

### What it does not give, and is added here

- **Authorization.** The authorization specification is OAuth 2.1 over HTTP and
  says so: "Authorization is **OPTIONAL** for MCP implementations", and
  "Implementations using alternative transports **MUST** follow established
  security best practices for their protocol." A custom TLS transport is an
  alternative transport. Everything about who may call - the certificates, the
  pinning, the approval, the grants, the revocation - is this document's and
  has no MCP shape to borrow.
- **Any enforcement from annotations.** `ToolAnnotations` carries
  `readOnlyHint`, `destructiveHint`, `idempotentHint` and `openWorldHint`, and
  the schema's own comment is unambiguous: "NOTE: all properties in
  `ToolAnnotations` are **hints**. They are not guaranteed to provide a
  faithful description of tool behavior... Clients should never make tool use
  decisions based on `ToolAnnotations` received from untrusted servers." So the
  annotations are set for display and enforce nothing. The enforcement is that
  the acting tools are **not in the list** a read certificate receives, and are
  refused if called by name anyway.
- **Rate limiting, audit and refusal wording.** The tools page says servers
  **MUST** "rate limit tool invocations" and clients **SHOULD** "log tool usage
  for audit purposes" and stops there. Sections 9 and 10 are the numbers.

### What revision 2026-07-28 costs a server, plainly

The revision is newer than most implementations, so the list of what a
conforming server owes is worth having in one place rather than discovering it:

- **There is no `initialize`.** "There is no negotiation handshake. Every
  request carries its protocol version." A server that waits to be initialised
  waits for ever.
- **`_meta` is required on every request.**
  `io.modelcontextprotocol/protocolVersion` and
  `io.modelcontextprotocol/clientCapabilities` are both marked required, and "a
  request missing any required field is malformed; the server **MUST** reject
  it with JSON-RPC error code `-32602`".
- **`server/discover` is a MUST.** "Servers **MUST** implement
  `server/discover`", even though clients **MAY** skip it. So the read server
  and the act server each answer it, and each answers before any certificate
  question is settled, which makes it the one call an unapproved client can
  usefully make. It says which protocol versions this server speaks and nothing
  about the desktop, which is the right amount.
- **`resultType` is a MUST on every result**, `"complete"` in the ordinary
  case, and an unrecognised value **MUST** be treated as invalid by clients.
- **Version mismatch is `UnsupportedProtocolVersionError`, `-32022`**, listing
  what this server supports, and the `-32020` to `-32099` range is the
  specification's: implementations "**MUST NOT** emit any code from this
  sub-range that is not defined by this specification".
- `io.modelcontextprotocol/clientInfo` is self-reported and the specification
  says implementations "**SHOULD NOT** rely on them for security decisions",
  which is the same sentence principle 6 has been making about notification
  senders. The audit log records it as a claim, beside the fingerprint, which
  is not one.

One thing this document does not assert, because a check of the base page did
not confirm it: whether sampling is deprecated in favour of elicitation in this
revision. The overview still lists "Elicitation, sampling and root directory
lists" as client features. It does not matter here, since the assistant uses
neither, but it should not be repeated as fact on this document's authority.

### Why not Streamable HTTP

Streamable HTTP is the other standard binding, and a remote client cannot use
stdio because there is no subprocess to launch across a network. The obvious
answer is therefore HTTPS with client certificates. It is not taken, for two
reasons.

The first is that most MCP clients cannot be told to present a client
certificate, and the ones that can do it through a proxy anyway. The second is
better: **HTTP brings a browser-shaped attack surface to a thing no browser
needs to reach.** The Streamable HTTP page requires servers to validate the
`Origin` header on all connections "to prevent DNS rebinding attacks", and to
respond 403 when it is present and invalid. Those requirements exist because a
web page can make an HTTP request to a local server. A raw TLS socket speaking
newline-delimited JSON-RPC is not reachable from a web page at all, so the
whole class goes away rather than being defended against.

The price is that no off-the-shelf client speaks the transport, which is what
`zde-assist connect` is for: the client machine runs it as its stdio MCP
server, it holds the client key, and the MCP client sees stdio and learns
nothing new. That also puts the private key in a process whose only job is to
hold it, which is where a private key should be. The security best practices
page recommends the same direction for local servers - "Use the `stdio`
transport to limit access to just the MCP client" - and the bridge is that with
a network in the middle.

## 8. The bind list

Two entries at most: loopback, always; and one LAN address, when the owner sets
one. Configuration, sketched, and the shape matters more than the file format:

```yaml
# /etc/zde/assist.yaml, written by nix/system.nix, read at start
listen:
  loopback: true            # 127.0.0.1 and ::1, always
  address: 192.168.1.40     # exactly one, or absent
  # Two servers, two ports (section 7). A certificate is approved for one of
  # them and refused at the other.
  read: 7717
  act: 7718
grants:
  approved: /var/lib/zde-assist/approved
```

`address` has no default, and absent means loopback only. That is the
fail-closed answer and it is the same reasoning `zde.ask.tiers` has no default
for: an unset thing is unset, and a default that reached the network would be
zde making a decision about somebody's LAN for them.

**An IP address is not an identity.** mTLS is the authentication; the bind list
is defence in depth, and it is worth listing what it is and is not worth.

What it buys: the listener is not on every interface, so a machine that gets a
second network - a VPN, a tether, a bridge somebody set up for a container -
does not silently start answering on it. That is a real and ordinary failure
and this prevents it.

What it does not buy: anything about who is calling. An address on a LAN is
claimed by whatever is on that LAN; ARP is not authentication, DHCP hands the
same address to a different machine tomorrow, and a laptop that joins a café
network has neighbours. The bind list narrows where the port is answered, not
who answers to it.

And the sharpest case against treating it as a boundary is on this very
machine. **A container with the host's network namespace is `localhost`.** zde
does not decide that - zinc does, and an ordinary rootless container gets a
namespace of its own and cannot reach the host's loopback - but "loopback only"
is a statement about interfaces and not about processes, and the moment
something is granted host networking the loopback bind lets it in. The
certificate is what stops it. This is the same shape as principle 6: a claim
about where something arrived from is not a claim about what it is, and the
unforgeable thing is the channel and the key.

## 9. Audit

Every call is recorded. Where, and with what bound, is arithmetic rather than
preference, because the journal already has a growth problem that cost this
project real money once.

### Not the journal

`internal/journal/journal.go` says it in its own words: `compactAt` "was chosen
when the journal held desk switches, which are human-paced... It is not any
more: every notification writes a line here too... What that costs is a longer
replay at the next login, and a file that grows for as long as the session runs
- compaction happens at Open and nowhere else, so nothing shortens it in
between." And `QueueMax`'s comment records what that cost: "**a flood filled a
16 GB tmpfs and took every shell on the machine with it**", measured "on a live
daemon at 1065 arrivals a second, which is 2.6 MB/s of fsynced appends at the
worst case", with "a 49 MB journal of queued entries compacted to 49 MB".

A model calling tools is machine-paced by construction. Putting its calls in a
file that fsyncs per line and is compacted only when zded restarts would be the
same mistake with a faster writer. So the assistant writes its own files, in
its own state directory, with their own bounds. There is a second reason: the
journal is rewritten by zded at `Open`, and two writers to one append-only file
that one of them rewrites is a lost tail.

### Two files, two bounds

An audit exists to answer "what did it do", and acts are what it did. So:

**`/var/lib/zde-assist/acts.jsonl`**, 0600. One line per act, whatever the
verdict. Each line carries the time; the fingerprint, short form plus the
approved name; the grant; the tool; the arguments as received, each put through
the `attn.Line` rule - one line, printable, bounded - because this file is read
in a terminal and the same audit that produced section 5 also found four paths
into a terminal with no filter on them; the zded method it became; the peer
address, as a fact and not as an identity; and the verdict, with the refusal's
own words when it is one.

The arithmetic. A line is about 200 bytes. The act rate limit (section 10) is
one act every two seconds sustained, so the ceiling is 100 bytes a second, 8.6
MB a day of a client hammering the limit continuously, and a few hundred bytes
a day in ordinary use. Bound: 4 MiB, then rename to `acts.1.jsonl` and start a
fresh one, and only those two ever exist. Ceiling 8 MiB, about 40,000 acts: at
the rate limit that is 22 hours of continuous abuse before the oldest is lost,
and in ordinary use it is years. Nothing here fsyncs per line - an audit line
that is lost to a crash is one line, and the trade the journal makes for a
person's place on a desk is not the trade to make for a machine's tenth call
this second.

**`/var/lib/zde-assist/reads.jsonl`**, 0600. A **refused** read is a line, in
the same shape. A read that succeeded is a **counter**: one row per
fingerprint, per tool, per hour, written on the hour and on the way out. What a
person needs from a read log is that this client read the queue 1,800 times
this hour, and that is one row, not 1,800 lines saying "queue.list, ok". The
counters file is a few KiB and rewritten in place. The read limit was lowered
after this section was first written (section 10), which takes the worst case
from 173 MB a day to 8.6 MB, so the counters are no longer load-bearing against
a disk filling up - they are load-bearing against an audit nobody can read.

Both files, and the fact that acts also produce something a person can see
while it happens (section 11), are the answer to "every call recorded". A
counter is a record; a hundred thousand identical lines is not more of one.

## 10. Rate limiting, and refusal

The house shape for a ceiling is `asksMax`: a number with arithmetic behind it
about what somebody can honestly want at once, and past it a refusal that says
what to do. Same here.

**One call at a time per connection.** A second call while one is in flight is
refused, in the shape `sink.asking` already uses: "this connection is still
answering the last question: wait for it, or ask on another."

**Four connections.** One assistant, one spare, and past that the fifth is
waiting on nothing. The same reasoning as `asksMax`, and it also bounds what
one client can hold open.

**Acts: one per two seconds, burst five.** The number comes from what an act
is: it moves what is on somebody's screen. A person watching their desk change
five times in ten seconds already knows something is wrong and has time to
reach for the key that ends it; sixty times in a minute is what the limit
exists to make impossible. The burst of five is one deliberate sequence - switch
desk, start an app, set the mode - without the limit being felt.

**Reads: one per two seconds sustained, burst ten.** The earlier draft said ten
a second flat and justified it with "reads cost a socket round trip and change
nothing". Both halves were wrong.

They are not free. `status` asks niri for the desk map and then calls
`exec.LookPath` for the zinc runner on every call; `desk.apps` re-reads and
re-parses **every manifest in the directory** with no cache; and `cmd/zded`'s
compositor dials a fresh niri connection per method, deliberately, "because
niri restarts, and a daemon holding a dead socket would answer with stale truth
instead of saying it cannot see the compositor" (`main.go:277-289`). Ten reads
a second is ten niri connections a second and ten directory walks, from a
network client, against the daemon that answers every keybind.

And they do change something, in the one way this project cares about. `status`
answers the attn mode and the queue depth, and those move when the owner moves.
A read grant polling ten times a second is a **presence and activity feed**
with a tenth-of-a-second resolution: when they sat down, when they started
working, when they stopped. That is the same objection `net.status` was refused
under, arriving through a tool that was kept. Removing `onDesk` and `lastDesk`
(section 3) takes the location out of it; the timing is what is left, and the
answer to timing is the sampling rate.

So the shape is a token bucket rather than a flat rate, and the two numbers are
two different arguments. **The burst of ten** is one model turn: a client
reading the queue, the desks and the windows before it answers should not feel
a limiter. **The sustained one per two seconds** is the resolution: the finest
trace anybody can build out of this is a two-second one, which is coarse enough
not to be a keystroke-adjacent signal and far finer than an assistant needs.

Section 9's shape survives the change and its reasoning is unaltered: bursts
still exist, so a client can still spend lines in bunches, and a hundred
thousand identical success lines is still not more of a record than a counter
is. What changes is the ceiling, downward: at one read every two seconds a line
each would be 8.6 MB a day rather than 173 MB, which is the same order as the
act log. The counters stay because the argument for them was never only the
byte count.

A refusal names what was refused, the limit, and when the caller may ask again:

> `desk.switch` is refused: this certificate has made 5 acts in the last 10
> seconds, which is as many as the assistant performs in that time. The next
> one is allowed in 2 seconds.

It arrives as a tool execution error, `isError: true`, so the model reads it
and can wait rather than retrying into the wall. Refusals are counted per
fingerprint, and a certificate that spends a whole minute being refused is
worth a line on the bar, because the shape of that is either a broken client or
somebody trying things.

## 11. What a person sees

Principle 4: whatever a keypress depends on is on the bar. An assistant acting
on the machine while somebody is using it is a surprise unless something says
so, and there are three different things to say, which want three different
places.

**The listener, on the bar, permanently.** Whether `zde-assist` is answering
and on what: `assist local` for loopback only, and `assist lan` in the loud
colour when it answers on an address other people can reach. This is state, not
an event, and it is not dismissible. A network listener you cannot see is the
thing this project would refuse to ship.

**A connected client, on the bar, while it is connected.** The count and the
grant: `assist 1 read`, or `assist 1 act` in the loud colour. Two facts and no
more, in the same monospace vocabulary the queue and the attn mode use, so what
is on the bar and what `zde assist list` prints are one set of words.

**An act, in front of the person, as it happens.** A notification through zde's
own sender, saying what acted and what it did, so that the person can undo it
without going to look. It replaces its own previous card rather than stacking,
using `replaces_id`, which is the mechanism that already turns one download's
hundred progress updates into one row: at the act rate limit a card per act
would otherwise be 1,800 popups an hour.

The difference between the second and the third is worth naming, because this
month's audit is what makes it sharp. **The bar is state and the card is a
claim.** The bar is drawn from what zded knows, over the event stream, and
nothing on the session bus can write to it. The card is a notification, and a
notification's sender column is the sender's own claim: only the exact word
`zde` is reserved, and `internal/attn/notify.go` says what that does and does
not buy - "an impostor is drawn under its bus address (':1.57'), which no app
name looks like", but "a name that merely looks like it" is the general
unverified-column problem and is not solved. So an application can send a card
that looks like the assistant's. The card is a convenience, the bar is the
truth, and a person who wants to know what actually happened reads
`zde assist log`, which reads section 9's file.

One more, and it is the thing to try on a real machine rather than to decide
here: whether an act while somebody is typing needs anything stronger than a
card. The popup deliberately takes no keyboard until `Mod+Ctrl+n`, which is the
right default for a notification and may be the wrong one for "something else
just changed your desk". The alternative - an act that asks first - is a
different component, because a model that has to wait for a yes on every call
is not an assistant, it is a remote control with a person holding it.

## 12. Where it sits

**Layers** ([`delivery.md`](delivery.md)). The assistant is the first zde piece
that is not layer 1, and that is the uid's doing rather than a choice:

- **Layer 0**: the `zde-assist` user and group, the `/run/zde` tmpfiles rule
  (setgid, section 1), the system unit that runs `zde-assist serve` as that
  user, and `/var/lib/zde-assist`. zde opens no port in any firewall it
  manages, so reaching the assistant from the LAN is a decision made twice: once
  in `listen.address` and once in the host's own firewall.
- **Layer 1** (`nix/home.nix`): the `zde assist` CLI verbs, which come with
  `zde`; the approval surface, which is the shell's; and zded's second listener,
  which comes with zded.
- **Layer 2**: nothing. The assistant is not a zinc container, because the
  grant it needs is a zde socket and principle 7 says that socket is never
  mounted into one.

Three things about that arrangement are awkward and are not smoothed over.

**Layer 0 is two things, not one.** [`delivery.md`](delivery.md) gives it two
owners: `nix/system.nix` on NixOS, and `install.sh` on any other distro, whose
stated maintenance cost is "the script alone". Creating a system user, a group
and a setgid runtime directory is one option block on NixOS and distro-specific
shell on everything else. So the honest position for the portable path is that
it gets the **loopback-only** build until somebody does that work, and that is
a supported state rather than a failure: section 1 already says loopback adds
no authority, so the portable path is not shipping a weakened assistant, it is
shipping the one whose label is accurate.

**The binary is not its own derivation.** `nix/zde.nix` is a single
`buildGoModule` with `subPackages = [ "cmd/zde" "cmd/zded" "cmd/zde-keymap" ]`,
and `nix/home.nix` builds it once as `zdeTools`. `zde-keymap` is not a separate
package and neither would `zde-assist` be, so a layer 0 unit would have to
reference a derivation that layer 1 currently instantiates for itself. The
answer is that the flake exposes the package and both modules take it as an
argument rather than each calling `callPackage`, which is a small change and
has to happen before the unit exists, not after somebody notices two zde builds
in one closure.

**A system unit answers whenever the machine is powered.** Every other zde
component is `PartOf = graphical-session.target` and dies with the session. A
system unit does not: it is up at the greeter, with nobody logged in, and it is
up while the screen is locked. Meanwhile the bar that makes it visible
(section 11) exists only inside a session, so the one window in which the
listener most needs to be visible is the one in which nothing can show it. Two
answers, and this document takes both: the unit is `BindsTo` the session in the
sense that `zde-assist serve` **refuses every call while zded is not answering
on `/run/zde/assist.sock`**, which is true at the greeter and after a logout;
and the refusal says so, rather than the connection simply hanging. That leaves
the listener accepting TCP with nobody logged in, which is a smaller thing than
it answering, and it means an unapproved prober learns only that the port
exists and the session is not up. What it does not do is make the assistant
usable when nobody is logged in, and that is deliberate: a desktop assistant
for a desktop nobody is sitting at is a remote shell with a friendlier name.

**Phase**: 0.3. It is in no phase today, and 0.3 is where it belongs for three
reasons. 0.2 is the phase that makes the desktop usable by the person sitting
at it, and shipping remote control before the security set - guest, panic and
the decoy, zen, lock-preset, capture-block, `net.kill` - would mean the machine
can be driven from elsewhere before it can be hidden here. 0.3 already contains
the other two places an LLM touches this system, doctor's runbook agent and
update's re-pin agent, and the rules about what an agent may reach want writing
once in one phase rather than three times in three. And one read tool waits on
0.2's media work anyway.

Ordered inside the phase, because the order is the argument of section 1:

1. `window.list` and `desk.start` in zded, with tests. Small, and useful on
   their own.
2. The `zde-assist` uid, the `/run/zde` socket, and zded's second dispatch
   table. Nothing network-facing yet, and this is the item that decides whether
   the rest is real.
3. `zde-assist serve` on loopback, the approval surface, the audit files, the
   bar indicators. Usable, and honestly labelled as adding no authority.
4. The LAN bind, and `zde-assist connect`.

## 13. What this refuses to promise

- The read/act split does not survive a compromise of `zde-assist serve`
  (section 1). It defends against the loop and against one stolen certificate.
- The uid split does not make the assistant safe; it makes a compromised
  assistant cost the nine tools instead of the session.
- The act grant's replies are drawn from a closed vocabulary, which bounds what
  they can say to a few bits rather than to nothing (section 5). Whether a desk
  exists is still learnable, and so is the number of screens.
- `desk.switch` starts every app the target desk declares and closing their
  windows stops none of the containers. A client that walks the desk list has
  started everything on the machine, and only the rate limit and a person
  looking at the screen stand in the way.
- Revocation runs inside the process being revoked. `systemctl stop` and the
  socket's group are the two that do not (section 6).
- The listener accepts connections while nobody is logged in, and refuses every
  call in that state. The bar that would say so is not running then either
  (section 12).
- The bind list is not authentication and nothing here treats it as such. A
  container granted host networking is on loopback.
- Expiry is hygiene. Because the pin is on the key, the only revocation is
  removing the fingerprint, and it only means anything because it also drops
  live connections.
- `attn.center` and `clip.history` are refused for good, not deferred. There is
  no later phase in which the notification bodies and the clipboard become
  readable over a network.
- The sender of the assistant's own notification cards is impersonatable, like
  every sender except the exact word `zde` (section 11). The bar is what cannot
  be forged.
- Nothing here makes the model trustworthy. It makes the model's reach small,
  its actions undoable, its calls visible, and its credential separable from
  the text it reads. A model that decides to switch desks for no reason will
  switch desks, and the answer to that is that switching desks is one key back.

## Appendix: what would have to change elsewhere

Both documents named here are frozen, so this is the wording rather than an
edit.

**[`glossary.md`](glossary.md)** has no word for this. The entry would be:

| **assist** | the local assistant: an mTLS listener that offers a model a small set of reads and undoable actions over MCP |

**[`vision.md`](vision.md), principle 7** currently reads "The zded socket is
never mounted into a container; apps reach zde only via the notification bus
and declared grants." The assistant obeys it by not being a container, so
nothing has to change. If a later owner wants the option of the assistant in a
container, the sentence that would allow it without weakening it is: "The zded
socket is never mounted into a container; apps reach zde only via the
notification bus and declared grants. A socket answering a named subset of
methods is a grant like any other, and may be mounted where a manifest declares
it." That is offered as a wording, not a request: the uid is the cheaper answer
and it needs no reinterpretation.

**[`vision.md`](vision.md), section 2** lists the components. The assistant
would be one, between `ask` and `pass`, and its paragraph is the first two
sentences of this document.
