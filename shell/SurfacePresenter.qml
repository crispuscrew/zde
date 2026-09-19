pragma ComponentBehavior: Bound

import QtQuick
import Quickshell

QtObject {
    id: presenter
    required property var surfaces
    required property var network
    property var up: null

    function screenFor(event) {
        for (const screen of Quickshell.screens) {
            if (screen.name === event.output)
                return screen;
        }
        return Quickshell.screens[0] ?? null;
    }

    // niri gives the oldest mapped overlay the grab; exactly one may own it.
    function present(surface, event) {
        if (presenter.up && presenter.up !== surface)
            presenter.up.hide();
        presenter.up = surface;
        surface.screen = presenter.screenFor(event);
    }

    function desks(event, kind, caption) {
        const here = event.on ?? "";
        presenter.present(presenter.surfaces.picker, event);
        presenter.surfaces.picker.show(kind, (event.desks ?? []).map(desk => ({
            key: desk, label: desk, note: desk === here ? "here" : ""
        })), here, event.token ?? "", caption);
    }

    function receive(message) {
        const event = message.event;
        const targets = presenter.surfaces;
        if (event.kind === "picker") {
            targets.pickMethod = "desk.switch";
            presenter.desks(event, "desks", "");
        } else if (event.kind === "picker.move-window") {
            targets.pickMethod = "desk.move-window-to";
            presenter.desks(event, "desks-move-window", "send this window to");
        } else if (event.kind === "picker.move-workspace") {
            targets.pickMethod = "desk.move-workspace-to";
            presenter.desks(event, "desks-move-workspace", "send this workspace to");
        } else if (event.kind === "windows") {
            targets.pickMethod = "window.jump-to";
            presenter.present(targets.picker, event);
            targets.picker.show("windows", (event.windows ?? []).map(window => ({
                key: String(window.id),
                label: (window.appId ?? "") + (window.title ? "  " + window.title : ""),
                note: window.workspace ?? ""
            })), "", event.token ?? "", "");
        } else if (event.kind === "notif-center") {
            presenter.present(targets.center, event);
            targets.center.show(event.notifications ?? [], event.token ?? "");
        } else if (event.kind === "connections") {
            presenter.present(presenter.network.surface, event);
            presenter.network.surface.show(event.networks ?? [], event.link ?? ({}), event.token ?? "");
        } else if (event.kind === "ask" || event.kind === "ask.panel") {
            presenter.present(targets.ask, event);
            targets.ask.show(event.kind === "ask.panel", event.token ?? "", event.question ?? "");
        } else if (event.kind === "palette") {
            presenter.present(targets.palette, event);
            targets.palette.show((event.actions ?? []).map(action => ({
                name: action.name, desc: action.desc ?? "", key: action.key ?? "",
                live: action.live === true, why: action.why ?? ""
            })), event.token ?? "");
        } else if (event.kind === "power") {
            presenter.present(targets.power, event);
            targets.power.show((event.choices ?? []).map(choice => ({
                name: choice.name, label: choice.label ?? choice.name, desc: choice.desc ?? "",
                confirm: choice.confirm === true, costs: choice.costs ?? [], why: choice.why ?? ""
            })), event.token ?? "");
        } else if (event.kind === "clip") {
            presenter.present(targets.clips, event);
            targets.clips.show((event.clips ?? []).map(clip => ({
                id: clip.id, preview: clip.preview ?? "", cut: clip.cut === true,
                kind: clip.kind ?? "text", why: clip.why ?? "", at: clip.at
            })), event.token ?? "");
        } else if (event.kind === "attn.popup") {
            const arrivals = event.notifications ?? [];
            if (arrivals.length === 0)
                return;
            // An unsolicited popup never hides the current surface or takes its grab.
            if (!targets.popup.reached)
                targets.popup.screen = presenter.screenFor(event);
            targets.popup.arrived(arrivals[0]);
        } else if (event.kind === "attn.reach") {
            if (targets.popup.visible) {
                presenter.present(targets.popup, event);
                targets.popup.reach(event.token ?? "");
            }
        } else if (event.kind === "attn.hide") {
            targets.popup.hide();
        }
    }
}
