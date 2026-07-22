-- Host-theme inheritance ("for first": follow the host, no bundled colorscheme).
--
-- A zde app inherits the host theme. For a terminal app the faithful, minimal way
-- to do that is to render through the terminal's own 16 ANSI colors: the host
-- themes the terminal, so nvim follows automatically -- no per-app palette to keep
-- in sync. We therefore use Neovim's built-in "default" scheme with truecolor off
-- (config/autocmds.lua keeps termguicolors off across scheme loads), and take only
-- light/dark from the desktop via $ZDE_APPEARANCE.
--
-- Next step (not "first"): if the desktop exports a base16 palette, read it here to
-- get the full host palette in truecolor. Until then this stays 16-color by design.

local appearance = os.getenv("ZDE_APPEARANCE")
vim.o.background = appearance == "light" and "light" or "dark"

return {
  { "LazyVim/LazyVim", opts = { colorscheme = "default" } },
}
