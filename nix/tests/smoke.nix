# Smoke test: boot zde in a QEMU VM and check that both layers actually arrive
# on a machine. This is where the host eval in `nix flake check` stops being
# enough - that one proves the modules evaluate, not that a machine comes up
# with a greeter and a config niri accepts.
#
# niri does start here, which took some finding: a build sandbox has no GPU, and
# niri has no software renderer of its own. It runs nested inside a headless
# cage on llvmpipe instead, which needs no /dev/dri at all. That is enough for
# everything zde does, because everything zde does is IPC - so the daemon and
# the client are exercised against a real compositor rather than a fake.
#
# What it still is not is a session on real hardware: no GPU, no logind seat,
# no input devices, no monitors coming and going (docs/roadmap.md, verify
# list). There is a Wayland seat - cage gives niri one and niri gives foot one
# - which is why a client can start at all.
{
  pkgs,
  zdeModule,
  homeManagerModule,
  zdeConfig,
  zincModule,
}:
let
  # A locker that locks nothing, under the name a real one has.
  #
  # The name is the part that matters: `zde doctor` looks a locker up in
  # /etc/pam.d by the basename of its binary, because a locker with no PAM
  # service takes the screen and then refuses every password. A fake called
  # `touch` made this session look like exactly that machine and failed a check
  # that was right to fail. Under the name layer 1 actually configures, both
  # stay honest - the leader chain still ends in a file on disk, and doctor
  # still has its teeth.
  fakeLocker = pkgs.writeShellScriptBin "swaylock" ''
    exec ${pkgs.coreutils}/bin/touch /tmp/zde-locked
  '';

  # An ask tier that needs no network, no key and no model: it reads the
  # question on stdin and answers with it. What this proves is the seam - a
  # home-manager option becomes a file, zded runs the command it names with the
  # question on its stdin, and what it says streams back - and deliberately not
  # anybody's model.
  fakeTier = pkgs.writeShellScriptBin "zde-fake-tier" ''
    exec ${pkgs.gnused}/bin/sed -e 's/^/answered: /'
  '';

  # One script, so the quoting lives in a shell file rather than inside a
  # Python string inside a Nix string.
  liveCheck = pkgs.writeShellScript "zde-live-check" ''
        set -euo pipefail
        # niri msg has no client-side deadline of its own: pointed at a
        # compositor that accepts and never answers it blocks for ever, and the
        # bounded loops below would never reach their second iteration.
        nirimsg() { timeout 10 niri msg "$@"; }
        # Bounded in seconds rather than in tries, because with that timeout a
        # try is worth up to eleven seconds: counting tries is not counting
        # time, and three loops of sixty could outlast the whole script's
        # budget with the driver killing it before it says why.
        waitfor() {
          secs=$1; shift
          end=$((SECONDS + secs))
          while [ "$SECONDS" -lt "$end" ]; do
            "$@" && return 0
            sleep 1
          done
          return 1
        }
        # Its own runtime directory: the zded started earlier in this test is
        # still listening on /tmp/rt, and it has never heard of a compositor.
        export XDG_RUNTIME_DIR=/tmp/live-rt
        mkdir -p "$XDG_RUNTIME_DIR" /tmp/desks
        chmod 700 "$XDG_RUNTIME_DIR"

        # A desk that does not exist yet, declared - with an app on it, which
        # nothing launches yet and which is still the half of a manifest that
        # says what the desk is for (docs/model.md, section 5).
        #
        # Dots in the app id, deliberately, and this is the only place the shape
        # of that id is chosen. Nearly every real application id has one -
        # org.mozilla.firefox, org.gnome.Nautilus - and a dot is two things at
        # once where this ends up: a metacharacter to the regex niri matches
        # with, and, once escaped for that, a backslash in a KDL string. This
        # fixture was zde-pinned-probe until an escaping bug shipped through it,
        # because an id with no metacharacter in it is the one shape that cannot
        # fail, and the whole point of the assertions below is that a real niri
        # reads what zded wrote.
        printf 'name: vshop
    monitors:
      winit: { workspaces: [code, notes] }
    apps:
      - { app: absent-app, instance: vshop, app_id: zde.pinned.probe, monitor: winit, workspace: code }
    '       > /tmp/desks/vshop.yaml

        # niri, nested and headless. cage gives it a Wayland host; pixman and
        # llvmpipe give both of them something to draw on without a GPU.
        export WLR_BACKENDS=headless WLR_RENDERER=pixman WLR_LIBINPUT_NO_DEVICES=1
        export LIBGL_ALWAYS_SOFTWARE=1
        cage -- niri >/tmp/niri.log 2>&1 &

        for i in $(seq 60); do
          set +e; NIRI_SOCKET=$(ls "$XDG_RUNTIME_DIR"/niri.*.sock 2>/dev/null | head -1); set -e
          [ -n "$NIRI_SOCKET" ] && break
          sleep 1
        done
        if [ -z "''${NIRI_SOCKET:-}" ]; then
          echo "niri never opened its IPC socket:"; cat /tmp/niri.log; exit 1
        fi
        export NIRI_SOCKET
        # The socket file appearing is not niri answering on it. Software
        # rendering in a VM is slow to get going, and zded's first question would
        # otherwise time out against a compositor that is not listening yet.
        niri_answers() { nirimsg version >/dev/null 2>&1; }
        if ! waitfor 60 niri_answers; then
          echo "niri opened $NIRI_SOCKET but never answered on it:"
          cat /tmp/niri.log; exit 1
        fi
        echo "niri is up on $NIRI_SOCKET"

        # First, the way a login actually starts the daemon, because everything
        # after this line starts it by hand and a person never does. niri is up
        # and this test is standing where niri stands: it imports the
        # environment into the user manager and brings up the target, which is
        # what niri --session does on its way to telling systemd it is ready.
        # Then zded has to appear on its own.
        #
        # Its own runtime dir is the manager's, not this script's, so the
        # daemon that comes up listens somewhere else than the one below and
        # the two never meet.
        # systemctl --user reaches the manager through XDG_RUNTIME_DIR, and
        # this script has pointed that at a directory of its own. So every call
        # here says where the manager actually is, rather than the script
        # moving to it and taking niri's socket path with it.
        mgr=/run/user/$(id -u)
        sctl() { XDG_RUNTIME_DIR=$mgr systemctl --user "$@"; }
        # niri names its IPC socket after the Wayland display it opened, so the
        # display comes back out of the socket path. The bar needs it: it is a
        # Wayland client, where zded only needs the IPC socket.
        #
        # Absolute, which is the part that matters here. A bare "wayland-1" is
        # resolved against XDG_RUNTIME_DIR, and the two directories in this
        # script are not the same one: niri opened its socket in the script's
        # own runtime dir, and the unit below runs with the user manager's.
        # Pointed at the wrong directory, Qt does not report a missing socket -
        # it fails to initialise its Wayland plugin and aborts with a backtrace
        # about platform plugins, which is a long way from the cause.
        # libwayland takes an absolute WAYLAND_DISPLAY as the socket itself.
        export WAYLAND_DISPLAY="$XDG_RUNTIME_DIR/$(basename "$NIRI_SOCKET" | cut -d. -f2)"
        if [ ! -S "$WAYLAND_DISPLAY" ]; then
          echo "niri's IPC socket is $NIRI_SOCKET but there is no wayland socket at $WAYLAND_DISPLAY:"
          ls -la "$XDG_RUNTIME_DIR"; exit 1
        fi
        sctl import-environment NIRI_SOCKET WAYLAND_DISPLAY
        # This machine has no GPU and QtQuick defaults to wanting one. On real
        # hardware the bar gets the same renderer niri does; here it gets Qt's
        # software one, set on the manager rather than in the unit so that the
        # unit stays honest about what it needs.
        sctl set-environment QT_QUICK_BACKEND=software

        # Nothing is on a layer yet, which is what makes "the bar is on a
        # layer" mean anything further down. It has to be asked before the
        # target starts, because the target is what starts the bar - asking
        # afterwards is asking whether the thing that just happened had not
        # happened yet. The json form, because the human one prints nothing at
        # all for an empty list and "nothing" is not something to grep for.
        nirimsg --json layers 2>&1 | tee /tmp/layers-before.txt
        if [ "$(cat /tmp/layers-before.txt)" != "[]" ]; then
          echo "something was already on a layer before the session started:"
          cat /tmp/layers-before.txt; exit 1
        fi

        # Pulled in rather than started: graphical-session.target refuses a
        # manual start, by design - it is meant to arrive as somebody's
        # dependency, which on a login is niri.service. NixOS ships this stand-in
        # for sessions that do not speak systemd, and it binds to the target the
        # same way, so what starts here is the real one.
        sctl start nixos-fake-graphical-session.target
        # Active and listening, not just active. systemd calls a Type=simple
        # unit active the moment it has forked, which is before zded has bound
        # anything - so waiting on the unit alone and then asking zde a question
        # is a race, and one this test lost on its third run after passing
        # twice. The socket is the readiness signal zded actually has.
        #
        # It is a real property of the system and not only of the test: anything
        # the session starts alongside zded can be up before the socket is. The
        # bar tolerates it by saying so and asking again; socket activation
        # would remove it, and would mean zded taking a passed fd.
        zded_up() { sctl is-active --quiet zded.service && [ -S "$mgr/zde/zded.sock" ]; }
        if ! waitfor 30 zded_up; then
          echo "the target came up and zded did not follow it:"
          sctl status zded.service || true
          ls -la "$mgr/zde" 2>&1 || true
          journalctl --user -u zded.service --no-pager | tail -20; exit 1
        fi
        # And it can see the compositor, which is the whole reason the unit
        # waits for the target rather than racing niri: NIRI_SOCKET is in the
        # environment only after niri has put it there.
        XDG_RUNTIME_DIR=$mgr zde status 2>&1 | tee /tmp/unit-status.txt
        grep -qx 'compositor connected' /tmp/unit-status.txt

        # The state snapshot, written by the unit the target pulled - which is
        # exactly what a login does on a real machine (zde.debug, nix/home.nix).
        # It is the one thing on this list written for a session that never
        # comes up, so what is asserted is that it arrives on a machine with no
        # GPU and is honest about it rather than failing.
        report_written() { sctl is-active --quiet zde-report.service; }
        if ! waitfor 60 report_written; then
          echo "the session started and nothing wrote a state snapshot:"
          sctl status zde-report.service || true
          journalctl --user -u zde-report.service --no-pager | tail -20; exit 1
        fi
        snap=$(ls -1 /var/log/zde/zde/*.txt 2>/dev/null | tail -1 || true)
        if [ -z "$snap" ]; then
          echo "zde-report.service ran and left no file in /var/log/zde/zde:"
          ls -la /var/log/zde /var/log/zde/zde 2>&1 || true
          journalctl --user -u zde-report.service --no-pager | tail -20; exit 1
        fi
        echo "the session start wrote $snap"
        # 0600 and this account's, which is the half of this that matters more
        # than the feature: it is a file about somebody's machine, sitting on a
        # disk, in a project written for people who mind about that.
        mode=$(stat -c %a "$snap"); owner=$(stat -c %U "$snap")
        [ "$mode" = 600 ] && [ "$owner" = zde ] || {
          echo "the snapshot is mode $mode and belongs to $owner"; exit 1
        }
        for want in graphics versions hardware doctor; do
          grep -qF "[$want]" "$snap" || {
            echo "[$want] is missing from $snap:"; cat "$snap"; exit 1
          }
        done
        # The graphics answer, which is the point of the exercise. This VM has
        # no GPU and its niri is nested under cage rather than started as
        # niri.service, so the two honest answers are "probably yes" - a
        # compositor answering with outputs and nothing in a log saying it fell
        # back - and "not known", which is what no niri.service log to read
        # looks like. What it must never say is that this machine fell back,
        # because it never went near a real card.
        # `|| true` because pipefail is on: a section with no answer line would
        # end the script here saying nothing, and the check below says it.
        answer=$(sed -n '/^\[graphics\]/,/^$/p' "$snap" | grep -E '^  answer +' | head -1 || true)
        printf '%s\n' "$answer" | grep -qE '^  answer +(probably yes|not known)' || {
          echo "the graphics answer is not one this machine could honestly give:"
          sed -n '/^\[graphics\]/,/^$/p' "$snap"; exit 1
        }
        # The verdict alone is not enough, because a compositor answering with a
        # screen for none of its outputs also reads "not known" - and that is the
        # black screen this section exists to catch, not a reading it is missing.
        # The string below is doctor's own sentence, pinned from the other side
        # by a Go test that constructs the state and reads this line out of this
        # file (internal/doctor, TestTheSmokeGuardRefusesTheBlackScreen).
        blackscreen='has a screen for none of its outputs' # pinned: graphics-black-screen
        case "$answer" in
          *"$blackscreen"*)
            echo "the snapshot says niri is answering and has nowhere to draw, which is the"
            echo "black screen this section exists to catch and not a reading it is missing:"
            sed -n '/^\[graphics\]/,/^$/p' "$snap"; exit 1 ;;
        esac
        # And it says which device the answer is about, on a machine that may
        # have no card at all: "none" is a reading, a missing line is a bug.
        grep -qE '^  device +' "$snap" || {
          echo "the snapshot does not say which device the answer is about:"
          sed -n '/^\[graphics\]/,/^$/p' "$snap"; exit 1
        }
        # The header, because somebody is going to paste this into a bug report
        # and the two questions they will have - is it safe to send, what is
        # missing from it - have to be answerable off the file itself.
        grep -qF 'no notification text' "$snap" || {
          echo "the snapshot does not say what it does not contain:"; head -30 "$snap"; exit 1
        }
        # And the two sentences it used to get wrong. The file carries the niri
        # lines it is judged off, so the header names that journal rather than
        # denying there is one; and it carries parameter names whose values it
        # has taken, so it says which. Both are the header telling the truth
        # about the file under it, which is the whole of what makes it pasteable.
        grep -qF 'No journal but the niri lines' "$snap" || {
          echo "the snapshot's header does not say which journal content is in it:"
          sed -n '1,/^\[graphics\]/p' "$snap"; exit 1
        }
        grep -qF 'counted and never named, because logind takes' "$snap" || {
          echo "the snapshot's header does not say what happens to an idle inhibitor's name:"
          sed -n '1,/^\[graphics\]/p' "$snap"; exit 1
        }
        # The bar is up and listening, so status has to say so. Waited for rather
        # than asked once: the target starts zded and the bar together, and Qt
        # takes a second or two to reach the socket, so a single question here
        # asks whether something that is still starting has started.
        #
        # This is the line somebody reads when a key drew nothing, and it is only
        # worth having if it is true in both directions - the false case is
        # asserted further down on the zded that has no shell at all.
        shell_seen() {
          XDG_RUNTIME_DIR=$mgr zde status 2>/dev/null | grep -qx 'shell      yes'
        }
        if ! waitfor 30 shell_seen; then
          # Not "the bar is running and": whether it is still running is the
          # other half of this and is what the unit status below answers.
          echo "nothing said a shell was listening in 30s:"
          XDG_RUNTIME_DIR=$mgr zde status; sctl status zde-bar.service || true; exit 1
        fi

        # And the bar, which the same target starts. Asserted against niri
        # rather than against systemd: an active unit proves quickshell did not
        # exit, and what a bar has to do is be on the screen. By name, not by
        # "something appeared" - quickshell names its surfaces after itself, and
        # a check that passes on any layer surface at all would pass on a
        # notification popup or on whatever comes next.
        # zde-bar is our namespace and not quickshell's default, which is the
        # point: the default would match any quickshell instance on the machine,
        # and would keep matching after zde grew a second surface.
        bar_layer() {
          nirimsg --json layers >/tmp/layers.txt 2>&1 &&
            grep -q '"namespace":"zde-bar"' /tmp/layers.txt
        }
        if ! waitfor 60 bar_layer; then
          echo "the bar never reached the screen:"
          cat /tmp/layers.txt
          sctl status zde-bar.service || true
          journalctl --user -u zde-bar.service --no-pager | tail -25; exit 1
        fi
        cat /tmp/layers.txt

        # And then what it shows, which is the half a layer surface says nothing
        # about: a bar that never read the queue, one stuck on a number from
        # five minutes ago, and one doing its job all look identical from
        # outside. So the bar answers for itself, over quickshell's own IPC.
        # Addressed by pid, so this needs neither the instance id nor the store
        # path the unit was built with.
        # --pid belongs to `ipc`, not before it: quickshell rejects it outright
        # in front of the subcommand. And stderr is kept rather than dropped,
        # because a failing call otherwise sets an empty answer that fails the
        # comparison below with nothing to say why - which is exactly how this
        # cost a CI round trip.
        barpid=$(sctl show -p MainPID --value zde-bar.service)
        barq() {
          XDG_RUNTIME_DIR=$mgr quickshell ipc --pid "$barpid" call queue "$1" 2>&1 |
            tr -d '\n' || true
        }
        pickerq() {
          XDG_RUNTIME_DIR=$mgr quickshell ipc --pid "$barpid" call picker "$@" 2>&1 |
            tr -d '\n' || true
        }

        # Height and reserved space first: zero of either is a surface the
        # compositor lists and a person cannot see, or one that quietly covers
        # the top of every window.
        geom=$(barq geometry)
        [ "$geom" = "26 26" ] || {
          echo "the bar came up as height/zone '$geom', wanted '26 26'"; exit 1
        }

        # No battery here, and the bar has to say so rather than show a stub. A
        # desktop is the same case as this VM, and a strip claiming 100% on a
        # machine with no battery is worse than one saying nothing.
        [ "$(barq battery)" = "none" ] || {
          echo "no battery on this machine and the bar reads '$(barq battery)'"; exit 1
        }

        # And no microphone, which is the same kind of case: layer 0 turns
        # PipeWire on and QEMU gives this VM no sound card, so there is no
        # source to find. What may never appear here is one of the other three:
        # a strip saying a room is being heard on a machine with no microphone
        # in it is the whole reason the widget exists.
        micread=$(barq mic)
        case "$micread" in
          none | unknown) ;;
          *) echo "no microphone on this machine and the bar reads '$micread'"; exit 1 ;;
        esac
        # Then "none" specifically, and not "unknown" as well. The two are
        # different facts - "none" is PipeWire answering that there is no
        # source, "unknown" is PipeWire not answering - and PipeWire is layer
        # 0's, so it does answer here. Accepting both would pass against a
        # subscription that never connected at all, which is the one failure
        # this can catch from outside a machine with real audio. Waited for
        # rather than read once: the session and the socket come up together.
        #
        # The value it last read, kept, rather than asked for again inside the
        # message: a second ask is a second question, and the answer names a
        # different fault each way. 'unknown' is PipeWire never answering;
        # 'muted' or 'open' would be a source this VM does not have.
        answered() { micread=$(barq mic); [ "$micread" = "none" ]; }
        if ! waitfor 20 answered; then
          echo "the mic widget last read '$micread', and 'none' is what PipeWire answering looks like on a machine with no source"
          journalctl --user -u zde-bar.service --no-pager | tail -25; exit 1
        fi

        # No NetworkManager either (zde.laptop is off for this node), which the
        # bar has to say as a state of its own rather than as "no network":
        # this VM is perfectly online and simply has nothing to ask. Same
        # argument as the battery above, and the first tick may not have
        # answered yet, so this waits.
        no_manager() { netread=$(barq net); [ "$netread" = "absent" ]; }
        if ! waitfor 20 no_manager; then
          echo "no NetworkManager here and the bar last read '$netread'"; exit 1
        fi

        # The idle hold. Nothing on this VM takes an idle inhibitor, so the two
        # readings that may appear are "none" - logind answering with an empty
        # table - and "unknown", which is logind not answering at all.
        #
        # Deliberately not waited into "none" the way the mic is waited into it.
        # The mic's answer is guaranteed here because PipeWire is layer 0's and
        # does come up; logind's is not, which is exactly why the doctor line
        # above accepts warn as well as ok (line 12: no logind seat). Demanding
        # "none" would make this test assert something about the VM's session
        # management rather than about the widget.
        #
        # What it does catch is the two failures that are the widget's own. An
        # empty string is the IPC call not landing at all, which is how a
        # function wired to nothing answers; and "held" on a machine where
        # nothing took an inhibitor is a strip inventing the one fact it exists
        # to report, which is worse than a strip that says nothing.
        idleread=$(barq idle)
        case "$idleread" in
          none | unknown) ;;
          "") echo "the bar's idle reading came back empty, so the call never landed"; exit 1 ;;
          *) echo "nothing holds an idle inhibitor here and the bar reads '$idleread'"; exit 1 ;;
        esac

        # Then the count, against a queue that changes underneath it. "0 0"
        # before, "1 0" after: the poll is two seconds, so both of these wait
        # rather than asking once and hoping.
        #
        # The before reading waits too, which it did not. The bar answers
        # 'unlinked' until its own socket is up and 'unknown' until the first
        # reply parses, and both are briefly true after a session starts - so
        # asking once here races the bar's first poll and reads as a bar that
        # miscounted an empty queue.
        was_empty() { empty=$(barq count); [ "$empty" = "0 0" ]; }
        if ! waitfor 20 was_empty; then
          echo "nothing has been added yet and the bar last read '$empty' for the queue"
          sctl status zde-bar.service || true
          journalctl --user -u zde-bar.service --no-pager | tail -25; exit 1
        fi
        XDG_RUNTIME_DIR=$mgr zde queue add something for the bar to count >/dev/null
        counted() { counts=$(barq count); [ "$counts" = "1 0" ]; }
        if ! waitfor 20 counted; then
          echo "the queue has one item and the bar last read '$counts'"
          sctl status zde-bar.service || true
          journalctl --user -u zde-bar.service --no-pager | tail -25; exit 1
        fi

        # The picker: the surface Mod+Tab opens. A key spawns `zde desk switcher`,
        # zded tells whoever is listening, and the shell draws it - so this
        # exercises the whole path, including the event stream that did not exist
        # until the picker needed it.
        #
        # A desk to pick, first. Nothing is named yet at this point in the test,
        # and a picker with no desks is a refusal rather than a surface. Named
        # `probe` and unnamed again afterwards, because a desk left behind here
        # would join the rotation the later tests count on: alphabetically it
        # would sit between haven and vshop, and `desk next` would stop
        # answering what those tests expect.
        nirimsg action set-workspace-name probe.winit.one >/dev/null
        ondesk() { XDG_RUNTIME_DIR=$mgr zde status 2>/dev/null | grep '^on desk' || true; }
        # What a switch moves: the focused workspace. Asked of niri rather than
        # of zded, because zded's idea of the desk you are on also changes when
        # the watcher notices a workspace being named - which happens moments
        # after this block names one, and is not the picker's doing.
        focusedws() {
          nirimsg --json workspaces 2>/dev/null | tr '{' '\n' |
            grep '"is_focused":[[:space:]]*true' | grep -o '"name":"[^"]*"' | head -1
        }
        # Which window has focus, as '"id":N'. Defined here rather than where nav
        # first needed it, because the window picker below asks the same question
        # and two spellings of it would eventually answer differently.
        focused_window() { nirimsg --json focused-window 2>/dev/null | grep -o '"id":[0-9]*' | head -1; }
        # How many windows are open. One object per line first: niri answers on a
        # single line, so counting matching lines without splitting says 1 for
        # any number of windows at all.
        count_windows() { nirimsg --json windows 2>/dev/null | tr '{' '\n' | grep -c '"id":' || true; }
        [ "$(pickerq state)" = "closed" ] || {
          echo "the picker is open before anything asked for it: $(pickerq state)"; exit 1
        }
        # A key that asks for a surface, and hears back. Every one of these is
        # the same round trip - verb, daemon, event, surface, token back - and
        # the daemon gives the shell 200ms to send the token (internal/zded,
        # ackWait) before deciding nothing drew anything and printing its list
        # to what on a real session is a keybind's stdout. So silence is the
        # pass, and the file holds whatever it printed instead.
        #
        # Asked once, that 200ms is a race written as an assertion. This VM is
        # software-rendered and CI builds several of them at a time, so a Qt
        # process that answers in a millisecond on a machine can miss the window
        # with nothing whatever wrong with it - which is how four unrelated
        # branches failed this file inside four minutes and passed either side.
        #
        # So it is asked again until it lands, inside a budget, and what is
        # reported is what was seen rather than a cause: how many asks it took,
        # and whether zded had a listener at all. That last is the whole
        # difference between a shell that is not there and one that was merely
        # late, and it is exactly what the old message ("a shell was listening
        # and it printed the list anyway") asserted without ever asking.
        #
        # Asking again is safe by construction and deliberately so: show()
        # acknowledges a surface that is already up (shell/Picker.qml,
        # ActionPalette.qml, NotifCenter.qml), because a key pressed twice must
        # not print either. That property is asserted on its own below.
        #
        # Twenty seconds, like every other wait on this shell drawing something
        # (picker_open, palette_open, one_surface): what is waited on is one Qt
        # process handling one event, not a unit starting. Named once and read
        # by the message, so that changing the budget cannot leave the sentence
        # claiming a number nobody waited.
        ack_budget=20
        acked() {
          ack_out=$1; shift
          ack_n=0
          ack_end=$((SECONDS + ack_budget))
          while :; do
            ack_n=$((ack_n + 1))
            XDG_RUNTIME_DIR=$mgr "$@" >"$ack_out" 2>&1 || true
            if [ ! -s "$ack_out" ]; then
              echo "$* was acknowledged, on ask $ack_n"
              return 0
            fi
            [ "$SECONDS" -lt "$ack_end" ] || break
            sleep 1
          done
          # "wrote something", not "printed its list": the other way this ends
          # is the client's own five second deadline (internal/zded/client.go)
          # expiring against a starved daemon, and that writes an error here
          # rather than a list. Saying which is the reader's job, and the file
          # below is what they read.
          echo "$* wrote to a keybind's stdout on all $ack_n asks in ''${ack_budget}s, and with a shell acknowledging it writes nothing."
          echo "what zded says about this session:"
          XDG_RUNTIME_DIR=$mgr zde status 2>&1 || true
          echo "and what the last ask wrote:"
          cat "$ack_out"
          journalctl --user -u zde-bar.service --no-pager | tail -25
          return 1
        }

        # Nothing printed, which means more than it used to: the shell has to
        # acknowledge the event before the verb calls it shown, so silence here
        # is the whole round trip - key, daemon, event, surface, token back.
        # A shell that read the socket and drew nothing would print the list.
        acked /tmp/switcher.txt zde desk switcher || {
          echo "Mod+Tab would have printed the desk list over the screen instead of opening the picker"
          exit 1
        }
        # Exactly what it should be showing: desks rather than windows, one of
        # them, and the one we are on. A match on "open" alone would pass a
        # picker that came up empty, or one that does not know where you are -
        # which is what Enter lands on - and now also one showing the wrong
        # rows, since both pickers are the same surface.
        picker_open() { [ "$(pickerq state)" = "open desks 1 probe" ]; }
        if ! waitfor 20 picker_open; then
          echo "the picker never opened with the right contents: $(pickerq state)"
          journalctl --user -u zde-bar.service --no-pager | tail -20; exit 1
        fi
        # One layer surface by namespace, or nothing. One object per line first:
        # niri answers on a single line with every surface on it, so a grep
        # without splitting is a grep about the whole screen - it would read the
        # bar's namespace and another surface's keyboard grab as one entry.
        layerof() {
          nirimsg --json layers 2>/dev/null | tr '{' '\n' | grep "\"namespace\":\"$1\"" || true
        }
        # How many surfaces hold the keyboard. Exactly one is the rule this
        # shell now keeps: they are all full-screen overlays taking an exclusive
        # grab, and niri gives that grab to the oldest of them while drawing the
        # newest on top.
        grabbers() {
          nirimsg --json layers 2>/dev/null | tr '{' '\n' |
            grep -c '"keyboard_interactivity":"Exclusive"' || true
        }
        # On the screen, and named ours: the bar is on a layer too, so this looks
        # for the picker's own namespace rather than any surface at all.
        #
        # Waited for rather than asked once. pickerq state answers off
        # picker.visible, a QML property set inside show(); the layer surface is
        # what quickshell commits afterwards and what niri lists after that - so
        # "open" is true before there is anything on a layer. On a machine that
        # gap is one round trip; here it is a Qt process on llvmpipe against
        # however many VMs the runner is building.
        picker_layer() {
          nirimsg --json layers >/tmp/layers-picker.txt 2>&1 &&
            grep -q '"namespace":"zde-picker"' /tmp/layers-picker.txt
        }
        if ! waitfor 20 picker_layer; then
          echo "the picker says it is open and niri has no such layer:"
          cat /tmp/layers-picker.txt; exit 1
        fi
        # And it has the keyboard, which is the half that makes it usable and the
        # half nothing else here would notice: driving it through IPC works just
        # as well with the keyboard never taken. niri prints the interactivity in
        # the same reply, so this costs nothing.
        #
        # Read off the picker's own entry rather than grepped across the whole
        # reply. Across it, this only says that something on the screen has the
        # keyboard - which is also true when the grab is on the surface
        # underneath, the fault checked below - and it held here at all because
        # the bar happens to set no keyboardFocus.
        # Waited for on its own, and not folded into the wait above, because the
        # grab is not part of the surface arriving: a run of this test caught
        # zde-picker on a layer reading "keyboard_interactivity":"None" and
        # passed anyway, because the next line asked niri a second time and by
        # then the grab had landed. Asked once, that is the same coin toss with
        # nothing to say it happened.
        picker_grabs() { layerof zde-picker | grep -q '"keyboard_interactivity":"Exclusive"'; }
        if ! waitfor 20 picker_grabs; then
          echo "the picker is on screen without the keyboard, so no key would reach it:"
          nirimsg --json layers; exit 1
        fi

        # Mod+Tab again, on a picker that is already up. Nothing about the
        # surface changes, so the handler that acknowledges the event never
        # fires unless show() says so itself - and without that the key waits
        # out its whole ack window, decides no shell drew anything, and prints
        # every desk to a keybind's stdout over whatever is on the screen.
        acked /tmp/switcher-again.txt zde desk switcher || {
          echo "the picker was already up, and asking again would have printed the desk list over it"
          exit 1
        }
        picker_open || {
          echo "asking again while it was up left the picker as: $(pickerq state)"; exit 1
        }

        # And the notification center over the top of it, which is what this
        # section is really about. Every surface here is a full-screen overlay
        # taking an exclusive keyboard grab, and niri picks the holder of that
        # grab in map order, oldest first - so two of them up at once puts the
        # keys on the one underneath. On this pair that is a desk list nobody
        # can see reading Enter and the digits, which switch desk, and l, which
        # locks the screen.
        #
        # So: one at a time. The surface asked for last is up and has the
        # keyboard, and the one before it is off the screen rather than merely
        # behind - which is also what stops two full-screen dims compositing
        # into a darker one.
        acked /tmp/center-over-picker.txt zde system notif-center || {
          echo "Mod+n would have printed the notification history over the screen instead of opening the center"
          exit 1
        }
        one_surface() {
          [ -z "$(layerof zde-picker)" ] && [ "$(grabbers)" = "1" ] &&
            layerof zde-notif-center | grep -q '"keyboard_interactivity":"Exclusive"'
        }
        if ! waitfor 20 one_surface; then
          echo "the center opened over the picker and both are up, so the keyboard is on the one underneath ($(pickerq state)):"
          nirimsg --json layers; exit 1
        fi

        # And back the other way, which is the same rule from the other side and
        # is how the picker comes to be up again for everything below: asking
        # for it takes the center off the screen rather than stacking under it.
        # Through acked rather than bare with its output thrown away, which is
        # how this line used to be written and how a run of this test died with
        # nothing to read: a `zde` that misses its own five second deadline
        # against a busy daemon exits non-zero, set -e ends the script there,
        # and the only trace is one line of stderr on the machine's console
        # about a socket timing out. It is the same round trip as its
        # neighbours and it gets the same treatment.
        acked /tmp/switcher-over-center.txt zde desk switcher || {
          echo "asking for the picker while the center was up never came back clean"
          exit 1
        }
        back_to_picker() {
          picker_open && [ -z "$(layerof zde-notif-center)" ] && [ "$(grabbers)" = "1" ]
        }
        if ! waitfor 20 back_to_picker; then
          echo "the picker was asked for over the center and the two are both up: $(pickerq state)"
          nirimsg --json layers; exit 1
        fi

        # Dismissing, before choosing: the way out that changes nothing. It is
        # the path Escape takes, and without this the whole hide-without-picking
        # branch is dead code as far as CI is concerned.
        before_dismiss=$(focusedws)
        [ "$(pickerq dismiss)" = "closed" ] || { echo "dismiss said $(pickerq dismiss)"; exit 1; }
        dismissed() { [ "$(pickerq state)" = "closed" ] && [ "$(nirimsg --json layers 2>/dev/null | grep -c zde-picker)" = "0" ]; }
        if ! waitfor 15 dismissed; then
          echo "the picker was dismissed and is still there: $(pickerq state)"
          nirimsg --json layers; exit 1
        fi
        # And nothing moved: dismissing is not choosing.
        [ "$(focusedws)" = "$before_dismiss" ] || {
          echo "dismissing the picker moved focus from $before_dismiss to $(focusedws)"; exit 1
        }

        # The leader: a letter that is not navigation runs an action and closes
        # the surface. Mod+Tab then l is what the keymap has been calling a
        # sequence that needs the input daemon, and it does not: the picker holds
        # the keyboard, so the second press is one this surface can read.
        #
        # Reopened first, because dismissing closed it.
        acked /tmp/switcher-leader.txt zde desk switcher || {
          echo "reopening the picker for the leader check never came back clean"
          exit 1
        }
        if ! waitfor 20 picker_open; then
          echo "the picker did not reopen for the leader check: $(pickerq state)"; exit 1
        fi
        rm -f /tmp/zde-locked
        [ "$(pickerq act lock)" = "acted" ] || {
          echo "the leader did not act: $(pickerq act lock)"; exit 1
        }
        # And the command ran: picker, shell, zde, the apps table, an exec. The
        # locker on this machine is a touch rather than swaylock, because a VM
        # with no input devices that locks itself cannot unlock itself - what is
        # asserted is the chain, and whether a real locker takes the screen is a
        # by-hand item (docs/verify.md).
        ran() { [ -e /tmp/zde-locked ]; }
        if ! waitfor 20 ran; then
          echo "the leader acted and nothing ran"; exit 1
        fi
        if ! waitfor 15 dismissed; then
          echo "the leader acted and the picker stayed on screen: $(pickerq state)"; exit 1
        fi

        # Then open it again for the half that does choose.
        acked /tmp/switcher-pick.txt zde desk switcher || {
          echo "reopening the picker for the pick check never came back clean"
          exit 1
        }
        if ! waitfor 20 picker_open; then
          echo "the picker did not reopen: $(pickerq state)"; exit 1
        fi

        # And choosing takes you there. Through the same IPC, because a machine
        # with no input devices cannot press Enter - what is being checked is the
        # wiring behind the key: the shell asks zded, zded switches the desk.
        [ "$(pickerq pick probe)" = "picked" ] || {
          echo "choosing probe from the picker: $(pickerq pick probe)"; exit 1
        }
        landed() { case "$(ondesk)" in *probe*) return 0 ;; *) return 1 ;; esac; }
        if ! waitfor 20 landed; then
          echo "the picker chose probe and zded is on '$(ondesk)'"; exit 1
        fi
        # And it closed itself on the way, surface and all.
        shut() { [ "$(pickerq state)" = "closed" ] && [ "$(nirimsg --json layers 2>/dev/null | grep -c zde-picker)" = "0" ]; }
        if ! waitfor 15 shut; then
          echo "the picker chose a desk and stayed on screen: $(pickerq state)"
          nirimsg --json layers; exit 1
        fi
        # The same surface again, for the two verbs that send something to a
        # desk rather than going to one. Their chords spawn the verb with no
        # name after it, because a desk is called whatever the person called it
        # and a shipped keymap cannot carry that - so this is the whole of what
        # Mod+Ctrl+Tab and Mod+Ctrl+Shift+Tab do, and the rows are where the
        # name comes from.
        #
        # What a chosen row means travels as the event's kind, which is why the
        # kind is asserted rather than the fact that something opened: the rows
        # are identical to the switcher's, so a surface that came up as "desks"
        # here is one whose Enter switches desk on a key that was meant to move
        # a window - the wrong verb, silently, on the desk somebody picked.
        acked /tmp/move-window-picker.txt zde desk move-window-to || {
          echo "zde desk move-window-to with no desk named would have printed the desk list instead of opening the picker"
          exit 1
        }
        # Two rows on a machine with one desk: the regulars are offered before
        # there is a band, since handing something to it is the only way one
        # ever comes into being. And the cursor starts on probe, the desk we are
        # already on, so Enter alone moves nothing anywhere.
        move_picker() { [ "$(pickerq state)" = "open desks-move-window 2 probe" ]; }
        if ! waitfor 20 move_picker; then
          echo "the move picker never opened with the right contents: $(pickerq state)"
          journalctl --user -u zde-bar.service --no-pager | tail -20; exit 1
        fi
        [ "$(pickerq dismiss)" = "closed" ] || { echo "dismiss said $(pickerq dismiss)"; exit 1; }
        if ! waitfor 15 dismissed; then
          echo "the move picker was dismissed and is still there: $(pickerq state)"
          nirimsg --json layers; exit 1
        fi

        # And the workspace one, which is the verb the regulars depend on.
        acked /tmp/move-workspace-picker.txt zde desk move-workspace-to || {
          echo "zde desk move-workspace-to with no desk named would have printed the desk list instead of opening the picker"
          exit 1
        }
        move_ws_picker() { [ "$(pickerq state)" = "open desks-move-workspace 2 probe" ]; }
        if ! waitfor 20 move_ws_picker; then
          echo "the workspace move picker never opened with the right contents: $(pickerq state)"
          journalctl --user -u zde-bar.service --no-pager | tail -20; exit 1
        fi
        [ "$(pickerq dismiss)" = "closed" ] || { echo "dismiss said $(pickerq dismiss)"; exit 1; }
        if ! waitfor 15 dismissed; then
          echo "the workspace move picker was dismissed and is still there: $(pickerq state)"
          nirimsg --json layers; exit 1
        fi

        # Mod+w, which is the same surface with different rows: zded hands over
        # the open windows, the shell draws them, and choosing one goes to it.
        #
        # Nothing is open yet, and that is a refusal rather than an empty
        # surface - a picker with no rows is one you have to press Escape to get
        # out of, and it answers a question nobody asked.
        if XDG_RUNTIME_DIR=$mgr zde window jump-to 2>&1 | tee /tmp/jump-none.txt; then
          echo "jump-to opened a picker with nothing to show:"
          cat /tmp/jump-none.txt; exit 1
        fi
        grep -q 'nothing is open' /tmp/jump-none.txt || {
          echo "the refusal does not say why there is nothing to pick:"
          cat /tmp/jump-none.txt; exit 1
        }
        [ "$(pickerq state)" = "closed" ] || {
          echo "a picker came up with no windows in it: $(pickerq state)"; exit 1
        }

        # Two windows, because with one, a jump to the right window, a jump to
        # the wrong one and a jump that did nothing all leave the same window
        # focused. Redirected, like every other client this script starts.
        foot -e sleep 600 >/tmp/jump-foot1.log 2>&1 &
        foot -e sleep 600 >/tmp/jump-foot2.log 2>&1 &
        two_windows() { [ "$(count_windows)" -ge 2 ]; }
        if ! waitfor 60 two_windows; then
          echo "the two windows to jump between never appeared:"
          cat /tmp/jump-foot1.log /tmp/jump-foot2.log; nirimsg windows; exit 1
        fi

        # Which window has focus, and the other one, both read now rather than
        # once the picker is up. A layer surface holding the keyboard reads to
        # niri as nothing focused at all - the roadmap's own warning, and the
        # reason the picker takes focus only while visible - so asked with the
        # picker on screen this answers nothing and takes the script with it.
        onnow=$(focused_window) || true
        onnow=''${onnow##*:}
        [ -n "$onnow" ] || {
          echo "two windows are open and niri has none of them focused:"
          nirimsg windows; exit 1
        }
        target=$(nirimsg --json windows 2>/dev/null | tr '{' '\n' |
          grep -o '"id":[0-9]*' | cut -d: -f2 | grep -v "^$onnow$" | head -1) || true
        [ -n "$target" ] || {
          echo "no second window to choose: everything reports the focused id ($onnow)"
          nirimsg windows; exit 1
        }

        # Nothing printed: a shell drew it and said so, the same round trip the
        # desk switcher makes - key, daemon, event, surface, token back.
        acked /tmp/jump-shell.txt zde window jump-to || {
          echo "Mod+w would have printed the window list over the screen instead of opening the picker"
          exit 1
        }
        # Both windows, and the window picker rather than the desk one: the two
        # share a surface now, so a Mod+w that opened the desks would otherwise
        # look exactly like this from outside. The dash is "no row you are
        # already on", which a list of windows has none of.
        jumper_open() { [ "$(pickerq state)" = "open windows 2 -" ]; }
        if ! waitfor 20 jumper_open; then
          echo "the window picker never opened with the right contents: $(pickerq state)"
          journalctl --user -u zde-bar.service --no-pager | tail -20; exit 1
        fi
        # Waited for, for the reason the desk picker's own layer check is: the
        # state is a QML property and the surface is a commit niri has still to
        # process, so "open" precedes "on a layer" by however long this machine
        # takes to get a frame's worth of work done.
        jumper_layer() {
          nirimsg --json layers >/tmp/layers-jump.txt 2>&1 &&
            grep -q '"namespace":"zde-picker"' /tmp/layers-jump.txt
        }
        if ! waitfor 20 jumper_layer; then
          echo "the window picker says it is open and niri has no such layer:"
          cat /tmp/layers-jump.txt; exit 1
        fi

        # The window that is not the one already focused - the newest has it, and
        # this was read before the picker took the keyboard - so that what is
        # asserted afterwards is a jump and not a no-op.
        [ "$(pickerq pick "$target")" = "picked" ] || {
          echo "choosing window $target: $(pickerq pick "$target")"; exit 1
        }
        # By id, and asked of niri. "a window is focused" is true before this
        # runs, so it would pass with the jump landing anywhere at all.
        jumped() { [ "$(focused_window)" = '"id":'"$target" ]; }
        if ! waitfor 20 jumped; then
          echo "the picker chose window $target and niri has $(focused_window) focused"
          nirimsg windows; exit 1
        fi
        # And it closed itself on the way, surface and all.
        if ! waitfor 15 shut; then
          echo "the picker chose a window and stayed on screen: $(pickerq state)"
          nirimsg --json layers; exit 1
        fi

        # Both windows closed again. Everything below this line was written for
        # a strip with a known set of windows on it, and a foot left here is a
        # window on whichever workspace was focused when it started - which is
        # how this test failed once already.
        nirimsg action close-window >/dev/null
        one_window() { [ "$(count_windows)" -le 1 ]; }
        if ! waitfor 30 one_window; then
          echo "the first jump window would not close:"; nirimsg windows; exit 1
        fi
        nirimsg action close-window >/dev/null
        no_windows() { [ "$(count_windows)" -eq 0 ]; }
        if ! waitfor 30 no_windows; then
          echo "a jump window outlived its test, and later checks need the strip as it was:"
          nirimsg windows; exit 1
        fi

        # Mod+semicolon: the palette. The same round trip as the picker - key,
        # daemon, event, surface, token back - with the two halves that are its
        # own: the filter, and running a row, which has to do what the row's key
        # would have done rather than merely close.
        paletteq() {
          XDG_RUNTIME_DIR=$mgr quickshell ipc --pid "$barpid" call palette "$@" 2>&1 |
            tr -d '\n' || true
        }
        acked /tmp/palette.txt zde palette || {
          echo "Mod+semicolon would have printed every action over the screen instead of opening the palette"
          exit 1
        }
        palette_open() { case "$(paletteq state)" in "open "*) return 0 ;; *) return 1 ;; esac; }
        if ! waitfor 20 palette_open; then
          echo "the palette never opened: $(paletteq state)"
          journalctl --user -u zde-bar.service --no-pager | tail -20; exit 1
        fi
        # Typing narrows it. The second number is what is left on screen, and a
        # filter that did nothing would leave it equal to the first.
        #
        # The row is the wallpapers widget, which nothing is written behind
        # and nothing in flight is writing. It used to be desk.panic, and panic
        # is written now - what these two lines need is a dead row, and picking
        # one somebody is about to bring to life turns this red for a reason
        # that has nothing to do with the palette.
        narrowed() { case "$(paletteq state)" in *" 1") return 0 ;; *) return 1 ;; esac; }
        paletteq filter system.wallpapers >/dev/null
        if ! waitfor 15 narrowed; then
          echo "typing into the palette did not narrow it: $(paletteq state)"; exit 1
        fi
        # A row with nothing written behind it refuses to run, and the surface
        # stays up saying why. This is the whole design - a key that does
        # nothing silently, and a palette that says so instead - and until this
        # line nothing in CI ever touched it.
        [ "$(paletteq run system.wallpapers)" = "cannot" ] || {
          echo "the palette ran a row nothing is written behind: $(paletteq run system.wallpapers)"; exit 1
        }
        [ "$(paletteq state)" != "closed" ] || {
          echo "refusing a dead row closed the palette, so nobody saw the reason"; exit 1
        }

        # And the row that does work does what Mod+Ctrl+semicolon does. The
        # locker on this machine is a touch, so what is asserted is the chain:
        # surface, shell, zded, the spawn, the apps table, an exec.
        # Filtered by the description and not by the name: the palette matches
        # on both, and `system.lock` stopped being one row the day
        # `system.lock-preset` was registered beside it. The name is still what
        # `run` is given below - this narrows to one row so that the surface's
        # filter is what is being asserted.
        paletteq filter "lock the screen" >/dev/null
        if ! waitfor 15 narrowed; then
          echo "the palette did not narrow to the row to run: $(paletteq state)"; exit 1
        fi
        rm -f /tmp/zde-locked
        [ "$(paletteq run system.lock)" = "ran" ] || {
          echo "running system.lock from the palette: $(paletteq run system.lock)"; exit 1
        }
        if ! waitfor 20 ran; then
          # The daemon's log, because the two ways this fails look identical
          # from here: a row zded marked dead and refused, and a spawn that
          # could not find what it was asked to start.
          echo "the palette ran system.lock and nothing locked"
          journalctl --user -u zded.service --no-pager | tail -20; exit 1
        fi
        # And it took itself off the screen, which now says more than it used
        # to: the surface waits for zded's answer and hides on the one that
        # worked, so a palette still up here is a reply that never arrived or a
        # refusal nobody would have seen. A palette left holding the keyboard
        # also reads to niri as nothing focused at all, which every check below
        # this line would then answer wrongly.
        palette_shut() {
          [ "$(paletteq state)" = "closed" ] &&
            [ "$(nirimsg --json layers 2>/dev/null | grep -c zde-palette)" = "0" ]
        }
        if ! waitfor 15 palette_shut; then
          echo "the palette ran an action and stayed on screen: $(paletteq state)"
          nirimsg --json layers; exit 1
        fi

        # And the probe desk goes away again, so the rotation tests further down
        # see the two desks they were written for.
        nirimsg action focus-workspace probe.winit.one >/dev/null
        nirimsg action unset-workspace-name >/dev/null
        nirimsg workspaces 2>&1 | tee /tmp/ws-probe.txt
        if grep -q 'probe.winit.one' /tmp/ws-probe.txt; then
          echo "the probe desk outlived its test and will join the rotation:"
          cat /tmp/ws-probe.txt; exit 1
        fi

        # And that it comes back. zded is restarted by every home-manager switch
        # that changes it, and the bar's own unit is not restarted with it - so a
        # socket that does not redial leaves the bar blind for the rest of the
        # session. It did, until this test existed.
        sctl restart zded.service
        if ! waitfor 30 zded_up; then
          echo "zded did not come back:"; sctl status zded.service || true; exit 1
        fi
        # Every answer the bar gave, in order, with the socket's state at that
        # moment beside it. Three things this could not say before, in the order
        # they cost somebody a diagnosis.
        #
        # The mechanism under test is the one thing the log could not see. The
        # redial is a two second timer that sets connected and returns without
        # writing anything (shell/shell.qml), so recovery is two ticks plus a
        # round trip - and nothing anywhere records that an attempt was made. A
        # bar halfway through retrying and a bar that has stopped retrying both
        # answer 'unlinked'. The run of answers is as close to that mechanism as
        # anything outside the process can stand.
        #
        # How much of the budget it actually got. barq is a `quickshell ipc`
        # process per ask and waitfor sleeps a second between them, so a runner
        # building four VMs at once may get five asks out of the budget rather
        # than sixty - and a message quoting only the last value cannot tell a
        # check that ran five times from one that ran sixty.
        #
        # And whether the socket was there at all when it asked. A restarting
        # zded deletes the socket and recreates it, and an ask that lands in
        # that window is not the bar failing to redial.
        : > /tmp/recover.txt
        recovered() {
          if [ -S "$mgr/zde/zded.sock" ]; then sock_now=sock; else sock_now=nosock; fi
          seen=$(barq count)
          printf '%ss\t%s\t%s\n' "$SECONDS" "$sock_now" "$seen" >> /tmp/recover.txt
          [ "$seen" = "1 0" ]
        }
        # Sixty rather than thirty, to match the neighbours that wait on a
        # process doing something rather than on a file appearing: bar_layer,
        # two_windows and foot_window all get sixty. zded_up above keeps thirty,
        # because that is systemd starting a unit it already has on disk. Named
        # once and read by the message below, so the budget and the sentence
        # about it cannot drift apart.
        recover_budget=60
        if ! waitfor "$recover_budget" recovered; then
          polls=$(wc -l < /tmp/recover.txt)
          last=$(tail -1 /tmp/recover.txt | cut -f3)
          echo "the bar was asked $polls times in ''${recover_budget}s and answered: $(cut -f3 /tmp/recover.txt | sort -u | paste -sd'|' -)"
          cat /tmp/recover.txt
          # Which of the three facts recovery is made of failed. One message
          # blamed the socket for all three, and for two of them that sentence
          # is false - it would send the next person after the wrong thing.
          case "$last" in
            unlinked)
              # With 'sock' against every line above, the daemon is there and
              # the bar is not reaching it, which is the redial and not the
              # daemon. The first place to look is a redial that only gets one
              # try: quickshell's Socket keeps the dead QLocalSocket when a
              # connect fails and dials only when it has none (src/io/socket.cpp,
              # onSocketError and setConnected), so an attempt that lands in the
              # window where zded has deleted the socket and not yet made it
              # again is the last attempt there will be.
              echo "the bar's socket to zded never came back, so the redial is what to look at" ;;
            unknown)
              echo "the bar is connected and nothing it read parsed as a queue, which is the reply and not the socket" ;;
            "0 0")
              echo "the bar is connected and reading, and the queue it reads is empty: the item did not survive the restart, which is the journal and not the socket" ;;
            *)
              echo "the bar answered '$last', which is neither a count nor a state it has a word for" ;;
          esac
          # Whose fault it is, which the bar's own journal cannot say: from that
          # side "the bar cannot reach a healthy daemon" and "nobody can reach
          # the daemon" read identically. The `zde queue` below is the line that
          # separates them - one CLI, one socket, one answer, no Qt in the way.
          ls -la "$mgr/zde/" || true
          sctl status zded.service || true
          journalctl --user -u zded.service --no-pager | tail -25
          journalctl --user -u zde-bar.service --no-pager | tail -25
          echo "and what the socket answers a CLI:"
          XDG_RUNTIME_DIR=$mgr zde queue || true
          exit 1
        fi
        # And on the way past, what it took to get here. A run that reconnects
        # on its fortieth ask out of sixty seconds is one slower runner away
        # from being the failure above, and nothing else in the log would say
        # so - which is the whole complaint about the check this replaced.
        echo "the bar reconnected on ask $(wc -l < /tmp/recover.txt):"
        cat /tmp/recover.txt

        XDG_RUNTIME_DIR=$mgr zde queue 2>&1 | tee /tmp/bar-queue.txt
        grep -q 'something for the bar to count' /tmp/bar-queue.txt
        id=$(grep 'something for the bar' /tmp/bar-queue.txt | cut -f1)
        XDG_RUNTIME_DIR=$mgr zde queue done "$id" >/dev/null

        # PartOf, which is what keeps one session's daemon out of the next
        # one's way: the target going down takes zded with it, and zded on its
        # way out takes its socket. Ending the session is ending what held the
        # target up - graphical-session.target is StopWhenUnneeded, so letting
        # go of it is how a session ends rather than stopping it by name.
        sctl stop nixos-fake-graphical-session.target
        gone() { ! sctl is-active --quiet zded.service; }
        if ! waitfor 15 gone; then
          echo "the session ended and zded stayed:"
          sctl status zded.service || true; exit 1
        fi
        socket_gone() { [ ! -e "$mgr/zde/zded.sock" ]; }
        if ! waitfor 15 socket_gone; then
          echo "zded left $mgr/zde/zded.sock behind for the next session"; exit 1
        fi
        # The bar goes with it, and takes its surface off the screen. A bar left
        # behind by a session that ended is a strip drawn over the next one.
        bar_gone() { ! sctl is-active --quiet zde-bar.service; }
        if ! waitfor 15 bar_gone; then
          echo "the session ended and the bar stayed:"
          sctl status zde-bar.service || true; exit 1
        fi
        layers_gone() { [ "$(nirimsg --json layers 2>/dev/null)" = "[]" ]; }
        if ! waitfor 15 layers_gone; then
          echo "the bar stopped and its surface is still on the screen:"
          nirimsg --json layers; exit 1
        fi

        # A session bus, so zded can be the notification server on it. Started
        # before zded because zded takes the name at startup and carries on
        # without one - which is the right behaviour and also means a bus
        # started afterwards would leave notifications going nowhere.
        eval "$(dbus-launch --sh-syntax)"
        export DBUS_SESSION_BUS_ADDRESS

        zded -journal /tmp/live.jsonl -desks /tmp/desks >/tmp/zded-live.log 2>&1 &
        livepid=$!
        for i in $(seq 30); do
          [ -S "$XDG_RUNTIME_DIR/zde/zded.sock" ] && break
          sleep 1
        done
        if [ ! -S "$XDG_RUNTIME_DIR/zde/zded.sock" ]; then
          echo "zded never listened:"; cat /tmp/zded-live.log; exit 1
        fi

        # The daemon can see the compositor, which is the thing a fake cannot
        # prove.
        zde status 2>&1 | tee /tmp/status.txt
        grep -qx 'compositor connected' /tmp/status.txt

        # ask, through the whole seam: the tier is a command layer 1 was
        # configured with, zded runs it with the question on its stdin, and the
        # answer streams back to a terminal - which is what the key does on a
        # session with no shell to draw a window.
        zde ask oneshot what is the capital of peru 2>&1 | tee /tmp/ask.txt
        grep -qx 'answered: what is the capital of peru' /tmp/ask.txt || {
          echo "the ask tier did not answer through zded:"; cat /tmp/ask.txt; exit 1
        }
        # And piped in rather than written as an argument, which is how a
        # question stays out of `ps` while it is being answered - the form the
        # tier called private is meant to be asked with.
        printf 'what is the capital of peru' | zde ask oneshot 2>&1 | tee /tmp/ask-stdin.txt
        grep -qx 'answered: what is the capital of peru' /tmp/ask-stdin.txt || {
          echo "a question piped in was not asked:"; cat /tmp/ask-stdin.txt; exit 1
        }
        # And a tier nobody configured fails closed, saying which option would
        # configure it. An ask that quietly did nothing is the failure this
        # whole component is arranged against.
        if zde ask local something private 2>&1 | tee /tmp/ask-local.txt; then
          echo "the local tier is not configured and the ask worked anyway:"
          cat /tmp/ask-local.txt; exit 1
        fi
        grep -q 'zde.ask.tiers.local' /tmp/ask-local.txt || {
          echo "the refusal does not say which option to set:"
          cat /tmp/ask-local.txt; exit 1
        }

        # A desk that exists only as a manifest is created and entered.
        zde desk switch vshop 2>&1 | tee /tmp/switch.txt
        grep -qx 'vshop.winit.code' /tmp/switch.txt

        # Layer 2, end to end and through the CLI: the desk's app comes back
        # as the address zinc takes, on the workspace zde names, with the state
        # directory zinc itself answered for. The last column is the one worth
        # the VM - it is `zcr where`, run by zde, and the point of asking is
        # that the layout stays zinc's to change.
        XDG_STATE_HOME=/tmp/state zde desk apps vshop 2>&1 | tee /tmp/apps.txt
        awk '$1=="absent-app@vshop" && $2=="vshop.winit.code" { found=1 }
             END { exit !found }' /tmp/apps.txt || {
          echo "zde desk apps did not name the app the manifest declares:"
          cat /tmp/apps.txt; exit 1
        }
        # And it asked zinc rather than answering for it. This desk names an app
        # zinc does not have, so what comes back is zinc's refusal, passed
        # through - which is the same round trip that fills the state column for
        # an app that exists (asserted directly against zcr further up, where it
        # costs nothing).
        grep -q 'no app "absent-app" defined' /tmp/apps.txt || {
          echo "zde desk apps did not report what zcr said about the app:"
          cat /tmp/apps.txt; exit 1
        }
        # And the desk you are on, which is the form a keybind would use: the
        # switch above put us on vshop.
        XDG_STATE_HOME=/tmp/state zde desk apps 2>&1 | tee /tmp/apps-here.txt
        grep -q 'absent-app@vshop' /tmp/apps-here.txt || {
          echo "zde desk apps, on vshop, did not list vshop's apps:"
          cat /tmp/apps-here.txt; exit 1
        }
        # And entering the desk tried to start what it declares: the whole chain,
        # from a switch through the manifest to zcr with the address the
        # manifest spells. A desk that starts nothing writes nothing here.
        #
        # The app is one zinc does not have, deliberately. A defined app would
        # send podman looking for an image this VM has no network to fetch, on
        # every switch in this script, and the first thing that broke was an
        # assertion three screens away about a bar reconnecting. A refusal
        # arrives immediately and proves the same wiring.
        waitfor 20 grep -q 'starting absent-app@vshop' /tmp/zded-live.log || {
          echo "switching to vshop did not try to start the app it declares:"
          cat /tmp/zded-live.log; exit 1
        }
        # And it said so where somebody would see it, which is the half a log
        # cannot do: what a switch could not start arrives like anything else
        # that arrives, so it waits on the queue in the mode this session is in
        # (docs/verify.md, section 5 - work here, and no manifest on this
        # machine declares an attn policy).
        #
        # The property, not the sentence: the arrival names the desk, names the
        # app, says it did not start, and comes from the desktop rather than
        # from an app - the sender column is otherwise a claim an app makes
        # about itself, and this is the one arrival zde sends itself.
        #
        # Column 3 is the half of that a claim cannot reach. The sender in
        # column 5 is a string, and a string can be drawn to look like "zde" in
        # a dozen alphabets (internal/attn, Notification.Self); the "*" is set
        # where the record is made and printed in a column of its own, and
        # nothing that arrives can hold a tab to reach it.
        launch_said() { zde queue >/tmp/q-launch.txt 2>&1 && grep -q 'did not start' /tmp/q-launch.txt; }
        waitfor 20 launch_said || {
          echo "the switch could not start the desk's app and told nobody:"
          cat /tmp/q-launch.txt /tmp/zded-live.log; exit 1
        }
        awk -F'\t' '$3=="*" && $5=="zde" && $6 ~ /vshop/ && $6 ~ /absent-app@vshop/ && $6 ~ /did not start/ { found=1 }
             END { exit !found }' /tmp/q-launch.txt || {
          echo "the launch failure reached the queue without saying which desk, which app, or who from:"
          cat /tmp/q-launch.txt; exit 1
        }
        # Finished here, because the queue section further down is about the
        # items it adds itself: it checks their order and that the queue empties
        # when they are done. Nothing else in this run adds one - the snapshot
        # below rewrites this manifest without its apps, deliberately
        # (internal/manifest, FromMap), so every later switch to vshop starts
        # nothing and has nothing to report.
        zde queue done "$(grep 'did not start' /tmp/q-launch.txt | cut -f1)" >/dev/null

        # And the desk's pin reached niri as a window rule. This is the file the
        # generated config includes and zded is the only thing that writes
        # (niri/config.kdl), so what is asserted is the whole seam: a manifest
        # pin, a rule in the right shape, and niri's own parser accepting the
        # config with it in. Written when zded started, before any of this.
        waitfor 20 grep -q 'open-on-workspace "vshop.winit.code"' \
          ~/.config/niri/dynamic.kdl || {
          echo "the desk pins an app and niri was never told:"
          cat ~/.config/niri/dynamic.kdl; exit 1
        }
        # Two backslashes before each dot, and -F because that is a claim about
        # bytes rather than about a pattern. The dot is escaped once for niri's
        # regex, and the backslash that escaped it is escaped again for the KDL
        # string carrying that regex; one backslash here is `\.`, which is not a
        # KDL escape, and the validate below would refuse the whole config.
        grep -qF 'match app-id="^zde\\.pinned\\.probe$"' ~/.config/niri/dynamic.kdl || {
          echo "the rule does not match the app id the manifest names:"
          cat ~/.config/niri/dynamic.kdl; exit 1
        }
        niri validate -c ~/.config/niri/config.kdl || {
          echo "zded wrote a dynamic.kdl niri will not load:"
          cat ~/.config/niri/dynamic.kdl; exit 1
        }

        # And the window actually lands there. A rule in a file niri accepted is
        # not a window in the right place: what this proves is the whole of it,
        # from the manifest's pin to where the thing opened. foot names its own
        # app id, which is how a window arrives claiming to be the app the desk
        # pinned without a container in the way.
        #
        # From another workspace on purpose. Opening it on the pinned one would
        # pass with no rule at all - that is where new windows go.
        nirimsg action focus-workspace vshop.winit.notes >/dev/null
        foot --app-id=zde.pinned.probe -e sleep 120 >/tmp/pinned.log 2>&1 &
        landed() {
          nirimsg --json windows |
            tr ',' '\n' | grep -qF '"app_id":"zde.pinned.probe"'
        }
        if ! waitfor 60 landed; then
          echo "the pinned window never opened:"; cat /tmp/pinned.log; exit 1
        fi
        pinned_ws=$(nirimsg --json windows | tr '{' '\n' |
          grep -F '"app_id":"zde.pinned.probe"' | tr ',' '\n' |
          grep '"workspace_id"' | head -1 | tr -dc '0-9')
        want_ws=$(nirimsg --json workspaces | tr '{' '\n' |
          grep '"name":"vshop.winit.code"' | tr ',' '\n' |
          grep '"id"' | head -1 | tr -dc '0-9')
        if [ -z "$pinned_ws" ] || [ "$pinned_ws" != "$want_ws" ]; then
          echo "the pinned window opened on workspace $pinned_ws, and the desk pins it to $want_ws:"
          nirimsg windows; nirimsg workspaces; exit 1
        fi
        # Closed, because everything below was written for a strip with a known
        # set of windows on it.
        nirimsg action focus-window --id "$(nirimsg --json windows | tr '{' '\n' |
          grep -F '"app_id":"zde.pinned.probe"' | tr ',' '\n' |
          grep '"id"' | head -1 | tr -dc '0-9')" >/dev/null
        nirimsg action close-window >/dev/null
        gone() { ! landed; }
        if ! waitfor 30 gone; then
          echo "the pinned window would not close:"; nirimsg windows; exit 1
        fi

        # zcr is on the session's PATH, said by the daemon that has that PATH.
        # A machine where this is no is one whose desks declare apps it cannot
        # run, and every symptom of it looks like something else.
        grep -qx 'zinc       yes' /tmp/status.txt || {
          echo "zded cannot see zcr, so nothing sandboxed can start:"
          cat /tmp/status.txt; exit 1
        }

        # No shell on this zded, and status says so. Between this and the line
        # above, the fact is asserted in both directions - a status field that is
        # always "yes" answers nothing.
        zde status 2>&1 | tee /tmp/status-noshell.txt
        grep -qx 'shell      no' /tmp/status-noshell.txt || {
          echo "no shell is listening and status does not say so:"
          cat /tmp/status-noshell.txt; exit 1
        }
        grep -q '^notify' /tmp/status-noshell.txt || {
          echo "status does not say whether notifications are ours:"
          cat /tmp/status-noshell.txt; exit 1
        }

        # And doctor, which is all of that on one screen. This is as healthy as
        # this script ever gets - a daemon answering, a compositor it can see,
        # the notification name taken, a manifest that parses - so nothing may
        # fail and the exit status has to be zero. The units are inactive here
        # (this zded was started by hand) and no shell is listening, which is
        # exactly why those two are warnings: neither is a session somebody
        # cannot work in, and a command that exits non-zero on every machine is
        # one nobody reads the output of. Nothing is warned about the desks yet
        # - the directory doctor reads is still empty at this point, and the
        # desk that names an app zinc has not got is written further down.
        if ! zde doctor >/tmp/doctor.txt 2>&1; then
          echo "doctor failed a check on a session that is working:"
          cat /tmp/doctor.txt; exit 1
        fi
        cat /tmp/doctor.txt
        for line in 'ok +zded' 'ok +compositor +connected' 'ok +notify' 'warn +shell' 'ok +locker'; do
          grep -qE "^$line" /tmp/doctor.txt || {
            echo "doctor has no '$line' line:"; cat /tmp/doctor.txt; exit 1
          }
        done
        # The locker is the line this command is most worth having: swaylock is
        # what layer 1 configures and /etc/pam.d/swaylock is what lets it
        # authenticate, so a machine where that file is not there locks and
        # never unlocks. The python half of this test asserts the file exists;
        # this asserts doctor is the thing that would notice if it stopped.
        grep -q '/etc/pam.d/swaylock' /tmp/doctor.txt || {
          echo "doctor does not say what the screen lock would authenticate against:"
          cat /tmp/doctor.txt; exit 1
        }
        # Which resolver judged the desks, on the one machine in CI that has
        # both halves: zcr is on this PATH, and zde.apps names a terminal, a
        # lock and a help.
        #
        # In the directory doctor itself reads, which is not the one zded was
        # given above: that check goes to the disk rather than through the
        # daemon (internal/doctor, probeDesks), so a desk in /tmp/desks would
        # leave the line below an all-clear about an empty directory - true, and
        # about nothing.
        mkdir -p ~/.config/zde/desks
        printf 'name: doctor-probe
    monitors:
      winit: { workspaces: [code] }
    apps:
      - { app: absent-app }
    '       > ~/.config/zde/desks/doctor-probe.yaml
        # Still a warning and not a failure: a desk naming an app nobody has
        # defined is the ordinary young machine (docs/delivery.md, layer 2), and
        # doctor's exit status has to keep meaning "something was promised and
        # is not here".
        if ! zde doctor >/tmp/doctor-desks.txt 2>&1; then
          echo "a desk naming an app zinc has not got made doctor fail:"
          cat /tmp/doctor-desks.txt; exit 1
        fi
        # `absent-app` is a zinc app name, and zde.apps has never heard of it
        # and never would - the two are different namespaces that the docs
        # happen to spell alike. So the verdict has to be zcr's own refusal
        # about that name. Judged against zde.apps this line would be right here
        # by accident, and wrong on every machine that keeps its apps in zinc,
        # which is the machine zde is for.
        grep -qE '^warn +desk apps +doctor-probe names absent-app.*asked of zcr' /tmp/doctor-desks.txt || {
          echo "doctor did not judge the desk's app with the resolver a launch uses:"
          cat /tmp/doctor-desks.txt; exit 1
        }
        grep -q 'no app "absent-app" defined' /tmp/doctor-desks.txt || {
          echo "doctor did not carry zcr's own answer about the name:"
          cat /tmp/doctor-desks.txt; exit 1
        }
        rm -f ~/.config/zde/desks/doctor-probe.yaml
        # logind, which is what the four power verbs are. Warn or ok and never
        # fail - a session with no logind still works, and the exit status above
        # has to keep meaning something - but the line has to be there: "the
        # power menu refuses everything on this machine" is a sentence about
        # polkit or a bus, and it is invisible until somebody presses the key.
        grep -qE '^(ok|warn) +logind ' /tmp/doctor.txt || {
          echo "doctor says nothing about logind, so nothing says whether this session may power off:"
          cat /tmp/doctor.txt; exit 1
        }
        # The idle hold, on the same terms and for the same reason: this VM may
        # or may not have a logind that answers, so the level is not the
        # assertion and the line being there is.
        grep -qE '^(ok|warn) +idle ' /tmp/doctor.txt || {
          echo "doctor says nothing about what is holding this session awake:"
          cat /tmp/doctor.txt; exit 1
        }
        # And whichever way it answered, it has to say what it could not see.
        # This is the one line in the report a person could read as a promise
        # that their screen will lock, and it is not one: the Wayland half of
        # the mechanism is invisible to every interface zde has. A clean line
        # with the caveat dropped is the failure worth catching from here,
        # because nothing about it looks wrong.
        grep -E '^(ok|warn) +idle ' /tmp/doctor.txt | grep -q 'zwp_idle_inhibit_manager_v1' || {
          echo "doctor's idle line does not say which half of the mechanism it could see:"
          grep -E '^(ok|warn) +idle ' /tmp/doctor.txt; exit 1
        }
        # And the whole of it, not the front. The name above is 22 characters
        # into a caveat of 340, and this column is filtered because most of what
        # lands in it was written by another program - so a filter aimed at
        # foreign text and pointed at this sentence by mistake leaves the grep
        # above passing on a line whose qualification had been cut off an
        # all-clear. It has been exactly that: cut at a queue row's 300
        # characters the line stopped at "and e" and lost both documents. The
        # last words of it are what says the caveat arrived.
        grep -E '^(ok|warn) +idle ' /tmp/doctor.txt | grep -q 'docs/verify.md, section 11)' || {
          echo "doctor's idle caveat is cut short, so the line reads as more than it is:"
          grep -E '^(ok|warn) +idle ' /tmp/doctor.txt; exit 1
        }

        # The state snapshot by hand, with two desks declared: an ordinary one
        # and one that says private. This is the assertion the whole file is
        # written around. A private desk is history only (docs/vision.md,
        # section 3), and this file is meant to be carried off the machine, so
        # neither the desk's name nor the app on it may be in it - naming the
        # app and hiding the desk would be the same disclosure with a step in
        # front of it.
        printf 'name: report-open
    monitors:
      winit: { workspaces: [code] }
    apps:
      - { app: absent-app }
    '       > ~/.config/zde/desks/report-open.yaml
        printf 'name: report-secret
    private: true
    monitors:
      winit: { workspaces: [code] }
    apps:
      - { app: absent-secret-app }
    '       > ~/.config/zde/desks/report-secret.yaml
        # And one that will not parse, in the daemon's own desk directory,
        # because that list comes back over the socket rather than off this
        # process's disk. Nothing can read a private flag out of a file that did
        # not load - the flag is inside the file - so the report may name none
        # of them, and this one's name is a desk's name.
        #
        # desk.apps rather than a restart: it re-reads the directory and
        # remembers what would not parse (internal/zded, rememberProblems),
        # which is the cheapest way to put a real entry in that list.
        printf 'name: report-broken\n  monitors: [oh dear\n' > /tmp/desks/report-broken.yaml
        XDG_STATE_HOME=/tmp/state zde desk apps vshop >/dev/null 2>&1 || true
        # Eight older files this package would recognise as its own, one it
        # would not, and one named for a century from now. The bound has to
        # hold; it has to hold without deleting something somebody copied in
        # here while debugging, which is the difference between a bound and a
        # program that deletes things; and it must not be steerable by a name,
        # which is what the last of these is. Any process running as this
        # account can write that file, and while rotation kept whatever sorted
        # highest, one of them was enough to evict a real snapshot on every
        # write - starting with the one just written.
        for i in 1 2 3 4 5 6 7 8; do
          : > "/var/log/zde/zde/2020010''${i}T000000Z-deadbeef.txt"
        done
        : > /var/log/zde/zde/notes.txt
        : > /var/log/zde/zde/29991231T235959Z-ffffffff.txt
        zde report > /tmp/report-path.txt 2>&1 || {
          echo "zde report failed:"; cat /tmp/report-path.txt; exit 1
        }
        snap2=$(sed -n 's/^wrote //p' /tmp/report-path.txt)
        [ -n "$snap2" ] && [ -f "$snap2" ] || {
          echo "zde report wrote no file, and said:"; cat /tmp/report-path.txt; exit 1
        }
        for secret in report-secret absent-secret-app report-broken; do
          if grep -qF "$secret" "$snap2"; then
            echo "$secret names a desk this file may not name and is in a file meant to leave this machine:"
            sed -n '/^\[doctor\]/,$p' "$snap2"; exit 1
          fi
        done
        # Counted, though, and still a failure: those desks are not declared and
        # somebody has to be told, without being told which files.
        grep -qE '^  fail +manifests +[0-9]+ manifest\(s\)' "$snap2" || {
          echo "the manifests that would not parse are neither named nor counted:"
          sed -n '/^\[doctor\]/,$p' "$snap2"; exit 1
        }
        # And `zde doctor` on this person's own screen names it, because that
        # screen is theirs and the path is the file they have to go and open.
        # Not through a pipe: a manifest that will not parse is a failed check,
        # so doctor exits non-zero here and pipefail would read that as the
        # grep having found nothing.
        zde doctor >/tmp/doctor-broken.txt 2>&1 || true
        grep -qF 'report-broken.yaml' /tmp/doctor-broken.txt || {
          echo "doctor in a terminal will not say which manifest to go and fix:"
          cat /tmp/doctor-broken.txt; exit 1
        }
        rm -f /tmp/desks/report-broken.yaml
        XDG_STATE_HOME=/tmp/state zde desk apps vshop >/dev/null 2>&1 || true
        # And the desk that declared nothing is still reported, or the redaction
        # would be a way to silence the check by declaring everything private.
        grep -qF 'report-open names absent-app' "$snap2" || {
          echo "the desk that did not declare private was redacted too:"
          sed -n '/^\[doctor\]/,$p' "$snap2"; exit 1
        }
        # A file that silently dropped lines is a file whose all-clear cannot be
        # trusted, so the count survives even though the names do not.
        grep -qF '1 app(s) on desks that declare private' "$snap2" || {
          echo "nothing in the file says something was left out of it:"
          sed -n '/^\[doctor\]/,$p' "$snap2"; exit 1
        }
        # The bound took one out for the one that went in - the oldest - and
        # exactly one, because a snapshot writer that deletes eight things
        # because of one write is a tool and a name is all it takes to aim it.
        # Asserted by name rather than by a count, since the session start
        # already left one of these here.
        test -f /var/log/zde/zde/20200101T000000Z-deadbeef.txt && {
          echo "the oldest snapshot is still here, so the bound did nothing:"
          ls -la /var/log/zde/zde; exit 1
        }
        test -f /var/log/zde/zde/20200102T000000Z-deadbeef.txt || {
          echo "one write took more than the one file it replaced:"
          ls -la /var/log/zde/zde; exit 1
        }
        # And the three rotation must not touch: a name it could not have
        # written, something copied in here by hand, and the file zde has just
        # told somebody it wrote.
        test -f /var/log/zde/zde/29991231T235959Z-ffffffff.txt || {
          echo "rotation deleted a file dated after the write, which it cannot have written"; exit 1
        }
        test -f /var/log/zde/zde/notes.txt || {
          echo "rotation deleted a file zde never wrote"; exit 1
        }
        test -f "$snap2" || {
          echo "zde printed the path of a file rotation deleted as it landed: $snap2"; exit 1
        }
        rm -f /var/log/zde/zde/29991231T235959Z-ffffffff.txt

        # The other name this file may not carry, and the only one that needs a
        # real logind to show. `man systemd-inhibit`: --who= "defaults to the
        # command line string" of whatever took the inhibitor - so an ordinary
        # backup puts a path under somebody's home directory, the host it is
        # copying to and the name of the file into logind's table, and all of it
        # used to go into the snapshot verbatim under a header promising "No
        # command line but the kernel's own".
        #
        # Held by wrapping the command that reads it, so the inhibitor is taken
        # and released by one process and there is nothing left running here.
        #
        # A second after the report above, because a snapshot is named for the
        # boot and the time to the second, and O_EXCL refuses the second one of
        # any second rather than writing over the first (internal/doctor,
        # TestASnapshotNeverWritesOverOne). Nothing between the two calls takes
        # a second on its own, so without this they land in the same one
        # whenever the boundary happens to fall after both.
        sleep 1
        systemd-inhibit --what=idle --who=/home/zde/secret-backup.sh \
          --why='copying /home/zde/private-notes' \
          zde report > /tmp/report-held.txt 2>&1 || {
          echo "zde report failed with an idle inhibitor held:"; cat /tmp/report-held.txt; exit 1
        }
        snap3=$(sed -n 's/^wrote //p' /tmp/report-held.txt)
        [ -n "$snap3" ] && [ -f "$snap3" ] || {
          echo "zde report wrote no file, and said:"; cat /tmp/report-held.txt; exit 1
        }
        for secret in secret-backup.sh private-notes /home/zde/secret; do
          if grep -qF "$secret" "$snap3"; then
            echo "$secret came off another program's command line into a file meant to leave this machine:"
            sed -n '/^\[doctor\]/,$p' "$snap3"; exit 1
          fi
        done
        # Counted, though, and still a warning: a redaction that turned into an
        # all-clear would be the one failure this check must not have, since
        # refusing to give an all-clear it cannot support is its whole purpose.
        grep -qE '^  warn +idle +[0-9]+ thing\(s\) logind names are holding this session awake' "$snap3" || {
          echo "the snapshot does not say the session is being held awake at all:"
          sed -n '/^\[doctor\]/,$p' "$snap3"; exit 1
        }
        # And `zde doctor` on this person's own screen names it, because that
        # screen is theirs and that program is the one they have to go and stop.
        # Not through a pipe, for the reason the manifest check above is not.
        systemd-inhibit --what=idle --who=/home/zde/secret-backup.sh \
          --why='copying /home/zde/private-notes' \
          zde doctor >/tmp/doctor-held.txt 2>&1 || true
        grep -qF 'secret-backup.sh' /tmp/doctor-held.txt || {
          echo "doctor in a terminal will not say what is holding the screen awake:"
          grep -E '^(ok|warn) +idle ' /tmp/doctor-held.txt || cat /tmp/doctor-held.txt
          exit 1
        }

        rm -f ~/.config/zde/desks/report-open.yaml ~/.config/zde/desks/report-secret.yaml

        # Mod+t, which is `zde app launch terminal`. The one thing a desktop has
        # to be able to do: until this existed, a session could be entered and
        # nothing could be started from it, and the live image carried a bind of
        # its own just so somebody could type.
        #
        # Asserted on a window appearing, not on the config file existing: the
        # name has to resolve, the binary has to be there, and exec has to
        # replace this process with it.
        before_terminal=$(nirimsg --json windows | grep -c '"id"' || true)
        # Redirected, like every other client this script starts. A background
        # process that inherits stdout holds the pipe open, and the runner waits
        # for EOF on it - so leaving this bare hung the whole test until the
        # global timeout, twenty five minutes later, with the terminal it
        # launched sitting there working perfectly.
        zde app launch terminal >/tmp/launch.log 2>&1 &
        more_windows() {
          [ "$(nirimsg --json windows | grep -c '"id"' || true)" -gt "$before_terminal" ]
        }
        if ! waitfor 60 more_windows; then
          echo "zde app launch terminal started nothing:"
          zde app list; nirimsg windows; exit 1
        fi

        # And close it again. Everything below this line was written for a strip
        # with a known set of windows on it - the move-window checks in
        # particular expect the workspace they land on to be empty - and a
        # terminal left running here is a window on whichever workspace was
        # focused when it started. It failed exactly that way once.
        nirimsg action close-window >/dev/null
        closed() {
          [ "$(nirimsg --json windows | grep -c '"id"' || true)" -le "$before_terminal" ]
        }
        if ! waitfor 30 closed; then
          echo "the launched terminal would not close, and later checks need the strip as it was:"
          nirimsg windows; exit 1
        fi

        # And the lock is one of the names, because Mod+Ctrl+semicolon resolves
        # through the same table and a machine that cannot lock its screen is
        # not one to take out of the house.
        zde app list 2>&1 | tee /tmp/applist.txt
        grep -q '^lock' /tmp/applist.txt || {
          echo "no lock configured, so Mod+Ctrl+semicolon does nothing:"
          cat /tmp/applist.txt; exit 1
        }

        # And a name nobody configured says what is configured, rather than
        # failing in a way that leaves somebody wondering whether they typed it
        # wrong or their machine has none.
        if zde app launch nosuchthing 2>&1 | tee /tmp/nosuch.txt; then
          echo "launched an app that is not configured"; exit 1
        fi
        grep -q 'terminal' /tmp/nosuch.txt || {
          echo "the refusal does not say what this machine can start:"; cat /tmp/nosuch.txt; exit 1
        }

        # Bluetooth on a machine with no radio, which is this VM and every
        # desktop that has not asked for one (zde.bluetooth.enable is off here,
        # and so is zde.laptop). That is the case worth pinning: the verb has to
        # say so and exit zero, rather than erroring, or hanging on a bus
        # nobody answers on. It goes through zded to bluetoothd and back, so a
        # daemon that blocked on the system bus would be caught here.
        zde system bluetooth 2>&1 | tee /tmp/bt.txt
        grep -q '^adapter    none' /tmp/bt.txt || {
          echo "no bluetooth adapter and the verb does not say so legibly:"
          cat /tmp/bt.txt; exit 1
        }

        # And asking it to do something is a refusal that names the reason. The
        # other half of the same fact: a verb that reports success for a pairing
        # that cannot have happened is worse than one that errors.
        if zde system bluetooth pair 44:5C:E9:1A:2B:3C 2>&1 | tee /tmp/bt-pair.txt; then
          echo "pairing succeeded on a machine with no radio:"; cat /tmp/bt-pair.txt; exit 1
        fi
        grep -qi 'bluetooth' /tmp/bt-pair.txt || {
          echo "the refusal does not say what is missing:"; cat /tmp/bt-pair.txt; exit 1
        }

        # The switcher with nothing listening, which is this zded: the session
        # target was stopped above, so there is no shell here. The key still has
        # to do something, so it prints the desks and marks the one you are on -
        # which is what it did before there was a picker to open.
        zde desk switcher 2>&1 | tee /tmp/switcher-noshell.txt
        grep -q 'vshop' /tmp/switcher-noshell.txt || {
          echo "no shell to show the picker and the switcher printed nothing:"
          cat /tmp/switcher-noshell.txt; exit 1
        }
        grep -q '(here)' /tmp/switcher-noshell.txt || {
          echo "the printed list does not say which desk you are on:"
          cat /tmp/switcher-noshell.txt; exit 1
        }

        # The palette makes the same bargain, and its list is the answer to
        # "what does this machine do" that `zde keys` gives for keys - with the
        # half the cheatsheet cannot give, which is which of them do anything
        # yet. Both directions, because a column that always says the same
        # thing says nothing.
        zde palette 2>&1 | tee /tmp/palette-noshell.txt
        grep -q 'desk.switcher.*Mod+Tab' /tmp/palette-noshell.txt || {
          echo "the palette does not say which key runs an action:"
          cat /tmp/palette-noshell.txt; exit 1
        }
        grep -q 'nothing is written behind it yet' /tmp/palette-noshell.txt || {
          echo "every action on this machine reads as working, which is not true:"
          cat /tmp/palette-noshell.txt; exit 1
        }

        # And it still works with a broken manifest sitting beside it, which is
        # the ordinary state of a directory somebody edits by hand. One bad file
        # used to fail the whole read, and the caller that mattered dropped the
        # error - so every desk on the machine quietly stopped being declared.
        # Left here for the rest of the run, deliberately: everything below this
        # line is therefore also a test that a broken manifest does not spread.
        printf 'name: haven\nmonitorz: nope\n' > /tmp/desks/broken.yaml
        zde desk switch vshop 2>&1 | tee /tmp/switch2.txt
        grep -qx 'vshop.winit.code' /tmp/switch2.txt
        # And somebody is told, by the one command that exists to say what is
        # wrong with a session.
        zde status 2>&1 | tee /tmp/status-bad.txt
        grep -q 'broken.yaml' /tmp/status-bad.txt || {
          echo "a manifest could not be read and zde status did not mention it:"
          cat /tmp/status-bad.txt; exit 1
        }
        # And doctor says so in both ways it has: the line that names the file,
        # and the exit status - which is the half that makes this worth piping
        # into a bug report. The same command exited zero a few lines up, so
        # between the two the status is asserted in both directions.
        if zde doctor >/tmp/doctor-bad.txt 2>&1; then
          echo "a manifest could not be read and doctor still passed:"
          cat /tmp/doctor-bad.txt; exit 1
        fi
        grep -qE '^fail +manifests +.*broken.yaml' /tmp/doctor-bad.txt || {
          echo "doctor failed and does not name the manifest it failed over:"
          cat /tmp/doctor-bad.txt; exit 1
        }

        # niri's own client agrees that both declared workspaces are there.
        nirimsg workspaces 2>&1 | tee /tmp/ws.txt
        grep -q 'vshop.winit.code' /tmp/ws.txt
        grep -q 'vshop.winit.notes' /tmp/ws.txt

        # The journal recorded where the desk was left.
        grep -q '"slot":"code"' /tmp/live.jsonl

        # Reconcile is quiet on a world that is already true, and niri's own
        # trailing empty workspace is left alone rather than adopted.
        zde desk reconcile 2>&1 | tee /tmp/rec.txt
        grep -qx 'nothing to reconcile' /tmp/rec.txt

        # Adoption, against a real compositor and a real window. Everything up
        # to here could be done with no windows at all; this is the path that
        # names a workspace after what is in it.
        #
        # foot is a Wayland client that starts without a GPU, which not many
        # do. It has to talk to niri rather than to the cage hosting it, which
        # is what WAYLAND_DISPLAY above is for - exported where the bar needed
        # it first, and read out of niri's socket name either way.
        # Past the end of the strip, onto the empty workspace niri keeps there.
        # That is where a new workspace comes from in real use, and it is
        # unnamed until something claims it.
        # niri clamps at the last workspace instead of wrapping, so any count
        # that reaches the end lands there. Two is one per declared workspace,
        # which is what keeps this honest if [code, notes] ever grows: fewer
        # downs than workspaces and foot opens on a named one instead.
        nirimsg action focus-workspace-down >/dev/null
        nirimsg action focus-workspace-down >/dev/null
        foot -e sleep 600 >/tmp/foot.log 2>&1 &
        # Asked for into a file and grepped there rather than piped: under
        # pipefail a producer that exits non-zero after grep already matched -
        # SIGPIPE, or the timeout firing late - would read as no match.
        foot_window() { nirimsg windows >/tmp/win.txt 2>&1 && grep -qi foot /tmp/win.txt; }
        if ! waitfor 60 foot_window; then
          echo "no window ever appeared:"; cat /tmp/foot.log /tmp/win.txt; exit 1
        fi

        # Deliberately no reconcile here. What keeps names true while a session
        # runs is the watcher, and asserting the command instead would pass even
        # with the watcher broken - so wait for the name to appear on its own.
        # zded's own log is the one worth printing when it does not: a watcher
        # that never saw an event and one that errored every reconcile leave
        # exactly the same workspace list behind.
        adopted() { nirimsg workspaces >/tmp/ws2.txt 2>&1 && grep -q 'vshop.winit.foot' /tmp/ws2.txt; }
        if ! waitfor 30 adopted; then
          echo "the watcher never named the workspace after what is in it:"
          cat /tmp/ws2.txt /tmp/zded-live.log; exit 1
        fi

        # Only then the manual path, which must find nothing left: adoption
        # converges on a real compositor, not only in a unit test.
        zde desk reconcile 2>&1 | tee /tmp/rec3.txt
        grep -qx 'nothing to reconcile' /tmp/rec3.txt

        # And the desk can be written back out as a manifest.
        if zde desk snapshot haven 2>&1 | tee /tmp/snapfail.txt; then
          echo "snapshotted a desk that does not exist"; exit 1
        fi
        grep -q 'no workspaces' /tmp/snapfail.txt
        rm -f /tmp/desks/vshop.yaml
        zde desk snapshot 2>&1 | tee /tmp/snap.txt
        grep -q 'vshop.yaml' /tmp/snap.txt
        # The adopted workspace is the one worth checking: it is what a
        # snapshot newly captures that a hand-written manifest would not have
        # had.
        grep -q 'foot' /tmp/desks/vshop.yaml

        # Rotation, which needs a second desk to be about anything. Declared
        # here rather than at the top so that everything above it is about one
        # desk, the way it was written.
        printf 'name: haven
    monitors:
      winit: { workspaces: [db] }
    '       > /tmp/desks/haven.yaml
        zde desk switch haven 2>&1 | tee /tmp/haven.txt
        grep -qx 'haven.winit.db' /tmp/haven.txt

        # Alphabetical, and a loop: from haven forward is vshop, and from
        # vshop forward again is haven rather than the end of a list.
        zde desk next 2>&1 | tee /tmp/next.txt
        grep -q 'vshop.winit' /tmp/next.txt
        zde desk next 2>&1 | tee /tmp/next2.txt
        grep -qx 'haven.winit.db' /tmp/next2.txt
        # And prev is next backwards, wrapping the other way.
        zde desk prev 2>&1 | tee /tmp/prev.txt
        grep -q 'vshop.winit' /tmp/prev.txt

        # nav, the axis the model reads as one: the window below first, the
        # desk after it.
        #
        # On the workspace that has a window in it, deliberately. A desk switch
        # comes back to the workspace you left, which here is an empty one, and
        # an empty workspace rotates whatever nav does with a stack - so this
        # would pass just as well with the window half of nav missing.
        nirimsg action focus-workspace vshop.winit.foot >/dev/null

        # One window is the end of its own stack, so the desk turns. This is
        # also where the assumption under nav gets tested: niri's
        # FocusWindowDown does nothing at the end of a column rather than
        # falling through to the next workspace. If it ever fell through, focus
        # would have changed, nav would report no desk change, and the grep
        # below would fail here rather than on somebody's machine.
        zde nav down 2>&1 | tee /tmp/nav-down.txt
        grep -qx 'haven.winit.db' /tmp/nav-down.txt

        # And with two windows stacked in one column the same key stays put,
        # moving focus inside the workspace. niri opens the second in its own
        # column, so it has to be consumed into the first.
        nirimsg action focus-workspace vshop.winit.foot >/dev/null
        first=$(focused_window)
        foot -e sleep 600 >/tmp/foot2.log 2>&1 &
        second_window() { [ -n "$(focused_window)" ] && [ "$(focused_window)" != "$first" ]; }
        if ! waitfor 60 second_window; then
          echo "the second window never took focus:"; cat /tmp/foot2.log; exit 1
        fi
        nirimsg action consume-or-expel-window-left >/dev/null
        # From the top of the stack, so down has somewhere to go regardless of
        # which end consuming left focus on.
        nirimsg action focus-window-top >/dev/null
        top=$(focused_window)
        zde nav down 2>&1 | tee /tmp/nav-stack.txt
        [ ! -s /tmp/nav-stack.txt ] # nothing about the desk changed
        [ -n "$top" ] && [ "$(focused_window)" != "$top" ] || {
          echo "nav down stayed on window $top instead of the one below it"
          exit 1
        }

        # Shift on the same axis: the window comes along, and so do you.
        #
        # The window half is niri's own answer - the focused window id after
        # the move is read from the compositor, and a carry that moved nothing
        # would leave haven's empty workspace focused and no window at all. The
        # desk half is zded's answer rather than niri's, which is the weaker of
        # the two; and with one screen and two desks this cannot see a carry
        # that went the wrong way or one that let focus follow it. Those are
        # pinned in the unit tests, where three desks and a focus-following
        # compositor are cheap.
        carried=$(focused_window)
        zde desk move-window next 2>&1 | tee /tmp/move.txt
        grep -qx 'haven.winit.db' /tmp/move.txt
        [ "$(focused_window)" = "$carried" ] || {
          echo "window $carried did not come along: focus is on $(focused_window)"
          nirimsg windows; exit 1
        }

        # The regulars: a band reachable from anywhere by its own key, and
        # never scrolled into by anything else. Made last, so everything above
        # it ran on a machine that had none.
        #
        # Made with the verb, which is the whole point of the verb: a manifest
        # cannot declare this band and adoption names into the desk you are on,
        # so until there was one, the only way to have regulars was to name a
        # workspace through niri by hand - which this test used to do, and which
        # is not something to ask of anybody.
        #
        # So: a workspace worth keeping, made the way a person makes one. Past
        # the end of the strip, open something, let adoption name it. vshop
        # already has a foot workspace and its snapshot declares it, so this one
        # is adopted as foot-2 - undeclared, which is what makes it movable.
        #
        # The desk is said out loud rather than inherited from the block above.
        # Adoption names into the desk you are on, and the tests before this one
        # left haven active, so a workspace opened here without switching first
        # is adopted into haven - which is what happened when this was written,
        # and cost a CI round trip to a name that never appeared.
        zde desk switch vshop >/dev/null
        for i in $(seq 10); do nirimsg action focus-workspace-down >/dev/null; done
        foot -e sleep 600 >/tmp/foot3.log 2>&1 &
        promoted() { nirimsg workspaces >/tmp/ws3.txt 2>&1 && grep -q 'vshop.winit.foot-2' /tmp/ws3.txt; }
        if ! waitfor 60 promoted; then
          echo "the workspace to promote was never adopted:"
          cat /tmp/foot3.log /tmp/ws3.txt /tmp/zded-live.log; exit 1
        fi

        # First the refusal, on a workspace the manifest declares: it would be
        # recreated by the next switch, so moving it would look undone by
        # something invisible. foot is in vshop's snapshot from earlier.
        nirimsg action focus-workspace vshop.winit.foot >/dev/null
        if zde desk move-workspace-to regulars 2>&1 | tee /tmp/mws-no.txt; then
          echo "moved a workspace vshop declares, which comes straight back"; exit 1
        fi
        grep -q 'vshop' /tmp/mws-no.txt
        grep -q 'foot' /tmp/mws-no.txt

        # Then the move that works, and the band exists because of it.
        nirimsg action focus-workspace vshop.winit.foot-2 >/dev/null
        zde desk move-workspace-to regulars 2>&1 | tee /tmp/mws.txt
        grep -qx 'regulars.winit.foot-2' /tmp/mws.txt
        nirimsg workspaces 2>&1 | tee /tmp/ws4.txt
        grep -q 'regulars.winit.foot-2' /tmp/ws4.txt
        # A rename and not a move: the window that was in it is still in it.
        [ -n "$(focused_window)" ] || {
          echo "the workspace changed bands and lost what was in it:"; nirimsg windows; exit 1
        }

        # Making the band left focus sitting in it - the workspace under you
        # changed hands, you did not go anywhere - so the desk to come back to
        # is chosen here rather than inherited: what is being checked is
        # reaching the regulars from somewhere and landing back on that
        # somewhere, which needs the somewhere to be known.
        zde desk switch haven >/dev/null
        zde desk regulars 2>&1 | tee /tmp/reg.txt
        grep -qx 'regulars.winit.foot-2' /tmp/reg.txt

        # Reaching for them costs nothing, because coming back is one key.
        zde desk last 2>&1 | tee /tmp/reg-back.txt
        grep -q 'haven.winit' /tmp/reg-back.txt

        # And the rotation steps over them, in both directions and in both
        # senses. From the band, which is in no rotation, forward is the first
        # desk and back is the last; were the regulars in it they would be an
        # index in the middle, and both of these would answer the other desk.
        zde desk regulars >/dev/null
        zde desk next 2>&1 | tee /tmp/reg-next.txt
        grep -q 'haven.winit' /tmp/reg-next.txt
        zde desk regulars >/dev/null
        zde desk prev 2>&1 | tee /tmp/reg-prev.txt
        grep -q 'vshop.winit' /tmp/reg-prev.txt

        # The step that would actually land on them: alphabetically the
        # regulars sit between haven and vshop, so going back from vshop is
        # where a rotation that stopped excluding them would arrive.
        zde desk prev 2>&1 | tee /tmp/reg-prev2.txt
        grep -q 'haven.winit' /tmp/reg-prev2.txt

        # Into the regulars and back out, which is what a desk named rather
        # than counted to is for. Out was already possible - from inside the
        # band the rotation falls back to an end of it - but only to whichever
        # desk that fallback picked. Getting a window in was impossible.
        zde desk switch vshop >/dev/null
        nirimsg action focus-workspace vshop.winit.foot >/dev/null
        moved=$(focused_window)
        [ -n "$moved" ] || {
          echo "expected a window on vshop.winit.foot to carry:"; nirimsg windows; exit 1
        }
        # Where the window ended up, counted on the band's own workspace rather
        # than read off what has focus. This used to check that focus followed
        # the carried window, which held only because the band's workspace was
        # empty: the band is made by promoting a workspace now, so it has work
        # in it, and landing on the workspace lands next to what was already
        # there. Which of them niri focuses is niri's business; whether the
        # window arrived is ours.
        # Which workspace the carried window is on, asked of niri about that
        # window by id. Not counted, and not read off what has focus: counting
        # let the wrong window pass this test once, because the trip back
        # carried whatever had focus rather than the window that went in.
        wsid() {
          nirimsg --json workspaces 2>/dev/null | tr '{' '\n' | grep "\"name\":\"$1\"" |
            grep -o '"id":[0-9]*' | head -1 | cut -d: -f2
        }
        winws() {
          nirimsg --json windows 2>/dev/null | tr '{' '\n' | grep "\"id\":$1," |
            grep -o '"workspace_id":[0-9]*' | head -1 | cut -d: -f2
        }
        # focused_window answers '"id":2'; the number is what niri wants back.
        movedid=''${moved##*:}
        band=$(wsid regulars.winit.foot-2)
        [ -n "$band" ] || { echo "cannot find the band's workspace id"; nirimsg --json workspaces; exit 1; }

        zde desk move-window-to regulars 2>&1 | tee /tmp/mwt-in.txt
        grep -qx 'regulars.winit.foot-2' /tmp/mwt-in.txt
        [ "$(winws "$movedid")" = "$band" ] || {
          echo "window $movedid is on workspace $(winws "$movedid"), not the band's $band"
          nirimsg windows; exit 1
        }

        # Focus it before sending it back, because the verb carries whatever is
        # focused - and the band already had a window, so landing there did not
        # leave focus on the one that arrived. That ambiguity is a question for
        # the by-hand list; here it just has to be pinned down.
        nirimsg action focus-window --id "$movedid" >/dev/null
        # Whole line, not a substring: tee hides the exit status, so this grep
        # is all that stands between a refusal and a green run - and a refusal
        # that names the desk it could not reach contains "vshop.winit" too.
        # vshop is entered on code, which is where the journal left it: the
        # raw focus-workspace above went behind zded's back and did not move
        # what it remembers.
        zde desk move-window-to vshop 2>&1 | tee /tmp/mwt-out.txt
        grep -qx 'vshop.winit.code' /tmp/mwt-out.txt
        # Out again, and it is the same window that left: it is on the
        # workspace vshop was entered on, and it is what you are looking at -
        # that workspace is empty, so here the carried window is the only thing
        # focus can be.
        [ "$(winws "$movedid")" = "$(wsid vshop.winit.code)" ] || {
          echo "window $movedid did not come back out: it is on workspace $(winws "$movedid")"
          nirimsg windows; exit 1
        }
        [ "$(focused_window)" = "$moved" ] || {
          echo "window $moved came out of the band and focus is on $(focused_window)"; exit 1
        }

        # Band-clamped scrolling against a real strip. zde names the workspace
        # it wants rather than asking niri to step, so escaping the desk is
        # structurally impossible here; what this can catch is the band being
        # the wrong set - too wide, in the wrong order, or on the wrong screen
        # - which is what the walk and the focus check below are for.
        focused_workspace() {
          nirimsg --json workspaces 2>/dev/null | tr '{' '\n' \
            | grep '"is_focused":[[:space:]]*true' \
            | grep -o '"name":[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/'
        }
        zde desk switch vshop >/dev/null
        : > /tmp/ws-walk.txt
        for i in $(seq 8); do zde workspace next >>/tmp/ws-walk.txt 2>&1; done
        [ -s /tmp/ws-walk.txt ] || { echo "the scroll never moved at all"; exit 1; }
        if grep -qv '^vshop\.winit\.' /tmp/ws-walk.txt; then
          echo "a scroll left the desk:"; cat /tmp/ws-walk.txt; exit 1
        fi
        # And the end of the band is silent rather than wrapping round.
        zde workspace next 2>&1 | tee /tmp/ws-end.txt
        [ ! -s /tmp/ws-end.txt ] || { echo "the band did not end"; exit 1; }

        # niri's own answer, not zde's: every check above this line believes
        # what the daemon said about where it went.
        landed=$(tail -1 /tmp/ws-walk.txt)
        here=$(focused_workspace)
        [ -n "$here" ] || { echo "could not read the focused workspace from niri"; exit 1; }
        [ "$here" = "$landed" ] || {
          echo "zde said it focused $landed, niri has $here focused"; exit 1
        }

        # The queue, which is the half of 0.1 that is not desks. What makes it
        # a queue rather than a list is that each item knows the desk it was
        # taken on, so jumping has somewhere to go: added from vshop, jumped to
        # from haven, and the jump has to cross back.
        zde desk switch vshop >/dev/null
        zde queue add reply to ilya about the invoice 2>&1 | tee /tmp/q-add.txt
        grep -q 'reply to ilya about the invoice' /tmp/q-add.txt || {
          echo "what came back from queue add is not what was typed:"
          cat /tmp/q-add.txt; exit 1
        }
        id=$(cut -f1 /tmp/q-add.txt)
        [ -n "$id" ] || { echo "no id came back"; cat /tmp/q-add.txt; exit 1; }

        # Every assertion in this block says what it wanted and what it got. A
        # bare `grep -q` under `set -e` ends the script with no message at all,
        # and the run above it has already scrolled past - which is exactly how
        # a column added to this listing cost a CI round trip to find.
        #
        # The columns are id, urgency, whether the desktop wrote it, desk,
        # sender, text (cmd/zde, queueList). The third is a dot here and the
        # fifth is a dash: a reminder somebody typed is neither an app nor the
        # desktop talking, and it is the dash that says a person wrote it.
        zde queue 2>&1 | tee /tmp/q-list.txt
        grep -q "^$id	.	.	vshop	-	reply to ilya" /tmp/q-list.txt || {
          echo "the reminder is not id, urgency, badge, desk, sender, text on the desk it was added on:"
          cat /tmp/q-list.txt; exit 1
        }

        # A second one, newer and on another desk. With one item a queue that
        # went to the newest and one that went to the oldest are the same
        # queue, and so are ids counted from the journal and from the length of
        # the list - so the check that follows would prove neither.
        zde desk switch haven >/dev/null
        zde queue add look at the build log 2>&1 | tee /tmp/q-add2.txt
        id2=$(cut -f1 /tmp/q-add2.txt)
        [ "$id2" != "$id" ] || { echo "both reminders got id $id"; exit 1; }
        zde queue > /tmp/q-list2.txt 2>&1
        grep -q "^$id	.	.	vshop	-	" /tmp/q-list2.txt || {
          echo "the first reminder is not still waiting on vshop:"
          cat /tmp/q-list2.txt; exit 1
        }
        grep -q "^$id2	.	.	haven	-	look at the build log" /tmp/q-list2.txt || {
          echo "the second reminder did not land on haven with what was typed:"
          cat /tmp/q-list2.txt; exit 1
        }
        # Oldest first, which is the order the jump below follows.
        [ "$(head -1 /tmp/q-list2.txt | cut -f1)" = "$id" ] || {
          echo "the list is not oldest first:"; cat /tmp/q-list2.txt; exit 1
        }

        # Standing on haven, where the newer one waits: the jump has to cross
        # back to vshop, because that is where the older one is.
        zde desk queue-jump 2>&1 | tee /tmp/q-jump.txt
        grep -q 'vshop.winit' /tmp/q-jump.txt || {
          echo "the jump did not cross back to the desk the oldest item was added on:"
          cat /tmp/q-jump.txt; exit 1
        }
        # Jumping is not finishing: it is still there afterwards.
        zde queue 2>&1 | tee /tmp/q-still.txt
        grep -q "^$id	" /tmp/q-still.txt || {
          echo "jumping to the oldest item took it off the queue:"
          cat /tmp/q-still.txt; exit 1
        }

        # A notification from something that has never heard of zde. This is
        # the whole point of zded being the notification server rather than
        # talking to one: notify-send speaks the freedesktop spec at whatever
        # holds the bus name, and what it says lands on the desk it arrived on
        # instead of on a popup nobody was looking at.
        zde desk switch vshop >/dev/null
        notify-send --urgency=critical "the build failed" "on the third try"
        arrived() { zde queue >/tmp/q-notify.txt 2>&1 && grep -q 'the build failed' /tmp/q-notify.txt; }
        if ! waitfor 15 arrived; then
          echo "the notification never reached the queue:"
          cat /tmp/q-notify.txt /tmp/zded-live.log; exit 1
        fi
        # Urgent, on the desk it arrived on, and attributed to what claimed to
        # send it - the claim being all anybody has until zinc gives each app
        # its own bus socket.
        grep -q '	!	.	vshop	notify-send	the build failed' /tmp/q-notify.txt || {
          echo "the notification arrived wrong:"; cat /tmp/q-notify.txt; exit 1
        }
        nid=$(grep 'the build failed' /tmp/q-notify.txt | cut -f1)

        # And in the history, which is the half the queue cannot answer: the
        # queue holds what is still waiting, the history holds what arrived.
        # Nothing is listening here, so Mod+n prints it rather than drawing it -
        # id, urgency, whether the desktop wrote it, when, sender, what became
        # of it, text.
        zde system notif-center 2>&1 | tee /tmp/notif-center.txt
        grep -q "^$nid	!	.	.*	notify-send	waiting	the build failed" /tmp/notif-center.txt || {
          echo "the notification arrived and the history does not have it:"
          cat /tmp/notif-center.txt; exit 1
        }
        # And what this server tells an app it can do. "actions" is the claim
        # that decides whether apps send buttons at all, so it is worth pinning
        # on the bus rather than only in a Go test: the center offers every
        # action a sender declares, and the day it stops this line has to fail.
        dbus-send --session --print-reply --dest=org.freedesktop.Notifications \
          /org/freedesktop/Notifications \
          org.freedesktop.Notifications.GetCapabilities > /tmp/caps.txt 2>&1
        grep -q '"actions"' /tmp/caps.txt || {
          echo "zded does not tell apps it offers actions, so they will not send any:"
          cat /tmp/caps.txt; exit 1
        }
        zde queue done "$nid"

        # And a sender drawn like the desktop's own. The "е" in this one is
        # Cyrillic: the reservation is on the word zde and cannot be on every
        # way of drawing that word, so this arrival keeps the name it asked for
        # and reads exactly like zde's own row in every column but one. The one
        # is column 3, which says who made the record rather than who claims to
        # have (internal/attn, Notification.Self) - and nothing off the bus can
        # reach it, because a name cannot hold a tab.
        notify-send -a "zdе" "your session has expired" "run 'zde unlock' and type your password"
        lookalike() { zde queue >/tmp/q-look.txt 2>&1 && grep -q 'session has expired' /tmp/q-look.txt; }
        if ! waitfor 15 lookalike; then
          echo "the lookalike never reached the queue:"; cat /tmp/q-look.txt; exit 1
        fi
        awk -F'\t' '$6 ~ /session has expired/ && $3=="*" { bad=1 } END { exit bad }' /tmp/q-look.txt || {
          echo "a notification off the bus is drawn as the desktop's own message:"
          cat /tmp/q-look.txt; exit 1
        }
        zde queue done "$(grep 'session has expired' /tmp/q-look.txt | cut -f1)" >/dev/null

        # And the other way out, which is the one that matters on a queue
        # somebody cannot face: one call rather than one per item. Recovery used
        # to be `zde queue done` a thousand times, and a journal written before
        # the queue had any bounds can still hold more than the ceiling.
        #
        # The count is the assertion. A clear whose effect cannot be checked
        # afterwards - the list it dropped is gone - is one that has to say what
        # it did, and "2" here is the two still waiting from further up.
        cleared=$(zde queue clear)
        [ "$cleared" = "2" ] || { echo "zde queue clear said '$cleared', want the 2 that were waiting"; exit 1; }
        zde queue 2>&1 | tee /tmp/q-empty.txt
        [ ! -s /tmp/q-empty.txt ] || { echo "the queue did not empty:"; cat /tmp/q-empty.txt; exit 1; }
        # Twice is a person checking, not an error.
        again=$(zde queue clear)
        [ "$again" = "0" ] || { echo "clearing an empty queue said '$again'"; exit 1; }

        # Mod+w with nothing listening, which is this zded: the session target
        # was stopped long ago, so there is no shell here. The key still has to
        # do something, so it prints what it would have shown - one window per
        # line, id first, tab separated, so a session whose shell has died can
        # still be steered and so what is open can be grepped at all.
        zde window jump-to 2>&1 | tee /tmp/jump-list.txt
        # One row, matched on its shape: the id, then the zde name of the
        # workspace it is on, then the app. Taken out of the list rather than
        # off the top of it, so this says nothing about the order and everything
        # about the columns.
        row=$(grep -m1 '^[0-9][0-9]*	[a-z][a-z0-9-]*\.winit\.[a-z0-9-]*	foot' /tmp/jump-list.txt) || true
        [ -n "$row" ] || {
          echo "the printed list is not id, then the zde workspace name, then the app:"
          cat /tmp/jump-list.txt; nirimsg windows; exit 1
        }
        jid=$(printf '%s\n' "$row" | cut -f1)
        jws=$(printf '%s\n' "$row" | cut -f2)
        jdesk=''${jws%%.*}
        # Standing on a desk that is not the window's, whichever row this is, so
        # that the jump below crosses a desk rather than moving within one.
        if [ "$jdesk" = "vshop" ]; then
          zde desk switch haven >/dev/null
        else
          zde desk switch vshop >/dev/null
        fi

        zde window jump-to "$jid" 2>&1 | tee /tmp/jump.txt
        grep -qx "$jws" /tmp/jump.txt || {
          echo "jumping to window $jid says it landed somewhere other than $jws:"
          cat /tmp/jump.txt; exit 1
        }
        # niri's answer, not zded's, and by id: "a window is focused" is true
        # whatever the jump did, and counting windows cannot fail at all.
        [ "$(focused_window)" = '"id":'"$jid" ] || {
          echo "jumped to window $jid and niri has $(focused_window) focused"
          nirimsg windows; exit 1
        }
        # And the desk came up with it. A jump that only focused the window
        # would leave the session standing on the desk it started from, which is
        # a desk on one screen and another desk on the rest.
        zde status 2>&1 | tee /tmp/jump-status.txt
        grep -q "^on desk    $jdesk" /tmp/jump-status.txt || {
          echo "jumped to a window on $jdesk and zded is somewhere else:"
          cat /tmp/jump-status.txt; exit 1
        }

        # The attn mode outlives the daemon. zded is restarted by every rebuild
        # that touches it, and a mode that quietly reset to work each time would
        # be a mode that lies about why nothing is arriving - so it is journal
        # state, and this is the assertion that says so.
        zde attn quiet 2>&1 | tee /tmp/attn-set.txt
        grep -qx quiet /tmp/attn-set.txt
        kill "$livepid"
        wait "$livepid" 2>/dev/null || true
        sock_gone() { [ ! -S "$XDG_RUNTIME_DIR/zde/zded.sock" ]; }
        if ! waitfor 15 sock_gone; then
          echo "zded was stopped and left its socket behind, so the next one cannot start"
          exit 1
        fi
        zded -journal /tmp/live.jsonl -desks /tmp/desks >>/tmp/zded-live.log 2>&1 &
        listening() { [ -S "$XDG_RUNTIME_DIR/zde/zded.sock" ]; }
        if ! waitfor 30 listening; then
          echo "the restarted zded never listened:"; cat /tmp/zded-live.log; exit 1
        fi
        zde status 2>&1 | tee /tmp/status-restarted.txt
        grep -qx 'attn       quiet' /tmp/status-restarted.txt || {
          echo "quiet was set, zded restarted, and the mode came back as something else:"
          cat /tmp/status-restarted.txt; exit 1
        }

        echo "live compositor check passed"
  '';
in
pkgs.testers.runNixOSTest {
  name = "zde-smoke";

  # Shorter than the workflow's job timeout, so a hang is reported by the test
  # driver with the VM's console in the log rather than killed by GitHub with
  # nothing to read.
  globalTimeout = 1500;

  nodes.machine = {
    imports = [
      zdeModule
      homeManagerModule
      ../zde-user.nix
    ];

    # zde.laptop stays off here on purpose: it would hand the VM's network to
    # NetworkManager and make anything network-shaped in this script flaky.
    # nix/test-host.nix evaluates that branch instead.
    zde.enable = true;

    # The state snapshot, on, so that both halves of it are exercised on a real
    # NixOS host: layer 0 making a directory per account, and the unit the
    # session target pulls writing into it. A VM with no GPU is also the one
    # machine in CI that can prove the graphics answer degrades honestly
    # instead of guessing, which is the whole reason the file exists.
    zde.debug.enable = true;

    # Only so that shell_interact and a manual login work when debugging this
    # test; most assertions below run as root.
    users.users.zde.password = "zde";

    # Somebody else on the machine, so that "the zde socket is private" can be
    # asserted by a user who is actually subject to it - root is not.
    users.users.intruder.isNormalUser = true;

    home-manager.users.zde = {
      # Layer 2's tools, the way a real machine gets them: zinc's own
      # home-manager module, which is the shape it says zde should install it
      # in.
      imports = [ zincModule ];
      programs.zinc.enable = true;
      # zlg is opt-in in zinc's module, on the reasoning that a desktop shipping
      # its own launcher does not want a second one. zde ships none: its
      # generated keymap binds Mod+g to `zlg` already (common/keymap), so
      # leaving it out is not declining a second launcher, it is a bound key
      # that spawns nothing.
      programs.zinc.tools = [
        "zc"
        "zcr"
        "zlt"
        "zlg"
      ];

      # The locker, pointed at something that locks nothing. The leader check
      # below runs it for real - that is the point, it proves the whole chain
      # from a keypress to an exec - and a VM with no input devices that locked
      # itself could never unlock itself, taking every assertion after it down.
      #
      # It writes a file rather than exiting 0, so the test can see that it ran
      # rather than only that nothing complained.
      zde = {
        # The other half of zde.debug above: this is what writes.
        debug.enable = true;

        apps.lock = [ "${fakeLocker}/bin/swaylock" ];

        # One ask tier and only one: the provider. The other two stay unset on
        # purpose, so that the live check can assert both halves - a tier that
        # answers, and a tier nobody configured saying which option would.
        ask.tiers.provider = [ "${fakeTier}/bin/zde-fake-tier" ];

        # A second keyboard layout, which is what Mod+space switches between.
        # With one layout that key does nothing, which is what every zde
        # session has done so far - and a machine you cannot write to somebody
        # in is not one anybody travels with. Set here so the generated
        # local.kdl is what niri validates below, rather than a shape nothing
        # has ever parsed.
        niri.xkb = {
          layout = "us,ru";
          options = "grp:caps_toggle";
        };

        # A host's own binds, through the seam that exists for them: local.kdl,
        # included after the generated ones. The live image (nix/live.nix) puts
        # a terminal on a key this way, because nothing in the keymap can open
        # one yet - so niri accepting a second binds block is load-bearing
        # rather than incidental, and the niri validate below is what keeps it
        # that way.
        #
        # This block also goes through nix/niri-local.nix on the way in, which
        # parses it at build time - and that does not make the check below
        # redundant. Two different claims: that one is that this text is KDL,
        # this one is that home-manager put it where niri looks, that the
        # includes resolve at the real paths, and that the file zded writes
        # beside it is there and writable. A synthetic tree in a build sandbox
        # cannot answer any of those; a booted machine is the only thing that
        # can.
        niri.extraConfig = ''
          binds {
              Mod+Return { spawn "foot"; }
          }
        '';
      };
    };

    # cage hosts the nested niri; mesa's software rasteriser is what both of
    # them render with.
    environment.systemPackages = [
      pkgs.cage
      pkgs.foot # a Wayland client that runs without a GPU, for adoption
      pkgs.dbus # dbus-launch, for a session bus to be the notification server on
      pkgs.libnotify # notify-send: an app that has never heard of zde
    ];

    virtualisation = {
      memorySize = 2048;
      cores = 2;
    };
  };

  testScript =
    { nodes, ... }:
    ''
      machine.wait_for_unit("multi-user.target")

      # Layer 0: the greeter is up and everything a session is launched from is
      # installed.
      machine.wait_for_unit("greetd.service")
      machine.wait_until_succeeds("pgrep -f tuigreet")
      machine.succeed("test -x /run/current-system/sw/bin/niri-session")
      machine.succeed("test -f /run/current-system/sw/share/xdg-desktop-portal/niri-portals.conf")

      # Layer 0's half of the state snapshot: a directory per account that could
      # have a session, owned by it and 0700. Asserted here rather than only
      # where the file lands, because these two fail in different ways - a rule
      # tmpfiles refused leaves no directory and one line in a boot log, and the
      # session that could not write into it is an hour later and looks like the
      # writer's fault.
      machine.succeed("test -d /var/log/zde/zde")
      assert machine.succeed("stat -c '%U %a' /var/log/zde/zde").strip() == "zde 700"
      # And one for the other account on this machine, which is what makes it a
      # directory per account rather than one directory with a name in it.
      machine.succeed("test -d /var/log/zde/intruder")
      # Nothing in either yet: nobody has had a session (this test never logs in
      # graphically), so a file here now would mean something writes one without
      # a session to describe.
      assert machine.succeed("ls -A /var/log/zde/zde").strip() == ""

      # Layer 2's tools arrived, and the contract zde leans on holds against the
      # real binary. `zcr where` is what zde asks rather than joining that path
      # itself: the layout is zinc's, and two copies of it would drift the first
      # time either side moved.
      machine.succeed("su -l zde -c 'command -v zcr'")
      machine.succeed("su -l zde -c 'command -v zc'")
      # And zlg, which is not a nicety: Mod+g in the generated keymap spawns it
      # by name (common/keymap/keymap.yaml, launcher.open). Without it that key
      # is bound to a binary the machine does not have, which looks from the
      # keyboard exactly like a key that does nothing.
      machine.succeed("su -l zde -c 'command -v zlg'")
      # zinc 0.9 made `zcr where` load the config, so it answers only for apps
      # that exist. zc init is zinc's own seeding, which beats this test writing
      # an app file and owning zinc's schema to keep it parsing.
      machine.succeed("su -l zde -c 'zc init'")
      where = machine.succeed(
          "su -l zde -c 'XDG_STATE_HOME=/tmp/st zcr where example-instanced@work'"
      )
      assert "state: /tmp/st/zinc/example-instanced/work" in where, where
      # Both halves of an address are a name, which is the rule a desk manifest
      # feeds: zde refuses a path in either field, and so does zinc.
      machine.fail("su -l zde -c 'zcr where \"example-instanced@../../etc\"'")
      machine.fail("su -l zde -c 'zcr where \"../../../etc\"'")

      # The screen lock can authenticate. This is the trap worth a test rather
      # than the locking itself: a locker with no PAM service takes the screen
      # and then refuses every password, which is not a locked screen but a lost
      # machine. niri's own module provides it, and this is what notices if that
      # ever stops being true.
      #
      # Locking for real is a by-hand item (docs/verify.md): this VM has no
      # input devices, so anything that locked it could not unlock it.
      machine.succeed("test -e /etc/pam.d/swaylock")

      # The backlight rules reached udev, which is the difference between the
      # brightness keys working and failing for everyone who is not root. They
      # arrive only through services.udev.packages: installing brightnessctl
      # the package, which layer 1 does, puts its rules somewhere nothing
      # reads. Asserted on the file udev actually loads.
      machine.succeed("test -e /etc/udev/rules.d/90-brightnessctl.rules")
      machine.succeed("grep -q 'chgrp video' /etc/udev/rules.d/90-brightnessctl.rules")

      # niri's user units reached systemd. This is what stands between a login
      # and a greeter that silently takes it straight back: niri-session starts
      # niri.service, and a build that installs those units where NixOS does not
      # glob for them (share/ rather than lib/) produces exactly that, with
      # nothing on screen and nothing in a log. It held for a whole PR here.
      machine.succeed("test -f /etc/systemd/user/niri.service")

      # The session entry, asserted on the package because the system profile
      # does not link share/wayland-sessions.
      machine.succeed(
          "test -f ${nodes.machine.programs.niri.package}/share/wayland-sessions/niri.desktop"
      )

      # Layer 1: home-manager put the generated config and the cheatsheet in
      # place, and the config is the very build the flake exposes - the claim
      # that layer 1 and the flake produce identical output (docs/delivery.md).
      # Its activation is a oneshot that does not linger, so this waits on what
      # it produced rather than on the unit ever being active.
      machine.wait_until_succeeds("test -L /home/zde/.config/niri/config.kdl")
      # LoadState first: systemctl reports Result=success for a unit that does
      # not exist, so asking about Result alone would pass a typo.
      machine.succeed(
          "systemctl show -p LoadState --value home-manager-zde.service | grep -qx loaded"
      )
      machine.succeed(
          "systemctl show -p Result --value home-manager-zde.service | grep -qx success"
      )
      machine.succeed("test -s /home/zde/.config/zde/keymap-cheatsheet.md")
      machine.succeed(
          'test "$(readlink -f /home/zde/.config/niri/config.kdl)"'
          ' = "${zdeConfig}/niri-config.kdl"'
      )
      machine.succeed(
          'test "$(readlink -f /home/zde/.config/niri/binds.kdl)"'
          ' = "${zdeConfig}/binds.kdl"'
      )

      # dynamic.kdl is the one file zded writes at runtime, so it has to exist
      # before niri reads the config (a missing include is fatal) and it has to
      # be a real writable file, not a symlink into the read-only store.
      machine.succeed("test -f /home/zde/.config/niri/dynamic.kdl")
      machine.succeed("test ! -L /home/zde/.config/niri/dynamic.kdl")
      machine.succeed("su -l zde -c 'echo \"// zded was here\" >> ~/.config/niri/dynamic.kdl'")

      # And at the mode zded keeps it at, whichever layer got there first. The
      # nix seed and internal/zded/rules.go both write this file; they disagreed
      # about its mode for a while, which meant the answer depended on whether
      # the daemon had written yet. Asserted here because it is the only place
      # both layers are running at once.
      machine.succeed(
          'test "$(stat -c %a /home/zde/.config/niri/dynamic.kdl)" = 600'
      )

      # The whole tree is one config this niri accepts: the nodes parse, the
      # includes resolve, and every action name the keymap emitted is real.
      # Run as the user, since the includes resolve relative to the file.
      machine.succeed("su -l zde -c 'niri validate -c ~/.config/niri/config.kdl'")

      # The layout reached the config niri reads. Two of them, because one is
      # what a session has without this and Mod+space needs somewhere to switch
      # to.
      machine.succeed("grep -q 'layout \"us,ru\"' /home/zde/.config/niri/local.kdl")
      machine.succeed("grep -q 'grp:caps_toggle' /home/zde/.config/niri/local.kdl")

      # A broken include has to fail loudly rather than boot into a default
      # config: this is the seam zded writes through every day.
      machine.succeed("su -l zde -c 'mv ~/.config/niri/dynamic.kdl ~/dyn.bak'")
      machine.fail("su -l zde -c 'niri validate -c ~/.config/niri/config.kdl'")
      machine.succeed("su -l zde -c 'mv ~/dyn.bak ~/.config/niri/dynamic.kdl'")
      machine.succeed("su -l zde -c 'niri validate -c ~/.config/niri/config.kdl'")

      # Every binary the binds spawn has to be on the user's PATH, or the key
      # does nothing and says nothing. zlg is zinc's launcher and is the last
      # exception left; that list shrinks, it must not grow.
      spawned = machine.succeed(
          "grep -ho 'spawn \"[^\"]*\"' /home/zde/.config/niri/*.kdl"
          " | cut -d'\"' -f2 | sort -u"
      ).split()
      # The binds moved to binds.kdl once the config was split, and a grep of
      # the wrong file would make everything below pass by finding nothing.
      assert len(spawned) >= 3, f"expected the binds to spawn several commands, found {spawned}"
      missing = [
          c for c in spawned
          if c not in ("zlg",)
          and machine.execute(f"su -l zde -c 'command -v {c}'")[0] != 0
      ]
      assert missing == [], f"binds spawn commands that are not installed: {missing}"

      # And one level down: a bind that spawns `zde app launch help` resolves to
      # zde, which the check above is satisfied by - what the name resolves to
      # lives in apps.json, and a program missing there is the same silent key
      # with one more step in front of it.
      import json
      apps = json.loads(machine.succeed("cat /home/zde/.config/zde/apps.json"))
      assert "help" in apps, f"no help app, so Mod+slash opens nothing: {apps}"
      for name, argv in apps.items():
          machine.succeed(f"su -l zde -c 'test -x {argv[0]}'")
          # And behind a terminal's -e, which hides a second program: the line
          # above is satisfied by the terminal itself, so `foot -e nvim` on a
          # machine with no nvim passed this check and opened a window that shut
          # again the moment it looked for the editor. That is how Mod+e shipped
          # naming a program layer 1 does not install.
          if "-e" in argv and argv.index("-e") + 1 < len(argv):
              inner = argv[argv.index("-e") + 1]
              machine.succeed(f"su -l zde -c 'command -v {inner}'")

      # The keymap as a key shows it. `zde help` printed usage to a stderr no
      # keypress has, which on a desktop where most keys are silent made the one
      # key that says which keys work another silent one.
      keys = machine.succeed("su -l zde -c 'zde keys'")
      assert "Mod+Tab" in keys, keys
      # Plain text, not the markdown cheatsheet: this one goes through a pager.
      assert "|" not in keys and "`" not in keys, keys

      # Screenshots are niri's own actions rather than a `zde capture` nobody
      # has written. Asserted in the generated binds because the check above
      # cannot see it: those keys spawned `zde`, which is installed, and did
      # nothing at all when pressed.
      binds = machine.succeed("cat /home/zde/.config/niri/binds.kdl")
      for shot in ("{ screenshot; }", "{ screenshot-screen; }", "{ screenshot-window; }"):
          assert shot in binds, f"no {shot} bind: {binds}"

      # The keys a focused app cannot take. niri 26.04 hands
      # zwp_keyboard_shortcuts_inhibit_manager_v1 to every client, sandboxed or
      # not - it is the one sensitive global its security-context filter misses
      # - and while such a surface has the keyboard it forwards rather than acts
      # on every bind whose allow-inhibiting is true, which is niri's default.
      # So this property is the whole of what keeps panic, lock, the mode picker
      # and the key that ends the grab working, and the `niri validate` above is
      # what makes these greps mean something: the property parsed, and it is
      # not a string this file happens to find in a comment.
      #
      # Whether an app can really swallow the rest is by hand (docs/verify.md,
      # section 10): this VM has no input devices and nothing in it grabs.
      for chord in (
          "Mod+Shift+Escape",     # panic
          "Mod+Ctrl+semicolon",   # lock
          "Mod+Ctrl+Escape",      # the grab toggle, which a grab must not eat
          "Mod+m",                # the mode picker, which is how a mode is left
      ):
          line = [l for l in binds.splitlines() if l.strip().startswith(chord + " ")]
          assert len(line) == 1, f"{chord} is not one bind in the config: {binds}"
          assert "allow-inhibiting=false" in line[0], (
              f"{chord} is a key an app can take off you: {line[0]}"
          )
      # And the mirror: an ordinary bind must not carry it, or every app that
      # legitimately grabs the keyboard - a VM, a nested compositor - has been
      # locked out of the whole keymap by a default nobody argued for.
      nav = [l for l in binds.splitlines() if l.strip().startswith("Mod+j ")]
      assert nav and "allow-inhibiting" not in nav[0], f"Mod+j is unsuppressible: {nav}"

      # The unit that starts the daemon with the session. Every check in this
      # file runs zded by hand, which is the one thing a person never does:
      # on a login it is niri that brings up graphical-session.target, and
      # this is what has to be hanging off it. Without the symlink the session
      # comes up with every desk key silent and nothing to say why.
      machine.succeed(
          "test -L /home/zde/.config/systemd/user/graphical-session.target.wants/zded.service"
      )

      # That is the precise-cause check, and on its own it is a weak one: a
      # unit can be wanted by the target and still be wrong in every way that
      # matters. Starting it the way a login does needs a compositor to import
      # an environment from, so it happens in the live check below, where there
      # is one. What this does is give the user manager the linger that check
      # needs - su gives no session (the podman check at the end of this file
      # leans on the same fact from the other side).
      uid = machine.succeed("id -u zde").strip()
      machine.succeed("loginctl enable-linger zde")
      machine.wait_until_succeeds(f"systemctl is-active user@{uid}.service")

      # zded comes up and answers its socket. There is no compositor in a
      # build sandbox, which is the point: the daemon everyone asks why the
      # session is broken has to run, and say so, when niri is not there.
      machine.succeed("su -l zde -c 'mkdir -p /tmp/rt'")
      machine.succeed(
          "su -l zde -c 'XDG_RUNTIME_DIR=/tmp/rt nohup zded >/tmp/zded.log 2>&1 &'"
      )
      machine.wait_until_succeeds("test -S /tmp/rt/zde/zded.sock")
      status = machine.succeed("su -l zde -c 'XDG_RUNTIME_DIR=/tmp/rt zde status'")
      assert "zded" in status, status
      # It reports the missing compositor rather than claiming a working one.
      assert "NIRI_SOCKET" in status, status

      # The connections key, on a machine with no NetworkManager and no wifi
      # hardware - which this VM is, and which is the case that has to degrade
      # rather than hang or fail: the key is bound, so a person will press it.
      # It answers in one legible line and exits 0, because there is nothing
      # here to fix.
      conns = machine.succeed(
          "su -l zde -c 'XDG_RUNTIME_DIR=/tmp/rt zde system connections'"
      )
      assert "no NetworkManager" in conns, conns

      # The socket is the zde boundary (docs/vision.md, principle 7). Asserted
      # as another user, because root is not subject to the mode bits and
      # would pass this test no matter what zded did.
      machine.fail("su -l intruder -c 'test -r /tmp/rt/zde/zded.sock'")
      machine.fail("su -l intruder -c 'ls /tmp/rt/zde'")

      # A real compositor: zded and zde against niri itself, not a fake.
      machine.succeed("su -l zde -c ${liveCheck}", timeout=600)

      # Rootless podman, what zcr will run apps with. su gives no logind
      # session, so this exercises podman's cgroupfs fallback rather than the
      # systemd path a real login would take.
      machine.succeed("su -l zde -c 'podman info'")
    '';
}
