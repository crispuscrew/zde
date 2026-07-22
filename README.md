# zde-niri
## Zinc Desktop Environment - Niri based

A keyboard-first desktop environment where every user-facing app runs under
**zinc** sandboxing (rootless Podman, fail-closed) as defined by the Zinc app
schema.

This repo builds and maintains the **niri** variant only, on
[niri](https://github.com/YaLTeR/niri) (scrollable tiling).

A **hypr** variant (on [Hyprland](https://hyprland.org)) is possible on the
same zinc base and the same `common/` material, but it is not maintained here.
If you want to maintain a hypr version, you are welcome - open an issue or
reach out.

## License

[Apache-2.0](LICENSE). Third-party attributions: [`NOTICE.md`](NOTICE.md).

## Docs

- [`docs/vision.md`](docs/vision.md) - what zde is: principles, components,
  security model, the scenario catalog, what zde asks of zinc.
- [`docs/model.md`](docs/model.md) - the spatial model (desks over niri) and
  the action map.
- [`docs/roadmap.md`](docs/roadmap.md) - phases 0.1-0.4 and the
  verify-in-prototype list.
- [`docs/glossary.md`](docs/glossary.md) - the terms; every doc uses only
  these words.

## Layout

- `common/` - variant-agnostic apps and config (e.g. the nvim editor); kept
  compositor-neutral so a future hypr variant could reuse it.
- `niri/` - pieces that only make sense for niri.

Apps (wherever they live) follow the same shape under `apps/<name>/`:

- `<name>.yaml` - the app definition (schema v2, see
  `../hyprzinc/common/domain/schema/schema.go`).
- `configs/` - files mounted into the container at the paths the app expects.
- `<name>.png` - the app icon, shipped with the app so it resolves on any host
  (the containerized program is never installed on the host). zde registers these
  under the icon name in each app's `Icon` field.
