#!/usr/bin/env bash
# Boot zde-live.iso in a VM (docs/verify.md). Not part of the build: this runs
# on whatever machine you are sitting at, needs no nix, and exists because the
# flags that matter are not guessable and one of them fails in a way that looks
# like a broken image.
#
# That one is the GPU. With a display-only virtual card - `-vga std`, `-vga
# virtio` without gl - niri starts, opens its Wayland and IPC sockets, and never
# draws, leaving the console text on the screen. Its log says "failed to
# initialize renderer, falling back to primary gpu: software EGL renderers are
# skipped", because niri skips software EGL on purpose. So this script always
# asks for virtio-vga-gl, and virgl is why the session comes up.
#
#   dev/vm.sh                        # a window, one screen
#   dev/vm.sh --screens 2            # two outputs, for the band-per-screen items
#   dev/vm.sh --uefi                 # boot the way your hardware does
#   dev/vm.sh --disk zde.qcow2       # attach a disk, to try installing onto one
#   dev/vm.sh --headless             # no window: VNC, for a machine elsewhere
#   dev/vm.sh --boot-shot boot.png   # one screenshot, for "did it boot at all"
#
# Anything a VM cannot answer - latency, a real GPU, docking, the lid, the
# keyboard you actually own - is section 3 onwards of docs/verify.md, and needs
# the stick.
set -euo pipefail

iso=""
screens=1
mem=4096
cpus=4
disk=""
uefi=false
headless=false
shot=""
after=90
vnc=5
print_only=false
runtime="${TMPDIR:-/tmp}/zde-vm"

die() {
	printf 'dev/vm.sh: %s\n' "$1" >&2
	exit 1
}

usage() {
	sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'
	cat <<'EOF'

Options:
  --iso PATH        the image (default: ./result/iso/zde-live.iso, then
                    /data/zde/zde-live.iso)
  --screens N       virtual outputs (default 1). Two is enough for the
                    band-per-screen and monitor-focus items.
  --mem MiB         guest memory (default 4096). The live image runs from a
                    tmpfs overlay, so below about 2048 it starts failing in
                    ways that look like bugs in zde.
  --cpus N          guest cores (default 4)
  --uefi            boot through OVMF rather than SeaBIOS
  --disk PATH[:GiB] attach a qcow2, created at 20G if absent. For trying an
                    install; the live image itself touches no disk.
  --headless        no window: EGL offscreen plus VNC, for a machine you are
                    not sitting at. Attach a viewer to see it.
  --vnc N           VNC display number for --headless (default 5, so :5905)
  --boot-shot FILE  boot once with a plain VGA card, screenshot after --after
                    seconds, kill the VM, exit. This is the "does this image
                    boot at all" check, and it stops at the greeter: a plain
                    card cannot render the session, because niri skips
                    software EGL. Needs socat.
  --after SECS      how long to wait before --boot-shot (default 90)
  --print           print the qemu command and exit
  -h, --help        this
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--iso) iso="${2:?--iso needs a path}"; shift 2 ;;
	--screens) screens="${2:?}"; shift 2 ;;
	--mem) mem="${2:?}"; shift 2 ;;
	--cpus) cpus="${2:?}"; shift 2 ;;
	--disk) disk="${2:?}"; shift 2 ;;
	--uefi) uefi=true; shift ;;
	--headless) headless=true; shift ;;
	--boot-shot) shot="${2:?--boot-shot needs a file}"; shift 2 ;;
	--after) after="${2:?}"; shift 2 ;;
	--vnc) vnc="${2:?}"; shift 2 ;;
	--print) print_only=true; shift ;;
	-h | --help) usage; exit 0 ;;
	-*) die "unknown option $1 (try --help)" ;;
	*) iso="$1"; shift ;;
	esac
done

if [ -z "$iso" ]; then
	for candidate in ./result/iso/zde-live.iso /data/zde/zde-live.iso; do
		[ -f "$candidate" ] && iso="$candidate" && break
	done
fi
[ -n "$iso" ] || die "no image found. Build one with 'nix build .#zde-iso', or pass --iso PATH"
[ -f "$iso" ] || die "no such image: $iso"

command -v qemu-system-x86_64 >/dev/null || die "qemu-system-x86_64 is not installed"

# KVM is not optional here in practice. Without it a software-rendered
# compositor inside an emulated CPU is slow enough that the greeter looks
# broken rather than slow, which wastes the evening this script is meant to
# save.
[ -w /dev/kvm ] || die "/dev/kvm is not writable by $(id -un): without KVM this is too slow to judge anything by"

# Short, because a unix socket path is capped at 108 bytes and a long TMPDIR
# silently spends most of them.
mkdir -p "$runtime"
monitor="$runtime/monitor.sock"
rm -f "$monitor"

