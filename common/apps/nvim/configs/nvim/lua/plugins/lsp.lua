-- Language support: Go, C/C++, Python, Web (TS/JS + HTML/CSS), Lua, Nix, shell,
-- and the markup formats (Markdown/JSON/YAML/TOML/Docker).
--
-- All servers, parsers and tools are baked into the image at build time
-- (see nvim.yaml -> ImageMeta.Install). Mason's registry must NOT be reached at
-- runtime -- the sandbox is network-locked -- so this file only declares what to
-- install; the build performs the install once.

return {
  -- LSP servers. LazyVim's mason-lspconfig integration installs the matching
  -- mason package for each key, so listing the server is enough.
  {
    "neovim/nvim-lspconfig",
    opts = {
      servers = {
        gopls = {},
        clangd = {},
        basedpyright = {},
        ruff = {},
        ts_ls = {},
        html = {},
        cssls = {},
        lua_ls = {},
        nixd = {},
        bashls = {},
        marksman = {},
        jsonls = {},
        yamlls = {},
        taplo = {},
        dockerls = {},
      },
    },
  },

  -- Treesitter parsers (built at image time; none fetched at runtime).
  {
    "nvim-treesitter/nvim-treesitter",
    opts = function(_, opts)
      vim.list_extend(opts.ensure_installed, {
        "bash", "c", "cpp", "go", "gomod", "gowork", "gosum",
        "lua", "luadoc", "nix", "python", "javascript", "typescript", "tsx",
        "html", "css", "json", "jsonc", "yaml", "toml", "dockerfile",
        "markdown", "markdown_inline", "diff", "git_config", "gitcommit",
      })
    end,
  },

  -- Non-LSP tools: formatters and linters mason pulls in at build time.
  {
    "williamboman/mason.nvim",
    opts = {
      ensure_installed = {
        "stylua", "shfmt", "shellcheck",
        "gofumpt", "goimports",
        "prettier", "clang-format",
        "markdownlint-cli2",
      },
    },
  },
}
