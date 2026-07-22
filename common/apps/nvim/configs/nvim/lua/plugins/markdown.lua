-- Markdown rendering, pure-Lua only. The host config's mermaid/image pipeline
-- (mmdc, puppeteer, chromium, image.nvim sixel) is dropped on purpose: it hard-codes
-- host binary paths and needs a browser in the sandbox. render-markdown is self-
-- contained and needs nothing outside nvim.
return {
  {
    "MeanderingProgrammer/render-markdown.nvim",
    opts = { latex = { enabled = false } },
    keys = {
      { "<leader>um", "<cmd>RenderMarkdown toggle<cr>", desc = "Toggle Render Markdown" },
    },
  },
}