args=(
	-enable-kvm
	-machine q35
	-cpu host
	-m "$mem"
	-smp "$cpus"
	-drive "file=$iso,media=cdrom,readonly=on"
	-boot d
	-monitor "unix:$monitor,server,nowait"
	# A tablet rather than a mouse: absolute pointing, so the guest cursor
	# tracks yours instead of drifting away from it.
	-device virtio-tablet-pci
	-device virtio-keyboard-pci
)

if $uefi; then
	ovmf=""
	for f in /usr/share/edk2/ovmf/OVMF_CODE.fd /usr/share/OVMF/OVMF_CODE.fd \
		/usr/share/qemu/ovmf-x86_64-code.bin; do
		[ -f "$f" ] && ovmf="$f" && break
	done
	[ -n "$ovmf" ] || die "--uefi asked for, and no OVMF firmware found (install edk2-ovmf)"
	# Read-only, with no writable varstore: this image keeps nothing, so there
	# are no boot entries worth persisting, and a shared varstore is a way for
	# one run to confuse the next.
	args+=(-bios "$ovmf")
fi

if [ -n "$disk" ]; then
	path="${disk%%:*}"
	size="20G"
	case "$disk" in *:*) size="${disk##*:}" ;; esac
	if [ ! -f "$path" ]; then
		command -v qemu-img >/dev/null || die "qemu-img is not installed, so --disk cannot create $path"
		qemu-img create -f qcow2 "$path" "$size" >/dev/null
		printf 'dev/vm.sh: created %s (%s)\n' "$path" "$size" >&2
	fi
	args+=(-drive "file=$path,if=virtio,format=qcow2")
fi

# The card, and it is the whole reason this script exists. Two of the three
# modes ask for virgl, because that is what niri renders on; --boot-shot is the
# exception and says so.
if [ -n "$shot" ]; then
	# A plain card, and a readable one: `screendump` on the monitor needs a
	# surface it can copy out of main memory, and there is none behind
	# virtio-vga-gl - not with egl-headless, and not with a VNC front end
	# either (both answer "no surface"; checked against qemu 10.1). So a
	# screenshot costs the session: this mode sees the firmware, the
	# bootloader and the greeter, all of which are text, and then niri fails
	# to find a renderer and draws nothing. Which is exactly what makes it
	# useful for "did this image boot", and useless for anything after.
	args+=(-vga std -display none)
elif $headless; then
	render=""
	for node in /dev/dri/renderD*; do
		[ -e "$node" ] && render="$node" && break
	done
	[ -n "$render" ] || die "--headless needs a render node in /dev/dri for EGL"
	args+=(
		-device "virtio-vga-gl,max_outputs=$screens"
		-display "egl-headless,rendernode=$render"
		-vnc ":$vnc"
	)
else
	# gl=on is the half that matters: it is what turns virtio-vga-gl into
	# virgl rather than a display-only card. max_outputs is what gives niri a
	# second screen to put a desk on.
	args+=(
		-device "virtio-vga-gl,max_outputs=$screens"
		-display "gtk,gl=on,show-cursor=on"
	)
fi

if $print_only; then
	printf 'qemu-system-x86_64'
	printf ' %q' "${args[@]}"
	printf '\n'
	exit 0
fi

printf 'dev/vm.sh: %s, %s MiB' "$iso" "$mem"
[ -z "$shot" ] && printf ', %s screen(s)' "$screens"
$uefi && printf ', UEFI'
[ -n "$shot" ] && printf ', plain VGA for one screenshot'
$headless && [ -z "$shot" ] && printf ', headless on vnc :%s' "$vnc"
printf '\n'
[ -z "$shot" ] && printf 'dev/vm.sh: log in as zde / zde, and Mod+Return opens a terminal\n'

if [ -n "$shot" ]; then
	command -v socat >/dev/null || die "--boot-shot talks to the qemu monitor over a unix socket, so it needs socat"
	qemu-system-x86_64 "${args[@]}" -pidfile "$runtime/qemu.pid" -daemonize
	pid=$(cat "$runtime/qemu.pid")
	printf 'dev/vm.sh: booting, screenshot in %ss\n' "$after"
	sleep "$after"
	ppm="$runtime/shot.ppm"
	rm -f "$ppm"
	printf 'screendump %s\n' "$ppm" | socat - "unix-connect:$monitor" >/dev/null 2>&1 || true
	kill "$pid" 2>/dev/null || true
	[ -f "$ppm" ] || die "the monitor gave no screenshot: is the VM still alive? (pid $pid)"
	if command -v magick >/dev/null; then
		magick "$ppm" "$shot"
	elif command -v convert >/dev/null; then
		convert "$ppm" "$shot"
	else
		shot="$shot.ppm"
		cp "$ppm" "$shot"
		printf 'dev/vm.sh: no imagemagick, so it stays a ppm\n' >&2
	fi
	printf 'dev/vm.sh: %s, and the VM is stopped\n' "$shot"
else
	exec qemu-system-x86_64 "${args[@]}"
fi
