-- Editing niceties carried over from the host config, minus host-path coupling.
return {
  -- Explorer shows dotfiles and gitignored files by default.
  {
    "folke/snacks.nvim",
    opts = {
      explorer = { hidden = true, ignored = true },
    },
  },

  -- Align text on a delimiter: `ga` then the character.
  {
    "junegunn/vim-easy-align",
    keys = {
      { "ga", "<Plug>(EasyAlign)", mode = { "n", "x" }, desc = "Easy Align" },
    },
  },
}
