-- Loaded on VeryLazy. LazyVim defaults:
-- https://github.com/LazyVim/LazyVim/blob/main/lua/lazyvim/config/autocmds.lua

-- Host-theme inheritance (see plugins/theme.lua for the rationale).
-- With termguicolors off, Neovim renders through the terminal's 16 ANSI colors,
-- which the zde host themes -- so nvim recolors with the host. LazyVim and some
-- plugins force termguicolors back on, so re-assert it whenever a scheme loads.
vim.api.nvim_create_autocmd("ColorScheme", {
  group = vim.api.nvim_create_augroup("zde_host_theme", { clear = true }),
  callback = function()
    vim.opt.termguicolors = false
  end,
})
