# mcpmu website

Static product site published by `.github/workflows/pages.yml` from this directory.
No build dependencies, analytics or external fonts; Inter is self-hosted from
`assets/fonts/` (SIL OFL, licence alongside). Screenshots are copied from
the main project README; refresh them when the management interfaces change.

Preview with `python3 -m http.server 4321 --directory site` from the repo root.
Changes to `site/**` on `main` deploy automatically. GitHub repository Settings →
Pages must use GitHub Actions as its source. The custom domain is configured in
Pages settings; DNS uses a CNAME to `bigsy.github.io` (without the repo path).
