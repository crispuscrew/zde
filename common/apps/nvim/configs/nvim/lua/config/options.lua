-- Loaded before lazy.nvim startup. LazyVim defaults:
-- https://github.com/LazyVim/LazyVim/blob/main/lua/lazyvim/config/options.lua

-- Use the actual cwd as project root, not the git root, so a submodule dir
-- cannot hijack the file explorer's root when a file is opened.
vim.g.root_spec = { "cwd" }

-- Indent with 4 spaces (LazyVim defaults to 2); expandtab is already on.
vim.opt.shiftwidth = 4
vim.opt.tabstop = 4
vim.opt.softtabstop = 4

vim.g.have_nerd_font = true
