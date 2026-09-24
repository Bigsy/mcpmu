# mcpmu website

Static product site published by `.github/workflows/pages.yml` from this directory.
No build step, dependencies, analytics or external fonts; Inter is self-hosted from
`assets/fonts/` (SIL OFL, licence alongside). Screenshots are copied from
the main project README; refresh them when the management interfaces change.

- `assets/mark.svg` is the μ logo traced to a single path. The page inlines the same
  path as the `#mark` sprite symbol, and the CSS uses the file as a mask for the faint
  background watermarks. Update both if the logo changes.
- `assets/og.jpg` is the 1200×630 social card; `favicon.svg` and
  `apple-touch-icon.png` carry the same mark.
- The compression-level demo in `script.js` mirrors `formatListing` in
  `internal/server/compress.go`. Keep it in step if the listing format changes.
- Motion (gateway packets, reveals, marquee, parallax) is off under
  `prefers-reduced-motion`, and every section renders fully without JavaScript.

Preview with `python3 -m http.server 4321 --directory site` from the repo root.
Changes to `site/**` on `main` deploy automatically. GitHub repository Settings →
Pages must use GitHub Actions as its source. The custom domain is configured in
Pages settings; DNS uses a CNAME to `bigsy.github.io` (without the repo path).
